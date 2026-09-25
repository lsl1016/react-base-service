package react

// 主循环终止边界（终止边界治理 Phase 1，设计见 docs/plan/20260925_AgentLoop终止边界与循环治理优化方案.md）。
//
// 设计原则对齐 ZCode：不用工具调用次数做硬停止；终止由"明确边界"承担——
//   - 用户取消 / 权限拒绝（既有实现）；
//   - run 级 token 预算与 wall-clock 超时（本文件）；
//   - 步数预算耗尽时的软着陆收尾（本文件）；
//   - 模型自然结束（主路径）。
//
// 次数类信号只用于软性守卫（重复调用提醒注入），不做主终止开关。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/metrics"
	"react-base-service/components/params"
	"react-base-service/conf"

	"react-base-service/golib/zlog"
)

var (
	// ErrReactRunTimeout run 触达 wall-clock 上限；按 timeout 终态优雅收敛（区别于取消与错误）。
	ErrReactRunTimeout = errors.New("react run timeout")
	// ErrInteractionTimeout 交互等待（ask_question / client tool / 工具确认）超过配置时限；
	// 以错误工具结果回灌模型继续循环，不终止 run。
	ErrInteractionTimeout = errors.New("react interaction wait timeout")
)

func IsReactRunTimeout(err error) bool {
	return errors.Is(err, ErrReactRunTimeout)
}

func IsErrInteractionTimeout(err error) bool {
	return errors.Is(err, ErrInteractionTimeout)
}

// logWarnf / logInfof 是 engine state 的 nil 安全日志包装：
// 单测构造的最小 state 没有 gin ctx，zlog 取 logID 需要非空 ctx。
func (s *reactEngineState) logWarnf(format string, args ...any) {
	if s.ctx == nil {
		return
	}
	zlog.Warnf(s.ctx, format, args...)
}

func (s *reactEngineState) logInfof(format string, args ...any) {
	if s.ctx == nil {
		return
	}
	zlog.Infof(s.ctx, format, args...)
}

// 软着陆触发原因。
const (
	softLandingReasonStepBudget     = "step_budget"
	softLandingReasonTokenBudget    = "token_budget"
	softLandingReasonTimeBudget     = "time_budget"
	softLandingReasonContextLimit   = "context_limit"
	terminationReasonExhausted      = "finish_exhausted"
	terminationReasonBudgetExceeded = "finish_budget_exhausted"
)

// softLandingAllowedTools 是软着陆收尾窗口内允许执行的工具：只读/记录类操作。
// 其余工具（写类、委派、计划、沙箱等）返回错误结果回灌模型，引导其立即总结收尾。
var softLandingAllowedTools = map[string]bool{
	metaToolGetTool:           true,
	metaToolGetSkill:          true,
	metaToolReadToolResult:    true,
	metaToolInspectData:       true,
	metaToolInspectAttachment: true,
	metaToolReadAttachment:    true,
	metaToolGetAsyncTask:      true,
	metaToolAskQuestion:       true,
	metaToolTodoWrite:         true,
	metaToolDisplayFiles:      true,
	metaToolMemoryList:        true,
	metaToolMemoryRead:        true,
	metaToolGraphMemorySearch: true,
}

// maybeEnterSoftLanding 在每轮模型调用前判定是否进入软着陆收尾窗口。
// 触发后保持到 run 结束；contextMessages 据此注入收尾提醒，executeToolCall 据此限流工具。
func (s *reactEngineState) maybeEnterSoftLanding(step, maxSteps int) {
	if s.softLandingActive {
		return
	}
	loopCfg := conf.GetReactRuntimeConfig().Loop
	reason := ""
	remainingSteps := 0
	remainingMillis := int64(0)
	if step >= maxSteps-loopCfg.SoftLandingSteps && step > 0 {
		reason = softLandingReasonStepBudget
		remainingSteps = maxSteps - step
	}
	if reason == "" && s.runTimeout > 0 && !s.runDeadline.IsZero() {
		remaining := time.Until(s.runDeadline)
		if remaining <= s.runTimeout/10 {
			reason = softLandingReasonTimeBudget
			if remaining < 0 {
				remaining = 0
			}
			remainingMillis = remaining.Milliseconds()
		}
	}
	if reason == "" {
		if budget := s.loopTokenBudget(); budget > 0 {
			used := s.loopTokenUsed()
			if budget-used <= budget/10 {
				reason = softLandingReasonTokenBudget
				remainingMillis = 0
			}
		}
	}
	if reason == "" {
		return
	}
	s.enterSoftLanding(reason, remainingSteps, remainingMillis)
}

