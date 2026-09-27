package react

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/metrics"
	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	toolService "react-base-service/service/tool"
)

// executeToolCalls 是“模型 ToolCall -> Runtime 执行”的统一调度入口。
//
// 默认按模型返回顺序串行执行，保证外部副作用顺序和 tool_result 回填顺序稳定；可并行集合
// （delegate_agent + ToolMeta.ConcurrentSafe 只读/隔离型工具，WP1 统一元数据）在多调用同轮时
// 并发执行。即使并行执行，最终 results 仍按原 ToolCall 索引回填，
// 因此下一轮模型看到的 tool_result 顺序不会被 goroutine 完成先后打乱。
//
// 闭合治理（Phase 1）：本函数返回的 results 永远与 calls 等长且每个槽位都有配对 ToolUseID——
// 中断/出错时由 completeToolRoundResults 收口补全（中断登记优先，其次合成"状态未知"），
// 串行中断后的未开始调用直接合成"未执行"，让引擎层无论如何都能为本轮 tool_use 落一条配对结果。
//
// 并行集合受 semaphore 限制：delegate 上限 subagent.max_parallel，只读类工具固定 4；
// 整体并行度 1（无可并行调用）时与历史完全串行等价。
// 并行委派的多个子 run 同时等待用户输入（ask_question/client tool）时，上行消息经
// clientHub 按 toolUseId 路由到各自的等待者（并行 HITL，见 client_hub.go）。
func (s *reactEngineState) executeToolCalls(calls []llm.ToolCall, step int) ([]llm.ToolResultContent, error) {
	// 新一轮清空上一轮的中断结果登记：登记表的生命周期与"一轮调度"一致。
	s.roundInterruptedResults = nil
	results := make([]llm.ToolResultContent, len(calls))
	duplicates := duplicateToolCallIndexes(calls)
	for i := range duplicates {
		results[i] = s.duplicateToolCallResult(calls[i])
	}

	parallelism := s.roundParallelism(calls, duplicates)
	if parallelism <= 1 {
		return s.executeToolCallsSerial(calls, results, duplicates, step)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	recordErr := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}
	sem := make(chan struct{}, parallelism)
	for i, call := range calls {
		if !s.isParallelizableCall(call, duplicates[i]) {
			continue
		}
		wg.Add(1)
		go func(i int, call llm.ToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			result, err := s.executeToolCall(call, step)
			metrics.ObserveToolCall(s.metricToolName(call), start, result.IsError)
			results[i] = result
			recordErr(err)
		}(i, call)
	}
	lastSerialIndex := -1
	serialStopped := false
	for i, call := range calls {
		if s.isParallelizableCall(call, duplicates[i]) {
			continue
		}
		start := time.Now()
		result, err := s.executeToolCall(call, step)
		metrics.ObserveToolCall(s.metricToolName(call), start, result.IsError)
		results[i] = result
		lastSerialIndex = i
		if err != nil {
			// 串行工具出错即停并返回，与历史中止语义一致；取消/断线错误同时会让在途委派子 run 级联收敛。
			// 后续未开始的串行调用在 wg.Wait 后按"未执行"补全，保证本轮 tool_use 全部有配对结果。
			recordErr(err)
			serialStopped = true
			break
		}
	}
	wg.Wait()
	if serialStopped {
		s.fillUnexecutedSerialSuffix(calls, results, duplicates, lastSerialIndex, true)
	}
	completed := s.completeToolRoundResults(calls, results)
	return completed, firstErr
}

// isParallelizableCall 判断一次调用是否进入并行批次（重复调用槽位已填充，永不并行）：
//   - delegate_agent 仅当配置放行（subagent.enabled && max_parallel>1）——多委派并行是显式配置的成本决策，默认串行；
//   - 其余工具按 ToolMeta.ConcurrentSafe（只读/隔离型，无副作用竞争）。
func (s *reactEngineState) isParallelizableCall(call llm.ToolCall, duplicate bool) bool {
	if duplicate {
		return false
	}
	if call.Name == metaToolDelegateAgent {
		if s.req == nil || !s.profile.AllowSubagent {
			return false
		}
		cfg := conf.GetReactRuntimeConfig().SubAgent
		return cfg.SubAgentEnabled() && cfg.MaxParallel > 1
	}
	return s.isConcurrentSafeTool(call)
}

