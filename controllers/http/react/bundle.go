package react

import (
	"encoding/json"

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

// UploadBundleSource 上传本地 bundle 目录作为安装来源
// @Summary      上传 bundle 目录
// @Description  接收目录选择器选出的全部文件：multipart files[] 携带文件内容、paths 携带同顺序的目录内相对路径 JSON 数组（multipart filename 会被剥成 basename，故路径走独立字段）。写入 cache_dir/uploads 服务端暂存区并预检可解析，返回暂存目录路径——直接填入安装表单 source 即可，无需把路径加入白名单。
// @Tags         React
// @Accept       multipart/form-data
// @Produce      json
// @Param        files  formData  file  true  "bundle 目录内全部文件（可多条）"
// @Param        paths  formData  string  true  "与 files 同顺序的相对路径 JSON 数组"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.BundleUploadResp}  "暂存目录"
// @Failure      400  {object} components.DefaultRenderWithTrace  "空目录/路径逃逸/超限/包非法"
// @Router       /bundle/upload [post]
func UploadBundleSource(ctx *gin.Context) {
	form, err := ctx.MultipartForm()
	if err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("files不能为空（multipart 表单解析失败: %v）", err))
		return
	}
	files := form.File["files"]
	var paths []string
	if raw := form.Value["paths"]; len(raw) > 0 {
		if err := json.Unmarshal([]byte(raw[0]), &paths); err != nil {
			components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("paths 必须是相对路径 JSON 数组: %v", err))
			return
		}
	}
	resp, err := bundleService.UploadFiles(ctx, files, paths)
	if err != nil {
		zlog.Errorf(ctx, "[Bundle.Upload] 上传失败: files=%d err=%v", len(files), err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, resp)
}

