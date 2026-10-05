package react

import (
	"react-base-service/components"
	"react-base-service/service/workspace"

	"github.com/gin-gonic/gin"
)

// ListActiveWorkspaces 查询活跃代码工作区
// @Summary      查询活跃代码工作区
// @Description  返回当前进程内全部活跃的 (service, commit) 共享代码工作区实例：service@ref(commit)、路径、挂载工具前缀、引用它的 run 与 caller 计数、挂载时间。同一 commit 多 run 共享一份；全部引用释放即回收，空列表为正常态。
// @Tags         React
// @Produce      json
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]workspace.ActiveEntry}  "活跃共享实例清单"
// @Router       /workspace/active [post]
func ListActiveWorkspaces(ctx *gin.Context) {
	manager := workspace.Default()
	if manager == nil {
		components.RenderJsonSucc(ctx, []workspace.ActiveEntry{})
		return
	}
	components.RenderJsonSucc(ctx, manager.ActiveSnapshot())
}
