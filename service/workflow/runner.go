package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"react-base-service/components/metrics"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"
	"react-base-service/service/react"

	"react-base-service/components/params"

	"github.com/gin-gonic/gin"
)

// executeWorkflowRun 执行一次无人值守 run（与 memory_reflection 同构的 headless 链路）：
// 置 running → 定时上下文内跑完整 ReAct run → 终态映射/判级 → 更新历史行 → webhook 通知。
// 入参 wf 是触发时的定义快照；定义变更经 SyncRegister 重注册后拿到新快照。
func executeWorkflowRun(wf *model.Workflow, run *model.WorkflowRun) {
	ctx := context.Background()
	cfg := conf.GetWorkflowConfig()
	startedAt := time.Now()
	if err := model.UpdateWorkflowRunByID(ctx, run.ID, map[string]any{
		"status":     model.WorkflowRunStatusRunning,
		"started_at": startedAt,
	}); err != nil {
		zlog.Errorf(nil, "[workflow.runner] 置 running 失败(继续执行): runId=%d err=%v", run.ID, err)
	}
	metrics.WorkflowRunning.Inc()

	// wall-clock 超时与 react 引擎内的注册/停机机制双保险（方案 §5.2 第 4 点）。
	timeoutSec := wf.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = cfg.DefaultTimeoutSec
	}
	runCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	ginCtx := react.NewHeadlessGinContext(wf.UserName)
	payload := params.ReactRunPayload{
		CallerKey:    wf.CallerKey,
		RouteValues:  parseRouteValues(wf.RouteValues),
		Type:         model.ReactSessionTypeScheduled,
		UserPrompt:   renderWorkflowPrompt(wf),
		SessionTitle: buildWorkflowSessionTitle(wf, run),
		ModelKey:     wf.ModelKey,
		ModelVersion: wf.ModelVersion,
		MaxSteps:     wf.MaxSteps,
		ControlContext: mustJSON(map[string]any{
			"source":        "workflow",
			"workflowRunId": run.ID,
		}),
	}
	result, err := react.RunWithClientReaderContext(ginCtx, runCtx, payload, "", react.HeadlessEventWriter(ginCtx), nil)

	// 终态映射：nil→completed；IsReactRunCancelled→cancelled（区分停机）；
	// DeadlineExceeded/IsReactRunTimeout→timeout；其他→failed（方案 §5.2）。
	status, errMsg := resolveTerminalStatus(ginCtx, err, result)
	summary, riskLevel := summarizeRunResult(ginCtx, result, wf)

	finishedAt := time.Now()
	update := map[string]any{
		"status":      status,
		"summary":     summary,
		"risk_level":  riskLevel,
		"error_msg":   errMsg,
		"finished_at": finishedAt,
	}
	if result != nil {
		update["session_id"] = result.SessionID
		update["run_id"] = result.RunID
	}
	if err := model.UpdateWorkflowRunByID(ctx, run.ID, update); err != nil {
		zlog.Errorf(nil, "[workflow.runner] 回写终态失败: runId=%d err=%v", run.ID, err)
	}

	metrics.WorkflowTriggerTotal.WithLabelValues(wf.WorkflowKey, status).Inc()
	metrics.WorkflowRunDuration.WithLabelValues(wf.WorkflowKey).Observe(finishedAt.Sub(startedAt).Seconds())
	zlog.Infof(ginCtx, "[workflow.runner] run 终态: key=%s runId=%d status=%s risk=%s cost=%ds",
		wf.WorkflowKey, run.ID, status, riskLevel, int(finishedAt.Sub(startedAt).Seconds()))

	notifyWorkflowResult(ginCtx, wf, run, status, riskLevel, summary, result)
}

