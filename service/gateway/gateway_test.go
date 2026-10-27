package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	react "react-base-service/service/react"

	"github.com/gin-gonic/gin"
	larktypes "github.com/larksuite/oapi-sdk-go/v3/channel/types"
)

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// fakeChannel 记录推送并返回固定平台消息 ID。
type fakeChannel struct {
	mu      sync.Mutex
	pushes  []PushAddr
	texts   []string
	msgID   string
	pushErr error
	lastID  string // 最近一次推送返回的平台消息 ID（e2e 绑定断言用）
}

func (f *fakeChannel) Kind() string      { return "fake" }
func (f *fakeChannel) ChannelID() string { return "gwch_fake" }
func (f *fakeChannel) Start(ctx context.Context, handler MessageHandler) error {
	return nil
}
func (f *fakeChannel) Stop(ctx context.Context) error { return nil }
func (f *fakeChannel) PushText(ctx context.Context, addr PushAddr, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pushErr != nil {
		return "", f.pushErr
	}
	f.pushes = append(f.pushes, addr)
	f.texts = append(f.texts, text)
	id := f.msgID
	if id == "" {
		id = "om_fake_" + time.Now().Format("150405.000000000")
	}
	f.lastID = id
	return id, nil
}

func (f *fakeChannel) lastText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.texts) == 0 {
		return ""
	}
	return f.texts[len(f.texts)-1]
}

func (f *fakeChannel) textCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.texts)
}

// newTestService 构造挂有 fake 渠道的 service（绕过 DB 与 Manager.Start）。
func newTestService(ch Channel) *service {
	s := &service{adapters: map[string]*imAdapter{}, runInvoker: nil}
	s.manager = newManager(nil)
	s.manager.channels[ch.ChannelID()] = ch
	return s
}

// fakeInteractiveChannel 在 fakeChannel 基础上实现可选能力接口（inline 按钮 / 消息编辑）。
type fakeInteractiveChannel struct {
	fakeChannel
	interactives []InteractivePayload
	edits        []string // chatID|messageID|text
	editErr      error
}

func (f *fakeInteractiveChannel) PushInteractive(ctx context.Context, addr PushAddr, iv InteractivePayload) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interactives = append(f.interactives, iv)
	return "om_iv_1", nil
}

func (f *fakeInteractiveChannel) EditText(ctx context.Context, chatID, messageID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	f.edits = append(f.edits, chatID+"|"+messageID+"|"+text)
	return nil
}

// fakeChannelWithID 指定渠道 ID 的 fake（跨端投递需要多渠道并存区分目标）。
type fakeChannelWithID struct {
	fakeChannel
	id string
}

func (f *fakeChannelWithID) ChannelID() string { return f.id }

func newTestAdapter(gw *service) (*imAdapter, *[]*model.ChannelMessage) {
	var bindings []*model.ChannelMessage
	a := newIMAdapter(gw, "gwch_fake", "chat_1", "om_inbound_1", "session_test", "alice", func() {})
	a.recordBinding = func(row *model.ChannelMessage) error {
		bindings = append(bindings, row)
		return nil
	}
	return a, &bindings
}

// ---------------------------------------------------------------------------
// 折叠与终态推送
// ---------------------------------------------------------------------------

func TestAdapterFoldsOuterRunAnswerAndPushesOnDone(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, bindings := newTestAdapter(gw)

	a.handleEvent(params.ReactEvent{Type: react.EventContentEnd, RunID: "run_1", Payload: params.ReactContentEndPayload{Content: "第一轮中间输出"}})
	// 子代理内容不上墙。
	a.handleEvent(params.ReactEvent{Type: react.EventContentEnd, RunID: "run_1", AgentPath: "main/ops-agent", Payload: params.ReactContentEndPayload{Content: "子代理输出"}})
	a.handleEvent(params.ReactEvent{Type: react.EventContentEnd, RunID: "run_1", Payload: params.ReactContentEndPayload{Content: "最终答案：42"}})
	a.handleEvent(params.ReactEvent{Type: react.EventDone, RunID: "run_1", Payload: params.ReactDonePayload{}})

	if got := ch.lastText(); got != "最终答案：42" {
		t.Fatalf("终态推送内容应为最后一条外层内容，got=%q", got)
	}
	if len(*bindings) != 1 {
		t.Fatalf("应写入一条消息绑定，got %d", len(*bindings))
	}
	b := (*bindings)[0]
	if b.SessionID != "session_test" || b.RunID != "run_1" || b.MessageID == "" {
		t.Fatalf("绑定字段不完整: %+v", b)
	}
	// 终态应以回复触发消息的方式投递（线程感）。
	if ch.pushes[len(ch.pushes)-1].ReplyMessageID != "om_inbound_1" {
		t.Fatalf("终态推送应带回复锚点: %+v", ch.pushes[len(ch.pushes)-1])
	}
	if !a.pushedTerminal() {
		t.Fatalf("终态标记应为 true")
	}
}

