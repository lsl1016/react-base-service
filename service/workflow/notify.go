package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"react-base-service/conf"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"
	"react-base-service/service/react"
)

// notifyWorkflowResult 在 run 终态后发送 webhook 通知（企业微信/飞书通用格式，按 URL 域名识别）。
//
// 通知决策（方案 §5.3）：
//   - cancelled 不通知（停机收敛，非业务结果）
//   - completed + risk=none 受 notify_only_on_risk 控制（可只告警不报平安）
//   - completed + risk=high、failed、timeout 恒通知
//
// 失败只记日志不重试：通知是旁路，重试风暴比丢一条通知危害更大。
func notifyWorkflowResult(ctx context.Context, wf *model.Workflow, run *model.WorkflowRun, status, riskLevel, summary string, result *react.RunResult) {
	webhookURL := strings.TrimSpace(wf.NotifyWebhookURL)
	if webhookURL == "" {
		return
	}
	if status == model.WorkflowRunStatusCancelled {
		return
	}
	cfg := conf.GetWorkflowConfig()
	if status == model.WorkflowRunStatusCompleted &&
		riskLevel == model.WorkflowRiskLevelNone && cfg.NotifyOnlyOnRisk {
		return
	}

	content := buildNotifyMarkdown(wf, run, status, riskLevel, summary, result)
	payload := buildWebhookPayload(webhookURL, content)
	if payload == nil {
		zlog.Warnf(ctx, "[workflow.notify] 无法识别的 webhook 地址(跳过通知): key=%s url=%s", wf.WorkflowKey, webhookURL)
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		zlog.Errorf(ctx, "[workflow.notify] 构造通知请求失败: key=%s err=%v", wf.WorkflowKey, err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: notifyTimeout}
	resp, err := client.Do(request)
	if err != nil {
		zlog.Errorf(ctx, "[workflow.notify] 通知发送失败(不重试): key=%s err=%v", wf.WorkflowKey, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		zlog.Errorf(ctx, "[workflow.notify] 通知响应异常(不重试): key=%s status=%d", wf.WorkflowKey, resp.StatusCode)
	}
}

// buildNotifyMarkdown 渲染通知正文（企业微信 markdown / 飞书 text 通用文案）。
func buildNotifyMarkdown(wf *model.Workflow, run *model.WorkflowRun, status, riskLevel, summary string, result *react.RunResult) string {
	var sb strings.Builder
	sb.WriteString("**【定时工作流】" + wf.Name + "**\n")
	sb.WriteString("- 状态：" + workflowStatusText(status))
	if riskLevel == model.WorkflowRiskLevelHigh {
		sb.WriteString("（风险级别：**high**，结论命中判级规则）")
	}
	sb.WriteString("\n")
	if run.PlannedFireAt != nil {
		sb.WriteString("- 计划触发：" + run.PlannedFireAt.Format("2006-01-02 15:04:05") + "\n")
	}
	switch status {
	case model.WorkflowRunStatusCompleted:
		firstLine := firstSummaryLine(summary)
		if firstLine != "" {
			sb.WriteString("- 结论：" + firstLine + "\n")
		}
	case model.WorkflowRunStatusTimeout, model.WorkflowRunStatusFailed:
		sb.WriteString("- 错误：" + firstSummaryLine(run.ErrorMsg) + "\n")
	}
	if result != nil && result.SessionID != "" {
		sb.WriteString("- 回放：" + buildReplayReference(result.SessionID) + "\n")
	}
	return sb.String()
}

// buildWebhookPayload 按 webhook 域名识别 IM 格式：
// open.feishu.cn → 飞书自定义机器人（text）；其余（qyapi.weixin.qq.com 等）→ 企业微信（markdown）。
func buildWebhookPayload(webhookURL, content string) map[string]any {
	parsed, err := url.Parse(webhookURL)
	if err != nil || parsed.Host == "" {
		return nil
	}
	if strings.Contains(parsed.Host, "feishu.cn") {
		return map[string]any{
			"msg_type": "text",
			"content":  map[string]any{"text": content},
		}
	}
	return map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
}

// buildReplayReference 渲染回放入口：配置了 replay_base_url 时拼绝对链接，否则给会话 ID。
func buildReplayReference(sessionID string) string {
	base := strings.TrimRight(strings.TrimSpace(conf.CustomConf.LLM.Workflow.ReplayBaseURL), "/")
	if base == "" {
		return sessionID
	}
	return base + "/react-base-service/react/replay?sessionId=" + url.QueryEscape(sessionID)
}

// workflowStatusText 渲染状态中文文案（通知面向运维读者）。
func workflowStatusText(status string) string {
	switch status {
	case model.WorkflowRunStatusCompleted:
		return "已完成"
	case model.WorkflowRunStatusFailed:
		return "失败"
	case model.WorkflowRunStatusTimeout:
		return "超时"
	default:
		return status
	}
}

// firstSummaryLine 取文本首行并截断，保证通知卡片可读。
func firstSummaryLine(text string) string {
	text = strings.TrimSpace(text)
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		text = text[:idx]
	}
	runes := []rune(text)
	if len(runes) > notifySummaryLineLimit {
		return string(runes[:notifySummaryLineLimit]) + "…"
	}
	return text
}

const (
	// notifyTimeout 是 webhook 发送超时（旁路通知不阻塞收尾太久）。
	notifyTimeout = 5 * time.Second
	// notifySummaryLineLimit 是通知里结论/错误单行截断长度。
	notifySummaryLineLimit = 120
)