// roundParallelism 返回本轮并行度：无可并行调用返回 1（走纯串行路径）；
// 并行批次含已放行的 delegate 时取 max(subagent.max_parallel, 只读类并发上限)，否则固定只读上限。
func (s *reactEngineState) roundParallelism(calls []llm.ToolCall, duplicates map[int]bool) int {
	parallelCount, delegateAllowed := 0, false
	for i, call := range calls {
		if s.isParallelizableCall(call, duplicates[i]) {
			parallelCount++
			if call.Name == metaToolDelegateAgent {
				delegateAllowed = true
			}
		}
	}
	if parallelCount <= 1 {
		return 1
	}
	limit := concurrentSafeParallelLimit
	if delegateAllowed {
		if mp := conf.GetReactRuntimeConfig().SubAgent.MaxParallel; mp > limit {
			limit = mp
		}
	}
	if parallelCount < limit {
		return parallelCount
	}
	return limit
}

// concurrentSafeParallelLimit 是 ConcurrentSafe（只读）工具同轮并发的固定上限：
// 只读调用无副作用竞争，4 路已足够摊薄多源检索延迟，不随配置放大。
const concurrentSafeParallelLimit = 4

// executeToolCallsSerial 串行执行路径（含去重跳过）；出错时后续调用补"未执行"结果。
func (s *reactEngineState) executeToolCallsSerial(calls []llm.ToolCall, results []llm.ToolResultContent, duplicates map[int]bool, step int) ([]llm.ToolResultContent, error) {
	for i, call := range calls {
		if duplicates[i] {
			continue
		}
		start := time.Now()
		result, err := s.executeToolCall(call, step)
		metrics.ObserveToolCall(s.metricToolName(call), start, result.IsError)
		results[i] = result
		if err != nil {
			s.fillUnexecutedSerialSuffix(calls, results, duplicates, i, false)
			break
		}
	}
	return s.completeToolRoundResults(calls, results), nil
}

// fillUnexecutedSerialSuffix 把串行执行停止点之后的未开始调用补成"未执行"结果（确定无副作用）。
// 并行路径（skipParallel=true）跳过全部并行批次调用（delegate_agent + ConcurrentSafe 工具）：
// 这些 goroutine 在停止点前已全部启动并运行到终态，其结果槽位由 completeToolRoundResults 收口补全。
func (s *reactEngineState) fillUnexecutedSerialSuffix(calls []llm.ToolCall, results []llm.ToolResultContent, duplicates map[int]bool, lastExecuted int, skipParallel bool) {
	for i := lastExecuted + 1; i < len(calls); i++ {
		if duplicates[i] || skipParallel && s.isParallelizableCall(calls[i], false) || strings.TrimSpace(results[i].ToolUseID) != "" {
			continue
		}
		results[i] = s.synthesizeUnexecutedToolResult(calls[i], toolUnexecutedContent)
	}
}

// metricToolName 解析用于指标标签的工具名：execute_tool 反解入参里的业务工具名，其余用元工具名。
func (s *reactEngineState) metricToolName(call llm.ToolCall) string {
	if call.Name == metaToolExecuteTool {
		var payload struct {
			Name     string `json:"name"`
			CallName string `json:"callName"`
		}
		if err := json.Unmarshal(call.Input, &payload); err == nil {
			if payload.CallName != "" {
				return payload.CallName
			}
			if payload.Name != "" {
				return payload.Name
			}
		}
	}
	return call.Name
}

