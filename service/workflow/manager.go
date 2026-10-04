package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"react-base-service/components/metrics"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"

	cron "github.com/robfig/cron/v3"
)

// cronManager 是进程内 cron 调度器单例：所有定义变更经 manager 单点同步内存 entry，
// 防止内存与 DB 定义漂移（启动全量重注册兜底恢复）。
//
// 防重语义：着火 → 插 run 行（uk_workflow_fire 唯一索引）→ 并发闸门 → goroutine 执行。
// 「插入 run」本身就是抢锁——多实例部署时同刻着火只允许一条 run 落库，无需分布式锁。
type cronManager struct {
	mu      sync.Mutex
	c       *cron.Cron
	entries map[string]*cronEntry
	// inflight 跟踪在跑 run，优雅停机时等待收敛。
	inflight sync.WaitGroup
}

// cronEntry 是一个已注册的调度条目（保留 Schedule 供 next_fire_at 展示缓存计算）。
type cronEntry struct {
	entryID  cron.EntryID
	schedule cron.Schedule
}

var manager = &cronManager{entries: make(map[string]*cronEntry)}

// start 加载 enabled 的 cron workflow 并启动调度器；失败不 panic（着火面降级为不触发）。
func (m *cronManager) start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.c != nil {
		return
	}
	workflows, err := model.ListWorkflows(context.Background())
	if err != nil {
		zlog.Errorf(nil, "[workflow.start] 加载 workflow 定义失败，调度器未启动: %v", err)
		return
	}
	c := cron.New(cron.WithChain(cron.Recover(&cronPanicLogger{})))
	m.c = c
	registered := 0
	for i := range workflows {
		wf := &workflows[i]
		if !wf.CronSchedulable() {
			continue
		}
		if err := m.registerLocked(c, wf); err != nil {
			zlog.Errorf(nil, "[workflow.start] 注册 workflow 失败(跳过): key=%s expr=%s %s err=%v",
				wf.WorkflowKey, wf.CronExpr, wf.Timezone, err)
			continue
		}
		registered++
	}
	c.Start()
	zlog.Infof(nil, "[workflow.start] 定时触发调度器已启动: entries=%d/%d", registered, len(workflows))
}

// stop 停止 cron 着火并等待在跑 run 收敛（有界等待）。
func (m *cronManager) stop() {
	m.mu.Lock()
	c := m.c
	m.c = nil
	m.entries = make(map[string]*cronEntry)
	m.mu.Unlock()
	if c == nil {
		return
	}
	// Stop 返回的 ctx 在所有在跑 job 函数返回后关闭；fire 是异步派发（快速返回），
	// 真正的 run 收敛由 inflight 等待，超时后交给 react 停机钩子 expire 兜底。
	<-c.Stop().Done()
	done := make(chan struct{})
	go func() {
		m.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopInflightGrace):
		zlog.Warnf(nil, "[workflow.stop] 等待在跑 workflow run 收敛超时(%s)，交给停机钩子兜底", stopInflightGrace)
	}
}

// SyncRegister 创建/更新/启用后重注册 entry：先摘旧再注册，保证内存快照与 DB 定义一致。
// 调度器未运行（总开关关闭）时为空操作——启动时会从 DB 全量重注册。
func (m *cronManager) SyncRegister(wf *model.Workflow) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.c == nil {
		return
	}
	m.removeLocked(wf.WorkflowKey)
	if !wf.CronSchedulable() {
		return
	}
	if err := m.registerLocked(m.c, wf); err != nil {
		zlog.Errorf(nil, "[workflow.SyncRegister] 注册 workflow 失败: key=%s err=%v", wf.WorkflowKey, err)
	}
}

// SyncRemove 停用/软删后摘除 entry。
func (m *cronManager) SyncRemove(workflowKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.c == nil {
		return
	}
	m.removeLocked(workflowKey)
}

// registerLocked 注册单个 entry（须持 m.mu）；成功后回写 next_fire_at 展示缓存。
func (m *cronManager) registerLocked(c *cron.Cron, wf *model.Workflow) error {
	// 行级时区以 CRON_TZ 前缀注入表达式（注册前已由 definition 校验合法性）。
	schedule, err := cron.ParseStandard("CRON_TZ=" + wf.Timezone + " " + wf.CronExpr)
	if err != nil {
		return fmt.Errorf("解析 cron 表达式失败: %w", err)
	}
	entryID := c.Schedule(schedule, cron.FuncJob(func() { m.fire(wf) }))
	m.entries[wf.WorkflowKey] = &cronEntry{entryID: entryID, schedule: schedule}
	// 展示缓存：从当前时刻起的下一个着火点（非调度依据，只供列表页展示）。
	next := schedule.Next(time.Now())
	if err := model.UpdateWorkflowByKey(context.Background(), wf.WorkflowKey,
		map[string]any{"next_fire_at": next}); err != nil {
		zlog.Warnf(nil, "[workflow.register] 回写 next_fire_at 失败: key=%s err=%v", wf.WorkflowKey, err)
	}
	return nil
}

