package react

import (
	"encoding/json"
	"fmt"

	llm "react-base-service/api/llm"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	memoryService "react-base-service/service/memory"

	"github.com/gin-gonic/gin"
)

const (
	memoryReadMaxItems    = 10
	memoryListDefaultSize = 20
	memoryListMaxSize     = 50
)

// memoryScopeResolved 保留 ReAct 包内兼容视图；实际作用域解析由 service/memory 负责。
type memoryScopeResolved struct {
	owners      []model.MemoryOwner
	writeOwner  model.MemoryOwner
	allowedKeys map[string]struct{}
}

func resolveMemoryScope(callerKey, userName string, allowUserScope bool) memoryScopeResolved {
	scope := memoryService.ResolveRuntimeScope(callerKey, userName, allowUserScope)
	return memoryScopeResolved{
		owners:      scope.Owners,
		writeOwner:  scope.WriteOwner,
		allowedKeys: scope.AllowedKeys,
	}
}

func memoryOwnerKey(owner model.MemoryOwner) string {
	return memoryService.OwnerScopeKey(owner.OwnerType, owner.OwnerKey)
}

func mergeMemoryItems(items []model.MemoryItem) []model.MemoryItem {
	return memoryService.MergeRuntimeItems(items)
}

func renderMemoryContext(items []model.MemoryItem, cfg conf.ReactMemoryConfig) string {
	return memoryService.RenderRuntimeContext(items, cfg)
}

// buildMemoryContextForRun 保留 React Runtime 入口；查询/合并/渲染已下沉到 service/memory。
func buildMemoryContextForRun(ctx *gin.Context, callerKey, userName string) (string, error) {
	return memoryService.BuildRuntimeContext(ctx, callerKey, userName, conf.GetReactRuntimeConfig().Memory)
}

// memoryToolDefinitions 声明三个记忆工具；仅在 memory.enabled 时注册。
func memoryToolDefinitions() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		objectTool(metaToolMemoryList,
			"列出当前作用域的长期记忆索引（默认按需层全部）。返回条目的 itemId/title/description/tags/layer，不含正文；需要正文时用 memory_read。",
			map[string]interface{}{
				"layer": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"resident", "detached", "all"},
					"description": "层级过滤，默认 detached。",
				},
				"tag":     stringSchema("按标签精确过滤，可选。"),
				"keyword": stringSchema("标题/描述关键词过滤，可选。"),
				"limit":   numberSchema("返回条数上限，默认 20，最大 50。"),
			}),
		objectTool(metaToolMemoryRead,
			"按 itemId 读取记忆全文（正文、版本、来源与最近修订原因）。一次最多 10 条。",
			map[string]interface{}{
				"itemIds": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "number"},
					"description": "memory_list 或记忆目录返回的 itemId 数组。",
				},
			}),
		memoryWriteToolDefinition(),
	}
}

func memoryWriteToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        metaToolMemoryWrite,
		Description: "写入/更新/删除长期记忆。只记稳定事实与明确偏好（用户称呼、业务口径、长期约定），不记一次性任务上下文，不记录凭证、证件号、密钥等敏感信息（含敏感形态的内容会被直接拒绝）；宁可少写不写错。用户明确要求忘记时执行 delete。每次操作必须给 reason。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次工具调用的简短描述，用于向用户说明为什么调用该内部工具或正在做什么。"),
				"action": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"create", "update", "delete"},
					"description": "操作类型。",
				},
				"itemId":  numberSchema("条目 ID，update/delete 必填。"),
				"version": numberSchema("update 时读取到的版本号（乐观锁）；不传则直接覆盖。"),
				"layer": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"resident", "detached"},
					"description": "层级。create 默认 detached；update 不传保持原层级。常驻层注入每一轮对话，写入需加倍审慎。",
				},
				"title":         stringSchema("短标题（≤32字），create/update 必填。"),
				"content":       stringSchema("记忆正文，一到三句原子事实（≤500字），create/update 必填。"),
				"retrievalHint": stringSchema("检索提示：什么场景需要想起这条记忆（≤512字），create/update 必填。"),
				"tags":          stringSchema("逗号分隔标签，可选。"),
				"reason":        stringSchema("必填：为什么写入/修改/删除。"),
			},
			"required":             []string{"description", "action", "reason"},
			"additionalProperties": false,
		},
	}
}

type memoryListInput struct {
	Layer   string `json:"layer"`
	Tag     string `json:"tag"`
	Keyword string `json:"keyword"`
	Limit   int    `json:"limit"`
}

type memoryListItemView struct {
	ItemID      uint   `json:"itemId"`
	Layer       string `json:"layer"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Tags        string `json:"tags"`
	Source      string `json:"source"`
	Version     int    `json:"version"`
	UpdatedAt   string `json:"updatedAt"`
}

// executeMemoryList 列出当前作用域记忆索引（不含正文）。
func (s *reactEngineState) executeMemoryList(input json.RawMessage) (string, bool, error) {
	var req memoryListInput
	_ = json.Unmarshal(input, &req)

	cfg := conf.GetReactRuntimeConfig().Memory
	memoryRuntime := s.services.memoryExecutor()
	scope := memoryRuntime.ResolveScope(s.req.payload.CallerKey, s.req.userName, cfg.MemoryAllowUserScope())
	items, err := memoryRuntime.List(s.ctx, scope, memoryService.RuntimeListOptions{
		Layer:   req.Layer,
		Tag:     req.Tag,
		Keyword: req.Keyword,
		Limit:   req.Limit,
	})
	if err != nil {
		return "", true, err
	}
	data, _ := json.Marshal(map[string]interface{}{"items": items, "total": len(items)})
	return string(data), false, nil
}

func memoryItemHasTag(tags, tag string) bool {
	return memoryService.RuntimeItemHasTag(tags, tag)
}

func memoryItemMatchesKeyword(item model.MemoryItem, keyword string) bool {
	return memoryService.RuntimeItemMatchesKeyword(item, keyword)
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

type memoryWriteInput struct {
	Action      string `json:"action"`
	ItemID      uint64 `json:"itemId"`
	Version     int    `json:"version"`
	Layer       string `json:"layer"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	Description string `json:"retrievalHint"`
	Tags        string `json:"tags"`
	Reason      string `json:"reason"`
}

// executeMemoryWrite 是引擎侧记忆写入口：解析入参、解析作用域后交给统一写核心
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

// memorySourceForRun 按会话类型区分写入来源：reflection 子 run 记 source=reflection，主对话记 model。
func memorySourceForRun(isReflection bool) string {
	if isReflection {
		return model.MemorySourceReflection
	}
	return model.MemorySourceModel
}
