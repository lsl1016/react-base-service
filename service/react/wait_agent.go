package react

// wait_agent：子 run 结果等待原语（join，todo P1，方案 docs/todo/20260926_多智能体编排补齐点与场景.md）。
//
// 解决的洞：后台委派（background=true）后父模型在某个 step 确实需要某个子 run 的结果，
// 但它还在跑——此前模型没有任何等待手段，只能被动在步边界收通知；父 run 提前收尾时
// 完成通知还会被拒投（errBackgroundParentInactive，结果静默丢失）。wait_agent 让父循环
// 阻塞在工具执行内轮询目标 run 状态，与阻塞委派同构，复用取消级联与并行 HITL 通道。
//
// 语义约定：
//   - 返回是「各 run 的状态快照」而非整体报错——部分超时、部分成功是常态，父模型据此
//     自行决定下一步（继续等 / send_message 催办 / 放弃并自行收尾）；
//   - waiting_client_message / waiting_plan（子 run 在 HITL 等用户输入）不算完成：
//     立即返回快照（status=waiting），由父模型决定催办或放弃，避免空转；
//   - timeout 按快照收场（未完成 run 标 running），不是错误结果；
//   - 等待期间目标 run 的完成通知仍会投递命令箱（父 run 活跃），下一步边界照常吸收，
//     与快照信息冗余但无害（通知 claim-once，快照是权威返回）。
//
// 安全边界：run_ids 只允许等本 run 委派出去的子 run（快照校验 parent_run_id == 本 run），
// 防跨 run 窥探其它会话/父级的子任务状态。

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/conf"
	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

const metaToolWaitAgent = "wait_agent"

// waitAgentPollInterval 是子 run 状态轮询间隔；等待是父循环内的 DB 轻查询，2s 足够。
const waitAgentPollInterval = 2 * time.Second

// waitAgentTimeoutBounds 是 timeout_sec 的钳位范围：下限防 0/负值空转，上限防单工具占用循环过久。
const (
	waitAgentMinTimeoutSec = 1
	waitAgentMaxTimeoutSec = 3600
)

// waitAgentMaxRuns 是单次等待的 run 数上限：同轮后台委派的并行度受 subagent.max_parallel
// 约束，10 远超实际并行数，防模型塞入超长列表拖慢轮询。
const waitAgentMaxRuns = 10

// 子 run 终态对外词汇：delegate_agent 返回值与 wait_agent 快照共用同一套。
// success/error/cancelled/timeout 与后台通知（backgroundNotificationStatus）口径一致；
// completed 是 finished 的对外词（带 finalResponse），expired/waiting/not_found 为 wait_agent 扩展。
const (
	subRunStatusCompleted = "completed"
	subRunStatusSuccess   = "success"
	subRunStatusWaiting   = "waiting"
	subRunStatusRunning   = "running"
	subRunStatusNotFound  = "not_found"
	subRunStatusError     = "error"
	subRunStatusTimeout   = "timeout"
	subRunStatusCancelled = "cancelled"
	subRunStatusExpired   = "expired"
)

func waitAgentToolDefinition() llm.ToolDefinition {
	return objectTool(metaToolWaitAgent,
		"等待一个或多个后台子 Agent（delegate_agent background=true 返回的 runId）到达终态，并返回各 run 的状态快照。"+
			"适用于：后台委派后继续主干任务，推进到某一步必须使用某个子任务结果时，用本工具收割。"+
			"返回按 run 逐条给出 status 与 finalResponse（status=completed 时）；waiting 表示该子任务正在等用户输入，"+
			"running 表示仍在执行（超时收场），此时可再次等待、send_message 催办或放弃并自行收尾。"+
			"不要等待阻塞委派（background 缺省）的委派——那类调用已经在结果里带回结论。",
		map[string]interface{}{
			"run_ids": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"minItems":    1,
				"maxItems":    waitAgentMaxRuns,
				"description": "要等待的后台子 run ID 列表（delegate_agent 后台启动回执中的 runId）。",
			},
			"timeout_sec": map[string]interface{}{
				"type":        "number",
				"description": "最长等待秒数，可选；缺省跟随子代理看门狗上限，未配置时 300 秒。超时不是错误：已到达终态的 run 正常返回，未完成的标记 running。",
			},
		})
}

