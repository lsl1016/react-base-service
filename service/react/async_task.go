package react

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	model "react-base-service/models/llm"
	"react-base-service/service/asynctask"
	toolService "react-base-service/service/tool"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

const (
	// reactAsyncTaskTTL 是异步任务提醒的存活时间；超时后惰性置 expired，不再注入提醒。
	reactAsyncTaskTTL = 7 * 24 * time.Hour
	// maxReactAsyncTaskReminderItems 限制单次注入的任务条数，防止提醒膨胀撑大上下文。
	maxReactAsyncTaskReminderItems = 10
	// maxReactAsyncTaskRenderRunes 是注入提醒时每个字段的截断上限，只为省每轮 token，不影响落库全文；
	// 被截断的内容可通过 get_async_task 回读完整记录。落库不截断：submit_input/submit_result 列为 MEDIUMTEXT，
	// 存完整副本供 get_async_task 回读，超 16MB 由 DB 写入报错触发降级（不落库不提醒），无需人为上限。
	maxReactAsyncTaskRenderRunes = 2048
	// maxReactAsyncHintRunes 匹配 async_hint 列的 varchar(512) 宽度，是真实列约束。
	maxReactAsyncHintRunes = 512

	reactAsyncResolveStatusDone   = "done"
	reactAsyncResolveStatusFailed = "failed"

	// reactAsyncTaskTruncatedMark 标识内容被截断，避免模型把残缺 JSON 当成完整内容或接口异常。
	reactAsyncTaskTruncatedMark = "…(已截断，可用 get_async_task 获取完整内容)"
)

// truncateAsyncSnapshot 截断内容并在发生截断时追加标记；已带标记的内容再截不会重复追加。
func truncateAsyncSnapshot(content string, limit int) string {
	truncated := truncateRunes(content, limit)
	if len(truncated) < len(content) && !strings.HasSuffix(truncated, reactAsyncTaskTruncatedMark) {
		return truncated + reactAsyncTaskTruncatedMark
	}
	return truncated
}

// recordAsyncSubmit 把“提交后不会立即得到最终结果”的业务 Tool 转换为 React Async Task。
//
// 当前 Async Task 仍属于“跨 Run 提醒 + 显式查询/resolve”模型：它能跨会话轮次保存任务快照，
// 但不会因为外部任务完成而自动唤醒原 Run。真正的自动暂停/恢复应由后续 Durable Runtime 负责。
// 因此这里的职责是记录、提醒和查询，不承担后台长期执行编排。
//
//
// submit_input 存提交入参快照：历史被压缩后，模型仍能凭它把查询结果关联回任务的业务语义。
// rawContent 是工具原始响应，落库存全文（列为 MEDIUMTEXT），供 get_async_task 回读完整内容。
// normalized.IsError 只覆盖 HTTP 传输层失败；HTTP 2xx 但响应体是业务错误时后端无法识别，会照常记录，
// 依赖模型读到错误响应后按提醒规则调用 resolve_async_task 标记 failed 清除。
// 落库失败只告警不阻断工具结果回填：提醒缺失可由模型翻历史弥补，不值得让 run 失败。
func (s *reactEngineState) recordAsyncSubmit(tool model.Tool, submitInput json.RawMessage, rawContent string, normalized NormalizedToolResult) {
	if normalized.IsError {
		return
	}
	cfg, err := toolService.ParseToolConfig(tool.Config)
	if err != nil || !cfg.Async {
		return
	}
	task := &model.ReactAsyncTask{
		SessionID:       s.sessionID,
		RunID:           s.runID,
		ToolUseID:       normalized.ToolUseID,
		ToolName:        tool.Name,
		SubmitInput:     strings.TrimSpace(string(submitInput)),
		SubmitResult:    rawContent,
		AsyncHint:       truncateAsyncSnapshot(strings.TrimSpace(cfg.AsyncHint), maxReactAsyncHintRunes),
		State:           model.ReactAsyncTaskStatePending,
		ExecutionStatus: "",
		TaskInfo:        "{}",
		// 显式设置创建时间，保证内存 append 的对象渲染提醒时时间正确，不依赖 gorm 回填。
		CreatedAt: time.Now(),
		ExpireAt:  time.Now().Add(reactAsyncTaskTTL),
	}
	s.applyManagedAsyncTaskFields(task, cfg, tool, submitInput, rawContent)
	if err := model.CreateReactAsyncTask(s.ctx, task); err != nil {
		zlog.Warnf(s.ctx, "[React] 记录异步任务失败(忽略,不影响工具结果): runId=%s, toolUseId=%s, err=%v", s.runID, normalized.ToolUseID, err)
		return
	}
	// 新任务插入内存列表头部，本 run 后续步骤的提醒立即可见。
	s.pendingAsyncTasks = append([]model.ReactAsyncTask{*task}, s.pendingAsyncTasks...)
	if len(s.pendingAsyncTasks) > maxReactAsyncTaskReminderItems {
		s.pendingAsyncTasks = s.pendingAsyncTasks[:maxReactAsyncTaskReminderItems]
		s.pendingAsyncTasksHasMore = true
	}
}

