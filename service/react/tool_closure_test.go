package react

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	llm "react-base-service/api/llm"
	model "react-base-service/models/llm"
)

// ---------- tool_call id 去重（闭合治理 Phase 2） ----------

func TestDuplicateToolCallIndexes(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "a", Name: "list_tools"},
		{ID: "b", Name: "get_tool"},
		{ID: "a", Name: "list_tools"}, // 与首次出现重复
		{ID: "", Name: "unknown"},     // 空 ID 不参与去重
		{ID: "", Name: "unknown"},
	}
	duplicates := duplicateToolCallIndexes(calls)
	if len(duplicates) != 1 || !duplicates[2] {
		t.Fatalf("expected only index 2 duplicate, got %v", duplicates)
	}
}

func TestCollectLLMStreamDedupesToolCallIDs(t *testing.T) {
	state := &reactEngineState{runCtx: context.Background()}
	stream := make(chan llm.StreamChunk, 1)
	stream <- llm.StreamChunk{Done: true, StopReason: "tool_use", ToolCalls: []llm.ToolCall{
		{ID: "call_1", Name: "list_tools"},
		{ID: "call_1", Name: "list_tools"}, // adapter 异常重复投递
		{ID: "call_2", Name: "get_tool"},
	}}
	close(stream)

	res, err := state.collectLLMStream(stream, 0, func() {}, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("expected 2 unique tool calls after dedupe, got %d: %+v", len(res.ToolCalls), res.ToolCalls)
	}
	if res.ToolCalls[0].ID != "call_1" || res.ToolCalls[1].ID != "call_2" {
		t.Fatalf("unexpected tool calls: %+v", res.ToolCalls)
	}
}

// ---------- 轮次结果收口补全（闭合治理 Phase 1） ----------

func TestCompleteToolRoundResultsPrefersInterruptedRegistry(t *testing.T) {
	state := &reactEngineState{runCtx: context.Background()}
	state.recordInterruptedToolResult(llm.ToolResultContent{ToolUseID: "call_a", Content: `{"interrupted":true}`, IsError: true})

	calls := []llm.ToolCall{
		{ID: "call_a", Name: "execute_tool"}, // 执行中被打断，已有登记
		{ID: "call_b", Name: "list_tools"},   // 槽位缺失且无登记
	}
	results := []llm.ToolResultContent{
		{ToolUseID: "call_a", Content: `{"interrupted":true}`, IsError: true},
		{},
	}
	completed := state.completeToolRoundResults(calls, results)
	if len(completed) != 2 {
		t.Fatalf("expected 2 results, got %d", len(completed))
	}
	if completed[0].Content != `{"interrupted":true}` {
		t.Fatalf("expected registry result preserved, got %s", completed[0].Content)
	}
	if !completed[1].IsError || completed[1].ToolUseID != "call_b" {
		t.Fatalf("expected synthetic error result for call_b, got %+v", completed[1])
	}
	var payload NormalizedToolResult
	if err := json.Unmarshal([]byte(completed[1].Content), &payload); err != nil {
		t.Fatalf("synthetic content should be normalized JSON: %v", err)
	}
	if payload.Status != toolExecutionStatusCancelled {
		t.Fatalf("expected cancelled status, got %s", payload.Status)
	}
}

func TestFillUnexecutedSerialSuffixMarksNotExecuted(t *testing.T) {
	state := &reactEngineState{runCtx: context.Background()}
	calls := []llm.ToolCall{
		{ID: "call_1", Name: "execute_tool"},
		{ID: "call_2", Name: "list_tools"}, // 出错调用
		{ID: "call_3", Name: "get_tool"},   // 未开始
		{ID: "call_4", Name: "get_tool"},   // 未开始
	}
	results := make([]llm.ToolResultContent, len(calls))
	duplicates := duplicateToolCallIndexes(calls)
	results[1] = llm.ToolResultContent{ToolUseID: "call_2", Content: "boom", IsError: true}

	state.fillUnexecutedSerialSuffix(calls, results, duplicates, 1, false)
	completed := state.completeToolRoundResults(calls, results)

	if completed[0].ToolUseID != "call_1" || completed[0].Content == "" {
		// call_1 在停止点之前、本测试未执行，收口后应合成"状态未知"
		if !completed[0].IsError {
			t.Fatalf("expected call_1 filled with unknown-state error, got %+v", completed[0])
		}
	}
	for _, i := range []int{2, 3} {
		var payload NormalizedToolResult
		if err := json.Unmarshal([]byte(completed[i].Content), &payload); err != nil {
			t.Fatalf("call_%d content should be normalized JSON: %v", i+1, err)
		}
		if payload.Status != toolExecutionStatusCancelled || !completed[i].IsError {
			t.Fatalf("expected not-executed error result for index %d, got %+v", i, completed[i])
		}
	}
}

