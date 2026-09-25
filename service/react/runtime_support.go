package react

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	toolService "react-base-service/service/tool"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

const (
	toolExecutionStatusRunning   = "running"
	toolExecutionStatusWaiting   = "waiting"
	toolExecutionStatusSuccess   = "success"
	toolExecutionStatusError     = "error"
	toolExecutionStatusCancelled = "cancelled"
)

// NormalizedToolResult 是写入模型上下文和事件流的工具结果，避免把大结果直接塞回上下文。
type NormalizedToolResult struct {
	ToolUseID    string `json:"toolUseId"`
	Content      string `json:"content"`
	ResultRef    string `json:"resultRef,omitempty"`
	Truncated    bool   `json:"truncated"`
	OmittedChars int    `json:"omittedChars,omitempty"`
	IsError      bool   `json:"isError"`
	ExecutedBy   string `json:"executedBy"`
	Status       string `json:"status"`
}

// LLMContent 将标准化工具结果序列化为模型可读内容，保持事件流和上下文里的结构一致。
func (r NormalizedToolResult) LLMContent() string {
	payload, _ := json.Marshal(r)
	return string(payload)
}

// normalizeToolResult 将原始工具输出转成模型可消费的结果；所有结果都生成 resultRef 并落库，
// 大结果额外做 preview 截断以控制上下文体积。
func normalizeToolResult(toolUseID, content string, isError bool, executedBy string) NormalizedToolResult {
	toolResultCfg := conf.GetReactRuntimeConfig().ToolResult
	result := NormalizedToolResult{
		ToolUseID:  toolUseID,
		Content:    content,
		ResultRef:  buildResultRef(toolUseID, content),
		IsError:    isError,
		ExecutedBy: executedBy,
		Status:     toolExecutionStatusSuccess,
	}
	if isError {
		result.Status = toolExecutionStatusError
	}
	// 是否截断仍以字节阈值判定（控制上下文/存储体积）；一旦截断，预览长度与省略量统一按 rune 计量，
	// 与 read_tool_result 的 offset/limit/nextOffset 坐标系保持一致。
	if len([]byte(content)) > toolResultCfg.InlineLimitBytes {
		preview := headRunes(content, toolResultCfg.PreviewLimit)
		result.Content = preview
		result.Truncated = true
		result.OmittedChars = utf8.RuneCountInString(content) - utf8.RuneCountInString(preview)
	}
	return result
}

// classifyToolInterruption 区分用户主动取消和客户端断连；只有前者属于 cancelled，断连仍按 error 处理。
// 文案统一为"执行中被打断、结果未提交、副作用状态未知"（ZCode unknown_execution_state 语义）：
// 调用已经 started，不能宣称"未执行"，必须引导模型先核实状态再决定是否重试。
func classifyToolInterruption(runCtx context.Context, err error) (status, content string, ok bool) {
	var cause error
	if runCtx != nil {
		cause = context.Cause(runCtx)
	}
	switch {
	case errors.Is(err, ErrReactClientDisconnected) || errors.Is(cause, ErrReactClientDisconnected):
		return toolExecutionStatusError, "连接断开，工具执行已中断，结果未提交，副作用状态未知。请先核实当前状态再决定是否重试，不要盲目重试同一操作。", true
	case errors.Is(err, ErrReactRunCancelled) || errors.Is(cause, ErrReactRunCancelled):
		return toolExecutionStatusCancelled, "用户取消了本次运行，工具执行被中断，结果未提交，副作用状态未知。请先核实当前状态再决定是否重试，不要盲目重试同一操作。", true
	case errors.Is(err, context.Canceled):
		return toolExecutionStatusError, "工具执行上下文已中断，结果未提交，副作用状态未知。请先核实当前状态再决定是否重试，不要盲目重试同一操作。", true
	default:
		return "", "", false
	}
}

