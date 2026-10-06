package react

import (
	hub "react-base-service/service/react/internal/hub"

	asynctask "react-base-service/service/react/internal/asynctask"
	mem "react-base-service/service/react/internal/mem"

	core "react-base-service/service/react/internal/core"
	pyexec "react-base-service/service/react/internal/pyexec"
)

// core_bridge 把 internal/core 的基础件以包内既有私有符号名重新导出，
// 使引擎门面与方法群文件在 core 拆分后零改动。新增能力域子包时引用 core 导出名，
// 不要再往本文件追加与引擎状态耦合的符号。

// 内置 Meta Tool 名称（core 是唯一权威定义处）。
const (
	metaToolListTools         = core.MetaToolListTools
	metaToolGetTool           = core.MetaToolGetTool
	metaToolExecuteTool       = core.MetaToolExecuteTool
	metaToolListSkills        = core.MetaToolListSkills
	metaToolGetSkill          = core.MetaToolGetSkill
	metaToolReadToolResult    = core.MetaToolReadToolResult
	metaToolInspectData       = core.MetaToolInspectData
	metaToolPythonExec        = core.MetaToolPythonExec
	metaToolTodoWrite         = core.MetaToolTodoWrite
	metaToolAskQuestion       = core.MetaToolAskQuestion
	metaToolDisplayFiles      = core.MetaToolDisplayFiles
	metaToolResolveAsyncTask  = core.MetaToolResolveAsyncTask
	metaToolGetAsyncTask      = core.MetaToolGetAsyncTask
	metaToolReadAttachment    = core.MetaToolReadAttachment
	metaToolInspectAttachment = core.MetaToolInspectAttachment
	metaToolCreatePlan        = core.MetaToolCreatePlan
	metaToolLoadRuntimeCode   = core.MetaToolLoadRuntimeCode
	metaToolDelegateAgent     = core.MetaToolDelegateAgent
	metaToolSendMessage       = core.MetaToolSendMessage
	metaToolWaitAgent         = core.MetaToolWaitAgent
	metaToolMemoryList        = core.MetaToolMemoryList
	metaToolMemoryRead        = core.MetaToolMemoryRead
	metaToolMemoryWrite       = core.MetaToolMemoryWrite
	metaToolGraphMemorySearch = core.MetaToolGraphMemorySearch
	metaToolGraphMemoryWrite  = core.MetaToolGraphMemoryWrite
)

func isInternalMetaTool(name string) bool { return core.IsInternalMetaTool(name) }

// ExecutionProfile 类型别名：方法（AllowsInternalTool）随类型走 core，门面零改动。
type ExecutionProfile = core.ExecutionProfile

// JSON Schema 校验（Business Tool 入参）。
type JSONSchema = core.JSONSchema

var ValidateJSONSchemaValue = core.ValidateJSONSchemaValue

// token 估算兜底（lastInputTokens 不可用时使用）。
const (
	msgRoleOverheadTokens  = core.MsgRoleOverheadTokens
	toolUsePartOverhead    = core.ToolUsePartOverhead
	toolResultPartOverhead = core.ToolResultPartOverhead
	toolDefinitionOverhead = core.ToolDefinitionOverhead
)

var (
	estimateMessagesTokens        = core.EstimateMessagesTokens
	estimateToolDefinitionsTokens = core.EstimateToolDefinitionsTokens
)

// Skill 关键词触发匹配。
type skillTriggerMatch = core.SkillTriggerMatch

var (
	matchSkillTriggers     = core.MatchSkillTriggers
	renderSkillTriggerHint = core.RenderSkillTriggerHint
	parseSkillTriggersJSON = core.ParseSkillTriggersJSON
)

const skillTriggerMatchLimit = core.SkillTriggerMatchLimit

// 工具声明构造器与 rune 截断工具（core/toolkit.go）。
var (
	objectTool    = core.ObjectTool
	stringSchema  = core.StringSchema
	numberSchema  = core.NumberSchema
	truncateRunes = core.TruncateRunes
	headRunes     = core.HeadRunes
)

// 大结果读取 / 附件解析 / cookie 提取原语（core/refs.go）与产物下载入口（pyexec/artifact.go）。
var (
	readResultRef           = core.ReadResultRef
	sliceResultContent      = core.SliceResultContent
	resolveAttachmentRecord = core.ResolveAttachmentRecord
	requestCookies          = core.RequestCookies
	LoadArtifactForDownload = pyexec.LoadArtifactForDownload
)

// pyexecRunContext 从引擎状态构造分析执行域的最小执行面。
func (s *reactEngineState) pyexecRunContext() pyexec.RunContext {
	return pyexec.RunContext{
		GinCtx:    s.ctx,
		RunCtx:    s.runCtx,
		RunID:     s.runID,
		SessionID: s.sessionID,
		UserName:  s.req.userName,
		CallerKey: s.req.payload.CallerKey,
	}
}

// python_exec 产物元数据类型（display_files 渲染共用）。
type (
	pythonExecArtifactMetaItem = pyexec.ArtifactMetaItem
	pythonExecArtifactMeta     = pyexec.ArtifactMeta
)

const pythonExecArtifactURLPrefix = pyexec.ArtifactURLPrefix

// 模型重试分类与退避（core/modelretry.go）、思考参数解析（core/reasoning.go）。
type modelFailureClass = core.ModelFailureClass

const (
	modelFailureRetryable = core.ModelFailureRetryable
	modelFailureFatal     = core.ModelFailureFatal
	modelFailureCancelled = core.ModelFailureCancelled
	reasoningBudgetMax    = core.ReasoningBudgetMax
)

