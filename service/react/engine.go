package react

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/metrics"
	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"react-base-service/golib/zlog"
)

const (
	EventToolUseStart       = "tool_use_start"
	EventToolUseEnd         = "tool_use_end"
	EventClientToolUseStart = "client_tool_use_start"
	EventClientToolUseEnd   = "client_tool_use_end"
	EventCompactStart       = "compact_start"
	EventCompactEnd         = "compact_end"
	EventTodoUpdate         = "todo_update"
	EventModelFallback      = "model_fallback"

	executedByServer   = "server"
	executedByClient   = "client"
	executedByInternal = "internal"
)

type ClientMessageReader func() (params.ReactWSMessage, error)

type reactClientToolCall struct {
	index int
	call  llm.ToolCall
	tool  model.Tool
}

type collectLLMStreamResult struct {
	Content            string
	ReasoningContent   string
	ReasoningSignature string
	ToolCalls          []llm.ToolCall
	InputTokens        int
	OutputTokens       int
	StopReason         string
	TerminationReason  string
}

// executeReactLoop 是 ReAct Runtime 的核心状态机。
//
// 每一轮严格按以下顺序推进：
//   1. 持久化 stepIndex，并在需要时压缩上下文；
//   2. 只向模型暴露稳定 Meta Tool，业务 Tool Schema 通过 get_tool 按需加载；
//   3. 流式调用模型，收集正文/思考/tool_calls/usage；
//   4. 持久化 assistant 消息；
//   5. 无 tool_use 时收敛为最终回答；
//   6. 有 tool_use 时执行工具、持久化 tool_result，并把结果追加到下一轮模型上下文。
//
// 关键不变量：落库消息顺序必须与送入下一轮模型的顺序一致；取消/断连时尽量保存已经收到的
// assistant_partial，避免“前端看过内容但历史里完全不存在”。
func executeReactLoop(ctx *gin.Context, runCtx context.Context, req *runtimeRequest, runID, sessionID string, emitter *runEventEmitter, readClient ClientMessageReader) error {
	currentModel, failoverModels := configuredReactModelRouting(req)
	client, err := llm.GetClientWithUserModel(req.apiKey, currentModel.ModelKey)
	if err != nil {
		return components.ErrorLLMRequest.Sprintf(err.Error())
	}
	prefixDebugKey := fmt.Sprintf("react:%s", sessionID)

	messages, err := buildInitialChatMessages(ctx, req, runID, sessionID)
	if err != nil {
		return err
	}

	state := newReactEngineState(ctx, runCtx, req, runID, sessionID, client, currentModel, failoverModels, emitter, readClient, messages)
	// 恢复上一个 run 已加载且定义未变化的 Business Tool，避免模型按历史上下文直接 execute_tool 时空转报错。
	if err := state.restoreActiveToolsFromPreviousRun(); err != nil {
		zlog.Warnf(ctx, "[React] 恢复历史已加载工具失败(忽略,模型可重新 get_tool): runId=%s, sessionId=%s, err=%v", runID, sessionID, err)
	}
	// 加载当前 Session 的未完结异步任务，作为临时提醒注入模型上下文（子 run 不注入）。
	if state.profile.InjectAsyncTaskReminder {
		if pendingTasks, hasMore, err := loadPendingReactAsyncTasks(ctx, sessionID); err != nil {
			zlog.Warnf(ctx, "[React] 加载未完结异步任务失败(忽略,本次不注入提醒): runId=%s, sessionId=%s, err=%v", runID, sessionID, err)
		} else {
			state.pendingAsyncTasks = pendingTasks
			state.pendingAsyncTasksHasMore = hasMore
		}
	}

	maxSteps := normalizeMaxSteps(req.payload.MaxSteps)
	for step := 0; step < maxSteps; step++ {
		if err := model.UpdateReactRunByRunID(ctx, runID, map[string]interface{}{"step_index": step}); err != nil {
			return err
		}
		if err := maybeCompactContext(state, step); err != nil {
			return err
		}

		// 每轮只暴露稳定的 Meta Tool；业务工具 schema 通过 get_tool 返回到消息上下文，并由 execute_tool 统一执行。
		tools := state.buildToolDefinitions()

		var roundResult modelRoundResult
		modelRoundStart := time.Now()
		roundResult, err = state.callModelRound(step, prefixDebugKey, tools)
		metrics.ModelRoundDuration.WithLabelValues(roundResult.Model.ModelKey + "/" + roundResult.Model.ModelVersion).Observe(time.Since(modelRoundStart).Seconds())
		streamResult := roundResult.Stream
		state.logModelRoundResult(step, roundResult.Model, streamResult)
		if IsReactRunCancelled(err) {
			if persistErr := state.persistPartialAssistant(streamResult, roundResult.Model, step); persistErr != nil {
				return persistErr
			}
			return ErrReactRunCancelled
		}
		if IsReactClientDisconnected(err) {
			if persistErr := state.persistPartialAssistant(streamResult, roundResult.Model, step); persistErr != nil {
				return persistErr
			}
			return err
		}
		if err != nil {
			return err
		}
		if err := state.addTokenUsage(streamResult.InputTokens, streamResult.OutputTokens); err != nil {
			return err
		}
		if err := state.updateLastTokenUsage(streamResult.InputTokens, streamResult.OutputTokens); err != nil {
			return err
		}

		assistantMsg, assistantRef, err := persistAssistantMessage(ctx, req, runID, sessionID, roundResult.Model, streamResult.Content, streamResult.ReasoningContent, streamResult.ReasoningSignature, streamResult.ToolCalls, step)
		if err != nil {
			return err
		}

		if streamResult.StopReason != "tool_use" || len(streamResult.ToolCalls) == 0 {
			// 没有工具调用时说明本轮已经得到最终回答，直接收敛 run 状态并结束循环。
			return state.finish(streamResult.Content, streamResult.TerminationReason)
		}

		// 子代理预算（P3）：仅在还有后续工具轮次时检查——已产出最终回答的轮次保留完成态
		//（OH max_budget_per_run 同款语义）；超限错误经委派软错误通道回填父循环。
		if err := state.checkTokenBudget(req.tokenBudget); err != nil {
			return err
		}

		results, err := state.executeToolCalls(streamResult.ToolCalls, step)
		if err != nil {
			return err
		}
		_, userMsg := llm.BuildToolRoundMessagesWithReasoning(streamResult.Content, streamResult.ReasoningContent, streamResult.ReasoningSignature, streamResult.ToolCalls, results)
		toolResultRef, err := persistToolResultMessage(ctx, req, runID, sessionID, results, step)
		if err != nil {
			return err
		}
		state.messages = append(state.messages, assistantMsg, userMsg)
		state.messageRefs = append(state.messageRefs, []reactMessageRef{assistantRef}, []reactMessageRef{toolResultRef})
	}

	return components.ErrorReactRunFailed.Sprintf("exceed maxSteps")
}