// applyManagedAsyncTaskFields 尝试把通用异步提交结果映射为受管理任务。
// Provider 成功识别后可补充 schedulerType/taskKey/nextSyncAt 等结构化信息；识别失败时仍保留旧的
// 模型提醒模式，因此“异步任务 Provider”是增强能力，不是 Tool 调用成功的必要条件。
//
//
// 识别失败只降级为旧的模型提醒模式，不影响 Tool 结果和当前 ReAct run。
func (s *reactEngineState) applyManagedAsyncTaskFields(task *model.ReactAsyncTask, cfg *toolService.ToolConfig, tool model.Tool, submitInput json.RawMessage, rawContent string) {
	if task == nil || cfg == nil || cfg.AsyncTask == nil {
		return
	}
	managed, err := asynctask.IdentifySubmission(s.runCtx, cfg.AsyncTask.SchedulerType, asynctask.TaskSubmission{
		ToolName:     tool.Name,
		SubmitInput:  submitInput,
		SubmitResult: json.RawMessage(rawContent),
		UserName:     s.req.userName,
	})
	if err != nil {
		zlog.Warnf(s.ctx, "[React] 异步任务 Provider 识别失败，降级旧提醒模式: tool=%s schedulerType=%s err=%v", tool.Name, cfg.AsyncTask.SchedulerType, err)
		return
	}
	task.SchedulerType = managed.SchedulerType
	task.TaskKey = managed.TaskKey
	task.TaskInfo = managed.TaskInfo
	task.ExecutionStatus = managed.ExecutionStatus
	task.NextSyncAt = managed.NextSyncAt
}

// loadPendingReactAsyncTasks 加载 Session 级未完结任务，供新的外层 Run 注入提醒。
// 子 Agent Run 不注入这批 Session 级提醒，避免专家子任务被无关历史异步任务干扰。
// 加载时顺带执行惰性过期，保证长期无人访问的任务不会永久占据上下文。
func loadPendingReactAsyncTasks(ctx *gin.Context, sessionID string) ([]model.ReactAsyncTask, bool, error) {
	if err := model.ExpireReactAsyncTasks(ctx, sessionID); err != nil {
		return nil, false, err
	}
	tasks, err := model.ListPendingReactAsyncTasks(ctx, sessionID, maxReactAsyncTaskReminderItems+1)
	if err != nil {
		return nil, false, err
	}
	if len(tasks) > maxReactAsyncTaskReminderItems {
		return tasks[:maxReactAsyncTaskReminderItems], true, nil
	}
	return tasks, false, nil
}