// enterSoftLanding 显式进入软着陆（供预算/rapid-refill 路径复用）。
func (s *reactEngineState) enterSoftLanding(reason string, remainingSteps int, remainingMillis int64) {
	if s.softLandingActive {
		return
	}
	s.softLandingActive = true
	s.softLandingReason = reason
	// Steering：外层 run 进入收尾窗口后不再接收 guide（跨 goroutine 对准入决策可见）。
	// A2：子 run 也注册软着陆可见性——SendMessage 需要在发送前拒绝正在收尾的目标子 run
	//（与 Steering 在收尾窗口拒绝 guide 同一纪律）。markRunSoftLanding 对未注册 run 是空操作，
	// reflection/headless 等不经 delegate 的 run 不受影响。
	markRunSoftLanding(s.runID)
	metrics.SoftLandingsTotal.WithLabelValues(reason).Inc()
	s.logWarnf("[React.Boundary] 进入软着陆收尾窗口: runId=%s, reason=%s, remainingSteps=%d, remainingMillis=%d", s.runID, reason, remainingSteps, remainingMillis)
	if s.emitter != nil {
		_ = s.emitter.Emit(EventSoftLanding, params.ReactSoftLandingPayload{Reason: reason, RemainingSteps: remainingSteps, RemainingMillis: remainingMillis})
	}
}

// loopTokenBudget 返回外层 run 的 token 预算（请求级覆盖 > 配置；子 run 保持 agent 策略语义，不套用全局预算）。
func (s *reactEngineState) loopTokenBudget() int {
	if s.agentPath != "" {
		return 0
	}
	if s.req != nil && s.req.payload.TokenBudget > 0 {
		return s.req.payload.TokenBudget
	}
	return conf.GetReactRuntimeConfig().Loop.BudgetTokensPerRun
}

// loopTokenUsed 返回外层 run 的递归 token 消耗口径（与本 run 输入+输出+委派孙代理一致）。
func (s *reactEngineState) loopTokenUsed() int {
	return s.inputTokens + s.outputTokens + int(s.delegatedInputTokens.Load()) + int(s.delegatedOutputTokens.Load())
}

// checkLoopTokenBudget 外层 run 的预算边界：≥90% 触发软着陆；超过 100% 以预算耗尽完成态收尾。
// 返回 exhausted=true 时调用方应立即 finishExhausted（本轮 assistant 消息已持久化）。
func (s *reactEngineState) checkLoopTokenBudget() (exhausted bool) {
	budget := s.loopTokenBudget()
	if budget <= 0 {
		return false
	}
	used := s.loopTokenUsed()
	if used > budget {
		return true
	}
	if budget-used <= budget/10 {
		s.maybeEnterSoftLandingByToken()
	}
	return false
}

func (s *reactEngineState) maybeEnterSoftLandingByToken() {
	if s.softLandingActive {
		return
	}
	s.enterSoftLanding(softLandingReasonTokenBudget, 0, 0)
}

// softLandingToolBlocked 判定软着陆窗口内工具是否被限流；被限流时返回回灌给模型的错误文案。
func softLandingToolBlocked(toolName string) (string, bool) {
	if softLandingAllowedTools[toolName] {
		return "", false
	}
	return fmt.Sprintf("执行预算即将耗尽，工具 %s 已被暂停（仅保留只读/记录类操作）。请基于已有结果立即给出最终回答；未完成事项写入 todo 并明确说明。", toolName), true
}

// toolCallSignature 计算"工具名 + 参数稳定序列化"的签名（参数 key 排序，map 序列化顺序无关）。
func toolCallSignature(call llm.ToolCall) string {
	var input any
	if len(call.Input) > 0 {
		_ = json.Unmarshal(call.Input, &input)
	}
	normalized, err := json.Marshal(sortJSONKeys(input))
	if err != nil {
		normalized = []byte("{}")
	}
	sum := sha256.Sum256([]byte(call.Name + "\x00" + string(normalized)))
	return hex.EncodeToString(sum[:])[:16]
}

