package react

// 统一工具元数据注册模型（参考 ZCode 工具注册块，方案 docs/参考ZCode的运行时增强方案.md WP1）：
//
//   - 每个 Meta Tool 一次性声明 readOnly / concurrentSafe / sideEffect / riskLevel /
//     timeoutMs / maxOutputBytes；权限确认、并发调度、输出裁剪等由声明驱动，工具实现不写 if-else；
//   - 业务工具（tblLlmTool）经 config 的同名字段（readOnly/riskLevel/maxOutputBytes/timeout_ms）
//     解析出同结构（见 businessToolMeta），两类工具在执行层获得同一套语义；
//   - 只读标记是只读执行域（enforceReadOnlyTools，见 runtime_state.go）硬拦截的判定依据：
//     该执行域内非 readOnly 业务工具与 client 工具直接拒绝执行。

import (
	"fmt"
	"strings"

	llm "react-base-service/api/llm"
	agentService "react-base-service/service/agent"
	model "react-base-service/models/llm"
	toolService "react-base-service/service/tool"
)

// ToolMeta 工具执行语义的统一声明。
type ToolMeta struct {
	// ReadOnly 只读工具：无外部副作用，可进入只读执行域白名单。
	ReadOnly bool
	// ConcurrentSafe 同轮多调用是否可并行执行（只读/隔离型工具为 true）。
	ConcurrentSafe bool
	// SideEffect 副作用范围：none | session | network | system。
	SideEffect string
	// RiskLevel 风险等级：low | medium | high（前端徽标 + 确认门展示口径）。
	RiskLevel string
	// TimeoutMs 单次执行超时；0 = 跟随全局默认（ReactToolConfig.DefaultTimeoutMs）。
	TimeoutMs int
	// MaxOutputBytes 输出字节上限；0 = 跟随全局 InlineLimitBytes。
	MaxOutputBytes int
}

const (
	toolSideEffectNone    = "none"
	toolSideEffectSession = "session"
	toolSideEffectNetwork = "network"
	toolSideEffectSystem  = "system"

	toolRiskLevelLow    = "low"
	toolRiskLevelMedium = "medium"
	toolRiskLevelHigh   = "high"
)

