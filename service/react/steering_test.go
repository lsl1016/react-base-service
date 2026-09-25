package react

import (
	"encoding/json"
	"testing"

	llm "react-base-service/api/llm"
	model "react-base-service/models/llm"
)

// Steering S1（docs/plan/20260925_Session与Steering机制借鉴方案.md §2.3）：
// 准入决策、结算原因映射、账本状态机与 guide 挑选全部写成纯函数，不依赖真实 DB；
// 需要真库的准入落库/事务原子性由启动服务实测覆盖（见 changelog §4）。

func TestSteerAdmissionDecision(t *testing.T) {
	cases := []struct {
		name         string
		in           steerAdmissionInput
		wantKind     string
		wantReason   string
	}{
		// run 处于 running、无附件、非软着陆 → guide（最高优先路径）。
		{"running_clean_guide", steerAdmissionInput{ActiveRunState: model.ReactRunStateRunning}, steerDecisionGuide, ""},
		// waiting/cancelling 状态不打断 HITL 等待：S1（排队未开）明确拒绝。
		{"waiting_client_message_reject", steerAdmissionInput{ActiveRunState: model.ReactRunStateWaitingClientMessage}, steerDecisionReject, steerRejectReasonRunNotSteerable},
		{"waiting_plan_reject", steerAdmissionInput{ActiveRunState: model.ReactRunStateWaitingPlan}, steerDecisionReject, steerRejectReasonRunNotSteerable},
		{"cancelling_reject", steerAdmissionInput{ActiveRunState: model.ReactRunStateCancelling}, steerDecisionReject, steerRejectReasonRunNotSteerable},
		// 软着陆收尾窗口内不再接收 guide（硬约束）。
		{"soft_landing_reject", steerAdmissionInput{ActiveRunState: model.ReactRunStateRunning, SoftLanding: true}, steerDecisionReject, steerRejectReasonSoftLanding},
		// 带附件不走 guide：S1 明确拒绝，S2（排队开启）落 queued。
		{"attachments_reject", steerAdmissionInput{ActiveRunState: model.ReactRunStateRunning, HasAttachments: true}, steerDecisionReject, steerRejectReasonAttachments},
		{"attachments_queue_when_enabled", steerAdmissionInput{ActiveRunState: model.ReactRunStateRunning, HasAttachments: true, QueueEnabled: true}, steerDecisionQueue, steerQueueReasonAttachments},
		// 排队开启后，非 running 状态与软着陆都改为落队列。
		{"waiting_queue_when_enabled", steerAdmissionInput{ActiveRunState: model.ReactRunStateWaitingClientMessage, QueueEnabled: true}, steerDecisionQueue, steerQueueReasonNotSteerable},
		{"soft_landing_queue_when_enabled", steerAdmissionInput{ActiveRunState: model.ReactRunStateRunning, SoftLanding: true, QueueEnabled: true}, steerDecisionQueue, steerQueueReasonSoftLanding},
		// 终态 run 不可能成为准入目标，防御性拒绝。
		{"finished_reject", steerAdmissionInput{ActiveRunState: model.ReactRunStateFinished}, steerDecisionReject, steerRejectReasonRunNotSteerable},
		{"empty_state_reject", steerAdmissionInput{}, steerDecisionReject, steerRejectReasonRunNotSteerable},
	}
	for _, tc := range cases {
		decision := steerAdmissionDecision(tc.in)
		if decision.Kind != tc.wantKind || decision.Reason != tc.wantReason {
			t.Fatalf("%s: got kind=%s reason=%s, want kind=%s reason=%s", tc.name, decision.Kind, decision.Reason, tc.wantKind, tc.wantReason)
		}
	}
}

func TestSteerDiscardReasonForRunState(t *testing.T) {
	cases := []struct {
		state string
		want  string
	}{
		{model.ReactRunStateCancelled, model.ReactPendingSettleTurnCancelled},
		{model.ReactRunStateCancelling, model.ReactPendingSettleTurnCancelled},
		{model.ReactRunStateError, model.ReactPendingSettleTurnFailed},
		{model.ReactRunStateTimeout, model.ReactPendingSettleTurnFailed},
		{model.ReactRunStateExpired, model.ReactPendingSettleTurnFailed},
		{model.ReactRunStateFinished, model.ReactPendingSettleRunFinished},
	}
	for _, tc := range cases {
		if got := steerDiscardReasonForRunState(tc.state); got != tc.want {
			t.Fatalf("state=%s: got %s, want %s", tc.state, got, tc.want)
		}
	}
}

