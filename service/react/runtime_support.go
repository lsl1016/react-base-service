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
func classifyToolInterruption(runCtx context.Context, err error) (status, content string, ok bool) {
	var cause error
	if runCtx != nil {
		cause = context.Cause(runCtx)
	}
	switch {
	case errors.Is(err, ErrReactClientDisconnected) || errors.Is(cause, ErrReactClientDisconnected):
		return toolExecutionStatusError, "连接断开，工具执行已中断，结果可能不确定。", true
	case errors.Is(err, ErrReactRunCancelled) || errors.Is(cause, ErrReactRunCancelled):
		return toolExecutionStatusCancelled, "用户取消了本次运行，工具未完成执行。", true
	case errors.Is(err, context.Canceled):
		return toolExecutionStatusError, "工具执行上下文已中断，结果可能不确定。", true
	default:
		return "", "", false
	}
}

// closeInterruptedToolUse 为已经发出 start 事件的工具补齐结构化终态和历史 tool_result，随后由调用方继续向上传播中断错误。
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
	if _, persistErr := persistToolResultMessage(s.ctx, s.req, s.runID, s.sessionID, []llm.ToolResultContent{result}, step); persistErr != nil {
		zlog.Warnf(s.ctx, "[React] 中断工具结果落库失败(忽略): runId=%s, toolUseId=%s, err=%v", s.runID, call.ID, persistErr)
	}
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

// maybeCompactContext 在上下文过长时压缩早期消息，只保留摘要和最近若干轮对话。
func maybeCompactContext(s *reactEngineState, step int) error {
	compactCfg := conf.GetReactRuntimeConfig().ContextCompact
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
	if err != nil || strings.TrimSpace(summary) == "" {
		zlog.Warnf(s.ctx, "[react.maybeCompactContext] LLM上下文压缩失败，回退本地压缩: runId=%s, err=%v", s.runID, err)
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
	if err := s.emitter.EmitStep(step, EventCompactEnd, params.ReactCompactEndPayload{BeforeMessageCount: beforeCount, AfterMessageCount: len(s.messages), Summary: summary}); err != nil {
		return err
	}
	// compact_end 成功后判定是否派生记忆维护（extractor 优先、与 reflection 互斥）：异步、受限、绝不阻塞主 run；
	// compactPart 是被压缩掉的原始消息，此刻仍在内存中，直接作为整理素材传入。
	maybeTriggerMemoryMaintenance(s, summary, compactPart)
	return nil
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
func buildLLMCompactSummary(s *reactEngineState, messages []llm.ChatMessage) (string, int, int, error) {
	compactCfg := conf.GetReactRuntimeConfig().ContextCompact
	ctx, cancel := context.WithTimeout(s.ctx.Request.Context(), time.Duration(compactCfg.TimeoutSec)*time.Second)
	defer cancel()

	input, err := json.Marshal(messages)
	if err != nil {
		return "", 0, 0, err
	}
	prompt := buildCompactSummaryPrompt(string(input))

	stream, err := s.client.ChatStream(ctx, []llm.LLMMessage{
		{Role: "system", Content: "你只负责压缩上下文，不执行工具，不回答原始业务问题。"},
		{Role: "user", Content: prompt},
	}, s.currentModel.ModelVersion)
	if err != nil {
		return "", 0, 0, err
	}

	var summary strings.Builder
	var inputTokens, outputTokens int
	for chunk := range stream {
		if chunk.Error != nil {
			return "", inputTokens, outputTokens, chunk.Error
		}
		if chunk.Content != "" {
			summary.WriteString(chunk.Content)
		}
		if chunk.Done {
			inputTokens = chunk.InputTokens
			outputTokens = chunk.OutputTokens
		}
	}
	return truncateRunes(summary.String(), compactCfg.SummaryLimit), inputTokens, outputTokens, nil
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
