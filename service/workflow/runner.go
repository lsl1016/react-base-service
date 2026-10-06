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
	llm "react-base-service/api/llm"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"
	"react-base-service/service/apikey"
	"react-base-service/service/judge"
	llmmodelService "react-base-service/service/llmmodel"
	"react-base-service/service/react"

	"react-base-service/components/params"

	"github.com/gin-gonic/gin"
)
// workflowSystemUser 是 workflow 未配置 user_name 时无人值守 run 的兜底身份。
// react 引擎要求 run 必须归属用户（审计/工具白名单都按用户解析），不支持匿名 run；
// 方案里"空则匿名"落地为固定系统账号。
const workflowSystemUser = "workflow"

// resolveWorkflowUserName 解析 run 身份：定义显式配置优先，空回退系统账号。
func resolveWorkflowUserName(wf *model.Workflow) string {
	if name := strings.TrimSpace(wf.UserName); name != "" {
		return name
	}
	return workflowSystemUser
}

// executeWorkflowRun 执行一次无人值守 run（与 memory_reflection 同构的 headless 链路）：
// 置 running → 定时上下文内跑完整 ReAct run（completed 后裁判外环可自动追问续跑）→
// 终态映射/语义+规则双判级 → 更新历史行 → 连环失败熔断检查 → webhook 通知。
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

	// wall-clock 超时与 react 引擎内的注册/停机机制双保险（方案 §5.2 第 4 点）；
	// 追问轮共享同一超时预算——外环不放宽总时限。
	timeoutSec := wf.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = cfg.DefaultTimeoutSec
	}
	runCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	ginCtx := react.NewHeadlessGinContext(resolveWorkflowUserName(wf))
	payload := params.ReactRunPayload{
		CallerKey:    wf.CallerKey,
		RouteValues:  parseRouteValues(wf.RouteValues),
		Type:         model.ReactSessionTypeScheduled,
		UserPrompt:   renderWorkflowPrompt(wf),
		SessionTitle: buildWorkflowSessionTitle(wf, run),
		ModelKey:     resolveWorkflowModelKey(wf),
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

	// 裁判外环（二期）：completed 后语义判定目标是否达成，未达成自动追问续跑（上限 N 轮）；
	// 判定器故障 fail-open——降级回规则判级，绝不阻断收尾。
	var verdict *judge.Verdict
	if status == model.WorkflowRunStatusCompleted && result != nil {
		verdict, result, status, errMsg = runJudgeLoop(ginCtx, runCtx, wf, payload, result, status, errMsg)
	}

	summary := summarizeRunResult(ginCtx, result)
	// 双判级：裁判语义级别优先，裁判未生效时回退 risk_patterns 关键词规则。
	riskLevel := evaluateRiskLevel(summary, wf.RiskPatterns)
	if verdict != nil {
		riskLevel = judgeRiskToWorkflowRisk(verdict.RiskLevel, riskLevel)
	}
	artifact := findSessionReportArtifact(ginCtx, result)

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
	zlog.Infof(ginCtx, "[workflow.runner] run 终态: key=%s runId=%d status=%s risk=%s judged=%v cost=%ds",
		wf.WorkflowKey, run.ID, status, riskLevel, verdict != nil, int(finishedAt.Sub(startedAt).Seconds()))

	outcome := runOutcome{
		Status: status, RiskLevel: riskLevel, Summary: summary,
		Verdict: verdict, Artifact: artifact,
	}
	// 连环失败熔断（二期 §5.6）：连续 N 次 failed/timeout 自动停用并通知，防故障风暴。
	if status == model.WorkflowRunStatusFailed || status == model.WorkflowRunStatusTimeout {
		outcome.CircuitBreakerTripped = maybeTripFailureBreaker(ginCtx, wf, status)
	}
	notifyWorkflowResult(ginCtx, wf, run, outcome, result)
}

