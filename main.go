// @title           react-base-service API
// @version         1.0
// @description     ReAct Agent 基座服务：提供 ReAct 运行时、会话管理与回放、工具/Skill/系统提示词注册管理等能力
// @host            localhost:8080
// @BasePath        /react-base-service
package main

import (
	"context"
	"time"

	"react-base-service/components"
	"react-base-service/conf"
	"react-base-service/helpers"
	"react-base-service/models/llm"
	"react-base-service/router"
	skillService "react-base-service/service/skill"

	"react-base-service/golib"
	"react-base-service/golib/base"
	"react-base-service/golib/server/http"
	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
)

func main() {
	// gin
	engine := gin.New()

	// 初始化基础配置
	helpers.PreInit()
	defer helpers.Clear()

	golib.Bootstraps(engine, golib.BootstrapConf{
		// 业务自定义recover handler
		HandleRecovery: func(c *gin.Context, err interface{}) {
			base.RenderJsonAbort(c, components.ErrorSystemError)
		},
	})

	var rootCmd = &cobra.Command{
		Use:   "goweb",
		Short: "react-base-service application",
		Run: func(cmd *cobra.Command, args []string) {
			httpServer(engine)
		},
	}

	// 加载支持的子命令行（基座服务默认无子命令）
	router.Commands(rootCmd, engine)

	if err := rootCmd.Execute(); err != nil {
		panic(err.Error())
	}
}

func httpServer(engine *gin.Engine) {
	// web 服务所需资源初始化
	helpers.InitResource(engine)
	defer helpers.Release()

	// 清理上一进程遗留的陈旧活跃 run：取消注册表随进程丢失，残留 run 会永久阻塞对应会话的新消息
	if n, err := model.ExpireStaleActiveReactRuns(context.Background(), 30*time.Minute); err != nil {
		zlog.Errorf(nil, "[startup] 陈旧活跃 run 清理失败: err=%v", err)
	} else if n > 0 {
		zlog.Infof(nil, "[startup] 已清理 %d 个陈旧活跃 run（state→expired，超过 30 分钟未更新）", n)
	}

	// 内置技能包幂等补种（WP3）：版本升级后为存量 caller 补齐新增技能；同名行跳过，不回写用户覆盖
	go skillService.SeedBundledSkillsForAllCallers()

	// 初始化http服务路由
	router.Http(engine)

	// 启动后台任务（异步任务状态同步框架，无 Provider 时为空转）
	router.Tasks(engine)
	defer router.StopTasks()

	// 启动web server
	if err := http.Start(engine, conf.BasicConf.Server); err != nil {
		panic(err.Error())
	}
}
