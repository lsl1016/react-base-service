package react

import (
	"context"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/metrics"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"
	memoryService "react-base-service/service/memory"
	"react-base-service/service/memory/extractor"
	"react-base-service/service/memory/resolver"

	"github.com/gin-gonic/gin"
)

// 记忆自动沉淀（extractor，V2 Phase 2）：压缩事件后异步执行结构化
// 「一次 LLM 抽取候选 → 确定性预过滤 → 一次 LLM 批量冲突消解 → 统一写核心落库」流水线。
// 与 reflection（agentic 子 run）在 compact_end 触发点互斥：extractor 开启时优先，
// 同一份压缩素材只消费一次；安全阀（冷却/防自触发/失败静默/写限）与 reflection 对齐。

// memoryExtractorCooldown 是 extractor 的冷却表（sessionID 维度），与 reflection 相互独立。
var memoryExtractorCooldown = &scopedCooldownTable{last: make(map[string]time.Time)}

// maybeTriggerMemoryMaintenance 是 compact_end 后的记忆维护入口：
// extractor.enabled 时走自动沉淀流水线，否则走 agentic reflection；二者互斥。
func maybeTriggerMemoryMaintenance(s *reactEngineState, summary string, compacted []llm.ChatMessage) {
	if conf.GetReactRuntimeConfig().Memory.Extractor.ExtractorEnabled() {
		maybeTriggerMemoryExtraction(s, summary, compacted)
		return
	}
	maybeTriggerMemoryReflection(s, summary, compacted)
}

// maybeTriggerMemoryExtraction 判定冷却与防自触发后，异步派生一次沉淀流水线；绝不阻塞主 run。
func maybeTriggerMemoryExtraction(s *reactEngineState, summary string, compacted []llm.ChatMessage) {
	defer func() {
		// 触发链路自身绝不能影响主 run：panic 只记日志。
		if r := recover(); r != nil {
			zlog.Errorf(s.ctx, "[react.maybeTriggerMemoryExtraction] 触发 panic: runId=%s err=%v", s.runID, r)
		}
	}()

	cfg := conf.GetReactRuntimeConfig().Memory
	if !cfg.MemoryEnabled() || !cfg.Extractor.ExtractorEnabled() {
		return
	}
	// 防自触发：reflection/extractor 会话自身的压缩不再派生维护任务。
	if s.req.payload.Type == model.ReactSessionTypeReflection {
		return
	}
	if strings.TrimSpace(summary) == "" {
		return
	}
	if !memoryExtractorCooldown.acquire(s.sessionID, time.Duration(cfg.Extractor.CooldownMinutes)*time.Minute) {
		metrics.MemoryExtractorTotal.WithLabelValues("cooldown_skipped").Inc()
		return
	}
	metrics.MemoryExtractorTotal.WithLabelValues("triggered").Inc()

	// 捕获触发 run 的模型路由与身份：goroutine 内自足执行，不持有 engine state。
	spec := extractionRunSpec{
		apiKey:       s.req.apiKey,
		modelKey:     s.currentModel.ModelKey,
		modelVersion: s.currentModel.ModelVersion,
		callerKey:    s.req.payload.CallerKey,
		userName:     s.req.userName,
		runID:        s.runID,
	}
	go func() {
		// 父 context 用 Background：沉淀流水线生命周期独立于触发它的 WS 连接。
		runMemoryExtraction(spec, summary, compacted)
	}()
}

// extractionRunSpec 是一次沉淀执行所需的身份与模型路由快照。
type extractionRunSpec struct {
	apiKey       string
	modelKey     string
	modelVersion string
	callerKey    string
	userName     string
	runID        string
}

// runMemoryExtraction 执行一次沉淀流水线；失败静默重试一次，终态统一打点。
func runMemoryExtraction(spec extractionRunSpec, summary string, compacted []llm.ChatMessage) {
	status := "success"
	defer func() {
		metrics.MemoryExtractorTotal.WithLabelValues(status).Inc()
	}()
	ginCtx := newHeadlessGinContext(spec.userName)
	for attempt := 0; attempt < 2; attempt++ {
		outcome, err := runMemoryExtractionOnce(ginCtx, spec, summary, compacted)
		if err == nil {
			if outcome.candidates == 0 {
				status = "empty"
			}
			zlog.Infof(ginCtx, "[react.runMemoryExtraction] 沉淀完成: caller=%s run=%s 候选=%d 落库=%d",
				spec.callerKey, spec.runID, outcome.candidates, outcome.applied)
			return
		}
		if IsReactRunCancelled(err) {
			return
		}
		status = "error"
		zlog.Warnf(ginCtx, "[react.runMemoryExtraction] 沉淀失败(第%d次): caller=%s run=%s err=%v",
			attempt+1, spec.callerKey, spec.runID, err)
	}
}

