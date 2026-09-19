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
// 默认按模型返回顺序串行执行，保证外部副作用顺序和 tool_result 回填顺序稳定；只有 delegate_agent
// 被允许并行，因为它创建隔离的子 ReactRun。即使并行执行，最终 results 仍按原 ToolCall 索引回填，
// 因此下一轮模型看到的 tool_result 顺序不会被 goroutine 完成先后打乱。
//
// 
// delegate_agent 调用之间可并行（agent 委派无副作用），受 subagent.max_parallel 限制；
// 其余工具保持串行；并行度 1（默认）时与历史完全串行等价。
// 并行委派的多个子 run 同时等待用户输入（ask_question/client tool）时，上行消息经
// clientHub 按 toolUseId 路由到各自的等待者（并行 HITL，见 client_hub.go）。
func (s *reactEngineState) executeToolCalls(calls []llm.ToolCall, step int) ([]llm.ToolResultContent, error) {
	if s.delegateParallelism(len(calls)) <= 1 {
		return s.executeToolCallsSerial(calls, step)
	}

	results := make([]llm.ToolResultContent, len(calls))
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
	sem := make(chan struct{}, s.delegateParallelism(len(calls)))
	for i, call := range calls {
		if call.Name != metaToolDelegateAgent {
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
	for i, call := range calls {
		if call.Name == metaToolDelegateAgent {
			continue
		}
		start := time.Now()
		result, err := s.executeToolCall(call, step)
		metrics.ObserveToolCall(s.metricToolName(call), start, result.IsError)
		results[i] = result
		if err != nil {
			// 串行工具出错即停并返回，与历史中止语义一致；取消/断线错误同时会让在途委派子 run 级联收敛。
			recordErr(err)
			break
		}
	}
	wg.Wait()
	if firstErr != nil {
		return results, firstErr
	}
	return results, nil
}

func (s *reactEngineState) executeToolCallsSerial(calls []llm.ToolCall, step int) ([]llm.ToolResultContent, error) {
	results := make([]llm.ToolResultContent, len(calls))
	for i, call := range calls {
		start := time.Now()
		result, err := s.executeToolCall(call, step)
		metrics.ObserveToolCall(s.metricToolName(call), start, result.IsError)
		results[i] = result
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

// delegateParallelism 返回本轮 delegate_agent 的并行度：
// 仅当同轮存在多个委派调用、执行档案放行且配置 max_parallel>1 时取配置值，否则 1。
func (s *reactEngineState) delegateParallelism(callCount int) int {
	if callCount <= 1 || s.req == nil || !s.profile.AllowSubagent {
		return 1
	}
	cfg := conf.GetReactRuntimeConfig().SubAgent
	if !cfg.SubAgentEnabled() || cfg.MaxParallel <= 1 {
		return 1
	}
	return cfg.MaxParallel
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
	_ = s.emitter.EmitStep(step, EventToolUseStart, params.ReactToolUseStartPayload{ToolUseID: call.ID, ToolName: tool.Name, ToolInput: json.RawMessage(call.Input), Description: strings.TrimSpace(description), ExecutedBy: executedByServer, Status: toolExecutionStatusRunning})
	start := time.Now()

	// 危险操作确认门（P2-3）：permission_mode 命中时先等人工允许；拒绝按 rejected 工具结果回填。
	approved, confirmErr := s.confirmServerToolIfNeeded(call, tool, json.RawMessage(call.Input), step, start)
	if confirmErr != nil {
		return llm.ToolResultContent{}, confirmErr
	}
	if !approved {
		content := renderToolRejectedResult(tool, "")
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
	normalized := normalizeToolResult(call.ID, content, err != nil, executedByServer)
	if err != nil {
		normalized.Content = err.Error()
	}
	if normalized.ResultRef != "" {
		// resultRef 只保存完整大结果，给模型回填的是预览和可分页读取的引用，避免撑爆上下文。
		if storeErr := storeResultRef(s.ctx, s.sessionID, s.runID, call.ID, normalized.ResultRef, content); storeErr != nil {
			return llm.ToolResultContent{}, storeErr
		}
	}
	_ = s.emitter.EmitStep(step, EventToolUseEnd, params.ReactToolUseEndPayload{ToolUseID: call.ID, Content: normalized.Content, ResultRef: normalized.ResultRef, Truncated: normalized.Truncated, OmittedChars: normalized.OmittedChars, IsError: normalized.IsError, ExecutedBy: executedByServer, Status: normalized.Status, DurationMs: time.Since(start).Milliseconds()})
	// 异步提交型工具成功后保留提交快照，作为 Session 级提醒注入后续 run 的模型上下文。
	s.recordAsyncSubmit(tool, call.Input, content, normalized)
	return llm.ToolResultContent{ToolUseID: call.ID, Content: normalized.LLMContent(), IsError: normalized.IsError}, nil
}
