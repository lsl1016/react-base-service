package setting

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	settingService "react-base-service/service/setting"

	"github.com/gin-gonic/gin"
	"react-base-service/golib/zlog"
)

// GetMemorySetting 查询长期记忆策略
// @Summary      查询长期记忆策略
// @Description  返回长期记忆策略的生效视图：effective（合并 DB 覆盖/yaml/默认值后的引擎口径）、逐字段 source 与 DB 覆盖原文。
// @Tags         setting
// @Accept       json
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.MemorySettingResp}  "成功返回生效配置"
// @Failure      400  {object} components.DefaultRenderWithTrace  "查询失败"
// @Router       /setting/memory/get [post]
func GetMemorySetting(ctx *gin.Context) {
	resp, err := settingService.GetMemorySetting(ctx)
	if err != nil {
		zlog.Errorf(ctx, "[Setting.GetMemory] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// UpdateMemorySetting 更新长期记忆策略
// @Summary      更新长期记忆策略
// @Description  字段级覆盖 custom.yaml：指针非 nil 即覆盖，clearFields 内字段清除覆盖回落 yaml/默认值；写后本进程立即生效。
// @Tags         setting
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateMemorySettingReq  true  "更新长期记忆策略请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.MemorySettingResp}  "成功返回更新后的生效配置"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或更新失败"
// @Router       /setting/memory/update [post]
func UpdateMemorySetting(ctx *gin.Context) {
	var req params.UpdateMemorySettingReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Setting.UpdateMemory] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Wrap(err))
		return
	}
	resp, err := settingService.UpdateMemorySetting(ctx, &req, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[Setting.UpdateMemory] 更新失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
