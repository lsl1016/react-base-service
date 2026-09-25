package react

import (
	"encoding/json"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

const historyTimeFormat = "2006-01-02 15:04:05"

type historyUserInputContent struct {
	Content        string                    `json:"content"`
	ControlContext json.RawMessage           `json:"controlContext"`
	LLMContext     json.RawMessage           `json:"llmContext"`
	Attachments    []reactAttachmentSnapshot `json:"attachments"`
	ModelMessage   llm.ChatMessage           `json:"modelMessage"`
}

// ListHistorySessions 返回当前用户可见的 ReAct 历史会话列表，供前端历史侧边栏使用。
func ListHistorySessions(ctx *gin.Context, req params.ReactSessionListReq) (params.ReactSessionListResp, error) {
	userName := helpers.GetUserName(ctx)
	routeValues := req.RouteValues
	if routeValues == nil {
		routeValues = []string{}
	}
	routeValuesBytes, _ := json.Marshal(routeValues)

	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	sessions, total, err := model.ListReactSessionsByCallerRouteUser(ctx, strings.TrimSpace(req.CallerKey), string(routeValuesBytes), userName, normalizeSessionType(req.Type), req.Keyword, page, pageSize)
	if err != nil {
		return params.ReactSessionListResp{}, err
	}

	items := make([]params.ReactSessionItem, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, params.ReactSessionItem{
			SessionID:   session.SessionID,
			CallerKey:   session.CallerKey,
			RouteValues: parseRouteValues(session.RouteValues),
			Type:        session.SessionType,
			Title:       session.Title,
			LastRunID:   session.LastRunID,
			LastMessage: session.LastMessage,
			State:       session.State,
			CreatedAt:   formatHistoryTime(session.CreatedAt),
			UpdatedAt:   formatHistoryTime(session.UpdatedAt),
		})
	}

	return params.ReactSessionListResp{Sessions: items, Total: total, Page: page, PageSize: pageSize}, nil
}

// GetHistoryEvents 将 ReAct 持久化消息还原为接近实时 WebSocket 协议的历史事件流。
// 会话本人可直接访问；查看他人会话时，IPS 登录用户必须在 playground 白名单中。
func GetHistoryEvents(ctx *gin.Context, req params.ReactSessionEventsReq) (params.ReactSessionEventsResp, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return params.ReactSessionEventsResp{}, components.ErrorParamInvalid.Sprintf("sessionId不能为空")
	}

	session, err := model.GetReactSessionBySessionID(ctx, sessionID)
	if err != nil {
		return params.ReactSessionEventsResp{}, err
	}
	if session == nil {
		return params.ReactSessionEventsResp{}, components.ErrorReactSessionNotFound.Sprintf(sessionID)
	}
	if !canViewReactSessionHistory(helpers.GetUserName(ctx), session.UserName) {
		return params.ReactSessionEventsResp{}, components.ErrorReactPlaygroundForbidden
	}

	messages, err := model.GetReactMessagesBySessionIDTimeline(ctx, sessionID)
	if err != nil {
		return params.ReactSessionEventsResp{}, err
	}
	runs, err := model.GetReactRunsBySessionID(ctx, sessionID)
	if err != nil {
		return params.ReactSessionEventsResp{}, err
	}

	builder := newHistoryEventBuilder(sessionID, runs, messages)
	events := builder.Build()
	return params.ReactSessionEventsResp{SessionID: session.SessionID, Title: session.Title, Events: events}, nil
}

// canViewReactSessionHistory 判定历史回放可见性：会话本人始终可见，跨用户查看需登录用户在 playground 白名单内。
func canViewReactSessionHistory(loginUserName, sessionUserName string) bool {
	loginUserName = strings.TrimSpace(loginUserName)
	if loginUserName != "" && loginUserName == strings.TrimSpace(sessionUserName) {
		return true
	}
	return conf.IsReactPlaygroundWhitelisted(loginUserName)
}