func TestAdapterPushesFallbackWhenDoneWithoutContent(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)
	a.handleEvent(params.ReactEvent{Type: react.EventDone, RunID: "run_1"})
	if !strings.Contains(ch.lastText(), "已完成") {
		t.Fatalf("无内容终态应有占位文案，got=%q", ch.lastText())
	}
}

func TestAdapterErrorTerminalUsesErrorPayload(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)
	a.handleEvent(params.ReactEvent{Type: react.EventError, RunID: "run_1", Payload: params.ReactErrorPayload{ErrMsg: "模型超时"}})
	if !strings.Contains(ch.lastText(), "模型超时") {
		t.Fatalf("错误终态应包含错误信息，got=%q", ch.lastText())
	}
}

func TestAdapterCancelledDoesNotPush(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)
	a.handleEvent(params.ReactEvent{Type: react.EventContentEnd, RunID: "run_1", Payload: params.ReactContentEndPayload{Content: "部分输出"}})
	a.handleEvent(params.ReactEvent{Type: react.EventCancelled, RunID: "run_1"})
	if ch.textCount() != 0 {
		t.Fatalf("cancelled 终态不应推送，got %d 条", ch.textCount())
	}
}

// ---------------------------------------------------------------------------
// HITL：ask_question 推送 + 回复组装
// ---------------------------------------------------------------------------

func TestAdapterAskQuestionRegistersPendingAndPushes(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)

	toolInput := json.RawMessage(`{"questions":[{"id":"time_range","prompt":"统计时间范围？","options":[{"id":"d30","label":"最近30天"},{"id":"d90","label":"最近90天"}]}]}`)
	a.handleEvent(params.ReactEvent{Type: react.EventToolUseStart, RunID: "run_1", Payload: params.ReactToolUseStartPayload{
		ToolUseID: "tu_1", ToolName: "ask_question", ToolInput: toolInput, Status: "waiting",
	}})

	if a.takePending() == nil || a.takePending().ToolUseID != "tu_1" {
		t.Fatalf("应登记 ask_question 等待")
	}
	if !strings.Contains(ch.lastText(), "统计时间范围") || !strings.Contains(ch.lastText(), "最近30天") {
		t.Fatalf("问题推送应包含题面与选项，got=%q", ch.lastText())
	}
}

// ---------------------------------------------------------------------------
// 交互按钮（InteractivePusher 可选能力）
// ---------------------------------------------------------------------------

func TestAdapterAskQuestionUsesInteractiveWhenSupported(t *testing.T) {
	ch := &fakeInteractiveChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)

	toolInput := json.RawMessage(`{"questions":[{"id":"q1","prompt":"统计时间范围？","options":[{"id":"d30","label":"最近30天"},{"id":"d90","label":"最近90天"}]}]}`)
	a.handleEvent(params.ReactEvent{Type: react.EventToolUseStart, RunID: "run_1", Payload: params.ReactToolUseStartPayload{
		ToolUseID: "tu_1", ToolName: "ask_question", ToolInput: toolInput, Status: "waiting",
	}})

	if len(ch.interactives) != 1 {
		t.Fatalf("支持按钮的渠道应推送交互消息，got %d", len(ch.interactives))
	}
	iv := ch.interactives[0]
	// 2 个选项行 + 1 个跳过行；callback_data 带 toolUseId+序号文本。
	if len(iv.Rows) != 3 {
		t.Fatalf("按钮行数不符（选项+跳过）: %d", len(iv.Rows))
	}
	if iv.Rows[0][0].CallbackData != encodeHitlCallbackData("tu_1", "1") {
		t.Fatalf("选项按钮 callback_data 不符: %q", iv.Rows[0][0].CallbackData)
	}
	if iv.Rows[2][0].Text != "跳过" || iv.Rows[2][0].CallbackData != encodeHitlCallbackData("tu_1", "跳过") {
		t.Fatalf("跳过按钮不符: %+v", iv.Rows[2][0])
	}
	if !strings.Contains(iv.Text, "统计时间范围") {
		t.Fatalf("交互正文应含题面: %q", iv.Text)
	}
	// 交互成功后不再重复推送纯文本。
	if ch.textCount() != 0 {
		t.Fatalf("交互推送成功时不应再推文本，got %d", ch.textCount())
	}
	if a.takePending() == nil {
		t.Fatalf("交互推送同样要登记 pending")
	}
}

