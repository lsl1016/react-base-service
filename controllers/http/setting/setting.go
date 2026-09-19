package setting

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	settingService "react-base-service/service/setting"

	"github.com/gin-gonic/gin"
	"react-base-service/golib/zlog"
)

// GetSubAgentSetting 查询 subagent 委派策略
// @Summary      查询 subagent 委派策略
// @Description  返回 subagent 委派策略的生效视图：effective（合并 DB 覆盖/yaml/默认值后的引擎口径）、逐字段 source 与 DB 覆盖原文。
// @Tags         setting
// @Accept       json
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SubAgentSettingResp}  "成功返回生效配置"
// @Failure      400  {object} components.DefaultRenderWithTrace  "查询失败"
// @Router       /setting/subagent/get [post]
func GetSubAgentSetting(ctx *gin.Context) {
	resp, err := settingService.GetSubAgentSetting(ctx)
	if err != nil {
		zlog.Errorf(ctx, "[Setting.GetSubAgent] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// UpdateSubAgentSetting 更新 subagent 委派策略
// @Summary      更新 subagent 委派策略
// @Description  字段级覆盖 custom.yaml：指针非 nil 即覆盖，clearFields 内字段清除覆盖回落 yaml/默认值；写后本进程立即生效。
// @Tags         setting
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateSubAgentSettingReq  true  "更新 subagent 策略请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SubAgentSettingResp}  "成功返回更新后的生效配置"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或更新失败"
// @Router       /setting/subagent/update [post]
func UpdateSubAgentSetting(ctx *gin.Context) {
	var req params.UpdateSubAgentSettingReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Setting.UpdateSubAgent] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := settingService.UpdateSubAgentSetting(ctx, &req, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[Setting.UpdateSubAgent] 更新失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
