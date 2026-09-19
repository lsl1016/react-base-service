package plan

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"react-base-service/components"
	model "react-base-service/models/llm"
	reactService "react-base-service/service/react"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func getExecution(ctx *gin.Context, planExecutionID string) (*model.PlanExecution, error) {
	var item model.PlanExecution
	err := model.GetLLMDB().WithContext(ctx).Where("plan_execution_id = ?", strings.TrimSpace(planExecutionID)).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &item, nil
}

func listExecutionsBySession(ctx *gin.Context, sessionID string) ([]model.PlanExecution, error) {
	var items []model.PlanExecution
	err := model.GetLLMDB().WithContext(ctx).
		Where("session_id = ?", strings.TrimSpace(sessionID)).
		Order("created_at ASC, id ASC").Find(&items).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return items, nil
}

func getVersion(ctx *gin.Context, planVersionID string) (*model.PlanVersion, error) {
	if strings.TrimSpace(planVersionID) == "" {
		return nil, nil
	}
	var item model.PlanVersion
	err := model.GetLLMDB().WithContext(ctx).Where("plan_version_id = ?", strings.TrimSpace(planVersionID)).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &item, nil
}

func listSteps(ctx *gin.Context, planExecutionID, planVersionID string) ([]model.PlanStep, error) {
	if strings.TrimSpace(planVersionID) == "" {
		return []model.PlanStep{}, nil
	}
	var items []model.PlanStep
	err := model.GetLLMDB().WithContext(ctx).
		Where("plan_execution_id = ? AND plan_version_id = ?", planExecutionID, planVersionID).
		Order("step_order ASC, id ASC").Find(&items).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return items, nil
}

func getStep(ctx *gin.Context, planExecutionID, stepID string) (*model.PlanStep, error) {
	var item model.PlanStep
	err := model.GetLLMDB().WithContext(ctx).
		Where("plan_execution_id = ? AND step_id = ?", planExecutionID, stepID).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &item, nil
}

func nextPendingStep(ctx *gin.Context, execution *model.PlanExecution) (*model.PlanStep, error) {
	if execution == nil || strings.TrimSpace(execution.CurrentVersionID) == "" {
		return nil, nil
	}
	var item model.PlanStep
	err := model.GetLLMDB().WithContext(ctx).
		Where("plan_execution_id = ? AND plan_version_id = ? AND status = ?",
			execution.PlanExecutionID, execution.CurrentVersionID, model.PlanStepStatusPending).
		Order("step_order ASC, id ASC").First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &item, nil
}

func listAttempts(ctx *gin.Context, planExecutionID string) ([]model.PlanStepAttempt, error) {
	var items []model.PlanStepAttempt
	err := model.GetLLMDB().WithContext(ctx).
		Where("plan_execution_id = ?", planExecutionID).
		Order("created_at ASC, id ASC").Find(&items).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return items, nil
}

func listStepAttempts(ctx *gin.Context, stepID string) ([]model.PlanStepAttempt, error) {
	var items []model.PlanStepAttempt
	err := model.GetLLMDB().WithContext(ctx).
		Where("step_id = ?", stepID).
		Order("attempt_no ASC, id ASC").Find(&items).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return items, nil
}

func getAttempt(ctx *gin.Context, planExecutionID, stepAttemptID string) (*model.PlanStepAttempt, error) {
	var item model.PlanStepAttempt
	err := model.GetLLMDB().WithContext(ctx).
		Where("plan_execution_id = ? AND step_attempt_id = ?", planExecutionID, stepAttemptID).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &item, nil
}

func getLatestStepResult(ctx *gin.Context, stepID string) (*model.PlanStepResult, error) {
	var item model.PlanStepResult
	err := model.GetLLMDB().WithContext(ctx).
		Where("step_id = ?", stepID).
		Order("id DESC").First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &item, nil
}

func getPendingWait(ctx *gin.Context, planExecutionID string) (*model.PlanWait, error) {
	var item model.PlanWait
	err := model.GetLLMDB().WithContext(ctx).
		Where("plan_execution_id = ? AND status = ?", planExecutionID, model.PlanWaitStatusPending).
		Order("id DESC").First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &item, nil
}

