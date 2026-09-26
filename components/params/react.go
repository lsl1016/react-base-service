package params

import (
	"encoding/json"
	"time"
)

type ReactWSMessage struct {
	Type      string          `json:"type"`
	RunID     string          `json:"runId,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty" swaggertype:"object"`
}

type ReactAttachmentRef struct {
	FileID      string `json:"fileId"`
	FileName    string `json:"fileName,omitempty"`
	Description string `json:"description,omitempty"`
}

type ReactExecutionMode string

const (
	ReactExecutionModeReact ReactExecutionMode = "react"
	ReactExecutionModePlan  ReactExecutionMode = "plan"
)

// ReactReasoningOptions 思考程度三态（run 级）：off 关闭 / auto 自适应 / custom 显式指定。
// custom 时 effort 档位（minimal/low/medium/high）与 budgetTokens 思考预算二选一，
// 协议侧自动翻译：OpenAI 系 → reasoning_effort，Anthropic 系 → thinking.budget_tokens。
type ReactReasoningOptions struct {
	Mode         string `json:"mode"`
	Effort       string `json:"effort,omitempty"`
	BudgetTokens int    `json:"budgetTokens,omitempty"`
}

type ReactRunPayload struct {
	CallerKey   string               `json:"callerKey"`
	RouteValues []string             `json:"routeValues"`
	Type        string               `json:"type"`
	UserPrompt  string               `json:"userPrompt"`
	Attachments []ReactAttachmentRef `json:"attachments,omitempty"`
	// ControlContext 是运行控制参数，给后端运行态和工具执行链路使用，不作为普通用户提示词直接入模。
	// 这里适合放 requestSource、工具控制参数、执行开关等运行时控制信息，不应放文件正文。
	ControlContext json.RawMessage `json:"controlContext" swaggertype:"object"`
	// LLMContext 是补充给模型的业务上下文/页面上下文，支持 JSON 对象或纯文本字符串，会随用户消息一起参与入模。
	// 这里适合放业务背景、页面态信息、补充说明等提示词上下文，不应用来承载工具控制参数或文件正文。
	LLMContext   json.RawMessage `json:"llmContext" swaggertype:"object"`
	ModelKey     string          `json:"modelKey"`
	ModelVersion string          `json:"modelVersion"`
	ModelHash    string          `json:"modelHash"`
	MaxSteps     int             `json:"maxSteps"`
	// TokenBudget 是本次 run 的递归 token 预算（输入+输出+委派孙代理）；
	// 0 时回退 react.loop.budget_tokens_per_run，仍为 0 表示不限。
	TokenBudget int `json:"tokenBudget,omitempty"`
	// Reasoning 控制本次 run 的思考程度（off/auto/custom）；nil 按 auto 兼容旧客户端。
	Reasoning *ReactReasoningOptions `json:"reasoning,omitempty"`
	// ExecutionMode 控制本轮执行范式；空值按 react 兼容旧客户端。
	ExecutionMode ReactExecutionMode `json:"executionMode,omitempty"`
	// PromotePendingInputID 是 Steering S2 自动续跑时待晋升的排队输入账本 ID。
	// 仅服务端 run 循环内部传递，不参与 WS/API 序列化，也不写入账本 payload 快照。
	PromotePendingInputID uint `json:"-"`
}

type ReactModelInfo struct {
	ModelKey     string `json:"modelKey"`
	ModelVersion string `json:"modelVersion"`
	DisplayName  string `json:"displayName"`
	// 以下为模型配置面板/思考档位选择器消费的目录能力信息；未配置时为 0/false。
	ContextTokens   int  `json:"contextTokens,omitempty"`
	MaxOutputTokens int  `json:"maxOutputTokens,omitempty"`
	SupportThinking bool `json:"supportThinking,omitempty"`
}

type ReactModelsResp struct {
	Models       []ReactModelInfo `json:"models"`
	DefaultModel ReactModelInfo   `json:"defaultModel"`
}

