package react

// Steering S1：运行中用户消息的准入、guide 引导注入与账本结算
// （docs/plan/20260925_Session与Steering机制借鉴方案.md §2，对齐 ZCode
// prompt-admission.ts 决策树、turn-tools.ts:469 / turn-stop.ts:195 的注入点纪律、
// promoteSessionInput 的消费原子性与 discardPendingInput 的结算语义）。
//
// 核心纪律：
//   - guide 注入点唯一——整批 tool_result 落库之后、下一次模型请求之前（或纯文本
//     finish 判定之前），注入的 user 消息成为请求尾部，绝不插入 tool_use/tool_result 之间；
//   - 准入原子——决策 + 账本落账在同一持有 session 行锁的事务内完成；
//   - 消费原子——guide 消费 = 同一事务内"账本置 guided + 用户消息落库"；
//   - 重启不复活队列——进程重启后残留 admitted 一律结算 discarded(session_resumed)。

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"react-base-service/golib/zlog"
)

// WS 回执与事件词汇（方案 §2.1：steer_queued/guided/drained/rejected/discarded）。
const (
	EventSteerGuided         = "steer_guided"
	EventSteerQueued         = "steer_queued"
	EventSteerDrained        = "steer_drained"
	EventSteerRejected       = "steer_rejected"
	EventSteerDiscarded      = "steer_discarded"
	EventSteerDeliveryChange = "steer_delivery_changed"
)

// 准入决策结论（内部词汇；回执层映射为 guided/queued/rejected）。
const (
	steerDecisionGuide  = "guide"
	steerDecisionQueue  = "queue"
	steerDecisionReject = "reject"
)

// 回执 kind（对齐 ZCode PromptAdmissionReceipt）。
const (
	steerReceiptGuided   = "guided"
	steerReceiptQueued   = "queued"
	steerReceiptRejected = "rejected"
)

// 拒绝/排队原因（客户端可直接据其渲染提示）。
const (
	steerReasonRunNotSteerable = "run_not_steerable" // waiting_client_message/waiting_plan/cancelling 等非 running 状态
	steerReasonSoftLanding     = "soft_landing"      // 软着陆收尾窗口内不再接收 guide
	steerReasonAttachments     = "attachments_unsupported"
)

const (
	steerRejectReasonRunNotSteerable = steerReasonRunNotSteerable
	steerRejectReasonSoftLanding     = steerReasonSoftLanding
	steerRejectReasonAttachments     = steerReasonAttachments

	steerQueueReasonNotSteerable = steerReasonRunNotSteerable
	steerQueueReasonSoftLanding  = steerReasonSoftLanding
	steerQueueReasonAttachments  = steerReasonAttachments
)

// steerAdmissionInput 是准入决策的输入快照；ActiveRunState 为空表示无活跃 run 可作为目标。
type steerAdmissionInput struct {
	ActiveRunState string
	SoftLanding    bool
	HasAttachments bool
	// QueueEnabled 为 S2 的排队开关：关闭时所有"不可引导"情形一律明确拒绝。
	QueueEnabled bool
}

// steerDecision 是纯函数化的准入结论（ZCode prompt-admission.ts 决策树的等价实现）。
type steerDecision struct {
	Kind   string // guide / queue / reject
	Reason string // queue/reject 时给出原因
}

// steerAdmissionDecision 判定运行中会话收到的新用户输入如何准入：
//   - running 且无附件且非软着陆 → guide（注入当前 run 的下一个模型轮）；
//   - 其余（waiting_*/cancelling、软着陆、带附件）→ QueueEnabled 时落队列，否则明确拒绝。
//
// HITL 等待（waiting_client_message/waiting_plan）与取消收尾（cancelling）状态绝不被 guide 打断。
func steerAdmissionDecision(in steerAdmissionInput) steerDecision {
	notSteerable := in.ActiveRunState != model.ReactRunStateRunning
	if notSteerable || in.SoftLanding || in.HasAttachments {
		reason := steerReasonRunNotSteerable
		switch {
		case notSteerable:
			reason = steerReasonRunNotSteerable
		case in.SoftLanding:
			reason = steerReasonSoftLanding
		default:
			reason = steerReasonAttachments
		}
		if in.QueueEnabled {
			return steerDecision{Kind: steerDecisionQueue, Reason: reason}
		}
		return steerDecision{Kind: steerDecisionReject, Reason: reason}
	}
	return steerDecision{Kind: steerDecisionGuide}
}