// addTokenUsage 累加当前进程实际产生的模型 usage，并立即写回 ReactRun。
//
// token 统计不等到 Run 结束统一落库，因为流式中断、Client 断连或 Tool 报错都可能提前终止。
// 实时写回可以让成本、预算和排障口径在异常路径上仍尽量接近真实值。
func (s *reactEngineState) addTokenUsage(inputTokens, outputTokens int) error {
	if inputTokens == 0 && outputTokens == 0 && s.cacheReadTokens == 0 && s.cacheCreateTokens == 0 {
		return nil
	}
	s.inputTokens += inputTokens
	s.outputTokens += outputTokens
	return model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{
		"total_input_tokens":  s.runBaseInputTokens + s.inputTokens,
		"total_output_tokens": s.runBaseOutputTokens + s.outputTokens,
		"cache_read_tokens":   s.runBaseCacheReadTokens + s.cacheReadTokens,
		"cache_create_tokens": s.runBaseCacheCreateTokens + s.cacheCreateTokens,
	})
}

// updateLastTokenUsage 记录最近一次成功拿到 usage 的模型轮次。
// last_* 主要服务上下文窗口占用与压缩判断；total_* 则表示整个 Run 的累计消耗。
func (s *reactEngineState) updateLastTokenUsage(inputTokens, outputTokens int) error {
	if inputTokens == 0 && outputTokens == 0 {
		return nil
	}
	s.lastInputTokens = inputTokens
	s.lastOutputTokens = outputTokens
	return model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{
		"last_input_tokens":  inputTokens,
		"last_output_tokens": outputTokens,
	})
}