// closeInterruptedToolUse 为已经发出 start 事件的工具合成结构化终态并登记中断结果，
// 由引擎层在本轮收口时统一落库（闭合治理 Phase 1：不在此处单独落库，避免与轮次整体
// 落库产生重复 tool_result），随后由调用方继续向上传播中断错误。
func (s *reactEngineState) closeInterruptedToolUse(call llm.ToolCall, step int, executedBy string, startedAt time.Time, err error) llm.ToolResultContent {
	status, content, ok := classifyToolInterruption(s.runCtx, err)
	if !ok {
		return llm.ToolResultContent{}
	}
	normalized := normalizeToolResult(call.ID, content, true, executedBy)
	normalized.Status = status
	result := llm.ToolResultContent{ToolUseID: call.ID, Content: normalized.LLMContent(), IsError: true}
	if normalized.ResultRef != "" {
		if storeErr := storeResultRef(s.ctx, s.sessionID, s.runID, call.ID, normalized.ResultRef, content); storeErr != nil {
			zlog.Warnf(s.ctx, "[React] 中断工具结果引用落库失败(忽略): runId=%s, toolUseId=%s, err=%v", s.runID, call.ID, storeErr)
		}
	}
	s.recordInterruptedToolResult(result)
	if s.emitter != nil && s.emitter.write != nil {
		_ = s.emitter.EmitStep(step, EventToolUseEnd, params.ReactToolUseEndPayload{
			ToolUseID: call.ID, Content: normalized.Content, ResultRef: normalized.ResultRef,
			Truncated: normalized.Truncated, OmittedChars: normalized.OmittedChars,
			IsError: true, ExecutedBy: executedBy, Status: status, DurationMs: time.Since(startedAt).Milliseconds(),
		})
	}
	return result
}

// buildResultRef 基于工具调用和内容生成短引用 ID，用于后续 read_tool_result 分片读取。
func buildResultRef(toolUseID, content string) string {
	sum := sha256.Sum256([]byte(toolUseID + "\x00" + content + "\x00" + time.Now().Format(time.RFC3339Nano)))
	return "result_ref_" + hex.EncodeToString(sum[:])[:24]
}

// storeResultRef 将完整工具结果落库，单条超过 16MB 时直接失败，避免写入不可控大对象。
func storeResultRef(ctx *gin.Context, sessionID, runID, toolUseID, resultRef, content string) error {
	resultRef = strings.TrimSpace(resultRef)
	if resultRef == "" {
		return nil
	}
	toolResultCfg := conf.GetReactRuntimeConfig().ToolResult
	expireAfter := time.Duration(toolResultCfg.DBTTLSec) * time.Second
	sizeBytes := len([]byte(content))
	if sizeBytes > toolResultCfg.DBMaxBytes {
		return components.ErrorReactToolResultTooLarge.Sprintf(fmt.Sprintf("resultRef=%s, sizeBytes=%d, maxBytes=%d", resultRef, sizeBytes, toolResultCfg.DBMaxBytes))
	}
	expireAt := time.Now().Add(expireAfter)
	return model.CreateReactToolResult(ctx, &model.ReactToolResult{
		ResultRef: resultRef, SessionID: sessionID, RunID: runID, ToolUseID: toolUseID,
		Content: content, SizeBytes: sizeBytes,
		ExpireAt: &expireAt,
	})
}

// readResultRef 只允许当前 session/run 读取自己的大结果，过期或不存在统一按未命中处理。
func readResultRef(ctx *gin.Context, sessionID, runID, resultRef string) (string, bool, error) {
	resultRef = strings.TrimSpace(resultRef)
	if resultRef == "" {
		return "", false, nil
	}
	stored, err := model.GetReactToolResultByResultRef(ctx, resultRef)
	if err != nil {
		return "", false, err
	}
	if stored == nil {
		return "", false, nil
	}
	if stored.SessionID != sessionID {
		return "", false, fmt.Errorf("resultRef forbidden")
	}
	if stored.ExpireAt != nil && time.Now().After(*stored.ExpireAt) {
		return "", false, nil
	}
	return stored.Content, true, nil
}

// sliceResultContent 按 rune 维度切分 resultRef 内容，避免中文等多字节字符被截断。
func sliceResultContent(content string, offset, limit int) (string, bool, int) {
	toolResultCfg := conf.GetReactRuntimeConfig().ToolResult
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = toolResultCfg.ReadLimit
	}
	if limit > toolResultCfg.MaxReadLimit {
		limit = toolResultCfg.MaxReadLimit
	}
	runes := []rune(content)
	if offset >= len(runes) {
		return "", false, len(runes)
	}
	end := offset + limit
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[offset:end]), end < len(runes), end
}