// steerDiscardReasonForRunState 将 run 终态映射为账本结算原因（对齐 ZCode discardPendingInput 词汇）。
func steerDiscardReasonForRunState(state string) string {
	switch state {
	case model.ReactRunStateCancelled, model.ReactRunStateCancelling:
		return model.ReactPendingSettleTurnCancelled
	case model.ReactRunStateFinished:
		return model.ReactPendingSettleRunFinished
	default:
		// error / timeout / expired：非用户取消的异常终态。
		return model.ReactPendingSettleTurnFailed
	}
}

// validPendingInputTransition 校验账本状态迁移合法性：guided/cancelled/discarded 是终态。
func validPendingInputTransition(from, to string) bool {
	switch from {
	case model.ReactPendingStatusAdmitted:
		return to == model.ReactPendingStatusGuided || to == model.ReactPendingStatusQueued || to == model.ReactPendingStatusDiscarded
	case model.ReactPendingStatusQueued:
		return to == model.ReactPendingStatusGuided || to == model.ReactPendingStatusCancelled || to == model.ReactPendingStatusDiscarded
	default:
		return false
	}
}

// pickPendingGuide 从账本行中挑出下一条待消费 guide：delivery=guide、status=admitted、
// 按会话准入序号 FIFO 取最早一条（queue 行与已消费/已结算行不参与）。
func pickPendingGuide(rows []model.ReactPendingInput) *model.ReactPendingInput {
	return pickPendingRow(rows, model.ReactPendingStatusAdmitted, model.ReactPendingDeliveryGuide)
}

// pickPendingQueued 挑出队首待晋升输入（S2 自动续跑）：status=queued 的最早一条。
func pickPendingQueued(rows []model.ReactPendingInput) *model.ReactPendingInput {
	return pickPendingRow(rows, model.ReactPendingStatusQueued, "")
}

// pickPendingRow 按 status（及可选 delivery）过滤后取 seq/id 最小的一条。
func pickPendingRow(rows []model.ReactPendingInput, status, delivery string) *model.ReactPendingInput {
	var picked *model.ReactPendingInput
	for i := range rows {
		row := &rows[i]
		if row.Status != status {
			continue
		}
		if delivery != "" && row.Delivery != delivery {
			continue
		}
		if picked == nil || row.Seq < picked.Seq || (row.Seq == picked.Seq && row.ID < picked.ID) {
			picked = row
		}
	}
	return picked
}

// guideMessageContent 构造 guide 注入的模型消息：纯文本 user 消息（S1 不携带附件与 llmContext）。
func guideMessageContent(content string) llm.ChatMessage {
	return llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: content}
}

// appendGuideToContext 把 guide 追加到引擎上下文尾部（连同落库引用）。
// 调用时机保证上下文末尾已是完整工具轮（assistant tool_use + 整批 tool_result）或纯文本
// assistant——guide 成为下一次模型请求的尾部 user 消息。
func appendGuideToContext(messages []llm.ChatMessage, refs [][]reactMessageRef, guide llm.ChatMessage, ref reactMessageRef) struct {
	messages []llm.ChatMessage
	refs     [][]reactMessageRef
} {
	outMessages := make([]llm.ChatMessage, 0, len(messages)+1)
	outMessages = append(outMessages, messages...)
	outMessages = append(outMessages, guide)
	outRefs := make([][]reactMessageRef, 0, len(refs)+1)
	outRefs = append(outRefs, refs...)
	outRefs = append(outRefs, []reactMessageRef{ref})
	return struct {
		messages []llm.ChatMessage
		refs     [][]reactMessageRef
	}{outMessages, outRefs}
}