type historyEventBuilder struct {
	sessionID                  string
	seq                        int
	runByID                    map[string]model.ReactRun
	runs                       []model.ReactRun
	resultByToolUse            map[string]NormalizedToolResult
	toolMetaByUse              map[string]json.RawMessage
	toolNameByUse              map[string]string
	toolDisplayNameByRunToolID map[string]map[string]string
	messages                   []model.ReactMessage
	currentRunID               string
	runHasMessages             map[string]bool
	closedRunTerminal          map[string]bool
	contextMessages            []llm.ChatMessage
	contextMessageRefs         [][]reactMessageRef
	contextUsedTokensByRunID   map[string]int
	events                     []params.ReactHistoryEvent
}

func newHistoryEventBuilder(sessionID string, runs []model.ReactRun, messages []model.ReactMessage) *historyEventBuilder {
	runByID := make(map[string]model.ReactRun, len(runs))
	toolDisplayNameByRunToolID := make(map[string]map[string]string, len(runs))
	for _, run := range runs {
		runByID[run.RunID] = run
		toolDisplayNameByRunToolID[run.RunID] = buildHistoryToolDisplayNameByID(run.ToolIndexSnapshotJSON)
	}
	builder := &historyEventBuilder{
		sessionID:                  sessionID,
		runByID:                    runByID,
		runs:                       runs,
		resultByToolUse:            make(map[string]NormalizedToolResult),
		toolMetaByUse:              make(map[string]json.RawMessage),
		toolNameByUse:              make(map[string]string),
		toolDisplayNameByRunToolID: toolDisplayNameByRunToolID,
		messages:                   messages,
		runHasMessages:             make(map[string]bool),
		closedRunTerminal:          make(map[string]bool),
		contextUsedTokensByRunID:   make(map[string]int),
	}
	builder.indexToolCalls(messages)
	builder.indexToolResults(messages)
	return builder
}

// Build 按消息落库顺序合成历史事件；seq 为历史接口内的稳定递增序号，不承诺等于实时 WebSocket seq。
// delegate_agent 子 run 的消息按真实时间线混排在父 run 消息之间，事件带 agentPath 供前端分卡片渲染。
func (b *historyEventBuilder) Build() []params.ReactHistoryEvent {
	for _, message := range b.messages {
		if message.RunID != "" {
			b.runHasMessages[message.RunID] = true
		}
		if b.currentRunID != "" && message.RunID != "" && message.RunID != b.currentRunID {
			// 只有进入当前 run 的子 run 时才不提前收敛父 run：委派结束后父 run 还会继续产生消息，
			// 父终态事件必须落在其全部消息之后；离开子 run（回父/切兄弟子 run）照常收敛子 run。
			next, nextOK := b.runByID[message.RunID]
			if !(nextOK && next.ParentRunID == b.currentRunID) {
				b.appendRunTerminal(b.currentRunID)
			}
		}
		if message.RunID != "" {
			b.currentRunID = message.RunID
		}
		b.trackContextUsage(message)
		b.appendMessageEvents(message)
	}
	if b.currentRunID != "" {
		b.appendRunTerminal(b.currentRunID)
	}
	// 兜底收敛：父 run 在子 run 之后中断（error/cancelled）时，切换逻辑不会触发其终态，此处统一补齐。
	for _, run := range b.runs {
		if b.runHasMessages[run.RunID] && !b.closedRunTerminal[run.RunID] {
			b.appendRunTerminal(run.RunID)
		}
	}
	return b.events
}

