package react

import (
	"encoding/json"

	llm "react-base-service/api/llm"
	model "react-base-service/models/llm"
	"react-base-service/service/token"
	toolService "react-base-service/service/tool"
)

// 上下文构成分类键。与前端上下文容量卡片的分类一一对应，前端持有各键的展示名与配色。
const (
	ContextBreakdownSystemPrompt = "systemPrompt"
	ContextBreakdownMessages     = "messages"
	ContextBreakdownMcpTools     = "mcpTools"
	ContextBreakdownSystemTools  = "systemTools"
	ContextBreakdownSkills       = "skills"
	ContextBreakdownOthers       = "others"
)

// ContextBreakdown 是一次模型调用前上下文的 token 构成估算。
//
// 口径与 estimateMessagesTokens 一致（CJK 加权估算 + 协议开销常数），总量用于
// 按构成可视化上下文占用，不参与计费或压缩判定。分类规则：
//   - systemPrompt：system 前缀消息（含注入其中的业务工具索引、Skill 摘要）；
//   - messages：用户/助手的对话正文与思考内容；
//   - mcpTools：http(MCP 网关) 业务工具的 schema 加载与调用轮次；
//   - systemTools：内置 Meta Tool 的 schema 与调用轮次，以及 client 型业务工具；
//   - skills：get_skill 的技能文档加载轮次；
//   - others：临时提醒、消息角色开销等难以归类的部分。
type ContextBreakdown struct {
	SystemPrompt int `json:"systemPrompt"`
	Messages     int `json:"messages"`
	McpTools     int `json:"mcpTools"`
	SystemTools  int `json:"systemTools"`
	Skills       int `json:"skills"`
	Others       int `json:"others"`
}

// Total 返回各分类估算之和。
func (b ContextBreakdown) Total() int {
	return b.SystemPrompt + b.Messages + b.McpTools + b.SystemTools + b.Skills + b.Others
}

// computeContextBreakdownJSON 估算一次模型调用上下文的分类构成并序列化，
// 供 ReactRun.context_breakdown_json 持久化；失败时返回空串。
func computeContextBreakdownJSON(messages []llm.ChatMessage, reminderTailCount int, tools []llm.ToolDefinition, activeTools map[string]model.Tool) string {
	breakdown := classifyContextMessages(messages, reminderTailCount, tools, activeTools)
	data, err := json.Marshal(breakdown)
	if err != nil {
		return ""
	}
	return string(data)
}

// computeRoundContextBreakdown 估算本轮即将发送给模型的上下文构成；
// contextMessages 末尾追加的临时提醒消息单独归入 others。
func (s *reactEngineState) computeRoundContextBreakdown(tools []llm.ToolDefinition) string {
	messages := s.contextMessages()
	reminderTailCount := len(messages) - len(s.messages)
	if reminderTailCount < 0 {
		reminderTailCount = 0
	}
	return computeContextBreakdownJSON(messages, reminderTailCount, tools, s.activeTools)
}

// persistContextBreakdown 把本轮上下文构成估算写回 ReactRun，与 last_* token 同步更新。
func (s *reactEngineState) persistContextBreakdown() error {
	if s.lastContextBreakdownJSON == "" {
		return nil
	}
	return model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{
		"context_breakdown_json": s.lastContextBreakdownJSON,
	})
}