// SteerReceipt 是准入回执（对齐 ZCode PromptAdmissionReceipt 词汇）。
type SteerReceipt struct {
	Kind           string `json:"kind"` // guided / queued / rejected
	PendingInputID string `json:"pendingInputId,omitempty"`
	QueueLength    int    `json:"queueLength,omitempty"`
	Reason         string `json:"reason,omitempty"`
	RunID          string `json:"runId,omitempty"`
}

// steerReceiptError 把准入回执从 createReactRunContext 穿透到调用方：
// 它表示"本次输入已被准入处理（引导/排队/拒绝），没有也不应启动新 run"，不是运行失败。
type steerReceiptError struct{ receipt SteerReceipt }

func (e *steerReceiptError) Error() string {
	switch e.receipt.Kind {
	case steerReceiptGuided:
		return "当前会话已有运行中的 ReAct run，本次输入已作为引导注入当前运行: " + e.receipt.RunID
	case steerReceiptQueued:
		return "当前会话已有运行中的 ReAct run，本次输入已加入会话队列: " + e.receipt.RunID
	default:
		return "当前会话已有运行中的 ReAct run，本次输入未准入（原因: " + e.receipt.Reason + "）"
	}
}

// SteerReceiptFromError 从 run 启动链路的错误中还原准入回执（errors.As 语义）。
func SteerReceiptFromError(err error) (SteerReceipt, bool) {
	var sentinel *steerReceiptError
	if err == nil || !asSteerReceiptError(err, &sentinel) {
		return SteerReceipt{}, false
	}
	return sentinel.receipt, true
}

