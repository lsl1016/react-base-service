package react

// 终止边界治理（Phase 1/2）单元测试：软着陆、重复调用守卫、输出续写、外层预算。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	llm "react-base-service/api/llm"
	"react-base-service/conf"
)

func TestToolCallSignatureStableAcrossKeyOrder(t *testing.T) {
	first := toolCallSignature(llm.ToolCall{ID: "t1", Name: "execute_tool", Input: json.RawMessage(`{"name":"a","args":{"b":1,"a":"x"}}`)})
	second := toolCallSignature(llm.ToolCall{ID: "t2", Name: "execute_tool", Input: json.RawMessage(`{"args":{"a":"x","b":1},"name":"a"}`)})
	if first == "" {
		t.Fatalf("signature must not be empty")
	}
	if first != second {
		t.Fatalf("same params in different key order must produce same signature: %s vs %s", first, second)
	}
	different := toolCallSignature(llm.ToolCall{ID: "t3", Name: "execute_tool", Input: json.RawMessage(`{"name":"a","args":{"b":2,"a":"x"}}`)})
	if different == first {
		t.Fatalf("different params must produce different signature")
	}
}

func TestRecordToolAnomalyStreakAndCap(t *testing.T) {
	state := &reactEngineState{}
	call := llm.ToolCall{ID: "t1", Name: "inspect_data", Input: json.RawMessage(`{"sql":"select 1"}`)}
	other := llm.ToolCall{ID: "t2", Name: "inspect_data", Input: json.RawMessage(`{"sql":"select 2"}`)}

	// 未达阈值：无提醒
	state.recordToolAnomaly([]llm.ToolCall{call})
	state.recordToolAnomaly([]llm.ToolCall{other})
	if state.anomalyReminder() != "" {
		t.Fatalf("reminder must be empty before threshold")
	}

	// 达到阈值：注入提醒
	state.recordToolAnomaly([]llm.ToolCall{call})
	state.recordToolAnomaly([]llm.ToolCall{call})
	state.recordToolAnomaly([]llm.ToolCall{call})
	// streak=3（other 后连续 3 次 call），恰达阈值 3
	reminder := state.anomalyReminder()
	if reminder == "" || !strings.Contains(reminder, "inspect_data") {
		t.Fatalf("expected anomaly reminder after repeated signature, got %q", reminder)
	}
	state.consumeAnomalyRender()

	// 注入达上限后停止提醒
	for i := 0; i < maxAnomalyInjections+2; i++ {
		state.anomalyReminder()
		state.consumeAnomalyRender()
	}
	if state.anomalyReminder() != "" {
		t.Fatalf("reminder must stop after injection cap")
	}

	// 新签名打断 streak
	state.anomalyInjections = 0
	state.recordToolAnomaly([]llm.ToolCall{other})
	if state.anomalyReminder() != "" {
		t.Fatalf("new signature must reset streak")
	}
}

func TestSoftLandingToolBlocked(t *testing.T) {
	for _, allowed := range []string{metaToolReadToolResult, metaToolTodoWrite, metaToolAskQuestion, metaToolGetTool} {
		if reason, blocked := softLandingToolBlocked(allowed); blocked {
			t.Fatalf("tool %s should be allowed in soft landing: %s", allowed, reason)
		}
	}
	for _, name := range []string{metaToolExecuteTool, metaToolDelegateAgent, metaToolPythonExec, metaToolCreatePlan, metaToolMemoryWrite, metaToolLoadRuntimeCode} {
		if _, blocked := softLandingToolBlocked(name); !blocked {
			t.Fatalf("tool %s should be blocked in soft landing", name)
		}
	}
}

func TestMaybeEnterSoftLandingByStepBudget(t *testing.T) {
	state := &reactEngineState{}
	// maxSteps=10, softLanding=2：step 8 触发（收尾轮 8/9），step 7 未触发
	state.maybeEnterSoftLanding(7, 10)
	if state.softLandingActive {
		t.Fatalf("soft landing must not trigger before window")
	}
	state.maybeEnterSoftLanding(8, 10)
	if !state.softLandingActive || state.softLandingReason != softLandingReasonStepBudget {
		t.Fatalf("expected step_budget soft landing at step 8/10, got active=%v reason=%s", state.softLandingActive, state.softLandingReason)
	}
	// 已激活则不重复触发
	state.softLandingReason = ""
	state.maybeEnterSoftLanding(9, 10)
	if state.softLandingReason != "" {
		t.Fatalf("soft landing must stay sticky")
	}
}

