package react

// 微压缩与压缩器治理（Phase 3）单元测试。

import (
	"encoding/json"
	"strings"
	"testing"

	llm "react-base-service/api/llm"
	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
)

func toolResultMessage(toolUseID, content string, isError bool) llm.ChatMessage {
	return llm.ChatMessage{Role: model.ReactMessageRoleUser, Parts: []llm.ContentPart{
		{Type: "tool_result", ToolUseID: toolUseID, Content: content, IsError: isError},
	}}
}

func microcompactEnvelope(resultRef string) string {
	data, _ := json.Marshal(map[string]any{"toolUseId": "x", "content": "result body", "resultRef": resultRef})
	return string(data)
}

func TestMicrocompactReplacesOldToolResultsKeepsRecent(t *testing.T) {
	enabled := true
	original := conf.CustomConf.LLM.React.ContextCompact
	defer func() { conf.CustomConf.LLM.React.ContextCompact = original }()
	conf.CustomConf.LLM.React.ContextCompact.MicrocompactEnabled = &enabled
	conf.CustomConf.LLM.React.ContextCompact.MicrocompactKeepRecent = 2

	state := &reactEngineState{runID: "run_test"}
	state.messages = []llm.ChatMessage{
		{Role: model.ReactMessageRoleSystem, Content: "system"},
		{Role: model.ReactMessageRoleUser, Content: "user question"},
	}
	// 5 组工具结果，保留最近 2 组 → 前 3 组应被清理
	for i := 0; i < 5; i++ {
		state.messages = append(state.messages,
			llm.ChatMessage{Role: model.ReactMessageRoleAssistant, Parts: []llm.ContentPart{{Type: "tool_use", ID: string(rune('a' + i)), Name: "t"}}},
			toolResultMessage(string(rune('a'+i)), microcompactEnvelope("result_ref_"+string(rune('a'+i))), false),
		)
	}

	if err := state.microcompactNow(); err != nil {
		t.Fatalf("microcompactNow failed: %v", err)
	}
	cleared := 0
	for _, msg := range state.messages {
		for _, part := range msg.Parts {
			if part.Type == "tool_result" && strings.HasPrefix(part.Content, microcompactPlaceholderPrefix) {
				cleared++
				if !strings.Contains(part.Content, "read_tool_result") {
					t.Fatalf("placeholder must keep resultRef guidance, got %q", part.Content)
				}
			}
		}
	}
	if cleared != 3 {
		t.Fatalf("expected 3 old tool results cleared, got %d", cleared)
	}
	// 最近 2 组保持原文
	for i := 3; i < 5; i++ {
		part := state.messages[2+i*2].Parts[0]
		if strings.HasPrefix(part.Content, microcompactPlaceholderPrefix) {
			t.Fatalf("recent tool result %d must stay intact", i)
		}
	}

	// 幂等：再次执行不再替换
	if err := state.microcompactNow(); err != nil {
		t.Fatalf("second microcompactNow failed: %v", err)
	}
	count := 0
	for _, msg := range state.messages {
		for _, part := range msg.Parts {
			if part.Type == "tool_result" && strings.HasPrefix(part.Content, microcompactPlaceholderPrefix) {
				count++
			}
		}
	}
	if count != 3 {
		t.Fatalf("microcompact must be idempotent, got %d placeholders", count)
	}
}

func TestMicrocompactSkipsErrorResultsAndDisabled(t *testing.T) {
	disabled := false
	original := conf.CustomConf.LLM.React.ContextCompact
	defer func() { conf.CustomConf.LLM.React.ContextCompact = original }()
	conf.CustomConf.LLM.React.ContextCompact.MicrocompactEnabled = &disabled
	conf.CustomConf.LLM.React.ContextCompact.MicrocompactKeepRecent = 0

	state := &reactEngineState{runID: "run_test"}
	state.messages = []llm.ChatMessage{
		toolResultMessage("t1", microcompactEnvelope("r1"), false),
		toolResultMessage("t2", microcompactEnvelope("r2"), true),
		toolResultMessage("t3", microcompactEnvelope("r3"), false),
		toolResultMessage("t4", microcompactEnvelope("r4"), false),
		toolResultMessage("t5", microcompactEnvelope("r5"), false),
	}
	if err := state.microcompactNow(); err != nil {
		t.Fatalf("microcompactNow failed: %v", err)
	}
	if strings.HasPrefix(state.messages[0].Parts[0].Content, microcompactPlaceholderPrefix) {
		t.Fatalf("microcompact disabled must not touch messages")
	}

	enabled := true
	conf.CustomConf.LLM.React.ContextCompact.MicrocompactEnabled = &enabled
	conf.CustomConf.LLM.React.ContextCompact.MicrocompactKeepRecent = 1
	if err := state.microcompactNow(); err != nil {
		t.Fatalf("microcompactNow failed: %v", err)
	}
	if strings.HasPrefix(state.messages[1].Parts[0].Content, microcompactPlaceholderPrefix) {
		t.Fatalf("error tool results must not be cleared")
	}
}

