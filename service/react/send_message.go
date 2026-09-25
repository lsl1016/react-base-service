package react

// A2 SendMessage：父 Agent → 子代理的双向消息通道
// （docs/plan/20260925_Session与Steering机制借鉴方案.md §4.3，对齐 ZCode messageSink——
// 父→子自由文本 = 向子 run 的 pendingInputs 注入 guide，完全复用 S1 的 Steering 机制）。
//
// 纪律：
//   - 只允许向本 run 直接委派的子 run（parent_run_id = 当前 run）发送；
//   - 子 run 活跃（running/waiting_*）→ 账本落 admitted guide 行，子引擎在下一个模型步
//     边界经既有 drainPendingGuideBoundary 消费（无需内存通道）；
//   - 子 run 终态 → 明确报错回给父模型（远期再支持按 agentPath 历史重建续聊 run）；
//   - message 是子代理视角的自包含输入：子代理看不到父对话，需写清背景与期望。

import (
	"encoding/json"
	"fmt"
	"strings"

	llm "react-base-service/api/llm"
	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
)

// sendMessageToolDefinition 构造 send_message 工具声明。与 delegate_agent 同门控
// （profile.AllowSubagent，见 runtimeToolDefinitions / allowsInternalTool）。
func sendMessageToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: metaToolSendMessage,
		Description: "向已委派的后台/进行中的子 Agent 发送一条补充消息（例如补充关键信息、纠偏、追加要求）。" +
			"子 Agent 看不到当前对话，消息必须自包含；消息会作为引导注入子 Agent 的下一个模型轮。" +
			"仅支持本运行内委派出的子 Agent（按 agent_path 或 runId 定位）。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent_path": stringSchema("目标子 Agent 的 agent_path（如 main/ops-agent），与 runId 二选一；同一路径多次委派时取最新的活跃子 run。"),
				"run_id":     stringSchema("目标子 run 的 runId（delegate_agent 返回的 runId），与 agent_path 二选一。"),
				"message":    stringSchema("发给子 Agent 的完整消息。自包含：写明补充什么信息、对既有任务做何调整。"),
			},
			"required":             []string{"message"},
			"additionalProperties": false,
		},
	}
}

type sendMessageInput struct {
	AgentPath string `json:"agent_path"`
	RunID     string `json:"run_id"`
	Message   string `json:"message"`
}

// sendMessageActiveRunStates 是可接收消息的子 run 状态集合。
func sendMessageActiveRunStates() map[string]bool {
	return map[string]bool{
		model.ReactRunStateRunning:              true,
		model.ReactRunStateWaitingClientMessage: true,
		model.ReactRunStateWaitingPlan:          true,
	}
}

// chooseSendMessageTarget 从候选子 run（创建序，升序）中选择消息投递目标（纯函数）：
// 优先活跃 run（同路径多委派时自然取最新活跃）；无活跃 run 时返回最新的终态 run
//（供上层给出明确的"已结束"报错）。cancelling 不算可投递（取消收尾中的 run 不再接受新输入）。
func chooseSendMessageTarget(runs []model.ReactRun) *model.ReactRun {
	var active, latest *model.ReactRun
	for i := range runs {
		run := &runs[i]
		latest = run
		if sendMessageActiveRunStates()[run.State] {
			active = run
		}
	}
	if active != nil {
		return active
	}
	return latest
}

