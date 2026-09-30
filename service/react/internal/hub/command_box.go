package hub

// Q1 运行时通知邮箱（runtime command box）
// （docs/plan/20260925_Session与Steering机制借鉴方案.md §3，对齐 ZCode
// runtime-command-active-loop.ts 的轮内吸收器与 background-notifications.ts 的账本镜像）。
//
// 两个队列不可混淆：Steering pendingInputs 承载用户意图（steering.go），
// 命令箱承载机器事件（当前只有后台委派完成通知一种，kind 字段预留扩展）。
//
// 核心纪律：
//   - 账本镜像是唯一持久痕迹——通知投递时写 tblLlmReactPendingInput（kind=notification,
//     status=admitted），内存命令箱只是投递通道；run 终态结算与重启作废复用 Steering 账本机制；
//   - 落账与父 run 活跃校验同一事务（run 行锁）——父 run 收尾与通知投递的竞态窗口被
//     行锁串行化，不会产生无人消费的残留账本行；
//   - 消费 claim-once——吸收事务内条件更新 admitted→guided，与通知消息落库原子；
//   - 吸收点唯一——引擎循环顶部（step>0，对齐 ZCode 吸收器位置）与纯文本 finish 判定前；
//     通知合并为一条 model-only 的 user 消息（react_notice 类型）拼进当前轮请求，不新开 turn；
//   - 重启不复活——命令箱随进程死亡，残留 admitted 通知由 session_resumed 统一作废
//     （对齐"后台子进程随重启已死"）。

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	model "react-base-service/models/llm"
	core "react-base-service/service/react/internal/core"
)

// core.EventNoticeDrained 通知在模型步边界被吸收进当前轮的事件（前端渲染系统卡片）。

// maxNotificationSummaryChars 通知摘要截断上限。ZCode 为 120k；本项目上下文更敏感，
// 取其 1/10——超长结论应引导用户经子 run 记录/resultRef 按需查看，而不是全量进上下文。
const maxNotificationSummaryChars = 12000

// taskNotificationPreamble 是合并通知消息的说明前缀：明确"系统生成、非用户输入"，
// 并给出消费建议（继续工作或在最终答复中转达），避免模型把通知当成新的用户指令重新开工。
const taskNotificationPreamble = "以下是后台任务的完成通知（系统自动生成，非用户输入）。" +
	"请结合通知内容继续当前工作；若当前任务已收尾，则在最终答复中简要告知用户对应后台任务的结果。"

// RuntimeNotification 是一条待吸收的后台任务完成通知（命令箱条目）。
type RuntimeNotification struct {
	// PendingInputID 是账本行 ID：吸收事务内条件更新 claim-once 的依据。
	PendingInputID uint
	SubRunID       string
	AgentKey       string
	AgentPath      string
	// Status 是子 run 终态词汇：success / error / cancelled / timeout。
	Status       string
	Summary      string
	InputTokens  int
	OutputTokens int
}

// RuntimeCommandBox 是单个 run 的运行时命令箱：完成监视 goroutine（生产者）与
// 引擎循环（唯一消费者）之间的 FIFO 队列。Go 侧引擎只在模型步边界轮询吸收，
// 无需 ZCode 命令队列的 notify channel 与优先级调度；kind 扩展时在条目上加类型字段。
type RuntimeCommandBox struct {
	mu            sync.Mutex
	notifications []RuntimeNotification
}

func NewRuntimeCommandBox() *RuntimeCommandBox {
	return &RuntimeCommandBox{}
}

// PushNotification 投递一条通知（生产者侧，任意 goroutine 安全）。
func (b *RuntimeCommandBox) PushNotification(n RuntimeNotification) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notifications = append(b.notifications, n)
}

// pushFrontNotifications 把消费失败的通知放回队首（单消费者语义下保持 FIFO，
// 下一边界重试）。放回顺序：先放回的排最前。
func (b *RuntimeCommandBox) PushFrontNotifications(notices []RuntimeNotification) {
	if len(notices) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notifications = append(append([]RuntimeNotification{}, notices...), b.notifications...)
}

// drainNotifications 取出当前全部通知（消费侧）。
func (b *RuntimeCommandBox) DrainNotifications() []RuntimeNotification {
	b.mu.Lock()
	defer b.mu.Unlock()
	taken := b.notifications
	b.notifications = nil
	return taken
}

