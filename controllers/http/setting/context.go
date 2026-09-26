package setting

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	settingService "react-base-service/service/setting"

	"github.com/gin-gonic/gin"
	"react-base-service/golib/zlog"
)

// GetContextCompactSetting 查询上下文压缩策略
// @Summary      查询上下文压缩策略
// @Description  返回上下文压缩策略的生效视图：effective（合并 DB 覆盖/yaml/默认值后的引擎口径）、逐字段 source 与 DB 覆盖原文。
// @Tags         setting
// @Accept       json
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.ContextCompactSettingResp}  "成功返回生效配置"
// @Failure      400  {object} components.DefaultRenderWithTrace  "查询失败"
// @Router       /setting/context/get [post]
func GetContextCompactSetting(ctx *gin.Context) {
	resp, err := settingService.GetContextCompactSetting(ctx)
	if err != nil {
		zlog.Errorf(ctx, "[Setting.GetContext] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// UpdateContextCompactSetting 更新上下文压缩策略
// @Summary      更新上下文压缩策略
// @Description  字段级覆盖 custom.yaml：指针非 nil 即覆盖，clearFields 内字段清除覆盖回落 yaml/默认值；写后本进程立即生效。
// @Tags         setting
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateContextCompactSettingReq  true  "更新上下文压缩策略请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.ContextCompactSettingResp}  "成功返回更新后的生效配置"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或更新失败"
// @Router       /setting/context/update [post]
func UpdateContextCompactSetting(ctx *gin.Context) {
	var req params.UpdateContextCompactSettingReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Setting.UpdateContext] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := settingService.UpdateContextCompactSetting(ctx, &req, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[Setting.UpdateContext] 更新失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