// runJudgeLoop 是裁判外环主循环：Review → 未达成则注入追问续跑同一会话 → 再 Review。
// 返回最终判定与（可能被追问轮更新的）run 结果/终态；任何判定器故障 fail-open 返回 nil 判定。
func runJudgeLoop(ctx *gin.Context, runCtx context.Context, wf *model.Workflow, payload params.ReactRunPayload,
	result *react.RunResult, status, errMsg string) (*judge.Verdict, *react.RunResult, string, string) {
	cfg := conf.GetWorkflowConfig()
	j := judge.NewLLMJudge(newJudgeInvoker(ctx, wf), cfg.Judge.TranscriptChars)
	if j == nil {
		return nil, result, status, errMsg
	}
	for round := 0; ; round++ {
		verdict, err := j.Review(ctx, judge.Input{
			Goal:       payload.UserPrompt,
			Transcript: buildJudgeTranscript(ctx, result.SessionID),
		})
		if err != nil {
			// 判定失败降级：保留规则判级路径，不追问、不改终态。
			zlog.Warnf(ctx, "[workflow.judge] 裁判审阅失败(降级规则判级): key=%s round=%d err=%v", wf.WorkflowKey, round, err)
			metrics.WorkflowJudgeTotal.WithLabelValues(wf.WorkflowKey, "error").Inc()
			return nil, result, status, errMsg
		}
		judgeStatus := "unachieved"
		if verdict.Achieved {
			judgeStatus = "achieved"
		}
		metrics.WorkflowJudgeTotal.WithLabelValues(wf.WorkflowKey, judgeStatus).Inc()
		zlog.Infof(ctx, "[workflow.judge] 审阅结论: key=%s round=%d achieved=%v risk=%s reason=%s",
			wf.WorkflowKey, round, verdict.Achieved, verdict.RiskLevel, firstLine(verdict.Reason, 120))

		if verdict.Achieved || round >= cfg.Judge.MaxFollowupRounds || strings.TrimSpace(verdict.FollowUp) == "" {
			return verdict, result, status, errMsg
		}

		// 未达成且有追问建议：同一会话注入追问续跑（历史/上下文沿用，不重头再来）。
		metrics.WorkflowJudgeTotal.WithLabelValues(wf.WorkflowKey, "followup").Inc()
		followUpPayload := payload
		followUpPayload.UserPrompt = renderJudgeFollowUpPrompt(verdict.FollowUp, round+1)
		nextResult, runErr := react.RunWithClientReaderContext(ctx, runCtx, followUpPayload, result.SessionID, react.HeadlessEventWriter(ctx), nil)
		if runErr != nil {
			// 追问轮失败：终态按追问轮重新映射，判定保留供通知展示。
			nextStatus, nextErrMsg := resolveTerminalStatus(ctx, runErr, nextResult)
			return verdict, nextResult, nextStatus, nextErrMsg
		}
		result = nextResult
	}
}

