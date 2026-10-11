package app

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	appService "react-base-service/service/app"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// CopyConfig 复制 App 及其全部配置资源。
// @Summary      复制App配置
// @Description  以源App为模板创建新App，并复制其Skill、系统提示词、Tool、Tool用户策略、API Key等全部配置资源，整个复制在单事务内完成
// @Tags         app
// @Accept       json
// @Produce      json
// @Param        req  body     params.CopyAppConfigReq  true  "App配置复制请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.CopyAppConfigResp}  "复制成功，返回目标appKey及各资源复制数量"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败、源App不存在或目标appKey已存在"
// @Router       /app/copy_config [post]
func CopyConfig(ctx *gin.Context) {
	var req params.CopyAppConfigReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[App.CopyConfig] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	resp, err := appService.CopyConfig(ctx, &req, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[App.CopyConfig] 复制失败: sourceAppKey=%s, targetAppKey=%s, err=%v",
			req.SourceAppKey, req.TargetApp.AppKey, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
