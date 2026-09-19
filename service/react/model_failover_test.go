package react

import (
	"context"
	"testing"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/conf"
)

func boolPointer(value bool) *bool { return &value }

func TestConfiguredReactModelRoutingUsesSelectedModelFirst(t *testing.T) {
	original := conf.CustomConf.LLM.React.Models
	defer func() { conf.CustomConf.LLM.React.Models = original }()

	conf.CustomConf.LLM.React.Models = conf.ReactModelsConfig{
		Available: []conf.ReactModelConfig{
			{ModelKey: "DeepSeek", ModelVersion: "deepseek-v4-pro"},
			{ModelKey: "通义千问", ModelVersion: "qwen3.8-max"},
		},
		MutualFailover: boolPointer(true),
	}

	selected, candidates := configuredReactModelRouting(&runtimeRequest{runtimeRequestModel: runtimeRequestModel{
		resolvedModelKey:     "通义千问",
		resolvedModelVersion: "qwen3.8-max",
	}})
	if selected.ModelKey != "通义千问" || selected.ModelVersion != "qwen3.8-max" {
		t.Fatalf("unexpected selected model: %+v", selected)
	}
	if len(candidates) != 2 || candidates[0].ModelKey != "通义千问" || candidates[1].ModelKey != "DeepSeek" {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
}

func TestConfiguredReactModelRoutingDoesNotFailoverUnknownModel(t *testing.T) {
	original := conf.CustomConf.LLM.React.Models
	defer func() { conf.CustomConf.LLM.React.Models = original }()

	conf.CustomConf.LLM.React.Models = conf.ReactModelsConfig{
		Available: []conf.ReactModelConfig{
			{ModelKey: "DeepSeek", ModelVersion: "deepseek-v4-pro"},
			{ModelKey: "通义千问", ModelVersion: "qwen3.8-max"},
		},
		MutualFailover: boolPointer(true),
	}

	_, candidates := configuredReactModelRouting(&runtimeRequest{runtimeRequestModel: runtimeRequestModel{
		resolvedModelKey:     "OpenAI",
		resolvedModelVersion: "gpt-5.2",
	}})
	if len(candidates) != 1 || candidates[0].ModelKey != "OpenAI" {
		t.Fatalf("unknown selected model must not enter configured failover group: %+v", candidates)
	}
}

func TestMessagesForReactModelRemovesClaudeSignatureForGPTCompatibleModel(t *testing.T) {
	messages := []llm.ChatMessage{{
		Role: "assistant",
		Parts: []llm.ContentPart{
			{Type: "thinking", Thinking: "分析", Signature: "claude-signature"},
			{Type: "text", Text: "正文"},
		},
	}}

	converted := messagesForReactModel(messages, reactModelTarget{ModelKey: "通义千问", ModelVersion: "qwen3.8-max"})
	if converted[0].Parts[0].Signature != "" {
		t.Fatalf("GPT compatible target must not receive Claude signature: %+v", converted)
	}
	if messages[0].Parts[0].Signature != "claude-signature" {
		t.Fatalf("source messages must remain unchanged: %+v", messages)
	}
}

func TestCollectLLMStreamTreatsEOFWithoutDoneAsRetryable(t *testing.T) {
	stream := make(chan llm.StreamChunk)
	close(stream)
	state := &reactEngineState{runCtx: context.Background()}

	_, err := state.collectLLMStreamWithEmitter(stream, 0, func() {}, time.Second, false)
	retryable, ok := retryableModelErrorInfo(err)
	if !ok || retryable.reason != "upstream_eof" {
		t.Fatalf("expected retryable upstream_eof, got %T %v", err, err)
	}
}