// ConfigParamSchema 单个可配置参数的声明（声明式参数下发：前端按 min/max/step 渲染控件）。
type ConfigParamSchema struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Group       string   `json:"group"`       // model / context / reasoning
	Type        string   `json:"type"`        // int / bool / enum
	Min         *int     `json:"min,omitempty"`
	Max         *int     `json:"max,omitempty"`
	Step        *int     `json:"step,omitempty"`
	Default     *int     `json:"default,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Description string   `json:"description,omitempty"`
}

// ConfigSchemaResp 模型配置面板参数 schema（GET /react/config/schema）。
type ConfigSchemaResp struct {
	Params []ConfigParamSchema `json:"params"`
}

type ReactModelFallbackPayload struct {
	FromModelKey       string `json:"fromModelKey"`
	FromModelVersion   string `json:"fromModelVersion"`
	ToModelKey         string `json:"toModelKey"`
	ToModelVersion     string `json:"toModelVersion"`
	Reason             string `json:"reason"`
	ResetCurrentOutput bool   `json:"resetCurrentOutput"`
}

// ReactModelRetryPayload 模型调用同模型重试事件（model_retry）。
type ReactModelRetryPayload struct {
	ModelKey           string `json:"modelKey"`
	ModelVersion       string `json:"modelVersion"`
	Attempt            int    `json:"attempt"`
	MaxAttempts        int    `json:"maxAttempts"`
	DelayMs            int64  `json:"delayMs"`
	Reason             string `json:"reason"`
	ResetCurrentOutput bool   `json:"resetCurrentOutput"`
}

// ReactSoftLandingPayload 软着陆收尾触发事件（soft_landing）。
type ReactSoftLandingPayload struct {
	Reason          string `json:"reason"`
	RemainingSteps  int    `json:"remainingSteps"`
	RemainingMillis int64  `json:"remainingMillis,omitempty"`
}

type ReactEvent struct {
	Type      string `json:"type"`
	Seq       int    `json:"seq"`
	RunID     string `json:"runId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	StepIndex *int   `json:"stepIndex,omitempty"`
	// AgentPath 标记多 Agent 事件归属（如 main/ops-agent）；外层 run 省略该字段，
	// 旧客户端无感，新客户端按缺省 "main" 渲染。
	AgentPath string `json:"agentPath,omitempty"`
	Payload   any    `json:"payload,omitempty"`
}

type ReactThoughtStartPayload struct{}

type ReactThoughtDeltaPayload struct {
	ContentDelta string `json:"contentDelta,omitempty"`
}

type ReactThoughtEndPayload struct {
	Content           string `json:"content,omitempty"`
	InputTokens       int    `json:"inputTokens,omitempty"`
	OutputTokens      int    `json:"outputTokens,omitempty"`
	CacheReadTokens   int    `json:"cacheReadTokens,omitempty"`
	CacheCreateTokens int    `json:"cacheCreateTokens,omitempty"`
	ContextUsedTokens int    `json:"contextUsedTokens,omitempty"`
	MaxContextTokens  int    `json:"maxContextTokens,omitempty"`
}

type ReactContentStartPayload struct{}

type ReactContentDeltaPayload struct {
	ContentDelta string `json:"contentDelta"`
}

type ReactContentEndPayload struct {
	Content           string `json:"content,omitempty"`
	InputTokens       int    `json:"inputTokens,omitempty"`
	OutputTokens      int    `json:"outputTokens,omitempty"`
	CacheReadTokens   int    `json:"cacheReadTokens,omitempty"`
	CacheCreateTokens int    `json:"cacheCreateTokens,omitempty"`
	ContextUsedTokens int    `json:"contextUsedTokens,omitempty"`
	MaxContextTokens  int    `json:"maxContextTokens,omitempty"`
}

