package react

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"react-base-service/components"
	"react-base-service/components/metrics"
	"react-base-service/components/params"
	planService "react-base-service/service/plan"
	reactService "react-base-service/service/react"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

type wsEventWriter struct {
	conn *reactService.WSConn
	seq  int
	mu   sync.Mutex
}

func (w *wsEventWriter) Write(event params.ReactEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	event.Seq = w.seq
	return w.conn.WriteEvent(event)
}

type wsReadResult struct {
	msg params.ReactWSMessage
	err error
}

// WS ReAct WebSocket 单入口
// @Summary ReAct WebSocket
// @Description 建立 ReAct 运行通道，支持 run/cancel 消息。服务端返回 thought_start/thought_delta/thought_end/content_start/content_delta/content_end/done/error/cancelled 事件。
// @Tags React
// @Router /react/ws [get]
func WS(ctx *gin.Context) {
	connectionAttemptID := sanitizeConnectionAttemptID(ctx.Query("connection_attempt_id"))
	conn, err := reactService.Upgrade(ctx)
	if err != nil {
		logWSDiagnostic(ctx, "upgrade_failed", connectionAttemptID, nil, map[string]any{"error": err.Error()})
		return
	}
	logWSDiagnostic(ctx, "connection_open", connectionAttemptID, conn, nil)
	metrics.WSConnections.Inc()
	defer func() {
		metrics.WSConnections.Dec()
		_ = conn.CloseWithCause("handler_exit")
		logWSDiagnostic(ctx, "connection_closed", connectionAttemptID, conn, nil)
	}()

	writer := &wsEventWriter{conn: conn}

	connCtx, connCancel := context.WithCancelCause(ctx.Request.Context())
	defer connCancel(nil)
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go sendWSHeartbeat(ctx, writer.Write, conn, connectionAttemptID, heartbeatDone)

	incoming := make(chan wsReadResult, 16)
	go readWSMessages(conn, incoming)

	var runMsgCh chan params.ReactWSMessage
	var runDone chan struct{}
	for {
		select {
		case item, ok := <-incoming:
			if !ok || item.err != nil {
				if item.err != nil {
					fields := map[string]any{"error": item.err.Error()}
					var readErr *reactService.WSReadEndError
					if errors.As(item.err, &readErr) {
						fields["endKind"] = readErr.Kind
						fields["readStage"] = readErr.Stage
						if readErr.Kind == reactService.WSReadEndCloseFrame {
							fields["closeCode"] = readErr.CloseCode
							fields["closeReason"] = readErr.CloseReason
							fields["closeEchoFailed"] = readErr.Err != nil
						}
					}
					logWSDiagnostic(ctx, "read_end", connectionAttemptID, conn, fields)
				}
				connCancel(reactService.ErrReactClientDisconnected)
				if runMsgCh != nil {
					close(runMsgCh)
					runMsgCh = nil
				}
				if runDone != nil {
					<-runDone
				}
				return
			}
			runMsgCh, runDone = handleWSMessage(ctx, connCtx, writer.Write, item.msg, runMsgCh, runDone)
		case <-runDone:
			if runMsgCh != nil {
				close(runMsgCh)
				runMsgCh = nil
			}
			runDone = nil
		}
	}
}

func readWSMessages(conn *reactService.WSConn, incoming chan<- wsReadResult) {
	defer close(incoming)
	for {
		msg, err := conn.ReadMessage()
		incoming <- wsReadResult{msg: msg, err: err}
		if err != nil {
			return
		}
	}
}