func (b *historyEventBuilder) trackContextUsage(message model.ReactMessage) {
	// 子 run 消息不进入外层 run 的 LLM 历史（引擎侧已按外层 run 过滤），估算口径保持一致。
	if run, ok := b.runByID[message.RunID]; ok && run.ParentRunID != "" {
		return
	}
	chatMessage, ok := reactMessageToChatMessage(message)
	if !ok || strings.TrimSpace(message.RunID) == "" {
		return
	}
	ref := reactMessageRefFromStored(message)
	switch message.MessageType {
	case model.ReactMessageTypeAssistant:
		b.appendContextMessage(chatMessage, []reactMessageRef{ref})
		b.refreshContextUsedTokens(message.RunID)
	case model.ReactMessageTypeAssistantPartial:
		return
	case model.ReactMessageTypeCompactSummary:
		b.dropCoveredContextMessages(parseCompactCoveredThrough(message.ContentJSON))
		b.appendContextMessage(chatMessage, []reactMessageRef{ref})
		b.refreshContextUsedTokens(message.RunID)
	default:
		b.appendContextMessage(chatMessage, []reactMessageRef{ref})
		b.refreshContextUsedTokens(message.RunID)
	}
}

func (b *historyEventBuilder) refreshContextUsedTokens(runID string) {
	tools := internalMetaToolDefinitions()
	b.contextUsedTokensByRunID[runID] = estimateToolDefinitionsTokens(tools) + estimateMessagesTokens(b.contextMessages)
}

func (b *historyEventBuilder) appendContextMessage(message llm.ChatMessage, refs []reactMessageRef) {
	b.contextMessages = append(b.contextMessages, message)
	b.contextMessageRefs = append(b.contextMessageRefs, refs)
}

func (b *historyEventBuilder) dropCoveredContextMessages(coveredRefs []reactMessageRef) {
	if len(coveredRefs) == 0 || len(b.contextMessages) == 0 {
		return
	}
	covered := make(map[string]map[int]bool)
	for _, ref := range coveredRefs {
		if ref.RunID == "" || ref.Seq <= 0 {
			continue
		}
		if covered[ref.RunID] == nil {
			covered[ref.RunID] = make(map[int]bool)
		}
		for seq := 1; seq <= ref.Seq; seq++ {
			covered[ref.RunID][seq] = true
		}
	}
	messages := make([]llm.ChatMessage, 0, len(b.contextMessages))
	refs := make([][]reactMessageRef, 0, len(b.contextMessageRefs))
	for i, message := range b.contextMessages {
		refGroup := refsAt(b.contextMessageRefs, i)
		if contextRefCovered(refGroup, covered) {
			continue
		}
		messages = append(messages, message)
		refs = append(refs, refGroup)
	}
	b.contextMessages = messages
	b.contextMessageRefs = refs
}

func contextRefCovered(refs []reactMessageRef, covered map[string]map[int]bool) bool {
	for _, ref := range refs {
		if covered[ref.RunID][ref.Seq] {
			return true
		}
	}
	return false
}

func (b *historyEventBuilder) appendMessageEvents(message model.ReactMessage) {
	switch message.MessageType {
	case model.ReactMessageTypeUserInput:
		b.appendRunEvent(message)
	case model.ReactMessageTypeAssistant, model.ReactMessageTypeAssistantPartial:
		b.appendAssistantEvents(message)
	case model.ReactMessageTypeToolResult:
		b.appendToolResultEvents(message)
	case model.ReactMessageTypeCompactSummary:
		b.appendCompactEvent(message)
	case model.ReactMessageTypeNotice:
		// 运行时通知邮箱（Q1）：react_notice 消息回放为 notice_drained 事件（与实时吸收
		// 事件同词汇），payload 携带信封正文供前端渲染系统卡片。
		b.appendNoticeEvent(message)
	}
}

// appendNoticeEvent 把一条 react_notice 消息还原为 notice_drained 历史事件。
func (b *historyEventBuilder) appendNoticeEvent(message model.ReactMessage) {
	var content struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(message.ContentJSON), &content)
	if strings.TrimSpace(content.Content) == "" {
		return
	}
	b.appendStepEvent(message.StepIndex, EventNoticeDrained, message.RunID, params.ReactNoticeDrainedPayload{
		MessageID: message.MessageID,
		Count:     1,
		Content:   content.Content,
	}, message.CreatedAt)
}

