package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	react "react-base-service/service/react"
)

// imAdapter 是一次 IM 会话 run 的投影适配器：把 ReAct 运行时的事件流与 HITL 通道
// 适配到 IM 消息形态。一个 adapter 对应一次 RunWithClientReaderContext 调用。
//
//   - writer（EventWriter）：折叠外层 run 最后一条 assistant 内容；ask_question /
//     tool_confirm_request 推成 IM 消息并登记 pending；done/error 终态推送并写消息绑定；
//     cancelled 不推送（与 workflow 通知决策一致：停机收敛非业务结果）；
//   - reader（ClientMessageReader）：阻塞等待 IM 回复，按 pending 的 toolUseId 组装
//     tool_use_answer / tool_confirm_answer；run 结束时以 WSReadEndError 终止读取。
type imAdapter struct {
	gw *service

	// chat 定位与回复锚点
	channelID string
	chatID    string
	// replyAnchorMessageID 是本轮触发消息的平台 ID：终态以回复它的方式投递（线程感）。
	replyAnchorMessageID string

	sessionID string
	userName  string
	// appKey/routeValues 是会话五元组快照的路由部分（跨端投递的回放链接参数）。
	appKey      string
	routeValues string

	mu sync.Mutex
	// lastAnswer 是外层 run 最近一条 assistant 全文（多轮/排队续跑时取最后一条）。
	lastAnswer string
	// lastError 缓存最近一条 error 事件的负载（终态推送文案用）。
	lastError *params.ReactErrorPayload
	// terminalPushed 标记终态已推送（run 错误返回时的兜底推送去重）。
	terminalPushed bool
	// pending 是当前等待 IM 回复的交互（ask_question / 危险确认）；同一时刻至多一个。
	pending *pendingInteraction
	// runID 从事件流捕获，组装上行消息与绑定记录用。
	runID string
	// placeholderMessageID 是本轮的占位消息 ID（渠道支持编辑时）；终态原位编辑替换。
	placeholderMessageID string

	replies chan string
	done    chan struct{}
	cancel  context.CancelFunc
	closeOnce sync.Once
	// recordBinding 写消息绑定（默认落库；测试注入替身）。
	recordBinding func(row *model.ChannelMessage) error
}

// defaultRecordBinding 默认绑定写入：经 headless gin ctx 落库。
func defaultRecordBinding(row *model.ChannelMessage) error {
	return model.RecordChannelMessage(newGatewayGinContext(), row)
}

// pendingInteraction 登记一次等待中的 HITL 交互。
type pendingInteraction struct {
	// ToolUseID 是等待 tool_use_answer/tool_confirm_answer 的工具调用 ID。
	ToolUseID string
	// Kind: "ask"（ask_question）| "confirm"（危险操作确认）。
	Kind string
	// Questions 是 ask_question 的原始输入（M1 线性作答：文本回选项序号/原文，或自由文本）。
	Questions []askQuestionItemView
}