// truncateRunes 去掉首尾空白后按 rune 截断，保证摘要字段不会破坏 UTF-8 内容。
func truncateRunes(content string, limit int) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

// headRunes 直接取原始内容的前 limit 个 rune，不做 TrimSpace。
// 与 truncateRunes 的区别：truncateRunes 面向摘要展示（去空白、可读性优先），
// headRunes 面向可续读预览——保持与原始内容同一套 rune 坐标，使 read_tool_result
// 能从 len([]rune(preview)) 处无缝续读，避免首尾空白导致的偏移错位。
func headRunes(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runeCount := 0
	for byteIndex := range content {
		if runeCount == limit {
			return content[:byteIndex]
		}
		runeCount++
	}
	return content
}

// executedBy 返回工具实际执行端，用于 thought_end 和 tool_use 事件标识执行边界。
func (s *reactEngineState) executedBy(toolName string) string {
	if isInternalMetaTool(toolName) {
		return executedByInternal
	}
	tool, ok := s.activeTools[toolName]
	if !ok {
		return "unknown"
	}
	switch toolService.NormalizeToolType(tool.ToolType) {
	case toolService.ToolTypeClient:
		return executedByClient
	case toolService.ToolTypeHTTP:
		return executedByServer
	default:
		return "unknown"
	}
}

// reactCompactThreshold 按模型窗口推导压缩触发阈值（终止边界治理 Phase 3）：
// 有效窗口 = 模型目录 max_context_tokens - 输出预留（取配置预留与目录 max_output_tokens 的较大者）- buffer。
// 模型目录未配置窗口时返回 0，调用方回退 TokenTrigger 旧语义。
func reactCompactThreshold(modelKey string) int {
	cfg := conf.GetReactRuntimeConfig().ContextCompact
	catalog := conf.GetModelCatalog(modelKey)
	if catalog == nil || catalog.MaxContextTokens <= 0 {
		return 0
	}
	reserve := cfg.OutputReserveTokens
	if catalog.MaxOutputTokens > reserve {
		reserve = catalog.MaxOutputTokens
	}
	window := catalog.MaxContextTokens - reserve - cfg.BufferTokens
	if window <= 0 {
		return 0
	}
	return window
}

// compactConfigForModel 返回按模型窗口推导触发阈值后的压缩配置。
// 阈值推导只覆盖 TokenTrigger（触发水位/展示口径），TokenTarget 等仍走全局配置。
func compactConfigForModel(modelKey string) conf.ReactContextCompactConfig {
	cfg := conf.GetReactRuntimeConfig().ContextCompact
	if threshold := reactCompactThreshold(modelKey); threshold > 0 {
		cfg.TokenTrigger = threshold
	}
	return cfg
}

// maxContextTokens 返回当前模型的有效上下文窗口（事件展示口径）：
// 按模型目录推导；未配置目录时回退 token_trigger 旧语义。
func (s *reactEngineState) maxContextTokens() int {
	return reactMaxContextTokens(s.currentModel.ModelKey)
}

// reactMaxContextTokens 是 maxContextTokens 的独立函数版，供回放（history.go）与
// run 收敛路径（runtime.go，无 engine state）复用。
func reactMaxContextTokens(modelKey string) int {
	if threshold := reactCompactThreshold(modelKey); threshold > 0 {
		return threshold
	}
	return conf.GetReactRuntimeConfig().ContextCompact.TokenTrigger
}

