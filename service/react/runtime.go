package react

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/metrics"
	"react-base-service/components/params"
	"react-base-service/components/route"
	"react-base-service/conf"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	apikeyService "react-base-service/service/apikey"
	systempromptService "react-base-service/service/systemprompt"
	toolService "react-base-service/service/tool"
	"react-base-service/service/workspace"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"react-base-service/golib/zlog"
)

const (
	EventRun                   = "run"
	EventCancel                = "cancel"
	EventToolUseAnswer         = "tool_use_answer"
	EventThoughtStart          = "thought_start"
	EventThoughtDelta          = "thought_delta"
	EventThoughtEnd            = "thought_end"
	EventContentStart          = "content_start"
	EventContentDelta          = "content_delta"
	EventContentEnd            = "content_end"
	EventDone                  = "done"
	EventError                 = "error"
	EventCancelled             = "cancelled"
	EventHeartbeat             = "heartbeat"
	defaultReactSessionType    = model.ReactSessionTypeChat
	maxReactSessionTitleLength = 20
)

// EventWriter 屏蔽底层连接类型，Runtime 只负责发送标准 ReAct 事件。
type EventWriter func(params.ReactEvent) error

type RunResult struct {
	RunID     string `json:"runId"`
	SessionID string `json:"sessionId"`
}

// delegationAllowed 判定当前 run 是否装配 delegate_agent：
// subagent 开启、可见 agent 非空、且当前深度还允许再委派一层（depth < max_depth）。
func (r *runtimeRequest) delegationAllowed() bool {
	cfg := conf.GetReactRuntimeConfig().SubAgent
	if !cfg.SubAgentEnabled() || len(r.agents) == 0 {
		return false
	}
	return r.depth < cfg.MaxDepth
}

type reactMessageRef struct {
	RunID     string `json:"runId,omitempty"`
	MessageID string `json:"messageId,omitempty"`
	Seq       int    `json:"seq,omitempty"`
}

type compactSummaryContent struct {
	Summary        string            `json:"summary"`
	CoveredThrough []reactMessageRef `json:"coveredThrough,omitempty"`
}

// runEventEmitter 负责给运行时事件补齐 runId/sessionId/seq，并串行写出，避免并发工具事件打乱顺序。
// agentPath 标记多 Agent 事件归属（子 run 冒泡事件用），外层 run 为空、事件省略该字段。
type runEventEmitter struct {
	runID     string
	sessionID string
	agentPath string
	seq       int
	write     EventWriter
	mu        sync.Mutex
}

// Emit 统一封装 ReAct 对外事件信封；业务侧只需要传事件类型和 payload。
func (e *runEventEmitter) Emit(eventType string, payload any) error {
	return e.emit(eventType, nil, payload)
}

// EmitStep 发送带 ReAct 步骤归属的事件，stepIndex 放在事件外层而不是 payload 内。
func (e *runEventEmitter) EmitStep(stepIndex int, eventType string, payload any) error {
	return e.emit(eventType, &stepIndex, payload)
}

func (e *runEventEmitter) emit(eventType string, stepIndex *int, payload any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	return e.write(params.ReactEvent{
		Type:      eventType,
		Seq:       e.seq,
		RunID:     e.runID,
		SessionID: e.sessionID,
		StepIndex: stepIndex,
		AgentPath: e.agentPath,
		Payload:   payload,
	})
}

// generateRunID 生成一次 ReAct run 的业务 ID，统一使用 run_ 前缀方便日志和排查。
func generateRunID() string {
	return "run_" + strings.ReplaceAll(uuid.New().String(), "-", "")
}

// generateSessionID 生成 ReAct 会话 ID，创建新会话时由后端兜底分配。
func generateSessionID() string {
	return "session_" + strings.ReplaceAll(uuid.New().String(), "-", "")
}

// generateMessageID 生成 ReAct 消息 ID，用于串联 run 内的用户输入、模型输出和工具结果。
func generateMessageID() string {
	return "msg_" + strings.ReplaceAll(uuid.New().String(), "-", "")
}

// normalizeSessionType 将会话类型收敛到受支持集合；reflection 为内部整理专用类型，
// 外部传入时不做特殊拒绝（其执行档案仅放行记忆工具，无滥用面）。
func normalizeSessionType(t string) string {
	switch strings.TrimSpace(t) {
	case model.ReactSessionTypeChat, model.ReactSessionTypeReflection:
		return strings.TrimSpace(t)
	default:
		return defaultReactSessionType
	}
}

// normalizeMaxSteps 统一 run 最大步数默认值，防止调用方未传时主循环直接跳过。
func normalizeMaxSteps(maxSteps int) int {
	if maxSteps <= 0 {
		return conf.GetReactRuntimeConfig().MaxSteps
	}
	return maxSteps
}

// buildSessionTitle 为 ReAct 会话构造默认标题。
func buildSessionTitle(content string) string {
	v := strings.TrimSpace(content)
	if v == "" {
		return "新对话"
	}
	runes := []rune(v)
	if len(runes) <= maxReactSessionTitleLength {
		return v
	}
	return string(runes[:maxReactSessionTitleLength]) + "..."
}

