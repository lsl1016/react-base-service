package plan

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	reactService "react-base-service/service/react"

	"github.com/gin-gonic/gin"
)

const publicTimeFormat = time.RFC3339

func executionVisibleTo(ctx *gin.Context, execution *model.PlanExecution, sessionID, callerKey string) bool {
	return execution != nil &&
		execution.SessionID == strings.TrimSpace(sessionID) &&
		execution.CallerKey == strings.TrimSpace(callerKey) &&
		execution.UserName == strings.TrimSpace(helpers.GetUserName(ctx))
}

func Detail(ctx *gin.Context, req params.PlanExecutionDetailReq) (params.PlanExecutionDetailResp, error) {
	execution, err := getExecution(ctx, req.PlanExecutionID)
	if err != nil {
		return params.PlanExecutionDetailResp{}, err
	}
	if !executionVisibleTo(ctx, execution, req.SessionID, req.CallerKey) {
		return params.PlanExecutionDetailResp{}, components.ErrorParamInvalid.Sprintf("plan execution not found")
	}
	view, err := buildPublicView(ctx, execution)
	if err != nil {
		return params.PlanExecutionDetailResp{}, err
	}
	attemptRows, err := listAttempts(ctx, execution.PlanExecutionID)
	if err != nil {
		return params.PlanExecutionDetailResp{}, err
	}
	attempts := make([]params.PlanAttemptItem, 0, len(attemptRows))
	for _, item := range attemptRows {
		attempts = append(attempts, params.PlanAttemptItem{
			PlanExecutionID: item.PlanExecutionID,
			StepID:          item.StepID,
			StepAttemptID:   item.StepAttemptID,
			AttemptNo:       item.AttemptNo,
			Status:          item.Status,
			StepRunID:       item.StepRunID,
			ErrorCode:       item.ErrorCode,
			ErrorSummary:    item.ErrorSummary,
			CreatedAt:       item.CreatedAt.Format(publicTimeFormat),
			UpdatedAt:       item.UpdatedAt.Format(publicTimeFormat),
		})
	}
	return params.PlanExecutionDetailResp{View: view, Attempts: attempts}, nil
}

func StepEvents(ctx *gin.Context, req params.PlanStepEventsReq) (params.PlanStepEventsResp, error) {
	execution, err := getExecution(ctx, req.PlanExecutionID)
	if err != nil {
		return params.PlanStepEventsResp{}, err
	}
	if !executionVisibleTo(ctx, execution, req.SessionID, req.CallerKey) {
		return params.PlanStepEventsResp{}, components.ErrorParamInvalid.Sprintf("plan execution not found")
	}
	attempt, err := getAttempt(ctx, execution.PlanExecutionID, req.StepAttemptID)
	if err != nil {
		return params.PlanStepEventsResp{}, err
	}
	if attempt == nil {
		return params.PlanStepEventsResp{}, components.ErrorParamInvalid.Sprintf("plan attempt not found")
	}
	events := make([]params.ReactHistoryEvent, 0)
	if strings.TrimSpace(attempt.StepRunID) != "" {
		events, err = reactService.GetRunHistoryEvents(ctx, attempt.StepRunID)
		if err != nil {
			return params.PlanStepEventsResp{}, err
		}
	}
	return params.PlanStepEventsResp{
		PlanExecutionID: execution.PlanExecutionID,
		StepAttemptID:   attempt.StepAttemptID,
		StepID:          attempt.StepID,
		AttemptNo:       attempt.AttemptNo,
		Events:          events,
	}, nil
}