// maybeCompactContext 在上下文过长时压缩早期消息，只保留摘要和最近若干轮对话。
//
// 终止边界治理（Phase 3）新增三道防线：
//   - LLM 压缩连续失败熔断：连续失败达到阈值后本轮 run 直接走本地摘要，不再反复烧模型调用；
//   - rapid-refill 防抖：最近 ≤3 轮内已压缩 ≥2 次仍超阈值，说明工作集超过压缩后预算，
//     继续压缩只会形成"压缩→立即满→再压缩"的抖动循环，此时转入软着陆收尾；
//   - 压缩器输入分片：超长历史分段总结后归并，不再静默截断（见 buildLLMCompactSummary）。
func maybeCompactContext(s *reactEngineState, step int) error {
	compactCfg := compactConfigForModel(s.currentModel.ModelKey)
	beforeCount := len(s.messages)
	beforeSize := estimateMessagesSize(s.messages)

	systemPrefixCount := currentSystemPrefixCount(s.messages)
	compactableMessages := s.messages[systemPrefixCount:]
	compactableRefs := s.messageRefs[systemPrefixCount:]

	// overhead 是固定保留且不可压缩的输入开销：当前 system prompt + 当前执行域 tools schema。
	overheadTokens := estimateToolDefinitionsTokens(s.buildToolDefinitions())
	if systemPrefixCount > 0 {
		overheadTokens += estimateMessagesTokens(s.messages[:systemPrefixCount])
	}

	// 冷启动（lastInputTokens=0）才需要估算 token；有真实值时跳过估算节省 CPU。
	estimatedTokens := 0
	if s.lastInputTokens == 0 {
		estimatedTokens = overheadTokens + estimateMessagesTokens(compactableMessages)
	}
	if !shouldCompactContext(s.lastInputTokens, estimatedTokens, compactCfg) {
		return nil
	}

	// rapid-refill 防抖（仅确认超阈值后判定）：近 3 轮内已压缩 2 次仍超阈值，
	// 说明工作集本身超过压缩后预算，继续压缩只会抖动循环 → 转入软着陆收尾。
	if rapidRefillExceeded(s, step) {
		s.enterSoftLanding(softLandingReasonContextLimit, 0, 0)
		return nil
	}

	keepCount := selectCompactKeepCount(compactableMessages, overheadTokens, compactCfg)
	if keepCount <= 0 || keepCount >= len(compactableMessages) {
		return nil
	}

	keepStart := len(compactableMessages) - keepCount
	keepStart = alignKeepStartToTurnBoundary(compactableMessages, keepStart)
	if keepStart <= 0 {
		zlog.Warnf(s.ctx, "[react.maybeCompactContext] 跳过压缩：找不到 tool 轮次安全边界 runId=%s beforeCount=%d", s.runID, beforeCount)
		return nil
	}

	if err := s.emitter.EmitStep(step, EventCompactStart, params.ReactCompactStartPayload{MessageCount: beforeCount, EstimatedSize: beforeSize}); err != nil {
		return err
	}

	prefixPart := append([]llm.ChatMessage(nil), s.messages[:systemPrefixCount]...)
	prefixRefs := append([][]reactMessageRef(nil), s.messageRefs[:systemPrefixCount]...)
	compactPart := compactableMessages[:keepStart]
	compactRefs := flattenMessageRefs(compactableRefs[:keepStart])
	keptPart := append([]llm.ChatMessage(nil), compactableMessages[keepStart:]...)
	keptRefs := append([][]reactMessageRef(nil), compactableRefs[keepStart:]...)
	summary, inputTokens, outputTokens, err := buildLLMCompactSummary(s, compactPart)
	if tokenErr := s.addTokenUsage(inputTokens, outputTokens); tokenErr != nil {
		return tokenErr
	}
	if err != nil {
		// LLM 压缩失败熔断计数；成功路径在下方重置。
		s.compactLLMFailures++
		zlog.Warnf(s.ctx, "[react.maybeCompactContext] LLM上下文压缩失败(熔断计数=%d/%d)，回退本地压缩: runId=%s, err=%v", s.compactLLMFailures, compactCfg.CompactFailureBreaker, s.runID, err)
	} else {
		s.compactLLMFailures = 0
	}
	if err != nil || strings.TrimSpace(summary) == "" {
		summary = buildCompactSummary(compactPart)
	}
	seq, err := model.GetReactMessageMaxSeqByRunID(s.ctx, s.runID)
	if err != nil {
		return err
	}
	compactRef := reactMessageRef{RunID: s.runID, MessageID: generateMessageID(), Seq: seq + 1}
	contentJSON, _ := json.Marshal(compactSummaryContent{Summary: summary, CoveredThrough: compactCoveredThrough(compactRefs)})
	if err := model.CreateReactMessage(s.ctx, &model.ReactMessage{MessageID: compactRef.MessageID, RunID: s.runID, SessionID: s.sessionID, UserName: s.req.userName, CallerKey: s.req.payload.CallerKey, Seq: compactRef.Seq, StepIndex: step, Role: model.ReactMessageRoleUser, MessageType: model.ReactMessageTypeCompactSummary, ContentJSON: string(contentJSON)}); err != nil {
		return err
	}
	s.messages = append(prefixPart, llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: compactSummaryMessageContent(summary)})
	s.messages = append(s.messages, keptPart...)
	s.messageRefs = append(prefixRefs, []reactMessageRef{compactRef})
	s.messageRefs = append(s.messageRefs, keptRefs...)
	// 记录压缩发生的 step，供 rapid-refill 防抖判定。
	s.compactStepHistory = append(s.compactStepHistory, step)
	if err := s.emitter.EmitStep(step, EventCompactEnd, params.ReactCompactEndPayload{BeforeMessageCount: beforeCount, AfterMessageCount: len(s.messages), Summary: summary}); err != nil {
		return err
	}
	// compact_end 成功后判定是否派生记忆维护（extractor 优先、与 reflection 互斥）：异步、受限、绝不阻塞主 run；
	// compactPart 是被压缩掉的原始消息，此刻仍在内存中，直接作为整理素材传入。
	maybeTriggerMemoryMaintenance(s, summary, compactPart)
	return nil
}

