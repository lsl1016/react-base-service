package react

import (
	"encoding/json"
	"testing"

	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// TestSubRunStatusMapping：内部 run 状态 → 对外词汇的映射口径。
func TestSubRunStatusMapping(t *testing.T) {
	cases := []struct {
		state    string
		want     string
		terminal bool
		waiting  bool
	}{
		{model.ReactRunStateFinished, subRunStatusCompleted, true, false},
		{model.ReactRunStateError, subRunStatusError, true, false},
		{model.ReactRunStateTimeout, subRunStatusTimeout, true, false},
		{model.ReactRunStateCancelled, subRunStatusCancelled, true, false},
		{model.ReactRunStateExpired, subRunStatusExpired, true, false},
		{model.ReactRunStateWaitingClientMessage, subRunStatusWaiting, false, true},
		{model.ReactRunStateWaitingPlan, subRunStatusWaiting, false, true},
		{model.ReactRunStateRunning, subRunStatusRunning, false, false},
		{model.ReactRunStateCancelling, subRunStatusRunning, false, false},
		{"unknown_state", subRunStatusRunning, false, false},
	}
	for _, c := range cases {
		got, terminal, waiting := subRunStatusMapping(c.state)
		if got != c.want || terminal != c.terminal || waiting != c.waiting {
			t.Fatalf("state=%s: got=(%s,%v,%v), want=(%s,%v,%v)", c.state, got, terminal, waiting, c.want, c.terminal, c.waiting)
		}
	}
}

// TestResolveWaitAgentTimeout：缺省跟随看门狗、显式值钳位、不超过看门狗。
func TestResolveWaitAgentTimeout(t *testing.T) {
	// 看门狗未配置（0=不限）时缺省 300s。
	if got := resolveWaitAgentTimeout(0).Seconds(); got != 300 {
		t.Fatalf("无看门狗时缺省应为 300s, got %v", got)
	}
	// 显式值在下限与绝对上限内取值。
	if got := resolveWaitAgentTimeout(5).Seconds(); got != 5 {
		t.Fatalf("显式 5s 应取 5s, got %v", got)
	}
	if got := resolveWaitAgentTimeout(7200).Seconds(); got != waitAgentMaxTimeoutSec {
		t.Fatalf("超过绝对上限应钳位, got %v", got)
	}
	if got := resolveWaitAgentTimeout(-3).Seconds(); got != 300 {
		t.Fatalf("负值按缺省处理, got %v", got)
	}
	// 显式值不超过看门狗：等更久没有意义（看门狗先杀子 run）。
	watchdog := 60
	conf.CustomConf.LLM.React.SubAgent.MaxRunSeconds = watchdog
	defer func() { conf.CustomConf.LLM.React.SubAgent.MaxRunSeconds = 0 }()
	if got := resolveWaitAgentTimeout(120).Seconds(); got != 60 {
		t.Fatalf("显式值应被看门狗收紧, got %v", got)
	}
	if got := resolveWaitAgentTimeout(0).Seconds(); got != 60 {
		t.Fatalf("缺省应取看门狗值, got %v", got)
	}
}

// TestAgentKeyFromAgentPath：agentPath 末段即本 run 的 agentKey。
func TestAgentKeyFromAgentPath(t *testing.T) {
	cases := map[string]string{
		"main/ops-agent":       "ops-agent",
		"main/ops-agent/dba":   "dba",
		"main":                 "main",
		"":                     "",
		"  main/ops-agent    ": "ops-agent",
	}
	for path, want := range cases {
		if got := agentKeyFromAgentPath(path); got != want {
			t.Fatalf("agentPath=%q: got %q, want %q", path, got, want)
		}
	}
}

// TestSubRunFinalStatus：delegate_agent 返回值的状态词来源。
func TestSubRunFinalStatus(t *testing.T) {
	if got := subRunFinalStatus(nil); got != subRunStatusCompleted {
		t.Fatalf("run 缺失应回退 completed, got %s", got)
	}
	if got := subRunFinalStatus(&model.ReactRun{State: model.ReactRunStateFinished}); got != subRunStatusCompleted {
		t.Fatalf("finished 应映射 completed, got %s", got)
	}
	if got := subRunFinalStatus(&model.ReactRun{State: model.ReactRunStateTimeout}); got != subRunStatusTimeout {
		t.Fatalf("timeout 应映射 timeout, got %s", got)
	}
}

// TestWaitAgentEntryForRunAuthorization：只允许等本 run 委派出去的子 run，
// 他人 runId / 不存在的 run 一律 not_found 且不 pending（防跨 run 窥探）。
// 终态错误 / waiting / running 的快照语义一并覆盖；completed 需查消息库，由 e2e 覆盖。
func TestWaitAgentEntryForRunAuthorization(t *testing.T) {
	const parentID = "parent-1"

	// run 不存在 → not_found
	entry, pending := waitAgentEntryForRun(nil, "r1", parentID, nil)
	if entry.Status != subRunStatusNotFound || pending {
		t.Fatalf("nil run 应为 not_found 非 pending: %+v, pending=%v", entry, pending)
	}
	// 他人委派的子 run（parent 不匹配）→ not_found，防跨 run 窥探
	entry, pending = waitAgentEntryForRun(&model.ReactRun{ParentRunID: "other-parent", State: model.ReactRunStateFinished}, "r1", parentID, nil)
	if entry.Status != subRunStatusNotFound || pending {
		t.Fatalf("parent 不匹配应为 not_found 非 pending: %+v, pending=%v", entry, pending)
	}

	// 终态 error：状态与 errorMessage 透传，不 pending
	entry, pending = waitAgentEntryForRun(&model.ReactRun{ParentRunID: parentID, State: model.ReactRunStateError, ErrorMessage: "模型故障"}, "r1", parentID, nil)
	if entry.Status != subRunStatusError || entry.ErrorMessage != "模型故障" || pending {
		t.Fatalf("error 终态快照不符: %+v, pending=%v", entry, pending)
	}
	// 终态 timeout：空 errorMessage 时给兜底文案
	entry, pending = waitAgentEntryForRun(&model.ReactRun{ParentRunID: parentID, State: model.ReactRunStateTimeout}, "r1", parentID, nil)
	if entry.Status != subRunStatusTimeout || entry.ErrorMessage == "" || pending {
		t.Fatalf("timeout 终态快照不符: %+v, pending=%v", entry, pending)
	}

	// waiting_*（HITL）：不算完成、不 pending，立即随快照返回
	entry, pending = waitAgentEntryForRun(&model.ReactRun{ParentRunID: parentID, State: model.ReactRunStateWaitingClientMessage, AgentPath: "main/echo-agent"}, "r1", parentID, nil)
	if entry.Status != subRunStatusWaiting || entry.AgentKey != "echo-agent" || pending {
		t.Fatalf("waiting 快照不符: %+v, pending=%v", entry, pending)
	}

	// running：pending=true（值得继续等）
	entry, pending = waitAgentEntryForRun(&model.ReactRun{ParentRunID: parentID, State: model.ReactRunStateRunning}, "r1", parentID, nil)
	if entry.Status != subRunStatusRunning || !pending {
		t.Fatalf("running 应为 pending: %+v, pending=%v", entry, pending)
	}
}

// TestWaitAgentEntryJSON：快照序列化字段稳定性（runId/agentKey/status/finalResponse/errorMessage）。
func TestWaitAgentEntryJSON(t *testing.T) {
	entry := waitAgentEntry{RunID: "r1", AgentKey: "echo-agent", Status: subRunStatusCompleted, FinalResponse: "答案"}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	for _, key := range []string{"runId", "agentKey", "status", "finalResponse"} {
		if _, ok := back[key]; !ok {
			t.Fatalf("快照缺少字段 %s: %s", key, data)
		}
	}
}