// Run 启动一次“只写事件、不读取前端回包”的 ReAct Run。
//
// 适用场景：HTTP/SSE、后台调用等不存在 Client Tool / ask_question 等双向交互的入口。
// 该函数最终仍进入统一的 run() 和 executeReactLoop()，因此 Session、持久化、Tool Runtime、
// Memory、SubAgent 等行为与 WebSocket 模式保持一致；差别只在 readClient=nil。
func Run(ctx *gin.Context, payload params.ReactRunPayload, sessionID string, write EventWriter) (*RunResult, error) {
	return run(ctx, ctx.Request.Context(), payload, sessionID, write, nil)
}

// RunWithClientReader 启动一次支持前端回包的 ReAct Run。
//
// WebSocket 入口通过 readClient 把 client_tool_use_end、ask_question 作答、危险操作确认等消息
// 送回 Runtime。run() 会为整棵父/子 Agent Run 创建共享 clientMessageHub，避免多个并行等待者
// 同时直接读取同一个 WebSocket。
func RunWithClientReader(ctx *gin.Context, payload params.ReactRunPayload, sessionID string, write EventWriter, readClient ClientMessageReader) (*RunResult, error) {
	return run(ctx, ctx.Request.Context(), payload, sessionID, write, readClient)
}

// RunWithClientReaderContext 使用指定父 context 启动支持 client tool 回填的 ReAct run。
func RunWithClientReaderContext(ctx *gin.Context, parent context.Context, payload params.ReactRunPayload, sessionID string, write EventWriter, readClient ClientMessageReader) (*RunResult, error) {
	if parent == nil {
		parent = ctx.Request.Context()
	}
	return run(ctx, parent, payload, sessionID, write, readClient)
}

// run 是一次外层 ReAct Run 的生命周期编排入口。
//
// 主流程：
//   1. prepareRuntimeRequest：解析调用方、用户、模型和各类能力快照；
//   2. 入口 token 校验：在创建 Run 前拒绝明显超出上下文窗口的请求；
//   3. createReactRunContext：事务内创建/锁定 Session、创建 Run、持久化用户输入；
//   4. 创建 clientMessageHub 与可取消 context，并注册运行中取消句柄；
//   5. executeReactLoop：进入“模型 -> Tool -> 结果回填 -> 下一轮”的核心循环；
//   6. 统一收敛 finished / error / cancelled，并释放本 Run 的 Workspace。
//
// 这里负责 Run 生命周期，不实现 Tool、Memory、Agent 等领域能力本身。
//
// Steering S2：run 正常结束后若队列中还有排队输入且自动续跑开启，则用队首内容
// 在同一连接内自动开启下一个 run（模型/工具快照按新 run 重新解析），循环直至队列排空；
// cancel/error/timeout 收敛不续跑（失败语义对齐 ZCode：错误后暂停，留给用户决定）。
func run(ctx *gin.Context, parent context.Context, payload params.ReactRunPayload, sessionID string, write EventWriter, readClient ClientMessageReader) (*RunResult, error) {
	currentPayload := payload
	currentSessionID := sessionID
	for {
		result, err, nextPayload := runSingle(ctx, parent, currentPayload, currentSessionID, write, readClient)
		if err != nil || nextPayload == nil {
			return result, err
		}
		if result != nil && result.SessionID != "" {
			currentSessionID = result.SessionID
		}
		currentPayload = *nextPayload
	}
}

