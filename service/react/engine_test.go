package react

import (
	"context"
	"testing"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/params"
)

// 暂时下线 create_plan 后不再要求压缩提示保留计划专属信息，恢复工具时取消注释此测试。
// func TestBuildCompactSummaryPromptPreservesCreatedPlans(t *testing.T) {
// 	prompt := buildCompactSummaryPrompt(`[{"role":"assistant","parts":[{"type":"tool_use","name":"create_plan"}]}]`)
// 	for _, expected := range []string{
// 		"成功创建的 create_plan",
// 		"planId、title、overview",
// 		"各步骤的 id、outline 和关键 message",
// 		"不得只概括为“已生成计划”",
// 		"优先完整保留较新的计划",
// 		"摘要空间不足时可以精简较早计划",
// 	} {
// 		if !strings.Contains(prompt, expected) {
// 			t.Fatalf("compact summary prompt should contain %q, got: %s", expected, prompt)
// 		}
// 	}
// }

func TestCollectLLMStreamIdleTimeoutCancelsAndErrors(t *testing.T) {
	state := &reactEngineState{runCtx: context.Background()}
	stream := make(chan llm.StreamChunk) // 永不产出，模拟模型卡死

	cancelled := false
	cancel := func() { cancelled = true }

	_, err := state.collectLLMStream(stream, 0, cancel, 30*time.Millisecond)
	if err == nil {
		t.Fatalf("expected idle timeout error, got nil")
	}
	if !cancelled {
		t.Fatalf("expected cancelLLM to be called on idle timeout")
	}
}

func TestCollectLLMStreamDoesNotTimeoutWhileChunksArrive(t *testing.T) {
	state := &reactEngineState{runCtx: context.Background()}
	stream := make(chan llm.StreamChunk)

	go func() {
		// 每 10ms 推一个 chunk，远小于 50ms idle 阈值，最后正常结束
		for i := 0; i < 5; i++ {
			stream <- llm.StreamChunk{}
			time.Sleep(10 * time.Millisecond)
		}
		stream <- llm.StreamChunk{Done: true, StopReason: "end_turn"}
		close(stream)
	}()

	res, err := state.collectLLMStream(stream, 0, func() {}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error while chunks keep arriving: %v", err)
	}
	if res.StopReason != "end_turn" {
		t.Fatalf("expected stopReason end_turn, got %q", res.StopReason)
	}
}

func TestCollectLLMStreamEmitsCumulativeCacheTokensInContentEnd(t *testing.T) {
	var events []params.ReactEvent
	state := &reactEngineState{
		runCtx: context.Background(),
		reactEngineUsageState: reactEngineUsageState{
			inputTokens:     400,
			outputTokens:    50,
			cacheReadTokens: 1000,
		},
		emitter: &runEventEmitter{
			runID:     "run_test",
			sessionID: "session_test",
			write: func(event params.ReactEvent) error {
				events = append(events, event)
				return nil
			},
		},
	}
	stream := make(chan llm.StreamChunk, 3)
	stream <- llm.StreamChunk{ReasoningContent: "先分析问题"}
	stream <- llm.StreamChunk{Content: "最终答案"}
	stream <- llm.StreamChunk{Done: true, StopReason: "end_turn", InputTokens: 80, OutputTokens: 20, CacheReadTokens: 200}
	close(stream)

	_, err := state.collectLLMStream(stream, 3, func() {}, time.Second)
	if err != nil {
		t.Fatalf("unexpected error collecting stream: %v", err)
	}

	var contentEnd *params.ReactContentEndPayload
	for _, event := range events {
		if payload, ok := event.Payload.(params.ReactContentEndPayload); ok {
			copied := payload
			contentEnd = &copied
		}
	}
	if contentEnd == nil {
		t.Fatalf("expected content_end event, got %#v", events)
	}
	if contentEnd.InputTokens != 480 {
		t.Fatalf("expected content_end cumulative inputTokens=480, got %d", contentEnd.InputTokens)
	}
	if contentEnd.OutputTokens != 70 {
		t.Fatalf("expected content_end cumulative outputTokens=70, got %d", contentEnd.OutputTokens)
	}
	if contentEnd.CacheReadTokens != 1200 {
		t.Fatalf("expected content_end cumulative cacheReadTokens=1200, got %d", contentEnd.CacheReadTokens)
	}
}

func TestCollectLLMStreamEmitsCumulativeCacheTokensInThoughtEndWhenNoContent(t *testing.T) {
	var events []params.ReactEvent
	state := &reactEngineState{
		runCtx: context.Background(),
		reactEngineUsageState: reactEngineUsageState{
			inputTokens:     400,
			outputTokens:    50,
			cacheReadTokens: 1000,
		},
		emitter: &runEventEmitter{
			runID:     "run_test",
			sessionID: "session_test",
			write: func(event params.ReactEvent) error {
				events = append(events, event)
				return nil
			},
		},
	}
	stream := make(chan llm.StreamChunk, 2)
	stream <- llm.StreamChunk{ReasoningContent: "先分析问题"}
	stream <- llm.StreamChunk{Done: true, StopReason: "end_turn", InputTokens: 80, OutputTokens: 20, CacheReadTokens: 200}
	close(stream)

	_, err := state.collectLLMStream(stream, 3, func() {}, time.Second)
	if err != nil {
		t.Fatalf("unexpected error collecting stream: %v", err)
	}

	var thoughtEnd *params.ReactThoughtEndPayload
	for _, event := range events {
		if payload, ok := event.Payload.(params.ReactThoughtEndPayload); ok {
			copied := payload
			thoughtEnd = &copied
		}
	}
	if thoughtEnd == nil {
		t.Fatalf("expected thought_end event, got %#v", events)
	}
	if thoughtEnd.InputTokens != 480 {
		t.Fatalf("expected thought_end cumulative inputTokens=480, got %d", thoughtEnd.InputTokens)
	}
	if thoughtEnd.OutputTokens != 70 {
		t.Fatalf("expected thought_end cumulative outputTokens=70, got %d", thoughtEnd.OutputTokens)
	}
	if thoughtEnd.CacheReadTokens != 1200 {
		t.Fatalf("expected thought_end cumulative cacheReadTokens=1200, got %d", thoughtEnd.CacheReadTokens)
	}
}