// rapidRefillExceeded 判定是否处于"压缩→立即再满"的抖动循环：
// 最近 compactRapidRefillWindow 个 step 内已发生 compactRapidRefillCount 次压缩。
func rapidRefillExceeded(s *reactEngineState, step int) bool {
	const window = 3
	const count = 2
	recent := 0
	for _, compactStep := range s.compactStepHistory {
		if step-compactStep < window {
			recent++
		}
	}
	if recent < count {
		return false
	}
	s.logWarnf("[react.maybeCompactContext] rapid-refill 防抖触发：近 %d 轮内已压缩 %d 次仍超阈值，转入软着陆 runId=%s, step=%d", window, recent, s.runID, step)
	return true
}

func currentSystemPrefixCount(messages []llm.ChatMessage) int {
	if len(messages) == 0 {
		return 0
	}
	if messages[0].Role == model.ReactMessageRoleSystem && strings.TrimSpace(messages[0].Content) != "" {
		return 1
	}
	return 0
}

// hasPartOfType 判断消息 Parts 中是否包含指定类型，用于 tool_use/tool_result 边界检测。
func hasPartOfType(msg llm.ChatMessage, partType string) bool {
	for _, p := range msg.Parts {
		if p.Type == partType {
			return true
		}
	}
	return false
}

// alignKeepStartToTurnBoundary 把 keepStart 往前挪到完整轮次边界，避免：
//  1. keptPart 以孤儿 tool_result 开头（API 找不到匹配 tool_use 会报错）
//  2. compactPart 以 dangling assistant tool_use 结尾（pair 被切开）
//
// 返回 0 表示无法找到安全边界，调用方应放弃本次压缩。
func alignKeepStartToTurnBoundary(messages []llm.ChatMessage, keepStart int) int {
	for keepStart > 0 && keepStart < len(messages) {
		first := messages[keepStart]
		prev := messages[keepStart-1]
		if hasPartOfType(first, "tool_result") || hasPartOfType(prev, "tool_use") {
			keepStart--
			continue
		}
		break
	}
	return keepStart
}

// shouldCompactContext 判断是否触发压缩，按以下优先级：
//  1. 禁用开关
//  2. lastInputTokens（上一轮真实 input_tokens）超 TokenTrigger
//  3. 冷启动估算 token 超 TokenTrigger
func shouldCompactContext(lastInputTokens, estimatedTokens int, cfg conf.ReactContextCompactConfig) bool {
	if cfg.Enabled != nil && !*cfg.Enabled {
		return false
	}
	if lastInputTokens > 0 {
		return lastInputTokens > cfg.TokenTrigger
	}
	return estimatedTokens > cfg.TokenTrigger
}

