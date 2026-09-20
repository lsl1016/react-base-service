package react

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/metrics"
	"react-base-service/components/params"
	"react-base-service/conf"

	"react-base-service/golib/zlog"
)

// reactModelTarget 是一次模型调用所需的完整模型标识。API Key 仍按当前 caller 路由统一解析。
type reactModelTarget struct {
	ModelKey     string
	ModelVersion string
}

// modelRoundResult 把本轮流式结果与实际使用的模型绑定。
// 当发生互备切换时，持久化消息和指标必须记录真正成功的模型，而不是用户最初选择的模型。
type modelRoundResult struct {
	Stream collectLLMStreamResult
	Model  reactModelTarget
}

type retryableModelError struct {
	reason string
	err    error
}

func (e *retryableModelError) Error() string { return e.err.Error() }
func (e *retryableModelError) Unwrap() error { return e.err }

func newRetryableModelError(reason string, err error) error {
	return &retryableModelError{reason: reason, err: err}
}

func retryableModelErrorInfo(err error) (*retryableModelError, bool) {
	var target *retryableModelError
	if !errors.As(err, &target) {
		return nil, false
	}
	return target, true
}

func messagesForReactModel(messages []llm.ChatMessage, target reactModelTarget) []llm.ChatMessage {
	if llm.NormalizeModelKey(target.ModelKey) == "claude" {
		return messages
	}
	result := make([]llm.ChatMessage, 0, len(messages))
	for _, message := range messages {
		if len(message.Parts) == 0 {
			result = append(result, message)
			continue
		}
		cloned := message
		cloned.Parts = make([]llm.ContentPart, 0, len(message.Parts))
		for _, part := range message.Parts {
			if part.Type == "thinking" {
				part.Signature = ""
			}
			cloned.Parts = append(cloned.Parts, part)
		}
		result = append(result, cloned)
	}
	return result
}

func sameReactModel(left, right reactModelTarget) bool {
	return left.ModelKey == right.ModelKey && left.ModelVersion == right.ModelVersion
}

// configuredReactModelRouting 计算当前 Run 的模型尝试集合。
// 只有用户选择的模型本身位于 models.available 中时才参加 mutual failover；未知/临时模型不自动
// 混入互备组，避免配置外模型意外切换到其它供应商。
func configuredReactModelRouting(req *runtimeRequest) (reactModelTarget, []reactModelTarget) {
	selected := reactModelTarget{ModelKey: req.resolvedModelKey, ModelVersion: req.resolvedModelVersion}
	modelsCfg := conf.GetReactRuntimeConfig().Models
	models := []reactModelTarget{selected}
	if modelsCfg.MutualFailover == nil || !*modelsCfg.MutualFailover {
		return selected, models
	}

	selectedConfigured := false
	for _, item := range modelsCfg.Available {
		if sameReactModel(selected, reactModelTarget{ModelKey: item.ModelKey, ModelVersion: item.ModelVersion}) {
			selectedConfigured = true
			break
		}
	}
	if !selectedConfigured {
		return selected, models
	}

	for _, item := range modelsCfg.Available {
		candidate := reactModelTarget{ModelKey: item.ModelKey, ModelVersion: item.ModelVersion}
		if sameReactModel(candidate, selected) {
			continue
		}
		models = append(models, candidate)
	}
	return selected, models
}

func (s *reactEngineState) modelAttemptOrder() []reactModelTarget {
	order := make([]reactModelTarget, 0, len(s.failoverModels)+1)
	order = append(order, s.currentModel)
	for _, candidate := range s.failoverModels {
		if sameReactModel(candidate, s.currentModel) {
			continue
		}
		order = append(order, candidate)
	}
	return order
}

func (s *reactEngineState) emitModelFallback(step int, from, to reactModelTarget, reason string, reset bool) error {
	metrics.ModelFailoversTotal.Inc()
	emitter := s.emitter
	if emitter == nil {
		return nil
	}
	return emitter.EmitStep(step, EventModelFallback, params.ReactModelFallbackPayload{
		FromModelKey:       from.ModelKey,
		FromModelVersion:   from.ModelVersion,
		ToModelKey:         to.ModelKey,
		ToModelVersion:     to.ModelVersion,
		Reason:             reason,
		ResetCurrentOutput: reset,
	})
}

func (s *reactEngineState) emitCollectedModelResult(step int, result collectLLMStreamResult) error {
	if result.ReasoningContent != "" {
		if err := s.emitter.EmitStep(step, EventThoughtStart, params.ReactThoughtStartPayload{}); err != nil {
			return err
		}
		if err := s.emitter.EmitStep(step, EventThoughtDelta, params.ReactThoughtDeltaPayload{ContentDelta: result.ReasoningContent}); err != nil {
			return err
		}
		if err := s.emitter.EmitStep(step, EventThoughtEnd, params.ReactThoughtEndPayload{Content: result.ReasoningContent, InputTokens: s.inputTokens + result.InputTokens, OutputTokens: s.outputTokens + result.OutputTokens, CacheReadTokens: s.cacheReadTokens, CacheCreateTokens: s.cacheCreateTokens, ContextUsedTokens: result.InputTokens + result.OutputTokens, MaxContextTokens: conf.GetReactRuntimeConfig().ContextCompact.TokenTrigger}); err != nil {
			return err
		}
	}
	if result.Content != "" {
		if err := s.emitter.EmitStep(step, EventContentStart, params.ReactContentStartPayload{}); err != nil {
			return err
		}
		if err := s.emitter.EmitStep(step, EventContentDelta, params.ReactContentDeltaPayload{ContentDelta: result.Content}); err != nil {
			return err
		}
		if err := s.emitter.EmitStep(step, EventContentEnd, params.ReactContentEndPayload{Content: result.Content, InputTokens: s.inputTokens + result.InputTokens, OutputTokens: s.outputTokens + result.OutputTokens, CacheReadTokens: s.cacheReadTokens, CacheCreateTokens: s.cacheCreateTokens, ContextUsedTokens: result.InputTokens + result.OutputTokens, MaxContextTokens: conf.GetReactRuntimeConfig().ContextCompact.TokenTrigger}); err != nil {
			return err
		}
	}
	return nil
}

