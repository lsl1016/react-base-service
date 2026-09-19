package react

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/metrics"
	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	"react-base-service/service/workspace"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PreparedExternalRun 是给 Plan Runtime 等上层执行器使用的“外层 Run 锚点”。
// runtimeRequest 保持包内私有；上层只能通过这里的窄接口复用 ReAct 能力，避免泄漏引擎内部结构。
type PreparedExternalRun struct {
	RunID        string
	SessionID    string
	UserName     string
	CallerKey    string
	ModelKey     string
	ModelVersion string
	UserPrompt   string
	req          *runtimeRequest
}

type TextCompletionResult struct {
	Content           string
	ModelKey          string
	ModelVersion      string
	InputTokens       int
	OutputTokens      int
	CacheReadTokens   int
	CacheCreateTokens int
}

type StructuredToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]interface{}
}

type StructuredCompletionResult struct {
	Arguments         json.RawMessage
	ModelKey          string
	ModelVersion      string
	InputTokens       int
	OutputTokens      int
	CacheReadTokens   int
	CacheCreateTokens int
}

type ScopedStepSpec struct {
	StepKey            string
	UserPrompt         string
	SystemPromptSuffix string
	MaxSteps           int
}

type ScopedStepResult struct {
	RunID         string
	FinalResponse string
}

// PrepareExternalRun 完成普通外层 Run 的装配、入口 token 校验、Session/Run/用户消息持久化，
// 但不进入 executeReactLoop。Plan Runtime 用它建立稳定 outer_run_id。
func PrepareExternalRun(ctx *gin.Context, payload params.ReactRunPayload, sessionID string) (*PreparedExternalRun, error) {
	req, err := prepareRuntimeRequest(ctx, payload, sessionID)
	if err != nil {
		return nil, err
	}
	compactCfg := conf.GetReactRuntimeConfig().ContextCompact
	initialTools := runtimeToolDefinitions(req, executionProfileForRun(req))
	initialSystemContent := buildReactSystemContent(req.systemPrompt, renderToolIndexSummary(req.toolsIndexSnapshotJSON), renderSkillIndexSummary(req.skillsIndexSnapshotJSON), req.memoryContext, req.graphMemoryContext)
	if err := checkEntryInputTokens(initialSystemContent, req.modelUserMessage, initialTools, compactCfg.TokenTrigger); err != nil {
		return nil, err
	}
	runID, resolvedSessionID, err := createReactRunContext(ctx, req)
	if err != nil {
		return nil, err
	}
	if req.callerRuntimeContext.SessionID == "" {
		req.callerRuntimeContext.SessionID = resolvedSessionID
	}
	return &PreparedExternalRun{
		RunID: runID, SessionID: resolvedSessionID, UserName: req.userName,
		CallerKey: req.payload.CallerKey, ModelKey: req.resolvedModelKey,
		ModelVersion: req.resolvedModelVersion, UserPrompt: req.payload.UserPrompt, req: req,
	}, nil
}

// RestoreExternalRun 根据 outer ReactRun 还原上层 Runtime 的执行快照。
// V1 恢复时重新解析系统提示词/Memory/Agent；Tool/Skill 可见集合优先沿用 outer run 的持久化快照。
func RestoreExternalRun(ctx *gin.Context, runID string) (*PreparedExternalRun, error) {
	run, err := model.GetReactRunByRunID(ctx, strings.TrimSpace(runID))
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, components.ErrorParamInvalid.Sprintf("outer run not found")
	}
	messages, err := model.GetReactMessagesByRunID(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	var prompt string
	var attachments []params.ReactAttachmentRef
	for _, stored := range messages {
		if stored.MessageType != model.ReactMessageTypeUserInput {
			continue
		}
		var content historyUserInputContent
		if json.Unmarshal([]byte(stored.ContentJSON), &content) == nil {
			prompt = content.Content
			attachments = reactAttachmentRefsFromSnapshots(content.Attachments)
			break
		}
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("outer run user input not found")
	}
	payload := params.ReactRunPayload{
		CallerKey: run.CallerKey,
		RouteValues: parseRouteValues(run.RouteValues),
		Type: model.ReactSessionTypeChat,
		UserPrompt: prompt,
		Attachments: attachments,
		ControlContext: json.RawMessage(run.ControlContextJSON),
		LLMContext: json.RawMessage(run.LLMContextJSON),
		ModelKey: run.ModelKey,
		ModelVersion: run.ModelVersion,
		MaxSteps: run.MaxSteps,
		ExecutionMode: params.ReactExecutionModePlan,
	}
	req, err := prepareRuntimeRequest(ctx, payload, run.SessionID)
	if err != nil {
		return nil, err
	}
	// outer run 已经保存了实际凭证与能力快照；恢复时优先复用，避免配置变更导致同一 Plan 中途漂移。
	req.apiKey = run.ApiKey
	if strings.TrimSpace(run.ToolIndexSnapshotJSON) != "" {
		req.toolsIndexSnapshotJSON = run.ToolIndexSnapshotJSON
	}
	if strings.TrimSpace(run.SkillsIndexSnapshotJSON) != "" {
		req.skillsIndexSnapshotJSON = run.SkillsIndexSnapshotJSON
	}
	return &PreparedExternalRun{
		RunID: run.RunID, SessionID: run.SessionID, UserName: run.UserName,
		CallerKey: run.CallerKey, ModelKey: run.ModelKey, ModelVersion: run.ModelVersion, UserPrompt: prompt, req: req,
	}, nil
}