// runSingle 执行单个 run（含终态收敛与 Steering 结算/降级）；
// 返回的 nextPayload 非空表示需要自动续跑队首排队输入。
func runSingle(ctx *gin.Context, parent context.Context, payload params.ReactRunPayload, sessionID string, write EventWriter, readClient ClientMessageReader) (*RunResult, error, *params.ReactRunPayload) {
	req, err := prepareRuntimeRequest(ctx, payload, sessionID)
	if err != nil {
		return nil, err, nil
	}
	// Steering S2：自动续跑时待晋升的排队输入 ID（由 run 循环写入 payload）。
	req.promotePendingInputID = payload.PromotePendingInputID
	compactCfg := compactConfigForModel(req.resolvedModelKey)
	initialTools := runtimeToolDefinitions(req, executionProfileForRun(req))
	initialSystemContent := buildReactSystemContent(req.systemPrompt, renderToolIndexSummary(req.toolsIndexSnapshotJSON), renderSkillIndexSummary(req.skillsIndexSnapshotJSON), req.memoryContext, req.graphMemoryContext)
	if err := checkEntryInputTokens(initialSystemContent, req.modelUserMessage, initialTools, compactCfg.TokenTrigger); err != nil {
		return nil, err, nil
	}

	runID, sessionID, err := createReactRunContext(ctx, req)
	if err != nil {
		return nil, err, nil
	}

	if req.callerRuntimeContext.SessionID == "" {
		req.callerRuntimeContext.SessionID = sessionID
	}
	// 外层 run 创建上行消息分发器：并行委派时多个等待者（父/子 run 的 ask_question、
	// client tool 回填）按 toolUseId 认领消息；单等待者行为与历史直读一致。
	req.clientHub = newClientMessageHub(readClient)
	runCtx, cancel := context.WithCancelCause(components.ContextWithCallerRuntime(parent, req.callerRuntimeContext))
	registerReactRunCancel(runID, cancel)
	// Steering：注册软着陆标记（准入决策据此在收尾窗口拒绝 guide），run 结束时注销。
	registerReactRunSoftLanding(runID)
	metrics.RunsActive.Inc()
	defer func() {
		cancel(nil)
		unregisterReactRunCancel(runID)
		unregisterReactRunSoftLanding(runID)
		metrics.RunsActive.Dec()
		// 释放本 run 分配的代码工作区（未分配时为空操作；workspace 未启用同理）。
		workspace.Default().ReleaseRun(ctx, runID)
	}()

	emitter := &runEventEmitter{runID: runID, sessionID: sessionID, write: write}
	if err := executeReactLoop(ctx, runCtx, req, runID, sessionID, emitter, readClient); err != nil {
		if IsReactClientDisconnected(context.Cause(runCtx)) {
			err = ErrReactClientDisconnected
		}
		if IsReactRunCancelled(err) {
			metrics.RunsTotal.WithLabelValues("cancelled").Inc()
			_ = model.UpdateReactRunByRunID(ctx, runID, map[string]any{"state": model.ReactRunStateCancelled})
			cancelledRun, _ := model.GetReactRunByRunID(ctx, runID)
			usedTokens, maxTokens := reactContextWindowFields(cancelledRun)
			// 结算先于终态事件：前端先看到 steer_discarded 再看到 cancelled。
			settleRunPendingGuides(ctx, emitter, runID, model.ReactPendingSettleTurnCancelled)
			_ = emitter.Emit(EventCancelled, params.ReactCancelledPayload{OK: true, Reason: "本次运行已被用户取消", ContextUsedTokens: usedTokens, MaxContextTokens: maxTokens})
			return &RunResult{RunID: runID, SessionID: sessionID}, err, nil
		}
		if IsReactRunTimeout(err) {
			// run 级 wall-clock 边界：按 timeout 终态优雅收敛（不是 error，也不是 cancelled）。
			metrics.RunsTotal.WithLabelValues("timeout").Inc()
			_ = model.UpdateReactRunByRunID(ctx, runID, map[string]any{
				"state":         model.ReactRunStateTimeout,
				"error_message": fmt.Sprintf("run timeout: %ds", conf.GetReactRuntimeConfig().Loop.RunTimeoutSec),
			})
			timedOutRun, _ := model.GetReactRunByRunID(ctx, runID)
			usedTokens, maxTokens := reactContextWindowFields(timedOutRun)
			settleRunPendingGuides(ctx, emitter, runID, model.ReactPendingSettleTurnFailed)
			_ = emitter.Emit(EventTimeout, params.ReactCancelledPayload{OK: false, Reason: "本次运行超出时间上限被终止", ContextUsedTokens: usedTokens, MaxContextTokens: maxTokens})
			return &RunResult{RunID: runID, SessionID: sessionID}, err, nil
		}

		errorMessage := err.Error()
		if IsReactClientDisconnected(err) {
			errorMessage = "client disconnected"
		}
		run, getErr := model.GetReactRunByRunID(ctx, runID)
		if getErr == nil && run != nil && run.State == model.ReactRunStateCancelling {
			errorMessage = "cancel failed: " + errorMessage
		}

		zlog.Errorf(ctx, "[react.Run] run执行失败: runId=%s, err=%v", runID, err)
		metrics.RunsTotal.WithLabelValues("error").Inc()
		_ = model.UpdateReactRunByRunID(ctx, runID, map[string]any{
			"state":         model.ReactRunStateError,
			"error_message": errorMessage,
		})
		settleRunPendingGuides(ctx, emitter, runID, model.ReactPendingSettleTurnFailed)
		if !IsReactClientDisconnected(err) {
			usedTokens, maxTokens := reactContextWindowFields(run)
			_ = emitter.Emit(EventError, params.ReactErrorPayload{ErrNo: components.ErrorReactRunFailed.ErrNo, ErrMsg: errorMessage, ContextUsedTokens: usedTokens, MaxContextTokens: maxTokens})
		}
		return &RunResult{RunID: runID, SessionID: sessionID}, err, nil
	}

	// Steering S2：run 正常结束（含 finishExhausted）后自动续跑队首排队输入；
	// 读取失败只降级为"本轮不续跑"，队列留给下一次 run 结束后消费。
	if shouldAutoDrainQueue(conf.GetReactRuntimeConfig().Steering.QueueEnabled(), conf.GetReactRuntimeConfig().Steering.QueueAutoDrainEnabled(), true) {
		nextPayload, pendingID, drainErr := dequeueQueuedHeadForAutoDrain(ctx, sessionID)
		if drainErr != nil {
			zlog.Warnf(ctx, "[React.Steer] 读取自动续跑队首失败(本轮不续跑): sessionId=%s, err=%v", sessionID, drainErr)
		} else if nextPayload != nil {
			zlog.Infof(ctx, "[React.Steer] run 结束自动续跑队首: sessionId=%s, prevRunId=%s, pendingInputId=%s", sessionID, runID, pendingID)
			_ = emitter.Emit(EventSteerDrained, params.ReactSteerDrainedPayload{PendingInputID: pendingID})
			return &RunResult{RunID: runID, SessionID: sessionID}, nil, nextPayload
		}
	}
	return &RunResult{RunID: runID, SessionID: sessionID}, nil, nil
}