func TestSplitMessagesByRuneBudgetKeepsToolPairTogether(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: model.ReactMessageRoleUser, Content: strings.Repeat("长", 100)},
		{Role: model.ReactMessageRoleAssistant, Parts: []llm.ContentPart{{Type: "tool_use", ID: "u1", Name: "t"}}},
		toolResultMessage("u1", "ok", false),
		{Role: model.ReactMessageRoleUser, Content: "next"},
	}
	// 预算恰好把 assistant(tool_use) 切进下一块的开头：边界应对齐回 user 消息。
	chunks := splitMessagesByRuneBudget(messages, 120)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if hasPartOfType(chunk[0], "tool_result") {
			t.Fatalf("chunk must not start with orphan tool_result")
		}
		if hasPartOfType(chunk[len(chunk)-1], "tool_use") && len(chunk) == 1 {
			t.Fatalf("chunk must not end with dangling tool_use")
		}
	}
}

func TestRapidRefillExceeded(t *testing.T) {
	state := &reactEngineState{}
	state.compactStepHistory = []int{2, 3}
	if !rapidRefillExceeded(state, 4) {
		t.Fatalf("2 compacts within last 3 steps should trigger rapid-refill guard")
	}
	state.compactStepHistory = []int{1}
	if rapidRefillExceeded(state, 4) {
		t.Fatalf("single recent compact should not trigger guard")
	}
	state.compactStepHistory = []int{10, 11}
	if rapidRefillExceeded(state, 40) {
		t.Fatalf("old compacts outside window should not trigger guard")
	}
}

func TestReactCompactThresholdFallsBackToTokenTrigger(t *testing.T) {
	// 注入测试目录（测试环境不加载 custom.yaml 的 models 段）。
	originalModels := conf.CustomConf.LLM.Models
	originalCompact := conf.CustomConf.LLM.React.ContextCompact
	defer func() {
		conf.CustomConf.LLM.Models = originalModels
		conf.CustomConf.LLM.React.ContextCompact = originalCompact
	}()
	conf.CustomConf.LLM.Models = map[string]conf.ModelCatalog{
		"test-model": {MaxContextTokens: 100000, MaxOutputTokens: 8000},
	}
	cfg := conf.GetReactRuntimeConfig().ContextCompact

	// 阈值 = max_context_tokens - max(output_reserve, max_output_tokens) - buffer
	//      = 100000 - 32768 - 13000 = 54232
	threshold := reactCompactThreshold("test-model")
	reserve := cfg.OutputReserveTokens
	if reserve < 8000 {
		reserve = 8000
	}
	expected := 100000 - reserve - cfg.BufferTokens
	if threshold != expected {
		t.Fatalf("expected derived threshold %d, got %d", expected, threshold)
	}
	if got := reactMaxContextTokens("test-model"); got != expected {
		t.Fatalf("reactMaxContextTokens should match derived threshold, got %d", got)
	}
	// 未配置目录的 modelKey 回退 0（调用方再回退 TokenTrigger）
	if got := reactCompactThreshold("not-exists"); got != 0 {
		t.Fatalf("unknown model should return 0, got %d", got)
	}
	if got := reactMaxContextTokens("not-exists"); got != cfg.TokenTrigger {
		t.Fatalf("unknown model should fall back to TokenTrigger, got %d", got)
	}
}

// 确保 compact 事件 payload 引用不被裁剪（编译期引用检查）。
var _ = params.ReactCompactStartPayload{}