func asSteerReceiptError(err error, target **steerReceiptError) bool {
	for err != nil {
		if typed, ok := err.(*steerReceiptError); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// pendingInputID 账本行对外 ID（pend_ 前缀，与 run_/msg_/session_ 命名风格一致）。
func pendingInputID(id uint) string {
	return "pend_" + strconv.FormatUint(uint64(id), 10)
}

// ---------------------------------------------------------------------------
// 准入入口
// ---------------------------------------------------------------------------

// SteerSession 是 WS 侧"run 活跃期间收到新用户消息"的服务层准入入口：
// 会话行锁事务内完成归属校验、活跃 run 定位、准入决策与账本落账。
// Steering 未开启时返回与历史一致的 ErrorReactRunActive（保持硬拒绝行为）。
func SteerSession(ctx *gin.Context, sessionID string, payload params.ReactRunPayload) (SteerReceipt, error) {
	if !conf.GetReactRuntimeConfig().Steering.SteeringEnabled() {
		return SteerReceipt{}, components.ErrorReactRunActive.Sprintf(sessionID)
	}
	sessionID = strings.TrimSpace(sessionID)
	payload.UserPrompt = strings.TrimSpace(payload.UserPrompt)
	if sessionID == "" || payload.UserPrompt == "" {
		return SteerReceipt{}, components.ErrorParamInvalid.Sprintf("sessionId、userPrompt 不能为空")
	}
	requestSource, err := parseControlContextRequestSource(payload.ControlContext)
	if err != nil {
		return SteerReceipt{}, err
	}
	userName, err := resolveReactUserName(ctx, requestSource, payload.ControlContext)
	if err != nil {
		return SteerReceipt{}, err
	}
	if payload.RouteValues == nil {
		payload.RouteValues = []string{}
	}
	routeValuesBytes, _ := json.Marshal(payload.RouteValues)

	var receipt SteerReceipt
	err = model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		session, err := model.GetReactSessionBySessionIDForUpdate(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if session == nil {
			return components.ErrorReactSessionNotFound.Sprintf(sessionID)
		}
		if err := validateReactSessionContext(session, userName, payload.CallerKey, string(routeValuesBytes), normalizeSessionType(payload.Type)); err != nil {
			return err
		}
		activeRun, err := model.GetActiveOuterReactRunBySessionIDWithDB(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if activeRun == nil {
			receipt = SteerReceipt{Kind: steerReceiptRejected, Reason: steerReasonRunNotSteerable}
			return nil
		}
		receipt, err = admitSteeringInputTx(ctx, tx, sessionID, activeRun, payload, len(payload.Attachments) > 0)
		return err
	})
	if err != nil {
		return SteerReceipt{}, err
	}
	return receipt, nil
}

// admitSteeringInputTx 在已持有 session 行锁的事务内完成准入决策与账本落账。
// 返回的回执由调用方决定投递方式（WS 事件 / steerReceiptError 穿透）。
// 账本行同时保存原始 run 请求快照（payload_json），供 queue 晋升/自动续跑时重建 run 请求。
func admitSteeringInputTx(ctx *gin.Context, tx *gorm.DB, sessionID string, activeRun *model.ReactRun, payload params.ReactRunPayload, hasAttachments bool) (SteerReceipt, error) {
	decision := steerAdmissionDecision(steerAdmissionInput{
		ActiveRunState: activeRun.State,
		SoftLanding:    isRunSoftLanding(activeRun.RunID),
		HasAttachments: hasAttachments,
		QueueEnabled:   conf.GetReactRuntimeConfig().Steering.QueueEnabled(),
	})

	seq, err := model.GetReactPendingInputMaxSeqBySessionIDWithDB(ctx, tx, sessionID)
	if err != nil {
		return SteerReceipt{}, err
	}
	// steer 消息通常不带模型选择：快照缺省时回填被引导 run 的实际模型，
	// 保证 queue 晋升/自动续跑重建 run 请求时模型选择有合理默认（prepareRuntimeRequest 仍会重新解析）。
	if payload.ModelKey == "" && payload.ModelHash == "" {
		payload.ModelKey = activeRun.ModelKey
		payload.ModelVersion = activeRun.ModelVersion
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return SteerReceipt{}, err
	}
	row := &model.ReactPendingInput{
		SessionID:   sessionID,
		RunID:       activeRun.RunID,
		Kind:        model.ReactPendingKindUserInput,
		Delivery:    model.ReactPendingDeliveryGuide,
		Status:      model.ReactPendingStatusAdmitted,
		Content:     payload.UserPrompt,
		Seq:         seq + 1,
		PayloadJSON: string(payloadJSON),
	}
	switch decision.Kind {
	case steerDecisionGuide:
		// guide：admitted 入账，等引擎在模型步边界消费。
	case steerDecisionQueue:
		row.Delivery = model.ReactPendingDeliveryQueue
		row.Status = model.ReactPendingStatusQueued
	default:
		return SteerReceipt{Kind: steerReceiptRejected, Reason: decision.Reason, RunID: activeRun.RunID}, nil
	}
	if err := model.CreateReactPendingInputWithDB(ctx, tx, row); err != nil {
		return SteerReceipt{}, err
	}
	queueLength := int64(0)
	if decision.Kind == steerDecisionQueue {
		if queueLength, err = model.CountReactPendingInputsBySessionStatusWithDB(ctx, tx, sessionID, model.ReactPendingStatusQueued); err != nil {
			return SteerReceipt{}, err
		}
	}
	kind := steerReceiptGuided
	if decision.Kind == steerDecisionQueue {
		kind = steerReceiptQueued
	}
	return SteerReceipt{
		Kind:           kind,
		PendingInputID: pendingInputID(row.ID),
		QueueLength:    int(queueLength),
		RunID:          activeRun.RunID,
	}, nil
}

// HandleWSSteer 处理 run 活跃期间收到的新 run 消息：准入并把回执以事件下发。
// Steering 未开启或校验失败时保持历史行为（错误事件"react run is active"），不打断当前 run。
func HandleWSSteer(ctx *gin.Context, write EventWriter, msg params.ReactWSMessage) {
	var payload params.ReactRunPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		_ = write(params.ReactEvent{Type: EventError, RunID: msg.RunID, SessionID: msg.SessionID, Payload: params.ReactErrorPayload{ErrNo: components.ErrorParamInvalid.ErrNo, ErrMsg: err.Error()}})
		return
	}
	receipt, err := SteerSession(ctx, msg.SessionID, payload)
	if err != nil {
		zlog.Infof(ctx, "[React.Steer] 准入未受理(保持历史拒绝行为): sessionId=%s, err=%v", msg.SessionID, err)
		_ = write(params.ReactEvent{Type: EventError, RunID: msg.RunID, SessionID: msg.SessionID, Payload: params.ReactErrorPayload{ErrNo: components.ErrorReactRunFailed.ErrNo, ErrMsg: err.Error()}})
		return
	}
	EmitSteerReceipt(write, receipt, msg.SessionID)
}

// EmitSteerReceipt 按回执 kind 选择 steer_guided/steer_queued/steer_rejected 事件下发。
func EmitSteerReceipt(write EventWriter, receipt SteerReceipt, sessionID string) {
	eventType := EventSteerGuided
	switch receipt.Kind {
	case steerReceiptQueued:
		eventType = EventSteerQueued
	case steerReceiptRejected:
		eventType = EventSteerRejected
	}
	_ = write(params.ReactEvent{
		Type:      eventType,
		RunID:     receipt.RunID,
		SessionID: sessionID,
		Payload: params.ReactSteerReceiptPayload{
			Kind:           receipt.Kind,
			PendingInputID: receipt.PendingInputID,
			QueueLength:    receipt.QueueLength,
			Reason:         receipt.Reason,
		},
	})
}

// ---------------------------------------------------------------------------
// 引擎侧消费与结算
// ---------------------------------------------------------------------------

// drainPendingGuideBoundary 在合法的模型步边界消费至多一条 pending guide（FIFO）：
// 同一事务内"账本置 guided + 用户消息落库"（promote 原子性），并把消息追加进引擎上下文。
// 返回是否发生注入；事务失败返回错误（账本行未动，下一边界自动重试）。
func (s *reactEngineState) drainPendingGuideBoundary(step int) (bool, error) {
	var drained *model.ReactPendingInput
	var guideRef reactMessageRef
	err := model.GetLLMDB().WithContext(s.ctx).Transaction(func(tx *gorm.DB) error {
		pending, err := model.GetNextAdmittedGuideForUpdateWithDB(s.ctx, tx, s.runID)
		if err != nil || pending == nil {
			return err
		}
		message, ref, err := persistGuideUserMessageTx(s.ctx, tx, s.req, s.runID, s.sessionID, pending.Content, step)
		if err != nil {
			return err
		}
		claimed, err := model.MarkPendingInputGuidedWithDB(s.ctx, tx, pending.ID)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}
		appended := appendGuideToContext(s.messages, s.messageRefs, message, ref)
		s.messages = appended.messages
		s.messageRefs = appended.refs
		drained = pending
		guideRef = ref
		return nil
	})
	if err != nil || drained == nil {
		if err != nil {
			s.logWarnf("[React.Steer] guide 消费失败(下一边界重试): runId=%s, err=%v", s.runID, err)
		}
		return false, err
	}
	s.logInfof("[React.Steer] guide 已注入下一模型轮: runId=%s, step=%d, pendingInputId=%s, messageId=%s", s.runID, step, pendingInputID(drained.ID), guideRef.MessageID)
	if s.emitter != nil {
		_ = s.emitter.EmitStep(step, EventSteerDrained, params.ReactSteerDrainedPayload{
			PendingInputID: pendingInputID(drained.ID),
			MessageID:      guideRef.MessageID,
		})
	}
	return true, nil
}

