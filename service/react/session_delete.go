package react

// 会话删除（硬删）：单事务内级联删除该会话的全部关联数据。
//
// 约束与纪律：
//   - 归属校验复用 validateReactSessionContext 五元组口径——只能删自己的会话；
//   - 有活跃 run（running/waiting_*/cancelling）的会话一律拒绝：删除运行中的会话
//     涉及取消时序与事件流竞争，由用户先停止任务再删除；
//   - 一期不动物理文件（上传附件、沙箱产物只删 DB 行）：文件记录可能被多处引用，
//     物理清理需要引用计数，留待后续；
//   - Steering 排队账本随 tblLlmReactPendingInput 一并删除，队列不会残留孤儿。

import (
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"react-base-service/golib/zlog"
)

// DeleteReactSession 删除一个会话及其全部关联数据（硬删，事务级联）。
func DeleteReactSession(ctx *gin.Context, req params.ReactSessionDeleteReq) (params.ReactSessionDeleteResp, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return params.ReactSessionDeleteResp{}, components.ErrorParamInvalid.Sprintf("sessionId 不能为空")
	}

	resp := params.ReactSessionDeleteResp{}
	err := model.GetLLMDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 行锁 + 存在性
		session, err := model.GetReactSessionBySessionIDForUpdate(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if session == nil {
			return components.ErrorReactSessionNotFound.Sprintf(sessionID)
		}
		// 归属校验（与 list/events 同口径）
		if err := validateReactSessionContext(session, helpers.GetUserName(ctx), req.CallerKey, marshalRouteValuesForValidate(req.RouteValues), normalizeSessionType("")); err != nil {
			return err
		}
		// 活跃 run 一律拒绝：避免与正在执行的 run goroutine 竞争
		active, err := model.HasActiveReactRunWithDB(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if active {
			return components.ErrorParamInvalid.Sprintf("会话有正在运行的任务，请先停止后再删除")
		}

		// Plan 子表按 plan_execution_id 关联：先收集本会话的执行 ID，再删子表
		var planExecutionIDs []string
		if err := tx.Model(&model.PlanExecution{}).WithContext(ctx).
			Where("session_id = ?", sessionID).
			Pluck("plan_execution_id", &planExecutionIDs).Error; err != nil {
			return components.ErrorDbUpdate.Wrap(err)
		}
		planRows, err := deletePlanCascade(ctx, tx, sessionID, planExecutionIDs)
		if err != nil {
			return err
		}
		resp.PlanRows = planRows

		// 会话直关联表
		var err2 error
		if resp.ToolResults, err2 = deleteBySession(ctx, tx, &model.ReactToolResult{}, sessionID); err2 != nil {
			return err2
		}
		if resp.PendingInputs, err2 = deleteBySession(ctx, tx, &model.ReactPendingInput{}, sessionID); err2 != nil {
			return err2
		}
		if resp.Feedbacks, err2 = deleteBySession(ctx, tx, &model.ReactRunFeedback{}, sessionID); err2 != nil {
			return err2
		}
		if resp.Artifacts, err2 = deleteBySession(ctx, tx, &model.ReactArtifact{}, sessionID); err2 != nil {
			return err2
		}
		if resp.AsyncTasks, err2 = deleteBySession(ctx, tx, &model.ReactAsyncTask{}, sessionID); err2 != nil {
			return err2
		}
		if resp.Messages, err2 = deleteBySession(ctx, tx, &model.ReactMessage{}, sessionID); err2 != nil {
			return err2
		}
		if resp.Runs, err2 = deleteBySession(ctx, tx, &model.ReactRun{}, sessionID); err2 != nil {
			return err2
		}
		// 会话行最后删
		if _, err2 = deleteBySession(ctx, tx, &model.ReactSession{}, sessionID); err2 != nil {
			return err2
		}
		resp.Deleted = true
		return nil
	})
	if err != nil {
		return params.ReactSessionDeleteResp{}, err
	}
	zlog.Infof(ctx, "[React.Session] 会话已删除: sessionId=%s, runs=%d, messages=%d, planRows=%d",
		sessionID, resp.Runs, resp.Messages, resp.PlanRows)
	return resp, nil
}

// deleteBySession 按会话删除一张表的关联行，返回删除行数。
func deleteBySession(ctx *gin.Context, tx *gorm.DB, dest interface{}, sessionID string) (int64, error) {
	result := tx.WithContext(ctx).Where("session_id = ?", sessionID).Delete(dest)
	if result.Error != nil {
		return 0, components.ErrorDbUpdate.Wrap(result.Error)
	}
	return result.RowsAffected, nil
}

// deletePlanCascade 删除 Plan 直系与子表（Version/Step/StepAttempt/StepResult/Wait 按
// plan_execution_id 关联），返回删除总行数。
func deletePlanCascade(ctx *gin.Context, tx *gorm.DB, sessionID string, planExecutionIDs []string) (int64, error) {
	var total int64
	// 子表：按 plan_execution_id IN (...) 删
	childTables := []interface{}{
		&model.PlanVersion{}, &model.PlanStep{}, &model.PlanStepAttempt{},
		&model.PlanStepResult{}, &model.PlanWait{},
	}
	if len(planExecutionIDs) > 0 {
		for _, dest := range childTables {
			result := tx.WithContext(ctx).Where("plan_execution_id IN ?", planExecutionIDs).Delete(dest)
			if result.Error != nil {
				return 0, components.ErrorDbUpdate.Wrap(result.Error)
			}
			total += result.RowsAffected
		}
	}
	// 直系：按 session_id 删
	result := tx.WithContext(ctx).Where("session_id = ?", sessionID).Delete(&model.PlanExecution{})
	if result.Error != nil {
		return 0, components.ErrorDbUpdate.Wrap(result.Error)
	}
	total += result.RowsAffected
	return total, nil
}