func (s *reactEngineState) terminalModelContextError(err error) error {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	cause := context.Cause(s.runCtx)
	if errors.Is(cause, ErrReactRunCancelled) {
		return ErrReactRunCancelled
	}
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrReactClientDisconnected
}

// callModelRound 在同一个逻辑 ReAct Step 内完成模型互备。
//
// 模型切换不会增加 stepIndex，也不会执行任何 Tool；只有某个模型完整返回本轮结果后，ReAct 才继续。
// 成功的备选模型会提升为后续轮次的 currentModel，避免每一轮都重复撞击已故障的首选模型。
//
// 任一模型成功后会成为当前 Run 后续轮次的首选模型；同一轮每个模型最多尝试一次。
func (s *reactEngineState) callModelRound(step int, prefixDebugKey string, tools []llm.ToolDefinition) (modelRoundResult, error) {
	return s.callModelRoundWithEmitter(step, prefixDebugKey, tools, true)
}

// callModelRoundWithEmitter 是 callModelRound 的底层版本。
// emitEvents=false 时仍完整收集模型输出和 usage，只是不立即向前端发 thought/content 事件；
// 该能力给 Plan Finalizer 等“先校验结果、再决定是否公开”的上层 Runtime 复用。
func (s *reactEngineState) callModelRoundWithEmitter(step int, prefixDebugKey string, tools []llm.ToolDefinition, emitEvents bool) (modelRoundResult, error) {
	attempts := s.modelAttemptOrder()
	// 各备选模型共享同一份上下文，构成估算只需算一次；随本轮 usage 落库。
	s.lastContextBreakdownJSON = s.computeRoundContextBreakdown(tools)
	var lastResult modelRoundResult
	var lastErr error

	for index, target := range attempts {
		client := s.client
		if client == nil || !sameReactModel(target, s.currentModel) {
			var err error
			client, err = llm.GetClientWithUserModel(s.req.apiKey, target.ModelKey)
			if err != nil {
				lastResult = modelRoundResult{Model: target}
				lastErr = components.ErrorLLMRequest.Sprintf(err.Error())
				if index+1 >= len(attempts) {
					return lastResult, lastErr
				}
				next := attempts[index+1]
				if emitErr := s.emitModelFallback(step, target, next, "request_error", false); emitErr != nil {
					return lastResult, emitErr
				}
				continue
			}
		}

		llmCtx := llm.WithReasoning(s.runCtx, llm.ReasoningOptions{Effort: "high"})
		llmCtx = llm.WithPrefixDebugRun(llmCtx, prefixDebugKey, step, s.runID)
		llmCtx, cancel := context.WithCancel(llmCtx)
		stream, err := client.ChatStreamWithTools(llmCtx, messagesForReactModel(s.contextMessages(), target), target.ModelVersion, tools)
		if err != nil {
			cancel()
			if terminal := s.terminalModelContextError(err); terminal != nil {
				return modelRoundResult{Model: target}, terminal
			}
			lastResult = modelRoundResult{Model: target}
			lastErr = components.ErrorLLMRequest.Sprintf(err.Error())
		} else {
			idleTimeout := time.Duration(conf.GetReactRuntimeConfig().StreamIdleTimeoutSec) * time.Second
			streamResult, collectErr := s.collectLLMStreamWithEmitter(stream, step, cancel, idleTimeout, emitEvents && index == 0)
			cancel()
			lastResult = modelRoundResult{Stream: streamResult, Model: target}
			if collectErr == nil {
				if emitEvents && index > 0 {
					if err := s.emitCollectedModelResult(step, streamResult); err != nil {
						return lastResult, err
					}
				}
				s.client = client
				s.currentModel = target
				return lastResult, nil
			}
			if IsReactRunCancelled(collectErr) || IsReactClientDisconnected(collectErr) || errors.Is(collectErr, context.DeadlineExceeded) {
				return lastResult, collectErr
			}
			if _, retryable := retryableModelErrorInfo(collectErr); !retryable {
				return lastResult, collectErr
			}
			lastErr = collectErr
		}

		if index+1 >= len(attempts) {
			if retryable, ok := retryableModelErrorInfo(lastErr); ok {
				return lastResult, retryable.err
			}
			return lastResult, lastErr
		}

		next := attempts[index+1]
		reason := "request_error"
		if retryable, ok := retryableModelErrorInfo(lastErr); ok && strings.TrimSpace(retryable.reason) != "" {
			reason = retryable.reason
		}
		reset := strings.TrimSpace(lastResult.Stream.Content) != "" || strings.TrimSpace(lastResult.Stream.ReasoningContent) != "" || len(lastResult.Stream.ToolCalls) > 0
		zlog.Warnf(s.ctx, "[react.callModelRound] 模型调用失败，切换互备模型: runId=%s, step=%d, from=%s/%s, to=%s/%s, reason=%s, err=%v", s.runID, step, target.ModelKey, target.ModelVersion, next.ModelKey, next.ModelVersion, reason, lastErr)
		if err := s.emitModelFallback(step, target, next, reason, reset); err != nil {
			return lastResult, fmt.Errorf("emit model fallback: %w", err)
		}
	}

	return lastResult, lastErr
}