func TestAdapterConfirmUsesInteractiveButtons(t *testing.T) {
	ch := &fakeInteractiveChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)
	a.handleEvent(params.ReactEvent{Type: react.EventToolConfirmRequest, RunID: "run_1", Payload: params.ReactToolConfirmRequestPayload{
		ToolUseID: "tu_c", ToolName: "drop_table", ToolInput: json.RawMessage(`{"table":"x"}`),
	}})

	if len(ch.interactives) != 1 || len(ch.interactives[0].Rows) != 1 || len(ch.interactives[0].Rows[0]) != 2 {
		t.Fatalf("危险确认应推送一行两枚按钮（允许/拒绝）: %+v", ch.interactives)
	}
	buttons := ch.interactives[0].Rows[0]
	if buttons[0].CallbackData != encodeHitlCallbackData("tu_c", "允许") || buttons[1].CallbackData != encodeHitlCallbackData("tu_c", "拒绝") {
		t.Fatalf("确认按钮作答值不符: %+v", buttons)
	}
	if ch.textCount() != 0 {
		t.Fatalf("交互推送成功时不应再推文本")
	}
}

func TestAdapterAskQuestionNoOptionsFallsBackToText(t *testing.T) {
	ch := &fakeInteractiveChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)

	toolInput := json.RawMessage(`{"questions":[{"id":"q1","prompt":"补充说明？","options":[]}]}`)
	a.handleEvent(params.ReactEvent{Type: react.EventToolUseStart, RunID: "run_1", Payload: params.ReactToolUseStartPayload{
		ToolUseID: "tu_1", ToolName: "ask_question", ToolInput: toolInput, Status: "waiting",
	}})
	if len(ch.interactives) != 0 || ch.textCount() != 1 {
		t.Fatalf("无选项问题应退纯文本: interactive=%d text=%d", len(ch.interactives), ch.textCount())
	}
}

func TestAskQuestionButtonRowsLimits(t *testing.T) {
	build := func(n int) []askQuestionItemView {
		q := askQuestionItemView{ID: "q1"}
		for i := 0; i < n; i++ {
			q.Options = append(q.Options, struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			}{ID: fmt.Sprintf("o%d", i), Label: fmt.Sprintf("选项%d", i)})
		}
		return []askQuestionItemView{q}
	}
	// 选项过多（>8）退文本。
	if rows := askQuestionButtonRows("tu_1", build(9)); rows != nil {
		t.Fatalf("选项过多应返回 nil")
	}
	// 超长 toolUseId 撑爆 callback_data（>64 字节）时退文本。
	if rows := askQuestionButtonRows(strings.Repeat("t", 61), build(2)); rows != nil {
		t.Fatalf("callback_data 超限应返回 nil")
	}
}