// executeSendMessage 执行 send_message：定位本 run 委派出的目标子 run，
// 活跃则把 message 作为 guide 落账本（复用 S1 消费机制），终态则明确报错。
func (s *reactEngineState) executeSendMessage(call llm.ToolCall, step int) (string, bool, error) {
	var input sendMessageInput
	if err := json.Unmarshal(call.Input, &input); err != nil {
		return "send_message input must be a valid JSON object", true, nil
	}
	input.AgentPath = strings.TrimSpace(input.AgentPath)
	input.RunID = strings.TrimSpace(input.RunID)
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" {
		return "send_message requires message", true, nil
	}
	if input.AgentPath == "" && input.RunID == "" {
		return "send_message requires agent_path or run_id", true, nil
	}

	var candidates []model.ReactRun
	if input.RunID != "" {
		run, err := model.GetReactRunByRunID(s.ctx, input.RunID)
		if err != nil {
			return "", true, err
		}
		if run != nil {
			candidates = append(candidates, *run)
		}
	} else {
		runs, err := model.GetReactRunsByParentRunIDWithDB(s.ctx, model.GetLLMDB(), s.runID)
		if err != nil {
			return "", true, err
		}
		for _, run := range runs {
			if run.AgentPath == input.AgentPath {
				candidates = append(candidates, run)
			}
		}
	}
	// 归属校验：候选必须全部是本 run 直接委派的子 run（防跨 run/跨会话寻址）。
	for _, run := range candidates {
		if run.ParentRunID != s.runID || run.SessionID != s.sessionID {
			return fmt.Sprintf("子代理 %s 不属于当前运行，无法发送消息", input.AgentPathOrRunID()), true, nil
		}
	}
	target := chooseSendMessageTarget(candidates)
	if target == nil {
		return fmt.Sprintf("未找到目标子代理（%s）。请确认 agent_path/runId 是否来自本次运行的 delegate_agent 调用。", input.AgentPathOrRunID()), true, nil
	}
	if !sendMessageActiveRunStates()[target.State] {
		return fmt.Sprintf("子代理 %s 已结束（state=%s），无法再发送消息；如需继续该任务请重新委派。", target.AgentPath, target.State), true, nil
	}
	// 软着陆收尾窗口拒收（对齐 S1 纪律）：收尾中的子 run 不再接受新输入。
	if isRunSoftLanding(target.RunID) {
		return fmt.Sprintf("子代理 %s 正在收尾（步数/预算临近耗尽），消息无法注入；如需继续请重新委派并附上该信息。", target.AgentPath), true, nil
	}

	seq, err := model.GetReactPendingInputMaxSeqBySessionIDWithDB(s.ctx, model.GetLLMDB(), s.sessionID)
	if err != nil {
		return "", true, err
	}
	row := &model.ReactPendingInput{
		SessionID: s.sessionID,
		RunID:     target.RunID,
		Kind:      model.ReactPendingKindUserInput,
		Delivery:  model.ReactPendingDeliveryGuide,
		Status:    model.ReactPendingStatusAdmitted,
		Content:   input.Message,
		Seq:       seq + 1,
	}
	if err := model.CreateReactPendingInputWithDB(s.ctx, model.GetLLMDB(), row); err != nil {
		return "", true, err
	}
	// 发送后复核（发送与目标 run 收敛的竞态兜底）：行已被终态结算作废说明消息未送达，
	// 如实回报失败（子 run 的定向消息按 discarded 结算，绝不改投会话队列）。
	latest, err := model.GetReactPendingInputByIDWithDB(s.ctx, model.GetLLMDB(), row.ID)
	if err != nil {
		return "", true, err
	}
	if latest != nil && latest.Status == model.ReactPendingStatusDiscarded {
		zlog.Infof(s.ctx, "[React.SendMessage] 子代理在消息注入瞬间收敛，消息已作废: parentRun=%s, subRun=%s, pendingInputId=%s", s.runID, target.RunID, pendingInputID(row.ID))
		return fmt.Sprintf("子代理 %s 恰好在消息送达前完成，消息未能消费（已在账本作废）；可把该信息转告用户或重新委派。", target.AgentPath), true, nil
	}
	zlog.Infof(s.ctx, "[React.SendMessage] 消息已注入子代理: parentRun=%s, subRun=%s, agentPath=%s, pendingInputId=%s, chars=%d",
		s.runID, target.RunID, target.AgentPath, pendingInputID(row.ID), len([]rune(input.Message)))
	return fmt.Sprintf("消息已作为引导注入子代理 %s（runId=%s）的下一个模型轮，它会在当前步骤边界读到并继续任务。", target.AgentPath, target.RunID), false, nil
}

// AgentPathOrRunID 是报错文案用的目标描述。
func (i sendMessageInput) AgentPathOrRunID() string {
	if i.AgentPath != "" {
		return i.AgentPath
	}
	return i.RunID
}