func TestSynthesizeUnexecutedToolResultOmitsResultRef(t *testing.T) {
	state := &reactEngineState{runCtx: context.Background()}
	result := state.synthesizeUnexecutedToolResult(llm.ToolCall{ID: "call_x", Name: "execute_tool"}, toolUnexecutedContent)
	var payload NormalizedToolResult
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("content should be normalized JSON: %v", err)
	}
	if payload.ResultRef != "" {
		t.Fatalf("synthetic result should not carry a resultRef (no stored content), got %s", payload.ResultRef)
	}
	if !result.IsError || result.ToolUseID != "call_x" {
		t.Fatalf("expected error result paired to call_x, got %+v", result)
	}
}

// ---------- 历史重建孤儿 tool_use 回填（第三道防线） ----------

func assistantWithToolUse(id string) llm.ChatMessage {
	return llm.ChatMessage{Role: model.ReactMessageRoleAssistant, Parts: []llm.ContentPart{
		{Type: "text", Text: "调用工具"},
		{Type: "tool_use", ID: id, Name: "execute_tool"},
	}}
}

func closureToolResultMessage(id string) llm.ChatMessage {
	return llm.ChatMessage{Role: model.ReactMessageRoleUser, Parts: []llm.ContentPart{
		{Type: "tool_result", ToolUseID: id, Content: "ok"},
	}}
}

func userTextMessage(text string) llm.ChatMessage {
	return llm.ChatMessage{Role: model.ReactMessageRoleUser, Content: text}
}

func orphanResultIDs(messages []llm.ChatMessage) map[string]string {
	found := make(map[string]string)
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Type == "tool_result" && part.Content == toolOrphanBackfillContent {
				found[part.ToolUseID] = part.Content
			}
		}
	}
	return found
}

func TestBackfillOrphanToolResultsInsertsAfterResultBlock(t *testing.T) {
	messages := []llm.ChatMessage{
		assistantWithToolUse("call_1"),
		closureToolResultMessage("call_1"),
		{Role: model.ReactMessageRoleAssistant, Parts: []llm.ContentPart{
			{Type: "tool_use", ID: "call_2", Name: "execute_tool"},
			{Type: "tool_use", ID: "call_3", Name: "get_tool"},
		}},
		closureToolResultMessage("call_2"), // call_3 孤儿
		userTextMessage("继续"),
	}
	refs := make([][]reactMessageRef, len(messages))
	for i := range refs {
		refs[i] = []reactMessageRef{{RunID: "run", Seq: i + 1}}
	}

	out, outRefs := backfillOrphanToolResults(messages, refs)
	orphans := orphanResultIDs(out)
	if len(orphans) != 1 || orphans["call_3"] == "" {
		t.Fatalf("expected backfill for call_3 only, got %v", orphans)
	}
	// 回填消息必须插在 call_2 结果之后、普通 user 输入之前，且 refs 与 messages 等长对齐。
	if len(out) != len(messages)+1 || len(outRefs) != len(out) {
		t.Fatalf("expected one inserted message with aligned refs, got messages=%d refs=%d", len(out), len(outRefs))
	}
	if out[len(out)-1].Content != "继续" {
		t.Fatalf("user text should stay after the backfill message, got tail: %+v", out[len(out)-1])
	}
	if outRefs[len(out)-1][0].Seq != 5 {
		t.Fatalf("original refs should be preserved, got %+v", outRefs[len(out)-1])
	}
	if outRefs[len(out)-2] != nil {
		t.Fatalf("backfill message should carry nil ref, got %+v", outRefs[len(out)-2])
	}
}

func TestBackfillOrphanToolResultsNoopWhenPaied(t *testing.T) {
	messages := []llm.ChatMessage{
		assistantWithToolUse("call_1"),
		closureToolResultMessage("call_1"),
		userTextMessage("继续"),
	}
	out, outRefs := backfillOrphanToolResults(messages, make([][]reactMessageRef, len(messages)))
	if len(out) != len(messages) || len(outRefs) != len(out) {
		t.Fatalf("paired history should be untouched, got %d messages", len(out))
	}
	if orphans := orphanResultIDs(out); len(orphans) != 0 {
		t.Fatalf("expected no backfill, got %v", orphans)
	}
}

func TestBackfillOrphanToolResultsAppendsAtEndWhenRunEndedMidRound(t *testing.T) {
	messages := []llm.ChatMessage{
		userTextMessage("问题"),
		assistantWithToolUse("call_9"), // run 在结果落库前终止
	}
	out, outRefs := backfillOrphanToolResults(messages, make([][]reactMessageRef, len(messages)))
	orphans := orphanResultIDs(out)
	if len(out) != len(messages)+1 || len(outRefs) != len(out) {
		t.Fatalf("expected one inserted message with aligned refs, got messages=%d refs=%d", len(out), len(outRefs))
	}
	if len(orphans) != 1 || orphans["call_9"] == "" {
		t.Fatalf("expected backfill for call_9, got %v", orphans)
	}
	if out[len(out)-1].Role != model.ReactMessageRoleUser || len(out[len(out)-1].Parts) == 0 {
		t.Fatalf("backfill message should be the tail user message, got %+v", out[len(out)-1])
	}
}