// extractionOutcome 汇总一次执行的产出。
type extractionOutcome struct {
	candidates int
	applied    int
}

// runMemoryExtractionOnce 执行完整流水线：查清单 → 抽取 → 预过滤 → 消解落库。
func runMemoryExtractionOnce(ctx *gin.Context, spec extractionRunSpec, summary string, compacted []llm.ChatMessage) (extractionOutcome, error) {
	cfg := conf.GetReactRuntimeConfig().Memory

	client, err := llm.GetClientWithUserModel(spec.apiKey, spec.modelKey)
	if err != nil {
		return extractionOutcome{}, err
	}
	invoker := extractorLLMInvoker{client: client, model: spec.modelVersion}

	runtime := memoryService.DefaultRuntime()
	scope := runtime.ResolveScope(spec.callerKey, spec.userName, cfg.MemoryAllowUserScope())
	items, err := runtime.ActiveItems(ctx, scope)
	if err != nil {
		return extractionOutcome{}, err
	}

	existingKeys := make(map[string]struct{}, len(items))
	for _, item := range items {
		existingKeys[memoryService.ItemKey(item.Content)] = struct{}{}
	}

	candidates, err := extractor.Extract(ctx, invoker, extractor.ExtractInput{
		Summary:     summary,
		Transcript:  renderMemoryReflectionTranscript(compacted, cfg.Extractor.TranscriptCharLimit),
		Manifest:    extractor.BuildManifest(items),
		CurrentDate: time.Now().Format("2006-01-02"),
		MaxCandidates: cfg.Extractor.MaxCandidatesPerRun,
	})
	if err != nil {
		return extractionOutcome{}, err
	}
	candidates = extractor.PrefilterCandidates(ctx, candidates, existingKeys, cfg.Extractor.MinConfidence, cfg.Extractor.MaxCandidatesPerRun)
	if len(candidates) == 0 {
		return extractionOutcome{}, nil
	}

	decisions, err := resolver.Resolve(ctx, invoker, nil, resolver.ResolveInput{
		Owners:      scope.Owners,
		WriteOwner:  scope.WriteOwner,
		AllowedKeys: scope.AllowedKeys,
		Items:       items,
		SimilarTopK: cfg.Extractor.SimilarTopK,
		MaxWrites:   cfg.Extractor.MaxWritesPerRun,
		CreatedBy:   spec.runID,
	}, candidates)
	if err != nil {
		return extractionOutcome{candidates: len(candidates)}, err
	}
	applied := 0
	for _, decision := range decisions {
		if decision.Applied {
			applied++
			zlog.Infof(ctx, "[react.runMemoryExtraction] 决策落库: action=%s target=%d title=%q",
				decision.Action, decision.TargetID, decision.Candidate.Title)
		} else if decision.Action == resolver.ActionSkip && decision.Note != "" {
			zlog.Infof(ctx, "[react.runMemoryExtraction] 决策跳过: title=%q note=%s", decision.Candidate.Title, decision.Note)
		}
	}
	return extractionOutcome{candidates: len(candidates), applied: applied}, nil
}

// extractorLLMInvoker 把 api/llm 流式客户端适配为 extractor.LLMInvoker（非工具纯文本调用）。
type extractorLLMInvoker struct {
	client llm.LLMClient
	model  string
}

// ChatOnce 以 system+user 发起一次非工具流式调用并收集全文；超时由 extractor.ChatTimeout 约束。
func (i extractorLLMInvoker) ChatOnce(ctx context.Context, system, user string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, extractor.ChatTimeout)
	defer cancel()
	stream, err := i.client.ChatStream(callCtx, []llm.LLMMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, i.model)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for chunk := range stream {
		if chunk.Error != nil {
			return "", chunk.Error
		}
		sb.WriteString(chunk.Content)
	}
	return sb.String(), nil
}