// BeginPreparedRun 注册 outer run 的可取消上下文。cleanup 必须在当前执行片段结束时调用；
// WAIT 后 cleanup 会释放 goroutine 资源，但 DB 状态仍保留，可由后续 Resume 重建执行上下文。
func BeginPreparedRun(parent context.Context, prepared *PreparedExternalRun) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	runCtx, cancel := context.WithCancelCause(components.ContextWithCallerRuntime(parent, prepared.req.callerRuntimeContext))
	registerReactRunCancel(prepared.RunID, cancel)
	metrics.RunsActive.Inc()
	return runCtx, func() {
		cancel(nil)
		unregisterReactRunCancel(prepared.RunID)
		metrics.RunsActive.Dec()
	}
}

func newExternalModelState(ctx *gin.Context, runCtx context.Context, prepared *PreparedExternalRun, messages []llm.ChatMessage) (*reactEngineState, error) {
	current, failovers := configuredReactModelRouting(prepared.req)
	client, err := llm.GetClientWithUserModel(prepared.req.apiKey, current.ModelKey)
	if err != nil {
		return nil, components.ErrorLLMRequest.Sprintf(err.Error())
	}
	return newReactEngineState(ctx, runCtx, prepared.req, prepared.RunID, prepared.SessionID, client, current, failovers, nil, nil, messages), nil
}

// CompleteText 复用 ReAct 的模型路由与互备，但不暴露任何 Tool，不持久化为对话消息。
// Plan Finalizer 用它做纯文本总结。
func CompleteText(ctx *gin.Context, runCtx context.Context, prepared *PreparedExternalRun, systemPrompt, userPrompt string) (TextCompletionResult, error) {
	state, err := newExternalModelState(ctx, runCtx, prepared, []llm.ChatMessage{
		{Role: model.ReactMessageRoleSystem, Content: strings.TrimSpace(systemPrompt)},
		{Role: model.ReactMessageRoleUser, Content: strings.TrimSpace(userPrompt)},
	})
	if err != nil {
		return TextCompletionResult{}, err
	}
	result, err := state.callModelRoundWithEmitter(0, "plan:"+prepared.SessionID, nil, false)
	if err != nil {
		return TextCompletionResult{}, err
	}
	if len(result.Stream.ToolCalls) > 0 {
		return TextCompletionResult{}, fmt.Errorf("text completion unexpectedly returned tool calls")
	}
	return TextCompletionResult{
		Content: result.Stream.Content, ModelKey: result.Model.ModelKey, ModelVersion: result.Model.ModelVersion,
		InputTokens: result.Stream.InputTokens, OutputTokens: result.Stream.OutputTokens,
		CacheReadTokens: result.Stream.CacheReadTokens, CacheCreateTokens: result.Stream.CacheCreateTokens,
	}, nil
}