// tokenBudgetExceeded 判定子 run 递归 token 口径是否超限（P3 子代理预算，0=不限）。
func tokenBudgetExceeded(budget, inputTokens, outputTokens, delegatedInputTokens, delegatedOutputTokens int) bool {
	if budget <= 0 {
		return false
	}
	return inputTokens+outputTokens+delegatedInputTokens+delegatedOutputTokens > budget
}

// checkTokenBudget 每轮模型调用后按递归口径（本 run 输入+输出+委派孙代理 delegated_*）检查预算；
// 超限返回预算错误，run 走 error 态并经委派软错误通道回填父循环。
func (s *reactEngineState) checkTokenBudget(budget int) error {
	delegatedInput := int(s.delegatedInputTokens.Load())
	delegatedOutput := int(s.delegatedOutputTokens.Load())
	if !tokenBudgetExceeded(budget, s.inputTokens, s.outputTokens, delegatedInput, delegatedOutput) {
		return nil
	}
	return components.ErrorSubAgentBudgetExceeded.Sprintf(
		s.inputTokens+s.outputTokens+delegatedInput+delegatedOutput, budget, s.agentPath)
}

// persistPartialAssistant 在取消或断连发生于模型流式输出过程中时，保存已经收到的部分内容。
// 这类消息使用 assistant_partial 类型：可用于历史回放和排障，但不会被当成完整 assistant 回答恢复进上下文。
func (s *reactEngineState) persistPartialAssistant(result collectLLMStreamResult, actualModel reactModelTarget, step int) error {
	if err := s.addTokenUsage(result.InputTokens, result.OutputTokens); err != nil {
		return err
	}
	if strings.TrimSpace(result.Content) == "" && strings.TrimSpace(result.ReasoningContent) == "" && len(result.ToolCalls) == 0 {
		return nil
	}
	message, ref, err := persistAssistantMessageWithType(s.ctx, s.req, s.runID, s.sessionID, actualModel, result.Content, result.ReasoningContent, result.ReasoningSignature, result.ToolCalls, step, model.ReactMessageTypeAssistantPartial)
	if err != nil {
		return err
	}
	s.messages = append(s.messages, message)
	s.messageRefs = append(s.messageRefs, []reactMessageRef{ref})
	return nil
}

// contextMessages 在发送给模型前按 ExecutionProfile 追加临时提醒；持久化消息不受影响。
func (s *reactEngineState) contextMessages() []llm.ChatMessage {
	reminders := make([]string, 0, 2)
	if s.profile.InjectAsyncTaskReminder {
		if reminder := renderReactAsyncTaskReminder(s.pendingAsyncTasks, s.pendingAsyncTasksHasMore); strings.TrimSpace(reminder) != "" {
			reminders = append(reminders, reminder)
		}
	}
	if s.profile.AllowTodo {
		if reminder := renderReactTodoReminder(s.todoStateJSON); strings.TrimSpace(reminder) != "" {
			reminders = append(reminders, reminder)
		}
	}
	if len(reminders) == 0 {
		return s.messages
	}
	messages := make([]llm.ChatMessage, 0, len(s.messages)+1)
	messages = append(messages, s.messages...)
	messages = append(messages, llm.ChatMessage{Role: "user", Content: strings.Join(reminders, "\n\n")})
	return messages
}

// buildToolDefinitions 按执行档案暴露工具：reflection 只暴露记忆三工具，其余暴露完整 Meta Tool 集；
// delegate_agent 在 subagent 开启、可见 agent 非空且未达深度上限时动态装配。
// req 为 nil 时（单测构造的最小引擎状态）回退到完整集合。
func (s *reactEngineState) buildToolDefinitions() []llm.ToolDefinition {
	if s.req == nil {
		return internalMetaToolDefinitions()
	}
	return runtimeToolDefinitions(s.req, s.profile)
}

func sortedActiveToolNames(tools map[string]model.Tool) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *reactEngineState) logModelRoundResult(step int, actualModel reactModelTarget, result collectLLMStreamResult) {
	zlog.Infof(s.ctx, "[React.ModelRound] 模型轮次完成: runId=%s, step=%d, model=%s/%s, stopReason=%s, toolCallCount=%d, toolNames=%v", s.runID, step, actualModel.ModelKey, actualModel.ModelVersion, result.StopReason, len(result.ToolCalls), reactToolCallNames(result.ToolCalls))
}