// sortJSONKeys 递归排序 map 的 key，保证同参数不同序列化顺序得到同一签名。
func sortJSONKeys(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		ordered := make(map[string]any, len(typed))
		for _, key := range keys {
			ordered[key] = sortJSONKeys(typed[key])
		}
		return ordered
	case []any:
		for i, item := range typed {
			typed[i] = sortJSONKeys(item)
		}
		return typed
	default:
		return value
	}
}

// maxAnomalyInjections 单 run 内重复调用提醒的注入上限（防提醒本身刷屏）。
const maxAnomalyInjections = 3

// recordToolAnomaly 在每轮工具执行后更新"同签名连续重复"计数。
// 只做软性守卫：达到阈值后由 contextMessages 注入收束提醒，不阻断循环。
func (s *reactEngineState) recordToolAnomaly(calls []llm.ToolCall) {
	threshold := conf.GetReactRuntimeConfig().Loop.AnomalyRepeatThreshold
	if threshold <= 0 || len(calls) == 0 {
		return
	}
	for _, call := range calls {
		signature := toolCallSignature(call)
		if signature == s.anomalyLastSignature {
			s.anomalyStreak++
		} else {
			s.anomalyLastSignature = signature
			s.anomalyStreak = 1
			s.anomalyLastTool = call.Name
		}
	}
	if s.anomalyStreak == threshold && s.anomalyInjections < maxAnomalyInjections {
		// 恰好跨越阈值时记一次指标；提醒注入由 contextMessages 在 streak>=threshold 时持续进行。
		metrics.AnomalyWarningsTotal.Inc()
		s.logWarnf("[React.Boundary] 检测到重复工具调用: runId=%s, tool=%s, streak=%d, signature=%s", s.runID, s.anomalyLastTool, s.anomalyStreak, s.anomalyLastSignature)
	}
}

// anomalyReminder 渲染重复调用提醒；streak 达到阈值且未超注入上限时非空。
// 渲染幂等且自带标记（每轮会被上下文估算等多次调用，轮末由 consumeAnomalyRender 只结算一次）。
func (s *reactEngineState) anomalyReminder() string {
	threshold := conf.GetReactRuntimeConfig().Loop.AnomalyRepeatThreshold
	if threshold <= 0 || s.anomalyStreak < threshold || s.anomalyInjections >= maxAnomalyInjections {
		return ""
	}
	s.anomalyReminderRendered = true
	toolName := s.anomalyLastTool
	if toolName == "" {
		toolName = "同一工具"
	}
	return fmt.Sprintf("<system-reminder>你已用完全相同的参数连续调用工具 %s 共 %d 次，继续重复不会有不同结果。请改变方法、使用已有结果继续推进，或直接向用户说明当前困境。</system-reminder>", toolName, s.anomalyStreak)
}

// consumeAnomalyRender 在轮末结算提醒注入：本轮确实渲染过提醒则计数 +1。
func (s *reactEngineState) consumeAnomalyRender() {
	if !s.anomalyReminderRendered {
		return
	}
	s.anomalyReminderRendered = false
	s.anomalyInjections++
}

// softLandingReminder 渲染软着陆收尾提醒（软着陆激活期间每轮注入）。
func (s *reactEngineState) softLandingReminder() string {
	if !s.softLandingActive {
		return ""
	}
	return "<system-reminder>本 run 的执行预算即将耗尽（原因：" + s.softLandingReason + "）。请立即停止开启新的多步工作，基于已有结果给出最终回答；未完成事项写入 todo 并明确说明。</system-reminder>"
}

// continuationReminder 渲染输出截断续写指令（一次性，消费后清除）。
func continuationReminder() string {
	return "<system-reminder>上一条输出因达到 max_tokens 上限被截断。请从中断处直接继续输出剩余内容，不要重复已输出内容，不要道歉，不要重新总结。</system-reminder>"
}

// runDeadlineScope 为 run 包装 wall-clock 超时边界；返回替换后的 ctx 与 deadline 信息。
// 超时触发时 context.Cause 为 ErrReactRunTimeout，取消/超时语义在全链路可区分。
func runDeadlineScope(parent context.Context) (context.Context, time.Time, time.Duration, context.CancelFunc) {
	timeout := time.Duration(conf.GetReactRuntimeConfig().Loop.RunTimeoutSec) * time.Second
	if timeout <= 0 {
		return parent, time.Time{}, 0, func() {}
	}
	ctx, cancel := context.WithTimeoutCause(parent, timeout, ErrReactRunTimeout)
	deadline, _ := ctx.Deadline()
	return ctx, deadline, timeout, cancel
}