func createExecution(ctx *gin.Context, prepared *reactService.PreparedExternalRun) (*model.PlanExecution, error) {
	item := &model.PlanExecution{
		PlanExecutionID: newID("plan_exec_"),
		SessionID: prepared.SessionID,
		OuterRunID: prepared.RunID,
		CallerKey: prepared.CallerKey,
		UserName: prepared.UserName,
		Status: model.PlanExecutionStatusPlanning,
		ExecutionMode: "plan",
		Summary: "正在生成执行计划",
	}
	if err := model.GetLLMDB().WithContext(ctx).Create(item).Error; err != nil {
		return nil, components.ErrorDbInsert.Wrap(err)
	}
	return item, nil
}

func persistInitialDraft(ctx *gin.Context, execution *model.PlanExecution, draft PlanDraft) error {
	return model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		versionID := newID("plan_ver_")
		version := &model.PlanVersion{
			PlanVersionID: versionID,
			PlanExecutionID: execution.PlanExecutionID,
			VersionNo: 1,
			Title: draft.Title,
			Overview: draft.Overview,
			Reason: "initial",
			CreatedBy: execution.OuterRunID,
		}
		if err := tx.Create(version).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
		for index, draftStep := range draft.Steps {
			deps, _ := json.Marshal(draftStep.DependsOn)
			criteria, _ := json.Marshal(draftStep.SuccessCriteria)
			step := &model.PlanStep{
				StepID: newID("plan_step_"),
				PlanExecutionID: execution.PlanExecutionID,
				PlanVersionID: versionID,
				StepOrder: index + 1,
				StepKey: draftStep.StepKey,
				StepName: draftStep.Name,
				Instruction: draftStep.Instruction,
				ExpectedOutput: draftStep.ExpectedOutput,
				SuccessCriteria: string(criteria),
				Status: model.PlanStepStatusPending,
				StepType: draftStep.StepType,
				Required: stepRequired(draftStep),
				MaxAttempts: draftStep.MaxAttempts,
				DependsOnJSON: string(deps),
				InputJSON: encodeDraftStep(draftStep),
			}
			if err := tx.Create(step).Error; err != nil {
				return components.ErrorDbInsert.Wrap(err)
			}
		}
		if err := tx.Model(&model.PlanExecution{}).
			Where("plan_execution_id = ? AND status = ?", execution.PlanExecutionID, model.PlanExecutionStatusPlanning).
			Updates(map[string]any{
				"current_version_id": versionID,
				"status": model.PlanExecutionStatusRunning,
				"summary": draft.Title,
				"error_code": "",
				"error_summary": "",
			}).Error; err != nil {
			return components.ErrorDbUpdate.Wrap(err)
		}
		execution.CurrentVersionID = versionID
		execution.Status = model.PlanExecutionStatusRunning
		execution.Summary = draft.Title
		return nil
	})
}

var errPlanStepClaimConflict = errors.New("plan step claim conflict")

func claimStep(ctx *gin.Context, execution *model.PlanExecution, step *model.PlanStep) (bool, error) {
	now := time.Now()
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// V1 只允许线性执行：先 CAS 占用 Plan 的 current_step_id，再领取 Step。
		// 两个并发 Resume 即使同时读到同一个 PENDING Step，也只有一个事务能成功占用执行槽。
		planUpdate := tx.Model(&model.PlanExecution{}).
			Where("plan_execution_id = ? AND status = ? AND current_step_id = ''",
				execution.PlanExecutionID, model.PlanExecutionStatusRunning).
			Updates(map[string]any{
				"current_step_id": step.StepID,
				"error_code":      "",
				"error_summary":   "",
				"finished_at":     nil,
			})
		if planUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(planUpdate.Error)
		}
		if planUpdate.RowsAffected != 1 {
			return errPlanStepClaimConflict
		}

		stepUpdate := tx.Model(&model.PlanStep{}).
			Where("step_id = ? AND plan_execution_id = ? AND status = ?",
				step.StepID, execution.PlanExecutionID, model.PlanStepStatusPending).
			Updates(map[string]any{"status": model.PlanStepStatusRunning, "started_at": now})
		if stepUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(stepUpdate.Error)
		}
		if stepUpdate.RowsAffected != 1 {
			return errPlanStepClaimConflict
		}
		return nil
	})
	if errors.Is(err, errPlanStepClaimConflict) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	step.Status = model.PlanStepStatusRunning
	step.StartedAt = &now
	execution.Status = model.PlanExecutionStatusRunning
	execution.CurrentStepID = step.StepID
	execution.FinishedAt = nil
	return true, nil
}