// reactContextWindowFields 在 run 结束(含 error/cancelled/timeout)时计算上下文窗口与已用 token。
// 终止路径拿不到本轮 LLM 的 token,但 maxContextTokens 按模型目录推导(未配置回退配置)、
// 已用 token 来自上一轮成功调用写库的 last tokens,均可返回。
func reactContextWindowFields(run *model.ReactRun) (usedTokens, maxTokens int) {
	modelKey := ""
	if run != nil {
		modelKey = run.ModelKey
		usedTokens = run.LastInputTokens + run.LastOutputTokens
	}
	maxTokens = reactMaxContextTokens(modelKey)
	return usedTokens, maxTokens
}

// createReactRunContext 在一个数据库写事务内建立本次 Run 的持久化起点。
//
// 事务内同时完成 Session 获取/创建、Session 行锁、同会话并发 Run 检查（Steering 准入）、
// 历史快照读取、ReactRun 创建和用户输入落库。放在同一事务中，是为了保证“一个 Session 同时
// 只有一个外层活跃 Run”的约束和历史恢复视图一致，避免两个请求交错创建 Run。
//
// Steering（S1）：命中活跃 run 时不再一律硬拒绝——run 运行中且可引导时，本次输入作为 guide
// 落账本并返回回执（以 steerReceiptError 穿透，绝不启动新 run）；其余按排队配置落账或保持
// 原硬拒绝错误。准入与"active 检查 + 账本写入"同事务，杜绝同一会话双 run 的异步窗口。
func createReactRunContext(ctx *gin.Context, req *runtimeRequest) (string, string, error) {
	var runID string
	var sessionID string
	var steerReceipt *SteerReceipt

	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error

		// run 启动阶段必须在同一写事务中完成，避免 session 刚创建后又被后续写入读到不一致状态。
		sessionID, err = getOrCreateReactSession(ctx, tx, req)
		if err != nil {
			return err
		}

		// session 行已在 getOrCreateReactSession 中加锁，这里检查 active run 可以挡住同一会话并发启动。
		active, err := model.HasActiveReactRunWithDB(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if active {
			if conf.GetReactRuntimeConfig().Steering.SteeringEnabled() {
				activeRun, err := model.GetActiveOuterReactRunBySessionIDWithDB(ctx, tx, sessionID)
				if err != nil {
					return err
				}
				if activeRun != nil {
					receipt, err := admitSteeringInputTx(ctx, tx, sessionID, activeRun, req.payload, len(req.attachments) > 0)
					if err != nil {
						return err
					}
					// 提交账本写入后以回执穿透返回（事务闭包返回 error 会回滚准入落账）。
					steerReceipt = &receipt
					return nil
				}
			}
			return components.ErrorReactRunActive.Sprintf(sessionID)
		}

		// 重启不复活队列：能走到"创建新 run"，说明会话已无活跃 run。
		// admitted 残留（进程崩溃/竞态窗口遗留）与上一进程遗留的 queued 行统一作废。
		if _, err := model.SettlePendingInputsBySessionWithDB(ctx, tx, sessionID, model.ReactPendingSettleSessionResumed); err != nil {
			return err
		}
		if _, err := model.SettleStaleQueuedBySessionWithDB(ctx, tx, sessionID, steeringProcessStart); err != nil {
			return err
		}

		storedMessages, err := model.GetOuterReactMessagesBySessionIDWithDB(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		req.historyMessages, req.historyMessageRefs = reactMessagesToChatMessagesWithRefs(storedMessages)

		latestRun, err := model.GetLatestOuterReactRunBySessionIDWithDB(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if latestRun != nil {
			req.todoStateJSON = latestRun.TodoStateJSON
			req.prevActiveToolIDsJSON = latestRun.ActiveToolIDs
			req.prevActiveToolDefsJSON = latestRun.ActiveToolDefsJSON
		}
		runID = generateRunID()
		req.modelUserMessageRef = reactMessageRef{RunID: runID, MessageID: generateMessageID(), Seq: 1}
		run := &model.ReactRun{
			RunID:                   runID,
			SessionID:               sessionID,
			UserName:                req.userName,
			CallerKey:               req.payload.CallerKey,
			RouteValues:             req.routeValuesJSON,
			State:                   model.ReactRunStateRunning,
			StepIndex:               0,
			MaxSteps:                normalizeMaxSteps(req.payload.MaxSteps),
			ModelKey:                req.resolvedModelKey,
			ModelVersion:            req.resolvedModelVersion,
			ApiKey:                  req.apiKey,
			ControlContextJSON:      string(req.payload.ControlContext),
			LLMContextJSON:          string(req.payload.LLMContext),
			SkillsIndexSnapshotJSON: req.skillsIndexSnapshotJSON,
			ToolIndexSnapshotJSON:   req.toolsIndexSnapshotJSON,
			TodoStateJSON:           req.todoStateJSON,
		}
		if err := model.CreateReactRunWithDB(ctx, tx, run); err != nil {
			return err
		}
		if err := persistUserInput(ctx, tx, req, runID, sessionID); err != nil {
			return err
		}
		// Steering S2：自动续跑的排队输入在此事务内 claim-once 晋升（与 run 创建、
		// 用户消息落库原子）；已被晋升/作废则整体回滚，杜绝双 run 消费同一条排队输入。
		if req.promotePendingInputID > 0 {
			claimed, err := model.MarkPendingInputPromotedWithDB(ctx, tx, req.promotePendingInputID, runID)
			if err != nil {
				return err
			}
			if !claimed {
				return components.ErrorParamInvalid.Sprintf("排队输入已被消费或作废: %d", req.promotePendingInputID)
			}
		}
		return model.UpdateReactSessionBySessionIDWithDB(ctx, tx, sessionID, map[string]any{
			"last_run_id":  runID,
			"last_message": trimRunLastMessage(req.payload.UserPrompt),
		})
	})
	if err != nil {
		return "", "", err
	}
	if steerReceipt != nil {
		return "", "", &steerReceiptError{receipt: *steerReceipt}
	}
	return runID, sessionID, nil
}

// prepareRuntimeRequest 构造一次 Run 的运行快照。
//
// 它不会启动模型调用，也不会创建 ReactRun；只负责把外部 payload 解析为 Runtime 真正需要的
// identity / model / capabilities / execution / conversation 信息。这样 executeReactLoop 不再关心
// Caller 路由、API Key 查找、Tool/Skill 可见性、Memory 查询等装配细节。
func prepareRuntimeRequest(ctx *gin.Context, payload params.ReactRunPayload, sessionID string) (*runtimeRequest, error) {
	return prepareRuntimeRequestWithServices(ctx, payload, sessionID, defaultRuntimeServices())
}

// prepareRuntimeRequestWithServices 与 prepareRuntimeRequest 行为一致，但显式接收 runtimeServices。
//
// 主要用于：
//   - 子 Agent Run 继承父 Run 的同一组 Tool/Agent/Memory Runtime，避免递归执行时切换实现；
//   - 单元测试注入 fake runtime，只验证 ReAct 编排逻辑。
// 这里采用依赖注入，是后续扩展 Plan Runtime / Durable Runtime 时保持核心循环稳定的重要边界。
func prepareRuntimeRequestWithServices(ctx *gin.Context, payload params.ReactRunPayload, sessionID string, services runtimeServices) (*runtimeRequest, error) {
	payload.CallerKey = strings.TrimSpace(payload.CallerKey)
	payload.Type = normalizeSessionType(payload.Type)
	payload.ModelKey = strings.TrimSpace(payload.ModelKey)
	payload.ModelVersion = strings.TrimSpace(payload.ModelVersion)
	payload.ModelHash = strings.TrimSpace(payload.ModelHash)
	payload.UserPrompt = strings.TrimSpace(payload.UserPrompt)

	if payload.CallerKey == "" || payload.UserPrompt == "" {
		return nil, components.ErrorParamInvalid.Sprintf("callerKey、userPrompt 不能为空")
	}

	caller, err := model.GetActiveCallerByKey(ctx, payload.CallerKey)
	if err != nil {
		return nil, err
	}
	if caller == nil {
		return nil, components.ErrorCallerNotFound.Sprintf(payload.CallerKey)
	}

	requestSource, err := parseControlContextRequestSource(payload.ControlContext)
	if err != nil {
		return nil, err
	}
	userName, err := resolveReactUserName(ctx, requestSource, payload.ControlContext)
	if err != nil {
		return nil, err
	}
	callerRuntimeCtx, err := components.NormalizeCallerRuntimeContext(ctx, payload.CallerKey, requestSource, userName, strings.TrimSpace(sessionID), "")
	if err != nil {
		return nil, err
	}
	routeValues := payload.RouteValues
	if routeValues == nil {
		routeValues = []string{}
	}
	routeValuesBytes, _ := json.Marshal(routeValues)

	var apiKey, modelKey, modelVersion string
	if payload.ModelHash != "" {
		userModel, err := model.GetUserModelByHash(ctx, payload.ModelHash)
		if err != nil {
			return nil, err
		}
		if userModel == nil {
			return nil, components.ErrorUserModelNotFound.Sprintf(payload.ModelHash)
		}
		apiKey = userModel.ApiKey
		modelKey = userModel.ModelKey
		modelVersion = userModel.ModelVersion
	} else {
		if payload.ModelKey == "" {
			return nil, components.ErrorParamInvalid.Sprintf("modelKey 不能为空")
		}
		var err error
		apiKey, err = apikeyService.ResolveApiKey(ctx, payload.CallerKey, routeValues)
		if err != nil {
			return nil, err
		}
		if apiKey == "" {
			return nil, components.ErrorApiKeyNotFound.Sprintf(payload.CallerKey)
		}
		modelKey = payload.ModelKey
		modelVersion = llm.ResolveModelVersion(modelKey, payload.ModelVersion)
	}
	if strings.TrimSpace(modelVersion) == "" {
		return nil, components.ErrorModelNotSupported.Sprintf(modelKey)
	}

	systemPrompt, err := systempromptService.ResolveSystemPrompt(ctx, payload.CallerKey, routeValues)
	if err != nil {
		zlog.Warnf(ctx, "[react.prepareRuntimeRequest] 解析系统提示词失败: callerKey=%s, err=%v", payload.CallerKey, err)
	}

	skills, err := model.FindSkillsByCallerAndRoutes(ctx, payload.CallerKey, route.BuildRoutePrefixes(routeValues))
	if err != nil {
		return nil, err
	}
	skillsIndexSnapshotJSON := buildSkillIndexSnapshotJSON(skills)

	tools, err := toolService.FindVisibleToolsByCallerAndRoutes(ctx, payload.CallerKey, route.BuildRoutePrefixes(routeValues), userName)
	if err != nil {
		return nil, err
	}
	toolsIndexSnapshotJSON := buildToolIndexSnapshotJSON(tools)

	// 子 Agent 清单：subagent.enabled 时装配，用于 delegate_agent 工具描述动态渲染与委派解析。
	// 经 GetReactRuntimeConfig 取值以合并管理面板「运行时配置」的 DB 覆盖（与 delegate/profile 同口径）。
	var agents []model.Agent
	if conf.GetReactRuntimeConfig().SubAgent.SubAgentEnabled() {
		agents, err = services.agentResolver().FindVisible(ctx, payload.CallerKey, routeValues)
		if err != nil {
			return nil, err
		}
	}

	// 长期记忆注入块：memory.enabled 时解析 caller(+user) 作用域并渲染常驻层与目录索引；
	// 记忆为空返回空串（不注入，token 零增量）。
	var memoryContext string
	if conf.CustomConf.LLM.React.Memory.MemoryEnabled() {
		memoryContext, err = services.memoryExecutor().BuildContext(ctx, payload.CallerKey, userName, conf.GetReactRuntimeConfig().Memory)
		if err != nil {
			return nil, err
		}
	}

	// 时序图谱记忆注入块：graph_memory.enabled 且 inject.enabled 时按本次用户输入检索相关事实。
	// 检索失败/超时/空结果静默跳过（注入是增强不是依赖），失败绝不阻断 run 启动。
	var graphMemoryContext string
	if graphCfg := conf.CustomConf.LLM.React.GraphMemory; graphCfg.GraphMemoryEnabled() && graphCfg.Inject.InjectEnabled() {
		graphMemoryContext = buildGraphMemoryContextForRun(ctx, payload.CallerKey, userName, payload.UserPrompt)
	}

	payload.RouteValues = routeValues
	attachments, err := prepareReactAttachments(ctx, payload.Attachments, userName)
	if err != nil {
		return nil, err
	}
	payload.Attachments = reactAttachmentRefsFromSnapshots(attachments)
	// P2-2 关键词触发器：命中时把 skill 提示追加到该条用户消息末尾（进模型 + 随 persistUserInput
	// 落库供后续轮次回放；前端历史展示只读用户原文不受影响）。委派子 run 会整体覆写
	// modelUserMessage，hint 天然不进入子 run。
	userContent := buildUserMessageContent(payload.UserPrompt, payload.LLMContext, renderAttachmentManifest(attachments))
	if skillTriggerHint := renderSkillTriggerHint(matchSkillTriggers(payload.UserPrompt, skills)); skillTriggerHint != "" {
		userContent = userContent + "\n\n" + skillTriggerHint
	}
	modelUserMessage := llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: userContent}
	return &runtimeRequest{
		payload:  payload,
		services: services,
		runtimeRequestIdentity: runtimeRequestIdentity{
			inputSessionID:       strings.TrimSpace(sessionID),
			userName:             userName,
			routeValuesJSON:      string(routeValuesBytes),
			callerRuntimeContext: callerRuntimeCtx,
		},
		runtimeRequestModel: runtimeRequestModel{
			apiKey:               apiKey,
			resolvedModelKey:     modelKey,
			resolvedModelVersion: modelVersion,
		},
		runtimeRequestCapabilities: runtimeRequestCapabilities{
			systemPrompt:            systemPrompt,
			skillsIndexSnapshotJSON: skillsIndexSnapshotJSON,
			toolsIndexSnapshotJSON:  toolsIndexSnapshotJSON,
			memoryContext:           memoryContext,
			graphMemoryContext:      graphMemoryContext,
			agents:                  agents,
		},
		runtimeRequestConversation: runtimeRequestConversation{
			modelUserMessage: modelUserMessage,
			attachments:      attachments,
		},
	}, nil
}

