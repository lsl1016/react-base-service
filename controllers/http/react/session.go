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

// ForkSession 基于历史会话分叉出一个新会话（复制截断点之前的全部历史）
// @Summary 分叉 ReAct 会话
// @Description 以 throughMessageId 为分叉点，把该点之前的历史（按 run 边界对齐）复制成一个全新会话：全部业务 ID 重生成、compact 游标重写、created_at 保序；运行中的会话会被拒绝
// @Tags React
// @Accept json
// @Produce json
// @Param req body params.ReactSessionForkReq true "会话分叉请求体"
// @Success 200 {object} components.DefaultRenderWithTrace{data=params.ReactSessionForkResp}
// @Failure 400 {object} components.DefaultRenderWithTrace
// @Router /react/session/fork [post]
func ForkSession(ctx *gin.Context) {
	var req params.ReactSessionForkReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.ForkSession] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	resp, err := reactService.ForkReactSession(ctx, req)
	if err != nil {
		zlog.Errorf(ctx, "[React.ForkSession] 分叉会话失败: sessionId=%s, throughMessageId=%s, err=%v", req.SessionID, req.ThroughMessageID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// RenameSession 重命名一个会话（仅标题元数据）
// @Summary 重命名 ReAct 会话
// @Description 更新会话标题：去首尾空白并按 rune 截断；归属五元组校验与其它会话接口同口径；不限制运行中的会话
// @Tags React
// @Accept json
// @Produce json
// @Param req body params.ReactSessionRenameReq true "会话重命名请求体"
// @Success 200 {object} components.DefaultRenderWithTrace{data=params.ReactSessionRenameResp}
// @Failure 400 {object} components.DefaultRenderWithTrace
// @Router /react/session/rename [post]
func RenameSession(ctx *gin.Context) {
	var req params.ReactSessionRenameReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.RenameSession] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	resp, err := reactService.RenameReactSession(ctx, req)
	if err != nil {
		zlog.Errorf(ctx, "[React.RenameSession] 重命名会话失败: sessionId=%s, err=%v", req.SessionID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