func (s *reactEngineState) logToolCallInput(call llm.ToolCall, step int) {
	payload := map[string]interface{}{
		"runId":     s.runID,
		"sessionId": s.sessionID,
		"step":      step,
		"toolUseId": call.ID,
		"toolName":  call.Name,
		"input":     json.RawMessage(call.Input),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		zlog.Infof(s.ctx, "[React.Debug] 工具调用参数序列化失败: runId=%s, sessionId=%s, step=%d, toolUseId=%s, toolName=%s, err=%v", s.runID, s.sessionID, step, call.ID, call.Name, err)
		return
	}
	zlog.Infof(s.ctx, "[React.Debug] 工具调用参数: %s", string(data))
}

// collectLLMStream 聚合模型流式输出；只有收到真实 reasoning 增量时才推送 thought_start/thought_delta。
func (s *reactEngineState) collectLLMStream(stream <-chan llm.StreamChunk, step int, cancelLLM context.CancelFunc, idleTimeout time.Duration) (collectLLMStreamResult, error) {
	return s.collectLLMStreamWithEmitter(stream, step, cancelLLM, idleTimeout, true)
}

func (s *reactEngineState) collectLLMStreamWithEmitter(stream <-chan llm.StreamChunk, step int, cancelLLM context.CancelFunc, idleTimeout time.Duration, emitEvents bool) (collectLLMStreamResult, error) {
	var content strings.Builder
	var reasoningContent strings.Builder
	var reasoningBlockContent strings.Builder
	var reasoningSignature string
	var toolCalls []llm.ToolCall
	var inputTokens, outputTokens int
	var cacheReadTokens, cacheCreateTokens int
	var stopReason, terminationReason string
	thoughtActive := false
	contentActive := false
	receivedDone := false

	result := func() collectLLMStreamResult {
		return collectLLMStreamResult{
			Content:            content.String(),
			ReasoningContent:   reasoningContent.String(),
			ReasoningSignature: reasoningSignature,
			ToolCalls:          toolCalls,
			InputTokens:        inputTokens,
			OutputTokens:       outputTokens,
			StopReason:         stopReason,
			TerminationReason:  terminationReason,
		}
	}

	startContent := func() error {
		if contentActive {
			return nil
		}
		contentActive = true
		if !emitEvents {
			return nil
		}
		return s.emitter.EmitStep(step, EventContentStart, params.ReactContentStartPayload{})
	}
	closeContent := func() error {
		if !contentActive {
			return nil
		}
		contentActive = false
		if !emitEvents {
			return nil
		}
		return s.emitter.EmitStep(step, EventContentEnd, params.ReactContentEndPayload{
			Content:           content.String(),
			InputTokens:       s.inputTokens + inputTokens,
			OutputTokens:      s.outputTokens + outputTokens,
			CacheReadTokens:   s.cacheReadTokens,
			CacheCreateTokens: s.cacheCreateTokens,
			ContextUsedTokens: inputTokens + outputTokens,
			MaxContextTokens:  conf.GetReactRuntimeConfig().ContextCompact.TokenTrigger,
		})
	}

	startThought := func() error {
		if thoughtActive {
			return nil
		}
		thoughtActive = true
		if !emitEvents {
			return nil
		}
		return s.emitter.EmitStep(step, EventThoughtStart, params.ReactThoughtStartPayload{})
	}
	// done 表示本次关闭发生在 chunk.Done 分支：只有此时 token 才齐，才把用量带进 thought_end；
	// 中途因正文开始而关闭思考块时 token 还是 0，不带。
	closeThought := func(done bool) error {
		if !thoughtActive {
			return nil
		}
		thoughtActive = false
		if !emitEvents {
			reasoningBlockContent.Reset()
			return nil
		}
		payload := params.ReactThoughtEndPayload{Content: reasoningBlockContent.String()}
		if done {
			payload.InputTokens = s.inputTokens + inputTokens
			payload.OutputTokens = s.outputTokens + outputTokens
			payload.CacheReadTokens = s.cacheReadTokens
			payload.CacheCreateTokens = s.cacheCreateTokens
			payload.ContextUsedTokens = inputTokens + outputTokens
			payload.MaxContextTokens = conf.GetReactRuntimeConfig().ContextCompact.TokenTrigger
		}
		err := s.emitter.EmitStep(step, EventThoughtEnd, payload)
		reasoningBlockContent.Reset()
		return err
	}

	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

consume:
	for {
		var chunk llm.StreamChunk
		select {
		case received, ok := <-stream:
			if !ok {
				break consume
			}
			chunk = received
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleTimeout)
		case <-idleTimer.C:
			cancelLLM()
			return result(), newRetryableModelError("stream_idle_timeout", components.ErrorLLMStream.Sprintf("模型流式响应空闲超时：%ds 内未收到任何输出", int(idleTimeout/time.Second)))
		}

		if chunk.Error != nil {
			if errors.Is(chunk.Error, context.Canceled) {
				cause := context.Cause(s.runCtx)
				if errors.Is(cause, ErrReactRunCancelled) {
					return result(), ErrReactRunCancelled
				}
				if errors.Is(cause, context.DeadlineExceeded) {
					return result(), context.DeadlineExceeded
				}
				return result(), ErrReactClientDisconnected
			}
			return result(), newRetryableModelError("stream_error", components.ErrorLLMStream.Sprintf(chunk.Error.Error()))
		}
		if chunk.ReasoningContent != "" {
			if err := startThought(); err != nil {
				return result(), err
			}
			reasoningContent.WriteString(chunk.ReasoningContent)
			reasoningBlockContent.WriteString(chunk.ReasoningContent)
			if emitEvents {
				if err := s.emitter.EmitStep(step, EventThoughtDelta, params.ReactThoughtDeltaPayload{ContentDelta: chunk.ReasoningContent}); err != nil {
					return result(), err
				}
			}
		}
		if chunk.ReasoningSignature != "" {
			reasoningSignature = chunk.ReasoningSignature
		}
		if chunk.Content != "" {
			if err := closeThought(false); err != nil {
				return result(), err
			}
			if err := startContent(); err != nil {
				return result(), err
			}
			content.WriteString(chunk.Content)
			if emitEvents {
				if err := s.emitter.EmitStep(step, EventContentDelta, params.ReactContentDeltaPayload{ContentDelta: chunk.Content}); err != nil {
					return result(), err
				}
			}
		}
		if chunk.Done {
			receivedDone = true
			inputTokens = chunk.InputTokens
			outputTokens = chunk.OutputTokens
			cacheReadTokens = chunk.CacheReadTokens
			cacheCreateTokens = chunk.CacheCreateTokens
			s.cacheReadTokens += cacheReadTokens
			s.cacheCreateTokens += cacheCreateTokens
			stopReason = chunk.StopReason
			terminationReason = chunk.TerminationReason
			if len(chunk.ToolCalls) > 0 {
				toolCalls = chunk.ToolCalls
			}
			if err := closeThought(true); err != nil {
				return result(), err
			}
			if err := closeContent(); err != nil {
				return result(), err
			}
			if chunk.Cancelled {
				return result(), ErrReactRunCancelled
			}
		}
	}
	if errors.Is(s.runCtx.Err(), context.Canceled) {
		cause := context.Cause(s.runCtx)
		if errors.Is(cause, ErrReactRunCancelled) {
			return result(), ErrReactRunCancelled
		}
		if errors.Is(cause, context.DeadlineExceeded) {
			return result(), context.DeadlineExceeded
		}
		return result(), ErrReactClientDisconnected
	}
	if !receivedDone {
		if err := closeThought(false); err != nil {
			return result(), err
		}
		if err := closeContent(); err != nil {
			return result(), err
		}
		return result(), newRetryableModelError("upstream_eof", components.ErrorLLMStream.Sprintf("模型流式响应未正常结束"))
	}
	if err := closeThought(false); err != nil {
		return result(), err
	}
	if err := closeContent(); err != nil {
		return result(), err
	}
	return result(), nil
}