func parseControlContextRequestSource(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return "", nil
	}
	var payload struct {
		RequestSource string `json:"requestSource"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", components.ErrorParamInvalid.Sprintf("controlContext 非法: %v", err)
	}
	return strings.TrimSpace(payload.RequestSource), nil
}

// parseControlContextDingTalkUserName 从 controlContext.dingTalkMeta 中解析 userName。
// 与 plan 模式 parseUserNameFromDingTalkMeta 语义一致，钉钉服务间调用（中间件放行但不 SetUserName）由此取用户名。
func parseControlContextDingTalkUserName(raw json.RawMessage) string {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return ""
	}
	var payload struct {
		DingTalkMeta struct {
			UserName string `json:"userName"`
		} `json:"dingTalkMeta"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.DingTalkMeta.UserName)
}

// resolveReactUserName 解析用户身份：网页端由 IPS 中间件 SetUserName，直接读 Context；
// 仅 requestSource=datamap-knowledge-dingding 的钉钉服务间调用（中间件放行且不 SetUserName）
// 回退到 controlContext.dingTalkMeta.userName。
func resolveReactUserName(ctx *gin.Context, requestSource string, controlContext json.RawMessage) (string, error) {
	u := helpers.GetUserName(ctx)
	if u != "" && u != "unknown" && u != "system" {
		return strings.TrimSpace(u), nil
	}
	if requestSource == components.RequestSourceDatamapKnowledgeDingding {
		if u2 := parseControlContextDingTalkUserName(controlContext); u2 != "" {
			return u2, nil
		}
		return "", components.ErrorParamInvalid.Sprintf("dingTalkMeta.userName 不能为空")
	}
	return "", components.ErrorParamInvalid.Sprintf("userName 不能为空")
}