// selectCompactKeepCount 根据 token 预算选择保留的最近消息数。
//
// 算法：
//  1. 调用方传入 overheadTokens 表示不可压缩开销（system + tools 估算之和）。
//  2. TokenTarget 扣除不可压缩开销和摘要预算后，得到最近消息可用预算。
//  3. 从最后一条消息开始往前累计，预算允许就多保留；预算不足时停止。
//
// 不从 lastInputTokens 反推 overhead，因为 messages 在每个 step 末尾会追加新消息，
// 而 anchor 是上一轮 API 调用的输入，两者不可比。
func selectCompactKeepCount(messages []llm.ChatMessage, overheadTokens int, cfg conf.ReactContextCompactConfig) int {
	beforeCount := len(messages)
	if beforeCount <= 1 {
		return 0
	}
	maxKeep := beforeCount - 1
	if overheadTokens < 0 {
		overheadTokens = 0
	}
	// SummaryLimit 是摘要输出的字符（rune）上限，按 worst-case 全中文换算为 token 上限（×1.1）。
	summaryTokenBudget := cfg.SummaryLimit * 11 / 10
	budget := cfg.TokenTarget - overheadTokens - summaryTokenBudget
	keepCount := 0
	keptTokens := 0
	for k := 1; k <= maxKeep; k++ {
		single := estimateMessagesTokens(messages[beforeCount-k : beforeCount-k+1])
		if keepCount > 0 && keptTokens+single > budget {
			break
		}
		keepCount++
		keptTokens += single
	}
	if keepCount <= 0 {
		return 1
	}
	return keepCount
}

func compactSummaryMessageContent(summary string) string {
	return "以下是早期对话摘要，供继续回答时参考：\n" + strings.TrimSpace(summary)
}

func compactCoveredThrough(refs []reactMessageRef) []reactMessageRef {
	maxByRunID := make(map[string]reactMessageRef)
	for _, ref := range refs {
		if ref.RunID == "" || ref.Seq <= 0 {
			continue
		}
		current, ok := maxByRunID[ref.RunID]
		if !ok || ref.Seq > current.Seq {
			maxByRunID[ref.RunID] = ref
		}
	}
	covered := make([]reactMessageRef, 0, len(maxByRunID))
	for _, ref := range maxByRunID {
		covered = append(covered, ref)
	}
	return covered
}

func flattenMessageRefs(refGroups [][]reactMessageRef) []reactMessageRef {
	refs := make([]reactMessageRef, 0)
	for _, group := range refGroups {
		refs = append(refs, group...)
	}
	return refs
}

// estimateMessagesSize 用 JSON 序列化长度近似估算上下文大小，作为是否压缩的轻量判断依据。
func estimateMessagesSize(messages []llm.ChatMessage) int {
	data, _ := json.Marshal(messages)
	return len(data)
}