func createAttempt(ctx *gin.Context, execution *model.PlanExecution, step *model.PlanStep, attemptNo int) (*model.PlanStepAttempt, error) {
	now := time.Now()
	item := &model.PlanStepAttempt{
		StepAttemptID: newID("plan_attempt_"),
		PlanExecutionID: execution.PlanExecutionID,
		PlanVersionID: step.PlanVersionID,
		StepID: step.StepID,
		AttemptNo: attemptNo,
		Status: model.PlanAttemptStatusRunning,
		StartedAt: &now,
	}
	if err := model.GetLLMDB().WithContext(ctx).Create(item).Error; err != nil {
		return nil, components.ErrorDbInsert.Wrap(err)
	}
	return item, nil
}

func bindAttemptRun(ctx *gin.Context, attemptID, runID string) error {
	if err := model.GetLLMDB().WithContext(ctx).Model(&model.PlanStepAttempt{}).
		Where("step_attempt_id = ?", attemptID).
		Update("step_run_id", runID).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	return nil
}

func finishAttempt(ctx *gin.Context, attemptID, status, errorCode, errorSummary string) error {
	now := time.Now()
	if err := model.GetLLMDB().WithContext(ctx).Model(&model.PlanStepAttempt{}).
		Where("step_attempt_id = ?", attemptID).
		Updates(map[string]any{
			"status": status,
			"error_code": errorCode,
			"error_summary": errorSummary,
			"finished_at": now,
		}).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	return nil
}

func createStepResult(ctx *gin.Context, execution *model.PlanExecution, step *model.PlanStep, attempt *model.PlanStepAttempt, status, summary string, result any) (*model.PlanStepResult, error) {
	data, _ := json.Marshal(result)
	item := &model.PlanStepResult{
		StepResultID: newID("plan_result_"),
		PlanExecutionID: execution.PlanExecutionID,
		PlanVersionID: step.PlanVersionID,
		StepID: step.StepID,
		StepAttemptID: attempt.StepAttemptID,
		Status: status,
		Summary: summary,
		ResultJSON: string(data),
	}
	if err := model.GetLLMDB().WithContext(ctx).Create(item).Error; err != nil {
		return nil, components.ErrorDbInsert.Wrap(err)
	}
	return item, nil
}

func finishStep(ctx *gin.Context, execution *model.PlanExecution, step *model.PlanStep, status, summary, resultRef string) error {
	now := time.Now()
	if err := model.GetLLMDB().WithContext(ctx).Model(&model.PlanStep{}).
		Where("step_id = ?", step.StepID).
		Updates(map[string]any{
			"status": status,
			"result_summary": summary,
			"result_ref": resultRef,
			"finished_at": now,
		}).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	step.Status = status
	step.ResultSummary = summary
	step.ResultRef = resultRef
	step.FinishedAt = &now
	return nil
}

func resetStepForRetry(ctx *gin.Context, step *model.PlanStep) error {
	if err := model.GetLLMDB().WithContext(ctx).Model(&model.PlanStep{}).
		Where("step_id = ? AND status = ?", step.StepID, model.PlanStepStatusFailed).
		Updates(map[string]any{
			"status": model.PlanStepStatusPending,
			"result_summary": "",
			"result_ref": "",
			"finished_at": nil,
		}).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	step.Status = model.PlanStepStatusPending
	return nil
}

