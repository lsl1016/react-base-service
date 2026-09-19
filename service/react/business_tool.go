package react

import (
	"encoding/json"
	"fmt"
	"strings"

	llm "react-base-service/api/llm"
	"react-base-service/components/route"
	model "react-base-service/models/llm"
	toolService "react-base-service/service/tool"
)

// executeToolInput 是稳定 Meta Tool execute_tool 的输入信封。
//
// ReAct 不把所有业务 Tool 的完整 Schema 一次性暴露给模型，而采用两阶段协议：
//   1. get_tool(name/toolId)：按需加载目标 Tool 的 parameters/outputSchema；
//   2. execute_tool(...)：仅执行已经加载过且定义仍有效的 Tool。
// 这样在 MCP/HTTP Tool 数量较多时，可以显著降低 System Prompt 和 Provider Tool Schema 的上下文占用。
type executeToolInput struct {
	Description string          `json:"description"`
	ToolID      string          `json:"toolId"`
	Name        string          `json:"name"`
	CallName    string          `json:"callName"`
	Input       json.RawMessage `json:"input"`
	Arguments   json.RawMessage `json:"arguments"`
}

type reactToolIndexItem struct {
	ToolID      string `json:"toolId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// buildToolIndexSnapshotJSON 构建当前 run 可用 Business Tool 的轻量索引快照，在 run 初始化阶段注入 system 前缀，替代 list_tools 工具。
func buildToolIndexSnapshotJSON(tools []model.Tool) string {
	items := make([]reactToolIndexItem, 0, len(tools))
	for _, tool := range tools {
		items = append(items, reactToolIndexItem{
			ToolID:      tool.ToolID,
			Name:        tool.Name,
			Description: tool.Description,
		})
	}
	data, _ := json.Marshal(items)
	return string(data)
}

// renderToolIndexSummary 把工具索引快照渲染成可读摘要，注入 system 前缀；完整 parameters 仍通过 get_tool 按需加载。
func renderToolIndexSummary(snapshotJSON string) string {
	snapshotJSON = strings.TrimSpace(snapshotJSON)
	if snapshotJSON == "" || snapshotJSON == "null" || snapshotJSON == "[]" {
		return ""
	}
	var items []reactToolIndexItem
	if err := json.Unmarshal([]byte(snapshotJSON), &items); err != nil || len(items) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## 当前 run 可用 Business Tool 摘要索引\n")
	sb.WriteString("以下摘要仅用于判断需要哪个工具；确认要使用某个工具后，必须先调用 get_tool 加载其 parameters，再通过 execute_tool 执行，不要直接调用 callName。\n\n")
	for _, item := range items {
		sb.WriteString(fmt.Sprintf("- toolId: %s\n  name: %s\n  description: %s\n", item.ToolID, item.Name, item.Description))
	}
	return strings.TrimSpace(sb.String())
}

// getTool 加载一个当前 Caller/Route/User 可见的 Business Tool。
//
// 成功后会把 Tool 放入 activeTools，并把 parameters/outputSchema 作为普通 Tool Result 返回给模型。
// activeTools 是“本 Run 中模型已经显式了解过 Schema”的集合；execute_tool 只允许执行这个集合中的 Tool，
// 从而避免模型绕过 Schema 获取阶段直接猜参数调用。
func (s *reactEngineState) getTool(input json.RawMessage) (string, bool, error) {
	var req struct {
		ToolID string `json:"toolId"`
		Name   string `json:"name"`
	}
	_ = json.Unmarshal(input, &req)
	tools, err := toolService.FindVisibleToolsByCallerAndRoutes(s.ctx, s.req.payload.CallerKey, route.BuildRoutePrefixes(s.req.payload.RouteValues), s.req.userName)
	if err != nil {
		return "", true, err
	}
	for _, tool := range tools {
		if (req.ToolID != "" && tool.ToolID == req.ToolID) || (req.Name != "" && tool.Name == req.Name) {
			return s.activateBusinessTool(tool, businessToolDefinition(tool))
		}
	}
	return "", true, fmt.Errorf("tool not found")
}

// activateBusinessTool 将业务 Tool 标记为当前 Run 已加载，并立即持久化 active_tool_ids / definitions。
// 持久化的目的不是鉴权，而是支持下一 Run 根据历史上下文安全恢复已经加载过的 Tool。
func (s *reactEngineState) activateBusinessTool(tool model.Tool, definition llm.ToolDefinition) (string, bool, error) {
	callName := businessToolCallName(tool)
	s.activeTools[callName] = tool
	if err := s.persistActiveTools(); err != nil {
		return "", true, err
	}
	var outputSchema map[string]interface{}
	if cfg, err := toolService.ParseToolConfig(tool.Config); err == nil {
		outputSchema = cfg.OutputSchema
	}
	data, _ := json.Marshal(map[string]interface{}{
		"toolId":       tool.ToolID,
		"name":         tool.Name,
		"callName":     callName,
		"description":  effectiveToolDescription(tool),
		"toolType":     tool.ToolType,
		"parameters":   definition.Parameters,
		"outputSchema": outputSchema,
		"usage":        "调用该业务工具时，请调用固定工具 execute_tool，并在 execute_tool 顶层传入 description 与 toolId，在 input 中传入符合 parameters 的业务参数；工具执行结果按 outputSchema 理解；不要直接调用 callName。",
		"executeToolExample": map[string]interface{}{
			"description": "用一句话说明本次工具调用目的。",
			"toolId":      tool.ToolID,
			"input":       map[string]interface{}{},
		},
	})
	return string(data), false, nil
}

// executeLoadedBusinessTool 执行已经通过 get_tool 激活的 Business Tool。
//
// 执行前会做三层保护：
//   - 解析并校验 execute_tool 信封；
//   - 用 InputSchema 校验真实业务参数；
//   - 再次查询最新可见 Tool，并比较 definition fingerprint。
// 如果管理员在 Run 期间修改了 Tool Schema，旧上下文里的定义立即失效，模型必须重新 get_tool。
func (s *reactEngineState) executeLoadedBusinessTool(call llm.ToolCall, step int) (llm.ToolResultContent, error) {
	var req executeToolInput
	if err := json.Unmarshal(call.Input, &req); err != nil {
		return llm.ToolResultContent{ToolUseID: call.ID, Content: "execute_tool input must be a valid JSON object", IsError: true}, nil
	}

	tool, ok := s.findLoadedBusinessTool(req.ToolID, req.Name, req.CallName)
	if ok && !businessToolIdentifiersMatch(tool, req.ToolID, req.Name, req.CallName) {
		return llm.ToolResultContent{ToolUseID: call.ID, Content: "toolId/name/callName must identify the same loaded business tool", IsError: true}, nil
	}
	if !ok {
		// 未命中时现查最新工具，定义与上次加载一致则自动激活，避免让模型多跑一轮 get_tool（自愈）。
		reloaded, reloadedOK, failReason := s.reloadBusinessToolIfUnchanged(req.ToolID, req.Name, req.CallName)
		if !reloadedOK {
			return llm.ToolResultContent{ToolUseID: call.ID, Content: failReason, IsError: true}, nil
		}
		tool = reloaded
	}

	toolInput := req.Input
	if len(toolInput) == 0 {
		toolInput = req.Arguments
	}
	if len(toolInput) == 0 {
		toolInput = flattenedExecuteToolInput(call.Input)
	}
	if len(toolInput) == 0 {
		toolInput = json.RawMessage(`{}`)
	}
	normalizedInput, ok := normalizeExecuteToolInput(toolInput)
	if !ok {
		return llm.ToolResultContent{ToolUseID: call.ID, Content: "execute_tool input/arguments must be valid JSON", IsError: true}, nil
	}
	toolInput = normalizedInput
	var inputValue any
	if err := json.Unmarshal(toolInput, &inputValue); err != nil {
		return llm.ToolResultContent{ToolUseID: call.ID, Content: "execute_tool input must be valid JSON", IsError: true}, nil
	}
	cfg, err := toolService.ParseToolConfig(tool.Config)
	if err != nil {
		return llm.ToolResultContent{ToolUseID: call.ID, Content: err.Error(), IsError: true}, nil
	}
	if len(cfg.InputSchema) > 0 {
		if err := ValidateJSONSchemaValue(JSONSchema(cfg.InputSchema), inputValue); err != nil {
			return llm.ToolResultContent{ToolUseID: call.ID, Content: "execute_tool input schema invalid: " + err.Error(), IsError: true}, nil
		}
	}
	latestTool, err := s.revalidateBusinessToolForExecution(tool)
	if err != nil {
		return llm.ToolResultContent{ToolUseID: call.ID, Content: err.Error(), IsError: true}, nil
	}
	tool = latestTool

	businessCall := llm.ToolCall{
		ID:    call.ID,
		Name:  businessToolCallName(tool),
		Input: toolInput,
	}
	if strings.EqualFold(tool.ToolType, "client") {
		return s.executeClientTool(businessCall, tool, step, strings.TrimSpace(req.Description))
	}
	return s.executeServerTool(businessCall, tool, step, strings.TrimSpace(req.Description))
}

func normalizeExecuteToolInput(input json.RawMessage) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(string(input))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`), true
	}
	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal(input, &text); err != nil {
			return nil, false
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return json.RawMessage(`{}`), true
		}
		data := []byte(text)
		if !json.Valid(data) {
			return nil, false
		}
		return json.RawMessage(data), true
	}
	if !json.Valid(input) {
		return nil, false
	}
	return input, true
}

