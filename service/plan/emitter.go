package plan

import (
	"sync"

	"react-base-service/components/params"
	model "react-base-service/models/llm"
	reactService "react-base-service/service/react"

	"github.com/gin-gonic/gin"
)

const (
	EventPlanViewUpdate = "plan_view_update"
	EventPlanStepEvent  = "plan_step_event"
	EventPlanResume     = "plan_resume"
	EventPlanRetry      = "plan_retry"
	EventPlanSkip       = "plan_skip"
	EventPlanCancel     = "plan_cancel"
)

type runtimeEmitter struct {
	ctx       *gin.Context
	runID     string
	sessionID string
	write     reactService.EventWriter
	mu        sync.Mutex
}

func newRuntimeEmitter(ctx *gin.Context, prepared *reactService.PreparedExternalRun, write reactService.EventWriter) *runtimeEmitter {
	if write == nil {
		write = func(params.ReactEvent) error { return nil }
	}
	return &runtimeEmitter{ctx: ctx, runID: prepared.RunID, sessionID: prepared.SessionID, write: write}
}

func (e *runtimeEmitter) emit(eventType string, payload any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.write(params.ReactEvent{
		Type:      eventType,
		RunID:     e.runID,
		SessionID: e.sessionID,
		Payload:   payload,
	})
}

func (e *runtimeEmitter) emitView(execution *model.PlanExecution) error {
	view, err := buildPublicView(e.ctx, execution)
	if err != nil {
		return err
	}
	return e.emit(EventPlanViewUpdate, params.ReactPlanViewUpdatePayload{
		PlanExecutionID: execution.PlanExecutionID,
		View:            view,
	})
}

func (e *runtimeEmitter) stepWriter(execution *model.PlanExecution, step *model.PlanStep, attempt *model.PlanStepAttempt) reactService.EventWriter {
	return func(event params.ReactEvent) error {
		if attempt.StepRunID == "" && event.RunID != "" {
			attempt.StepRunID = event.RunID
			_ = bindAttemptRun(e.ctx, attempt.StepAttemptID, event.RunID)
		}

		// Client Tool 必须额外向顶层透出 start/end：SDK 的 ToolExecutor 只监听顶层事件。
		// Plan 卡片本身仍通过 plan_step_event 保存完整执行明细。
		switch event.Type {
		case reactService.EventClientToolUseStart:
			if payload, ok := event.Payload.(params.ReactClientToolUseStartPayload); ok {
				payload.PlanExecutionID = execution.PlanExecutionID
				payload.StepAttemptID = attempt.StepAttemptID
				if err := e.emit(reactService.EventClientToolUseStart, payload); err != nil {
					return err
				}
			}
		case reactService.EventClientToolUseEnd:
			if payload, ok := event.Payload.(params.ReactClientToolUseEndPayload); ok {
				payload.PlanExecutionID = execution.PlanExecutionID
				payload.StepAttemptID = attempt.StepAttemptID
				if err := e.emit(reactService.EventClientToolUseEnd, payload); err != nil {
					return err
				}
			}
		}

		return e.emit(EventPlanStepEvent, params.ReactPlanStepEventPayload{
			PlanExecutionID: execution.PlanExecutionID,
			PlanVersionID:   step.PlanVersionID,
			StepID:          step.StepID,
			StepOrder:       step.StepOrder,
			StepAttemptID:   attempt.StepAttemptID,
			AttemptNo:       attempt.AttemptNo,
			StepRunID:       event.RunID,
			Event:           event,
		})
	}
}