// renderReactAsyncTaskReminder 把未完结任务渲染为临时模型上下文，而不是永久写入聊天历史。
// 这样提醒只在需要时动态生成，不会随着每次 Run 重复落库造成历史膨胀。
//
//
// 每个字段按 maxReactAsyncTaskRenderRunes 截断，截断内容可通过 get_async_task 回读完整记录。
func renderReactAsyncTaskReminder(tasks []model.ReactAsyncTask, hasMore bool) string {
	if len(tasks) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<async_tasks>\n")
	sb.WriteString("本会话存在未完结的异步任务（以下列表仅包含最近部分未完结任务；结果需通过对应查询工具获取）：\n")
	for _, task := range tasks {
		sb.WriteString(fmt.Sprintf("- toolUseId: %s | 工具: %s | 提交时间: %s\n", task.ToolUseID, task.ToolName, task.CreatedAt.Format("01-02 15:04")))
		if input := truncateAsyncSnapshot(task.SubmitInput, maxReactAsyncTaskRenderRunes); strings.TrimSpace(input) != "" {
			sb.WriteString(fmt.Sprintf("  提交参数: %s\n", input))
		}
		sb.WriteString(fmt.Sprintf("  提交响应: %s\n", truncateAsyncSnapshot(task.SubmitResult, maxReactAsyncTaskRenderRunes)))
		if strings.TrimSpace(task.AsyncHint) != "" {
			sb.WriteString(fmt.Sprintf("  查询提示: %s\n", task.AsyncHint))
		}
	}
	if hasMore {
		sb.WriteString("（另有更早的未完结任务未列出）\n")
	}
	sb.WriteString(strings.Join([]string{
		"处理规则：",
		"1. <async_tasks> 是容量受限的任务快照，只展示最近部分未完结任务；本列表列出的 toolUseId 可以确定可被处理，但未列出不代表不是历史任务。",
		"2. 可以处理当前列表中的 ID，或来自可信历史 <async_tasks> 提醒、历史异步提交记录的 ID；仅当任务与用户当前请求相关时才处理。ID 来源不明确时不得猜测，也不得直接调用 resolve_async_task。",
		"3. 处理可信任务时，若当前上下文缺少完整提交参数或响应，或内容带有截断标记，先调用 get_async_task 获取完整记录。",
		"4. 通过查询工具确认任务已完成或失败后，必须调用 resolve_async_task 标记完结，之后系统不再提醒该任务。",
		"5. 若可信任务的提交响应本身已表明任务并未成功创建或受理，同样调用 resolve_async_task 标记 failed，避免无效任务持续提醒。",
		"6. 任务仍在执行中时如实告知用户当前进度，不要在一次 run 内反复轮询。",
		"7. 系统不会在任务完成时主动通知你，只有用户下次发消息时你才有机会再查询进度。因此结束回复时不要向用户承诺\"完成后我会立即/自动处理\"，应引导用户稍后回来询问（如\"任务已提交，稍后你可以让我查询进度和结果\"）。",
		"</async_tasks>",
	}, "\n"))
	return sb.String()
}

func resolveAsyncTaskToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        metaToolResolveAsyncTask,
		Description: "标记异步任务已完结。只能处理当前 <async_tasks> 列表或可信历史异步提醒、提交记录中的任务；<async_tasks> 仅展示最近部分任务，未列出不代表不是历史任务，但来源不明确时不得猜测或调用本工具。仅当通过查询工具确认任务已完成（done）或失败（failed），或提交结果本身明确失败后调用。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次工具调用的简短描述，用于向用户说明为什么调用该内部工具或正在做什么。"),
				"toolUseId":   stringSchema("异步任务提交时的 toolUseId，来自 async_tasks 提醒。"),
				"status": map[string]interface{}{
					"type":        "string",
					"enum":        []string{reactAsyncResolveStatusDone, reactAsyncResolveStatusFailed},
					"description": "任务完结结果：done 表示已完成，failed 表示已失败。",
				},
			},
			"required":             []string{"description", "toolUseId", "status"},
			"additionalProperties": false,
		},
	}
}

func getAsyncTaskToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        metaToolGetAsyncTask,
		Description: "读取当前执行范围中某个异步任务的完整提交记录。在普通外层 Run 中，只能读取当前 <async_tasks> 列表或可信历史外层异步提醒、提交记录中的任务；<async_tasks> 仅展示最近部分任务，未列出不代表不是历史外层任务，但来源不明确时不得猜测。在 Plan Step Run 中，只能读取当前 StepAttempt 创建的任务。plan_context 或 PlanPublicView 的 wait_request.pending_tool_use_ids 属于对应 Plan Step，外层不得用本工具读取这些 ID，而应直接恢复对应 Plan。仅当上下文内容被截断或需要完整提交参数来构造查询请求时使用。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"description": stringSchema("本次工具调用的简短描述，用于向用户说明为什么调用该内部工具或正在做什么。"),
				"toolUseId":   stringSchema("异步任务提交时的 toolUseId，来自 async_tasks 提醒。"),
			},
			"required":             []string{"description", "toolUseId"},
			"additionalProperties": false,
		},
	}
}