// CompleteStructured 用单一结构化 Tool Schema 约束 Planner 输出。
// 它不是 ReAct：不会执行 Tool，只把模型给该虚拟 Tool 的 arguments 当作结构化结果返回。
func CompleteStructured(ctx *gin.Context, runCtx context.Context, prepared *PreparedExternalRun, systemPrompt, userPrompt string, spec StructuredToolSpec) (StructuredCompletionResult, error) {
	state, err := newExternalModelState(ctx, runCtx, prepared, []llm.ChatMessage{
		{Role: model.ReactMessageRoleSystem, Content: strings.TrimSpace(systemPrompt)},
		{Role: model.ReactMessageRoleUser, Content: strings.TrimSpace(userPrompt)},
	})
	if err != nil {
		return StructuredCompletionResult{}, err
	}
	tool := llm.ToolDefinition{Name: spec.Name, Description: spec.Description, Parameters: spec.Parameters}
	result, err := state.callModelRoundWithEmitter(0, "plan-planner:"+prepared.SessionID, []llm.ToolDefinition{tool}, false)
	if err != nil {
		return StructuredCompletionResult{}, err
	}
	if result.Stream.StopReason != "tool_use" || len(result.Stream.ToolCalls) != 1 || result.Stream.ToolCalls[0].Name != spec.Name {
		return StructuredCompletionResult{}, fmt.Errorf("planner did not return required structured tool call")
	}
	args := result.Stream.ToolCalls[0].Input
	if len(args) == 0 || !json.Valid(args) {
		return StructuredCompletionResult{}, fmt.Errorf("planner returned invalid structured arguments")
	}
	return StructuredCompletionResult{
		Arguments: append(json.RawMessage(nil), args...),
		ModelKey: result.Model.ModelKey, ModelVersion: result.Model.ModelVersion,
		InputTokens: result.Stream.InputTokens, OutputTokens: result.Stream.OutputTokens,
		CacheReadTokens: result.Stream.CacheReadTokens, CacheCreateTokens: result.Stream.CacheCreateTokens,
	}, nil
}

// RunScopedStep 从 outer 快照派生隔离 Step ReactRun。
// 不继承 outer 历史/Todo/已加载 Tool；继承 Tool/Skill/Memory/Agent/附件能力快照。
func RunScopedStep(ctx *gin.Context, runCtx context.Context, prepared *PreparedExternalRun, spec ScopedStepSpec, write EventWriter, readClient ClientMessageReader) (ScopedStepResult, error) {
	base := *prepared.req
	base.payload = prepared.req.payload
	base.payload.ExecutionMode = params.ReactExecutionModeReact
	base.payload.UserPrompt = strings.TrimSpace(spec.UserPrompt)
	if spec.MaxSteps > 0 {
		base.payload.MaxSteps = spec.MaxSteps
	}
	base.historyMessages = nil
	base.historyMessageRefs = nil
	base.todoStateJSON = ""
	base.prevActiveToolIDsJSON = ""
	base.prevActiveToolDefsJSON = ""
	base.agentPath = "plan/" + strings.TrimSpace(spec.StepKey)
	base.depth = 0
	base.clientHub = newClientMessageHub(readClient)
	if suffix := strings.TrimSpace(spec.SystemPromptSuffix); suffix != "" {
		base.systemPrompt = strings.TrimSpace(base.systemPrompt) + "\n\n" + suffix
	}
	attachmentText := renderAttachmentManifest(base.attachments)
	base.modelUserMessage = llm.ChatMessage{
		Role: model.ReactMessageRoleUser,
		Content: buildUserMessageContent(base.payload.UserPrompt, nil, attachmentText),
	}

	stepRunID := generateRunID()
	base.modelUserMessageRef = reactMessageRef{RunID: stepRunID, MessageID: generateMessageID(), Seq: 1}
	run := &model.ReactRun{
		RunID: stepRunID, SessionID: prepared.SessionID, UserName: base.userName,
		CallerKey: base.payload.CallerKey, RouteValues: base.routeValuesJSON,
		State: model.ReactRunStateRunning, StepIndex: 0, MaxSteps: normalizeMaxSteps(base.payload.MaxSteps),
		ModelKey: base.resolvedModelKey, ModelVersion: base.resolvedModelVersion, ApiKey: base.apiKey,
		ControlContextJSON: string(base.payload.ControlContext), LLMContextJSON: string(base.payload.LLMContext),
		SkillsIndexSnapshotJSON: base.skillsIndexSnapshotJSON, ToolIndexSnapshotJSON: base.toolsIndexSnapshotJSON,
		ParentRunID: prepared.RunID, AgentPath: base.agentPath,
	}
	if err := model.CreateReactRun(ctx, run); err != nil {
		return ScopedStepResult{}, err
	}
	if err := persistUserInput(ctx, model.GetLLMDB(), &base, stepRunID, prepared.SessionID); err != nil {
		return ScopedStepResult{}, err
	}

	stepCtx, cancel := context.WithCancelCause(runCtx)
	registerReactRunCancel(stepRunID, cancel)
	metrics.RunsActive.Inc()
	defer func() {
		cancel(nil)
		unregisterReactRunCancel(stepRunID)
		metrics.RunsActive.Dec()
		workspace.Default().ReleaseRun(ctx, stepRunID)
	}()

	emitter := &runEventEmitter{runID: stepRunID, sessionID: prepared.SessionID, agentPath: base.agentPath, write: write}
	if err := executeReactLoop(ctx, stepCtx, &base, stepRunID, prepared.SessionID, emitter, readClient); err != nil {
		state := model.ReactRunStateError
		if IsReactRunCancelled(err) {
			state = model.ReactRunStateCancelled
		}
		_ = model.UpdateReactRunByRunID(ctx, stepRunID, map[string]any{"state": state, "error_message": err.Error()})
		return ScopedStepResult{RunID: stepRunID}, err
	}
	final, err := subAgentFinalResponse(ctx, stepRunID)
	if err != nil {
		return ScopedStepResult{RunID: stepRunID}, err
	}
	return ScopedStepResult{RunID: stepRunID, FinalResponse: final}, nil
}