var (
	isRetryableHTTPStatus = core.IsRetryableHTTPStatus
	classifyModelFailure  = core.ClassifyModelFailure
	modelRetryDelay       = core.ModelRetryDelay
	modelRetryMaxAttempts = core.ModelRetryMaxAttempts
	recordModelRetry      = core.RecordModelRetry
	parseReasoningOptions = core.ParseReasoningOptions
)

// Runtime 错误哨兵与判定（core/errors.go）。哨兵全局唯一，别名为同一实例。
var (
	ErrReactRunCancelled       = core.ErrReactRunCancelled
	ErrReactClientDisconnected = core.ErrReactClientDisconnected
	ErrReactRunTimeout         = core.ErrReactRunTimeout
	ErrInteractionTimeout      = core.ErrInteractionTimeout
	newRetryableModelError     = core.NewRetryableModelError
)

func IsReactRunTimeout(err error) bool       { return core.IsReactRunTimeout(err) }
func IsErrInteractionTimeout(err error) bool { return core.IsErrInteractionTimeout(err) }

// 记忆 adapter 纯函数（mem 包）与异步任务查询 API（asynctask 包）。
type (
	memoryScopeResolved = mem.MemoryScopeResolved
)

var (
	resolveMemoryScope       = mem.ResolveMemoryScope
	memoryOwnerKey           = mem.MemoryOwnerKey
	mergeMemoryItems         = mem.MergeMemoryItems
	renderMemoryContext      = mem.RenderMemoryContext
	buildMemoryContextForRun = mem.BuildMemoryContextForRun
	memoryToolDefinitions    = mem.MemoryToolDefinitions
	memoryItemHasTag         = mem.MemoryItemHasTag
	memoryItemMatchesKeyword = mem.MemoryItemMatchesKeyword
	memorySourceForRun       = mem.MemorySourceForRun

	ListSessionAsyncTasks = asynctask.ListSessionAsyncTasks
)

const historyTimeFormat = core.HistoryTimeFormat

var (
	canViewReactSessionHistory = core.CanViewReactSessionHistory
	formatHistoryTime          = core.FormatHistoryTime
)

// WS/事件协议常量（core/events.go）。
const (
	EventRun                 = core.EventRun
	EventCancel              = core.EventCancel
	EventCancelled           = core.EventCancelled
	EventToolUseAnswer       = core.EventToolUseAnswer
	EventThoughtStart        = core.EventThoughtStart
	EventThoughtDelta        = core.EventThoughtDelta
	EventThoughtEnd          = core.EventThoughtEnd
	EventContentStart        = core.EventContentStart
	EventContentDelta        = core.EventContentDelta
	EventContentEnd          = core.EventContentEnd
	EventDone                = core.EventDone
	EventError               = core.EventError
	EventHeartbeat           = core.EventHeartbeat
	EventClientToolUseStart  = core.EventClientToolUseStart
	EventClientToolUseEnd    = core.EventClientToolUseEnd
	EventCompactStart        = core.EventCompactStart
	EventCompactEnd          = core.EventCompactEnd
	EventModelFallback       = core.EventModelFallback
	EventModelRetry          = core.EventModelRetry
	EventSoftLanding         = core.EventSoftLanding
	EventTimeout             = core.EventTimeout
	EventTodoUpdate          = core.EventTodoUpdate
	EventToolUseStart        = core.EventToolUseStart
	EventToolUseEnd          = core.EventToolUseEnd
	EventToolConfirmRequest  = core.EventToolConfirmRequest
	EventToolConfirmAnswer   = core.EventToolConfirmAnswer
	EventNoticeDrained       = core.EventNoticeDrained
	EventQueueSend           = core.EventQueueSend
	EventSteerGuided         = core.EventSteerGuided
	EventSteerQueued         = core.EventSteerQueued
	EventSteerRejected       = core.EventSteerRejected
	EventSteerDrained        = core.EventSteerDrained
	EventSteerDiscarded      = core.EventSteerDiscarded
	EventSteerDeliveryChange = core.EventSteerDeliveryChange
)

// ClientMessageReader 是 WS 读端抽象（core 唯一定义处）。
type ClientMessageReader = core.ClientMessageReader

// 客户端消息枢纽与运行期命令箱（internal/hub）。
type (
	clientMessageHub    = hub.ClientMessageHub
	runtimeCommandBox   = hub.RuntimeCommandBox
	runtimeNotification = hub.RuntimeNotification
)

var (
	newClientMessageHub  = hub.NewClientMessageHub
	newRuntimeCommandBox = hub.NewRuntimeCommandBox
)

// hub 侧辅助（通知判定/消息解析）。
var (
	toolUseAnswerID                      = hub.ToolUseAnswerID
	clientToolUseEndIDs                  = hub.ClientToolUseEndIDs
	renderBackgroundDelegateLaunchedText = hub.RenderBackgroundDelegateLaunchedText
	backgroundNotificationStatus         = hub.BackgroundNotificationStatus
	shouldDeliverBackgroundNotification  = hub.ShouldDeliverBackgroundNotification
)

// 工具执行状态词汇（core/events.go）。
const (
	toolExecutionStatusRunning   = core.ToolExecutionStatusRunning
	toolExecutionStatusWaiting   = core.ToolExecutionStatusWaiting
	toolExecutionStatusSuccess   = core.ToolExecutionStatusSuccess
	toolExecutionStatusError     = core.ToolExecutionStatusError
	toolExecutionStatusCancelled = core.ToolExecutionStatusCancelled
	toolExecutionStatusRejected  = core.ToolExecutionStatusRejected
)
