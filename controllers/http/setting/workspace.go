package setting

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	settingService "react-base-service/service/setting"

	"github.com/gin-gonic/gin"
	"react-base-service/golib/zlog"
)

// GetWorkspaceSetting 查询检索代码和工作区开关
// @Summary      查询检索代码和工作区开关
// @Description  返回生效视图（DB 覆盖 > custom.yaml > 默认启用）；全局设置，无 app 维度
// @Tags         setting
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.WorkspaceSettingResp}
// @Router       /setting/workspace/get [post]
func GetWorkspaceSetting(ctx *gin.Context) {
	resp, err := settingService.GetWorkspaceSetting(ctx)
	if err != nil {
		zlog.Errorf(ctx, "[Setting.GetWorkspace] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

// UpdateWorkspaceSetting 更新检索代码和工作区开关
// @Summary      更新检索代码和工作区开关
// @Description  字段级覆盖 custom.yaml；写后本进程立即生效（下一个 run 起 load_runtime_code/ws_* 可见性随之变化）
// @Tags         setting
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateWorkspaceSettingReq  true  "更新请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.WorkspaceSettingResp}
// @Router       /setting/workspace/update [post]
func UpdateWorkspaceSetting(ctx *gin.Context) {
	var req params.UpdateWorkspaceSettingReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Setting.UpdateWorkspace] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := settingService.UpdateWorkspaceSetting(ctx, &req, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[Setting.UpdateWorkspace] 更新失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