// resolveAsyncTask 处理 resolve_async_task：置 resolved 并从内存 pending 列表移除，本 run 后续不再提醒。
func (s *reactEngineState) resolveAsyncTask(input json.RawMessage) (string, bool, error) {
	var req struct {
		ToolUseID string `json:"toolUseId"`
		Status    string `json:"status"`
	}
	_ = json.Unmarshal(input, &req)
	toolUseID := strings.TrimSpace(req.ToolUseID)
	status := strings.TrimSpace(req.Status)
	if toolUseID == "" {
		return "", true, fmt.Errorf("resolve_async_task toolUseId is required")
	}
	if status != reactAsyncResolveStatusDone && status != reactAsyncResolveStatusFailed {
		return "", true, fmt.Errorf("resolve_async_task status must be done or failed")
	}
	affected, err := model.ResolveReactAsyncTask(s.ctx, s.sessionID, toolUseID, status)
	if err != nil {
		return "", true, err
	}
	if affected == 0 {
		task, err := model.GetReactAsyncTaskByToolUseID(s.ctx, s.sessionID, toolUseID)
		if err != nil {
			return "", true, err
		}
		return s.handleResolvedAsyncTaskState(toolUseID, task)
	}
	s.removePendingAsyncTask(toolUseID)
	data, _ := json.Marshal(map[string]interface{}{
		"toolUseId": toolUseID,
		"status":    status,
		"state":     model.ReactAsyncTaskStateResolved,
		"resolved":  true,
	})
	return string(data), false, nil
}

func (s *reactEngineState) handleResolvedAsyncTaskState(toolUseID string, task *model.ReactAsyncTask) (string, bool, error) {
	if task == nil {
		data, _ := json.Marshal(map[string]interface{}{
			"toolUseId":   toolUseID,
			"found":       false,
			"terminal":    false,
			"retrySameId": false,
			"message":     "no async task matches the provided toolUseId; do not retry the same value; re-read the exact toolUseId from the latest async_tasks reminder",
		})
		return string(data), false, nil
	}

	switch task.State {
	case model.ReactAsyncTaskStateResolved:
		s.removePendingAsyncTask(toolUseID)
		data, _ := json.Marshal(map[string]interface{}{
			"toolUseId":       toolUseID,
			"status":          task.ResolveStatus,
			"state":           task.State,
			"resolved":        true,
			"alreadyResolved": true,
			"message":         "async task already resolved; no retry is required",
		})
		return string(data), false, nil
	case model.ReactAsyncTaskStateExpired:
		s.removePendingAsyncTask(toolUseID)
		data, _ := json.Marshal(map[string]interface{}{
			"toolUseId":       toolUseID,
			"state":           task.State,
			"terminal":        true,
			"alreadyTerminal": true,
			"message":         "async task expired and is no longer pending; no retry is required",
		})
		return string(data), false, nil
	default:
		return "", true, fmt.Errorf("async task is not resolvable in state %s: %s", task.State, toolUseID)
	}
}

// getAsyncTask 按 toolUseId 回读单个异步任务的完整记录。
func (s *reactEngineState) getAsyncTask(input json.RawMessage) (string, bool, error) {
	var req struct {
		ToolUseID string `json:"toolUseId"`
	}
	_ = json.Unmarshal(input, &req)
	toolUseID := strings.TrimSpace(req.ToolUseID)
	if toolUseID == "" {
		return "", true, fmt.Errorf("get_async_task toolUseId is required")
	}
	task, err := model.GetReactAsyncTaskByToolUseID(s.ctx, s.sessionID, toolUseID)
	if err != nil {
		return "", true, err
	}
	if task == nil {
		return "", true, fmt.Errorf("async task not found: %s", toolUseID)
	}

	data, _ := json.Marshal(map[string]interface{}{
		"toolUseId":     task.ToolUseID,
		"toolName":      task.ToolName,
		"state":         task.State,
		"resolveStatus": task.ResolveStatus,
		"submitInput":   task.SubmitInput,
		"submitResult":  task.SubmitResult,
		"asyncHint":     task.AsyncHint,
		"submittedAt":   task.CreatedAt.Format("2006-01-02 15:04"),
		"expireAt":      task.ExpireAt.Format("2006-01-02 15:04"),
	})
	return string(data), false, nil
}

func (s *reactEngineState) removePendingAsyncTask(toolUseID string) {
	remaining := make([]model.ReactAsyncTask, 0, len(s.pendingAsyncTasks))
	for _, task := range s.pendingAsyncTasks {
		if task.ToolUseID != toolUseID {
			remaining = append(remaining, task)
		}
	}
	s.pendingAsyncTasks = remaining
}
