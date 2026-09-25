package react

import (
	"errors"
	"strings"
	"testing"

	model "react-base-service/models/llm"
)

func sampleNotification(subRunID, status string) runtimeNotification {
	return runtimeNotification{
		PendingInputID: 7,
		SubRunID:       subRunID,
		AgentKey:       "ops-agent",
		AgentPath:      "main/ops-agent",
		Status:         status,
		Summary:        "后台任务完成",
		InputTokens:    120,
		OutputTokens:   45,
	}
}

// 通知箱是单生产者（完成监视 goroutine）+ 单消费者（引擎循环）的 FIFO 队列；
// 消费失败放回队首必须保持原有顺序，供下一边界重试。
func TestRuntimeCommandBox_FIFOAndPushFront(t *testing.T) {
	box := newRuntimeCommandBox()
	if got := box.drainNotifications(); len(got) != 0 {
		t.Fatalf("空箱 drain 应返回空: %v", got)
	}
	box.PushNotification(sampleNotification("run_a", "success"))
	box.PushNotification(sampleNotification("run_b", "error"))

	drained := box.drainNotifications()
	if len(drained) != 2 || drained[0].SubRunID != "run_a" || drained[1].SubRunID != "run_b" {
		t.Fatalf("drain 应按 FIFO 返回全部通知: %+v", drained)
	}
	if got := box.drainNotifications(); len(got) != 0 {
		t.Fatalf("drain 后箱子应为空: %v", got)
	}

	// 放回队首：先放回的在前，后续 Push 追加在尾。
	box.pushFrontNotifications([]runtimeNotification{drained[1], drained[0]})
	box.PushNotification(sampleNotification("run_c", "success"))
	again := box.drainNotifications()
	if len(again) != 3 || again[0].SubRunID != "run_b" || again[1].SubRunID != "run_a" || again[2].SubRunID != "run_c" {
		t.Fatalf("pushFront 应恢复原顺序且新通知追加在尾: %+v", again)
	}
}

func TestRenderTaskNotificationEnvelope(t *testing.T) {
	envelope := renderTaskNotificationEnvelope(sampleNotification("run_abc", "success"))
	for _, required := range []string{
		"<task-notification>",
		"<task-id>run_abc</task-id>",
		"<agent>ops-agent</agent>",
		"<agent-path>main/ops-agent</agent-path>",
		"<status>success</status>",
		"<summary>",
		"后台任务完成",
		`input-tokens="120"`,
		`output-tokens="45"`,
		"</task-notification>",
	} {
		if !strings.Contains(envelope, required) {
			t.Fatalf("信封应包含 %q:\n%s", required, envelope)
		}
	}
}

// 通知摘要超长时必须截断：机器事件合并为一条 user 消息进入模型上下文，
// 不加限制会复刻大工具结果撑爆上下文的老问题。
func TestTruncateNotificationSummary(t *testing.T) {
	if got := truncateNotificationSummary("  短摘要  "); got != "短摘要" {
		t.Fatalf("短摘要应仅去空白: %q", got)
	}
	long := strings.Repeat("长", maxNotificationSummaryChars+10)
	got := truncateNotificationSummary(long)
	if len([]rune(got)) > maxNotificationSummaryChars+10 || !strings.HasSuffix(got, "…(截断)") {
		t.Fatalf("超长摘要应截断并标记: len=%d, tail=%q", len([]rune(got)), got[max(0, len(got)-24):])
	}
}

func TestMergeTaskNotificationContent(t *testing.T) {
	content := mergeTaskNotificationContent([]runtimeNotification{
		sampleNotification("run_a", "success"),
		sampleNotification("run_b", "error"),
	})
	if !strings.HasPrefix(content, taskNotificationPreamble) {
		t.Fatalf("合并消息应以说明前缀开头:\n%s", content)
	}
	if !strings.Contains(content, "<task-id>run_a</task-id>") || !strings.Contains(content, "<task-id>run_b</task-id>") {
		t.Fatalf("合并消息应包含全部通知信封:\n%s", content)
	}
	if strings.Count(content, "<task-notification>") != 2 {
		t.Fatalf("每条通知一个信封:\n%s", content)
	}
	if strings.Contains(content, "run_c") {
		t.Fatalf("不应包含未传入的通知:\n%s", content)
	}
}

// 后台子 run 终态到通知状态的映射：nil=success，取消/超时/其余错误各自成词。
func TestBackgroundNotificationStatus(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, "success"},
		{ErrReactRunCancelled, "cancelled"},
		{ErrReactRunTimeout, "timeout"},
		{errors.New("model exploded"), "error"},
	}
	for _, tc := range cases {
		if got := backgroundNotificationStatus(tc.err); got != tc.want {
			t.Fatalf("backgroundNotificationStatus(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// 父 run 只有 running 状态才有通知消费方；终态一律不投递（结果留在子 run 记录里）。
func TestShouldDeliverBackgroundNotification(t *testing.T) {
	for state, want := range map[string]bool{
		model.ReactRunStateRunning:              true,
		model.ReactRunStateFinished:             false,
		model.ReactRunStateCancelled:            false,
		model.ReactRunStateError:                false,
		model.ReactRunStateTimeout:              false,
		model.ReactRunStateWaitingClientMessage: false,
	} {
		if got := shouldDeliverBackgroundNotification(state); got != want {
			t.Fatalf("shouldDeliverBackgroundNotification(%s) = %v, want %v", state, got, want)
		}
	}
}

// async_launched 文案纪律（对齐 ZCode agent.ts:153-171）：告知 runId、说明自动通知、
// 要求模型简要转达并结束本轮回复——避免父模型空转等待或编造子任务结论。
func TestRenderBackgroundDelegateLaunchedText(t *testing.T) {
	text := renderBackgroundDelegateLaunchedText("ops-agent", "run_xyz")
	for _, required := range []string{"ops-agent", "run_xyz", "自动回灌", "结束本轮回复"} {
		if !strings.Contains(text, required) {
			t.Fatalf("启动回执文案应包含 %q: %s", required, text)
		}
	}
}

// delegate_agent 工具 schema 必须暴露 background 参数，模型才有可能选择后台模式。
func TestDelegateAgentToolDefinitionIncludesBackground(t *testing.T) {
	def := delegateAgentToolDefinition([]model.Agent{{AgentKey: "ops-agent", Name: "OPS", Description: "ops"}})
	properties, ok := def.Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("parameters.properties 缺失: %+v", def.Parameters)
	}
	background, ok := properties["background"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties.background 缺失: %+v", properties)
	}
	if background["type"] != "boolean" {
		t.Fatalf("background 应为 boolean: %+v", background)
	}
	if !strings.Contains(def.Description, "background") {
		t.Fatalf("工具描述应说明后台模式: %s", def.Description)
	}
}