// getOrCreateReactSession 按消息信封中的 sessionId 优先复用；sessionId 为空时始终创建新会话。
func getOrCreateReactSession(ctx *gin.Context, tx *gorm.DB, req *runtimeRequest) (string, error) {
	inputSessionID := strings.TrimSpace(req.inputSessionID)
	if inputSessionID != "" {
		// 显式 sessionId 代表客户端指定会话，优先锁定已有行；不存在时才按该 ID 创建。
		existing, err := model.GetReactSessionBySessionIDForUpdate(ctx, tx, inputSessionID)
		if err != nil {
			return "", err
		}
		if existing != nil {
			if err := validateReactSessionOwnership(existing, req); err != nil {
				return "", err
			}
			return existing.SessionID, nil
		}
		return createReactSession(ctx, tx, inputSessionID, req)
	}

	return createReactSession(ctx, tx, generateSessionID(), req)
}

// validateReactSessionOwnership 校验显式 sessionId 的业务归属，避免跨用户、Caller 或路由复用上下文。
func validateReactSessionOwnership(session *model.ReactSession, req *runtimeRequest) error {
	return validateReactSessionContext(session, req.userName, req.payload.CallerKey, req.routeValuesJSON, req.payload.Type)
}

// validateReactSessionContext 按「会话归属五元组」校验访问合法性；
// run 启动（validateReactSessionOwnership）与 Steering 准入（SteerSession）共用同一口径。
func validateReactSessionContext(session *model.ReactSession, userName, callerKey, routeValuesJSON, sessionType string) error {
	if session == nil {
		return components.ErrorParamInvalid.Sprintf("session 不存在")
	}
	if session.State != model.ReactSessionStateActive ||
		session.UserName != userName ||
		session.CallerKey != callerKey ||
		session.RouteValues != routeValuesJSON ||
		session.SessionType != sessionType {
		return components.ErrorParamInvalid.Sprintf("sessionId 无权访问或上下文不匹配")
	}
	return nil
}

