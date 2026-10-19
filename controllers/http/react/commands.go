package react

import (
	"react-base-service/components"
	"react-base-service/components/params"
	commandService "react-base-service/service/command"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// ListCommands 查询 app 可见命令目录（前端 / 菜单数据源）
// @Summary      命令目录
// @Description  聚合内置命令与 app+route 作用域内的自定义命令；slash_command.enabled 未开时返回空。
// @Tags         React
// @Accept       json
// @Produce      json
// @Param        req  body     params.CommandCatalogReq  true  "命令目录请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.CommandCatalogResp}
// @Router       /react/commands/list [post]
func ListCommands(ctx *gin.Context) {
	var req params.CommandCatalogReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[React.ListCommands] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	items, err := commandService.ListCatalog(ctx, req.AppKey, req.RouteValues)
	if err != nil {
		zlog.Errorf(ctx, "[React.ListCommands] 聚合命令目录失败: appKey=%s, err=%v", req.AppKey, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, params.CommandCatalogResp{Commands: items})
}