// sendWSHeartbeat 周期发协议层 ping 驱动断连检测（浏览器自动回 pong，读侧靠 pong 刷新读超时），
// 并保持原有 20s 一次的应用层 heartbeat 事件。任一写失败即关闭连接，唤醒阻塞的读协程走断连收敛。
func sendWSHeartbeat(ctx *gin.Context, write reactService.EventWriter, conn *reactService.WSConn, connectionAttemptID string, done <-chan struct{}) {
	const heartbeatEveryNPings = 4
	ticker := time.NewTicker(reactService.WSPingInterval)
	defer ticker.Stop()

	tickCount := 0
	for {
		select {
		case <-done:
			return
		case <-ctx.Request.Context().Done():
			return
		case <-ticker.C:
			if err := conn.WritePing(); err != nil {
				_ = conn.CloseWithCause("ping_write_failed")
				logWSDiagnostic(ctx, "ping_write_failed", connectionAttemptID, conn, map[string]any{"error": err.Error()})
				return
			}
			tickCount++
			if tickCount%heartbeatEveryNPings != 0 {
				continue
			}
			err := write(params.ReactEvent{
				Type:    reactService.EventHeartbeat,
				Payload: params.ReactHeartbeatPayload{Timestamp: time.Now().UnixMilli()},
			})
			if err != nil {
				_ = conn.CloseWithCause("heartbeat_write_failed")
				logWSDiagnostic(ctx, "heartbeat_write_failed", connectionAttemptID, conn, map[string]any{"error": err.Error()})
				return
			}
		}
	}
}

func logWSDiagnostic(ctx *gin.Context, event, connectionAttemptID string, conn *reactService.WSConn, fields map[string]any) {
	payload := map[string]any{
		"timestamp":           time.Now().UTC().Format(time.RFC3339Nano),
		"event":               event,
		"connectionAttemptId": connectionAttemptID,
		"logId":               zlog.GetLogID(ctx),
	}
	if conn != nil {
		payload["connection"] = conn.Diagnostics()
	}
	for key, value := range fields {
		payload[key] = value
	}
	data, err := json.Marshal(payload)
	if err != nil {
		zlog.Infof(ctx, "[React.WS] diagnostic_marshal_failed: event=%s err=%v", event, err)
		return
	}
	zlog.Infof(ctx, "[React.WS] diagnostic=%s", data)
}

func sanitizeConnectionAttemptID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 128 {
		return ""
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return ""
	}
	return value
}

