package app

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	appService "react-base-service/service/app"

	"github.com/gin-gonic/gin"
	"react-base-service/golib/zlog"
)

// RegisterApp 注册App
// @Summary      注册App
// @Description  注册一个新的调用方（App），appKey全局唯一
// @Tags         app
// @Accept       json
// @Produce      json
// @Param        req  body     params.RegisterAppReq  true  "App注册请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.AppResp}  "成功返回App信息"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或appKey已存在"
// @Router       /app/register [post]
func RegisterApp(ctx *gin.Context) {
	var req params.RegisterAppReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[App.Register] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)

	app, err := appService.RegisterApp(ctx, req.AppKey, req.Name, req.Description, req.Platform, req.AllowPlan, req.BindDefaultCapabilities, userName)
	if err != nil {
		zlog.Errorf(ctx, "[App.Register] 注册失败: appKey=%s, err=%v", req.AppKey, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, params.AppResp{
		AppKey:   app.AppKey,
		Name:        app.Name,
		Description: app.Description,
		Platform:    app.Platform,
		AllowPlan:   app.AllowPlan,
		BindDefaultCapabilities: app.BindDefaultCapabilities,
		Status:                  app.Status,
		CreatedAt:               app.CreatedAt.Format("2006-01-02 15:04:05"),
	})
}

// UpdateApp 更新App
// @Summary      更新App
// @Description  更新已有App的名称、描述、平台或状态
// @Tags         app
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateAppReq  true  "App更新请求体"
// @Success      200  {object} components.DefaultRenderWithTrace  "更新成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或App不存在"
// @Router       /app/update [post]
func UpdateApp(ctx *gin.Context) {
	var req params.UpdateAppReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[App.Update] 请求参数绑定失败: %v", err)
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
	if req.BindDefaultCapabilities != nil {
		updates["bind_default_capabilities"] = boolToInt(*req.BindDefaultCapabilities)
	}

	if err := appService.UpdateApp(ctx, req.AppKey, updates); err != nil {
		zlog.Errorf(ctx, "[App.Update] 更新失败: appKey=%s, err=%v", req.AppKey, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, gin.H{})
}

// ListApps 获取App列表
// @Summary      获取App列表
// @Description  查询所有已注册的App
// @Tags         app
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.AppResp}  "成功返回App列表"
// @Failure      500  {object} components.DefaultRenderWithTrace  "服务内部错误"
// @Router       /app/list [post]
func ListApps(ctx *gin.Context) {
	apps, err := appService.ListAll(ctx)
	if err != nil {
		zlog.Errorf(ctx, "[App.List] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}

	resp := make([]params.AppResp, 0, len(apps))
	for _, c := range apps {
		resp = append(resp, params.AppResp{
			AppKey:   c.AppKey,
			Name:        c.Name,
			Description: c.Description,
			Platform:    c.Platform,
			AllowPlan:   c.AllowPlan,
			BindDefaultCapabilities: c.BindDefaultCapabilities,
			Status:                  c.Status,
			CreatedAt:               c.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	components.RenderJsonSucc(ctx, resp)
}

// boolToInt 布尔转 0/1（bind_default_capabilities 列存储口径）。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
