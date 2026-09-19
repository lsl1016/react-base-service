package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	reactService "react-base-service/service/react"

	"github.com/gin-gonic/gin"
)

type CommandResult struct {
	View params.PlanPublicView `json:"view"`
}

// RunWithClientReaderContext 启动一次独立 Plan Runtime。
// Planner 先生成并持久化计划；Executor 再逐步创建 Scoped ReactRun 执行 AGENT Step。
func RunWithClientReaderContext(
	ctx *gin.Context,
	parent context.Context,
	payload params.ReactRunPayload,
	sessionID string,
	write reactService.EventWriter,
	readClient reactService.ClientMessageReader,
) (*reactService.RunResult, error) {
	payload.ExecutionMode = params.ReactExecutionModePlan
	prepared, err := reactService.PrepareExternalRun(ctx, payload, sessionID)
	if err != nil {
		return nil, err
	}
	result := &reactService.RunResult{RunID: prepared.RunID, SessionID: prepared.SessionID}
	execution, err := createExecution(ctx, prepared)
	if err != nil {
		_ = reactService.MarkExternalRunState(ctx, prepared, model.ReactRunStateError, err.Error())
		return result, err
	}
	runCtx, cleanup := reactService.BeginPreparedRun(parent, prepared)
	defer cleanup()
	emitter := newRuntimeEmitter(ctx, prepared, write)
	if err := emitter.emitView(execution); err != nil {
		return result, err
	}

	draft, plannerResult, err := generateDraft(ctx, runCtx, prepared, payload.UserPrompt)
	if err != nil {
		return result, failExecution(ctx, prepared, execution, emitter, "planner_failed", err)
	}
	if err := reactService.AddExternalRunUsage(ctx, prepared, plannerResult.InputTokens, plannerResult.OutputTokens, plannerResult.CacheReadTokens, plannerResult.CacheCreateTokens); err != nil {
		return result, failExecution(ctx, prepared, execution, emitter, "planner_usage_failed", err)
	}
	if err := persistInitialDraft(ctx, execution, draft); err != nil {
		return result, failExecution(ctx, prepared, execution, emitter, "persist_plan_failed", err)
	}
	if err := emitter.emitView(execution); err != nil {
		return result, err
	}

	runner := &executor{
		ctx:        ctx,
		runCtx:     runCtx,
		prepared:   prepared,
		execution:  execution,
		emitter:    emitter,
		readClient: readClient,
	}
	err = runner.run()
	if errors.Is(err, errPlanPaused) {
		return result, nil
	}
	if reactService.IsCancellation(err) {
		_ = cancelExecutionState(ctx, prepared, execution, emitter, "用户取消了 Plan")
		return result, err
	}
	if err != nil {
		return result, failExecution(ctx, prepared, execution, emitter, "plan_execution_failed", err)
	}
	return result, nil
}

func failExecution(ctx *gin.Context, prepared *reactService.PreparedExternalRun, execution *model.PlanExecution, emitter *runtimeEmitter, code string, cause error) error {
	summary := compactText(cause.Error(), 1600)
	// 与取消级联（D3）同理：FAILED 终态后不允许残留 PENDING 步骤，
	// 否则公开视图会出现"已失败但还有待执行步骤"的矛盾状态；手工 Retry 会把这些步骤一并复位。
	_ = cancelPendingSteps(ctx, execution, "Plan 失败，后续步骤未执行")
	_ = setExecutionStatus(ctx, execution, model.PlanExecutionStatusFailed, code, summary, true)
	_ = reactService.MarkExternalRunState(ctx, prepared, model.ReactRunStateError, summary)
	_ = emitter.emitView(execution)
	_ = emitter.emit(reactService.EventError, params.ReactErrorPayload{
		ErrNo:  components.ErrorReactRunFailed.ErrNo,
		ErrMsg: summary,
	})
	return cause
}