func TestValidPendingInputTransition(t *testing.T) {
	// admitted 可被消费/结算/降级排队；queued 只可被晋升或取消；其余一律终态。
	valid := [][2]string{
		{model.ReactPendingStatusAdmitted, model.ReactPendingStatusGuided},
		{model.ReactPendingStatusAdmitted, model.ReactPendingStatusDiscarded},
		{model.ReactPendingStatusAdmitted, model.ReactPendingStatusQueued},
		{model.ReactPendingStatusQueued, model.ReactPendingStatusGuided},
		{model.ReactPendingStatusQueued, model.ReactPendingStatusCancelled},
		{model.ReactPendingStatusQueued, model.ReactPendingStatusDiscarded},
	}
	for _, pair := range valid {
		if !validPendingInputTransition(pair[0], pair[1]) {
			t.Fatalf("transition %s->%s should be valid", pair[0], pair[1])
		}
	}
	invalid := [][2]string{
		{model.ReactPendingStatusGuided, model.ReactPendingStatusDiscarded},
		{model.ReactPendingStatusDiscarded, model.ReactPendingStatusGuided},
		{model.ReactPendingStatusCancelled, model.ReactPendingStatusQueued},
		{model.ReactPendingStatusGuided, model.ReactPendingStatusQueued},
	}
	for _, pair := range invalid {
		if validPendingInputTransition(pair[0], pair[1]) {
			t.Fatalf("transition %s->%s should be invalid", pair[0], pair[1])
		}
	}
}

func TestPickPendingGuide_FIFOOnlyAdmitted(t *testing.T) {
	rows := []model.ReactPendingInput{
		{ID: 1, Status: model.ReactPendingStatusGuided, Delivery: model.ReactPendingDeliveryGuide, Seq: 1},
		{ID: 2, Status: model.ReactPendingStatusAdmitted, Delivery: model.ReactPendingDeliveryQueue, Seq: 2},
		{ID: 3, Status: model.ReactPendingStatusAdmitted, Delivery: model.ReactPendingDeliveryGuide, Seq: 5},
		{ID: 4, Status: model.ReactPendingStatusAdmitted, Delivery: model.ReactPendingDeliveryGuide, Seq: 3},
		{ID: 5, Status: model.ReactPendingStatusDiscarded, Delivery: model.ReactPendingDeliveryGuide, Seq: 4},
	}
	picked := pickPendingGuide(rows)
	if picked == nil {
		t.Fatalf("expected an admitted guide to be picked")
	}
	// 只取 delivery=guide 且 status=admitted 的最早一条（seq=3 的 ID=4）；
	// queue 行（ID=2）与已消费/已结算行都不参与。
	if picked.ID != 4 || picked.Seq != 3 {
		t.Fatalf("expected FIFO admitted guide id=4 seq=3, got id=%d seq=%d", picked.ID, picked.Seq)
	}
	if empty := pickPendingGuide(nil); empty != nil {
		t.Fatalf("empty rows should pick nothing")
	}
}