func flattenedExecuteToolInput(input json.RawMessage) json.RawMessage {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil
	}
	delete(raw, "description")
	delete(raw, "toolId")
	delete(raw, "name")
	delete(raw, "callName")
	delete(raw, "input")
	delete(raw, "arguments")
	if len(raw) == 0 {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	return data
}

func businessToolIdentifiersMatch(tool model.Tool, toolID, name, callName string) bool {
	if value := strings.TrimSpace(toolID); value != "" && value != tool.ToolID {
		return false
	}
	if value := strings.TrimSpace(name); value != "" && value != tool.Name {
		return false
	}
	if value := strings.TrimSpace(callName); value != "" && value != businessToolCallName(tool) {
		return false
	}
	return true
}

func (s *reactEngineState) revalidateBusinessToolForExecution(tool model.Tool) (model.Tool, error) {
	visible, err := toolService.FindVisibleToolsByCallerAndRoutes(s.ctx, s.req.payload.CallerKey, route.BuildRoutePrefixes(s.req.payload.RouteValues), s.req.userName)
	if err != nil {
		return model.Tool{}, err
	}
	for _, latest := range visible {
		if latest.ToolID == tool.ToolID && latest.Status == 1 {
			if toolDefinitionFingerprint(businessToolDefinition(latest)) != toolDefinitionFingerprint(businessToolDefinition(tool)) {
				return model.Tool{}, fmt.Errorf("tool definition changed after get_tool; call get_tool again before execution")
			}
			return latest, nil
		}
	}
	return model.Tool{}, fmt.Errorf("tool is disabled or no longer visible")
}

