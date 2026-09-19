package params

import "encoding/json"

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
	// ExecutionMode 控制本轮执行范式；空值按 react 兼容旧客户端。
	ExecutionMode ReactExecutionMode `json:"executionMode,omitempty"`
}

type ReactModelInfo struct {
	ModelKey     string `json:"modelKey"`
	ModelVersion string `json:"modelVersion"`
	DisplayName  string `json:"displayName"`
}

type ReactModelsResp struct {
	Models       []ReactModelInfo `json:"models"`
	DefaultModel ReactModelInfo   `json:"defaultModel"`
}

type ReactModelFallbackPayload struct {
	FromModelKey       string `json:"fromModelKey"`
	FromModelVersion   string `json:"fromModelVersion"`
	ToModelKey         string `json:"toModelKey"`
	ToModelVersion     string `json:"toModelVersion"`
	Reason             string `json:"reason"`
	ResetCurrentOutput bool   `json:"resetCurrentOutput"`
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
	ToolUseID    string          `json:"toolUseId"`
	ToolName     string          `json:"toolName"`
	ToolInput    json.RawMessage `json:"toolInput,omitempty" swaggertype:"object"`
	Description  string          `json:"description,omitempty"`
	FrontendHint string          `json:"frontendHint,omitempty"`
	Status       string          `json:"status"`
	PlanExecutionID string       `json:"planExecutionId,omitempty"`
	StepAttemptID   string       `json:"stepAttemptId,omitempty"`
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
	PlanExecutionID string                `json:"plan_execution_id"`
	Status          string                `json:"status"`
	Summary         string                `json:"summary"`
	Steps           []PlanStepPublicView  `json:"steps"`
	CurrentStep     *PlanStepPublicView   `json:"current_step,omitempty"`
	Result          map[string]any        `json:"result,omitempty"`
	WaitRequest     *PlanWaitRequest      `json:"wait_request,omitempty"`
	Error           *PlanPublicError      `json:"error,omitempty"`
	CanResume       bool                  `json:"can_resume"`
	UpdatedAt       string                `json:"updated_at"`
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
