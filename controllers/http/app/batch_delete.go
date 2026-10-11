package app

import (
	"react-base-service/components"
	"react-base-service/components/params"
	appService "react-base-service/service/app"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// BatchDelete 软删除单个 App 及其全部配置资源。
// @Summary      删除App及其配置
// @Description  软删除指定的单个App，并在同一事务内删除其Skill、系统提示词、Tool、Tool用户策略、API Key等全部关联资源
// @Tags         app
// @Accept       json
// @Produce      json
// @Param        req  body     params.DeleteAppReq  true  "App删除请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.DeleteAppResp}  "删除成功，返回appKey及各资源删除数量"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或App不存在"
// @Router       /app/batch_delete [post]
func BatchDelete(ctx *gin.Context) {
	var req params.DeleteAppReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[App.BatchDelete] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	resp, err := appService.BatchDelete(ctx, req.AppKey)
	if err != nil {
		zlog.Errorf(ctx, "[App.BatchDelete] 删除失败: appKey=%v, err=%v", req.AppKey, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