func createWait(ctx *gin.Context, execution *model.PlanExecution, step *model.PlanStep, attempt *model.PlanStepAttempt, waitType, question string, responseSchema map[string]interface{}, data any) (*model.PlanWait, error) {
	schemaJSON, _ := json.Marshal(responseSchema)
	dataJSON, _ := json.Marshal(data)
	item := &model.PlanWait{
		WaitRequestID: newID("plan_wait_"),
		PlanExecutionID: execution.PlanExecutionID,
		StepID: step.StepID,
		WaitType: waitType,
		Question: question,
		ResponseSchemaJSON: string(schemaJSON),
		DataJSON: string(dataJSON),
		Status: model.PlanWaitStatusPending,
	}
	if attempt != nil {
		item.StepAttemptID = attempt.StepAttemptID
	}
	if err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(item).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
		planStatus := model.PlanExecutionStatusWaitUserInput
		if waitType == model.PlanWaitTypeUserAction {
			planStatus = model.PlanExecutionStatusWaitUserAction
		} else if waitType == model.PlanWaitTypeExternalTask {
			planStatus = model.PlanExecutionStatusWaitExternalTask
		}
		if err := tx.Model(&model.PlanStep{}).Where("step_id = ?", step.StepID).
			Update("status", model.PlanStepStatusWaiting).Error; err != nil {
			return components.ErrorDbUpdate.Wrap(err)
		}
		if err := tx.Model(&model.PlanExecution{}).Where("plan_execution_id = ?", execution.PlanExecutionID).
			Updates(map[string]any{"status": planStatus, "current_step_id": step.StepID}).Error; err != nil {
			return components.ErrorDbUpdate.Wrap(err)
		}
		execution.Status = planStatus
		execution.CurrentStepID = step.StepID
		step.Status = model.PlanStepStatusWaiting
		return nil
	}); err != nil {
		return nil, err
	}
	return item, nil
}

func resolveWait(ctx *gin.Context, execution *model.PlanExecution, wait *model.PlanWait, response map[string]interface{}) (*model.PlanStep, error) {
	step, err := getStep(ctx, execution.PlanExecutionID, wait.StepID)
	if err != nil {
		return nil, err
	}
	if step == nil {
		return nil, components.ErrorParamInvalid.Sprintf("wait step not found")
	}
	responseJSON, _ := json.Marshal(response)
	summary := "用户已确认继续"
	if wait.WaitType == model.PlanWaitTypeUserInput {
		summary = "用户补充信息: " + compactJSON(responseJSON, 800)
	}
	now := time.Now()
	syntheticAttempt := &model.PlanStepAttempt{
		StepAttemptID:   newID("plan_attempt_"),
		PlanExecutionID: execution.PlanExecutionID,
		PlanVersionID:   step.PlanVersionID,
		StepID:          step.StepID,
		AttemptNo:       1,
		Status:          model.PlanAttemptStatusSucceeded,
		StartedAt:       &now,
		FinishedAt:      &now,
	}
	stepResult := &model.PlanStepResult{
		StepResultID:    newID("plan_result_"),
		PlanExecutionID: execution.PlanExecutionID,
		PlanVersionID:   step.PlanVersionID,
		StepID:          step.StepID,
		StepAttemptID:   syntheticAttempt.StepAttemptID,
		Status:          model.PlanStepStatusSucceeded,
		Summary:         summary,
		ResultJSON:      string(responseJSON),
	}

	err = model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		waitUpdate := tx.Model(&model.PlanWait{}).
			Where("wait_request_id = ? AND plan_execution_id = ? AND status = ?",
				wait.WaitRequestID, execution.PlanExecutionID, model.PlanWaitStatusPending).
			Updates(map[string]any{
				"status":        model.PlanWaitStatusResolved,
				"response_json": string(responseJSON),
				"resolved_at":   now,
			})
		if waitUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(waitUpdate.Error)
		}
		if waitUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("wait request already resolved")
		}
		if err := tx.Create(syntheticAttempt).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
		if err := tx.Create(stepResult).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
		stepUpdate := tx.Model(&model.PlanStep{}).
			Where("step_id = ? AND plan_execution_id = ? AND status = ?",
				step.StepID, execution.PlanExecutionID, model.PlanStepStatusWaiting).
			Updates(map[string]any{
				"status":         model.PlanStepStatusSucceeded,
				"result_summary": summary,
				"result_ref":     stepResult.StepResultID,
				"finished_at":    now,
			})
		if stepUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(stepUpdate.Error)
		}
		if stepUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("wait step is no longer waiting")
		}
		planUpdate := tx.Model(&model.PlanExecution{}).
			Where("plan_execution_id = ? AND current_step_id = ?", execution.PlanExecutionID, step.StepID).
			Updates(map[string]any{
				"status":          model.PlanExecutionStatusRunning,
				"current_step_id": "",
				"error_code":      "",
				"error_summary":   "",
				"finished_at":     nil,
			})
		if planUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(planUpdate.Error)
		}
		if planUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("plan current step changed")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	step.Status = model.PlanStepStatusSucceeded
	step.ResultSummary = summary
	step.ResultRef = stepResult.StepResultID
	step.FinishedAt = &now
	execution.Status = model.PlanExecutionStatusRunning
	execution.CurrentStepID = ""
	execution.ErrorCode = ""
	execution.ErrorSummary = ""
	execution.FinishedAt = nil
	return step, nil
}

