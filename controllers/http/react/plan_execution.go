package react

import (
	"react-base-service/components"
	"react-base-service/components/params"
	planService "react-base-service/service/plan"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// GetPlanExecutionDetail 返回 Plan 最新公开视图和全部 StepAttempt 索引。
// Step 详细事件不在此接口一次性展开，前端展开 Attempt 时再按需调用 events 接口。
func GetPlanExecutionDetail(ctx *gin.Context) {
	var req params.PlanExecutionDetailReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := planService.Detail(ctx, req)
	if err != nil {
		zlog.Errorf(ctx, "[Plan.Detail] 查询失败: planExecutionId=%s err=%v", req.PlanExecutionID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// GetPlanStepEvents 返回单个 StepAttempt 对应 ReactRun 的持久化事件。
// Plan Step 的 thought/tool/content 不复制存储，直接从 step_run_id 指向的 ReactRun 历史重建。
func GetPlanStepEvents(ctx *gin.Context) {
	var req params.PlanStepEventsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := planService.StepEvents(ctx, req)
	if err != nil {
		zlog.Errorf(ctx, "[Plan.Events] 查询失败: planExecutionId=%s stepAttemptId=%s err=%v", req.PlanExecutionID, req.StepAttemptID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}


// ResumePlanExecution 解决当前 WaitRequest 并继续执行后续步骤。
// HTTP 入口适合纯服务端步骤；需要 Client Tool/HITL 的交互式恢复优先使用 WebSocket plan_resume。
func ResumePlanExecution(ctx *gin.Context) {
	var req params.PlanResumeReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := planService.Resume(ctx, ctx.Request.Context(), req, nil, nil)
	if err != nil {
		zlog.Errorf(ctx, "[Plan.Resume] 恢复失败: planExecutionId=%s waitRequestId=%s err=%v", req.PlanExecutionID, req.WaitRequestID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// RetryPlanStep 为 FAILED Step 创建新的不可变 Attempt 并继续执行。
func RetryPlanStep(ctx *gin.Context) {
	var req params.PlanRetryReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := planService.Retry(ctx, ctx.Request.Context(), req, nil, nil)
	if err != nil {
		zlog.Errorf(ctx, "[Plan.Retry] 重试失败: planExecutionId=%s stepId=%s err=%v", req.PlanExecutionID, req.StepID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// SkipPlanStep 跳过非 required 的 PENDING/FAILED/WAITING Step，并继续后续步骤。
func SkipPlanStep(ctx *gin.Context) {
	var req params.PlanSkipReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := planService.Skip(ctx, ctx.Request.Context(), req, nil, nil)
	if err != nil {
		zlog.Errorf(ctx, "[Plan.Skip] 跳过失败: planExecutionId=%s stepId=%s err=%v", req.PlanExecutionID, req.StepID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// CancelPlanExecution 取消持久化 Plan；WAIT 状态不依赖原 goroutine，也可直接取消。
func CancelPlanExecution(ctx *gin.Context) {
	var req params.PlanCancelReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := planService.Cancel(ctx, req, nil)
	if err != nil {
		zlog.Errorf(ctx, "[Plan.Cancel] 取消失败: planExecutionId=%s err=%v", req.PlanExecutionID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