// buildLLMCompactSummary 使用当前 run 的模型生成语义压缩摘要；失败时由调用方回退到本地压缩。
//
// 终止边界治理（Phase 3）修复压缩器输入截断问题：待压缩 JSON 超过单次输入预算时，
// 先按轮次边界分片逐段总结（map），再把各段摘要归并为最终摘要（reduce），
// 不再把超长历史静默截断丢弃。LLM 压缩被熔断时直接走本地摘要。
func buildLLMCompactSummary(s *reactEngineState, messages []llm.ChatMessage) (string, int, int, error) {
	compactCfg := conf.GetReactRuntimeConfig().ContextCompact
	if s.compactLLMFailures >= compactCfg.CompactFailureBreaker {
		return "", 0, 0, errors.New("compact llm breaker open")
	}

	inputBudget := compactCfg.SummaryLimit * 2
	totalInputTokens, totalOutputTokens := 0, 0
	summarize := func(items []llm.ChatMessage, chunkIndex, chunkCount int) (string, error) {
		ctx, cancel := context.WithTimeout(s.ctx.Request.Context(), time.Duration(compactCfg.TimeoutSec)*time.Second)
		defer cancel()

		input, err := json.Marshal(items)
		if err != nil {
			return "", err
		}
		var prompt string
		if chunkCount > 1 {
			prompt = buildCompactChunkSummaryPrompt(string(input), chunkIndex, chunkCount, compactPartialSummaryLimit)
		} else {
			prompt = buildCompactSummaryPrompt(string(input))
		}

		stream, err := s.client.ChatStream(ctx, []llm.LLMMessage{
			{Role: "system", Content: "你只负责压缩上下文，不执行工具，不回答原始业务问题。"},
			{Role: "user", Content: prompt},
		}, s.currentModel.ModelVersion)
		if err != nil {
			return "", err
		}

		var summary strings.Builder
		for chunk := range stream {
			if chunk.Error != nil {
				return "", chunk.Error
			}
			if chunk.Content != "" {
				summary.WriteString(chunk.Content)
			}
			if chunk.Done {
				totalInputTokens += chunk.InputTokens
				totalOutputTokens += chunk.OutputTokens
			}
		}
		return truncateRunes(summary.String(), compactCfg.SummaryLimit), nil
	}

	// 归并循环：每层把超预算的分片逐段总结，得到更短的摘要层，直到能一次总结完。
	layer := messages
	for pass := 0; ; pass++ {
		chunks := splitMessagesByRuneBudget(layer, inputBudget)
		if len(chunks) <= 1 || pass >= 3 {
			// 单片可总结（或归并层数上限）：若仍超预算，退化为截断输入（与历史行为一致，尽力而为）。
			summary, err := summarize(layer, 0, 1)
			return summary, totalInputTokens, totalOutputTokens, err
		}
		s.logInfof("[react.compact] 压缩输入超预算，分片归并: runId=%s, pass=%d, chunks=%d, messages=%d", s.runID, pass, len(chunks), len(layer))
		next := make([]llm.ChatMessage, 0, len(chunks))
		for i, chunk := range chunks {
			partial, err := summarize(chunk, i+1, len(chunks))
			if err != nil {
				return "", totalInputTokens, totalOutputTokens, err
			}
			if strings.TrimSpace(partial) == "" {
				return "", totalInputTokens, totalOutputTokens, errors.New("compact chunk summary empty")
			}
			next = append(next, llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: partial})
		}
		layer = next
	}
}

// compactPartialSummaryLimit 是分片总结时每段摘要的字符上限：
// 保证归并层总输入可控（默认 2000 字 × 分片数，两到三层内收敛）。
const compactPartialSummaryLimit = 2000

// splitMessagesByRuneBudget 按 JSON rune 预算把消息切片分块；块边界对齐到完整轮次，
// 避免切开 tool_use/tool_result 配对。单条消息超预算时独立成块（不强行截断）；
// 预算内的对齐回退无进展时向后扩展到下一个安全边界（保配对优先于预算）。
func splitMessagesByRuneBudget(messages []llm.ChatMessage, budget int) [][]llm.ChatMessage {
	if budget <= 0 || len(messages) <= 1 {
		return [][]llm.ChatMessage{messages}
	}
	sizes := make([]int, len(messages))
	for i, msg := range messages {
		data, _ := json.Marshal(msg)
		sizes[i] = utf8.RuneCountInString(string(data))
	}
	// isSafeSplitBoundary 判定 end 是否为安全切块点：
	// messages[end] 不能以孤儿 tool_result 开头，messages[end-1] 不能以 tool_use 结尾。
	isSafeSplitBoundary := func(end int) bool {
		return end == len(messages) ||
			(!hasPartOfType(messages[end], "tool_result") && !hasPartOfType(messages[end-1], "tool_use"))
	}

	chunks := make([][]llm.ChatMessage, 0, 4)
	start := 0
	for start < len(messages) {
		// 预算内向前扩到最大 end（至少含一条消息；单条超预算独立成块）。
		accumulated := 0
		end := start
		for end < len(messages) {
			if end > start && accumulated+sizes[end] > budget {
				break
			}
			accumulated += sizes[end]
			end++
		}
		// 回退对齐到安全边界；无进展时向前扩展到下一个安全边界。
		safe := alignKeepStartToTurnBoundary(messages, end)
		if safe <= start {
			for safe = end; safe < len(messages); safe++ {
				if isSafeSplitBoundary(safe) {
					break
				}
			}
		}
		chunks = append(chunks, messages[start:safe])
		start = safe
	}
	return chunks
}

