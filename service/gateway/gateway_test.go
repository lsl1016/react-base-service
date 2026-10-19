package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"react-base-service/components/params"
	model "react-base-service/models/llm"
	react "react-base-service/service/react"

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
	if f.msgID == "" {
		return "om_fake_" + time.Now().Format("150405.000000000"), nil
	}
	return f.msgID, nil
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