func completeAgentStepSuccess(ctx *gin.Context, execution *model.PlanExecution, step *model.PlanStep, attempt *model.PlanStepAttempt, summary string, result any) (*model.PlanStepResult, error) {
	now := time.Now()
	data, _ := json.Marshal(result)
	stepResult := &model.PlanStepResult{
		StepResultID:    newID("plan_result_"),
		PlanExecutionID: execution.PlanExecutionID,
		PlanVersionID:   step.PlanVersionID,
		StepID:          step.StepID,
		StepAttemptID:   attempt.StepAttemptID,
		Status:          model.PlanStepStatusSucceeded,
		Summary:         summary,
		ResultJSON:      string(data),
	}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attemptUpdate := tx.Model(&model.PlanStepAttempt{}).
			Where("step_attempt_id = ? AND status = ?", attempt.StepAttemptID, model.PlanAttemptStatusRunning).
			Updates(map[string]any{
				"status":        model.PlanAttemptStatusSucceeded,
				"error_code":    "",
				"error_summary": "",
				"finished_at":   now,
			})
		if attemptUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(attemptUpdate.Error)
		}
		if attemptUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("plan attempt is no longer running")
		}
		if err := tx.Create(stepResult).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
		stepUpdate := tx.Model(&model.PlanStep{}).
			Where("step_id = ? AND status = ?", step.StepID, model.PlanStepStatusRunning).
			Updates(map[string]any{
				"status":         model.PlanStepStatusSucceeded,
				"result_summary": summary,
				"result_ref":     stepResult.StepResultID,
				"finished_at":    now,
			})
		if stepUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(stepUpdate.Error)
		}
		if stepUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("plan step is no longer running")
		}
		planUpdate := tx.Model(&model.PlanExecution{}).
			Where("plan_execution_id = ? AND current_step_id = ?", execution.PlanExecutionID, step.StepID).
			Update("current_step_id", "")
		if planUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(planUpdate.Error)
		}
		if planUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("plan current step changed")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	attempt.Status = model.PlanAttemptStatusSucceeded
	attempt.FinishedAt = &now
	step.Status = model.PlanStepStatusSucceeded
	step.ResultSummary = summary
	step.ResultRef = stepResult.StepResultID
	step.FinishedAt = &now
	execution.CurrentStepID = ""
	return stepResult, nil
}