func cancelExecutionState(ctx *gin.Context, prepared *reactService.PreparedExternalRun, execution *model.PlanExecution, emitter *runtimeEmitter, summary string) error {
	_ = cancelPendingWait(ctx, execution.PlanExecutionID)
	if execution.CurrentStepID != "" {
		if step, _ := getStep(ctx, execution.PlanExecutionID, execution.CurrentStepID); step != nil &&
			(step.Status == model.PlanStepStatusRunning || step.Status == model.PlanStepStatusWaiting) {
			_ = finishStep(ctx, execution, step, model.PlanStepStatusCancelled, summary, "")
		}
	}
	// D3：取消必须级联——后续 PENDING 步骤一并置 CANCELLED，不留孤儿 PENDING 行。
	if err := cancelPendingSteps(ctx, execution, summary); err != nil {
		return err
	}
	if err := setExecutionStatus(ctx, execution, model.PlanExecutionStatusCancelled, "cancelled", summary, true); err != nil {
		return err
	}
	_ = reactService.MarkExternalRunState(ctx, prepared, model.ReactRunStateCancelled, summary)
	_ = emitter.emitView(execution)
	return emitter.emit(reactService.EventCancelled, params.ReactCancelledPayload{OK: true, Reason: summary})
}

func loadOwnedExecution(ctx *gin.Context, planExecutionID string) (*model.PlanExecution, error) {
	execution, err := getExecution(ctx, planExecutionID)
	if err != nil {
		return nil, err
	}
	if execution == nil {
		return nil, components.ErrorParamInvalid.Sprintf("plan execution not found")
	}
	userName := strings.TrimSpace(helpers.GetUserName(ctx))
	if userName == "" || execution.UserName != userName {
		return nil, components.ErrorReactPlaygroundForbidden
	}
	return execution, nil
}

func restoreForCommand(ctx *gin.Context, parent context.Context, execution *model.PlanExecution, write reactService.EventWriter, readClient reactService.ClientMessageReader) (*reactService.PreparedExternalRun, context.Context, func(), *runtimeEmitter, *executor, error) {
	// D2：恢复前校验外层 run 状态。waiting_plan（正常等待）与 expired（被显式批量过期等场景）
	// 允许恢复——expired 属复活路径，Plan 等待的权威状态在 PlanExecution，外层 run 仅是执行载体；
	// finished/cancelled/error 说明 run 已被别的路径收敛，与 Plan 等待态矛盾，应拒绝并引导取消。
	run, err := model.GetReactRunByRunID(ctx, execution.OuterRunID)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if run == nil {
		return nil, nil, nil, nil, nil, components.ErrorParamInvalid.Sprintf("outer run not found")
	}
	switch run.State {
	case model.ReactRunStateFinished, model.ReactRunStateCancelled, model.ReactRunStateError:
		return nil, nil, nil, nil, nil, components.ErrorParamInvalid.Sprintf(
			"outer run already %s while plan is %s; cancel the plan before starting a new one", run.State, execution.Status)
	}
	prepared, err := reactService.RestoreExternalRun(ctx, execution.OuterRunID)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if err := reactService.MarkExternalRunState(ctx, prepared, model.ReactRunStateRunning, ""); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	runCtx, cleanup := reactService.BeginPreparedRun(parent, prepared)
	emitter := newRuntimeEmitter(ctx, prepared, write)
	runner := &executor{ctx: ctx, runCtx: runCtx, prepared: prepared, execution: execution, emitter: emitter, readClient: readClient}
	return prepared, runCtx, cleanup, emitter, runner, nil
}