// resolveTerminalStatus 把 run 返回值映射为工作流终态（status, errMsg）。
func resolveTerminalStatus(ctx *gin.Context, err error, result *react.RunResult) (string, string) {
	if err == nil {
		// 方案 §5.2 第 3 点兜底：run 若意外进入等待人工输入状态（配置错误），
		// 调度侧视为异常终态 failed，识别而非傻等超时。
		if result != nil && result.RunID != "" {
			runRow, rerr := model.GetReactRunByRunID(ctx, result.RunID)
			if rerr == nil && runRow != nil {
				switch runRow.State {
				case model.ReactRunStateWaitingClientMessage, model.ReactRunStateWaitingPlan:
					return model.WorkflowRunStatusFailed,
						"run 进入等待人工输入状态（scheduled 档案应禁用交互类工具），请检查 caller 工具配置"
				}
			}
		}
		return model.WorkflowRunStatusCompleted, ""
	}
	switch {
	case react.IsReactRunCancelled(err):
		// 停机/取消属正常收敛，不作为错误文案。
		return model.WorkflowRunStatusCancelled, ""
	case react.IsReactRunTimeout(err) || errors.Is(err, context.DeadlineExceeded):
		return model.WorkflowRunStatusTimeout, truncateErrText(err.Error())
	default:
		return model.WorkflowRunStatusFailed, truncateErrText(err.Error())
	}
}

// summarizeRunResult 提取 run 结论快照（最后一条 assistant 消息）并按 risk_patterns 判级。
func summarizeRunResult(ctx *gin.Context, result *react.RunResult, wf *model.Workflow) (string, string) {
	if result == nil || result.RunID == "" {
		return "", model.WorkflowRiskLevelNone
	}
	messages, err := model.GetReactMessagesByRunID(ctx, result.RunID)
	if err != nil {
		zlog.Warnf(ctx, "[workflow.runner] 读取 run 消息失败: runId=%s err=%v", result.RunID, err)
		return "", model.WorkflowRiskLevelNone
	}
	summary := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != model.ReactMessageRoleAssistant ||
			messages[i].MessageType != model.ReactMessageTypeAssistant {
			continue
		}
		summary = extractAssistantText(messages[i].ContentJSON)
		break
	}
	return summary, evaluateRiskLevel(summary, wf.RiskPatterns)
}

// extractAssistantText 从落库的 ContentJSON（{"modelMessage":{...}} 信封）提取正文文本。
func extractAssistantText(contentJSON string) string {
	if strings.TrimSpace(contentJSON) == "" {
		return ""
	}
	var envelope struct {
		ModelMessage struct {
			Content string `json:"content"`
			Parts   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"modelMessage"`
	}
	if err := json.Unmarshal([]byte(contentJSON), &envelope); err != nil {
		return ""
	}
	if text := strings.TrimSpace(envelope.ModelMessage.Content); text != "" {
		return text
	}
	var sb strings.Builder
	for _, part := range envelope.ModelMessage.Parts {
		if part.Type == "text" && part.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}

// evaluateRiskLevel 一期规则判级：summary 命中任一 risk_patterns 正则即 high。
func evaluateRiskLevel(summary, riskPatterns string) string {
	if strings.TrimSpace(summary) == "" || strings.TrimSpace(riskPatterns) == "" {
		return model.WorkflowRiskLevelNone
	}
	for _, line := range strings.Split(riskPatterns, "\n") {
		pattern := strings.TrimSpace(line)
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			// 保存时已校验；运行期仍遇非法表达式时跳过该行，不让判级中断收尾。
			continue
		}
		if re.MatchString(summary) {
			return model.WorkflowRiskLevelHigh
		}
	}
	return model.WorkflowRiskLevelNone
}

// buildWorkflowSessionTitle 会话标题 =「{workflow 名} {触发时间}」（方案 §5.2 第 1 点）。
func buildWorkflowSessionTitle(wf *model.Workflow, run *model.WorkflowRun) string {
	fireAt := time.Now()
	if run.PlannedFireAt != nil {
		fireAt = *run.PlannedFireAt
	}
	return fmt.Sprintf("%s %s", wf.Name, fireAt.Format("01-02 15:04"))
}

// parseRouteValues 解析定义里的 route_values JSON 数组；非法时回退空。
func parseRouteValues(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	values := make([]string, 0, 4)
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil
	}
	return values
}

// mustJSON 序列化运行控制上下文；marshal map[string]any 不会失败，忽略错误仅作兜底。
func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}

// truncateErrText 限制错误文案长度，与 error_msg 列宽（text）和通知可读性对齐。
func truncateErrText(text string) string {
	const maxLen = 900
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "…(截断)"
	}
	return string(runes)
}