// buildJudgeTranscript 把会话消息转换为裁判通用转录。
// 纯文本之外必须保留工具执行证据：裁判只看文本会误判"未见实际执行"（真机教训）——
// assistant 消息附上 [工具执行] 摘要行（工具名 + 产物清单），供其核对目标是否实质达成。
func buildJudgeTranscript(ctx *gin.Context, sessionID string) []judge.Message {
	messages, err := model.GetReactMessagesBySessionID(ctx, sessionID)
	if err != nil {
		zlog.Warnf(ctx, "[workflow.judge] 读取会话消息失败: session=%s err=%v", sessionID, err)
		return nil
	}
	// 第一遍：toolUseID → 工具名（assistant 的 tool_use parts）。
	toolNames := map[string]string{}
	for i := range messages {
		for _, part := range decodeMessageParts(messages[i].ContentJSON) {
			if part.Type == "tool_use" && part.Name != "" {
				toolNames[part.ID] = part.Name
			}
		}
	}
	// 第二遍：toolUseID → 产物摘要（tool_result content 内层 JSON 的 artifacts）。
	artifactSummaries := map[string]string{}
	for i := range messages {
		msg := &messages[i]
		if msg.Role != model.ReactMessageRoleUser || msg.MessageType != model.ReactMessageTypeToolResult {
			continue
		}
		for _, part := range decodeMessageParts(msg.ContentJSON) {
			if part.Type != "tool_result" {
				continue
			}
			if summary := summarizeToolArtifacts(part.Content); summary != "" {
				artifactSummaries[part.ToolUseID] = summary
			}
		}
	}
	transcript := make([]judge.Message, 0, len(messages))
	for i := range messages {
		msg := &messages[i]
		switch {
		case msg.Role == model.ReactMessageRoleUser && msg.MessageType == model.ReactMessageTypeUserInput:
			transcript = append(transcript, judge.Message{Role: judge.RoleUser, Content: extractUserInputText(msg.ContentJSON)})
		case msg.Role == model.ReactMessageRoleAssistant && msg.MessageType == model.ReactMessageTypeAssistant:
			content := extractAssistantText(msg.ContentJSON)
			// 附工具执行证据：本消息发起的每个 tool_use + 对应产物摘要。
			var evidence strings.Builder
			for _, part := range decodeMessageParts(msg.ContentJSON) {
				if part.Type != "tool_use" || part.Name == "" {
					continue
				}
				if evidence.Len() > 0 {
					evidence.WriteString("；")
				}
				evidence.WriteString(part.Name)
				if summary, ok := artifactSummaries[part.ID]; ok {
					evidence.WriteString(" → " + summary)
				}
			}
			if evidence.Len() > 0 {
				content = strings.TrimSpace(content) + "\n[工具执行记录] " + evidence.String()
			}
			transcript = append(transcript, judge.Message{Role: judge.RoleAssistant, Content: content})
		}
	}
	return transcript
}

// decodeMessageParts 解析落库 ContentJSON 信封里的 modelMessage.parts；解析失败返回空。
func decodeMessageParts(contentJSON string) []llm.ContentPart {
	if strings.TrimSpace(contentJSON) == "" {
		return nil
	}
	var envelope struct {
		ModelMessage struct {
			Parts []llm.ContentPart `json:"parts"`
		} `json:"modelMessage"`
	}
	if err := json.Unmarshal([]byte(contentJSON), &envelope); err != nil {
		return nil
	}
	return envelope.ModelMessage.Parts
}