// persistGuideUserMessageTx 把 guide 作为真实用户消息落库（与账本置 guided 同一事务）。
// content 与 user_input 的历史内容结构一致，保证历史回放与重建走同一路径。
func persistGuideUserMessageTx(ctx *gin.Context, tx *gorm.DB, req *runtimeRequest, runID, sessionID, content string, step int) (llm.ChatMessage, reactMessageRef, error) {
	message := guideMessageContent(content)
	contentJSON, err := json.Marshal(map[string]any{
		"content":      content,
		"modelMessage": message,
	})
	if err != nil {
		return llm.ChatMessage{}, reactMessageRef{}, err
	}
	seq, err := model.GetReactMessageMaxSeqByRunIDWithDB(ctx, tx, runID)
	if err != nil {
		return llm.ChatMessage{}, reactMessageRef{}, err
	}
	ref := reactMessageRef{RunID: runID, MessageID: generateMessageID(), Seq: seq + 1}
	row := &model.ReactMessage{
		MessageID:   ref.MessageID,
		RunID:       runID,
		SessionID:   sessionID,
		Seq:         ref.Seq,
		StepIndex:   step,
		Role:        model.ReactMessageRoleUser,
		MessageType: model.ReactMessageTypeUserInput,
		ContentJSON: string(contentJSON),
	}
	if req != nil {
		row.UserName = req.userName
		row.CallerKey = req.payload.CallerKey
	}
	if err := model.CreateReactMessageWithDB(ctx, tx, row); err != nil {
		return llm.ChatMessage{}, reactMessageRef{}, err
	}
	return message, ref, nil
}