// classifyContextMessages 按消息与工具 schema 逐项归类的分类主体。
// reminderTailCount 是 contextMessages 在末尾追加的临时提醒消息条数（0 或 1）。
func classifyContextMessages(messages []llm.ChatMessage, reminderTailCount int, tools []llm.ToolDefinition, activeTools map[string]model.Tool) ContextBreakdown {
	var breakdown ContextBreakdown
	// 每轮请求的 tools 参数只含内置 Meta Tool（两阶段协议下业务 Schema 走消息）。
	breakdown.SystemTools += estimateToolDefinitionsTokens(tools)

	// toolUseCategory 记录 tool_use_id -> 归类，供后续 tool_result 部分复用；
	// 消息按序遍历，tool_use 总是先于其 tool_result 出现。
	toolUseCategory := make(map[string]string)
	systemPrefixCount := currentSystemPrefixCount(messages)
	reminderStart := len(messages) - reminderTailCount

	addPart := func(category string, tokens int) {
		switch category {
		case ContextBreakdownMcpTools:
			breakdown.McpTools += tokens
		case ContextBreakdownSystemTools:
			breakdown.SystemTools += tokens
		case ContextBreakdownSkills:
			breakdown.Skills += tokens
		default:
			breakdown.Others += tokens
		}
	}

	for index, msg := range messages {
		if index < systemPrefixCount {
			breakdown.SystemPrompt += msgRoleOverheadTokens + token.EstimateTokens(msg.Content)
			continue
		}
		if index >= reminderStart {
			breakdown.Others += msgRoleOverheadTokens + token.EstimateTokens(msg.Content)
			continue
		}

		// 纯文本消息（无 Parts）整条计入对话正文；带 Parts 的消息逐部分归类，
		// Content 与 Parts 的计费口径与 estimateMessagesTokens 保持一致。
		if len(msg.Parts) == 0 {
			breakdown.Messages += msgRoleOverheadTokens + token.EstimateTokens(msg.Content)
			continue
		}
		if msg.Content != "" {
			breakdown.Messages += token.EstimateTokens(msg.Content)
		}
		for _, part := range msg.Parts {
			switch part.Type {
			case "text":
				breakdown.Messages += token.EstimateTokens(part.Text)
			case "thinking":
				breakdown.Messages += token.EstimateTokens(part.Thinking)
			case "tool_use":
				category := classifyToolUsePart(part, activeTools)
				toolUseCategory[part.ID] = category
				addPart(category, toolUsePartOverhead+token.EstimateTokens(part.Name)+token.EstimateTokens(string(part.Input)))
			case "tool_result":
				category := toolUseCategory[part.ToolUseID]
				if category == "" {
					category = ContextBreakdownOthers
				}
				addPart(category, toolResultPartOverhead+token.EstimateTokens(part.Content))
			}
		}
		breakdown.Others += msgRoleOverheadTokens
	}
	return breakdown
}

// classifyToolUsePart 按工具名与输入信封判定一次工具调用的归类。
func classifyToolUsePart(part llm.ContentPart, activeTools map[string]model.Tool) string {
	switch part.Name {
	case metaToolGetSkill:
		// get_skill 的结果是被加载的技能全文。
		return ContextBreakdownSkills
	case metaToolGetTool:
		// get_tool 的结果是业务工具 Schema；按解析出的业务工具类型归类。
		var input struct {
			ToolID string `json:"toolId"`
			Name   string `json:"name"`
		}
		_ = json.Unmarshal(part.Input, &input)
		if tool, ok := resolveActiveBusinessTool(input.ToolID, input.Name, "", activeTools); ok {
			return businessToolBreakdownCategory(tool)
		}
		return ContextBreakdownOthers
	case metaToolExecuteTool:
		// execute_tool 的入参内嵌业务工具定位信息。
		var input executeToolInput
		_ = json.Unmarshal(part.Input, &input)
		if tool, ok := resolveActiveBusinessTool(input.ToolID, input.Name, input.CallName, activeTools); ok {
			return businessToolBreakdownCategory(tool)
		}
		return ContextBreakdownOthers
	default:
		if isInternalMetaTool(part.Name) {
			return ContextBreakdownSystemTools
		}
		// 直接调用命名工具（如 client tool 直调路径）。
		if tool, ok := resolveActiveBusinessTool("", "", part.Name, activeTools); ok {
			return businessToolBreakdownCategory(tool)
		}
		return ContextBreakdownOthers
	}
}

// businessToolBreakdownCategory 业务工具按类型归类：http 是 MCP 网关工具，
// client（宿主端工具）与其余类型归入系统工具侧。
func businessToolBreakdownCategory(tool model.Tool) string {
	switch toolService.NormalizeToolType(tool.ToolType) {
	case toolService.ToolTypeHTTP, toolService.ToolTypeMCP:
		return ContextBreakdownMcpTools
	default:
		return ContextBreakdownSystemTools
	}
}

// resolveActiveBusinessTool 按 callName / toolId / name 三种定位方式解析已加载业务工具；
// activeTools 以 callName 为键，toolId/name 走小规模线性匹配。
func resolveActiveBusinessTool(toolID, name, callName string, activeTools map[string]model.Tool) (model.Tool, bool) {
	if callName != "" {
		if tool, ok := activeTools[callName]; ok {
			return tool, true
		}
	}
	if toolID == "" && name == "" {
		return model.Tool{}, false
	}
	for _, candidate := range activeTools {
		if (toolID != "" && candidate.ToolID == toolID) || (name != "" && candidate.Name == name) {
			return candidate, true
		}
	}
	return model.Tool{}, false
}