func handleWSMessage(ctx *gin.Context, connCtx context.Context, write reactService.EventWriter, msg params.ReactWSMessage, runMsgCh chan params.ReactWSMessage, runDone chan struct{}) (chan params.ReactWSMessage, chan struct{}) {
	switch strings.TrimSpace(msg.Type) {
	case reactService.EventRun:
		if runMsgCh != nil {
			// Steering S1：run 活跃不再硬拒绝——新用户消息走准入（guide/queue/明确拒绝），
			// 回执以 steer_guided/steer_queued/steer_rejected 事件下发；Steering 未开启时保持
			// 历史错误事件行为。
			reactService.HandleWSSteer(ctx, write, msg)
			return runMsgCh, runDone
		}
		return startWSRun(ctx, connCtx, write, msg)
	case planService.EventPlanResume, planService.EventPlanRetry, planService.EventPlanSkip, planService.EventPlanCancel:
		// Plan WAIT 的最后一条 view 事件可能先于 run goroutine 的结束通知到达客户端。
		// 用户立即点击确认时，先检查上一段执行是否其实已经结束，避免把合法 Resume 误判成并发 Run。
		if runMsgCh != nil && runDone != nil {
			select {
			case <-runDone:
				close(runMsgCh)
				runMsgCh = nil
				runDone = nil
			default:
			}
		}
		if runMsgCh != nil {
			_ = write(params.ReactEvent{Type: reactService.EventError, RunID: msg.RunID, SessionID: msg.SessionID, Payload: params.ReactErrorPayload{ErrNo: components.ErrorReactRunFailed.ErrNo, ErrMsg: "plan command rejected while a run is active"}})
			return runMsgCh, runDone
		}
		return startWSPlanCommand(ctx, connCtx, write, msg)
	case reactService.EventQueueSend:
		// 队列管理 S3：显式发送一条排队输入（claim-once 晋升 + 当前连接开新 run）。
		// 与 EventRun 互斥：已有活跃 run 时拒绝（排队项留在队列，由自动续跑或再次发送消费）。
		if runMsgCh != nil {
			_ = write(params.ReactEvent{Type: reactService.EventError, RunID: msg.RunID, SessionID: msg.SessionID, Payload: params.ReactErrorPayload{ErrNo: components.ErrorReactRunFailed.ErrNo, ErrMsg: "queue_send rejected while a run is active"}})
			return runMsgCh, runDone
		}
		var queueSend params.ReactQueueSendReq
		if err := json.Unmarshal(msg.Payload, &queueSend); err != nil {
			_ = write(params.ReactEvent{Type: reactService.EventError, SessionID: msg.SessionID, Payload: params.ReactErrorPayload{ErrNo: components.ErrorParamInvalid.ErrNo, ErrMsg: err.Error()}})
			return runMsgCh, runDone
		}
		payload, pendingID, err := reactService.PrepareQueuedRunPayload(ctx, queueSend.SessionID, queueSend.PendingInputID)
		if err != nil {
			zlog.Infof(ctx, "[React.WS] queue_send 晋升失败: sessionId=%s, pendingInputId=%s, err=%v", queueSend.SessionID, queueSend.PendingInputID, err)
			_ = write(params.ReactEvent{Type: reactService.EventError, SessionID: queueSend.SessionID, Payload: params.ReactErrorPayload{ErrNo: components.ErrorParamInvalid.ErrNo, ErrMsg: err.Error()}})
			return runMsgCh, runDone
		}
		// 先回 steer_drained（与 S2 自动续跑同词汇：该输入已离开队列、成为新 run 的用户输入），
		// claim-once 由 createReactRunContext 事务内的条件更新兜底（双端并发发送只成功一次）。
		_ = write(params.ReactEvent{Type: reactService.EventSteerDrained, SessionID: queueSend.SessionID, Payload: params.ReactSteerDrainedPayload{PendingInputID: pendingID}})
		payloadBytes, _ := json.Marshal(payload)
		return startWSRun(ctx, connCtx, write, params.ReactWSMessage{Type: reactService.EventRun, SessionID: queueSend.SessionID, Payload: payloadBytes})
	case reactService.EventCancel:
		if err := reactService.Cancel(ctx, msg.RunID, msg.SessionID); err != nil {
			_ = write(params.ReactEvent{Type: reactService.EventError, RunID: msg.RunID, SessionID: msg.SessionID, Payload: params.ReactErrorPayload{ErrNo: components.ErrorReactRunFailed.ErrNo, ErrMsg: err.Error()}})
			return runMsgCh, runDone
		}
		forwardWSRunMessage(runMsgCh, msg)
	case reactService.EventClientToolUseEnd, reactService.EventToolUseAnswer, reactService.EventToolConfirmAnswer:
		if runMsgCh == nil {
			_ = write(params.ReactEvent{Type: reactService.EventError, RunID: msg.RunID, SessionID: msg.SessionID, Payload: params.ReactErrorPayload{ErrNo: components.ErrorParamInvalid.ErrNo, ErrMsg: "no active react run"}})
			return runMsgCh, runDone
		}
		forwardWSRunMessage(runMsgCh, msg)
	default:
		_ = write(params.ReactEvent{Type: reactService.EventError, RunID: msg.RunID, SessionID: msg.SessionID, Payload: params.ReactErrorPayload{ErrNo: components.ErrorParamInvalid.ErrNo, ErrMsg: "unsupported message type"}})
	}
	return runMsgCh, runDone
}

// dispatchRunExecution 按 payload.ExecutionMode 把一次 run 分流到对应 Runtime（D1）。
// 分流必须位于 controller 层而不是 react 包内：依赖方向是 plan → react（plan 复用 ReAct 引擎），
// react 不能反向 import plan；这里是两个 Runtime 共同的上层调用方。
// 当前唯一 run 入口是 WS；未来若增加 HTTP run 入口，必须复用本函数而不是各自 switch。
func dispatchRunExecution(
	ctx *gin.Context,
	connCtx context.Context,
	payload params.ReactRunPayload,
	sessionID string,
	write reactService.EventWriter,
	readClient reactService.ClientMessageReader,
) (*reactService.RunResult, error) {
	switch payload.ExecutionMode {
	case "", params.ReactExecutionModeReact:
		return reactService.RunWithClientReaderContext(ctx, connCtx, payload, sessionID, write, readClient)
	case params.ReactExecutionModePlan:
		return planService.RunWithClientReaderContext(ctx, connCtx, payload, sessionID, write, readClient)
	default:
		return nil, components.ErrorParamInvalid.Sprintf("unsupported executionMode: %s", payload.ExecutionMode)
	}
}