func TestLoopTokenBudgetNinetyAndExhausted(t *testing.T) {
	original := conf.CustomConf.LLM.React.Loop
	defer func() { conf.CustomConf.LLM.React.Loop = original }()
	conf.CustomConf.LLM.React.Loop.BudgetTokensPerRun = 1000

	state := &reactEngineState{}
	if state.loopTokenBudget() != 1000 {
		t.Fatalf("config budget should apply when request budget unset")
	}

	// 用量 850（85%）：不软着陆、不耗尽
	state.inputTokens = 850
	if state.checkLoopTokenBudget() {
		t.Fatalf("must not be exhausted at 85%%")
	}
	if state.softLandingActive {
		t.Fatalf("must not soft land below 90%%")
	}

	// 用量 950（95%）：软着陆、未耗尽
	state.inputTokens = 950
	if state.checkLoopTokenBudget() {
		t.Fatalf("must not be exhausted at 95%%")
	}
	if !state.softLandingActive || state.softLandingReason != softLandingReasonTokenBudget {
		t.Fatalf("expected token_budget soft landing at 95%%")
	}

	// 用量 1001（>100%）：耗尽
	state.inputTokens = 1001
	if !state.checkLoopTokenBudget() {
		t.Fatalf("must be exhausted above 100%%")
	}
}

func TestLoopTokenBudgetNotAppliedToSubRun(t *testing.T) {
	original := conf.CustomConf.LLM.React.Loop
	defer func() { conf.CustomConf.LLM.React.Loop = original }()
	conf.CustomConf.LLM.React.Loop.BudgetTokensPerRun = 1000

	state := &reactEngineState{agentPath: "main/ops"}
	if state.loopTokenBudget() != 0 {
		t.Fatalf("sub run must not inherit global loop budget")
	}
}

func TestShouldContinueAfterOutputTruncation(t *testing.T) {
	state := &reactEngineState{}
	if state.shouldContinueAfterOutputTruncation("max_tokens", 0, "部分回答") != true {
		t.Fatalf("max_tokens truncation without tool calls should continue")
	}
	if state.shouldContinueAfterOutputTruncation("max_tokens", 1, "部分回答") {
		t.Fatalf("truncation with tool calls must not continue")
	}
	if state.shouldContinueAfterOutputTruncation("max_tokens", 0, "  ") {
		t.Fatalf("truncation with empty content must not continue")
	}
	if state.shouldContinueAfterOutputTruncation("end_turn", 0, "完整回答") {
		t.Fatalf("normal end_turn must not continue")
	}
	// 超过续写上限后不再续写
	state.continuationCount = conf.GetReactRuntimeConfig().Loop.OutputContinuationMax
	if state.shouldContinueAfterOutputTruncation("max_tokens", 0, "again") {
		t.Fatalf("must stop continuing after output_continuation_max")
	}
}

func TestRunDeadlineScopeDisabledAndEnabled(t *testing.T) {
	ctx, deadline, timeout, cancel := runDeadlineScope(context.Background())
	defer cancel()
	if timeout != 0 || !deadline.IsZero() || ctx == nil {
		t.Fatalf("run timeout disabled (0) should pass ctx through untouched")
	}

	original := conf.CustomConf.LLM.React.Loop
	defer func() { conf.CustomConf.LLM.React.Loop = original }()
	conf.CustomConf.LLM.React.Loop.RunTimeoutSec = 1

	enabledCtx, _, enabledTimeout, enableCancel := runDeadlineScope(context.Background())
	defer enableCancel()
	if enabledTimeout != 1*1e9 || enabledCtx == nil {
		t.Fatalf("expected 1s run timeout, got %v", enabledTimeout)
	}
}
