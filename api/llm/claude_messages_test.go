package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// 回归：历史落库的 signature-only thinking 块（thinking 文本为空）在 claude 协议出站时
// 必须被跳过——omitempty 会丢掉 thinking 字段，严格反序列化的 provider 会以
// missing field `thinking` 422 拒收整次请求。
func TestToClaudeAnyMessagesSkipsEmptyThinkingParts(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "sys prompt"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Parts: []ContentPart{
			{Type: "thinking", Thinking: "", Signature: "sig-empty"},
			{Type: "text", Text: "answer"},
		}},
		{Role: "user", Parts: []ContentPart{
			{Type: "tool_result", ToolUseID: "t1", Content: "ok"},
		}},
		{Role: "assistant", Parts: []ContentPart{
			{Type: "thinking", Thinking: "   \n\t", Signature: "sig-blank"},
			{Type: "thinking", Thinking: "real thought", Signature: "sig-real"},
			{Type: "tool_use", ID: "t2", Name: "run_sql", Input: json.RawMessage(`{"x":1}`)},
		}},
	}

	system, apiMessages := toClaudeAnyMessages(messages)
	if system != "sys prompt" {
		t.Fatalf("system prompt = %q", system)
	}
	if len(apiMessages) != 4 {
		t.Fatalf("expected 4 api messages, got %d", len(apiMessages))
	}

	// 纯文本消息保持 string content
	if content, ok := apiMessages[0].Content.(string); !ok || content != "hello" {
		t.Fatalf("plain content passthrough broken: %#v", apiMessages[0].Content)
	}

	first := apiMessages[1].Content.([]claudeContentPart)
	if len(first) != 1 || first[0].Type != "text" || first[0].Text != "answer" {
		t.Fatalf("empty thinking part not skipped: %#v", first)
	}

	second := apiMessages[3].Content.([]claudeContentPart)
	if len(second) != 2 {
		t.Fatalf("expected thinking(text)+tool_use kept, got %d parts: %#v", len(second), second)
	}
	if second[0].Type != "thinking" || second[0].Thinking != "real thought" || second[0].Signature != "sig-real" {
		t.Fatalf("text-bearing thinking block altered: %#v", second[0])
	}
	if second[1].Type != "tool_use" || second[1].ID != "t2" || second[1].Name != "run_sql" {
		t.Fatalf("tool_use block altered: %#v", second[1])
	}
}

// 回归：消息在过滤空 thinking 块后不再有内容块时整体丢弃，不产出 content 为空数组的非法消息。
func TestToClaudeAnyMessagesDropsMessageWithOnlyEmptyThinking(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "q"},
		{Role: "assistant", Parts: []ContentPart{
			{Type: "thinking", Thinking: "", Signature: "sig-only"},
		}},
		{Role: "assistant", Content: "final answer"},
	}

	_, apiMessages := toClaudeAnyMessages(messages)
	if len(apiMessages) != 2 {
		t.Fatalf("expected poisoned message dropped, got %d messages: %#v", len(apiMessages), apiMessages)
	}
	for i, msg := range apiMessages {
		if parts, ok := msg.Content.([]claudeContentPart); ok && len(parts) == 0 {
			t.Fatalf("messages[%d] has empty content parts array", i)
		}
	}
}

// 最贴近线上 422 的断言：出站 JSON 里每个 type=thinking 块都必须携带非空 thinking 字段。
func TestToClaudeAnyMessagesOutboundJSONThinkingFieldAlwaysPresent(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Parts: []ContentPart{
			{Type: "thinking", Thinking: "", Signature: "sig-poison"},
			{Type: "thinking", Thinking: "kept", Signature: "sig-kept"},
			{Type: "tool_use", ID: "t1", Name: "run_sql", Input: json.RawMessage(`{"x":1}`)},
		}},
		{Role: "user", Parts: []ContentPart{
			{Type: "tool_result", ToolUseID: "t1", Content: "ok"},
		}},
	}

	_, apiMessages := toClaudeAnyMessages(messages)
	body, err := json.Marshal(apiMessages)
	if err != nil {
		t.Fatalf("marshal api messages: %v", err)
	}

	var decoded []struct {
		Role    string           `json:"role"`
		Content json.RawMessage  `json:"content"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal outbound body: %v", err)
	}

	thinkingBlocks := 0
	for mi, msg := range decoded {
		var parts []map[string]interface{}
		if err := json.Unmarshal(msg.Content, &parts); err != nil {
			continue // string content
		}
		for pi, part := range parts {
			if part["type"] != "thinking" {
				continue
			}
			thinkingBlocks++
			text, _ := part["thinking"].(string)
			if strings.TrimSpace(text) == "" {
				t.Fatalf("messages[%d].content[%d] thinking block missing thinking field: %s", mi, pi, msg.Content)
			}
		}
	}
	if thinkingBlocks != 1 {
		t.Fatalf("expected exactly 1 thinking block outbound, got %d", thinkingBlocks)
	}
}