func (b *historyEventBuilder) appendRunEvent(message model.ReactMessage) {
	var content historyUserInputContent
	_ = json.Unmarshal([]byte(message.ContentJSON), &content)
	run := b.runByID[message.RunID]
	payload := params.ReactRunPayload{
		CallerKey:      run.CallerKey,
		RouteValues:    parseRouteValues(run.RouteValues),
		Type:           model.ReactSessionTypeChat,
		UserPrompt:     content.Content,
		Attachments:    reactAttachmentRefsFromSnapshots(content.Attachments),
		ControlContext: normalizeRawJSON(content.ControlContext),
		LLMContext:     normalizeRawJSON(content.LLMContext),
		ModelKey:       run.ModelKey,
		ModelVersion:   run.ModelVersion,
		MaxSteps:       run.MaxSteps,
	}
	b.appendEvent(EventRun, message.RunID, payload, message.CreatedAt)
}

func (b *historyEventBuilder) appendAssistantEvents(message model.ReactMessage) {
	chatMessage := parseHistoryChatMessage(message.ContentJSON)
	reasoningContent := extractReasoningContent(chatMessage)
	content := extractAssistantContent(chatMessage)
	run := b.runByID[message.RunID]
	if reasoningContent != "" {
		b.appendStepEvent(message.StepIndex, EventThoughtStart, message.RunID, params.ReactThoughtStartPayload{}, message.CreatedAt)
		b.appendStepEvent(message.StepIndex, EventThoughtDelta, message.RunID, params.ReactThoughtDeltaPayload{ContentDelta: reasoningContent}, message.CreatedAt)
		b.appendStepEvent(message.StepIndex, EventThoughtEnd, message.RunID, params.ReactThoughtEndPayload{
			Content:           reasoningContent,
			InputTokens:       run.TotalInputTokens,
			OutputTokens:      run.TotalOutputTokens,
			CacheReadTokens:   run.CacheReadTokens,
			CacheCreateTokens: run.CacheCreateTokens,
			ContextUsedTokens: b.contextUsedTokens(run),
			MaxContextTokens:  reactMaxContextTokens(run.ModelKey),
		}, message.CreatedAt)
	}
	if content != "" {
		b.appendStepEvent(message.StepIndex, EventContentStart, message.RunID, params.ReactContentStartPayload{}, message.CreatedAt)
		b.appendStepEvent(message.StepIndex, EventContentDelta, message.RunID, params.ReactContentDeltaPayload{ContentDelta: content}, message.CreatedAt)
		b.appendStepEvent(message.StepIndex, EventContentEnd, message.RunID, params.ReactContentEndPayload{
			Content:           content,
			InputTokens:       run.TotalInputTokens,
			OutputTokens:      run.TotalOutputTokens,
			CacheReadTokens:   run.CacheReadTokens,
			CacheCreateTokens: run.CacheCreateTokens,
			ContextUsedTokens: b.contextUsedTokens(run),
			MaxContextTokens:  reactMaxContextTokens(run.ModelKey),
		}, message.CreatedAt)
	}
	for _, part := range chatMessage.Parts {
		if part.Type != "tool_use" {
			continue
		}
		b.appendToolUseStartEvent(message, part)
	}
}