type ReactToolUseStartPayload struct {
	ToolUseID   string          `json:"toolUseId"`
	ToolName    string          `json:"toolName"`
	ToolInput   json.RawMessage `json:"toolInput,omitempty" swaggertype:"object"`
	Description string          `json:"description,omitempty"`
	ExecutedBy  string          `json:"executedBy"`
	Status      string          `json:"status"`
}

type ReactClientToolUseStartPayload struct {
	ToolUseID       string          `json:"toolUseId"`
	ToolName        string          `json:"toolName"`
	ToolInput       json.RawMessage `json:"toolInput,omitempty" swaggertype:"object"`
	Description     string          `json:"description,omitempty"`
	FrontendHint    string          `json:"frontendHint,omitempty"`
	Status          string          `json:"status"`
	PlanExecutionID string          `json:"planExecutionId,omitempty"`
	StepAttemptID   string          `json:"stepAttemptId,omitempty"`
}

type ReactToolUseEndPayload struct {
	ToolUseID    string          `json:"toolUseId"`
	Content      string          `json:"content,omitempty"`
	ResultRef    string          `json:"resultRef,omitempty"`
	Truncated    bool            `json:"truncated"`
	OmittedChars int             `json:"omittedChars,omitempty"`
	IsError      bool            `json:"isError"`
	ExecutedBy   string          `json:"executedBy"`
	Status       string          `json:"status"`
	DurationMs   int64           `json:"durationMs"`
	Meta         json.RawMessage `json:"meta,omitempty" swaggertype:"object"`
}

type ReactClientToolOutput struct {
	ToolUseID string          `json:"toolUseId"`
	Content   string          `json:"content"`
	Meta      json.RawMessage `json:"meta,omitempty" swaggertype:"object"`
	IsError   bool            `json:"isError"`
	Status    string          `json:"status,omitempty"`
}

type ReactClientToolUseEndPayload struct {
	ToolOutputs     []ReactClientToolOutput `json:"toolOutputs"`
	PlanExecutionID string                  `json:"planExecutionId,omitempty"`
	StepAttemptID   string                  `json:"stepAttemptId,omitempty"`
}

// ReactToolConfirmRequestPayload 是危险操作确认请求（P2-3）：服务端工具执行前
// permission_mode 命中时下发，前端展示确认卡片后以 tool_confirm_answer 上行作答。
type ReactToolConfirmRequestPayload struct {
	ToolUseID string          `json:"toolUseId"`
	ToolName  string          `json:"toolName"`
	ToolInput json.RawMessage `json:"toolInput,omitempty" swaggertype:"object"`
	// Mode 是生效的确认模式：confirm / confirm_risky。
	Mode string `json:"mode"`
	// Reason 是触发原因（confirm_risky 时含命中的风险正则）。
	Reason string `json:"reason,omitempty"`
}

type ReactCompactStartPayload struct {
	MessageCount  int `json:"messageCount"`
	EstimatedSize int `json:"estimatedSize"`
}

type ReactCompactEndPayload struct {
	BeforeMessageCount int    `json:"beforeMessageCount"`
	AfterMessageCount  int    `json:"afterMessageCount"`
	Summary            string `json:"summary"`
}

type ReactTodoUpdatePayload struct {
	TodoState json.RawMessage `json:"todoState" swaggertype:"object"`
}

type ReactHeartbeatPayload struct {
	Timestamp int64 `json:"ts"`
}

type ReactDonePayload struct {
	InputTokens       int    `json:"inputTokens"`
	OutputTokens      int    `json:"outputTokens"`
	CacheReadTokens   int    `json:"cacheReadTokens"`
	CacheCreateTokens int    `json:"cacheCreateTokens"`
	ContextUsedTokens int    `json:"contextUsedTokens"`
	MaxContextTokens  int    `json:"maxContextTokens"`
	TerminationReason string `json:"terminationReason,omitempty"`
	// DelegatedInput/OutputTokens 是本 run 委派子 Agent 的 token 消耗（递归口径，
	// 含子 run 再委派的开销）；未发生委派时为 0/省略。
	DelegatedInputTokens  int `json:"delegatedInputTokens,omitempty"`
	DelegatedOutputTokens int `json:"delegatedOutputTokens,omitempty"`
}