// metaToolRegistry 是内置 Meta Tool 的元数据注册表；isInternalMetaTool 清单内的每个工具都必须登记。
var metaToolRegistry = map[string]ToolMeta{
	// 只读检索类：可并发、软着陆窗口放行。
	metaToolGetTool:            {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolGetSkill:           {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolReadToolResult:     {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolInspectData:        {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolInspectAttachment:  {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolReadAttachment:     {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolGetAsyncTask:       {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolMemoryList:         {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolMemoryRead:         {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolGraphMemorySearch:  {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNetwork, RiskLevel: toolRiskLevelLow},
	metaToolListTools:          {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolListSkills:         {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolWebFetch:           {ReadOnly: true, ConcurrentSafe: true, SideEffect: toolSideEffectNetwork, RiskLevel: toolRiskLevelMedium, TimeoutMs: 60000, MaxOutputBytes: 20 * 1024},
	// 会话状态类：只影响本 run 状态，不改外部世界。
	metaToolTodoWrite:          {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectSession, RiskLevel: toolRiskLevelLow},
	metaToolDisplayFiles:       {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectSession, RiskLevel: toolRiskLevelLow},
	metaToolCreatePlan:         {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectSession, RiskLevel: toolRiskLevelLow},
	metaToolResolveAsyncTask:   {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectSession, RiskLevel: toolRiskLevelLow},
	metaToolMemoryWrite:        {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectSession, RiskLevel: toolRiskLevelMedium},
	metaToolGraphMemoryWrite:   {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectSession, RiskLevel: toolRiskLevelMedium},
	metaToolDelegateAgent:      {ReadOnly: false, ConcurrentSafe: true, SideEffect: toolSideEffectSession, RiskLevel: toolRiskLevelLow},
	metaToolSendMessage:        {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectSession, RiskLevel: toolRiskLevelLow},
	metaToolLoadRuntimeCode:    {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectSession, RiskLevel: toolRiskLevelLow},
	// 交互/执行类：阻塞等待或改变外部世界，串行执行。
	metaToolAskQuestion:        {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectNone, RiskLevel: toolRiskLevelLow},
	metaToolPythonExec:         {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectSystem, RiskLevel: toolRiskLevelMedium},
	metaToolExecuteTool:        {ReadOnly: false, ConcurrentSafe: false, SideEffect: toolSideEffectSystem, RiskLevel: toolRiskLevelMedium},
}

// toolMetaForName 返回内置 Meta Tool 的元数据；未登记工具返回零值（保守视为非只读、不可并发）。
func toolMetaForName(name string) ToolMeta {
	return metaToolRegistry[name]
}

// isConcurrentSafeTool 判断一次工具调用是否可并行执行：内置工具查注册表，
// 业务工具按 config.readOnly（只读即无副作用，可并发）。
func (s *reactEngineState) isConcurrentSafeTool(call llm.ToolCall) bool {
	if isInternalMetaTool(call.Name) {
		return toolMetaForName(call.Name).ConcurrentSafe
	}
	if tool, ok := s.activeTools[call.Name]; ok {
		return businessToolMeta(tool).ConcurrentSafe
	}
	return false
}

// businessToolMeta 从业务工具 config 解析执行语义；解析失败按零值（保守：非只读、串行、全局预算）。
func businessToolMeta(tool model.Tool) ToolMeta {
	meta := ToolMeta{SideEffect: toolSideEffectSystem, RiskLevel: toolRiskLevelMedium}
	cfg, err := toolService.ParseToolConfig(tool.Config)
	if err != nil {
		return meta
	}
	if cfg.ReadOnly {
		meta.ReadOnly = true
		meta.ConcurrentSafe = true
		meta.SideEffect = toolSideEffectNone
		meta.RiskLevel = toolRiskLevelLow
	}
	if level := normalizeToolRiskLevel(cfg.RiskLevel); level != "" {
		meta.RiskLevel = level
	}
	meta.TimeoutMs = cfg.TimeoutMs
	meta.MaxOutputBytes = cfg.MaxOutputBytes
	return meta
}

func normalizeToolRiskLevel(level string) string {
	switch strings.TrimSpace(level) {
	case toolRiskLevelLow:
		return toolRiskLevelLow
	case toolRiskLevelMedium:
		return toolRiskLevelMedium
	case toolRiskLevelHigh:
		return toolRiskLevelHigh
	default:
		return ""
	}
}

// readOnlyViolationResult 在只读执行域（enforceReadOnlyTools）内判定业务工具是否可执行：
// 非 readOnly 声明的工具返回拒绝结果（模型可据此改用只读路径），执行域未开启或工具只读时返回 nil。
// 这是只读约束的第三层（唯一硬保证），前两层是工具白名单收敛与系统提示词禁止清单。
func (s *reactEngineState) readOnlyViolationResult(call llm.ToolCall, tool model.Tool, executedBy string) *llm.ToolResultContent {
	if s.req == nil || !s.req.enforceReadOnlyTools {
		return nil
	}
	if businessToolMeta(tool).ReadOnly {
		return nil
	}
	content := fmt.Sprintf("当前子 Agent 运行在只读执行域，工具 %s 未声明 readOnly，已被拒绝执行。"+
		"请改用只读方式继续调查（查询/检索类工具），并在结论中说明写操作需要授权后另行执行。", tool.Name)
	return &llm.ToolResultContent{ToolUseID: call.ID, Content: content, IsError: true}
}

// agentToolRefAllows 判定业务工具是否允许被子代理加载/激活（get_tool 与自愈激活路径的硬检查）。
// 语义与 service/agent 的 FilterToolIndexSnapshot 一致（同一对约定 token）：
//   - refs 为 nil：不限制（外层 run / 白名单为空=继承全部的存量语义）；
//   - 含 @none：全部拒绝（显式不继承任何业务工具）；
//   - 含 @readonly：声明 readOnly 的工具放行；
//   - 其余 refs 按 name/toolId 精确匹配。
func agentToolRefAllows(refs []string, tool model.Tool) bool {
	if len(refs) == 0 {
		return true
	}
	refSet := make(map[string]bool, len(refs))
	for _, ref := range refs {
		refSet[strings.TrimSpace(ref)] = true
	}
	if refSet[agentService.AgentToolRefNone] {
		return false
	}
	if refSet[agentService.AgentToolRefReadOnly] && businessToolMeta(tool).ReadOnly {
		return true
	}
	return refSet[tool.Name] || refSet[tool.ToolID]
}
