package core

import "react-base-service/components/params"

// WS/事件协议常量：run 生命周期、流式增量、工具交互、引导与队列事件的唯一权威定义处。
// react 门面经 core_bridge 以同名常量别名复用；子包直接引用 core 导出名。
const (
	EventCancel              = "cancel"
	EventCancelled           = "cancelled"
	EventClientToolUseEnd    = "client_tool_use_end"
	EventClientToolUseStart  = "client_tool_use_start"
	EventCompactEnd          = "compact_end"
	EventCompactStart        = "compact_start"
	EventContentDelta        = "content_delta"
	EventContentEnd          = "content_end"
	EventContentStart        = "content_start"
	EventDone                = "done"
	EventError               = "error"
	EventHeartbeat           = "heartbeat"
	EventModelFallback       = "model_fallback"
	EventModelRetry          = "model_retry"
	EventNoticeDrained       = "notice_drained"
	EventQueueSend           = "queue_send"
	EventRun                 = "run"
	EventSoftLanding         = "soft_landing"
	EventSteerDeliveryChange = "steer_delivery_changed"
	EventSteerDiscarded      = "steer_discarded"
	EventSteerDrained        = "steer_drained"
	EventSteerGuided         = "steer_guided"
	EventSteerQueued         = "steer_queued"
	EventSteerRejected       = "steer_rejected"
	EventThoughtDelta        = "thought_delta"
	EventThoughtEnd          = "thought_end"
	EventThoughtStart        = "thought_start"
	EventTimeout             = "timeout"
	EventTodoUpdate          = "todo_update"
	EventToolConfirmAnswer   = "tool_confirm_answer"
	EventToolConfirmRequest  = "tool_confirm_request"
	EventToolUseAnswer       = "tool_use_answer"
	EventToolUseEnd          = "tool_use_end"
	EventToolUseStart        = "tool_use_start"
)

// ClientMessageReader 是 WS 读端抽象：阻塞读取下一条客户端消息。
type ClientMessageReader func() (params.ReactWSMessage, error)

// 工具执行状态词汇：tool_use_start / tool_use_end 事件的 Status 字段取值。
const (
	ToolExecutionStatusRunning   = "running"
	ToolExecutionStatusWaiting   = "waiting"
	ToolExecutionStatusSuccess   = "success"
	ToolExecutionStatusError     = "error"
	ToolExecutionStatusCancelled = "cancelled"
	ToolExecutionStatusRejected  = "rejected"
)