type ReactErrorPayload struct {
	ErrNo             int    `json:"errNo"`
	ErrMsg            string `json:"errMsg"`
	ContextUsedTokens int    `json:"contextUsedTokens"`
	MaxContextTokens  int    `json:"maxContextTokens"`
}

type ReactCancelledPayload struct {
	OK                bool   `json:"ok"`
	Reason            string `json:"reason,omitempty"`
	ContextUsedTokens int    `json:"contextUsedTokens"`
	MaxContextTokens  int    `json:"maxContextTokens"`
}

// ReactSteerReceiptPayload 是 Steering 准入回执（steer_guided/steer_queued/steer_rejected）：
// kind 对齐 ZCode PromptAdmissionReceipt 词汇；reason 为 queue/rejected 时的原因
// （run_not_steerable/soft_landing/attachments_unsupported）。
type ReactSteerReceiptPayload struct {
	Kind           string `json:"kind"`
	PendingInputID string `json:"pendingInputId,omitempty"`
	QueueLength    int    `json:"queueLength,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// ReactSteerDrainedPayload 是引擎在模型步边界消费 guide 的事件（steer_drained）：
// 注入的用户消息 messageId 随事件下发，前端可在对应 run 卡片内渲染"引导"标记。
type ReactSteerDrainedPayload struct {
	PendingInputID string `json:"pendingInputId"`
	MessageID      string `json:"messageId,omitempty"`
}

// ReactSteerDiscardedPayload 是未消费 guide 被结算作废的事件（steer_discarded）：
// reason 对齐 ZCode discard 词汇（turn_cancelled/turn_failed/run_finished/session_resumed）。
type ReactSteerDiscardedPayload struct {
	Count  int64  `json:"count"`
	Reason string `json:"reason"`
}

// ReactNoticeDrainedPayload 是运行时通知邮箱（Q1）在模型步边界吸收后台完成通知的事件
//（notice_drained）：MessageID 指向合并落库的 react_notice 消息，前端渲染为系统卡片。
// Content 仅历史回放（session/events）填充：实时事件只有吸收事实，正文在消息表里；
// 历史（replay）把 react_notice 消息映射回本事件时携带信封正文供卡片渲染。
type ReactNoticeDrainedPayload struct {
	MessageID string `json:"messageId"`
	Count     int    `json:"count"`
	Content   string `json:"content,omitempty"`
}

// ---------------------------------------------------------------------------
// S3 队列管理 API（/react/queue/*）
// ---------------------------------------------------------------------------

// ReactQueueItem 是队列管理视图里的一条排队输入。
type ReactQueueItem struct {
	ID             uint      `json:"id"`
	PendingInputID string    `json:"pendingInputId"`
	SessionID      string    `json:"sessionId"`
	Content        string    `json:"content"`
	Seq            int       `json:"seq"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
}

// ReactQueueListReq 查询会话队列（S3）。
type ReactQueueListReq struct {
	SessionID   string   `json:"sessionId"`
	CallerKey   string   `json:"callerKey"`
	RouteValues []string `json:"routeValues"`
}

// ReactQueueListResp 是队列查询响应：items 为 FIFO 排序的排队输入，
// autoDrain/queueEnabled 回显当前 steering 配置（前端据此决定是否提示显式发送）。
type ReactQueueListResp struct {
	Items        []ReactQueueItem `json:"items"`
	QueueEnabled bool             `json:"queueEnabled"`
	AutoDrain    bool             `json:"autoDrain"`
}