func (s *reactEngineState) findLoadedBusinessTool(toolID, name, callName string) (model.Tool, bool) {
	toolID = strings.TrimSpace(toolID)
	name = strings.TrimSpace(name)
	callName = strings.TrimSpace(callName)
	if callName != "" {
		if tool, ok := s.activeTools[callName]; ok {
			return tool, true
		}
	}
	for loadedCallName, tool := range s.activeTools {
		if toolID != "" && tool.ToolID == toolID {
			return tool, true
		}
		if name != "" && tool.Name == name {
			return tool, true
		}
		if callName != "" && loadedCallName == callName {
			return tool, true
		}
	}
	return model.Tool{}, false
}

// businessToolDefinition 将业务工具配置转换为模型可识别的 tool definition。
func businessToolDefinition(tool model.Tool) llm.ToolDefinition {
	parameters := map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	if cfg, err := toolService.ParseToolConfig(tool.Config); err == nil && len(cfg.InputSchema) > 0 {
		parameters = cfg.InputSchema
	}
	return llm.ToolDefinition{Name: businessToolCallName(tool), Description: businessToolDescription(tool), Parameters: parameters}
}

func businessToolCallName(tool model.Tool) string {
	if isValidToolFunctionName(tool.ToolID) {
		return strings.TrimSpace(tool.ToolID)
	}
	if sanitized := sanitizeToolFunctionName(tool.ToolID); sanitized != "" {
		return sanitized
	}
	if sanitized := sanitizeToolFunctionName(tool.Name); sanitized != "" {
		return sanitized
	}
	return "tool"
}

// effectiveToolDescription 优先使用 config.description，为空时回退到 tool.Description。
func effectiveToolDescription(tool model.Tool) string {
	if cfg, err := toolService.ParseToolConfig(tool.Config); err == nil {
		if desc := strings.TrimSpace(cfg.Description); desc != "" {
			return desc
		}
	}
	return tool.Description
}

func businessToolDescription(tool model.Tool) string {
	name := strings.TrimSpace(tool.Name)
	description := strings.TrimSpace(effectiveToolDescription(tool))
	if name == "" {
		return description
	}
	if description == "" {
		return "工具展示名：" + name
	}
	return description + "\n\n工具展示名：" + name
}