func TestGuideMessageContent_ReplayAsUserMessage(t *testing.T) {
	guide := guideMessageContent("把结果整理成表格")
	if guide.Role != model.ReactMessageRoleUser || guide.Content != "把结果整理成表格" {
		t.Fatalf("guide message should be a plain user message, got %+v", guide)
	}
	// 落库后经历史重建必须原样回放为 user 消息（对齐 user_input 的重建路径）。
	contentJSON, err := json.Marshal(map[string]any{
		"content":      "把结果整理成表格",
		"modelMessage": guide,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	stored := model.ReactMessage{MessageType: model.ReactMessageTypeUserInput, ContentJSON: string(contentJSON)}
	replayed, ok := reactMessageToChatMessage(stored)
	if !ok {
		t.Fatalf("guide message should replay from stored history")
	}
	if replayed.Role != model.ReactMessageRoleUser || replayed.Content != "把结果整理成表格" {
		t.Fatalf("replayed message mismatch: %+v", replayed)
	}
}

func TestShouldAutoDrainQueue(t *testing.T) {
	// 自动续跑只在排队+自动续跑开关都开启、run 以成功 finish 收敛时触发；
	// cancel/error/timeout 收敛不续跑（失败语义对齐 ZCode：错误后暂停，留给用户决定）。
	if !shouldAutoDrainQueue(true, true, true) {
		t.Fatalf("queue enabled + auto drain + finished should drain")
	}
	if shouldAutoDrainQueue(true, true, false) {
		t.Fatalf("abnormal run end should not drain (paused)")
	}
	if shouldAutoDrainQueue(false, true, true) {
		t.Fatalf("queue disabled should not drain")
	}
	if shouldAutoDrainQueue(true, false, true) {
		t.Fatalf("auto drain disabled should not drain")
	}
}

func TestPickPendingQueued_FIFOOnlyQueued(t *testing.T) {
	rows := []model.ReactPendingInput{
		{ID: 1, Status: model.ReactPendingStatusGuided, Delivery: model.ReactPendingDeliveryQueue, Seq: 1},
		{ID: 2, Status: model.ReactPendingStatusAdmitted, Delivery: model.ReactPendingDeliveryGuide, Seq: 2},
		{ID: 3, Status: model.ReactPendingStatusQueued, Delivery: model.ReactPendingDeliveryQueue, Seq: 5},
		{ID: 4, Status: model.ReactPendingStatusQueued, Delivery: model.ReactPendingDeliveryQueue, Seq: 3},
		{ID: 5, Status: model.ReactPendingStatusDiscarded, Delivery: model.ReactPendingDeliveryQueue, Seq: 4},
	}
	// 队首只取 status=queued 的最早一条；admitted guide / 已消费 / 已结算行都不参与。
	picked := pickPendingQueued(rows)
	if picked == nil || picked.ID != 4 {
		t.Fatalf("expected FIFO queued row id=4, got %+v", picked)
	}
}

// TestAppendGuideContext_NeverBetweenToolUseAndToolResult 锁定注入顺序不变量：
// guide 只能追加在整批 tool_result 消息之后，成为下一次模型请求的尾部 user 消息。
func TestAppendGuideContext_NeverBetweenToolUseAndToolResult(t *testing.T) {
	assistantMsg := llm.ChatMessage{Role: model.ReactMessageRoleAssistant, Parts: []llm.ContentPart{
		{Type: "tool_use", ID: "call_1", Name: "get_tool"},
	}}
	toolResultMsg := llm.ChatMessage{Role: model.ReactMessageRoleUser, Parts: []llm.ContentPart{
		{Type: "tool_result", ToolUseID: "call_1", Content: "ok"},
	}}
	messages := []llm.ChatMessage{assistantMsg, toolResultMsg}
	refs := [][]reactMessageRef{nil, nil}

	guide := guideMessageContent("补充一句：加上环比")
	ref := reactMessageRef{RunID: "run_x", MessageID: "msg_g", Seq: 9}
	appended := appendGuideToContext(messages, refs, guide, ref)
	if len(appended.messages) != 3 {
		t.Fatalf("expected 3 messages after append, got %d", len(appended.messages))
	}
	last := appended.messages[len(appended.messages)-1]
	if last.Role != model.ReactMessageRoleUser || last.Content != "补充一句：加上环比" {
		t.Fatalf("guide must be the tail user message, got %+v", last)
	}
	// guide 之前的消息序列不变（tool_result 仍在 assistant 之后、guide 之前）。
	if appended.messages[0].Role != model.ReactMessageRoleAssistant || appended.messages[1].Role != model.ReactMessageRoleUser {
		t.Fatalf("existing tool round order must be preserved: %+v", appended.messages)
	}
	if len(appended.refs) != 3 || appended.refs[2][0].MessageID != "msg_g" {
		t.Fatalf("guide ref should be recorded, got %+v", appended.refs)
	}
}
