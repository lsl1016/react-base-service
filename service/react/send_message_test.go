package react

import (
	"strings"
	"testing"
	"time"

	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// SendMessage 目标选择（纯函数）：活跃优先、同路径多委派取最新活跃、
// 全部终态时返回最新终态行（供"已结束"明确报错）、cancelling 不算可投递。
func TestChooseSendMessageTarget(t *testing.T) {
	if got := chooseSendMessageTarget(nil); got != nil {
		t.Fatalf("空候选应返回 nil: %+v", got)
	}

	base := model.ReactRun{SessionID: "s1", ParentRunID: "run_p", AgentPath: "main/ops-agent"}
	active := base
	active.ID = 1
	active.RunID = "run_1"
	active.State = model.ReactRunStateRunning
	terminal := base
	terminal.ID = 2
	terminal.RunID = "run_2"
	terminal.State = model.ReactRunStateFinished

	// 活跃优先于更早的终态 run（候选按创建序）。
	got := chooseSendMessageTarget([]model.ReactRun{terminal, active})
	if got == nil || got.RunID != "run_1" {
		t.Fatalf("应选择活跃 run: %+v", got)
	}

	// 同路径多次委派：取最新的活跃 run。
	activeOld := active
	activeOld.ID = 3
	activeOld.RunID = "run_3"
	activeOld.State = model.ReactRunStateFinished
	got = chooseSendMessageTarget([]model.ReactRun{active, activeOld})
	if got == nil || got.RunID != "run_1" {
		t.Fatalf("终态 run 不应抢占活跃目标: %+v", got)
	}

	// 全部终态：返回最新（创建序最后一个）。
	got = chooseSendMessageTarget([]model.ReactRun{terminal, activeOld})
	if got == nil || got.RunID != "run_3" {
		t.Fatalf("无活跃时应返回最新终态 run: %+v", got)
	}

	// cancelling 不可投递：仅剩 cancelling 时返回该行，由上层按状态拒绝。
	cancelling := base
	cancelling.ID = 4
	cancelling.RunID = "run_4"
	cancelling.State = model.ReactRunStateCancelling
	got = chooseSendMessageTarget([]model.ReactRun{cancelling})
	if got == nil || got.State != model.ReactRunStateCancelling {
		t.Fatalf("无活跃时应返回 cancelling 行交由上层拒绝: %+v", got)
	}
}

func TestParsePendingInputID(t *testing.T) {
	id, err := parsePendingInputID("pend_13")
	if err != nil || id != 13 {
		t.Fatalf("pend_13 应解析为 13: id=%d, err=%v", id, err)
	}
	for _, bad := range []string{"", "pend_0", "pend_", "13", "queue_3"} {
		if _, err := parsePendingInputID(bad); err == nil {
			t.Fatalf("%q 应解析失败", bad)
		}
	}
}

// send_message 工具声明：message 必填，agent_path/run_id 定位二选一。
func TestSendMessageToolDefinition(t *testing.T) {
	def := sendMessageToolDefinition()
	if def.Name != metaToolSendMessage {
		t.Fatalf("工具名应为 %s: %s", metaToolSendMessage, def.Name)
	}
	required, _ := def.Parameters["required"].([]string)
	if len(required) != 1 || required[0] != "message" {
		t.Fatalf("required 应仅含 message: %v", required)
	}
	properties, _ := def.Parameters["properties"].(map[string]interface{})
	for _, key := range []string{"agent_path", "run_id", "message"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("properties 缺少 %s: %+v", key, properties)
		}
	}
	if !strings.Contains(def.Description, "agent_path") {
		t.Fatalf("描述应说明定位方式: %s", def.Description)
	}
}

// 看门狗时长解析：<=0 关闭，正数按秒。
func TestSubAgentWatchdogDuration(t *testing.T) {
	if d := (conf.ReactSubAgentConfig{}).SubAgentWatchdogDuration(); d != 0 {
		t.Fatalf("未配置应为 0（不限）: %v", d)
	}
	if d := (conf.ReactSubAgentConfig{MaxRunSeconds: -1}).SubAgentWatchdogDuration(); d != 0 {
		t.Fatalf("负值应为 0: %v", d)
	}
	if d := (conf.ReactSubAgentConfig{MaxRunSeconds: 90}).SubAgentWatchdogDuration(); d != 90*time.Second {
		t.Fatalf("90 秒应解析为 90s: %v", d)
	}
}