// buildCompactChunkSummaryPrompt 生成"分片总结"prompt：只总结本段历史的关键事实，
// 输出长度受限，供归并层继续压缩。
func buildCompactChunkSummaryPrompt(inputJSON string, chunkIndex, chunkCount, limit int) string {
	return "你是 ReAct Agent Runtime 的历史上下文压缩器。当前历史过长，已分段压缩；" +
		fmt.Sprintf("你现在处理第 %d/%d 段。\n\n", chunkIndex, chunkCount) +
		"只压缩下面的历史消息 JSON 中真实发生过的对话、工具调用、工具结果和状态变化。\n" +
		"1. 只保留事实，不补充、不猜测、不改写用户意图。\n" +
		"2. 保留用户真实目标、明确约束、已完成动作、关键结论、未完成事项。\n" +
		"3. 保留重要文件路径、ID、工具名、resultRef 和错误信息。\n" +
		"4. 不要输出本压缩任务的目标、规则或待压缩 JSON 本身。\n" +
		fmt.Sprintf("5. 输出中文，控制在 %d 字以内。\n\n", limit) +
		"本段历史消息 JSON：\n" + inputJSON
}

func buildCompactSummaryPrompt(inputJSON string) string {
	compactCfg := conf.GetReactRuntimeConfig().ContextCompact
	return "你是 ReAct Agent Runtime 的历史上下文压缩器。你的输出会作为后续模型的 user 历史上下文继续注入当前 run。\n" +
		"只压缩下面的历史消息 JSON 中真实发生过的对话、工具调用、工具结果和状态变化。\n\n" +
		"压缩规则：\n" +
		"1. 只保留历史消息中的事实，不补充、不猜测、不改写用户意图。\n" +
		"2. 保留用户真实目标、明确约束、已完成动作、关键结论、未完成事项。\n" +
		"3. 保留重要文件路径、ID、run/session 语义、工具名、resultRef 和错误信息。\n" +
		"4. 工具大结果如果已由 resultRef 表示，只记录 resultRef、摘要和读取方式，不展开全文。\n" +
		"5. 如果历史消息中存在 todo 状态，保留当前 todo 状态；不存在则省略，不要说明不存在。\n" +
		"6. 不要输出本压缩任务的目标、规则、提示词、输出格式要求或待压缩 JSON 本身。\n" +
		"7. 不要要求用户提供更多历史；信息少就输出极简摘要。\n" +
		fmt.Sprintf("8. 输出中文，控制在 %d 字以内，使用适合后续模型读取的自然段或短要点。\n\n", compactCfg.SummaryLimit) +
		"历史消息 JSON：\n" + truncateRunes(inputJSON, compactCfg.SummaryLimit*2)
}

// buildCompactSummary 将早期消息折叠为一条历史摘要，保留角色和主要内容便于模型延续上下文。
func buildCompactSummary(messages []llm.ChatMessage) string {
	compactCfg := conf.GetReactRuntimeConfig().ContextCompact
	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg.Content != "" {
			parts = append(parts, fmt.Sprintf("%s: %s", msg.Role, truncateRunes(msg.Content, compactCfg.FallbackMessagePreviewLimit)))
			continue
		}
		data, _ := json.Marshal(msg.Parts)
		parts = append(parts, fmt.Sprintf("%s: %s", msg.Role, truncateRunes(string(data), compactCfg.FallbackMessagePreviewLimit)))
	}
	return "以下为已压缩的历史上下文摘要，仅用于延续当前 run：\n" + strings.Join(parts, "\n")
}

// updateTodos 由内置 Meta Tool 调用，用于让模型增量维护当前 run 的结构化任务状态。
func (s *reactEngineState) updateTodos(call llm.ToolCall, step int) (string, bool, error) {
	now := nowUnixMilli()
	state, result, err := applyReactTodoWrite(s.todoStateJSON, call.Input, now)
	if err != nil {
		return "", true, err
	}
	s.todoStateJSON = encodeReactTodoState(state)
	if err := model.UpdateReactRunByRunID(s.ctx, s.runID, map[string]interface{}{"todo_state_json": s.todoStateJSON}); err != nil {
		return "", true, err
	}
	_ = s.emitter.EmitStep(step, EventTodoUpdate, params.ReactTodoUpdatePayload{TodoState: json.RawMessage(s.todoStateJSON)})
	data, _ := json.Marshal(result)
	return string(data), false, nil
}