func Resume(ctx *gin.Context, parent context.Context, req params.PlanResumeReq, write reactService.EventWriter, readClient reactService.ClientMessageReader) (CommandResult, error) {
	execution, err := loadOwnedExecution(ctx, req.PlanExecutionID)
	if err != nil {
		return CommandResult{}, err
	}
	wait, err := getPendingWait(ctx, execution.PlanExecutionID)
	if err != nil {
		return CommandResult{}, err
	}
	if wait == nil || wait.WaitRequestID != strings.TrimSpace(req.WaitRequestID) {
		return CommandResult{}, components.ErrorParamInvalid.Sprintf("wait request not found or already resolved")
	}
	if wait.WaitType == model.PlanWaitTypeUserAction {
		if approved, exists := req.Response["approved"]; exists {
			if value, ok := approved.(bool); ok && !value {
				prepared, err := reactService.RestoreExternalRun(ctx, execution.OuterRunID)
				if err != nil {
					return CommandResult{}, err
				}
				emitter := newRuntimeEmitter(ctx, prepared, write)
				if err := cancelExecutionState(ctx, prepared, execution, emitter, "用户拒绝了待确认操作"); err != nil {
					return CommandResult{}, err
				}
				view, err := buildPublicView(ctx, execution)
				return CommandResult{View: view}, err
			}
		}
	}
	if err := validateWaitResponse(wait, req.Response); err != nil {
		return CommandResult{}, err
	}

	_, _, cleanup, emitter, runner, err := restoreForCommand(ctx, parent, execution, write, readClient)
	if err != nil {
		return CommandResult{}, err
	}
	defer cleanup()
	if _, err := resolveWait(ctx, execution, wait, req.Response); err != nil {
		return CommandResult{}, err
	}
	if err := emitter.emitView(execution); err != nil {
		return CommandResult{}, err
	}
	runErr := runner.run()
	if errors.Is(runErr, errPlanPaused) {
		runErr = nil
	}
	if reactService.IsCancellation(runErr) {
		_ = cancelExecutionState(ctx, runner.prepared, execution, emitter, "用户取消了 Plan")
	}
	if runErr != nil && !reactService.IsCancellation(runErr) {
		_ = failExecution(ctx, runner.prepared, execution, emitter, "plan_resume_failed", runErr)
	}
	fresh, _ := getExecution(ctx, execution.PlanExecutionID)
	if fresh != nil {
		execution = fresh
	}
	view, viewErr := buildPublicView(ctx, execution)
	if runErr != nil {
		return CommandResult{View: view}, runErr
	}
	return CommandResult{View: view}, viewErr
}

func Retry(ctx *gin.Context, parent context.Context, req params.PlanRetryReq, write reactService.EventWriter, readClient reactService.ClientMessageReader) (CommandResult, error) {
	execution, err := loadOwnedExecution(ctx, req.PlanExecutionID)
	if err != nil {
		return CommandResult{}, err
	}
	step, err := getStep(ctx, execution.PlanExecutionID, req.StepID)
	if err != nil {
		return CommandResult{}, err
	}
	if step == nil || step.Status != model.PlanStepStatusFailed {
		return CommandResult{}, components.ErrorParamInvalid.Sprintf("only FAILED step can be retried")
	}
	if err := retryStepAndContinue(ctx, execution, step); err != nil {
		return CommandResult{}, err
	}

	_, _, cleanup, emitter, runner, err := restoreForCommand(ctx, parent, execution, write, readClient)
	if err != nil {
		return CommandResult{}, err
	}
	defer cleanup()
	_ = emitter.emitView(execution)
	runErr := runner.run()
	if errors.Is(runErr, errPlanPaused) {
		runErr = nil
	}
	if reactService.IsCancellation(runErr) {
		_ = cancelExecutionState(ctx, runner.prepared, execution, emitter, "用户取消了 Plan")
	} else if runErr != nil {
		_ = failExecution(ctx, runner.prepared, execution, emitter, "plan_retry_failed", runErr)
	}
	fresh, _ := getExecution(ctx, execution.PlanExecutionID)
	if fresh != nil {
		execution = fresh
	}
	view, viewErr := buildPublicView(ctx, execution)
	if runErr != nil {
		return CommandResult{View: view}, runErr
	}
	return CommandResult{View: view}, viewErr
}

