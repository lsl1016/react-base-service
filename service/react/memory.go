package react

import (
	"encoding/json"
	"fmt"

	"react-base-service/conf"
	model "react-base-service/models/llm"
	memoryService "react-base-service/service/memory"
)

// 长期记忆 adapter：作用域解析、上下文渲染与工具声明已下沉 internal/mem；
// 本文件保留依赖引擎状态的三个执行方法（memory_list / memory_read / memory_write）。
// Memory 与普通 Business Tool 不同：它属于 Runtime 内置能力，不走 get_tool/execute_tool 两阶段协议；
// 是否暴露由 ExecutionProfile.AllowMemory 控制，读写安全边界由 service/memory 再次校验。

const (
	memoryReadMaxItems    = 10
	memoryListDefaultSize = 20
	memoryListMaxSize     = 50
)

type memoryListInput struct {
	Layer      string `json:"layer"`
	MemoryType string `json:"memoryType"`
	Tag        string `json:"tag"`
	Keyword    string `json:"keyword"`
	Limit      int    `json:"limit"`
}

type memoryListItemView struct {
	ItemID      uint   `json:"itemId"`
	Layer       string `json:"layer"`
	MemoryType  string `json:"memoryType"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Tags        string `json:"tags"`
	Source      string `json:"source"`
	Version     int    `json:"version"`
	UpdatedAt   string `json:"updatedAt"`
}

type memoryReadInput struct {
	ItemIDs []uint64 `json:"itemIds"`
}

type memoryReadItemView struct {
	ItemID     uint   `json:"itemId"`
	Layer      string `json:"layer"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	Tags       string `json:"tags"`
	Source     string `json:"source"`
	Version    int    `json:"version"`
	LastReason string `json:"lastReason"`
	UpdatedAt  string `json:"updatedAt"`
}

type memoryWriteInput struct {
	Action      string `json:"action"`
	ItemID      uint64 `json:"itemId"`
	Version     int    `json:"version"`
	Layer       string `json:"layer"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	Description string `json:"retrievalHint"`
	MemoryType  string `json:"memoryType"`
	Tags        string `json:"tags"`
	Reason      string `json:"reason"`
}

// executeMemoryList 列出当前作用域记忆索引（不含正文）。
func (s *reactEngineState) executeMemoryList(input json.RawMessage) (string, bool, error) {
	var req memoryListInput
	_ = json.Unmarshal(input, &req)

	cfg := conf.GetReactRuntimeConfig().Memory
	memoryRuntime := s.services.memoryExecutor()
	scope := memoryRuntime.ResolveScope(s.req.payload.CallerKey, s.req.userName, cfg.MemoryAllowUserScope())
	items, err := memoryRuntime.List(s.ctx, scope, memoryService.RuntimeListOptions{
		Layer:      req.Layer,
		MemoryType: req.MemoryType,
		Tag:        req.Tag,
		Keyword:    req.Keyword,
		Limit:      req.Limit,
	})
	if err != nil {
		return "", true, err
	}
	data, _ := json.Marshal(map[string]interface{}{"items": items, "total": len(items)})
	return string(data), false, nil
}

// executeMemoryRead 按 ID 读取记忆全文；条目归属不在当前作用域内时整单拒绝，防跨用户读取。
func (s *reactEngineState) executeMemoryRead(input json.RawMessage) (string, bool, error) {
	var req memoryReadInput
	_ = json.Unmarshal(input, &req)

	cfg := conf.GetReactRuntimeConfig().Memory
	memoryRuntime := s.services.memoryExecutor()
	scope := memoryRuntime.ResolveScope(s.req.payload.CallerKey, s.req.userName, cfg.MemoryAllowUserScope())
	items, err := memoryRuntime.Read(s.ctx, scope, req.ItemIDs)
	if err != nil {
		return "", true, err
	}
	data, _ := json.Marshal(map[string]interface{}{"items": items})
	return string(data), false, nil
}

// executeMemoryWrite 是 ReAct 层的长期记忆写适配器。
// 它不直接操作 Memory 表，而是解析模型入参和当前运行身份后交给统一 Memory Runtime；这样聊天写入、
// Reflection 整理和管理面可以共享同一套敏感信息拦截、乐观锁、审计 revision 和 owner scope 校验。
//
// （service/memory.ApplyMutation，与管理面共用校验/幂等/修订流水/敏感拦截/指标）。
// reflection run 附加单次写入限额与 locked 条目只读约束。
func (s *reactEngineState) executeMemoryWrite(input json.RawMessage) (string, bool, error) {
	var req memoryWriteInput
	if err := json.Unmarshal(input, &req); err != nil {
		return "", true, fmt.Errorf("memory_write input must be a valid JSON object")
	}

	cfg := conf.GetReactRuntimeConfig().Memory
	isReflection := s.req.payload.Type == model.ReactSessionTypeReflection
	if isReflection && s.memoryWrites >= cfg.Reflection.MaxWritesPerRun {
		return "", true, fmt.Errorf("本次整理的写操作已达上限（%d 次）：停止写入，直接进入总结阶段", cfg.Reflection.MaxWritesPerRun)
	}

	memoryRuntime := s.services.memoryExecutor()
	scope := memoryRuntime.ResolveScope(s.req.payload.CallerKey, s.req.userName, cfg.MemoryAllowUserScope())
	result, err := memoryRuntime.ApplyMutation(s.ctx, memoryService.MutationInput{
		Action:           req.Action,
		ItemID:           uint(req.ItemID),
		Version:          req.Version,
		Layer:            req.Layer,
		Title:            req.Title,
		Content:          req.Content,
		Description:      req.Description,
		MemoryType:       req.MemoryType,
		Tags:             req.Tags,
		Reason:           req.Reason,
		Owner:            scope.WriteOwner,
		Source:           memorySourceForRun(isReflection),
		CreatedBy:        s.runID,
		AllowedOwnerKeys: scope.AllowedKeys,
		RespectLocked:    isReflection,
	})
	if err != nil {
		return "", true, err
	}
	if isReflection {
		s.memoryWrites++
	}
	data, _ := json.Marshal(result)
	return string(data), false, nil
}
