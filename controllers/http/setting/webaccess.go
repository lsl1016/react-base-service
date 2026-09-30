package setting

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	settingService "react-base-service/service/setting"

	"github.com/gin-gonic/gin"
	"react-base-service/golib/zlog"
)

// GetWebSetting 查询联网能力总开关
// @Summary      查询联网能力总开关（web_fetch + web_search）
// @Description  返回联网能力的生效视图：effective（合并 DB 覆盖/yaml/默认值后的引擎口径，两工具共用开关值；含 web_search 连接参数完整性 configured）、逐字段 source 与 DB 覆盖原文。
// @Tags         setting
// @Accept       json
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.WebSettingResp}  "成功返回生效配置"
// @Failure      400  {object} components.DefaultRenderWithTrace  "查询失败"
// @Router       /setting/web/get [post]
func GetWebSetting(ctx *gin.Context) {
	resp, err := settingService.GetWebSetting(ctx)
	if err != nil {
		zlog.Errorf(ctx, "[Setting.GetWeb] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// UpdateWebSetting 更新联网能力总开关
// @Summary      更新联网能力总开关（一枚开关同时启停 web_fetch 与 web_search）
// @Description  字段级覆盖 custom.yaml：指针非 nil 即覆盖，clearFields 内字段清除覆盖回落 yaml/默认值；写后本进程立即生效。web_search 半配置状态（yaml 缺 kind/base_url）允许保存，引擎注册门控会拦截该工具（web_fetch 不受影响）。
// @Tags         setting
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateWebSettingReq  true  "更新联网能力总开关请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.WebSettingResp}  "成功返回更新后的生效配置"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或更新失败"
// @Router       /setting/web/update [post]
func UpdateWebSetting(ctx *gin.Context) {
	var req params.UpdateWebSettingReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Setting.UpdateWeb] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Wrap(err))
		return
	}
	resp, err := settingService.UpdateWebSetting(ctx, &req, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[Setting.UpdateWeb] 更新失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