// finish 收敛 run 的最终状态，更新会话摘要，并向前端发送 done 事件。
// 子 run（agentPath 非空）不更新会话摘要：last_run_id/last_message 由外层 run 收敛时统一更新。
func (s *reactEngineState) finish(content, terminationReason string) error {
	metrics.RunsTotal.WithLabelValues("finished").Inc()
	if err := model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{
		"state":               model.ReactRunStateFinished,
		"total_input_tokens":  s.inputTokens,
		"total_output_tokens": s.outputTokens,
		"cache_read_tokens":   s.cacheReadTokens,
		"cache_create_tokens": s.cacheCreateTokens,
	}); err != nil {
		return err
	}
	if s.agentPath == "" {
		_ = model.UpdateReactSessionBySessionID(s.ctx, s.sessionID, map[string]interface{}{
			"last_run_id":  s.runID,
			"last_message": trimRunLastMessage(content),
		})
	}
	return s.emitter.Emit(EventDone, params.ReactDonePayload{
		InputTokens:           s.inputTokens,
		OutputTokens:          s.outputTokens,
		CacheReadTokens:       s.cacheReadTokens,
		CacheCreateTokens:     s.cacheCreateTokens,
		ContextUsedTokens:     s.contextUsedTokens(content),
		MaxContextTokens:      conf.GetReactRuntimeConfig().ContextCompact.TokenTrigger,
		TerminationReason:     terminationReason,
		DelegatedInputTokens:  int(s.delegatedInputTokens.Load()),
		DelegatedOutputTokens: int(s.delegatedOutputTokens.Load()),
	})
}