// ReactQueueUpdateReq 编辑一条排队输入的内容（S3）。
type ReactQueueUpdateReq struct {
	SessionID   string   `json:"sessionId"`
	CallerKey   string   `json:"callerKey"`
	RouteValues []string `json:"routeValues"`
	ID          uint     `json:"id"`
	Content     string   `json:"content"`
}

// ReactQueueReorderReq 重排会话队列（S3）：IDList 按期望的新顺序给出全部排队输入 ID。
type ReactQueueReorderReq struct {
	SessionID   string   `json:"sessionId"`
	CallerKey   string   `json:"callerKey"`
	RouteValues []string `json:"routeValues"`
	IDList      []uint   `json:"idList"`
}

// ReactQueueDeleteReq 删除（取消）一条排队输入（S3）：账本置 cancelled。
type ReactQueueDeleteReq struct {
	SessionID   string   `json:"sessionId"`
	CallerKey   string   `json:"callerKey"`
	RouteValues []string `json:"routeValues"`
	ID          uint     `json:"id"`
}

// ReactQueueMutateResp 是 update/reorder/delete 的统一响应：claimed=false 表示该输入
// 已被晋升或作废（多端并发下的 claim-once 落空）。
type ReactQueueMutateResp struct {
	Claimed       bool `json:"claimed"`
	QueueLength   int  `json:"queueLength"`
}

// ReactQueueSendReq 是 WS queue_send 消息的载荷（S3 显式发送：取队首/指定排队输入开新 run）。
type ReactQueueSendReq struct {
	SessionID      string `json:"sessionId"`
	PendingInputID string `json:"pendingInputId"`
}

type ReactSessionListReq struct {
	CallerKey   string   `json:"callerKey" binding:"required"`
	RouteValues []string `json:"routeValues"`
	Type        string   `json:"type"`
	Keyword     string   `json:"keyword"`
	Page        int      `json:"page"`
	PageSize    int      `json:"pageSize"`
}

