package gateway

import (
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
}
