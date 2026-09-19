package router

import (
	"react-base-service/service/asynctask"
	"react-base-service/service/mcpclient"
	"react-base-service/service/setting"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
)

// Commands 注册 CLI 子命令。基座服务默认只有 http 入口；
// go run main.go 默认启动 http 服务，接入方可按需在此追加定时任务子命令。
func Commands(rootCmd *cobra.Command, engine *gin.Engine) {
}

// Tasks 启动随 HTTP 服务运行的后台任务（异步任务状态同步框架，无 Provider 时为空转；
// MCP 客户端按配置拉起子进程并同步工具注册表，未配置时为空操作；
// 运行时设置先加载一次 DB 覆盖快照再按 TTL 周期刷新）。
func Tasks(engine *gin.Engine) {
	asynctask.Start(engine)
	mcpclient.Bootstrap(engine)
	setting.Bootstrap()
}

// StopTasks 停止随 HTTP 服务启动的后台任务。
func StopTasks() {
	asynctask.Stop()
	mcpclient.Shutdown()
	setting.Shutdown()
}
