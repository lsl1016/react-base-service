// Package workflow 实现定时触发工作流：按 cron 计划在服务端无人值守地发起一次完整
// ReAct run（见 docs/定时触发工作流实现方案.md）。
//
// 分层（触发定义 → 调度器 → headless run）：
//   - manager.go：robfig/cron v3 进程内调度；定义 CRUD 经 manager 单点同步，防内存 entry 与 DB 漂移
//   - runner.go：复用 reflection 同款 headless 链路执行 run（type=scheduled），终态映射/判级
//   - notify.go：webhook 通知（企业微信/飞书格式按域名识别）
//   - prompt.go：占位符渲染 + 无人值守纪律前缀
//   - definition.go：定义校验与 CRUD 服务（供控制器调用）
//
// 工程范式沿用 service/asynctask：Start/Stop 生命周期 + 空转降级（llm.workflow.enabled=false 时不启动）。
package workflow

import (
	"sync"

	"react-base-service/conf"
	"react-base-service/golib/zlog"

	"github.com/gin-gonic/gin"
)

var lifecycle struct {
	sync.Mutex
	running bool
}

// Start 启动定时触发调度器：加载 enabled 的 cron workflow 注册 entry。
// 总开关（llm.workflow.enabled）关闭时不启动、不加载（空转降级，与历史版本行为一致）。
func Start(engine *gin.Engine) {
	if !conf.CustomConf.LLM.Workflow.Enabled {
		return
	}
	lifecycle.Lock()
	defer lifecycle.Unlock()
	if lifecycle.running {
		return
	}
	manager.start()
	lifecycle.running = true
}

// Stop 停止调度器：先停 cron 着火，再等待在跑 run 收敛（有界等待，超时由
// react 停机钩子 expire 兜底）。挂 router.StopTasks，复用现有 shutdown 模式。
func Stop() {
	lifecycle.Lock()
	defer lifecycle.Unlock()
	if !lifecycle.running {
		return
	}
	manager.stop()
	lifecycle.running = false
	zlog.Infof(nil, "[workflow.Stop] 定时触发调度器已停止")
}