func (b *historyEventBuilder) appendToolUseStartEvent(message model.ReactMessage, part llm.ContentPart) {
	eventInput := b.buildToolUseStartInput(message.RunID, part)
	executedBy := b.inferExecutedBy(part)
	if executedBy == executedByClient {
		b.appendStepEvent(message.StepIndex, EventClientToolUseStart, message.RunID, params.ReactClientToolUseStartPayload{
			ToolUseID:    part.ID,
			ToolName:     eventInput.ToolName,
			ToolInput:    eventInput.ToolInput,
			Description:  eventInput.Description,
			FrontendHint: eventInput.ToolName,
			Status:       "waiting",
		}, message.CreatedAt)
		return
	}
	// ask_question 的初始态是等待用户作答；有配对 end 事件时会被覆盖为终态，无配对时保留 waiting 语义。
	status := "running"
	if part.Name == metaToolAskQuestion {
		status = "waiting"
	}
	b.appendStepEvent(message.StepIndex, EventToolUseStart, message.RunID, params.ReactToolUseStartPayload{
		ToolUseID:   part.ID,
		ToolName:    eventInput.ToolName,
		ToolInput:   eventInput.ToolInput,
		Description: eventInput.Description,
		ExecutedBy:  executedBy,
		Status:      status,
	}, message.CreatedAt)
}

type historyToolUseStartInput struct {
	ToolName    string
	ToolInput   json.RawMessage
	Description string
}

func (b *historyEventBuilder) buildToolUseStartInput(runID string, part llm.ContentPart) historyToolUseStartInput {
	input := historyToolUseStartInput{
		ToolName:  part.Name,
		ToolInput: normalizeRawJSON(part.Input),
	}
	if part.Name == metaToolExecuteTool {
		var req executeToolInput
		if err := json.Unmarshal(part.Input, &req); err != nil {
			return input
		}
		input.Description = strings.TrimSpace(req.Description)
		if toolName := b.resolveHistoryExecuteToolName(runID, req); toolName != "" {
			input.ToolName = toolName
		}

		toolInput := req.Input
		if len(toolInput) == 0 {
			toolInput = req.Arguments
		}
		if len(toolInput) == 0 {
			toolInput = flattenedExecuteToolInput(part.Input)
		}
		if normalizedInput, ok := normalizeExecuteToolInput(toolInput); ok {
			input.ToolInput = normalizeRawJSON(normalizedInput)
		}
		return input
	}
	if isInternalMetaTool(part.Name) {
		input.Description = extractToolDescription(part.Input)
		input.ToolInput = normalizeRawJSON(stripToolDescriptionInput(part.Input))
	}
	// displayFiles 的落库 input 是模型传的 artifactIds（前端渲染不了）；实时态发出去的是
	// 后端铸造的 {"artifacts":[{type,name,uri}]}（同时落在 toolMeta），回放时用它替换，保证直播/回放同形。
	if part.Name == metaToolDisplayFiles {
		if meta, ok := b.toolMetaByUse[part.ID]; ok && len(meta) > 0 {
			input.ToolInput = normalizeRawJSON(meta)
		}
	}
	return input
}

func buildHistoryToolDisplayNameByID(snapshotJSON string) map[string]string {
	var items []reactToolIndexItem
	if err := json.Unmarshal([]byte(snapshotJSON), &items); err != nil || len(items) == 0 {
		return nil
	}
	displayNameByID := make(map[string]string, len(items))
	for _, item := range items {
		toolID := strings.TrimSpace(item.ToolID)
		name := strings.TrimSpace(item.Name)
		if toolID == "" || name == "" {
			continue
		}
		displayNameByID[toolID] = name
	}
	return displayNameByID
}