func Skip(ctx *gin.Context, parent context.Context, req params.PlanSkipReq, write reactService.EventWriter, readClient reactService.ClientMessageReader) (CommandResult, error) {
	execution, err := loadOwnedExecution(ctx, req.PlanExecutionID)
	if err != nil {
		return CommandResult{}, err
	}
	step, err := getStep(ctx, execution.PlanExecutionID, req.StepID)
	if err != nil {
		return CommandResult{}, err
	}
	if step == nil {
		return CommandResult{}, components.ErrorParamInvalid.Sprintf("step not found")
	}
	if step.Required {
		return CommandResult{}, components.ErrorParamInvalid.Sprintf("required step cannot be skipped")
	}
	switch step.Status {
	case model.PlanStepStatusPending, model.PlanStepStatusFailed, model.PlanStepStatusWaiting:
	default:
		return CommandResult{}, components.ErrorParamInvalid.Sprintf("step state does not allow skip: %s", step.Status)
	}
	if err := skipStepAndContinue(ctx, execution, step); err != nil {
		return CommandResult{}, err
	}

	_, _, cleanup, emitter, runner, err := restoreForCommand(ctx, parent, execution, write, readClient)
	if err != nil {
		return CommandResult{}, err
	}
	defer cleanup()
	_ = emitter.emitView(execution)
	runErr := runner.run()
	if errors.Is(runErr, errPlanPaused) {
		runErr = nil
	}
	if reactService.IsCancellation(runErr) {
		_ = cancelExecutionState(ctx, runner.prepared, execution, emitter, "用户取消了 Plan")
	} else if runErr != nil {
		_ = failExecution(ctx, runner.prepared, execution, emitter, "plan_skip_failed", runErr)
	}
	fresh, _ := getExecution(ctx, execution.PlanExecutionID)
	if fresh != nil {
		execution = fresh
	}
	view, viewErr := buildPublicView(ctx, execution)
	if runErr != nil {
		return CommandResult{View: view}, runErr
	}
	return CommandResult{View: view}, viewErr
}

func Cancel(ctx *gin.Context, req params.PlanCancelReq, write reactService.EventWriter) (CommandResult, error) {
	execution, err := loadOwnedExecution(ctx, req.PlanExecutionID)
	if err != nil {
		return CommandResult{}, err
	}
	switch execution.Status {
	case model.PlanExecutionStatusSucceeded, model.PlanExecutionStatusCancelled:
		view, viewErr := buildPublicView(ctx, execution)
		return CommandResult{View: view}, viewErr
	}
	prepared, err := reactService.RestoreExternalRun(ctx, execution.OuterRunID)
	if err != nil {
		return CommandResult{}, err
	}
	// 当前仍在执行时先触发 outer context 取消，Step Run 会随父 context 级联收敛；
	// WAIT 状态没有活跃 goroutine，下面的持久化取消直接生效。
	_ = reactService.Cancel(ctx, execution.OuterRunID, execution.SessionID)
	emitter := newRuntimeEmitter(ctx, prepared, write)
	if err := cancelExecutionState(ctx, prepared, execution, emitter, "用户取消了 Plan"); err != nil {
		return CommandResult{}, err
	}
	view, err := buildPublicView(ctx, execution)
	return CommandResult{View: view}, err
}

func validateWaitResponse(wait *model.PlanWait, response map[string]interface{}) error {
	if wait == nil {
		return nil
	}
	if wait.WaitType == model.PlanWaitTypeUserAction {
		if _, ok := response["approved"].(bool); !ok {
			return components.ErrorParamInvalid.Sprintf("USER_ACTION requires boolean approved")
		}
		return nil
	}
	if wait.WaitType != model.PlanWaitTypeUserInput {
		return nil
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(wait.ResponseSchemaJSON), &schema); err != nil {
		return components.ErrorParamInvalid.Sprintf("invalid wait response schema")
	}
	for _, field := range schema.Required {
		value, ok := response[field]
		if !ok || strings.TrimSpace(fmt.Sprint(value)) == "" {
			return components.ErrorParamInvalid.Sprintf("missing required wait response field: %s", field)
		}
	}
	return nil
}