// createReactSession 在当前事务中创建新的 ReAct 会话，并返回最终落库的 sessionID。
func createReactSession(ctx *gin.Context, tx *gorm.DB, sessionID string, req *runtimeRequest) (string, error) {
	session := &model.ReactSession{
		SessionID:   sessionID,
		UserName:    req.userName,
		CallerKey:   req.payload.CallerKey,
		RouteValues: req.routeValuesJSON,
		SessionType: req.payload.Type,
		Title:       buildSessionTitle(req.payload.UserPrompt),
		State:       model.ReactSessionStateActive,
	}
	if err := model.CreateReactSessionWithDB(ctx, tx, session); err != nil {
		return "", err
	}
	metrics.SessionsCreatedTotal.Inc()
	return sessionID, nil
}

// persistUserInput 将用户本轮输入写入消息表，作为 run 的第一条可审计消息。
func persistUserInput(ctx *gin.Context, tx *gorm.DB, req *runtimeRequest, runID, sessionID string) error {
	content := map[string]any{
		"content":        req.payload.UserPrompt,
		"controlContext": json.RawMessage(req.payload.ControlContext),
		"llmContext":     json.RawMessage(req.payload.LLMContext),
		"attachments":    req.attachments,
		"modelMessage":   req.modelUserMessage,
	}
	contentJSON, _ := json.Marshal(content)
	return model.CreateReactMessageWithDB(ctx, tx, &model.ReactMessage{
		MessageID:   req.modelUserMessageRef.MessageID,
		RunID:       runID,
		SessionID:   sessionID,
		UserName:    req.userName,
		CallerKey:   req.payload.CallerKey,
		Seq:         1,
		StepIndex:   0,
		Role:        model.ReactMessageRoleUser,
		MessageType: model.ReactMessageTypeUserInput,
		ContentJSON: string(contentJSON),
	})
}