// ---------------------------------------------------------------------------
// 纯函数层（信封渲染 / 状态映射 / 准入判定）
// ---------------------------------------------------------------------------

// backgroundNotificationStatus 把子 run 终态映射为通知状态词汇。
func BackgroundNotificationStatus(loopErr error) string {
	switch {
	case loopErr == nil:
		return "success"
	case core.IsReactRunCancelled(loopErr):
		return "cancelled"
	case core.IsReactRunTimeout(loopErr):
		return "timeout"
	default:
		return "error"
	}
}

// shouldDeliverBackgroundNotification 判定父 run 是否还有通知消费方：
// 仅 running 状态投递；终态（含 waiting_*）一律不投递——结果留在子 run 记录里可查，
// 绝不为一条通知复活 run（对齐 ZCode finalizeBackgroundCompletion 的父 run 围栏）。
func ShouldDeliverBackgroundNotification(parentRunState string) bool {
	return parentRunState == model.ReactRunStateRunning
}

// truncateNotificationSummary 截断通知摘要（rune 口径），超限追加截断标记。
func truncateNotificationSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	runes := []rune(summary)
	if len(runes) <= maxNotificationSummaryChars {
		return summary
	}
	return string(runes[:maxNotificationSummaryChars]) + "…(截断)"
}

// renderTaskNotificationEnvelope 渲染单条通知的 <task-notification> XML 信封
// （task-id/status/summary/usage，对齐 ZCode background-notifications 的信封字段）。
func RenderTaskNotificationEnvelope(n RuntimeNotification) string {
	var sb strings.Builder
	sb.WriteString("<task-notification>\n")
	fmt.Fprintf(&sb, "  <task-id>%s</task-id>\n", n.SubRunID)
	fmt.Fprintf(&sb, "  <agent>%s</agent>\n", n.AgentKey)
	fmt.Fprintf(&sb, "  <agent-path>%s</agent-path>\n", n.AgentPath)
	fmt.Fprintf(&sb, "  <status>%s</status>\n", n.Status)
	fmt.Fprintf(&sb, "  <summary>\n%s\n  </summary>\n", truncateNotificationSummary(n.Summary))
	fmt.Fprintf(&sb, "  <usage input-tokens=\"%d\" output-tokens=\"%d\" />\n", n.InputTokens, n.OutputTokens)
	sb.WriteString("</task-notification>")
	return sb.String()
}

// mergeTaskNotificationContent 把本批通知合并为一条模型消息：说明前缀 + 信封按 "\n\n" 连接
// （对齐 ZCode 批合并：避免每条通知单独发起一次模型请求）。
func MergeTaskNotificationContent(notices []RuntimeNotification) string {
	envelopes := make([]string, 0, len(notices))
	for _, n := range notices {
		envelopes = append(envelopes, RenderTaskNotificationEnvelope(n))
	}
	return taskNotificationPreamble + "\n\n" + strings.Join(envelopes, "\n\n")
}

// renderBackgroundDelegateLaunchedText 是 background=true 时 delegate_agent 的工具结果：
// async_launched 文案纪律（对齐 ZCode agent.ts）——不等待、不编造子任务结论；完成通知会
// 自动回灌。wait_agent 与"结束本轮回复"等权重并列：本轮后续就需要结果时立即收割，
// 暂不需要时才转达后收尾，避免"结束回合"指令压过任务自身的等待需求。
func RenderBackgroundDelegateLaunchedText(agentKey, runID string) string {
	return fmt.Sprintf(
		"后台委派已启动：agent=%s, runId=%s。该子 Agent 正在后台独立执行，完成时会以系统通知自动回灌当前运行，无需轮询；"+
			"若本轮任务接下来就需要该结果，请立即用 wait_agent（run_ids=[\"%s\"]）主动收割后再继续，不要直接结束回合；"+
			"若暂不需要，简要告知用户已启动该后台任务，即可结束本轮回复。",
		agentKey, runID, runID)
}

// ---------------------------------------------------------------------------
// 引擎侧吸收
// ---------------------------------------------------------------------------

// ErrBackgroundParentInactive 是通知投递事务的哨兵错误：父 run 已终态，不落账本不入箱。
var ErrBackgroundParentInactive = errors.New("background notification parent run inactive")
