package react

import (
	"sort"
	"strings"

	llm "react-base-service/api/llm"
	model "react-base-service/models/llm"
)

// Tool Runtime 闭合治理（Phase 1，见 docs/plan/20260925_ToolRuntime流水线借鉴与优化方案.md）：
// 每个 assistant tool_use 必须有配对 tool_result 是结构保证，不靠各错误路径自觉。
//
// 实现分三层：
//  1. 中断路径（取消/断线/确认与问答等待被打断）不再各自单独落库，只把合成结果登记到
//     s.roundInterruptedResults，由引擎层在本轮收口时统一落库一次，保证每个 tool_use
//     恰好一条 tool_result（不会出现中断单独落库 + 轮次整体落库的双份结果）；
//  2. completeToolRoundResults 在执行收口处补全缺失结果：中断登记优先，其次合成"状态未知"；
//     串行中断时后续未开始的调用由调用方直接合成"未执行"；
//  3. backfillOrphanToolResults 在历史重建时兜底：存量数据或极端故障（结果落库失败）留下的
//     孤儿 tool_use 会被回填合成结果，避免恢复后的 provider 请求被拒绝。

const (
	// toolUnexecutedContent 用于"确定未开始执行"的调用（串行中断后的剩余调用、预算耗尽）：
	// 对应 ZCode not_executed 语义——可以按失败处理。
	toolUnexecutedContent = "运行在执行开始前被中断，该调用未执行、未产生任何副作用。请按失败处理，不要盲目重试同一操作。"
	// toolUnknownStateContent 用于"执行状态未知"的调用（执行中被打断但结果未登记、结果落库失败）：
	// 对应 ZCode unknown_execution_state 语义——必须先核实状态再决定是否重试。
	toolUnknownStateContent = "该工具调用未返回执行结果（执行被中断或结果未能保存），副作用状态未知。请先核实当前状态再决定是否重试，不要盲目重试同一操作。"
	// toolOrphanBackfillContent 用于历史重建时发现的孤儿 tool_use（存量数据或极端故障）。
	toolOrphanBackfillContent = "该工具调用的执行结果在历史中缺失（运行中断或结果落库失败），副作用状态未知。请按失败处理，先核实当前状态再决定是否重试，不要盲目重试同一操作。"
	// duplicateToolCallContent 用于同轮重复 tool_call id 的兜底拒绝。
	duplicateToolCallContent = "检测到重复的 tool_call id，本轮已忽略该重复调用；每个 tool_use 只会执行一次。"
)

// recordInterruptedToolResult 登记中断路径合成的 tool_result，延迟到引擎层本轮收口时统一落库。
func (s *reactEngineState) recordInterruptedToolResult(result llm.ToolResultContent) {
	if strings.TrimSpace(result.ToolUseID) == "" {
		return
	}
	if s.roundInterruptedResults == nil {
		s.roundInterruptedResults = make(map[string]llm.ToolResultContent)
	}
	s.roundInterruptedResults[result.ToolUseID] = result
	s.logInfof("[React.Closure] 中断工具结果登记(收口统一落库): runId=%s, toolUseId=%s", s.runID, result.ToolUseID)
}

// interruptedToolResult 查看已登记的中断结果（不删除，登记表随轮次清空）。
func (s *reactEngineState) interruptedToolResult(toolUseID string) (llm.ToolResultContent, bool) {
	if s.roundInterruptedResults == nil {
		return llm.ToolResultContent{}, false
	}
	result, ok := s.roundInterruptedResults[toolUseID]
	return result, ok
}

// synthesizeUnexecutedToolResult 为未获得执行的调用合成错误结果（不产生 resultRef，
// 因为没有可分页读取的完整内容；状态统一记为 cancelled，语义是"运行级中断"而非工具自身失败）。
func (s *reactEngineState) synthesizeUnexecutedToolResult(call llm.ToolCall, content string) llm.ToolResultContent {
	normalized := normalizeToolResult(call.ID, content, true, s.executedBy(call.Name))
	normalized.ResultRef = ""
	normalized.Status = toolExecutionStatusCancelled
	return llm.ToolResultContent{ToolUseID: call.ID, Content: normalized.LLMContent(), IsError: true}
}

// duplicateToolCallResult 为同轮重复 tool_call id 生成拒绝结果（不执行，只回灌提示）。
func (s *reactEngineState) duplicateToolCallResult(call llm.ToolCall) llm.ToolResultContent {
	return s.synthesizeUnexecutedToolResult(call, duplicateToolCallContent)
}

// duplicateToolCallIndexes 返回与更早索引出现相同非空 ID 的调用下标集合；
// 空 ID 无法判定重复，全部放行（由执行层按未知工具回灌）。
func duplicateToolCallIndexes(calls []llm.ToolCall) map[int]bool {
	duplicates := make(map[int]bool)
	seen := make(map[string]bool, len(calls))
	for i, call := range calls {
		if call.ID == "" {
			continue
		}
		if seen[call.ID] {
			duplicates[i] = true
			continue
		}
		seen[call.ID] = true
	}
	return duplicates
}