// settleRunPendingInputs 收敛 run 终态时的未消费输入（guide 与后台通知的统一结算入口）：
//   - 后台通知（kind=notification）绝不降级排队：run 终态后命令箱随 run 死亡、没有消费方，
//     一律结算 discarded（Q1，SettleNotificationsByRunIDWithDB，仅记日志不发 steer 事件）；
//   - guide（kind=user_input）：仅外层 run 在 queue 排队开启（S2）时降级 queue（对齐 ZCode
//     fallbackPendingGuidesToQueue，不丢输入；自动续跑仅在 run 正常结束后触发，error/cancel
//     天然暂停），广播 steer_delivery_changed——子 run（A2 SendMessage 的投递目标）的未消费
//     guide 是发给该子代理的定向消息，降级进会话队列会改投给下一轮主对话，语义错误，
//     一律结算 discarded；
//   - queue 排队关闭（S1 行为）：结算 discarded（取消/断连→turn_cancelled，error/timeout→turn_failed，
//     正常结束→run_finished），广播 steer_discarded。
func settleRunPendingInputs(ctx *gin.Context, emitter *runEventEmitter, runID string, isOuterRun bool, settleReason string) {
	if ctx == nil {
		return
	}
	if count, err := model.SettleNotificationsByRunIDWithDB(ctx, model.GetLLMDB(), runID, settleReason); err != nil {
		zlog.Warnf(ctx, "[React.Notify] 未消费后台通知结算失败(留待 session_resumed 兜底): runId=%s, err=%v", runID, err)
	} else if count > 0 {
		zlog.Infof(ctx, "[React.Notify] 未消费后台通知已结算: runId=%s, count=%d, reason=%s", runID, count, settleReason)
	}
	if isOuterRun && conf.GetReactRuntimeConfig().Steering.QueueEnabled() {
		count, err := model.FallbackPendingInputsToQueueWithDB(ctx, model.GetLLMDB(), runID)
		if err != nil {
			zlog.Warnf(ctx, "[React.Steer] guide 降级排队失败(留待 session_resumed 兜底): runId=%s, err=%v", runID, err)
			return
		}
		if count == 0 {
			return
		}
		zlog.Infof(ctx, "[React.Steer] 未消费 guide 已降级排队: runId=%s, count=%d", runID, count)
		if emitter != nil {
			_ = emitter.Emit(EventSteerDeliveryChange, params.ReactSteerDiscardedPayload{Count: count, Reason: steerFallbackReasonTurnEnded})
		}
		return
	}
	count, err := model.SettlePendingInputsByRunIDWithDB(ctx, model.GetLLMDB(), runID, settleReason)
	if err != nil {
		zlog.Warnf(ctx, "[React.Steer] guide 结算失败(留待 session_resumed 兜底): runId=%s, reason=%s, err=%v", runID, settleReason, err)
		return
	}
	if count == 0 {
		return
	}
	zlog.Infof(ctx, "[React.Steer] 未消费 guide 已结算: runId=%s, count=%d, reason=%s", runID, count, settleReason)
	if emitter != nil {
		_ = emitter.Emit(EventSteerDiscarded, params.ReactSteerDiscardedPayload{Count: count, Reason: settleReason})
	}
}

