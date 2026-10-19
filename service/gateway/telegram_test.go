package gateway

import (
	"strings"
	"testing"

	model "react-base-service/models/llm"

	telebot "gopkg.in/telebot.v3"
)

func newTelegramForTest(username string) *telegramChannel {
	return &telegramChannel{
		cfg:      &model.ChannelConfig{ChannelID: "gwch_tg", Kind: model.ChannelKindTelegram},
		username: username,
	}
}

func TestTelegramToInboundPrivate(t *testing.T) {
	ch := newTelegramForTest("base_bot")
	in := ch.toInbound(&telebot.Message{
		ID:   42,
		Text: "你好，帮我跑个任务",
		Chat: &telebot.Chat{ID: 10001, Type: telebot.ChatPrivate},
		Sender: &telebot.User{ID: 777},
	})
	if in == nil {
		t.Fatalf("私聊消息应处理")
	}
	if in.ChatID != "10001" || in.ChatType != "p2p" || in.UserID != "777" || in.MessageID != "42" {
		t.Fatalf("字段映射不符: %+v", in)
	}
	if in.ParentMessageID != "" {
		t.Fatalf("非回复消息 parent 应为空: %q", in.ParentMessageID)
	}
}

func TestTelegramToInboundReplyMapsParent(t *testing.T) {
	ch := newTelegramForTest("base_bot")
	in := ch.toInbound(&telebot.Message{
		ID:   43,
		Text: "继续刚才的任务",
		Chat: &telebot.Chat{ID: 10001, Type: telebot.ChatPrivate},
		ReplyTo: &telebot.Message{ID: 42},
	})
	if in == nil || in.ParentMessageID != "42" {
		t.Fatalf("回复消息应映射 parent=42: %+v", in)
	}
}

func TestTelegramToInboundGroupRequiresMention(t *testing.T) {
	ch := newTelegramForTest("base_bot")
	noMention := ch.toInbound(&telebot.Message{
		ID: 50, Text: "大家看看这个问题",
		Chat: &telebot.Chat{ID: -100123, Type: telebot.ChatSuperGroup},
	})
	if noMention != nil {
		t.Fatalf("群聊未 @ 机器人应忽略")
	}
	mentioned := ch.toInbound(&telebot.Message{
		ID: 51, Text: "@base_bot 帮我查一下线上日志",
		Chat: &telebot.Chat{ID: -100123, Type: telebot.ChatSuperGroup},
	})
	if mentioned == nil {
		t.Fatalf("群聊 @ 机器人应处理")
	}
	if mentioned.Text != "帮我查一下线上日志" {
		t.Fatalf("提及文本应被剥离: %q", mentioned.Text)
	}
	if mentioned.ChatType != "group" {
		t.Fatalf("群聊 chatType 应为 group: %q", mentioned.ChatType)
	}
	// 仅剩 @ 提及、剥离后为空：忽略。
	onlyMention := ch.toInbound(&telebot.Message{
		ID: 52, Text: "@base_bot",
		Chat: &telebot.Chat{ID: -100123, Type: telebot.ChatGroup},
	})
	if onlyMention != nil {
		t.Fatalf("剥离提及后为空应忽略")
	}
}

func TestTelegramToInboundSkipsUnknownChatTypeAndEmpty(t *testing.T) {
	ch := newTelegramForTest("base_bot")
	if ch.toInbound(nil) != nil {
		t.Fatalf("nil 消息应忽略")
	}
	if ch.toInbound(&telebot.Message{Text: "hi", Chat: nil}) != nil {
		t.Fatalf("无 chat 应忽略")
	}
	if ch.toInbound(&telebot.Message{Text: "  ", Chat: &telebot.Chat{ID: 1, Type: telebot.ChatPrivate}}) != nil {
		t.Fatalf("空文本应忽略")
	}
	if ch.toInbound(&telebot.Message{Text: "hi", Chat: &telebot.Chat{ID: 1, Type: telebot.ChatChannel}}) != nil {
		t.Fatalf("未知 chat 类型应忽略")
	}
}

func TestTelegramPushTextWithoutBotFails(t *testing.T) {
	ch := newTelegramForTest("base_bot")
	if _, err := ch.PushText(nil, PushAddr{ChatID: "10001"}, "text"); err == nil {
		t.Fatalf("未启动（无 bot）推送应报错")
	}
	if err := ch.EditText(nil, "10001", "42", "text"); err == nil {
		t.Fatalf("未启动（无 bot）编辑应报错")
	}
}

func TestTelegramToInboundStripsCommandMention(t *testing.T) {
	ch := newTelegramForTest("base_bot")
	in := ch.toInbound(&telebot.Message{
		ID: 60, Text: "/cancel@base_bot",
		Chat: &telebot.Chat{ID: 10001, Type: telebot.ChatPrivate},
	})
	if in == nil || in.Text != "/cancel" {
		t.Fatalf("私聊命令 @bot 后缀应剥离: %+v", in)
	}
	group := ch.toInbound(&telebot.Message{
		ID: 61, Text: "@base_bot /new@base_bot 帮我查库存",
		Chat: &telebot.Chat{ID: -100123, Type: telebot.ChatSuperGroup},
	})
	if group == nil || group.Text != "/new 帮我查库存" {
		t.Fatalf("群聊命令提及剥离不符: %+v", group)
	}
}

