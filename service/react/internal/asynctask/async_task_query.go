// Package asynctask 承载异步任务的会话级查询 API（列表/游标）。
// 提交与 resolve 走 react 门面（依赖引擎状态），本包只做纯查询。
package asynctask

import (
	"encoding/base64"
	"fmt"
	core "react-base-service/service/react/internal/core"
	"strconv"
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// ListSessionAsyncTasks 返回会话下第三方已终态但本地尚未处理的异步任务，并指示是否仍有执行中任务。
func ListSessionAsyncTasks(ctx *gin.Context, req params.ReactAsyncTaskListReq) (params.ReactAsyncTaskListResp, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return params.ReactAsyncTaskListResp{}, components.ParamInvalidf("sessionId不能为空")
	}
	session, err := model.GetReactSessionBySessionID(ctx, sessionID)
	if err != nil {
		return params.ReactAsyncTaskListResp{}, err
	}
	if session == nil {
		return params.ReactAsyncTaskListResp{}, components.ErrorReactSessionNotFound.Sprintf(sessionID)
	}
	if !core.CanViewReactSessionHistory(helpers.GetUserName(ctx), session.UserName) {
		return params.ReactAsyncTaskListResp{}, components.ErrorReactPlaygroundForbidden
	}

	afterID, err := decodeAsyncTaskCursor(req.Cursor)
	if err != nil {
		return params.ReactAsyncTaskListResp{}, components.ParamInvalidf("cursor无效")
	}
	if err := model.ExpireReactAsyncTasksBySession(ctx, sessionID); err != nil {
		return params.ReactAsyncTaskListResp{}, err
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 200 {
		pageSize = 200
	}
	tasks, err := model.ListReadyReactAsyncTasksBySessionPage(ctx, sessionID, afterID, pageSize+1)
	if err != nil {
		return params.ReactAsyncTaskListResp{}, err
	}
	hasProcessingTasks, err := model.HasProcessingReactAsyncTasks(ctx, sessionID)
	if err != nil {
		return params.ReactAsyncTaskListResp{}, err
	}
	hasMore := len(tasks) > pageSize
	if hasMore {
		tasks = tasks[:pageSize]
	}
	items := make([]params.ReactAsyncTaskItem, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, toReactAsyncTaskItem(task))
	}
	resp := params.ReactAsyncTaskListResp{Tasks: items, HasMore: hasMore, HasProcessingTasks: hasProcessingTasks}
	if hasMore && len(tasks) > 0 {
		resp.NextCursor = encodeAsyncTaskCursor(tasks[len(tasks)-1].ID)
	}
	return resp, nil
}

func toReactAsyncTaskItem(task model.ReactAsyncTask) params.ReactAsyncTaskItem {
	item := params.ReactAsyncTaskItem{
		ToolUseID:       task.ToolUseID,
		ToolName:        task.ToolName,
		State:           task.State,
		ExecutionStatus: task.ExecutionStatus,
		ProviderStatus:  task.ProviderStatus,
		Progress:        task.Progress,
		ErrorMessage:    task.ErrorMessage,
		CreatedAt:       core.FormatHistoryTime(task.CreatedAt),
		UpdatedAt:       core.FormatHistoryTime(task.UpdatedAt),
	}
	if task.LastObservedAt != nil {
		item.LastObservedAt = core.FormatHistoryTime(*task.LastObservedAt)
	}
	if task.CompletedAt != nil {
		item.CompletedAt = core.FormatHistoryTime(*task.CompletedAt)
	}
	return item
}

func encodeAsyncTaskCursor(id uint) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(id), 10)))
}

func decodeAsyncTaskCursor(cursor string) (uint, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(string(decoded), 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return uint(value), nil
}