// steerFallbackReasonTurnEnded 是 guide 降级排队事件的 reason 词汇。
const steerFallbackReasonTurnEnded = "turn_ended"

// steeringProcessStart 记录本进程启动时刻：重启后 createReactRunContext 据此把
// 上一进程遗留的 queued 行作废（重启不复活队列），本进程新产生的排队行不受影响。
var steeringProcessStart = time.Now()

// shouldAutoDrainQueue 判定 run 正常结束后是否自动续跑队首（纯函数）：
// 仅在排队与自动续跑开关都开启、run 以成功 finish 收敛时触发；
// cancel/error/timeout 收敛不续跑（失败语义对齐 ZCode：错误后暂停自动续跑，留给用户决定）。
func shouldAutoDrainQueue(queueEnabled, autoDrainEnabled, runCompletedNormally bool) bool {
	return queueEnabled && autoDrainEnabled && runCompletedNormally
}

// dequeueQueuedHeadForAutoDrain 取队首 queued 输入并构造续跑 payload（纯查询 + claim 前置读取）；
// payload 携带 PromotePendingInputID（晋升 claim 在新 run 的 createReactRunContext 事务内完成，
// 与"用户消息落库 + run 创建"原子）。无队列或未开启续跑返回 nil。
func dequeueQueuedHeadForAutoDrain(ctx *gin.Context, sessionID string) (*params.ReactRunPayload, string, error) {
	head, err := model.GetNextQueuedBySessionWithDB(ctx, model.GetLLMDB(), sessionID)
	if err != nil || head == nil {
		return nil, "", err
	}
	var payload params.ReactRunPayload
	if strings.TrimSpace(head.PayloadJSON) != "" {
		if err := json.Unmarshal([]byte(head.PayloadJSON), &payload); err != nil {
			zlog.Warnf(ctx, "[React.Steer] 队列项 payload 快照解析失败(按空请求续跑): pendingInputId=%s, err=%v", pendingInputID(head.ID), err)
			payload = params.ReactRunPayload{}
		}
	}
	payload.UserPrompt = head.Content
	payload.PromotePendingInputID = head.ID
	return &payload, pendingInputID(head.ID), nil
}

// ---------------------------------------------------------------------------
// 软着陆标记（跨 goroutine 的准入可见性）
// ---------------------------------------------------------------------------

// activeRunSoftLanding 记录外层 run 是否已进入软着陆收尾窗口（runID -> *atomic.Bool）。
// 引擎在 enterSoftLanding 时置位，准入决策据此拒绝 guide；run 结束时随注册表注销。
var activeRunSoftLanding sync.Map

func registerReactRunSoftLanding(runID string) {
	flag := &atomic.Bool{}
	activeRunSoftLanding.Store(runID, flag)
}

func unregisterReactRunSoftLanding(runID string) {
	activeRunSoftLanding.Delete(runID)
}

// markRunSoftLanding 置位软着陆标记；仅外层 run 注册过条目，子 run 不写。
func markRunSoftLanding(runID string) {
	if value, ok := activeRunSoftLanding.Load(runID); ok {
		if flag, ok := value.(*atomic.Bool); ok {
			flag.Store(true)
		}
	}
}

// isRunSoftLanding 查询 run 是否处于软着陆窗口；未注册（含跨进程/已结束）返回 false。
func isRunSoftLanding(runID string) bool {
	value, ok := activeRunSoftLanding.Load(runID)
	if !ok {
		return false
	}
	flag, ok := value.(*atomic.Bool)
	return ok && flag.Load()
}
