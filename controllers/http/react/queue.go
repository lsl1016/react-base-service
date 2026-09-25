package react

// S3 队列管理 API（见 service/react/queue_manager.go）：
// 会话排队输入（Steering S2/S3 账本 queued 行）的查询/编辑/重排/删除。
// 显式发送（晋升队列项开新 run）走 WS queue_send 消息——需要同一连接的实时事件流，
// 不提供 HTTP 入口（HTTP 无法回传 run 事件）。

import (
	"react-base-service/components"
	"react-base-service/components/params"
	reactService "react-base-service/service/react"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// ListQueue 获取会话队列
// @Summary ReAct 会话队列查询
// @Description 返回指定会话的排队输入（FIFO）与 steering 队列配置回显
// @Tags React
// @Accept json
// @Produce json
// @Param req body params.ReactQueueListReq true "队列查询请求体"
// @Success 200 {object} components.DefaultRenderWithTrace{data=params.ReactQueueListResp}
// @Failure 400 {object} components.DefaultRenderWithTrace
// @Router /react/queue/list [post]
func ListQueue(ctx *gin.Context) {
	var req params.ReactQueueListReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.ListQueue] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := reactService.ListSessionQueue(ctx, req)
	if err != nil {
		zlog.Infof(ctx, "[React.ListQueue] 查询失败: sessionId=%s, err=%v", req.SessionID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// UpdateQueueItem 编辑排队输入内容
// @Summary ReAct 会话队列编辑
// @Description 编辑一条排队输入的内容；已被晋升/作废时 claimed=false
// @Tags React
// @Accept json
// @Produce json
// @Param req body params.ReactQueueUpdateReq true "队列编辑请求体"
// @Success 200 {object} components.DefaultRenderWithTrace{data=params.ReactQueueMutateResp}
// @Failure 400 {object} components.DefaultRenderWithTrace
// @Router /react/queue/update [post]
func UpdateQueueItem(ctx *gin.Context) {
	var req params.ReactQueueUpdateReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.UpdateQueueItem] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := reactService.UpdateQueuedItem(ctx, req)
	if err != nil {
		zlog.Infof(ctx, "[React.UpdateQueueItem] 编辑失败: sessionId=%s, id=%d, err=%v", req.SessionID, req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// ReorderQueue 重排会话队列
// @Summary ReAct 会话队列重排
// @Description 按期望顺序重排全部排队输入，同步改写账本 seq；请求非法/并发状态变化时报错
// @Tags React
// @Accept json
// @Produce json
// @Param req body params.ReactQueueReorderReq true "队列重排请求体"
// @Success 200 {object} components.DefaultRenderWithTrace{data=params.ReactQueueMutateResp}
// @Failure 400 {object} components.DefaultRenderWithTrace
// @Router /react/queue/reorder [post]
func ReorderQueue(ctx *gin.Context) {
	var req params.ReactQueueReorderReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.ReorderQueue] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := reactService.ReorderSessionQueue(ctx, req)
	if err != nil {
		zlog.Infof(ctx, "[React.ReorderQueue] 重排失败: sessionId=%s, err=%v", req.SessionID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// DeleteQueueItem 删除（取消）排队输入
// @Summary ReAct 会话队列删除
// @Description 将一条排队输入置 cancelled；已被晋升/作废时 claimed=false
// @Tags React
// @Accept json
// @Produce json
// @Param req body params.ReactQueueDeleteReq true "队列删除请求体"
// @Success 200 {object} components.DefaultRenderWithTrace{data=params.ReactQueueMutateResp}
// @Failure 400 {object} components.DefaultRenderWithTrace
// @Router /react/queue/delete [post]
func DeleteQueueItem(ctx *gin.Context) {
	var req params.ReactQueueDeleteReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.DeleteQueueItem] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := reactService.DeleteQueuedItem(ctx, req)
	if err != nil {
		zlog.Infof(ctx, "[React.DeleteQueueItem] 删除失败: sessionId=%s, id=%d, err=%v", req.SessionID, req.ID, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