func startWSRun(ctx *gin.Context, connCtx context.Context, write reactService.EventWriter, msg params.ReactWSMessage) (chan params.ReactWSMessage, chan struct{}) {
	var payload params.ReactRunPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		_ = write(params.ReactEvent{Type: reactService.EventError, Payload: params.ReactErrorPayload{ErrNo: components.ErrorParamInvalid.ErrNo, ErrMsg: err.Error()}})
		return nil, nil
	}

	runMsgCh := make(chan params.ReactWSMessage, 16)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		readClient := func() (params.ReactWSMessage, error) {
			clientMsg, ok := <-runMsgCh
			if !ok {
				return params.ReactWSMessage{}, reactService.ErrReactClientDisconnected
			}
			return clientMsg, nil
		}
		result, err := dispatchRunExecution(ctx, connCtx, payload, msg.SessionID, write, readClient)
		if err == nil || reactService.IsReactRunCancelled(err) || reactService.IsReactClientDisconnected(err) {
			return
		}
		// Steering 准入回执（跨连接 run 冲突场景）：guided/queued/rejected 是准入结论，
		// 不是运行失败，按 steering 回执事件下发。
		if receipt, ok := reactService.SteerReceiptFromError(err); ok {
			reactService.EmitSteerReceipt(write, receipt, msg.SessionID)
			return
		}

		zlog.Errorf(ctx, "[React.WS] run失败: err=%v", err)
		if result != nil {
			return
		}
		errorEvent := params.ReactEvent{
			Type:      reactService.EventError,
			RunID:     msg.RunID,
			SessionID: msg.SessionID,
			Payload:   params.ReactErrorPayload{ErrNo: components.ErrorReactRunFailed.ErrNo, ErrMsg: err.Error()},
		}
		_ = write(errorEvent)
	}()
	return runMsgCh, runDone
}

func startWSPlanCommand(ctx *gin.Context, connCtx context.Context, write reactService.EventWriter, msg params.ReactWSMessage) (chan params.ReactWSMessage, chan struct{}) {
	runMsgCh := make(chan params.ReactWSMessage, 16)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		readClient := func() (params.ReactWSMessage, error) {
			clientMsg, ok := <-runMsgCh
			if !ok {
				return params.ReactWSMessage{}, reactService.ErrReactClientDisconnected
			}
			return clientMsg, nil
		}

		var err error
		switch strings.TrimSpace(msg.Type) {
		case planService.EventPlanResume:
			var req params.PlanResumeReq
			if err = json.Unmarshal(msg.Payload, &req); err == nil {
				_, err = planService.Resume(ctx, connCtx, req, write, readClient)
			}
		case planService.EventPlanRetry:
			var req params.PlanRetryReq
			if err = json.Unmarshal(msg.Payload, &req); err == nil {
				_, err = planService.Retry(ctx, connCtx, req, write, readClient)
			}
		case planService.EventPlanSkip:
			var req params.PlanSkipReq
			if err = json.Unmarshal(msg.Payload, &req); err == nil {
				_, err = planService.Skip(ctx, connCtx, req, write, readClient)
			}
		case planService.EventPlanCancel:
			var req params.PlanCancelReq
			if err = json.Unmarshal(msg.Payload, &req); err == nil {
				_, err = planService.Cancel(ctx, req, write)
			}
		default:
			err = components.ErrorParamInvalid.Sprintf("unsupported plan command: %s", msg.Type)
		}
		if err == nil || reactService.IsReactRunCancelled(err) || reactService.IsReactClientDisconnected(err) {
			return
		}
		zlog.Errorf(ctx, "[Plan.WS] 命令执行失败: type=%s err=%v", msg.Type, err)
		_ = write(params.ReactEvent{
			Type:      reactService.EventError,
			RunID:     msg.RunID,
			SessionID: msg.SessionID,
			Payload:   params.ReactErrorPayload{ErrNo: components.ErrorReactRunFailed.ErrNo, ErrMsg: err.Error()},
		})
	}()
	return runMsgCh, runDone
}

func forwardWSRunMessage(runMsgCh chan params.ReactWSMessage, msg params.ReactWSMessage) {
	if runMsgCh == nil {
		return
	}
	select {
	case runMsgCh <- msg:
	default:
	}
}