func (s *reactEngineState) contextUsedTokens(finalContent string) int {
	used := s.lastInputTokens + s.lastOutputTokens
	if used > 0 {
		return used
	}
	return estimateToolDefinitionsTokens(s.buildToolDefinitions()) + estimateMessagesTokens(s.contextMessages()) + estimateMessagesTokens([]llm.ChatMessage{{Role: model.ReactMessageRoleAssistant, Content: finalContent}})
}

// buildInitialChatMessages 组装当前 run 初始上下文：当前 system 固定在最前，历史消息保持原序，当前 user 追加到最后。
func buildInitialChatMessages(ctx *gin.Context, req *runtimeRequest, runID, sessionID string) ([]llm.ChatMessage, error) {
	base := buildInitialMessages(req)
	currentMessages := llm.LLMMessagesToChatMessages(base)
	currentRefs := buildCurrentMessageRefs(currentMessages, req.modelUserMessageRef)
	messages, messageRefs := mergeSystemHistoryIntoCurrentSystem(req.historyMessages, req.historyMessageRefs, currentMessages, currentRefs)
	req.historyMessageRefs = messageRefs
	return messages, nil
}

func buildCurrentMessageRefs(messages []llm.ChatMessage, userRef reactMessageRef) [][]reactMessageRef {
	refs := make([][]reactMessageRef, 0, len(messages))
	for _, message := range messages {
		if message.Role == model.ReactMessageRoleUser {
			refs = append(refs, []reactMessageRef{userRef})
			continue
		}
		refs = append(refs, nil)
	}
	return refs
}

func mergeSystemHistoryIntoCurrentSystem(historyMessages []llm.ChatMessage, historyRefs [][]reactMessageRef, currentMessages []llm.ChatMessage, currentRefs [][]reactMessageRef) ([]llm.ChatMessage, [][]reactMessageRef) {
	messages := make([]llm.ChatMessage, 0, len(historyMessages)+len(currentMessages))
	messageRefs := make([][]reactMessageRef, 0, len(historyRefs)+len(currentRefs))

	for i, message := range currentMessages {
		if message.Role == model.ReactMessageRoleSystem && strings.TrimSpace(message.Content) != "" {
			messages = append(messages, message)
			messageRefs = append(messageRefs, refsAt(currentRefs, i))
		}
	}
	for i, message := range historyMessages {
		messages = append(messages, message)
		messageRefs = append(messageRefs, refsAt(historyRefs, i))
	}
	for i, message := range currentMessages {
		if message.Role == model.ReactMessageRoleSystem {
			continue
		}
		messages = append(messages, message)
		messageRefs = append(messageRefs, refsAt(currentRefs, i))
	}
	return messages, messageRefs
}

func refsAt(refs [][]reactMessageRef, index int) []reactMessageRef {
	if index < 0 || index >= len(refs) {
		return nil
	}
	return refs[index]
}

func reactMessagesToChatMessagesWithRefs(storedMessages []model.ReactMessage) ([]llm.ChatMessage, [][]reactMessageRef) {
	filteredMessages := filterCoveredReactMessages(storedMessages)
	messages := make([]llm.ChatMessage, 0, len(filteredMessages))
	refs := make([][]reactMessageRef, 0, len(filteredMessages))
	for _, stored := range filteredMessages {
		message, ok := reactMessageToChatMessage(stored)
		if ok {
			messages = append(messages, message)
			refs = append(refs, []reactMessageRef{reactMessageRefFromStored(stored)})
		}
	}
	return messages, refs
}

