package react

import (
	"react-base-service/components"
	"react-base-service/components/params"
	planService "react-base-service/service/plan"
	reactService "react-base-service/service/react"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// ListSessions 获取 ReAct 历史会话列表
// @Summary ReAct 历史会话列表
// @Description 返回当前用户在 callerKey+routeValues 下的 ReAct 会话列表
// @Tags React
// @Accept json
// @Produce json
// @Param req body params.ReactSessionListReq true "ReAct 会话列表请求体"
// @Success 200 {object} components.DefaultRenderWithTrace{data=params.ReactSessionListResp}
// @Failure 400 {object} components.DefaultRenderWithTrace
// @Router /react/session/list [post]
func ListSessions(ctx *gin.Context) {
	var req params.ReactSessionListReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.ListSessions] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	resp, err := reactService.ListHistorySessions(ctx, req)
	if err != nil {
		zlog.Errorf(ctx, "[React.ListSessions] 查询历史会话失败: err=%v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// GetSessionEvents 获取 ReAct 历史事件流
// @Summary ReAct 历史事件流
// @Description 将指定 session 下的持久化消息还原为与实时协议对齐的事件列表
// @Tags React
// @Accept json
// @Produce json
// @Param req body params.ReactSessionEventsReq true "ReAct 历史事件请求体"
// @Success 200 {object} components.DefaultRenderWithTrace{data=params.ReactSessionEventsResp}
// @Failure 400 {object} components.DefaultRenderWithTrace
// @Router /react/session/events [post]
func GetSessionEvents(ctx *gin.Context) {
	var req params.ReactSessionEventsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.GetSessionEvents] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	resp, err := reactService.GetHistoryEvents(ctx, req)
	if err != nil {
		zlog.Errorf(ctx, "[React.GetSessionEvents] 查询历史事件失败: sessionId=%s, err=%v", req.SessionID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	planEvents, err := planService.SessionHistoryEvents(ctx, req.SessionID)
	if err != nil {
		zlog.Errorf(ctx, "[React.GetSessionEvents] 查询 Plan 历史视图失败: sessionId=%s, err=%v", req.SessionID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	nextSeq := len(resp.Events)
	for i := range planEvents {
		nextSeq++
		planEvents[i].Seq = nextSeq
		resp.Events = append(resp.Events, planEvents[i])
	}
	components.RenderJsonSucc(ctx, resp)
}

// DeleteSession 删除一个会话（硬删：级联删除全部关联数据）
// @Summary 删除 ReAct 会话
// @Description 事务内级联删除该会话的 runs/消息/工具结果/排队输入/反馈/产物/异步任务/Plan 数据；运行中的会话会被拒绝
// @Tags React
// @Accept json
// @Produce json
// @Param req body params.ReactSessionDeleteReq true "会话删除请求体"
// @Success 200 {object} components.DefaultRenderWithTrace{data=params.ReactSessionDeleteResp}
// @Failure 400 {object} components.DefaultRenderWithTrace
// @Router /react/session/delete [post]
func DeleteSession(ctx *gin.Context) {
	var req params.ReactSessionDeleteReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.DeleteSession] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	resp, err := reactService.DeleteReactSession(ctx, req)
	if err != nil {
		zlog.Errorf(ctx, "[React.DeleteSession] 删除会话失败: sessionId=%s, err=%v", req.SessionID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