// summarizeToolArtifacts 从 tool_result 的 content 提取产物清单摘要（名称列表）。
// 落库形态为两层包装：part.Content = {"toolUseId","content":"{\"exitCode\":...,\"artifacts\":[...]}"}
//（内层 content 是字符串化的工具输出）；兼容无包装的直接形态。
func summarizeToolArtifacts(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	var payload struct {
		Artifacts []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"artifacts"`
		// 包装层：真实工具输出在其 content 字符串里。
		InnerContent string `json:"content"`
	}
	candidates := []string{trimmed}
	if start, end := strings.Index(trimmed, "{"), strings.LastIndex(trimmed, "}"); start >= 0 && end > start {
		candidates = append(candidates, trimmed[start:end+1])
	}
	for _, candidate := range candidates {
		if err := json.Unmarshal([]byte(candidate), &payload); err != nil {
			continue
		}
		if names := artifactNames(payload.Artifacts); names != "" {
			return names
		}
		// 外层无 artifacts 时剥离包装层再试。
		if inner := strings.TrimSpace(payload.InnerContent); inner != "" && strings.HasPrefix(inner, "{") {
			payload.InnerContent = ""
			if err := json.Unmarshal([]byte(inner), &payload); err == nil {
				if names := artifactNames(payload.Artifacts); names != "" {
					return names
				}
			}
		}
		return ""
	}
	return ""
}

func artifactNames(artifacts []struct {
	Type string `json:"type"`
	Name string `json:"name"`
}) string {
	names := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Name != "" {
			names = append(names, artifact.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "产出文件 " + strings.Join(names, ", ")
}

// newJudgeInvoker 构造裁判的 LLM 调用器：凭证与主 run 同源（caller 连接/API Key），
// 模型用 workflow 显式配置，未配置时回退全局默认 ReAct 模型（与 run 同口径，轻量判定不必独配）。
func newJudgeInvoker(ctx *gin.Context, wf *model.Workflow) judge.Invoker {
	modelKey := resolveWorkflowModelKey(wf)
	modelVersion := wf.ModelVersion
	if strings.TrimSpace(modelVersion) == "" {
		modelVersion = llm.ResolveModelVersion(modelKey, "")
	}
	apiKey, err := resolveWorkflowCredential(ctx, wf)
	if err != nil {
		zlog.Warnf(ctx, "[workflow.judge] 解析裁判凭证失败(裁判不生效): key=%s err=%v", wf.WorkflowKey, err)
		return nil
	}
	return judgeLLMInvoker{apiKey: apiKey, modelKey: modelKey, modelVersion: modelVersion}
}

// judgeLLMInvoker 把 api/llm 流式客户端适配为 judge.Invoker（非工具纯文本调用，extractor 同款）。
type judgeLLMInvoker struct {
	apiKey       string
	modelKey     string
	modelVersion string
}

func (i judgeLLMInvoker) ChatOnce(ctx context.Context, system, user string) (string, error) {
	client, err := llm.GetClientWithUserModel(i.apiKey, i.modelKey)
	if err != nil {
		return "", err
	}
	stream, err := client.ChatStream(ctx, []llm.LLMMessage{
		{Role: model.ReactMessageRoleSystem, Content: system},
		{Role: model.ReactMessageRoleUser, Content: user},
	}, i.modelVersion)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for chunk := range stream {
		if chunk.Error != nil {
			return "", chunk.Error
		}
		sb.WriteString(chunk.Content)
	}
	return sb.String(), nil
}

// resolveWorkflowCredential 按 run 同口径解析凭证：caller+route 连接优先，旧 API Key 兜底。
func resolveWorkflowCredential(ctx *gin.Context, wf *model.Workflow) (string, error) {
	routeValues := parseRouteValues(wf.RouteValues)
	conn, err := llmmodelService.ResolveConnection(ctx, wf.CallerKey, routeValues)
	if err != nil {
		return "", err
	}
	if conn != nil {
		return conn.ApiKeyValue, nil
	}
	return apikey.ResolveApiKey(ctx, wf.CallerKey, routeValues)
}

// resolveWorkflowModelKey 解析 run/裁判共用的模型 key：workflow 显式配置优先，
// 空则回退全局默认 ReAct 模型（方案 §4：model_key 空 = caller 默认模型）。
func resolveWorkflowModelKey(wf *model.Workflow) string {
	if key := strings.TrimSpace(wf.ModelKey); key != "" {
		return key
	}
	return conf.GetReactRuntimeConfig().Models.Default.ModelKey
}

// judgeRiskToWorkflowRisk 映射裁判语义级别到 run 风险级别：
// high 映射 high；low 在无关键词命中时保留为提示级；其余回退关键词结果。
func judgeRiskToWorkflowRisk(judgeRisk, keywordRisk string) string {
	switch judgeRisk {
	case model.WorkflowRiskLevelHigh:
		return model.WorkflowRiskLevelHigh
	case model.WorkflowRiskLevelLow:
		if keywordRisk == model.WorkflowRiskLevelHigh {
			return model.WorkflowRiskLevelHigh
		}
		return model.WorkflowRiskLevelLow
	default:
		return keywordRisk
	}
}

// findSessionReportArtifact 挑选会话的 markdown 报告产物（报告产物化：通知带下载链接）。
// 只认 markdown 文件（mime 或 .md 后缀），避免把截图等无关产物当报告；取最新一份。
func findSessionReportArtifact(ctx *gin.Context, result *react.RunResult) *model.ReactArtifact {
	if result == nil || result.SessionID == "" {
		return nil
	}
	artifacts, err := model.ListReactArtifactsBySessionID(ctx, result.SessionID)
	if err != nil || len(artifacts) == 0 {
		return nil
	}
	var latest *model.ReactArtifact
	for i := range artifacts {
		artifact := &artifacts[i]
		if !isMarkdownArtifact(artifact) {
			continue
		}
		latest = artifact
	}
	return latest
}

func isMarkdownArtifact(artifact *model.ReactArtifact) bool {
	if strings.Contains(strings.ToLower(artifact.MimeType), "markdown") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(artifact.FileName), ".md")
}

// maybeTripFailureBreaker 连环失败熔断（OpenHands maybe_disable_unhealthy 对照）：
// 最近 threshold 条 run 全为 failed/timeout（含本次）→ 自动停用 + 摘除调度 entry + 熔断通知。
// 返回是否触发熔断。语义说明：连续计数跨停用窗口——重启用后若首次 cron run 仍失败会立即
// 再熔断；运维验证请先手动 dispatch 成功一次（成功即清零连续计数）。
func maybeTripFailureBreaker(ctx context.Context, wf *model.Workflow, status string) bool {
	cfg := conf.GetWorkflowConfig()
	threshold := cfg.FailureBreakerThreshold
	if threshold <= 0 {
		return false
	}
	statuses, err := model.ListRecentWorkflowRunStatuses(ctx, wf.ID, threshold)
	if err != nil {
		zlog.Errorf(ctx, "[workflow.breaker] 读取最近 run 状态失败: key=%s err=%v", wf.WorkflowKey, err)
		return false
	}
	if !shouldTripFailureBreaker(statuses, threshold) {
		return false
	}
	// 快照可能过期：仅当定义仍处启用态才动作，避免重复熔断通知。
	current, err := model.GetWorkflowByKey(ctx, wf.WorkflowKey)
	if err != nil || current == nil || current.Enabled != 1 {
		return false
	}
	if err := model.UpdateWorkflowByKey(ctx, wf.WorkflowKey, map[string]any{"enabled": 0}); err != nil {
		zlog.Errorf(ctx, "[workflow.breaker] 自动停用失败: key=%s err=%v", wf.WorkflowKey, err)
		return false
	}
	manager.SyncRemove(wf.WorkflowKey)
	metrics.WorkflowTriggerTotal.WithLabelValues(wf.WorkflowKey, "circuit_break").Inc()
	zlog.Errorf(ctx, "[workflow.breaker] 连续失败熔断: key=%s 连续 %d 次 %s，已自动停用并摘除调度（排查后可手动启用）",
		wf.WorkflowKey, threshold, status)
	return true
}

// shouldTripFailureBreaker 纯函数：最近 statuses（id 倒序，含本次）全部为失败/超时即熔断。
func shouldTripFailureBreaker(statuses []string, threshold int) bool {
	if threshold <= 0 || len(statuses) < threshold {
		return false
	}
	for _, status := range statuses {
		if status != model.WorkflowRunStatusFailed && status != model.WorkflowRunStatusTimeout {
			return false
		}
	}
	return true
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

// summarizeRunResult 提取 run 结论快照（最后一条 assistant 消息）。
func summarizeRunResult(ctx *gin.Context, result *react.RunResult) string {
	if result == nil || result.RunID == "" {
		return ""
	}
	messages, err := model.GetReactMessagesByRunID(ctx, result.RunID)
	if err != nil {
		zlog.Warnf(ctx, "[workflow.runner] 读取 run 消息失败: runId=%s err=%v", result.RunID, err)
		return ""
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != model.ReactMessageRoleAssistant ||
			messages[i].MessageType != model.ReactMessageTypeAssistant {
			continue
		}
		return extractAssistantText(messages[i].ContentJSON)
	}
	return ""
}

// extractUserInputText 从 user_input 落库信封提取正文（裁判转录用）。
func extractUserInputText(contentJSON string) string {
	if strings.TrimSpace(contentJSON) == "" {
		return ""
	}
	var envelope struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(contentJSON), &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Content)
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

// firstLine 取文本首行并截断（日志用）。
func firstLine(text string, limit int) string {
	text = strings.TrimSpace(text)
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		text = text[:idx]
	}
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
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
