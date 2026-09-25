package react

// 工具级危险操作确认（P2-3，方案 §4.2.3）：
//
//   - tblLlmTool.permission_mode：auto=自动执行（默认，行为与历史一致）、confirm=每次人工确认、
//     confirm_risky=入参/工具名命中风险正则才确认；
//   - 确认流复用 ask_question 式等待通道：工具执行前发 tool_confirm_request 事件（带 agentPath），
//     run 进入 waiting_client_message，经 clientHub 等前端上行 tool_confirm_answer
//     （并行委派下多个确认并发等待，按 toolUseId 路由互不干扰）；
//   - 允许 → 继续执行；拒绝 → 回填「用户拒绝」工具结果（OH UserRejectObservation 对应物），
//     工具卡片收敛为 rejected 终态；
//   - agent 级 permission_mode（tblLlmAgent）是子 run 内全部工具的下限：inherit=不收紧（默认）、
//     confirm/confirm_risky 与工具级取更严者；
//   - 仅拦截服务端执行通道（http/mcp）：client 工具由前端执行，展示前可自行确认；
//   - 不引入 LLM 自评 security_risk——规则表（内置默认 + config.riskPatterns 自定义）更适合运维域。

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	toolService "react-base-service/service/tool"

	"react-base-service/golib/zlog"
)

const (
	EventToolConfirmRequest = "tool_confirm_request"
	EventToolConfirmAnswer  = "tool_confirm_answer"
)

const toolExecutionStatusRejected = "rejected"

// defaultToolRiskPatterns 是 confirm_risky 模式的内置风险正则（大小写不敏感，
// 匹配目标为工具名 + 序列化入参）；工具 config.riskPatterns 非空时取而代之。
var defaultToolRiskPatterns = []string{
	`(?i)\bdrop\s+(table|database|index)\b`,
	`(?i)\balter\s+table\b`,
	`(?i)\btruncate\s+(table)?\b`,
	`(?i)\bdelete\s+from\b`,
	`(?i)\bupdate\s+\S+\s+set\b`,
	`(?i)\bkill\b`,
	`(?i)\bshutdown\b`,
	`(?i)\brestart\b`,
	`(?i)\btruncate\s+table\b`,
}

// toolConfirmDecision 是一次确认门判定的结论。
type toolConfirmDecision struct {
	NeedConfirm bool
	Mode        string
	Reason      string
}

// resolveEffectiveToolPermission 取工具级与 agent 级权限模式的更严者：
// agent 级（子 run 继承自 tblLlmAgent.permission_mode）只能收紧不能放宽；
// 未知值（含 agent 的 inherit）按 auto 档处理。
func resolveEffectiveToolPermission(toolMode, agentMode string) string {
	rank := func(mode string) int {
		switch strings.TrimSpace(mode) {
		case toolService.ToolPermissionConfirm:
			return 2
		case toolService.ToolPermissionConfirmRisky:
			return 1
		default:
			return 0
		}
	}
	toolMode = strings.TrimSpace(toolMode)
	agentMode = strings.TrimSpace(agentMode)
	// 未知值归一为 auto（配置错误的模式不产生拦截，fail-open 并由管理接口校验兜底）。
	normalize := func(mode string) string {
		switch mode {
		case toolService.ToolPermissionConfirm, toolService.ToolPermissionConfirmRisky:
			return mode
		default:
			return toolService.ToolPermissionAuto
		}
	}
	toolMode = normalize(toolMode)
	agentMode = normalize(agentMode)
	if rank(agentMode) > rank(toolMode) {
		return agentMode
	}
	return toolMode
}

// shouldConfirmServerTool 判定一次服务端工具执行是否需要人工确认。
// 匹配目标是工具名 + 序列化入参（运维域 SQL/命令通常在参数里）；正则异常按未命中处理并记日志。
func shouldConfirmServerTool(tool model.Tool, input json.RawMessage, agentMode string) toolConfirmDecision {
	mode := resolveEffectiveToolPermission(tool.PermissionMode, agentMode)
	switch mode {
	case toolService.ToolPermissionConfirm:
		return toolConfirmDecision{NeedConfirm: true, Mode: mode, Reason: "工具配置为 confirm 模式，每次执行需人工确认"}
	case toolService.ToolPermissionConfirmRisky:
		patterns := defaultToolRiskPatterns
		if cfg, err := toolService.ParseToolConfig(tool.Config); err == nil && len(cfg.RiskPatterns) > 0 {
			patterns = cfg.RiskPatterns
		}
		// 工具名的下划线归一为空格：\b 词边界对 snake_case（kill_query_tool）永不生效。
		target := strings.ReplaceAll(tool.Name, "_", " ") + "\n" + string(input)
		for _, pattern := range patterns {
			matched, err := regexp.MatchString(pattern, target)
			if err != nil {
				zlog.Warnf(nil, "[React.ToolConfirm] 风险正则非法(按未命中处理): tool=%s, pattern=%s, err=%v", tool.Name, pattern, err)
				continue
			}
			if matched {
				return toolConfirmDecision{NeedConfirm: true, Mode: mode, Reason: fmt.Sprintf("入参命中风险正则 %s，需人工确认", pattern)}
			}
		}
	}
	return toolConfirmDecision{Mode: mode}
}