// executeToolCall 执行单个工具调用；保留给非批量路径和后续扩展复用。
// executeToolCall 根据 ToolCall 名称分发到三类执行路径：
//   1. 内置 Meta Tool：Memory、Todo、SubAgent、Workspace 等 Runtime 能力；
//   2. execute_tool：执行已通过 get_tool 激活的 Business Tool；
//   3. Client Tool：需要前端实际执行并通过 WebSocket 回填。
// 所有路径最终都归一为 llm.ToolResultContent，供下一轮模型统一消费。
func (s *reactEngineState) executeToolCall(call llm.ToolCall, step int) (llm.ToolResultContent, error) {
	s.logToolCallInput(call, step)
	// 软着陆收尾窗口：只放行只读/记录类工具，写类/委派/沙箱工具以错误结果回灌，
	// 引导模型立即总结收尾而不是继续开启多步工作。
	if s.softLandingActive {
		if blockedReason, blocked := softLandingToolBlocked(call.Name); blocked {
			return llm.ToolResultContent{ToolUseID: call.ID, Content: blockedReason, IsError: true}, nil
		}
	}
	if isInternalMetaTool(call.Name) {
		if !s.profile.allowsInternalTool(call.Name) {
			return llm.ToolResultContent{ToolUseID: call.ID, Content: fmt.Sprintf("internal tool %s is not allowed by execution profile", call.Name), IsError: true}, nil
		}
		return s.executeInternalTool(call, step)
	}
	tool, ok := s.activeTools[call.Name]
	if !ok {
		// 之前加载过但定义已变化（或已下线）的工具不会被恢复，提示模型刷新而不是笼统报未激活。
		if _, loadedBefore := s.prevToolDefFingerprint[call.Name]; loadedBefore {
			return llm.ToolResultContent{ToolUseID: call.ID, Content: fmt.Sprintf("tool %s definition has changed or it is no longer available, call get_tool to reload it before use", call.Name), IsError: true}, nil
		}
		return llm.ToolResultContent{ToolUseID: call.ID, Content: fmt.Sprintf("tool %s is not active, call get_tool first", call.Name), IsError: true}, nil
	}
	switch toolService.NormalizeToolType(tool.ToolType) {
	case toolService.ToolTypeClient:
		return s.executeClientTool(call, tool, step, "")
	case toolService.ToolTypeHTTP:
		return s.executeServerTool(call, tool, step, "")
	default:
		return llm.ToolResultContent{ToolUseID: call.ID, Content: fmt.Sprintf("unsupported tool type: %s", tool.ToolType), IsError: true}, nil
	}
}