func filterCoveredReactMessages(storedMessages []model.ReactMessage) []model.ReactMessage {
	covered := make(map[string]map[int]bool)
	for _, stored := range storedMessages {
		if stored.MessageType != model.ReactMessageTypeCompactSummary {
			continue
		}
		for _, ref := range parseCompactCoveredThrough(stored.ContentJSON) {
			if ref.RunID == "" || ref.Seq <= 0 {
				continue
			}
			if covered[ref.RunID] == nil {
				covered[ref.RunID] = make(map[int]bool)
			}
			if covered[ref.RunID][ref.Seq] {
				continue
			}
			for seq := 1; seq <= ref.Seq; seq++ {
				covered[ref.RunID][seq] = true
			}
		}
	}
	filtered := make([]model.ReactMessage, 0, len(storedMessages))
	for _, stored := range storedMessages {
		if covered[stored.RunID][stored.Seq] {
			continue
		}
		filtered = append(filtered, stored)
	}
	return filtered
}

func parseCompactCoveredThrough(contentJSON string) []reactMessageRef {
	var content compactSummaryContent
	if err := json.Unmarshal([]byte(contentJSON), &content); err != nil {
		return nil
	}
	return content.CoveredThrough
}

func reactMessageRefFromStored(stored model.ReactMessage) reactMessageRef {
	return reactMessageRef{RunID: stored.RunID, MessageID: stored.MessageID, Seq: stored.Seq}
}

func reactMessagesToChatMessages(storedMessages []model.ReactMessage) []llm.ChatMessage {
	messages, _ := reactMessagesToChatMessagesWithRefs(storedMessages)
	return messages
}

func reactMessageToChatMessage(stored model.ReactMessage) (llm.ChatMessage, bool) {
	if stored.MessageType == model.ReactMessageTypeAssistantPartial {
		return llm.ChatMessage{}, false
	}
	if message, ok := parseStoredModelMessage(stored.ContentJSON); ok {
		return message, true
	}
	switch stored.MessageType {
	case model.ReactMessageTypeUserInput:
		var content historyUserInputContent
		if err := json.Unmarshal([]byte(stored.ContentJSON), &content); err != nil {
			return llm.ChatMessage{}, false
		}
		if strings.TrimSpace(content.Content) == "" {
			return llm.ChatMessage{}, false
		}
		// 附件历史只按快照渲染清单，不再重新下载内容；模型当时读过的内容已作为工具结果消息留在历史里。
		attachmentText := renderAttachmentManifest(content.Attachments)
		return llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: buildUserMessageContent(content.Content, content.LLMContext, attachmentText)}, true
	case model.ReactMessageTypeAssistant, model.ReactMessageTypeToolResult:
		message := parseChatMessage(stored.ContentJSON)
		if strings.TrimSpace(message.Role) == "" {
			return llm.ChatMessage{}, false
		}
		return message, true
	case model.ReactMessageTypeCompactSummary:
		var content struct {
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal([]byte(stored.ContentJSON), &content); err != nil || strings.TrimSpace(content.Summary) == "" {
			return llm.ChatMessage{}, false
		}
		return llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: content.Summary}, true
	default:
		return llm.ChatMessage{}, false
	}
}

func parseStoredModelMessage(contentJSON string) (llm.ChatMessage, bool) {
	var wrapped struct {
		ModelMessage llm.ChatMessage `json:"modelMessage"`
	}
	if err := json.Unmarshal([]byte(contentJSON), &wrapped); err != nil {
		return llm.ChatMessage{}, false
	}
	if !isValidModelMessage(wrapped.ModelMessage) {
		return llm.ChatMessage{}, false
	}
	return wrapped.ModelMessage, true
}

func isValidModelMessage(message llm.ChatMessage) bool {
	if strings.TrimSpace(message.Role) == "" {
		return false
	}
	return strings.TrimSpace(message.Content) != "" || len(message.Parts) > 0
}

// persistAssistantMessage 按真实进入模型上下文的 assistant 消息落库；tool call 作为 assistant parts 的一部分保存。
func persistAssistantMessage(ctx *gin.Context, req *runtimeRequest, runID, sessionID string, actualModel reactModelTarget, content, reasoningContent, reasoningSignature string, toolCalls []llm.ToolCall, step int) (llm.ChatMessage, reactMessageRef, error) {
	return persistAssistantMessageWithType(ctx, req, runID, sessionID, actualModel, content, reasoningContent, reasoningSignature, toolCalls, step, model.ReactMessageTypeAssistant)
}

