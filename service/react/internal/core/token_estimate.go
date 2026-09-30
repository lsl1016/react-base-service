package core

import (
	"encoding/json"

	llm "react-base-service/api/llm"
	"react-base-service/service/token"
)

const (
	MsgRoleOverheadTokens  = 4
	ToolUsePartOverhead    = 6
	ToolResultPartOverhead = 4
	ToolDefinitionOverhead = 10
)

// EstimateMessagesTokens 估算消息列表的 token 数，覆盖 Content 与 Parts 的全部类型。
// 仅在 lastInputTokens 不可用时（冷启动）作为兜底使用。
func EstimateMessagesTokens(messages []llm.ChatMessage) int {
	total := 0
	for _, msg := range messages {
		total += MsgRoleOverheadTokens
		if msg.Content != "" {
			total += token.EstimateTokens(msg.Content)
		}
		for _, p := range msg.Parts {
			switch p.Type {
			case "text":
				total += token.EstimateTokens(p.Text)
			case "thinking":
				total += token.EstimateTokens(p.Thinking)
			case "tool_use":
				total += ToolUsePartOverhead
				total += token.EstimateTokens(p.Name)
				total += token.EstimateTokens(string(p.Input))
			case "tool_result":
				total += ToolResultPartOverhead
				total += token.EstimateTokens(p.Content)
			}
		}
	}
	return total
}

// EstimateToolDefinitionsTokens 估算工具 schema 的 token 数，业务工具多时这部分会占可观比例。
func EstimateToolDefinitionsTokens(tools []llm.ToolDefinition) int {
	total := 0
	for _, t := range tools {
		total += ToolDefinitionOverhead
		total += token.EstimateTokens(t.Name)
		total += token.EstimateTokens(t.Description)
		if len(t.Parameters) == 0 {
			continue
		}
		if b, err := json.Marshal(t.Parameters); err == nil {
			total += token.EstimateTokens(string(b))
		}
	}
	return total
}
