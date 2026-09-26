package llm

import (
	"encoding/json"
	"testing"
)

// 回归：signature-only（无思考文本）不再构造 thinking 块——此前落库后经 claude 协议
// 重放，omitempty 丢掉 thinking 字段，会被严格 provider 以 missing field `thinking` 422 拒收。
func TestBuildToolRoundMessagesWithReasoningSkipsSignatureOnlyThinking(t *testing.T) {
	assistant, _ := BuildToolRoundMessagesWithReasoning("", "", "sig-without-text", nil, nil)
	if len(assistant.Parts) != 0 {
		t.Fatalf("signature-only thinking should not produce parts, got %#v", assistant.Parts)
	}

	// 纯空白思考文本同样不建块（与 gpt 出站 TrimSpace 判空口径一致）。
	assistant, _ = BuildToolRoundMessagesWithReasoning("", "   \n", "sig-blank", nil, nil)
	if len(assistant.Parts) != 0 {
		t.Fatalf("blank thinking text should not produce parts, got %#v", assistant.Parts)
	}
}

func TestBuildToolRoundMessagesWithReasoningKeepsTextThinking(t *testing.T) {
	assistant, _ := BuildToolRoundMessagesWithReasoning("answer", "thought", "sig", nil, nil)
	if len(assistant.Parts) != 2 {
		t.Fatalf("expected thinking+text parts, got %#v", assistant.Parts)
	}
	if assistant.Parts[0].Type != "thinking" || assistant.Parts[0].Thinking != "thought" || assistant.Parts[0].Signature != "sig" {
		t.Fatalf("thinking block fields lost: %#v", assistant.Parts[0])
	}
	if assistant.Parts[1].Type != "text" || assistant.Parts[1].Text != "answer" {
		t.Fatalf("text block altered: %#v", assistant.Parts[1])
	}
}

func TestBuildToolRoundMessagesWithReasoningToolRoundIntact(t *testing.T) {
	calls := []ToolCall{{ID: "t1", Name: "run_sql", Input: json.RawMessage(`{"x":1}`)}}
	results := []ToolResultContent{{ToolUseID: "t1", Content: "ok"}}

	assistant, user := BuildToolRoundMessagesWithReasoning("", "", "sig", calls, results)
	if len(assistant.Parts) != 1 || assistant.Parts[0].Type != "tool_use" {
		t.Fatalf("signature-only thinking must not displace tool_use: %#v", assistant.Parts)
	}
	if len(user.Parts) != 1 || user.Parts[0].Type != "tool_result" || user.Parts[0].ToolUseID != "t1" {
		t.Fatalf("tool_result round altered: %#v", user.Parts)
	}
}