func buildPublicView(ctx *gin.Context, execution *model.PlanExecution) (params.PlanPublicView, error) {
	version, err := getVersion(ctx, execution.CurrentVersionID)
	if err != nil {
		return params.PlanPublicView{}, err
	}
	steps, err := listSteps(ctx, execution.PlanExecutionID, execution.CurrentVersionID)
	if err != nil {
		return params.PlanPublicView{}, err
	}
	publicSteps := make([]params.PlanStepPublicView, 0, len(steps))
	var current *params.PlanStepPublicView
	for _, step := range steps {
		publicStatus := step.Status
		if step.Status == model.PlanStepStatusWaiting && step.StepID == execution.CurrentStepID {
			publicStatus = execution.Status
		}
		item := params.PlanStepPublicView{
			StepID:    step.StepID,
			StepOrder: step.StepOrder,
			StepName:  step.StepName,
			StepType:  step.StepType,
			Required:  step.Required,
			Status:    publicStatus,
			Summary:   step.ResultSummary,
		}
		if result, resultErr := getLatestStepResult(ctx, step.StepID); resultErr != nil {
			return params.PlanPublicView{}, resultErr
		} else if result != nil {
			item.StepResultRef = &params.PlanStepResultRef{
				PlanExecutionID: result.PlanExecutionID,
				PlanVersionID:   result.PlanVersionID,
				StepID:          result.StepID,
				StepAttemptID:   result.StepAttemptID,
				StepResultID:    result.StepResultID,
			}
		}
		publicSteps = append(publicSteps, item)
		if step.StepID == execution.CurrentStepID {
			copyItem := item
			current = &copyItem
		}
	}

	var waitView *params.PlanWaitRequest
	if wait, waitErr := getPendingWait(ctx, execution.PlanExecutionID); waitErr != nil {
		return params.PlanPublicView{}, waitErr
	} else if wait != nil {
		waitView = &params.PlanWaitRequest{
			RequestID: wait.WaitRequestID,
			Type:      wait.WaitType,
			Question:  wait.Question,
		}
		_ = json.Unmarshal([]byte(wait.ResponseSchemaJSON), &waitView.ResponseSchema)
		_ = json.Unmarshal([]byte(wait.DataJSON), &waitView.Data)
	}

	var result map[string]any
	if strings.TrimSpace(execution.ResultJSON) != "" {
		_ = json.Unmarshal([]byte(execution.ResultJSON), &result)
	}
	var publicErr *params.PlanPublicError
	if execution.ErrorCode != "" || execution.ErrorSummary != "" {
		publicErr = &params.PlanPublicError{Code: execution.ErrorCode, Summary: execution.ErrorSummary}
	}
	summary := strings.TrimSpace(execution.Summary)
	if summary == "" && version != nil {
		summary = strings.TrimSpace(version.Title)
	}
	canResume := execution.Status == model.PlanExecutionStatusWaitUserInput ||
		execution.Status == model.PlanExecutionStatusWaitUserAction ||
		execution.Status == model.PlanExecutionStatusWaitExternalTask
	return params.PlanPublicView{
		PlanExecutionID: execution.PlanExecutionID,
		Status:          execution.Status,
		Summary:         summary,
		Steps:           publicSteps,
		CurrentStep:     current,
		Result:          result,
		WaitRequest:     waitView,
		Error:           publicErr,
		CanResume:       canResume,
		UpdatedAt:       execution.UpdatedAt.Format(publicTimeFormat),
	}, nil
}

func MustPublicViewJSON(ctx *gin.Context, execution *model.PlanExecution) string {
	view, err := buildPublicView(ctx, execution)
	if err != nil {
		return fmt.Sprintf(`{"plan_execution_id":%q,"status":"FAILED","summary":"plan view unavailable","steps":[],"can_resume":false}`, execution.PlanExecutionID)
	}
	data, _ := json.Marshal(view)
	return string(data)
}


// SessionHistoryEvents 返回当前用户会话内每个 Plan 的最新公开视图。
// 主会话历史只保存一个最新 Plan 快照；Attempt 的完整事件继续由 /plan_execution/events 按需加载。
func SessionHistoryEvents(ctx *gin.Context, sessionID string) ([]params.ReactHistoryEvent, error) {
	executions, err := listExecutionsBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	userName := strings.TrimSpace(helpers.GetUserName(ctx))
	events := make([]params.ReactHistoryEvent, 0, len(executions))
	for _, execution := range executions {
		if execution.UserName != userName {
			continue
		}
		view, err := buildPublicView(ctx, &execution)
		if err != nil {
			return nil, err
		}
		events = append(events, params.ReactHistoryEvent{
			Type:      EventPlanViewUpdate,
			RunID:     execution.OuterRunID,
			SessionID: execution.SessionID,
			Payload: params.ReactPlanViewUpdatePayload{
				PlanExecutionID: execution.PlanExecutionID,
				View:            view,
			},
			CreatedAt: execution.CreatedAt.Format(publicTimeFormat),
		})
	}
	return events, nil
}
