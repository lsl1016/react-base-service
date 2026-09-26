package llm

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ToolDefinition 平台无关的工具定义（适配 Claude / GPT）
// 各 LLM 客户端在发送请求时内部完成格式转换
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolCall LLM 发起的工具调用（Claude/GPT 统一抽象）
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResultContent 工具执行结果
type ToolResultContent struct {
	ToolUseID string          `json:"tool_use_id"`
	Content   string          `json:"content"`
	IsError   bool            `json:"is_error,omitempty"`
	Meta      json.RawMessage `json:"-"` // UI 回放状态，旁路持久化，不进入模型消息。
}

// ChatMessage 支持工具内容的统一消息类型
// 当 Parts 非空时，Content 被忽略
type ChatMessage struct {
	Role    string        `json:"role"`
	Content string        `json:"content,omitempty"`
	Parts   []ContentPart `json:"parts,omitempty"`
}

// ContentPart 消息内容块，支持 text / thinking / tool_use / tool_result 四种类型
type ContentPart struct {
	Type string `json:"type"` // "text", "thinking", "tool_use", "tool_result"

	// type=text
	Text string `json:"text,omitempty"`

	// type=thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	// type=tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type=tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// canonicalToolInput 将工具入参 JSON 压缩为稳定字节，避免空白差异打断前缀缓存。
func canonicalToolInput(input json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`)
	}

	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return append(json.RawMessage(nil), input...)
	}
	return append(json.RawMessage(nil), buf.Bytes()...)
}

// LLMMessagesToChatMessages 将 LLMMessage 转换为 ChatMessage
func LLMMessagesToChatMessages(messages []LLMMessage) []ChatMessage {
	result := make([]ChatMessage, 0, len(messages))
	for _, msg := range messages {
		result = append(result, ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	return result
}

// BuildToolRoundMessages 根据一轮工具调用构建 assistant + user 消息对
// assistantText: 本轮 LLM 在调用工具前输出的文本（可为空）
func BuildToolRoundMessages(assistantText string, toolCalls []ToolCall, results []ToolResultContent) (assistantMsg, userMsg ChatMessage) {
	return BuildToolRoundMessagesWithReasoning(assistantText, "", "", toolCalls, results)
}

// BuildToolRoundMessagesWithReasoning 根据一轮工具调用构建 assistant + user 消息对，并在 Claude 场景保留 thinking 签名。
func BuildToolRoundMessagesWithReasoning(assistantText, reasoningText, reasoningSignature string, toolCalls []ToolCall, results []ToolResultContent) (assistantMsg, userMsg ChatMessage) {
	var assistantParts []ContentPart

	// thinking 块必须携带思考文本才能落库/续轮：signature-only 块经 omitempty 序列化后缺
	// thinking 字段，claude 协议重放会被严格 provider 以 missing field `thinking` 422 拒收。
	if strings.TrimSpace(reasoningText) != "" {
		assistantParts = append(assistantParts, ContentPart{
			Type:      "thinking",
			Thinking:  reasoningText,
			Signature: reasoningSignature,
		})
	}

	if assistantText != "" {
		assistantParts = append(assistantParts, ContentPart{
			Type: "text",
			Text: assistantText,
		})
	}

	for _, tc := range toolCalls {
		assistantParts = append(assistantParts, ContentPart{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Name,
			Input: canonicalToolInput(tc.Input),
		})
	}

	var userParts []ContentPart
	for _, r := range results {
		userParts = append(userParts, ContentPart{
			Type:      "tool_result",
			ToolUseID: r.ToolUseID,
			Content:   r.Content,
			IsError:   r.IsError,
		})
	}

	return ChatMessage{Role: "assistant", Parts: assistantParts},
		ChatMessage{Role: "user", Parts: userParts}
}