// removeLocked 摘除单个 entry（须持 m.mu）。
func (m *cronManager) removeLocked(workflowKey string) {
	entry, ok := m.entries[workflowKey]
	if !ok {
		return
	}
	m.c.Remove(entry.entryID)
	delete(m.entries, workflowKey)
}

// fire 是 cron 着火回调：插 run（唯一索引防重）→ 并发闸门 → 异步执行。
func (m *cronManager) fire(wf *model.Workflow) {
	ctx := context.Background()
	// 计划着火时刻按 cron 粒度（分钟）取整，作为多实例防重键与展示口径。
	location, err := time.LoadLocation(wf.Timezone)
	if err != nil {
		location = time.Local
	}
	plannedFireAt := time.Now().In(location).Truncate(time.Minute)

	run := &model.WorkflowRun{
		WorkflowID:    wf.ID,
		TriggerType:   model.WorkflowTriggerTypeCron,
		PlannedFireAt: &plannedFireAt,
		Status:        model.WorkflowRunStatusPending,
	}
	created, err := model.CreateWorkflowRun(ctx, run)
	if err != nil {
		zlog.Errorf(nil, "[workflow.fire] 插入 run 失败: key=%s err=%v", wf.WorkflowKey, err)
		return
	}
	if !created {
		// uk_workflow_fire 冲突 = 同次触发已有 run 落库（单实例正常不触发；多实例防重路径），静默跳过。
		return
	}
	now := time.Now()
	if err := model.UpdateWorkflowByKey(ctx, wf.WorkflowKey, map[string]any{"last_fire_at": now}); err != nil {
		zlog.Warnf(nil, "[workflow.fire] 回写 last_fire_at 失败: key=%s err=%v", wf.WorkflowKey, err)
	}

	// 并发闸门：caller 在途 run 数达上限 → 记 skipped（不算失败，防告警误报），不执行。
	cfg := conf.GetWorkflowConfig()
	running, err := model.CountRunningWorkflowRunsByCaller(ctx, wf.CallerKey, run.ID)
	if err != nil {
		zlog.Errorf(nil, "[workflow.fire] 并发闸门计数失败(放行): key=%s err=%v", wf.WorkflowKey, err)
	}
	if err == nil && running >= int64(cfg.MaxParallelPerCaller) {
		zlog.Warnf(nil, "[workflow.fire] caller 并发超限跳过: key=%s caller=%s running=%d max=%d",
			wf.WorkflowKey, wf.CallerKey, running, cfg.MaxParallelPerCaller)
		_ = model.UpdateWorkflowRunByID(ctx, run.ID, map[string]any{
			"status":      model.WorkflowRunStatusSkipped,
			"error_msg":   fmt.Sprintf("caller 在途 run 数 %d 达并发上限 %d", running, cfg.MaxParallelPerCaller),
			"finished_at": now,
		})
		metrics.WorkflowTriggerTotal.WithLabelValues(wf.WorkflowKey, "skipped").Inc()
		return
	}

	metrics.WorkflowTriggerTotal.WithLabelValues(wf.WorkflowKey, "fired").Inc()
	// 每次触发一个 goroutine；生命周期独立于调度器 goroutine，收敛由 inflight/停机钩子覆盖。
	m.inflight.Add(1)
	go func() {
		defer m.inflight.Done()
		executeWorkflowRun(wf, run)
	}()
}

// trackGo 在独立 goroutine 执行 run 并纳入停机收敛跟踪（手动 dispatch 复用）。
func (m *cronManager) trackGo(wf *model.Workflow, run *model.WorkflowRun) {
	m.inflight.Add(1)
	go func() {
		defer m.inflight.Done()
		executeWorkflowRun(wf, run)
	}()
}

// stopInflightGrace 是优雅停机时等待在跑 run 收敛的上限。
const stopInflightGrace = 10 * time.Second

// cronPanicLogger 把 cron job 内的 panic 落日志（Recover 包装器要求实现 Logger 接口）。
type cronPanicLogger struct{}

func (l *cronPanicLogger) Info(_ string, _ ...any)  {}
func (l *cronPanicLogger) Error(err error, msg string, _ ...any) {
	zlog.Errorf(nil, "[workflow.cron] %s: %v", msg, err)
}
