package gateway

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"

	telebot "gopkg.in/telebot.v3"
)

// telegramChannel 是 Telegram 渠道实现：官方 telebot v3 长轮询（无公网回调要求）。
//
// 凭证形态：单个 Bot Token（@BotFather /newbot 发放），存 ChannelConfig.AppSecret
// （面板打码回显）；AppID 可选，存机器人 @username（群聊 @ 过滤与提及剥离用，留空时
// 由 NewBot 的 getMe 结果自动补齐）。
//
// 国内网络注意：Telegram API 需代理，走进程级 HTTPS_PROXY/ALL_PROXY 环境变量
// （Go 标准 http.Transport 语义），本渠道不单独配置代理参数。
type telegramChannel struct {
	cfg      *model.ChannelConfig
	bot      *telebot.Bot
	username string

	stopOnce sync.Once
}

func newTelegramChannel(cfg *model.ChannelConfig) *telegramChannel {
	return &telegramChannel{cfg: cfg}
}

func (t *telegramChannel) Kind() string      { return model.ChannelKindTelegram }
func (t *telegramChannel) ChannelID() string { return t.cfg.ChannelID }

// Start 校验 token 并拉起长轮询。NewBot 内部走 getMe 校验凭证，
// token 非法在此同步返回错误；轮询循环独立 goroutine，由 Stop 收敛。
func (t *telegramChannel) Start(ctx context.Context, handler MessageHandler) error {
	token := strings.TrimSpace(t.cfg.AppSecret)
	if token == "" {
		return errChannelNotStarted // 语义：凭证缺失，无法启动
	}
	bot, err := telebot.NewBot(telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		return err
	}
	t.bot = bot
	if bot.Me != nil {
		t.username = strings.TrimSpace(bot.Me.Username)
	}
	// AppID 存了 @username 时优先用它（NewBot 后启动重载间 username 可能取不到的场景）。
	if u := strings.TrimPrefix(strings.TrimSpace(t.cfg.AppID), "@"); u != "" {
		t.username = u
	}

	bot.Handle(telebot.OnText, func(c telebot.Context) error {
		inbound := t.toInbound(c.Message())
		if inbound == nil {
			return nil
		}
		// handler 内部自行异步；OnText 需快速返回以维持轮询吞吐。
		go handler(context.Background(), *inbound)
		return nil
	})
	go func() {
		bot.Start() // 阻塞至 Stop
	}()
	zlog.Infof(nil, "[gateway.telegram] 渠道 %s 长轮询已启动: bot=@%s", t.cfg.ChannelID, t.username)
	return nil
}

func (t *telegramChannel) Stop(ctx context.Context) error {
	t.stopOnce.Do(func() {
		if t.bot != nil {
			t.bot.Stop()
		}
	})
	return nil
}

// PushText 推送文本；先按 markdown 解析发送，Telegram 解析报错（语法不兼容）时回退纯文本。
// 返回平台消息 ID（写入消息绑定，回复该消息时作为 parent 关联续聊）。
func (t *telegramChannel) PushText(ctx context.Context, addr PushAddr, text string) (string, error) {
	if t.bot == nil {
		return "", errChannelNotStarted
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(addr.ChatID), 10, 64)
	if err != nil {
		return "", err
	}
	opts := &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}
	if addr.ReplyMessageID != "" {
		if id, err := strconv.Atoi(addr.ReplyMessageID); err == nil {
			opts.ReplyTo = &telebot.Message{ID: id}
		}
	}
	sent, err := t.bot.Send(telebot.ChatID(chatID), text, opts)
	if err != nil {
		sent, err = t.bot.Send(telebot.ChatID(chatID), text)
		if err != nil {
			return "", err
		}
	}
	return strconv.Itoa(sent.ID), nil
}

// toInbound 把 telebot 消息转成网关入站消息；私聊全量响应，群聊仅响应 @机器人
// （剥离提及文本）；ReplyTo 消息映射为 ParentMessageID 命中续聊绑定。
func (t *telegramChannel) toInbound(m *telebot.Message) *InboundMessage {
	if m == nil || m.Chat == nil {
		return nil
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return nil
	}
	chatType := "p2p"
	switch m.Chat.Type {
	case telebot.ChatGroup, telebot.ChatSuperGroup:
		chatType = "group"
		if t.username != "" {
			mention := "@" + t.username
			if !strings.Contains(text, mention) {
				return nil
			}
			text = strings.TrimSpace(strings.ReplaceAll(text, mention, ""))
		}
		if text == "" {
			return nil
		}
	case telebot.ChatPrivate:
	default:
		return nil
	}
	parentID := ""
	if m.ReplyTo != nil {
		parentID = strconv.Itoa(m.ReplyTo.ID)
	}
	senderID := ""
	if m.Sender != nil {
		senderID = strconv.FormatInt(m.Sender.ID, 10)
	}
	return &InboundMessage{
		ChannelID:       t.cfg.ChannelID,
		Kind:            model.ChannelKindTelegram,
		ChatID:          strconv.FormatInt(m.Chat.ID, 10),
		ChatType:        chatType,
		UserID:          senderID,
		MessageID:       strconv.Itoa(m.ID),
		ParentMessageID: parentID,
		Text:            text,
	}
}