func completeAgentAttemptFailure(ctx *gin.Context, execution *model.PlanExecution, step *model.PlanStep, attempt *model.PlanStepAttempt, errorSummary string, terminal bool, result any) error {
	now := time.Now()
	data, _ := json.Marshal(result)
	stepResult := &model.PlanStepResult{
		StepResultID:    newID("plan_result_"),
		PlanExecutionID: execution.PlanExecutionID,
		PlanVersionID:   step.PlanVersionID,
		StepID:          step.StepID,
		StepAttemptID:   attempt.StepAttemptID,
		Status:          model.PlanStepStatusFailed,
		Summary:         errorSummary,
		ResultJSON:      string(data),
	}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attemptUpdate := tx.Model(&model.PlanStepAttempt{}).
			Where("step_attempt_id = ? AND status = ?", attempt.StepAttemptID, model.PlanAttemptStatusRunning).
			Updates(map[string]any{
				"status":        model.PlanAttemptStatusFailed,
				"error_code":    "step_run_failed",
				"error_summary": errorSummary,
				"finished_at":   now,
			})
		if attemptUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(attemptUpdate.Error)
		}
		if attemptUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("plan attempt is no longer running")
		}
		if err := tx.Create(stepResult).Error; err != nil {
			return components.ErrorDbInsert.Wrap(err)
		}
		if !terminal {
			return nil
		}
		stepUpdate := tx.Model(&model.PlanStep{}).
			Where("step_id = ? AND status = ?", step.StepID, model.PlanStepStatusRunning).
			Updates(map[string]any{
				"status":         model.PlanStepStatusFailed,
				"result_summary": errorSummary,
				"result_ref":     stepResult.StepResultID,
				"finished_at":    now,
			})
		if stepUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(stepUpdate.Error)
		}
		if stepUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("plan step is no longer running")
		}
		planUpdate := tx.Model(&model.PlanExecution{}).
			Where("plan_execution_id = ? AND current_step_id = ?", execution.PlanExecutionID, step.StepID).
			Updates(map[string]any{
				"status":        model.PlanExecutionStatusFailed,
				"error_code":    "step_failed",
				"error_summary": errorSummary,
				"finished_at":   now,
			})
		if planUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(planUpdate.Error)
		}
		if planUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("plan current step changed")
		}
		return nil
	})
	if err != nil {
		return err
	}
	attempt.Status = model.PlanAttemptStatusFailed
	attempt.ErrorCode = "step_run_failed"
	attempt.ErrorSummary = errorSummary
	attempt.FinishedAt = &now
	if terminal {
		step.Status = model.PlanStepStatusFailed
		step.ResultSummary = errorSummary
		step.ResultRef = stepResult.StepResultID
		step.FinishedAt = &now
		execution.Status = model.PlanExecutionStatusFailed
		execution.ErrorCode = "step_failed"
		execution.ErrorSummary = errorSummary
		execution.FinishedAt = &now
	}
	return nil
}

func retryStepAndContinue(ctx *gin.Context, execution *model.PlanExecution, step *model.PlanStep) error {
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		stepUpdate := tx.Model(&model.PlanStep{}).
			Where("step_id = ? AND plan_execution_id = ? AND status = ?",
				step.StepID, execution.PlanExecutionID, model.PlanStepStatusFailed).
			Updates(map[string]any{
				"status":         model.PlanStepStatusPending,
				"result_summary": "",
				"result_ref":     "",
				"finished_at":    nil,
			})
		if stepUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(stepUpdate.Error)
		}
		if stepUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("only FAILED step can be retried")
		}
		planUpdate := tx.Model(&model.PlanExecution{}).
			Where("plan_execution_id = ? AND status = ?", execution.PlanExecutionID, model.PlanExecutionStatusFailed).
			Updates(map[string]any{
				"status":          model.PlanExecutionStatusRunning,
				"current_step_id": "",
				"error_code":      "",
				"error_summary":   "",
				"finished_at":     nil,
			})
		if planUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(planUpdate.Error)
		}
		if planUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("plan is not retryable")
		}
		return nil
	})
	if err != nil {
		return err
	}
	step.Status = model.PlanStepStatusPending
	step.ResultSummary = ""
	step.ResultRef = ""
	step.FinishedAt = nil
	execution.Status = model.PlanExecutionStatusRunning
	execution.CurrentStepID = ""
	execution.ErrorCode = ""
	execution.ErrorSummary = ""
	execution.FinishedAt = nil
	return nil
}