// confirmServerToolIfNeeded 是服务端工具执行的确认门：需要确认时发 tool_confirm_request 并阻塞等待
// 前端 tool_confirm_answer。返回 approved=false 表示不执行（调用方回填拒绝/超时结果，rejectReason
// 说明具体原因）；err 仅在取消/断连等中断时非 nil。
func (s *reactEngineState) confirmServerToolIfNeeded(call llm.ToolCall, tool model.Tool, input json.RawMessage, step int, startedAt time.Time) (approved bool, rejectReason string, err error) {
	decision := shouldConfirmServerTool(tool, input, s.agentPermissionMode)
	if !decision.NeedConfirm {
		return true, "", nil
	}

	_ = s.emitter.EmitStep(step, EventToolConfirmRequest, params.ReactToolConfirmRequestPayload{
		ToolUseID: call.ID,
		ToolName:  tool.Name,
		ToolInput: input,
		Mode:      decision.Mode,
		Reason:    decision.Reason,
	})
	pendingJSON, _ := json.Marshal([]string{call.ID})
	if err := model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{"state": model.ReactRunStateWaitingClientMessage, "pending_tool_use_ids": string(pendingJSON)}); err != nil {
		return false, "", err
	}

	answer, waitErr := s.waitClientMessage(func(m params.ReactWSMessage) bool {
		return m.Type == EventToolConfirmAnswer && toolConfirmAnswerID(m) == call.ID
	})
	if !IsReactRunCancelled(waitErr) {
		_ = model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{"state": model.ReactRunStateRunning, "pending_tool_use_ids": "[]"})
	}
	if waitErr != nil {
		if IsErrInteractionTimeout(waitErr) {
			// 交互等待超时按未授权处理：拒绝结果回灌模型继续循环，不终止 run。
			seconds := conf.GetReactRuntimeConfig().Loop.InteractionTimeoutSec
			return false, fmt.Sprintf("确认等待超时（%d 秒），用户未响应", seconds), nil
		}
		if IsReactRunCancelled(waitErr) || IsReactClientDisconnected(waitErr) {
			// 等待确认被中断：登记 rejected 终态，由引擎层本轮收口统一落库，让卡片收敛、悬空 tool_use 有配对。
			s.recordToolConfirmInterrupted(call, tool, step, startedAt, waitErr)
		}
		return false, "", waitErr
	}
	switch answer.Type {
	case EventCancel:
		s.recordToolConfirmInterrupted(call, tool, step, startedAt, ErrReactRunCancelled)
		return false, "", ErrReactRunCancelled
	case EventToolConfirmAnswer:
		var payload toolConfirmAnswerPayload
		if err := json.Unmarshal(answer.Payload, &payload); err != nil {
			return false, "", fmt.Errorf("tool_confirm_answer payload invalid: %w", err)
		}
		if payload.ToolUseID != call.ID {
			return false, "", fmt.Errorf("tool_confirm_answer missing: %s", call.ID)
		}
		if payload.Approved {
			zlog.Infof(s.ctx, "[React.ToolConfirm] 用户允许执行: runId=%s, tool=%s, toolUseId=%s", s.runID, tool.Name, call.ID)
			return true, "", nil
		}
		zlog.Infof(s.ctx, "[React.ToolConfirm] 用户拒绝执行: runId=%s, tool=%s, toolUseId=%s, reason=%s", s.runID, tool.Name, call.ID, payload.Reason)
		return false, payload.Reason, nil
	default:
		return false, "", fmt.Errorf("unexpected message while waiting tool_confirm_answer: %s", answer.Type)
	}
}

// toolConfirmAnswerPayload 是上行 tool_confirm_answer 的信封。
type toolConfirmAnswerPayload struct {
	ToolUseID string `json:"toolUseId"`
	Approved  bool   `json:"approved"`
	Reason    string `json:"reason,omitempty"`
}

// toolConfirmAnswerID 解析 tool_confirm_answer 信封里的 toolUseId（hub 匹配键）。
func toolConfirmAnswerID(msg params.ReactWSMessage) string {
	if msg.Type != EventToolConfirmAnswer {
		return ""
	}
	var payload toolConfirmAnswerPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return ""
	}
	return payload.ToolUseID
}

// renderToolRejectedResult 生成「用户拒绝」的工具结果内容（模型与前端共用一份语义）。
func renderToolRejectedResult(tool model.Tool, reason string) string {
	note := strings.TrimSpace(reason)
	if note == "" {
		note = "用户拒绝了本次执行"
	}
	return fmt.Sprintf("用户拒绝执行工具 %s：%s。请勿重试同一操作，改为说明需要用户授权后等待，或换用只读方式继续。", tool.Name, note)
}

// recordToolConfirmInterrupted 在等待确认被取消/断线时登记终态（tool_use_end(rejected) +
// 中断 tool_result 交由引擎层本轮收口统一落库），语义与 ask_question 中断路径一致。
func (s *reactEngineState) recordToolConfirmInterrupted(call llm.ToolCall, tool model.Tool, step int, startedAt time.Time, waitErr error) {
	status, _, interrupted := classifyToolInterruption(s.runCtx, waitErr)
	if !interrupted {
		return
	}
	content := "用户取消了本次运行，工具未执行。"
	if status == toolExecutionStatusError {
		content = "连接断开，工具确认未完成，工具未执行。"
	}
	normalized := normalizeToolResult(call.ID, content, true, executedByServer)
	normalized.Status = toolExecutionStatusRejected
	s.recordInterruptedToolResult(llm.ToolResultContent{ToolUseID: call.ID, Content: normalized.LLMContent(), IsError: true})
	if status == toolExecutionStatusCancelled {
		_ = s.emitter.EmitStep(step, EventToolUseEnd, params.ReactToolUseEndPayload{
			ToolUseID: call.ID, Content: normalized.Content, IsError: true,
			ExecutedBy: executedByServer, Status: toolExecutionStatusRejected, DurationMs: time.Since(startedAt).Milliseconds(),
		})
	}
}