// PersistExternalFinalAnswer 把 Plan Finalizer 的最终答复写回 outer ReactRun，并收敛 Session 摘要。
func AddExternalRunUsage(ctx *gin.Context, prepared *PreparedExternalRun, inputTokens, outputTokens, cacheReadTokens, cacheCreateTokens int) error {
	if inputTokens == 0 && outputTokens == 0 && cacheReadTokens == 0 && cacheCreateTokens == 0 {
		return nil
	}
	return model.GetLLMDB().WithContext(ctx).Model(&model.ReactRun{}).
		Where("run_id = ?", prepared.RunID).
		Updates(map[string]any{
			"total_input_tokens":   gorm.Expr("total_input_tokens + ?", inputTokens),
			"total_output_tokens":  gorm.Expr("total_output_tokens + ?", outputTokens),
			"cache_read_tokens":    gorm.Expr("cache_read_tokens + ?", cacheReadTokens),
			"cache_create_tokens":  gorm.Expr("cache_create_tokens + ?", cacheCreateTokens),
			"last_input_tokens":    inputTokens,
			"last_output_tokens":   outputTokens,
		}).Error
}

// PersistExternalFinalAnswer 把 Plan Finalizer 的最终答复写回 outer ReactRun，并收敛 Session 摘要。
// token usage 由 AddExternalRunUsage 在 Planner/Finalizer 每次模型调用后独立累计。
func PersistExternalFinalAnswer(ctx *gin.Context, prepared *PreparedExternalRun, content, modelKey, modelVersion string) error {
	target := reactModelTarget{ModelKey: modelKey, ModelVersion: modelVersion}
	if target.ModelKey == "" {
		target = reactModelTarget{ModelKey: prepared.ModelKey, ModelVersion: prepared.ModelVersion}
	}
	if _, _, err := persistAssistantMessage(ctx, prepared.req, prepared.RunID, prepared.SessionID, target, content, "", "", nil, 0); err != nil {
		return err
	}
	if err := model.UpdateReactRunByRunID(ctx, prepared.RunID, map[string]any{
		"state": model.ReactRunStateFinished,
	}); err != nil {
		return err
	}
	return model.UpdateReactSessionBySessionID(ctx, prepared.SessionID, map[string]any{
		"last_run_id": prepared.RunID,
		"last_message": trimRunLastMessage(content),
	})
}

func MarkExternalRunState(ctx *gin.Context, prepared *PreparedExternalRun, state string, errText string) error {
	updates := map[string]any{"state": state}
	if strings.TrimSpace(errText) != "" {
		updates["error_message"] = errText
	}
	return model.UpdateReactRunByRunID(ctx, prepared.RunID, updates)
}

func IsCancellation(err error) bool {
	return IsReactRunCancelled(err) || errors.Is(err, context.Canceled)
}