func skipStepAndContinue(ctx *gin.Context, execution *model.PlanExecution, step *model.PlanStep) error {
	now := time.Now()
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if step.Status == model.PlanStepStatusWaiting {
			if err := tx.Model(&model.PlanWait{}).
				Where("plan_execution_id = ? AND step_id = ? AND status = ?",
					execution.PlanExecutionID, step.StepID, model.PlanWaitStatusPending).
				Updates(map[string]any{"status": model.PlanWaitStatusCancelled, "resolved_at": now}).Error; err != nil {
				return components.ErrorDbUpdate.Wrap(err)
			}
		}
		stepUpdate := tx.Model(&model.PlanStep{}).
			Where("step_id = ? AND plan_execution_id = ? AND status IN ?",
				step.StepID, execution.PlanExecutionID,
				[]string{model.PlanStepStatusPending, model.PlanStepStatusFailed, model.PlanStepStatusWaiting}).
			Updates(map[string]any{
				"status":         model.PlanStepStatusSkipped,
				"result_summary": "用户跳过此步骤",
				"result_ref":     "",
				"finished_at":    now,
			})
		if stepUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(stepUpdate.Error)
		}
		if stepUpdate.RowsAffected != 1 {
			return components.ErrorParamInvalid.Sprintf("step state does not allow skip")
		}
		planUpdate := tx.Model(&model.PlanExecution{}).
			Where("plan_execution_id = ?", execution.PlanExecutionID).
			Updates(map[string]any{
				"status":          model.PlanExecutionStatusRunning,
				"current_step_id": "",
				"error_code":      "",
				"error_summary":   "",
				"finished_at":     nil,
			})
		if planUpdate.Error != nil {
			return components.ErrorDbUpdate.Wrap(planUpdate.Error)
		}
		return nil
	})
	if err != nil {
		return err
	}
	step.Status = model.PlanStepStatusSkipped
	step.ResultSummary = "用户跳过此步骤"
	step.ResultRef = ""
	step.FinishedAt = &now
	execution.Status = model.PlanExecutionStatusRunning
	execution.CurrentStepID = ""
	execution.ErrorCode = ""
	execution.ErrorSummary = ""
	execution.FinishedAt = nil
	return nil
}

func setExecutionStatus(ctx *gin.Context, execution *model.PlanExecution, status, errorCode, errorSummary string, terminal bool) error {
	updates := map[string]any{"status": status, "error_code": errorCode, "error_summary": errorSummary}
	if terminal {
		updates["finished_at"] = time.Now()
	}
	if err := model.GetLLMDB().WithContext(ctx).Model(&model.PlanExecution{}).
		Where("plan_execution_id = ?", execution.PlanExecutionID).
		Updates(updates).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	execution.Status = status
	execution.ErrorCode = errorCode
	execution.ErrorSummary = errorSummary
	return nil
}

func setExecutionResult(ctx *gin.Context, execution *model.PlanExecution, result map[string]interface{}) error {
	data, _ := json.Marshal(result)
	now := time.Now()
	if err := model.GetLLMDB().WithContext(ctx).Model(&model.PlanExecution{}).
		Where("plan_execution_id = ?", execution.PlanExecutionID).
		Updates(map[string]any{
			"status": model.PlanExecutionStatusSucceeded,
			"result_json": string(data),
			"current_step_id": "",
			"finished_at": now,
			"error_code": "",
			"error_summary": "",
		}).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	execution.Status = model.PlanExecutionStatusSucceeded
	execution.ResultJSON = string(data)
	execution.CurrentStepID = ""
	execution.FinishedAt = &now
	return nil
}

func cancelPendingWait(ctx *gin.Context, planExecutionID string) error {
	now := time.Now()
	if err := model.GetLLMDB().WithContext(ctx).Model(&model.PlanWait{}).
		Where("plan_execution_id = ? AND status = ?", planExecutionID, model.PlanWaitStatusPending).
		Updates(map[string]any{"status": model.PlanWaitStatusCancelled, "resolved_at": now}).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	return nil
}

// cancelPendingSteps 把计划中剩余 PENDING 步骤批量置 CANCELLED。
// 取消语义要求级联（D3）：Plan 终态为 CANCELLED 后不允许残留 PENDING 行，
// 否则公开视图和历史审计都会出现"已取消但还有待执行步骤"的矛盾状态。
func cancelPendingSteps(ctx *gin.Context, execution *model.PlanExecution, summary string) error {
	now := time.Now()
	if err := model.GetLLMDB().WithContext(ctx).Model(&model.PlanStep{}).
		Where("plan_execution_id = ? AND plan_version_id = ? AND status = ?",
			execution.PlanExecutionID, execution.CurrentVersionID, model.PlanStepStatusPending).
		Updates(map[string]any{
			"status":         model.PlanStepStatusCancelled,
			"result_summary": summary,
			"finished_at":    now,
		}).Error; err != nil {
		return components.ErrorDbUpdate.Wrap(err)
	}
	return nil
}

func compactJSON(data []byte, limit int) string {
	value := strings.TrimSpace(string(data))
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