// askQuestionItemView 是 ask_question 工具输入的展示视图（字段名与工具入参 JSON 对齐）。
type askQuestionItemView struct {
	ID       string   `json:"id"`
	Prompt   string   `json:"prompt"`
	Options  []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"options"`
}

func newIMAdapter(gw *service, channelID, chatID, replyAnchorMessageID, sessionID, userName string, cancel context.CancelFunc) *imAdapter {
	return &imAdapter{
		gw:                    gw,
		channelID:             channelID,
		chatID:                chatID,
		replyAnchorMessageID:  replyAnchorMessageID,
		sessionID:             sessionID,
		userName:              userName,
		replies:               make(chan string, 4),
		done:                  make(chan struct{}),
		cancel:                cancel,
		recordBinding:         defaultRecordBinding,
	}
}

// finish 在 run 返回后调用：解除 chat 挂载、唤醒阻塞的 reader。
func (a *imAdapter) finish() {
	a.closeOnce.Do(func() { close(a.done) })
}

// takePending 返回当前等待中的交互（nil 表示无）。
func (a *imAdapter) takePending() *pendingInteraction {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending
}

// consumePending 取走并清空当前等待中的交互（作答组装时消费）。
func (a *imAdapter) consumePending() *pendingInteraction {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.pending
	a.pending = nil
	return p
}

// cancelRun 取消本 run 的执行 context（用户在 IM 发「取消」/「/cancel」）。
func (a *imAdapter) cancelRun() {
	if a.cancel != nil {
		a.cancel()
	}
}

// pushedTerminal 报告终态是否已推送（run 返回错误时的兜底推送去重）。
func (a *imAdapter) pushedTerminal() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.terminalPushed
}

// setPlaceholder 记录占位消息 ID（渠道支持编辑时，终态原位替换为结果）。
func (a *imAdapter) setPlaceholder(messageID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.placeholderMessageID = messageID
}

// setReplayInfo 记录会话路由快照（跨端投递消息的回放链接参数）。
func (a *imAdapter) setReplayInfo(appKey, routeValues string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.appKey = appKey
	a.routeValues = routeValues
}

// editPlaceholder 用新文本原位编辑占位消息；无占位或编辑失败返回 false。
func (a *imAdapter) editPlaceholder(text string) bool {
	a.mu.Lock()
	placeholder := a.placeholderMessageID
	a.mu.Unlock()
	if placeholder == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.gw.editText(ctx, a.channelID, a.chatID, placeholder, text) == nil
}

// notify 过程性通知：优先原位编辑占位消息（不产生新消息噪音），失败回退推送。
func (a *imAdapter) notify(text string) {
	if a.editPlaceholder(text) {
		return
	}
	_, _ = a.pushText(text)
}

// notifyIfPlaceholder 仅在有占位消息时原位编辑收尾（取消等原本静默的收敛，
// 无占位的渠道保持不推送，避免改变 M1 行为）。
func (a *imAdapter) notifyIfPlaceholder(text string) {
	a.editPlaceholder(text)
}

// deliverReply 把 IM 侧回复文本投给 reader；无等待者时进入有界缓冲（丢弃最旧）。
func (a *imAdapter) deliverReply(text string) bool {
	select {
	case a.replies <- text:
		return true
	default:
		// 缓冲满：挤掉最旧一条再入队（异常洪峰兜底，等待方由 run 超时收敛）。
		select {
		case <-a.replies:
		default:
		}
		a.replies <- text
		return true
	}
}

// ---------------------------------------------------------------------------
// EventWriter：运行时事件 → IM
// ---------------------------------------------------------------------------

func (a *imAdapter) writer() react.EventWriter {
	return func(event params.ReactEvent) error {
		a.handleEvent(event)
		return nil
	}
}

func (a *imAdapter) handleEvent(event params.ReactEvent) {
	if event.RunID != "" {
		a.mu.Lock()
		a.runID = event.RunID
		a.mu.Unlock()
	}
	switch event.Type {
	case react.EventContentEnd:
		// 只折叠外层 run 的 assistant 内容；子代理（agentPath 非空）的输出不上墙。
		if event.AgentPath != "" {
			return
		}
		if payload, ok := event.Payload.(params.ReactContentEndPayload); ok && strings.TrimSpace(payload.Content) != "" {
			a.mu.Lock()
			a.lastAnswer = payload.Content
			a.mu.Unlock()
		}
	case react.EventToolUseStart:
		payload, ok := event.Payload.(params.ReactToolUseStartPayload)
		if !ok || payload.ToolName != "ask_question" || payload.Status != toolStatusWaiting() {
			return
		}
		a.onAskQuestion(payload)
	case react.EventToolConfirmRequest:
		payload, ok := event.Payload.(params.ReactToolConfirmRequestPayload)
		if !ok {
			return
		}
		a.onToolConfirm(payload)
	case react.EventDone:
		a.pushTerminal(false)
	case react.EventError:
		if payload, ok := event.Payload.(params.ReactErrorPayload); ok {
			a.mu.Lock()
			normalized := payload
			a.lastError = &normalized
			a.mu.Unlock()
		}
		a.pushTerminal(true)
	case react.EventCancelled:
		// 停机收敛不推送（对齐 workflow notify 决策），仅日志。
	}
}

// toolStatusWaiting 与引擎 toolExecutionStatusWaiting 同值（避免导出依赖，字面量对齐）。
func toolStatusWaiting() string { return "waiting" }

// onAskQuestion 把 ask_question 的问题清单推成 IM 消息并登记等待。
func (a *imAdapter) onAskQuestion(payload params.ReactToolUseStartPayload) {
	var parsed struct {
		Questions []askQuestionItemView `json:"questions"`
	}
	_ = json.Unmarshal(payload.ToolInput, &parsed)

	var sb strings.Builder
	sb.WriteString("❓ 需要你补充信息：\n")
	for _, q := range parsed.Questions {
		sb.WriteString("\n" + q.Prompt + "\n")
		for i, opt := range q.Options {
			sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, opt.Label))
		}
	}
	sb.WriteString("\n直接回复消息作答：回复选项序号（多选用逗号分隔），或直接输入文本；回复「跳过」不答。")
	timeout := conf.CustomConf.LLM.Gateway.GatewayHitlTimeout()
	if timeout > 0 {
		sb.WriteString(fmt.Sprintf("（%d 分钟内未回复将跳过该问题）", timeout/60))
	}

	a.mu.Lock()
	a.pending = &pendingInteraction{ToolUseID: payload.ToolUseID, Kind: "ask", Questions: parsed.Questions}
	a.mu.Unlock()
	// 渠道支持按钮交互（Telegram inline keyboard）时题面带按钮推送；
	// 点击回流为 IsCallback 的 InboundMessage，与文本作答同路组装（见 handleInbound 分支 0）。
	if rows := askQuestionButtonRows(payload.ToolUseID, parsed.Questions); len(rows) > 0 && a.tryPushInteractive(sb.String(), rows) {
		return
	}
	a.pushText(sb.String())
}

// askQuestionButtonRows 为首个问题构造按钮行（M1 线性作答只支持第一问的选项）；
// 每个选项一枚按钮（值=序号文本），末尾附「跳过」。无选项/选项过多/callback_data
// 超长时返回 nil，退纯文本作答。
func askQuestionButtonRows(toolUseID string, questions []askQuestionItemView) [][]InteractiveButton {
	if len(questions) == 0 {
		return nil
	}
	q := questions[0]
	if len(q.Options) == 0 || len(q.Options) > 8 {
		return nil
	}
	rows := make([][]InteractiveButton, 0, len(q.Options)+1)
	for i, opt := range q.Options {
		data := encodeHitlCallbackData(toolUseID, strconv.Itoa(i+1))
		if len(data) > telegramCallbackDataLimit {
			return nil
		}
		rows = append(rows, []InteractiveButton{{
			Text:         fmt.Sprintf("%d. %s", i+1, truncateForButtonLabel(opt.Label)),
			CallbackData: data,
		}})
	}
	if data := encodeHitlCallbackData(toolUseID, "跳过"); len(data) <= telegramCallbackDataLimit {
		rows = append(rows, []InteractiveButton{{Text: "跳过", CallbackData: data}})
	}
	return rows
}

// truncateForButtonLabel 按钮文案截断（Telegram 按钮无硬限制，保显示整洁）。
func truncateForButtonLabel(label string) string {
	if runes := []rune(label); len(runes) > 32 {
		return string(runes[:32]) + "…"
	}
	return label
}

// tryPushInteractive 渠道实现 InteractivePusher 时推送带按钮的消息；成功返回 true。
func (a *imAdapter) tryPushInteractive(text string, rows [][]InteractiveButton) bool {
	ch := a.gw.manager.Get(a.channelID)
	if ch == nil {
		return false
	}
	pusher, ok := ch.(InteractivePusher)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := pusher.PushInteractive(ctx, PushAddr{
		ChannelID:      a.channelID,
		ChatID:         a.chatID,
		ReplyMessageID: a.replyAnchorMessageID,
	}, InteractivePayload{Text: text, Rows: rows})
	return err == nil && id != ""
}

// onToolConfirm 把危险操作确认推成 IM 消息并登记等待。
func (a *imAdapter) onToolConfirm(payload params.ReactToolConfirmRequestPayload) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⚠️ 工具 %q 请求确认执行：\n", payload.ToolName))
	if len(payload.ToolInput) > 0 {
		input := string(payload.ToolInput)
		if trimmed := []rune(input); len(trimmed) > 400 {
			input = string(trimmed[:400]) + "…"
		}
		sb.WriteString("```\n" + input + "\n```\n")
	}
	sb.WriteString("\n回复「允许」继续执行，其它内容视为拒绝。")

	a.mu.Lock()
	a.pending = &pendingInteraction{ToolUseID: payload.ToolUseID, Kind: "confirm"}
	a.mu.Unlock()
	// 危险确认带「允许/拒绝」按钮（值走 isApprovalText 白名单语义，与文本作答同路）。
	allowData := encodeHitlCallbackData(payload.ToolUseID, "允许")
	denyData := encodeHitlCallbackData(payload.ToolUseID, "拒绝")
	if len(allowData) <= telegramCallbackDataLimit && len(denyData) <= telegramCallbackDataLimit {
		rows := [][]InteractiveButton{{ 
			{Text: "✅ 允许", CallbackData: allowData},
			{Text: "⛔️ 拒绝", CallbackData: denyData},
		}}
		if a.tryPushInteractive(sb.String(), rows) {
			return
		}
	}
	a.pushText(sb.String())
}

// pushTerminal 处理 done/error 终态：折叠推送 + 写消息绑定。
func (a *imAdapter) pushTerminal(isError bool) {
	a.mu.Lock()
	answer := a.lastAnswer
	runID := a.runID
	a.pending = nil
	a.terminalPushed = true
	a.mu.Unlock()

	var text string
	if isError {
		a.mu.Lock()
		lastErr := a.lastError
		a.mu.Unlock()
		if lastErr != nil {
			text = "❌ 执行失败：" + lastErr.ErrMsg
		} else {
			text = "❌ 执行失败"
		}
	} else if strings.TrimSpace(answer) == "" {
		text = "✅ 已完成（无文本输出）"
	} else {
		text = answer
	}

	// 终态优先原位编辑占位消息（一条消息承载完整结果，消息绑定记占位 ID）；
	// 无占位/编辑失败（如超长）回退推送（分片渠道自动拆条）。
	messageID := ""
	if a.editPlaceholder(text) {
		a.mu.Lock()
		messageID = a.placeholderMessageID
		a.mu.Unlock()
	}
	if messageID == "" {
		var err error
		messageID, err = a.pushText(text)
		if err != nil || messageID == "" {
			return
		}
	}
	_ = a.recordBinding(&model.ChannelMessage{
		ChannelID: a.channelID,
		ChatID:    a.chatID,
		MessageID: messageID,
		SessionID: a.sessionID,
		RunID:     runID,
	})
	// 跨端投递（M2）：同一 userName 的其他渠道聊天收到结果 + 会话引用；
	// 异步执行不阻塞 run 事件流，投递端回复该消息即续聊原会话。
	go a.gw.crossDeliver(a, text)
}

// pushText 推送到本 chat（带回复锚点）；失败只记日志不重试（旁路通知语义）。
// 长文口径见 service.pushBounded：分片渠道发全量，其余渠道 push_max_runes 截断兜底。
func (a *imAdapter) pushText(text string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.gw.pushBounded(ctx, PushAddr{
		ChannelID:      a.channelID,
		ChatID:         a.chatID,
		ReplyMessageID: a.replyAnchorMessageID,
	}, text)
}

// ---------------------------------------------------------------------------
// ClientMessageReader：IM 回复 → 运行时
// ---------------------------------------------------------------------------

// reader 阻塞等待 IM 回复并组装上行消息；run 结束返回 WSReadEndError 终止读取。
func (a *imAdapter) reader() react.ClientMessageReader {
	return func() (params.ReactWSMessage, error) {
		select {
		case text := <-a.replies:
			return a.formatReply(text), nil
		case <-a.done:
			return params.ReactWSMessage{}, &react.WSReadEndError{
				Kind:  react.WSReadEndLocalClose,
				Stage: "gateway_run_end",
			}
		}
	}
}

// formatReply 按当前 pending 把 IM 文本组装为 tool_use_answer / tool_confirm_answer。
// 无 pending 时的消息类型不匹配任何等待者，由 clientHub 既有缓冲语义兜底丢弃。
func (a *imAdapter) formatReply(text string) params.ReactWSMessage {
	a.mu.Lock()
	runID := a.runID
	a.mu.Unlock()
	pending := a.consumePending()

	msg := params.ReactWSMessage{RunID: runID, SessionID: a.sessionID}
	if pending == nil {
		msg.Type = "gateway_stray_reply"
		return msg
	}
	trimmed := strings.TrimSpace(text)

	switch pending.Kind {
	case "confirm":
		msg.Type = "tool_confirm_answer"
		payload, _ := json.Marshal(map[string]any{
			"toolUseId": pending.ToolUseID,
			"approved":  isApprovalText(trimmed),
		})
		msg.Payload = payload
	default: // ask
		answer := buildAskAnswer(pending.Questions, trimmed)
		msg.Type = "tool_use_answer"
		payload, _ := json.Marshal(map[string]any{
			"toolUseId": pending.ToolUseID,
			"content":   answer,
		})
		msg.Payload = payload
	}
	return msg
}

// isApprovalText 判断确认回复是否表示允许。
func isApprovalText(text string) bool {
	switch strings.ToLower(text) {
	case "允许", "同意", "是", "好", "确定", "y", "yes", "allow", "ok":
		return true
	}
	return false
}

// buildAskAnswer 把自由文本映射为 ask_question 的作答结构（M1 线性作答）：
// 纯数字（可逗号分隔）按选项序号映射 selectedOptionIds；「跳过」映射 skipped；其余为自由文本。
func buildAskAnswer(questions []askQuestionItemView, text string) map[string]any {
	if text == "跳过" || strings.EqualFold(text, "skip") {
		return map[string]any{"answers": []any{}, "skipped": true}
	}
	if len(questions) == 0 {
		return map[string]any{"answers": []any{}, "skipped": false}
	}
	q := questions[0]
	if selected, ok := matchOptionIndexes(q, text); ok {
		return map[string]any{
			"answers": []any{map[string]any{
				"questionId":        q.ID,
				"selectedOptionIds": selected,
			}},
			"skipped": false,
		}
	}
	return map[string]any{
		"answers": []any{map[string]any{
			"questionId": q.ID,
			"freeText":   text,
		}},
		"skipped": false,
	}
}

// matchOptionIndexes 尝试把「1」「1,3」这类回复映射为选项 ID 列表；格式不符返回 false。
func matchOptionIndexes(q askQuestionItemView, text string) ([]string, bool) {
	if len(q.Options) == 0 {
		return nil, false
	}
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '，' || r == ' ' })
	if len(parts) == 0 {
		return nil, false
	}
	selected := make([]string, 0, len(parts))
	for _, p := range parts {
		idx := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				return nil, false
			}
			idx = idx*10 + int(r-'0')
		}
		if idx < 1 || idx > len(q.Options) {
			return nil, false
		}
		// 重复序号去重。
		dup := false
		for _, s := range selected {
			if s == q.Options[idx-1].ID {
				dup = true
				break
			}
		}
		if !dup {
			selected = append(selected, q.Options[idx-1].ID)
		}
	}
	return selected, true
}