// buildInitialMessages 组装当前 run 的 system/user 消息；llmContext 绑定到对应 user 消息，Skill 摘要合并到 system 前缀。
func buildInitialMessages(req *runtimeRequest) []llm.LLMMessage {
	var messages []llm.LLMMessage
	if systemContent := buildReactSystemContent(req.systemPrompt, renderToolIndexSummary(req.toolsIndexSnapshotJSON), renderSkillIndexSummary(req.skillsIndexSnapshotJSON), req.memoryContext, req.graphMemoryContext); systemContent != "" {
		messages = append(messages, llm.LLMMessage{Role: model.ReactMessageRoleSystem, Content: systemContent})
	}
	messages = append(messages, llm.LLMMessage{Role: req.modelUserMessage.Role, Content: req.modelUserMessage.Content})
	return messages
}

func buildReactSystemContent(systemPrompt, toolSummary, skillSummary string, extraContexts ...string) string {
	parts := make([]string, 0, 3+len(extraContexts))
	if systemPrompt = strings.TrimSpace(systemPrompt); systemPrompt != "" {
		parts = append(parts, systemPrompt)
	}
	if toolSummary = strings.TrimSpace(toolSummary); toolSummary != "" {
		parts = append(parts, toolSummary)
	}
	if skillSummary = strings.TrimSpace(skillSummary); skillSummary != "" {
		parts = append(parts, skillSummary)
	}
	for _, contextText := range extraContexts {
		if contextText = strings.TrimSpace(contextText); contextText != "" {
			parts = append(parts, contextText)
		}
	}
	return strings.Join(parts, "\n\n")
}

func buildUserMessageContent(prompt string, llmContext json.RawMessage, attachmentTexts ...string) string {
	prompt = strings.TrimSpace(prompt)
	llmContextText := renderLLMContext(llmContext)
	attachmentText := ""
	if len(attachmentTexts) > 0 {
		attachmentText = attachmentTexts[0]
	}
	parts := make([]string, 0, 6)
	if llmContextText == "" || llmContextText == "null" {
		parts = append(parts, prompt)
	} else {
		parts = append(parts,
			"用户输入：",
			prompt,
			"本轮用户补充上下文 llmContext（调用方提供，按用户输入的一部分处理）：",
			llmContextText,
		)
	}
	if attachmentText = strings.TrimSpace(attachmentText); attachmentText != "" {
		parts = append(parts, attachmentText)
	}
	return strings.Join(parts, "\n")
}

// renderLLMContext 将 JSON 字符串解包为纯文本；JSON 对象保持原始结构供模型理解。
func renderLLMContext(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return strings.TrimSpace(text)
		}
	}
	return string(raw)
}

// trimRunLastMessage 截断会话摘要字段，避免用户长输入直接写入 last_message。
func trimRunLastMessage(content string) string {
	content = strings.TrimSpace(content)
	if len([]rune(content)) <= 512 {
		return content
	}
	return string([]rune(content)[:512])
}

// Cancel 将指定 run 置为 cancelling，并触发运行时 context 取消；最终 cancelled 事件由 run 收敛后发送。
func Cancel(ctx *gin.Context, runID, sessionID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return components.ErrorParamInvalid.Sprintf("runId 不能为空")
	}
	run, err := model.GetReactRunByRunID(ctx, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return components.ErrorReactRunNotFound.Sprintf(runID)
	}
	if sessionID != "" && run.SessionID != sessionID {
		return components.ErrorParamInvalid.Sprintf("sessionId 与 run 不匹配")
	}
	switch run.State {
	case model.ReactRunStateFinished, model.ReactRunStateCancelled, model.ReactRunStateError, model.ReactRunStateExpired, model.ReactRunStateTimeout:
		return components.ErrorParamInvalid.Sprintf("run 已结束，不能取消: state=%s", run.State)
	}
	cancel, ok := getActiveReactRunCancel(runID)
	if !ok {
		return components.ErrorParamInvalid.Sprintf("run 不在当前进程运行中，无法取消: %s", runID)
	}
	if err := model.UpdateReactRunByRunID(ctx, runID, map[string]any{"state": model.ReactRunStateCancelling}); err != nil {
		return err
	}
	cancel(ErrReactRunCancelled)
	return nil
}