// completeToolRoundResults 是执行收口处的结果补全：零值槽位先取中断登记，再合成"状态未知"。
// 调用方必须已对"确定未开始执行"的后缀槽位填好"未执行"结果，这里只兜底剩余异常。
func (s *reactEngineState) completeToolRoundResults(calls []llm.ToolCall, results []llm.ToolResultContent) []llm.ToolResultContent {
	completed := make([]llm.ToolResultContent, len(calls))
	for i := range calls {
		result := llm.ToolResultContent{}
		if i < len(results) {
			result = results[i]
		}
		if strings.TrimSpace(result.ToolUseID) != "" {
			completed[i] = result
			continue
		}
		if recorded, ok := s.interruptedToolResult(calls[i].ID); ok {
			completed[i] = recorded
			continue
		}
		completed[i] = s.synthesizeUnexecutedToolResult(calls[i], toolUnknownStateContent)
	}
	return completed
}

// assistantToolUseIDs 按出现顺序提取 assistant 消息里的 tool_use id。
func assistantToolUseIDs(msg llm.ChatMessage) []string {
	ids := make([]string, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		if part.Type == "tool_use" && strings.TrimSpace(part.ID) != "" {
			ids = append(ids, part.ID)
		}
	}
	return ids
}

// messageToolResultIDs 提取消息内全部 tool_result 的 toolUseId 集合。
func messageToolResultIDs(msg llm.ChatMessage) map[string]bool {
	ids := make(map[string]bool)
	for _, part := range msg.Parts {
		if part.Type == "tool_result" && strings.TrimSpace(part.ToolUseID) != "" {
			ids[part.ToolUseID] = true
		}
	}
	return ids
}

// backfillOrphanToolResults 扫描重建出的历史，为没有配对 tool_result 的 assistant tool_use
// 插入一条合成 user 消息（每条孤儿结果一个 part），插入位置在该 assistant 的结果块末尾。
// 纯函数、无副作用：messages 与 refs 一同补齐（回填消息无 DB 引用，ref 记 nil）。
func backfillOrphanToolResults(messages []llm.ChatMessage, refs [][]reactMessageRef) ([]llm.ChatMessage, [][]reactMessageRef) {
	out := make([]llm.ChatMessage, 0, len(messages)+2)
	outRefs := make([][]reactMessageRef, 0, len(refs)+2)
	refAt := func(i int) []reactMessageRef {
		if i >= 0 && i < len(refs) {
			return refs[i]
		}
		return nil
	}

	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role != model.ReactMessageRoleAssistant {
			out = append(out, msg)
			outRefs = append(outRefs, refAt(i))
			continue
		}
		expected := assistantToolUseIDs(msg)
		if len(expected) == 0 {
			out = append(out, msg)
			outRefs = append(outRefs, refAt(i))
			continue
		}

		// 向后扫描结果块：遇到下一条 assistant 或普通 user 文本（下一轮输入）即结束。
		covered := make(map[string]bool, len(expected))
		blockEnd := len(messages)
		for j := i + 1; j < len(messages); j++ {
			next := messages[j]
			if next.Role == model.ReactMessageRoleAssistant {
				blockEnd = j
				break
			}
			ids := messageToolResultIDs(next)
			if len(ids) == 0 {
				blockEnd = j
				break
			}
			for id := range ids {
				covered[id] = true
			}
		}

		out = append(out, msg)
		outRefs = append(outRefs, refAt(i))
		for k := i + 1; k < blockEnd; k++ {
			out = append(out, messages[k])
			outRefs = append(outRefs, refAt(k))
		}

		orphans := make([]string, 0, len(expected))
		for _, id := range expected {
			if !covered[id] {
				orphans = append(orphans, id)
			}
		}
		if len(orphans) > 0 {
			out = append(out, orphanBackfillMessage(orphans))
			outRefs = append(outRefs, nil)
		}
		i = blockEnd - 1
	}
	return out, outRefs
}

// orphanBackfillMessage 为孤儿 tool_use 构造回填消息（part 顺序与 assistant 内 tool_use 顺序一致）。
func orphanBackfillMessage(orphans []string) llm.ChatMessage {
	sorted := append([]string(nil), orphans...)
	sort.Strings(sorted)
	parts := make([]llm.ContentPart, 0, len(sorted))
	for _, id := range sorted {
		parts = append(parts, llm.ContentPart{Type: "tool_result", ToolUseID: id, Content: toolOrphanBackfillContent, IsError: true})
	}
	return llm.ChatMessage{Role: model.ReactMessageRoleUser, Parts: parts}
}
