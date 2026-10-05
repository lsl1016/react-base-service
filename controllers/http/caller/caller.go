package caller

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	callerService "react-base-service/service/caller"

	"github.com/gin-gonic/gin"
	"react-base-service/golib/zlog"
)

// RegisterCaller 注册Caller
// @Summary      注册Caller
// @Description  注册一个新的调用方（Caller），callerKey全局唯一
// @Tags         caller
// @Accept       json
// @Produce      json
// @Param        req  body     params.RegisterCallerReq  true  "Caller注册请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.CallerResp}  "成功返回Caller信息"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或callerKey已存在"
// @Router       /caller/register [post]
func RegisterCaller(ctx *gin.Context) {
	var req params.RegisterCallerReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Caller.Register] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)

	caller, err := callerService.RegisterCaller(ctx, req.CallerKey, req.Name, req.Description, req.Platform, req.AllowPlan, userName)
	if err != nil {
		zlog.Errorf(ctx, "[Caller.Register] 注册失败: callerKey=%s, err=%v", req.CallerKey, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, params.CallerResp{
		CallerKey:   caller.CallerKey,
		Name:        caller.Name,
		Description: caller.Description,
		Platform:    caller.Platform,
		AllowPlan:   caller.AllowPlan,
		Status:      caller.Status,
		CreatedAt:   caller.CreatedAt.Format("2006-01-02 15:04:05"),
	})
}

// UpdateCaller 更新Caller
// @Summary      更新Caller
// @Description  更新已有Caller的名称、描述、平台或状态
// @Tags         caller
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateCallerReq  true  "Caller更新请求体"
// @Success      200  {object} components.DefaultRenderWithTrace  "更新成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或Caller不存在"
// @Router       /caller/update [post]
func UpdateCaller(ctx *gin.Context) {
	var req params.UpdateCallerReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Caller.Update] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	updates := make(map[string]interface{})
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Description != "" {
		updates["description"] = req.Description
	}
	if req.Platform != "" {
		updates["platform"] = req.Platform
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.AllowPlan != nil {
		updates["allow_plan"] = *req.AllowPlan
	}

	if err := callerService.UpdateCaller(ctx, req.CallerKey, updates); err != nil {
		zlog.Errorf(ctx, "[Caller.Update] 更新失败: callerKey=%s, err=%v", req.CallerKey, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, gin.H{})
}

// ListCallers 获取Caller列表
// @Summary      获取Caller列表
// @Description  查询所有已注册的Caller
// @Tags         caller
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.CallerResp}  "成功返回Caller列表"
// @Failure      500  {object} components.DefaultRenderWithTrace  "服务内部错误"
// @Router       /caller/list [post]
func ListCallers(ctx *gin.Context) {
	callers, err := callerService.ListAll(ctx)
	if err != nil {
		zlog.Errorf(ctx, "[Caller.List] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}

	resp := make([]params.CallerResp, 0, len(callers))
	for _, c := range callers {
		resp = append(resp, params.CallerResp{
			CallerKey:   c.CallerKey,
			Name:        c.Name,
			Description: c.Description,
			Platform:    c.Platform,
			AllowPlan:   c.AllowPlan,
			Status:      c.Status,
			CreatedAt:   c.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	components.RenderJsonSucc(ctx, resp)
}