func (b *historyEventBuilder) resolveHistoryExecuteToolName(runID string, req executeToolInput) string {
	if displayNameByID := b.toolDisplayNameByRunToolID[runID]; len(displayNameByID) > 0 {
		if toolName := displayNameByID[strings.TrimSpace(req.ToolID)]; toolName != "" {
			return toolName
		}
		if toolName := displayNameByID[strings.TrimSpace(req.CallName)]; toolName != "" {
			return toolName
		}
	}
	return firstNonEmpty(req.Name, req.CallName, req.ToolID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (b *historyEventBuilder) appendToolResultEvents(message model.ReactMessage) {
	chatMessage := parseHistoryChatMessage(message.ContentJSON)
	toolMeta := parseHistoryToolMeta(message.ContentJSON)
	for _, part := range chatMessage.Parts {
		if part.Type != "tool_result" {
			continue
		}
		result := parseNormalizedToolResult(part)
		if result.ToolUseID == "" {
			result.ToolUseID = part.ToolUseID
		}
		executedBy := normalizeExecutedBy(result.ExecutedBy)
		if executedBy == executedByClient {
			// client 工具实时态由前端自发 client_tool_use_end 配对 client_tool_use_start；
			// 历史态前端不发消息，需后端补出同形状事件，保证直播/回放共用一套解析逻辑。
			b.appendStepEvent(message.StepIndex, EventClientToolUseEnd, message.RunID, params.ReactClientToolUseEndPayload{
				ToolOutputs: []params.ReactClientToolOutput{{
					ToolUseID: result.ToolUseID,
					Content:   result.Content,
					Meta:      toolMeta[result.ToolUseID],
					IsError:   result.IsError,
					Status:    normalizeToolStatus(result.Status, result.IsError),
				}},
			}, message.CreatedAt)
		} else {
			b.appendStepEvent(message.StepIndex, EventToolUseEnd, message.RunID, params.ReactToolUseEndPayload{
				ToolUseID:    result.ToolUseID,
				Content:      result.Content,
				ResultRef:    result.ResultRef,
				Truncated:    result.Truncated,
				OmittedChars: result.OmittedChars,
				IsError:      result.IsError,
				ExecutedBy:   executedBy,
				Status:       normalizeToolStatus(result.Status, result.IsError),
				DurationMs:   0,
				Meta:         toolMeta[result.ToolUseID],
			}, message.CreatedAt)
		}
		toolName := b.toolNameByUse[result.ToolUseID]
		if strings.TrimSpace(toolName) == metaToolTodoWrite {
			if todoState, ok := todoStateFromToolResultContent(part.Content, message.CreatedAt.UnixMilli()); ok {
				stateJSON := encodeReactTodoState(todoState)
				b.appendStepEvent(message.StepIndex, EventTodoUpdate, message.RunID, params.ReactTodoUpdatePayload{TodoState: json.RawMessage(stateJSON)}, message.CreatedAt)
			}
		}
	}
}

func parseHistoryToolMeta(contentJSON string) map[string]json.RawMessage {
	var stored struct {
		ToolMeta map[string]json.RawMessage `json:"toolMeta"`
	}
	if err := json.Unmarshal([]byte(contentJSON), &stored); err != nil {
		return nil
	}
	return stored.ToolMeta
}

func (b *historyEventBuilder) appendCompactEvent(message model.ReactMessage) {
	var content struct {
		Summary string `json:"summary"`
	}
	_ = json.Unmarshal([]byte(message.ContentJSON), &content)
	if strings.TrimSpace(content.Summary) == "" {
		return
	}
	b.appendStepEvent(message.StepIndex, EventCompactEnd, message.RunID, params.ReactCompactEndPayload{Summary: content.Summary}, message.CreatedAt)
}

func (b *historyEventBuilder) appendRunTerminal(runID string) {
	if b.closedRunTerminal[runID] {
		return
	}
	run, ok := b.runByID[runID]
	if !ok {
		return
	}
	b.closedRunTerminal[runID] = true
	switch run.State {
	case model.ReactRunStateFinished:
		b.appendEvent(EventDone, runID, params.ReactDonePayload{
			InputTokens:       run.TotalInputTokens,
			OutputTokens:      run.TotalOutputTokens,
			CacheReadTokens:   run.CacheReadTokens,
			CacheCreateTokens: run.CacheCreateTokens,
			ContextUsedTokens: b.contextUsedTokens(run),
			MaxContextTokens:  reactMaxContextTokens(run.ModelKey),
		}, run.UpdatedAt)
	case model.ReactRunStateCancelled:
		b.appendEvent(EventCancelled, runID, params.ReactCancelledPayload{
			OK:                true,
			Reason:            "本次运行已被用户取消",
			ContextUsedTokens: b.contextUsedTokens(run),
			MaxContextTokens:  reactMaxContextTokens(run.ModelKey),
		}, run.UpdatedAt)
	case model.ReactRunStateTimeout:
		// 超时终态回放：与实时事件（EventTimeout）保持同一 payload 语义。
		b.appendEvent(EventTimeout, runID, params.ReactCancelledPayload{
			OK:                false,
			Reason:            "本次运行超出时间上限被终止",
			ContextUsedTokens: b.contextUsedTokens(run),
			MaxContextTokens:  reactMaxContextTokens(run.ModelKey),
		}, run.UpdatedAt)
	case model.ReactRunStateError, model.ReactRunStateExpired:
		errMsg := strings.TrimSpace(run.ErrorMessage)
		if errMsg == "" {
			errMsg = run.State
		}
		b.appendEvent(EventError, runID, params.ReactErrorPayload{
			ErrNo:             components.ErrorReactRunFailed.ErrNo,
			ErrMsg:            errMsg,
			ContextUsedTokens: b.contextUsedTokens(run),
			MaxContextTokens:  reactMaxContextTokens(run.ModelKey),
		}, run.UpdatedAt)
	}
}

func (b *historyEventBuilder) contextUsedTokens(run model.ReactRun) int {
	used := run.LastInputTokens + run.LastOutputTokens
	if used > 0 {
		return used
	}
	return b.contextUsedTokensByRunID[run.RunID]
}

func (b *historyEventBuilder) appendStepEvent(stepIndex int, eventType, runID string, payload any, createdAt time.Time) {
	b.appendEventWithStep(&stepIndex, eventType, runID, payload, createdAt)
}

func (b *historyEventBuilder) appendEvent(eventType, runID string, payload any, createdAt time.Time) {
	b.appendEventWithStep(nil, eventType, runID, payload, createdAt)
}

func (b *historyEventBuilder) appendEventWithStep(stepIndex *int, eventType, runID string, payload any, createdAt time.Time) {
	b.seq++
	b.events = append(b.events, params.ReactHistoryEvent{
		Type:      eventType,
		Seq:       b.seq,
		RunID:     runID,
		SessionID: b.sessionID,
		StepIndex: stepIndex,
		AgentPath: b.agentPathForRun(runID),
		Payload:   payload,
		CreatedAt: formatHistoryTime(createdAt),
	})
}

// agentPathForRun 从 run 行读取事件归属路径：外层 run 为空（前端按 main 渲染），子 run 形如 main/ops-agent。
func (b *historyEventBuilder) agentPathForRun(runID string) string {
	if run, ok := b.runByID[runID]; ok {
		return run.AgentPath
	}
	return ""
}

func (b *historyEventBuilder) indexToolCalls(messages []model.ReactMessage) {
	for _, message := range messages {
		if message.MessageType != model.ReactMessageTypeAssistant && message.MessageType != model.ReactMessageTypeAssistantPartial {
			continue
		}
		chatMessage := parseHistoryChatMessage(message.ContentJSON)
		for _, part := range chatMessage.Parts {
			if part.Type != "tool_use" || strings.TrimSpace(part.ID) == "" {
				continue
			}
			b.toolNameByUse[part.ID] = part.Name
		}
	}
}

func (b *historyEventBuilder) indexToolResults(messages []model.ReactMessage) {
	for _, message := range messages {
		if message.MessageType != model.ReactMessageTypeToolResult {
			continue
		}
		chatMessage := parseHistoryChatMessage(message.ContentJSON)
		for _, part := range chatMessage.Parts {
			if part.Type != "tool_result" {
				continue
			}
			result := parseNormalizedToolResult(part)
			if result.ToolUseID != "" {
				b.resultByToolUse[result.ToolUseID] = result
			}
		}
		// 同步索引 toolMeta：displayFiles 回放时直接用落库的铸造结果替换 tool_use_start 的 input。
		for toolUseID, meta := range parseHistoryToolMeta(message.ContentJSON) {
			b.toolMetaByUse[toolUseID] = meta
		}
	}
}

func (b *historyEventBuilder) inferExecutedBy(part llm.ContentPart) string {
	if result, ok := b.resultByToolUse[part.ID]; ok && strings.TrimSpace(result.ExecutedBy) != "" {
		return normalizeExecutedBy(result.ExecutedBy)
	}
	if isInternalMetaTool(part.Name) {
		return executedByInternal
	}
	return executedByServer
}

func parseChatMessage(contentJSON string) llm.ChatMessage {
	var message llm.ChatMessage
	_ = json.Unmarshal([]byte(contentJSON), &message)
	return message
}

func parseHistoryChatMessage(contentJSON string) llm.ChatMessage {
	if message, ok := parseStoredModelMessage(contentJSON); ok {
		return message
	}
	return parseChatMessage(contentJSON)
}

func parseNormalizedToolResult(part llm.ContentPart) NormalizedToolResult {
	var result NormalizedToolResult
	if err := json.Unmarshal([]byte(part.Content), &result); err != nil {
		result = NormalizedToolResult{
			ToolUseID:  part.ToolUseID,
			Content:    strings.TrimSpace(part.Content),
			IsError:    part.IsError,
			ExecutedBy: "unknown",
			Status:     normalizeToolStatus("", part.IsError),
		}
	}
	return result
}

func extractReasoningContent(message llm.ChatMessage) string {
	var parts []string
	for _, part := range message.Parts {
		if part.Type == "thinking" && strings.TrimSpace(part.Thinking) != "" {
			parts = append(parts, part.Thinking)
		}
	}
	return strings.Join(parts, "")
}

func extractAssistantContent(message llm.ChatMessage) string {
	if strings.TrimSpace(message.Content) != "" {
		return message.Content
	}
	var parts []string
	for _, part := range message.Parts {
		if part.Type == "text" && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "")
}

func normalizeExecutedBy(executedBy string) string {
	switch strings.TrimSpace(executedBy) {
	case executedByClient:
		return executedByClient
	case executedByInternal:
		return executedByInternal
	case executedByServer:
		return executedByServer
	default:
		return "unknown"
	}
}

func normalizeToolStatus(status string, isError bool) string {
	status = strings.TrimSpace(status)
	if status != "" {
		return status
	}
	if isError {
		return toolExecutionStatusError
	}
	return toolExecutionStatusSuccess
}

func normalizeRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage(`{}`)
	}
	return raw
}

func parseRouteValues(routeValuesJSON string) []string {
	var routeValues []string
	if err := json.Unmarshal([]byte(routeValuesJSON), &routeValues); err != nil {
		return []string{}
	}
	if routeValues == nil {
		return []string{}
	}
	return routeValues
}

func formatHistoryTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(historyTimeFormat)
}


// GetRunHistoryEvents 将单个 ReactRun 的持久化消息还原为事件。
// Plan Runtime 用它懒加载 StepAttempt 详情；不会混入同 Session 的父/兄弟 Run。
func GetRunHistoryEvents(ctx *gin.Context, runID string) ([]params.ReactHistoryEvent, error) {
	run, err := model.GetReactRunByRunID(ctx, strings.TrimSpace(runID))
	if err != nil {
		return nil, err
	}
	if run == nil {
		return []params.ReactHistoryEvent{}, nil
	}
	messages, err := model.GetReactMessagesByRunID(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	builder := newHistoryEventBuilder(run.SessionID, []model.ReactRun{*run}, messages)
	return builder.Build(), nil
}
