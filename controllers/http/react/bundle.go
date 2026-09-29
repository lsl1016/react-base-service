package react

import (
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	bundleService "react-base-service/service/bundle"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// InstallBundle 安装 Agent Bundle 插件包
// @Summary      安装 Agent Bundle 插件包
// @Description  从白名单来源（内部 git URL / 本地路径）拉取 manifest + agents/ + skills/ + .mcp.json 的打包，展开写入各注册表；同名覆盖（重装先卸载还原），失败自动回滚清理。
// @Tags         React
// @Accept       json
// @Produce      json
// @Param        req  body     params.BundleInstallReq  true  "安装请求"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.BundleResp}  "安装结果"
// @Failure      400  {object} components.DefaultRenderWithTrace  "来源不在白名单/包非法/应用失败"
// @Router       /bundle/install [post]
func InstallBundle(ctx *gin.Context) {
	var req params.BundleInstallReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	installed, err := bundleService.Install(ctx, &req, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[Bundle.Install] 安装失败: source=%s err=%v", req.Source, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	items, err := bundleService.ListInstalled(ctx)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	for i := range items {
		if items[i].BundleID == installed.BundleID {
			components.RenderJsonSucc(ctx, items[i])
			return
		}
	}
	components.RenderJsonSucc(ctx, installed)
}

// UninstallBundle 卸载 Agent Bundle
// @Summary      卸载 Agent Bundle
// @Description  按资源清单逆序回滚：安装新建的资源软删，覆盖的资源按安装前快照整行恢复；最后软删安装记录。
// @Tags         React
// @Accept       json
// @Produce      json
// @Param        req  body     params.BundleUninstallReq  true  "卸载请求"
// @Success      200  {object} components.DefaultRenderWithTrace  "卸载成功"
// @Failure      400  {object} components.DefaultRenderWithTrace  "Bundle 不存在或回滚失败"
// @Router       /bundle/uninstall [post]
func UninstallBundle(ctx *gin.Context) {
	var req params.BundleUninstallReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	if err := bundleService.Uninstall(ctx, &req); err != nil {
		zlog.Errorf(ctx, "[Bundle.Uninstall] 卸载失败: name=%s err=%v", req.Name, err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, gin.H{"name": req.Name})
}

// ListBundles 列出已安装 Bundle
// @Summary      列出已安装 Bundle
// @Description  返回全部已安装 bundle 及各类型资源计数。
// @Tags         React
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.BundleListItemResp}  "已安装列表"
// @Router       /bundle/list [post]
func ListBundles(ctx *gin.Context) {
	items, err := bundleService.ListInstalled(ctx)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, items)
}

// BrowseBundleSource 浏览白名单内可作安装来源的本地目录
// @Summary      Bundle 来源目录浏览
// @Description  供安装表单的目录选择器：path 为空返回白名单根目录，非空返回该目录的一层子目录（仅白名单本地前缀内，不跟随符号链接）。
// @Tags         React
// @Accept       json
// @Produce      json
// @Param        req  body     params.BundleBrowseReq  true  "浏览请求"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.BundleBrowseResp}  "目录清单"
// @Failure      400  {object} components.DefaultRenderWithTrace  "目录不在白名单内或不存在"
// @Router       /bundle/browse [post]
func BrowseBundleSource(ctx *gin.Context) {
	var req params.BundleBrowseReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}
	resp, err := bundleService.BrowseSourceDirs(req.Path)
	if err != nil {
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}