// waitAgentInput 是 wait_agent 的输入信封。
type waitAgentInput struct {
	Description string   `json:"description"`
	RunIDs      []string `json:"run_ids"`
	TimeoutSec  int      `json:"timeout_sec"`
}

// waitAgentEntry 是快照里单个 run 的状态行。
type waitAgentEntry struct {
	RunID         string `json:"runId"`
	AgentKey      string `json:"agentKey,omitempty"`
	Status        string `json:"status"`
	FinalResponse string `json:"finalResponse,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
}

// subRunStatusMapping 把子 run 内部状态映射为对外词汇：
// 返回 (对外状态, 是否终态, 是否等待用户输入)。内部 running/cancelling 归一为 running（继续等）。
func subRunStatusMapping(state string) (string, bool, bool) {
	switch state {
	case model.ReactRunStateFinished:
		return subRunStatusCompleted, true, false
	case model.ReactRunStateError:
		return subRunStatusError, true, false
	case model.ReactRunStateTimeout:
		return subRunStatusTimeout, true, false
	case model.ReactRunStateCancelled:
		return subRunStatusCancelled, true, false
	case model.ReactRunStateExpired:
		return subRunStatusExpired, true, false
	case model.ReactRunStateWaitingClientMessage, model.ReactRunStateWaitingPlan:
		return subRunStatusWaiting, false, true
	default:
		// running / cancelling / 未知状态：继续等。
		return subRunStatusRunning, false, false
	}
}

// resolveWaitAgentTimeout 钳位等待时长：缺省跟随子代理看门狗上限（看门狗会先杀子 run，
// 等更久没有意义），未配置看门狗时 300s；显式传入的值在 [1, 3600] 内取值，同样不超过看门狗。
func resolveWaitAgentTimeout(requested int) time.Duration {
	watchdogSec := conf.GetReactRuntimeConfig().SubAgent.SubAgentWatchdogDuration()
	watchdog := int(watchdogSec / time.Second)
	timeout := requested
	if timeout <= 0 {
		if watchdog > 0 {
			timeout = watchdog
		} else {
			timeout = 300
		}
	}
	if timeout > waitAgentMaxTimeoutSec {
		timeout = waitAgentMaxTimeoutSec
	}
	if watchdog > 0 && timeout > watchdog {
		timeout = watchdog
	}
	if timeout < waitAgentMinTimeoutSec {
		timeout = waitAgentMinTimeoutSec
	}
	return time.Duration(timeout) * time.Second
}

// executeWaitAgent 执行 wait_agent：校验入参 → 阻塞轮询目标 run → 返回状态快照。
// 返回值遵循 executeInternalToolContent 约定：err 仅在取消/断连时非 nil 向上传播。
func (s *reactEngineState) executeWaitAgent(call llm.ToolCall, step int) (string, bool, error) {
	var input waitAgentInput
	if err := json.Unmarshal(call.Input, &input); err != nil {
		return "wait_agent input must be a valid JSON object", true, nil
	}
	runIDs := make([]string, 0, len(input.RunIDs))
	seen := make(map[string]bool, len(input.RunIDs))
	for _, runID := range input.RunIDs {
		runID = strings.TrimSpace(runID)
		if runID == "" || seen[runID] {
			continue
		}
		seen[runID] = true
		runIDs = append(runIDs, runID)
		if len(runIDs) >= waitAgentMaxRuns {
			break
		}
	}
	if len(runIDs) == 0 {
		return "wait_agent requires run_ids (from delegate_agent background launch receipts)", true, nil
	}
	timeout := resolveWaitAgentTimeout(input.TimeoutSec)
	deadline := time.Now().Add(timeout)

	zlog.Infof(s.ctx, "[React.WaitAgent] 开始等待: runId=%s, targets=%v, timeout=%s", s.runID, runIDs, timeout)
	var entries []waitAgentEntry
	var pendingCount int
	for {
		entries, pendingCount = s.collectWaitAgentSnapshot(runIDs)
		if pendingCount == 0 {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			zlog.Infof(s.ctx, "[React.WaitAgent] 等待超时收场: runId=%s, pending=%d", s.runID, pendingCount)
			break
		}
		sleep := waitAgentPollInterval
		if remaining < sleep {
			sleep = remaining
		}
		select {
		case <-time.After(sleep):
		case <-s.runCtx.Done():
			// 父 run 取消/断连：与阻塞委派同构，交由 classifyToolInterruption 生成中断终态。
			return "", true, context.Cause(s.runCtx)
		}
	}
	payload, _ := json.Marshal(map[string]interface{}{"runs": entries})
	zlog.Infof(s.ctx, "[React.WaitAgent] 等待结束: runId=%s, targets=%d, pending=%d", s.runID, len(runIDs), pendingCount)
	return string(payload), false, nil
}

// collectWaitAgentSnapshot 采集一轮快照：逐 run 查最新状态，未在本 run 名下的 run 记 not_found。
// 返回快照条目与仍在运行（值得继续等）的数量——waiting/not_found/终态都不算 pending。
func (s *reactEngineState) collectWaitAgentSnapshot(runIDs []string) ([]waitAgentEntry, int) {
	entries := make([]waitAgentEntry, 0, len(runIDs))
	pending := 0
	for _, runID := range runIDs {
		run, err := model.GetReactRunByRunID(s.ctx, runID)
		if err != nil {
			zlog.Warnf(s.ctx, "[React.WaitAgent] 查询子 run 失败: runId=%s, target=%s, err=%v", s.runID, runID, err)
			entry := waitAgentEntry{RunID: runID, Status: subRunStatusNotFound, ErrorMessage: "查询子 run 状态失败"}
			entries = append(entries, entry)
			continue
		}
		entry, isPending := waitAgentEntryForRun(run, runID, s.runID, s.ctx)
		if isPending {
			pending++
		}
		entries = append(entries, entry)
	}
	return entries, pending
}

// waitAgentEntryForRun 由一行子 run 记录计算快照条目（纯函数，便于单测覆盖授权/终态/waiting 语义）：
//   - run 为 nil 或 parent_run_id 不是当前 run（跨 run/跨会话）→ not_found，防窥探；
//   - 终态 → completed（附 finalResponse）/ error / timeout / cancelled / expired（附 errorMessage）；
//   - waiting_*（HITL 等用户输入）→ waiting，不算 pending（立即随快照返回，由父模型决定催办或放弃）；
//   - running/cancelling → running 且 pending=true（值得继续等）。
func waitAgentEntryForRun(run *model.ReactRun, runID, parentRunID string, ctx *gin.Context) (waitAgentEntry, bool) {
	entry := waitAgentEntry{RunID: runID, Status: subRunStatusNotFound}
	if run == nil || run.ParentRunID != parentRunID {
		return entry, false
	}
	entry.AgentKey = agentKeyFromAgentPath(run.AgentPath)
	status, terminal, waiting := subRunStatusMapping(run.State)
	entry.Status = status
	switch {
	case terminal:
		if status == subRunStatusCompleted {
			if final, err := subAgentFinalResponse(ctx, runID); err == nil {
				entry.FinalResponse = final
			} else {
				entry.ErrorMessage = "未产生最终回复"
			}
		} else {
			entry.ErrorMessage = firstNonEmptyString(run.ErrorMessage, "子 run 以 "+status+" 终止")
		}
	case waiting:
		entry.ErrorMessage = "子 run 正在等待用户输入（ask_question/确认），可用 send_message 催办或放弃"
		return entry, false
	default:
		return entry, true
	}
	return entry, false
}

// agentKeyFromAgentPath 从 agentPath（如 main/ops-agent 或 main/a/b）取本 run 的 agentKey（末段）。
func agentKeyFromAgentPath(agentPath string) string {
	agentPath = strings.TrimSpace(agentPath)
	if idx := strings.LastIndex(agentPath, "/"); idx >= 0 {
		return agentPath[idx+1:]
	}
	return agentPath
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// subRunFinalStatus 取一个已收敛子 run 的对外状态词（delegate_agent 返回值用）：
// run 不存在或状态未知时回退 completed（历史成功路径的兼容口径）。
func subRunFinalStatus(run *model.ReactRun) string {
	if run == nil {
		return subRunStatusCompleted
	}
	status, terminal, _ := subRunStatusMapping(run.State)
	if terminal {
		return status
	}
	return subRunStatusCompleted
}