func isValidToolFunctionName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func sanitizeToolFunctionName(name string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// toolDefinitionFingerprint 计算模型实际可见 Tool Definition 的规范化指纹。
// 该指纹只用于判断“历史加载的 Schema 是否仍等价”，不是安全签名，也不承担鉴权职责。
// marshal→unmarshal→marshal 用于消除数值类型与 map 序列化差异，
// 保证"持久化快照反序列化后"与"从最新配置现算"的同一 definition 指纹一致。
func toolDefinitionFingerprint(def llm.ToolDefinition) string {
	data, err := json.Marshal(def)
	if err != nil {
		return ""
	}
	var normalized interface{}
	if err := json.Unmarshal(data, &normalized); err != nil {
		return ""
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return string(out)
}

// restoreActiveToolsFromPreviousRun 恢复上一个外层 Run 已加载的 Business Tool。
//
// 为什么需要恢复：Session 历史里可能已经存在 get_tool 的返回；如果新 Run 完全丢掉 activeTools，
// 模型可能根据历史直接 execute_tool，却被 Runtime 判定“未加载”。因此这里按 ID 重新查最新配置，并
// 仅当当前 definition 与上次持久化快照一致时才恢复；定义已变化的工具不恢复，强制模型重新 get_tool 拿新 schema。
func (s *reactEngineState) restoreActiveToolsFromPreviousRun() error {
	var prevIDs []string
	if raw := strings.TrimSpace(s.req.prevActiveToolIDsJSON); raw != "" {
		_ = json.Unmarshal([]byte(raw), &prevIDs)
	}
	var prevDefs []llm.ToolDefinition
	if raw := strings.TrimSpace(s.req.prevActiveToolDefsJSON); raw != "" {
		_ = json.Unmarshal([]byte(raw), &prevDefs)
	}
	for _, def := range prevDefs {
		if fp := toolDefinitionFingerprint(def); fp != "" {
			s.prevToolDefFingerprint[def.Name] = fp
		}
	}
	if len(prevIDs) == 0 {
		return nil
	}

	tools, err := toolService.FindVisibleToolsByCallerAndRoutes(s.ctx, s.req.payload.CallerKey, route.BuildRoutePrefixes(s.req.payload.RouteValues), s.req.userName)
	if err != nil {
		return err
	}
	toolByID := make(map[string]model.Tool, len(tools))
	for _, tool := range tools {
		toolByID[tool.ToolID] = tool
	}

	restored := 0
	for _, toolID := range prevIDs {
		tool, ok := toolByID[toolID]
		if !ok {
			// 工具已下线或当前 caller/route 不再可见，不恢复。
			continue
		}
		callName := businessToolCallName(tool)
		prevFP, hadPrev := s.prevToolDefFingerprint[callName]
		if !hadPrev || prevFP != toolDefinitionFingerprint(businessToolDefinition(tool)) {
			// 定义已变化：历史上下文里的 parameters 已过期，保持未激活，让模型重新 get_tool。
			continue
		}
		s.activeTools[callName] = tool
		restored++
	}
	if restored == 0 {
		return nil
	}
	return s.persistActiveTools()
}

// reloadBusinessToolIfUnchanged 是 execute_tool 未命中时的自愈：现查最新工具，仅当定义与上一个 run 加载时一致才自动激活。
// 返回值第二项表示是否成功激活；未激活时第三项给出面向模型的失败原因。
func (s *reactEngineState) reloadBusinessToolIfUnchanged(toolID, name, callName string) (model.Tool, bool, string) {
	tools, err := toolService.FindVisibleToolsByCallerAndRoutes(s.ctx, s.req.payload.CallerKey, route.BuildRoutePrefixes(s.req.payload.RouteValues), s.req.userName)
	if err != nil {
		return model.Tool{}, false, "business tool is not loaded, call get_tool first"
	}
	toolID = strings.TrimSpace(toolID)
	name = strings.TrimSpace(name)
	callName = strings.TrimSpace(callName)
	for _, tool := range tools {
		toolCallName := businessToolCallName(tool)
		if (toolID != "" && tool.ToolID == toolID) || (name != "" && tool.Name == name) || (callName != "" && toolCallName == callName) {
			prevFP, hadPrev := s.prevToolDefFingerprint[toolCallName]
			if !hadPrev {
				// 本会话此前没加载过该工具，模型上下文里没有它的 schema，仍要求先 get_tool。
				return model.Tool{}, false, "business tool is not loaded, call get_tool first"
			}
			if prevFP != toolDefinitionFingerprint(businessToolDefinition(tool)) {
				return model.Tool{}, false, fmt.Sprintf("tool %s definition has changed since it was last loaded, call get_tool to refresh its parameters before execute_tool", tool.Name)
			}
			s.activeTools[toolCallName] = tool
			if err := s.persistActiveTools(); err != nil {
				return model.Tool{}, false, "business tool is not loaded, call get_tool first"
			}
			return tool, true, ""
		}
	}
	return model.Tool{}, false, "business tool is not loaded, call get_tool first"
}

// persistActiveTools 持久化当前 run 已激活工具，便于后续排查模型实际可调用范围。
func (s *reactEngineState) persistActiveTools() error {
	ids := make([]string, 0, len(s.activeTools))
	defs := make([]llm.ToolDefinition, 0, len(s.activeTools))
	for _, name := range sortedActiveToolNames(s.activeTools) {
		tool := s.activeTools[name]
		ids = append(ids, tool.ToolID)
		defs = append(defs, businessToolDefinition(tool))
	}
	idsJSON, _ := json.Marshal(ids)
	defsJSON, _ := json.Marshal(defs)
	return model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{"active_tool_ids": string(idsJSON), "active_tool_defs_json": string(defsJSON)})
}