// executeServerTool 执行后端托管的 Business Tool。
//
// HTTP/MCP 传输细节已经下沉到 service/tool Runtime；本层只保留 Agent Runtime 语义：
// 危险操作确认、事件发射、取消/超时分类、ResultRef、大结果回填和 Async Task 快照。
// 这样新增新的服务端 Tool Transport 时，不需要修改 ReAct 主循环。
// HTTP/MCP 传输细节由 service/tool Runtime 统一负责；本层只保留确认、事件、取消、resultRef 与异步任务等 Agent Runtime 语义。
func (s *reactEngineState) executeServerTool(call llm.ToolCall, tool model.Tool, step int, description string) (llm.ToolResultContent, error) {
	// 只读执行域硬拦截（WP2）：enforceReadOnlyTools 的 run 内，非 readOnly 声明的服务端工具直接拒绝。
	if blocked := s.readOnlyViolationResult(call, tool, executedByServer); blocked != nil {
		_ = s.emitter.EmitStep(step, EventToolUseEnd, params.ReactToolUseEndPayload{ToolUseID: call.ID, Content: blocked.Content, IsError: true, ExecutedBy: executedByServer, Status: toolExecutionStatusRejected, DurationMs: 0})
		return *blocked, nil
	}
	meta := businessToolMeta(tool)
	_ = s.emitter.EmitStep(step, EventToolUseStart, params.ReactToolUseStartPayload{ToolUseID: call.ID, ToolName: tool.Name, ToolInput: json.RawMessage(call.Input), Description: strings.TrimSpace(description), ExecutedBy: executedByServer, Status: toolExecutionStatusRunning, RiskLevel: meta.RiskLevel, ReadOnly: meta.ReadOnly})
	start := time.Now()

	// 危险操作确认门（P2-3）：permission_mode 命中时先等人工允许；拒绝按 rejected 工具结果回填。
	approved, rejectReason, confirmErr := s.confirmServerToolIfNeeded(call, tool, json.RawMessage(call.Input), step, start)
	if confirmErr != nil {
		// 确认等待被取消/断线时中断结果已登记（闭合治理 Phase 1）：取回登记结果保证槽位配对。
		if recorded, ok := s.interruptedToolResult(call.ID); ok {
			return recorded, confirmErr
		}
		return llm.ToolResultContent{}, confirmErr
	}
	if !approved {
		content := renderToolRejectedResult(tool, rejectReason)
		_ = s.emitter.EmitStep(step, EventToolUseEnd, params.ReactToolUseEndPayload{ToolUseID: call.ID, Content: content, IsError: true, ExecutedBy: executedByServer, Status: toolExecutionStatusRejected, DurationMs: time.Since(start).Milliseconds()})
		return llm.ToolResultContent{ToolUseID: call.ID, Content: content, IsError: true}, nil
	}

	execution, err := s.services.serverToolExecutor().Execute(s.runCtx, toolService.ExecuteRequest{
		Tool:    tool,
		Input:   json.RawMessage(call.Input),
		Cookies: requestCookies(s.ctx),
	})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		cause := context.Cause(s.runCtx)
		if errors.Is(cause, ErrReactRunCancelled) {
			result := s.closeInterruptedToolUse(call, step, executedByServer, start, ErrReactRunCancelled)
			return result, ErrReactRunCancelled
		}
		if errors.Is(cause, context.DeadlineExceeded) {
			return llm.ToolResultContent{ToolUseID: call.ID, Content: "tool execution exceeded current step active timeout", IsError: true}, nil
		}
		result := s.closeInterruptedToolUse(call, step, executedByServer, start, ErrReactClientDisconnected)
		return result, ErrReactClientDisconnected
	}

	content := execution.Content
	// 输出预算按工具声明差异化（WP1）：config.maxOutputBytes 覆盖全局 InlineLimitBytes。
	normalized := normalizeToolResultWithLimit(call.ID, content, err != nil, executedByServer, businessToolMeta(tool).MaxOutputBytes)
	if err != nil {
		normalized.Content = err.Error()
	}
	if normalized.ResultRef != "" {
		// resultRef 只保存完整大结果，给模型回填的是预览和可分页读取的引用，避免撑爆上下文。
		if storeErr := storeResultRef(s.ctx, s.sessionID, s.runID, call.ID, normalized.ResultRef, content); storeErr != nil {
			return llm.ToolResultContent{}, storeErr
		}
	}
	_ = s.emitter.EmitStep(step, EventToolUseEnd, params.ReactToolUseEndPayload{ToolUseID: call.ID, Content: normalized.Content, ResultRef: normalized.ResultRef, Truncated: normalized.Truncated, OmittedChars: normalized.OmittedChars, IsError: normalized.IsError, ExecutedBy: executedByServer, Status: normalized.Status, DurationMs: time.Since(start).Milliseconds(), RiskLevel: meta.RiskLevel, ReadOnly: meta.ReadOnly})
	// 异步提交型工具成功后保留提交快照，作为 Session 级提醒注入后续 run 的模型上下文。
	s.recordAsyncSubmit(tool, call.Input, content, normalized)
	return llm.ToolResultContent{ToolUseID: call.ID, Content: normalized.LLMContent(), IsError: normalized.IsError}, nil
}