func persistAssistantMessageWithType(ctx *gin.Context, req *runtimeRequest, runID, sessionID string, actualModel reactModelTarget, content, reasoningContent, reasoningSignature string, toolCalls []llm.ToolCall, step int, messageType string) (llm.ChatMessage, reactMessageRef, error) {
	message := llm.ChatMessage{Role: model.ReactMessageRoleAssistant}
	if len(toolCalls) > 0 || reasoningContent != "" || reasoningSignature != "" {
		assistantMsg, _ := llm.BuildToolRoundMessagesWithReasoning(content, reasoningContent, reasoningSignature, toolCalls, nil)
		message = assistantMsg
	} else {
		message.Content = content
	}
	contentJSON, _ := json.Marshal(map[string]any{"modelMessage": message})
	seq, err := model.GetReactMessageMaxSeqByRunID(ctx, runID)
	if err != nil {
		return llm.ChatMessage{}, reactMessageRef{}, err
	}
	ref := reactMessageRef{RunID: runID, MessageID: generateMessageID(), Seq: seq + 1}
	if err := model.CreateReactMessage(ctx, &model.ReactMessage{MessageID: ref.MessageID, RunID: runID, SessionID: sessionID, UserName: req.userName, CallerKey: req.payload.CallerKey, Seq: ref.Seq, StepIndex: step, Role: model.ReactMessageRoleAssistant, MessageType: messageType, ModelKey: actualModel.ModelKey, ModelVersion: actualModel.ModelVersion, ContentJSON: string(contentJSON)}); err != nil {
		return llm.ChatMessage{}, reactMessageRef{}, err
	}
	return message, ref, nil
}

// persistToolResultMessage 按模型回填的 user/tool_result 消息落库，一轮多工具结果合并为一条上下文消息。
func persistToolResultMessage(ctx *gin.Context, req *runtimeRequest, runID, sessionID string, results []llm.ToolResultContent, step int) (reactMessageRef, error) {
	contentJSON, err := marshalStoredToolResultContent(results)
	if err != nil {
		return reactMessageRef{}, err
	}
	seq, err := model.GetReactMessageMaxSeqByRunID(ctx, runID)
	if err != nil {
		return reactMessageRef{}, err
	}
	ref := reactMessageRef{RunID: runID, MessageID: generateMessageID(), Seq: seq + 1}
	if err := model.CreateReactMessage(ctx, &model.ReactMessage{MessageID: ref.MessageID, RunID: runID, SessionID: sessionID, UserName: req.userName, CallerKey: req.payload.CallerKey, Seq: ref.Seq, StepIndex: step, Role: model.ReactMessageRoleUser, MessageType: model.ReactMessageTypeToolResult, ContentJSON: string(contentJSON)}); err != nil {
		return reactMessageRef{}, err
	}
	return ref, nil
}

// marshalStoredToolResultContent 将模型消息和 UI 回放状态分开保存，避免 meta 进入 LLM 上下文。
func marshalStoredToolResultContent(results []llm.ToolResultContent) ([]byte, error) {
	_, userMsg := llm.BuildToolRoundMessagesWithReasoning("", "", "", nil, results)
	storedContent := map[string]any{"modelMessage": userMsg}
	toolMeta := make(map[string]json.RawMessage)
	for _, result := range results {
		if len(result.Meta) > 0 {
			toolMeta[result.ToolUseID] = result.Meta
		}
	}
	if len(toolMeta) > 0 {
		storedContent["toolMeta"] = toolMeta
	}
	return json.Marshal(storedContent)
}

// requestCookies 提取当前 HTTP 请求 Cookie，透传给后端 HTTP 工具保持调用态一致。
func requestCookies(ctx *gin.Context) map[string]string {
	cookies := make(map[string]string)
	for _, cookie := range ctx.Request.Cookies() {
		cookies[cookie.Name] = cookie.Value
	}
	return cookies
}

// reactToolCallNames 提取本轮工具调用名列表，用于日志排查。
func reactToolCallNames(calls []llm.ToolCall) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, call.Name)
	}
	return names
}
