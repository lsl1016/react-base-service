package react

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	model "react-base-service/models/llm"
	toolService "react-base-service/service/tool"
)

func TestParseToolConfigAsyncFields(t *testing.T) {
	cfg, err := toolService.ParseToolConfig(`{"url":"http://example.com/submit","async":true,"asyncHint":"用 query_task 查询, taskId 放 body.query.taskId"}`)
	if err != nil {
		t.Fatalf("parse config failed: %v", err)
	}
	if !cfg.Async {
		t.Fatalf("expected async true")
	}
	if cfg.AsyncHint != "用 query_task 查询, taskId 放 body.query.taskId" {
		t.Fatalf("unexpected asyncHint: %q", cfg.AsyncHint)
	}

	plain, err := toolService.ParseToolConfig(`{"url":"http://example.com/query"}`)
	if err != nil {
		t.Fatalf("parse plain config failed: %v", err)
	}
	if plain.Async {
		t.Fatalf("expected async false by default")
	}
}

func TestTruncateAsyncSnapshot(t *testing.T) {
	// 未超限不加标记。
	if got := truncateAsyncSnapshot("short", 100); got != "short" {
		t.Fatalf("unexpected short result: %q", got)
	}
	// 超限截断并追加标记。
	got := truncateAsyncSnapshot(strings.Repeat("a", 50), 10)
	if !strings.HasSuffix(got, reactAsyncTaskTruncatedMark) {
		t.Fatalf("expected truncated mark suffix: %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 10)) {
		t.Fatalf("expected 10 leading runes: %q", got)
	}
	// 恰好等于上限不加标记。
	if got := truncateAsyncSnapshot(strings.Repeat("b", 10), 10); strings.Contains(got, reactAsyncTaskTruncatedMark) {
		t.Fatalf("exact-length content should not be marked: %q", got)
	}
}

func TestRenderReactAsyncTaskReminderEmpty(t *testing.T) {
	if got := renderReactAsyncTaskReminder(nil, false); got != "" {
		t.Fatalf("expected empty reminder, got %q", got)
	}
}

func TestRenderReactAsyncTaskReminder(t *testing.T) {
	createdAt := time.Date(2026, 7, 17, 14, 32, 0, 0, time.Local)
	tasks := []model.ReactAsyncTask{
		{
			ToolUseID:    "toolu_01",
			ToolName:     "run_report",
			SubmitInput:  `{"reportType":"sales","month":"2026-07"}`,
			SubmitResult: `{"taskId":"abc123"}`,
			AsyncHint:    "用 query_report_result 查询, taskId 放 body.query.taskId",
			CreatedAt:    createdAt,
		},
		{
			ToolUseID:    "toolu_02",
			ToolName:     "export_data",
			SubmitResult: `{"taskId":"def456"}`,
			CreatedAt:    createdAt,
		},
	}
	reminder := renderReactAsyncTaskReminder(tasks, false)
	for _, expected := range []string{
		"<async_tasks>", "</async_tasks>",
		"toolu_01", "run_report", `{"taskId":"abc123"}`,
		`提交参数: {"reportType":"sales","month":"2026-07"}`,
		"查询提示: 用 query_report_result 查询, taskId 放 body.query.taskId",
		"toolu_02", "export_data",
		"07-17 14:32",
		"容量受限的任务快照",
		"未列出不代表不是历史任务", "可信历史 <async_tasks>", "来源不明确时不得猜测",
		"resolve_async_task", "get_async_task",
	} {
		if !strings.Contains(reminder, expected) {
			t.Fatalf("reminder missing %q:\n%s", expected, reminder)
		}
	}
	if strings.Contains(reminder, "另有更早的未完结任务未列出") {
		t.Fatalf("unexpected hasMore hint:\n%s", reminder)
	}
	// 第二条任务未配置 asyncHint / 无提交入参时，不应出现空的查询提示或提交参数行。
	if strings.Count(reminder, "查询提示:") != 1 {
		t.Fatalf("expected exactly one hint line:\n%s", reminder)
	}
	if strings.Count(reminder, "提交参数:") != 1 {
		t.Fatalf("expected exactly one submit input line:\n%s", reminder)
	}
}

func TestRenderReactAsyncTaskReminderTruncatesLongFields(t *testing.T) {
	// 落库全文（16384 内），渲染时截到 2048 并打标记。
	longSQL := strings.Repeat("SELECT * FROM t; ", 400) // 约 6800 字符
	tasks := []model.ReactAsyncTask{{
		ToolUseID:    "toolu_01",
		ToolName:     "run_sql",
		SubmitInput:  longSQL,
		SubmitResult: "{}",
		CreatedAt:    time.Now(),
	}}
	reminder := renderReactAsyncTaskReminder(tasks, false)
	if !strings.Contains(reminder, reactAsyncTaskTruncatedMark) {
		t.Fatalf("expected truncation mark for long submit input:\n%s", reminder[:200])
	}
	if len([]rune(reminder)) > 4096 {
		t.Fatalf("reminder unexpectedly large: %d runes", len([]rune(reminder)))
	}
}

func TestRenderReactAsyncTaskReminderHasMore(t *testing.T) {
	tasks := []model.ReactAsyncTask{{ToolUseID: "toolu_01", ToolName: "run_report", SubmitResult: "{}", CreatedAt: time.Now()}}
	reminder := renderReactAsyncTaskReminder(tasks, true)
	if !strings.Contains(reminder, "另有更早的未完结任务未列出") {
		t.Fatalf("expected hasMore hint:\n%s", reminder)
	}
}

func TestRemovePendingAsyncTask(t *testing.T) {
	state := &reactEngineState{reactEngineAsyncState: reactEngineAsyncState{pendingAsyncTasks: []model.ReactAsyncTask{
		{ToolUseID: "toolu_01"},
		{ToolUseID: "toolu_02"},
	}}}
	state.removePendingAsyncTask("toolu_01")
	if len(state.pendingAsyncTasks) != 1 || state.pendingAsyncTasks[0].ToolUseID != "toolu_02" {
		t.Fatalf("unexpected pending tasks after remove: %+v", state.pendingAsyncTasks)
	}
	// 移除不存在的任务应保持列表不变。
	state.removePendingAsyncTask("toolu_missing")
	if len(state.pendingAsyncTasks) != 1 {
		t.Fatalf("expected list unchanged, got %+v", state.pendingAsyncTasks)
	}
}

func TestResolveAsyncTaskInputValidation(t *testing.T) {
	state := &reactEngineState{}
	if _, isError, err := state.resolveAsyncTask([]byte(`{"status":"done"}`)); !isError || err == nil || !strings.Contains(err.Error(), "toolUseId is required") {
		t.Fatalf("expected toolUseId required error, got isError=%v err=%v", isError, err)
	}
	if _, isError, err := state.resolveAsyncTask([]byte(`{"toolUseId":"toolu_01","status":"running"}`)); !isError || err == nil || !strings.Contains(err.Error(), "must be done or failed") {
		t.Fatalf("expected status invalid error, got isError=%v err=%v", isError, err)
	}
}

func TestHandleResolvedAsyncTaskState(t *testing.T) {
	t.Run("already resolved uses actual terminal status", func(t *testing.T) {
		state := &reactEngineState{reactEngineAsyncState: reactEngineAsyncState{pendingAsyncTasks: []model.ReactAsyncTask{
			{ToolUseID: "toolu_01"},
			{ToolUseID: "toolu_02"},
		}}}
		content, isError, err := state.handleResolvedAsyncTaskState("toolu_01", &model.ReactAsyncTask{
			ToolUseID:     "toolu_01",
			State:         model.ReactAsyncTaskStateResolved,
			ResolveStatus: reactAsyncResolveStatusFailed,
		})
		if err != nil || isError {
			t.Fatalf("expected idempotent success, got isError=%v err=%v", isError, err)
		}
		var result struct {
			Status          string `json:"status"`
			State           string `json:"state"`
			Resolved        bool   `json:"resolved"`
			AlreadyResolved bool   `json:"alreadyResolved"`
			Message         string `json:"message"`
		}
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			t.Fatalf("unmarshal result failed: %v, content=%s", err, content)
		}
		if result.Status != reactAsyncResolveStatusFailed || result.State != model.ReactAsyncTaskStateResolved || !result.Resolved || !result.AlreadyResolved {
			t.Fatalf("unexpected idempotent result: %+v", result)
		}
		if !strings.Contains(result.Message, "no retry") {
			t.Fatalf("expected no-retry message, got %+v", result)
		}
		if len(state.pendingAsyncTasks) != 1 || state.pendingAsyncTasks[0].ToolUseID != "toolu_02" {
			t.Fatalf("resolved task reminder was not removed: %+v", state.pendingAsyncTasks)
		}
	})

	t.Run("expired is a non-retryable terminal result", func(t *testing.T) {
		state := &reactEngineState{reactEngineAsyncState: reactEngineAsyncState{pendingAsyncTasks: []model.ReactAsyncTask{{ToolUseID: "toolu_expired"}}}}
		content, isError, err := state.handleResolvedAsyncTaskState("toolu_expired", &model.ReactAsyncTask{
			ToolUseID: "toolu_expired",
			State:     model.ReactAsyncTaskStateExpired,
		})
		if err != nil || isError {
			t.Fatalf("expected terminal success, got isError=%v err=%v", isError, err)
		}
		var result struct {
			State           string `json:"state"`
			Terminal        bool   `json:"terminal"`
			AlreadyTerminal bool   `json:"alreadyTerminal"`
			Message         string `json:"message"`
		}
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			t.Fatalf("unmarshal result failed: %v, content=%s", err, content)
		}
		if result.State != model.ReactAsyncTaskStateExpired || !result.Terminal || !result.AlreadyTerminal || !strings.Contains(result.Message, "no retry") {
			t.Fatalf("unexpected expired result: %+v", result)
		}
		if len(state.pendingAsyncTasks) != 0 {
			t.Fatalf("expired task reminder was not removed: %+v", state.pendingAsyncTasks)
		}
	})

	t.Run("missing task points to the exact reminder ID", func(t *testing.T) {
		state := &reactEngineState{reactEngineAsyncState: reactEngineAsyncState{pendingAsyncTasks: []model.ReactAsyncTask{{ToolUseID: "toolu_missing"}}}}
		content, isError, err := state.handleResolvedAsyncTaskState("toolu_typo", nil)
		if err != nil || isError {
			t.Fatalf("expected non-error result, got isError=%v err=%v", isError, err)
		}
		var result struct {
			Found       bool   `json:"found"`
			Terminal    bool   `json:"terminal"`
			RetrySameID bool   `json:"retrySameId"`
			Message     string `json:"message"`
		}
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			t.Fatalf("unmarshal result failed: %v, content=%s", err, content)
		}
		if result.Found || result.Terminal || result.RetrySameID || !strings.Contains(result.Message, "re-read the exact toolUseId") {
			t.Fatalf("unexpected missing result: %+v", result)
		}
		if len(state.pendingAsyncTasks) != 1 || state.pendingAsyncTasks[0].ToolUseID != "toolu_missing" {
			t.Fatalf("pending task reminder should remain available: %+v", state.pendingAsyncTasks)
		}
	})

	t.Run("unexpected non-terminal state remains an error", func(t *testing.T) {
		state := &reactEngineState{}
		if _, isError, err := state.handleResolvedAsyncTaskState("toolu_pending", &model.ReactAsyncTask{State: model.ReactAsyncTaskStatePending}); !isError || err == nil || !strings.Contains(err.Error(), "not resolvable") {
			t.Fatalf("expected non-terminal state error, got isError=%v err=%v", isError, err)
		}
	})
}

func TestGetAsyncTaskInputValidation(t *testing.T) {
	state := &reactEngineState{}
	if _, isError, err := state.getAsyncTask([]byte(`{}`)); !isError || err == nil || !strings.Contains(err.Error(), "toolUseId is required") {
		t.Fatalf("expected toolUseId required error, got isError=%v err=%v", isError, err)
	}
}