func TestAdapterReaderFormatsOptionIndexAnswer(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)

	toolInput := json.RawMessage(`{"questions":[{"id":"q1","prompt":"P","options":[{"id":"o1","label":"A"},{"id":"o2","label":"B"},{"id":"o3","label":"C"}]}]}`)
	a.handleEvent(params.ReactEvent{Type: react.EventToolUseStart, RunID: "run_1", Payload: params.ReactToolUseStartPayload{
		ToolUseID: "tu_9", ToolName: "ask_question", ToolInput: toolInput, Status: "waiting",
	}})

	msg := a.formatReply("1,3")
	if msg.Type != react.EventToolUseAnswer {
		t.Fatalf("回复应组装为 tool_use_answer，got %s", msg.Type)
	}
	var envelope struct {
		ToolUseID string `json:"toolUseId"`
		Content   struct {
			Answers []struct {
				QuestionID        string   `json:"questionId"`
				SelectedOptionIDs []string `json:"selectedOptionIds"`
				FreeText          string   `json:"freeText"`
			} `json:"answers"`
			Skipped bool `json:"skipped"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msg.Payload, &envelope); err != nil {
		t.Fatalf("payload 非法: %v", err)
	}
	if envelope.ToolUseID != "tu_9" {
		t.Fatalf("toolUseId 应为等待中的调用，got %s", envelope.ToolUseID)
	}
	want := []string{"o1", "o3"}
	if len(envelope.Content.Answers) != 1 || strings.Join(envelope.Content.Answers[0].SelectedOptionIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("序号 1,3 应映射选项 o1,o3，got %+v", envelope.Content.Answers)
	}
}

func TestAdapterReaderFreeTextAndSkip(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)
	toolInput := json.RawMessage(`{"questions":[{"id":"q1","prompt":"P","options":[{"id":"o1","label":"A"}]}]}`)
	a.handleEvent(params.ReactEvent{Type: react.EventToolUseStart, RunID: "run_1", Payload: params.ReactToolUseStartPayload{
		ToolUseID: "tu_1", ToolName: "ask_question", ToolInput: toolInput, Status: "waiting",
	}})

	msg := a.formatReply("自定义补充说明")
	var envelope struct {
		Content struct {
			Answers []struct {
				FreeText          string   `json:"freeText"`
				SelectedOptionIDs []string `json:"selectedOptionIds"`
			} `json:"answers"`
			Skipped bool `json:"skipped"`
		} `json:"content"`
	}
	_ = json.Unmarshal(msg.Payload, &envelope)
	if len(envelope.Content.Answers) != 1 || envelope.Content.Answers[0].FreeText != "自定义补充说明" {
		t.Fatalf("非序号文本应映射 freeText，got %+v", envelope.Content.Answers)
	}

	a.handleEvent(params.ReactEvent{Type: react.EventToolUseStart, RunID: "run_1", Payload: params.ReactToolUseStartPayload{
		ToolUseID: "tu_2", ToolName: "ask_question", ToolInput: toolInput, Status: "waiting",
	}})
	msg = a.formatReply("跳过")
	_ = json.Unmarshal(msg.Payload, &envelope)
	if !envelope.Content.Skipped {
		t.Fatalf("「跳过」应映射 skipped=true，got %+v", envelope.Content)
	}
}

func TestAdapterReaderConfirmApproval(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)
	a.handleEvent(params.ReactEvent{Type: react.EventToolConfirmRequest, RunID: "run_1", Payload: params.ReactToolConfirmRequestPayload{
		ToolUseID: "tu_c", ToolName: "drop_table", ToolInput: json.RawMessage(`{"table":"x"}`),
	}})

	approved := a.formatReply("允许")
	var okEnvelope struct {
		ToolUseID string `json:"toolUseId"`
		Approved  bool   `json:"approved"`
	}
	_ = json.Unmarshal(approved.Payload, &okEnvelope)
	if approved.Type != "tool_confirm_answer" || !okEnvelope.Approved || okEnvelope.ToolUseID != "tu_c" {
		t.Fatalf("「允许」应组装 tool_confirm_answer approved=true，got %+v", okEnvelope)
	}

	a.handleEvent(params.ReactEvent{Type: react.EventToolConfirmRequest, RunID: "run_1", Payload: params.ReactToolConfirmRequestPayload{
		ToolUseID: "tu_c2", ToolName: "rm", ToolInput: json.RawMessage(`{}`),
	}})
	rejected := a.formatReply("不要")
	var noEnvelope struct {
		Approved bool `json:"approved"`
	}
	_ = json.Unmarshal(rejected.Payload, &noEnvelope)
	if noEnvelope.Approved {
		t.Fatalf("非允许文本应视为拒绝")
	}
}

func TestAdapterReaderStrayReplyWithoutPending(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)
	msg := a.formatReply("随便一句话")
	if msg.Type == react.EventToolUseAnswer || msg.Type == "tool_confirm_answer" {
		t.Fatalf("无 pending 时不应组装为作答消息，got %s", msg.Type)
	}
}

// ---------------------------------------------------------------------------
// reader 生命周期
// ---------------------------------------------------------------------------

func TestAdapterReaderEndsAfterRunFinish(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)
	reader := a.reader()

	done := make(chan struct{})
	var err error
	go func() {
		_, err = reader()
		close(done)
	}()
	a.finish()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("run 结束后 reader 应返回")
	}
	if err == nil {
		t.Fatalf("reader 应返回 WSReadEndError")
	}
	if _, ok := err.(*react.WSReadEndError); !ok {
		t.Fatalf("错误类型应为 WSReadEndError，got %T", err)
	}
}

func TestAdapterDeliverReplyWakesReader(t *testing.T) {
	ch := &fakeChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)
	reader := a.reader()

	got := make(chan params.ReactWSMessage, 1)
	go func() {
		msg, err := reader()
		if err == nil {
			got <- msg
		}
		close(got)
	}()
	a.deliverReply("随便一句话")
	select {
	case msg, ok := <-got:
		// 无 pending 时 reader 仍应被唤醒并返回（stray 类型由 clientHub 缓冲语义兜底）。
		if !ok || msg.Type != "gateway_stray_reply" {
			t.Fatalf("应收到唤醒后的消息，got ok=%v type=%s", ok, msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatalf("回复投递应唤醒阻塞的 reader")
	}
}

// ---------------------------------------------------------------------------
// 纯函数
// ---------------------------------------------------------------------------

func TestMatchOptionIndexes(t *testing.T) {
	var q askQuestionItemView
	if err := json.Unmarshal([]byte(`{"options":[{"id":"a"},{"id":"b"},{"id":"c"}]}`), &q); err != nil {
		t.Fatal(err)
	}
	if got, ok := matchOptionIndexes(q, "2"); !ok || got[0] != "b" {
		t.Fatalf("单序号映射失败: %v %v", got, ok)
	}
	if got, ok := matchOptionIndexes(q, "1,3"); !ok || len(got) != 2 {
		t.Fatalf("多序号映射失败: %v %v", got, ok)
	}
	if got, ok := matchOptionIndexes(q, "1，2"); !ok || len(got) != 2 {
		t.Fatalf("中文逗号应支持: %v %v", got, ok)
	}
	if _, ok := matchOptionIndexes(q, "4"); ok {
		t.Fatalf("越界序号应失败")
	}
	if _, ok := matchOptionIndexes(q, "abc"); ok {
		t.Fatalf("非数字应失败")
	}
}

func TestExtractFeishuParentID(t *testing.T) {
	eventShape := map[string]any{"event": map[string]any{"message": map[string]any{"parent_id": "om_parent_1"}}}
	if got := extractFeishuParentID(eventShape); got != "om_parent_1" {
		t.Fatalf("event 形态提取失败: %q", got)
	}
	dataShape := map[string]any{"data": map[string]any{"message": map[string]any{"parent_id": "om_parent_2"}}}
	if got := extractFeishuParentID(dataShape); got != "om_parent_2" {
		t.Fatalf("data 形态提取失败: %q", got)
	}
	if got := extractFeishuParentID(map[string]any{"data": map[string]any{"message": map[string]any{}}}); got != "" {
		t.Fatalf("无 parent 应返回空: %q", got)
	}
	if got := extractFeishuParentID(nil); got != "" {
		t.Fatalf("nil 应返回空: %q", got)
	}
}

func TestBuildRunPayloadCarriesSnapshotTuple(t *testing.T) {
	snap := &sessionSnapshot{
		SessionID: "session_x", UserName: "alice", AppKey: "ops",
		RouteValues: `["north","prod"]`, SessionType: "chat",
	}
	payload := buildRunPayload(snap, "跑个报表")
	if payload.AppKey != "ops" || payload.Type != "chat" || payload.UserPrompt != "跑个报表" {
		t.Fatalf("payload 字段不符: %+v", payload)
	}
	if strings.Join(payload.RouteValues, ",") != "north,prod" {
		t.Fatalf("routeValues 解析失败: %v", payload.RouteValues)
	}
	if !strings.Contains(string(payload.ControlContext), "gateway") {
		t.Fatalf("controlContext 应带 requestSource=gateway: %s", payload.ControlContext)
	}
}

func TestNormalizeRouteValuesJSON(t *testing.T) {
	if got := normalizeRouteValuesJSON("not-json"); got != "[]" {
		t.Fatalf("非法输入应回退 []: %q", got)
	}
	if got := normalizeRouteValuesJSON(`["a"]`); got != `["a"]` {
		t.Fatalf("合法输入应保持: %q", got)
	}
}

func TestGenerateGatewaySessionIDFormat(t *testing.T) {
	id := generateGatewaySessionID()
	if !strings.HasPrefix(id, "session_") || strings.Contains(id, "-") || len(id) != len("session_")+32 {
		t.Fatalf("会话 ID 格式不符: %q", id)
	}
}

func TestToInboundGroupRequiresMention(t *testing.T) {
	f := &feishuChannel{cfg: &model.ChannelConfig{ChannelID: "gwch_1"}}
	groupNoMention := f.toInbound(&larktypes.NormalizedMessage{
		ChatType: "group", MentionedBot: false, Content: "hello", ChatID: "c1", MessageID: "m1",
	})
	if groupNoMention != nil {
		t.Fatalf("群聊未 @ 机器人应忽略")
	}
	groupMentioned := f.toInbound(&larktypes.NormalizedMessage{
		ChatType: "group", MentionedBot: true, Content: "hello", ChatID: "c1", MessageID: "m1",
	})
	if groupMentioned == nil {
		t.Fatalf("群聊 @ 机器人应处理")
	}
	p2p := f.toInbound(&larktypes.NormalizedMessage{
		ChatType: "p2p", Content: "hello", ChatID: "c1", MessageID: "m1",
	})
	if p2p == nil {
		t.Fatalf("单聊应处理")
	}
	empty := f.toInbound(&larktypes.NormalizedMessage{ChatType: "p2p", Content: "  ", ChatID: "c1"})
	if empty != nil {
		t.Fatalf("空文本应忽略")
	}
}

// ---------------------------------------------------------------------------
// 占位消息与原位编辑（MessageEditor 可选能力）
// ---------------------------------------------------------------------------

func TestAdapterTerminalEditsPlaceholder(t *testing.T) {
	ch := &fakeInteractiveChannel{}
	gw := newTestService(ch)
	a, bindings := newTestAdapter(gw)
	a.setPlaceholder("ph_9")

	a.handleEvent(params.ReactEvent{Type: react.EventContentEnd, RunID: "run_1", Payload: params.ReactContentEndPayload{Content: "最终答案：42"}})
	a.handleEvent(params.ReactEvent{Type: react.EventDone, RunID: "run_1", Payload: params.ReactDonePayload{}})

	if len(ch.edits) != 1 || !strings.Contains(ch.edits[0], "最终答案：42") || !strings.Contains(ch.edits[0], "ph_9") {
		t.Fatalf("终态应原位编辑占位消息: %v", ch.edits)
	}
	if ch.textCount() != 0 {
		t.Fatalf("占位编辑成功时不应再推新消息，got %d", ch.textCount())
	}
	// 消息绑定记录占位消息 ID（回复该消息即续聊本会话）。
	if len(*bindings) != 1 || (*bindings)[0].MessageID != "ph_9" {
		t.Fatalf("绑定应记占位消息 ID: %+v", *bindings)
	}
}

func TestAdapterTerminalEditFailureFallsBackToPush(t *testing.T) {
	ch := &fakeInteractiveChannel{editErr: errors.New("message is not modified")}
	gw := newTestService(ch)
	a, bindings := newTestAdapter(gw)
	a.setPlaceholder("ph_9")

	a.handleEvent(params.ReactEvent{Type: react.EventContentEnd, RunID: "run_1", Payload: params.ReactContentEndPayload{Content: "超长内容编辑失败场景"}})
	a.handleEvent(params.ReactEvent{Type: react.EventDone, RunID: "run_1"})

	if ch.textCount() != 1 {
		t.Fatalf("编辑失败应回退推送，got %d", ch.textCount())
	}
	if len(*bindings) != 1 || (*bindings)[0].MessageID == "ph_9" {
		t.Fatalf("回退推送时绑定应记新消息 ID: %+v", *bindings)
	}
}

func TestAdapterNotifyUsesPlaceholderFirst(t *testing.T) {
	ch := &fakeInteractiveChannel{}
	gw := newTestService(ch)
	a, _ := newTestAdapter(gw)

	// 无占位：通知走推送。
	a.notify("⏳ 提示")
	if ch.textCount() != 1 || len(ch.edits) != 0 {
		t.Fatalf("无占位时通知应推送: text=%d edits=%d", ch.textCount(), len(ch.edits))
	}
	// 有占位：原位编辑，不新增消息。
	a.setPlaceholder("ph_1")
	a.notify("再提示")
	if ch.textCount() != 1 || len(ch.edits) != 1 || !strings.Contains(ch.edits[0], "再提示") {
		t.Fatalf("有占位时通知应编辑: text=%d edits=%v", ch.textCount(), ch.edits)
	}
	// notifyIfPlaceholder 无占位时静默（取消等原本不推送的收敛不变）。
	b := newIMAdapter(gw, "gwch_fake", "chat_1", "om_in_2", "session_test", "alice", func() {})
	b.notifyIfPlaceholder("🛑 已取消")
	if ch.textCount() != 1 || len(ch.edits) != 1 {
		t.Fatalf("无占位的 notifyIfPlaceholder 应静默: text=%d edits=%d", ch.textCount(), len(ch.edits))
	}
	b.setPlaceholder("ph_2")
	b.notifyIfPlaceholder("🛑 已取消")
	if len(ch.edits) != 2 {
		t.Fatalf("有占位的 notifyIfPlaceholder 应编辑")
	}
}

// ---------------------------------------------------------------------------
// 命令解析与 /sessions 列表
// ---------------------------------------------------------------------------

func TestParseInboundCommand(t *testing.T) {
	if cmd, rest := parseInboundCommand("普通消息"); cmd != "" || rest != "普通消息" {
		t.Fatalf("非命令文本: cmd=%q rest=%q", cmd, rest)
	}
	if cmd, rest := parseInboundCommand("/cancel"); cmd != "/cancel" || rest != "" {
		t.Fatalf("纯命令: cmd=%q rest=%q", cmd, rest)
	}
	if cmd, rest := parseInboundCommand("/New@Ppx_Zzu_Bot  你好"); cmd != "/new" || rest != "你好" {
		t.Fatalf("带 @bot 后缀与正文: cmd=%q rest=%q", cmd, rest)
	}
}

func TestBuildSessionListText(t *testing.T) {
	empty := buildSessionListText(nil, "")
	if !strings.Contains(empty, "直接发消息") {
		t.Fatalf("空列表应引导开始: %q", empty)
	}
	rows := []model.ChannelSession{
		{SessionID: "session_abcdefgh1234", LastMessage: "查库存", LastActiveAt: time.Date(2026, 10, 19, 22, 19, 0, 0, time.Local)},
		{SessionID: "session_second0000099", LastMessage: strings.Repeat("长", 80), LastActiveAt: time.Date(2026, 10, 18, 9, 0, 0, 0, time.Local)},
	}
	text := buildSessionListText(rows, "session_second0000099")
	if !strings.Contains(text, "👉 2. session_second00…") {
		t.Fatalf("当前会话应打标: %q", text)
	}
	if !strings.Contains(text, "查库存") || !strings.Contains(text, " 1. session_abcd") {
		t.Fatalf("列表应含序号与摘要: %q", text)
	}
	if !strings.Contains(text, strings.Repeat("长", 40)+"…") {
		t.Fatalf("长摘要应截断: %q", text)
	}
}

// ---------------------------------------------------------------------------
// 跨端投递（M2 delivery.go）
// ---------------------------------------------------------------------------

// newDeliveryTestService 构造挂双 fake 渠道（origin/target）的投递测试服务：
// 目标解析与绑定写入均注入替身（单测无 DB）。
func newDeliveryTestService(t *testing.T, targets []model.ChannelSession) (*service, *fakeChannelWithID, *fakeChannelWithID, *[]*model.ChannelMessage) {
	t.Helper()
	origin := &fakeChannelWithID{fakeChannel: fakeChannel{}, id: "gwch_origin"}
	target := &fakeChannelWithID{fakeChannel: fakeChannel{}, id: "gwch_target"}
	gw := &service{adapters: map[string]*imAdapter{}}
	gw.manager = newManager(nil)
	gw.manager.channels[origin.ChannelID()] = origin
	gw.manager.channels[target.ChannelID()] = target
	gw.resolveDeliveryTargets = func(ctx *gin.Context, userName, excludeChannelID string) ([]model.ChannelSession, error) {
		return targets, nil
	}
	bindings := &[]*model.ChannelMessage{}
	originalRecord := recordCrossDeliveryBinding
	recordCrossDeliveryBinding = func(row *model.ChannelMessage) error {
		*bindings = append(*bindings, row)
		return nil
	}
	t.Cleanup(func() { recordCrossDeliveryBinding = originalRecord })
	return gw, origin, target, bindings
}

func withCrossDelivery(t *testing.T, enabled bool, replayBaseURL string) {
	t.Helper()
	original := conf.CustomConf.LLM.Gateway
	conf.CustomConf.LLM.Gateway.CrossDelivery = enabled
	conf.CustomConf.LLM.Gateway.ReplayBaseURL = replayBaseURL
	t.Cleanup(func() { conf.CustomConf.LLM.Gateway = original })
}

func TestCrossDeliverFansOutAndRecordsBinding(t *testing.T) {
	withCrossDelivery(t, true, "")
	gw, origin, target, bindings := newDeliveryTestService(t, []model.ChannelSession{
		{ChannelID: "gwch_target", ChatID: "oc_target_chat"},
	})

	a := newIMAdapter(gw, "gwch_origin", "chat_o", "m1", "session_x", "alice", func() {})
	a.setReplayInfo("demo-app", `["north"]`)
	gw.crossDeliver(a, "任务结果：库存 42")

	if target.textCount() != 1 {
		t.Fatalf("目标渠道应收到投递，got %d", target.textCount())
	}
	got := target.lastText()
	if !strings.Contains(got, "任务结果：库存 42") || !strings.Contains(got, "会话：session_x") {
		t.Fatalf("投递文案应含结果与会话引用: %q", got)
	}
	if !strings.Contains(got, "回复本消息") {
		t.Fatalf("投递文案应带续聊指引: %q", got)
	}
	if origin.textCount() != 0 {
		t.Fatalf("发起渠道不应重复收到投递，got %d", origin.textCount())
	}
	if len(*bindings) != 1 || (*bindings)[0].ChannelID != "gwch_target" || (*bindings)[0].ChatID != "oc_target_chat" ||
		(*bindings)[0].SessionID != "session_x" {
		t.Fatalf("投递绑定字段不符: %+v", *bindings)
	}
}

func TestCrossDeliverDisabledOrNoTargets(t *testing.T) {
	// 开关关闭：完全不投递。
	withCrossDelivery(t, false, "")
	gw, _, target, _ := newDeliveryTestService(t, []model.ChannelSession{{ChannelID: "gwch_target", ChatID: "c1"}})
	a := newIMAdapter(gw, "gwch_origin", "chat_o", "m1", "session_x", "alice", func() {})
	gw.crossDeliver(a, "结果")
	if target.textCount() != 0 {
		t.Fatalf("cross_delivery 关闭时不应投递")
	}
	// 无目标：不投递。
	withCrossDelivery(t, true, "")
	gw2, _, target2, _ := newDeliveryTestService(t, nil)
	a2 := newIMAdapter(gw2, "gwch_origin", "chat_o", "m1", "session_x", "alice", func() {})
	gw2.crossDeliver(a2, "结果")
	if target2.textCount() != 0 {
		t.Fatalf("无目标时不应投递")
	}
}

func TestCrossDeliverSkipsOriginAndUnknownChannels(t *testing.T) {
	withCrossDelivery(t, true, "")
	// 目标里混入发起渠道与未运行渠道：都应被跳过，只投可达目标。
	gw, origin, target, _ := newDeliveryTestService(t, []model.ChannelSession{
		{ChannelID: "gwch_origin", ChatID: "chat_o"},
		{ChannelID: "gwch_missing", ChatID: "chat_m"},
		{ChannelID: "gwch_target", ChatID: "chat_t"},
	})
	a := newIMAdapter(gw, "gwch_origin", "chat_o", "m1", "session_x", "alice", func() {})
	gw.crossDeliver(a, "结果")
	if target.textCount() != 1 || origin.textCount() != 0 {
		t.Fatalf("应只投可达的非发起渠道: target=%d origin=%d", target.textCount(), origin.textCount())
	}
	if got := target.pushes[len(target.pushes)-1]; got.ChatID != "chat_t" || got.ReplyMessageID != "" {
		t.Fatalf("投递寻址不符: %+v", got)
	}
}

func TestBuildDeliveryMessage(t *testing.T) {
	withCrossDelivery(t, true, "http://x/replay.html")
	got := buildDeliveryMessage(model.ChannelKindTelegram, "结果正文", "session_abcdef1234567890", "demo-app", `["a","b"]`)
	if !strings.Contains(got, "来自 Telegram") || !strings.Contains(got, "结果正文") {
		t.Fatalf("投递文案应含来源与结果: %q", got)
	}
	wantLink := "http://x/replay.html?sessionId=session_abcdef1234567890&appKey=demo-app&routeValues=" + url.QueryEscape(`["a","b"]`)
	if !strings.Contains(got, wantLink) {
		t.Fatalf("回放链接应带编码参数: want %q got %q", wantLink, got)
	}
	// replay_base_url 未配置：不附链接，保留会话引用与续聊指引。
	withCrossDelivery(t, true, "")
	got = buildDeliveryMessage(model.ChannelKindFeishu, "结果", "session_y", "demo-app", `[]`)
	if strings.Contains(got, "回放") || !strings.Contains(got, "来自 飞书") || !strings.Contains(got, "会话：session_y") {
		t.Fatalf("无链接投递文案不符: %q", got)
	}
}

func TestIdentityScopeKind(t *testing.T) {
	if got := identityScopeKind("telegram", "feishu"); got != "telegram" {
		t.Fatalf("入站 kind 优先: %q", got)
	}
	if got := identityScopeKind("", "feishu"); got != "feishu" {
		t.Fatalf("缺省回渠道配置 kind: %q", got)
	}
	if got := identityScopeKind("  ", "feishu"); got != "feishu" {
		t.Fatalf("空白 kind 回渠道配置: %q", got)
	}
}