func TestStripCommandMentionSuffix(t *testing.T) {
	if got := stripCommandMentionSuffix("/cancel@ppx_zzu_bot"); got != "/cancel" {
		t.Fatalf("命令后缀剥离失败: %q", got)
	}
	if got := stripCommandMentionSuffix("/new@ppx_zzu_bot 帮我查库存"); got != "/new 帮我查库存" {
		t.Fatalf("带正文命令剥离失败: %q", got)
	}
	if got := stripCommandMentionSuffix("普通@提到"); got != "普通@提到" {
		t.Fatalf("非命令文本应原样: %q", got)
	}
	if got := stripCommandMentionSuffix("/help"); got != "/help" {
		t.Fatalf("无后缀命令应原样: %q", got)
	}
}

func TestHitlCallbackDataRoundtrip(t *testing.T) {
	data := encodeHitlCallbackData("tu_abc123", "1")
	if len(data) > telegramCallbackDataLimit {
		t.Fatalf("callback_data 超 64 字节上限: %d", len(data))
	}
	toolUseID, value := parseHitlCallbackData(data)
	if toolUseID != "tu_abc123" || value != "1" {
		t.Fatalf("回调数据往返不符: %q %q", toolUseID, value)
	}
	if _, v := parseHitlCallbackData("x|a|b"); v != "" {
		t.Fatalf("非 hitl 前缀应拒绝")
	}
	if _, v := parseHitlCallbackData("h|only-two"); v != "" {
		t.Fatalf("缺作答段应拒绝")
	}
	// 中文作答值（允许/拒绝/跳过）合法。
	if _, v := parseHitlCallbackData(encodeHitlCallbackData("tu_1", "允许")); v != "允许" {
		t.Fatalf("中文作答值应支持: %q", v)
	}
}

func TestSplitTelegramText(t *testing.T) {
	if got := splitTelegramText("hello"); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("短文本不应分片: %v", got)
	}
	// 换行优先断点：断在窗口内最近的 \n。
	long := strings.Repeat("a", 3000) + "\n" + strings.Repeat("b", 2000)
	got := splitTelegramText(long)
	if len(got) != 2 || !strings.HasSuffix(got[0], "\n") {
		t.Fatalf("应在换行处断开: %d 片", len(got))
	}
	// 无换行硬切 + 拼回无损。
	hard := strings.Repeat("中", 7200)
	got = splitTelegramText(hard)
	if len(got) != 3 {
		t.Fatalf("7200 字符应切 3 片: %d", len(got))
	}
	for i, chunk := range got {
		if len([]rune(chunk)) > telegramChunkRunes {
			t.Fatalf("第 %d 片超限: %d", i, len([]rune(chunk)))
		}
	}
	if strings.Join(got, "") != hard {
		t.Fatalf("分片拼回应无损")
	}
}

func TestParseAllowedUsers(t *testing.T) {
	if got := parseAllowedUsers(""); got != nil {
		t.Fatalf("空配置应返回 nil")
	}
	if got := parseAllowedUsers("not-json"); got != nil {
		t.Fatalf("非法配置应返回 nil（语义=不限）")
	}
	got := parseAllowedUsers(`["12345","@Alice"," oc_x ",""]`)
	if len(got) != 3 || got[0] != "12345" || got[1] != "@alice" || got[2] != "oc_x" {
		t.Fatalf("白名单解析/归一不符: %v", got)
	}
}

func TestChannelAllowsSender(t *testing.T) {
	if !channelAllowsSender(nil, "anything") {
		t.Fatalf("空清单应放行")
	}
	allowed := []string{"12345", "@alice", "-100200"}
	if !channelAllowsSender(allowed, "12345") {
		t.Fatalf("user ID 命中应放行")
	}
	if !channelAllowsSender(allowed, "@Alice") {
		t.Fatalf("@username 应大小写不敏感")
	}
	if !channelAllowsSender(allowed, "-100200") {
		t.Fatalf("群 chat ID 命中应放行")
	}
	if channelAllowsSender(allowed, "999") {
		t.Fatalf("未命中应拒绝")
	}
	if !channelAllowsSender(allowed, "999", "12345") {
		t.Fatalf("任一候选命中即应放行")
	}
}

func TestTelegramAllowsChatWhitelist(t *testing.T) {
	ch := newTelegramForTest("base_bot")
	ch.cfg.AllowedUsers = `["777"]`
	if !ch.allowsChat(&telebot.Chat{ID: 10001, Type: telebot.ChatPrivate}, &telebot.User{ID: 777}) {
		t.Fatalf("白名单发送者应放行")
	}
	if ch.allowsChat(&telebot.Chat{ID: 10001, Type: telebot.ChatPrivate}, &telebot.User{ID: 888}) {
		t.Fatalf("白名单外发送者应拒绝")
	}
	// 群 chat ID 配入白名单 = 放行该群。
	group := &telegramChannel{cfg: &model.ChannelConfig{ChannelID: "gwch_tg", Kind: model.ChannelKindTelegram, AllowedUsers: `["-100123"]`}}
	if !group.allowsChat(&telebot.Chat{ID: -100123, Type: telebot.ChatSuperGroup}, &telebot.User{ID: 888}) {
		t.Fatalf("白名单群应放行全体成员")
	}
}