type ReactSessionItem struct {
	SessionID   string   `json:"sessionId"`
	CallerKey   string   `json:"callerKey"`
	RouteValues []string `json:"routeValues"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	LastRunID   string   `json:"lastRunId"`
	LastMessage string   `json:"lastMessage"`
	State       string   `json:"state"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

type ReactSessionListResp struct {
	Sessions []ReactSessionItem `json:"sessions"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"pageSize"`
}

type ReactSessionEventsReq struct {
	SessionID string `json:"sessionId" binding:"required"`
}

// ReactAsyncTaskListReq 查询当前会话第三方已终态但本地尚未处理的异步任务，使用稳定游标分页。
type ReactAsyncTaskListReq struct {
	SessionID string `json:"sessionId" binding:"required"`
	Cursor    string `json:"cursor"`
	PageSize  int    `json:"pageSize"`
}

// ReactAsyncTaskItem 是前端可见的异步任务状态，不暴露提交快照和 Provider 任务信息。
type ReactAsyncTaskItem struct {
	ToolUseID       string `json:"toolUseId"`
	ToolName        string `json:"toolName"`
	State           string `json:"state"`
	ExecutionStatus string `json:"executionStatus"`
	ProviderStatus  string `json:"providerStatus,omitempty"`
	Progress        *int   `json:"progress,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
	LastObservedAt  string `json:"lastObservedAt,omitempty"`
	CompletedAt     string `json:"completedAt,omitempty"`
}

// ReactAsyncTaskListResp 返回当前页和下一页游标；前端应取完全部页面后再替换会话任务快照。
type ReactAsyncTaskListResp struct {
	Tasks              []ReactAsyncTaskItem `json:"tasks"`
	NextCursor         string               `json:"nextCursor,omitempty"`
	HasMore            bool                 `json:"hasMore"`
	HasProcessingTasks bool                 `json:"hasProcessingTasks"`
}

type ReactHistoryEvent struct {
	Type      string `json:"type"`
	Seq       int    `json:"seq"`
	RunID     string `json:"runId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	StepIndex *int   `json:"stepIndex,omitempty"`
	AgentPath string `json:"agentPath,omitempty"`
	Payload   any    `json:"payload,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

type ReactSessionEventsResp struct {
	SessionID string              `json:"sessionId"`
	Title     string              `json:"title"`
	Events    []ReactHistoryEvent `json:"events"`
}

// ReactRunFeedbackReq 轮次反馈请求：feedback 与 problemFeedback 至少传一个
type ReactRunFeedbackReq struct {
	RunID           string  `json:"runId" binding:"required"`
	Feedback        *int    `json:"feedback,omitempty"`
	ProblemFeedback *string `json:"problemFeedback,omitempty"`
}

// ReactRunFeedbackResp 轮次反馈响应
type ReactRunFeedbackResp struct {
	RunID                    string `json:"runId"`
	SessionID                string `json:"sessionId"`
	Feedback                 int    `json:"feedback"`
	ProblemFeedback          string `json:"problemFeedback"`
	FeedbackUpdatedAt        string `json:"feedbackUpdatedAt,omitempty"`
	ProblemFeedbackUpdatedAt string `json:"problemFeedbackUpdatedAt,omitempty"`
}

// ReactSessionFeedbackReq 会话维度查询轮次反馈请求（历史回显）
type ReactSessionFeedbackReq struct {
	SessionID string `json:"sessionId" binding:"required"`
}

// ReactSessionFeedbackResp 会话维度轮次反馈列表
type ReactSessionFeedbackResp struct {
	Items []ReactRunFeedbackResp `json:"items"`
}

// PlanStepResultRef 是 Step Result 的公开引用，详情通过 plan_execution/events 等接口按需读取。
type PlanStepResultRef struct {
	PlanExecutionID string `json:"plan_execution_id,omitempty"`
	PlanVersionID   string `json:"plan_version_id,omitempty"`
	StepID          string `json:"step_id,omitempty"`
	StepAttemptID   string `json:"step_attempt_id"`
	StepResultID    string `json:"step_result_id"`
}

type PlanStepPublicView struct {
	StepID        string             `json:"step_id"`
	StepOrder     int                `json:"step_order"`
	StepName      string             `json:"step_name,omitempty"`
	StepType      string             `json:"step_type,omitempty"`
	Required      bool               `json:"required"`
	Status        string             `json:"status"`
	Summary       string             `json:"summary"`
	PublicFields  map[string]any     `json:"public_fields,omitempty"`
	StepResultRef *PlanStepResultRef `json:"step_result_ref,omitempty"`
}

type PlanWaitRequest struct {
	RequestID         string         `json:"request_id,omitempty"`
	Type              string         `json:"type"`
	Question          string         `json:"question,omitempty"`
	ResponseSchema    map[string]any `json:"response_schema,omitempty"`
	ToolUseID         string         `json:"tool_use_id,omitempty"`
	FrontendHint      string         `json:"frontend_hint,omitempty"`
	PendingToolUseIDs []string       `json:"pending_tool_use_ids,omitempty"`
	Data              any            `json:"data,omitempty"`
}

type PlanPublicError struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

type PlanPublicView struct {
	PlanExecutionID string               `json:"plan_execution_id"`
	Status          string               `json:"status"`
	Summary         string               `json:"summary"`
	Steps           []PlanStepPublicView `json:"steps"`
	CurrentStep     *PlanStepPublicView  `json:"current_step,omitempty"`
	Result          map[string]any       `json:"result,omitempty"`
	WaitRequest     *PlanWaitRequest     `json:"wait_request,omitempty"`
	Error           *PlanPublicError     `json:"error,omitempty"`
	CanResume       bool                 `json:"can_resume"`
	UpdatedAt       string               `json:"updated_at"`
}

type PlanAttemptItem struct {
	PlanExecutionID string `json:"planExecutionId"`
	StepID          string `json:"stepId"`
	StepAttemptID   string `json:"stepAttemptId"`
	AttemptNo       int    `json:"attemptNo"`
	Status          string `json:"status"`
	StepRunID       string `json:"stepRunId,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	ErrorSummary    string `json:"errorSummary,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

type PlanExecutionDetailReq struct {
	PlanExecutionID string `json:"planExecutionId" binding:"required"`
	SessionID       string `json:"sessionId" binding:"required"`
	CallerKey       string `json:"callerKey" binding:"required"`
}

type PlanExecutionDetailResp struct {
	View     PlanPublicView    `json:"view"`
	Attempts []PlanAttemptItem `json:"attempts"`
}

type PlanStepEventsReq struct {
	PlanExecutionID string `json:"planExecutionId" binding:"required"`
	StepAttemptID   string `json:"stepAttemptId" binding:"required"`
	SessionID       string `json:"sessionId" binding:"required"`
	CallerKey       string `json:"callerKey" binding:"required"`
}

type PlanStepEventsResp struct {
	PlanExecutionID string              `json:"planExecutionId"`
	StepAttemptID   string              `json:"stepAttemptId"`
	StepID          string              `json:"stepId"`
	AttemptNo       int                 `json:"attemptNo"`
	Events          []ReactHistoryEvent `json:"events"`
}

type ReactPlanViewUpdatePayload struct {
	PlanExecutionID string         `json:"planExecutionId"`
	View            PlanPublicView `json:"view"`
}

type ReactPlanStepEventPayload struct {
	PlanExecutionID string     `json:"planExecutionId"`
	PlanVersionID   string     `json:"planVersionId"`
	StepID          string     `json:"stepId"`
	StepOrder       int        `json:"stepOrder"`
	StepAttemptID   string     `json:"stepAttemptId"`
	AttemptNo       int        `json:"attemptNo"`
	StepRunID       string     `json:"stepRunId"`
	Event           ReactEvent `json:"event"`
}

type PlanResumeReq struct {
	PlanExecutionID string         `json:"planExecutionId" binding:"required"`
	WaitRequestID   string         `json:"waitRequestId" binding:"required"`
	Response        map[string]any `json:"response"`
}

type PlanRetryReq struct {
	PlanExecutionID string `json:"planExecutionId" binding:"required"`
	StepID          string `json:"stepId" binding:"required"`
}

type PlanSkipReq struct {
	PlanExecutionID string `json:"planExecutionId" binding:"required"`
	StepID          string `json:"stepId" binding:"required"`
}

type PlanCancelReq struct {
	PlanExecutionID string `json:"planExecutionId" binding:"required"`
}

// ReactUsageContextReq 查询会话最近一个外层 run 的上下文容量构成（上下文容量看板）。
type ReactUsageContextReq struct {
	SessionID string `json:"sessionId" binding:"required"`
}

// ReactUsageCategory 是上下文构成的单个分类（key 与前端卡片分类一一对应）。
type ReactUsageCategory struct {
	Key    string `json:"key"`
	Tokens int    `json:"tokens"`
}

// ReactUsageContextResp 是上下文容量看板的聚合结果：容量占用、缓存命中率与分类构成。
type ReactUsageContextResp struct {
	// UsedTokens 是最近一轮模型调用后的上下文占用（last_input + last_output）。
	UsedTokens int `json:"usedTokens"`
	// MaxTokens 是上下文窗口口径上限（与事件侧一致，取压缩触发阈值）。
	MaxTokens int `json:"maxTokens"`
	// CacheHitRate 是本 run 累计缓存命中率（cache_read / total_input），无输入时为 0。
	CacheHitRate float64              `json:"cacheHitRate"`
	Categories   []ReactUsageCategory `json:"categories"`
	// UpdatedAt 是最近一轮模型调用时间（毫秒），前端用于展示数据新鲜度。
	UpdatedAt int64 `json:"updatedAt"`
}
