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
//
// 增强能力（按能力接口启用，见 types.go）：
//   - InteractivePusher：ask_question 选项/危险确认渲染 inline keyboard，
//     按钮点击归一化为 IsCallback 的 InboundMessage 回流（HITL 组装零改动）；
//   - MessageEditor：占位消息「⏳ 正在处理…」终态 editMessageText 原位替换；
//   - LongTextSplitter：PushText 对超长文本按单条上限自动分片（不再截断）。
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
func (t *telegramChannel) SplitsLongText() bool { return true }

// telegramChunkRunes 单条分片上限（Telegram 官方 4096 字符，留 markdown/实体余量）。
const telegramChunkRunes = 3500

// telegramCallbackDataLimit 是 callback_data 的 1-64 字节 API 上限。
const telegramCallbackDataLimit = 64

// hitlCallbackPrefix HITL 按钮 callback_data 前缀；格式 h|<toolUseId>|<作答文本>。
const hitlCallbackPrefix = "h"

// encodeHitlCallbackData / parseHitlCallbackData HITL 按钮数据的编解码
//（toolUseId 用于过期点击校验，作答文本与文本回复同路组装）。
func encodeHitlCallbackData(toolUseID, value string) string {
	return hitlCallbackPrefix + "|" + toolUseID + "|" + value
}

func parseHitlCallbackData(data string) (toolUseID, value string) {
	parts := strings.SplitN(data, "|", 3)
	if len(parts) != 3 || parts[0] != hitlCallbackPrefix {
		return "", ""
	}
	return parts[1], parts[2]
}

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

	// 注册原生命令菜单（best-effort：失败仅记日志，不影响收发）。
	// 注意：telebot 的 SetCommands 只识别 []Command 直传（CommandParams 会被
	// extractCommandsParams 忽略成空清单），不要包一层 CommandParams。
	if err := bot.SetCommands(telegramBotCommands()); err != nil {
		zlog.Warnf(nil, "[gateway.telegram] setMyCommands 失败(不影响收发): %v", err)
	}

	bot.Handle(telebot.OnText, func(c telebot.Context) error {
		inbound := t.toInbound(c.Message())
		if inbound == nil {
			return nil
		}
		if !t.allowsChat(inboundChatOf(c.Message()), inboundSenderOf(c.Message())) {
			// 私聊给一句可读提示；群聊静默（避免刷屏）。
			if m := c.Message(); m != nil && m.Chat != nil && m.Chat.Type == telebot.ChatPrivate {
				_, _ = bot.Send(m.Chat, "⛔️ 未授权使用该机器人，请联系管理员添加白名单。")
			}
			zlog.Warnf(nil, "[gateway.telegram] 渠道 %s 白名单外消息已拒绝: chat=%s user=%s", t.cfg.ChannelID, inbound.ChatID, inbound.UserID)
			return nil
		}
		// handler 内部自行异步；OnText 需快速返回以维持轮询吞吐。
		go handler(context.Background(), *inbound)
		return nil
	})

	// inline keyboard 按钮点击：Respond 消除按钮加载态后，归一化为 Callback 入站消息
	// （Text=作答文本，CallbackToolUseID=指向的交互），走 handleInbound 分支 0 作答。
	bot.Handle(telebot.OnCallback, func(c telebot.Context) error {
		cb := c.Callback()
		if cb == nil || cb.Message == nil || cb.Message.Chat == nil {
			return nil
		}
		if !t.allowsChat(cb.Message.Chat, cb.Sender) {
			_ = c.Respond(&telebot.CallbackResponse{Text: "未授权"})
			return nil
		}
		toolUseID, value := parseHitlCallbackData(cb.Data)
		if value == "" {
			_ = c.Respond(&telebot.CallbackResponse{Text: "按钮已失效"})
			return nil
		}
		_ = c.Respond(&telebot.CallbackResponse{Text: "已记录"})
		inbound := &InboundMessage{
			ChannelID:         t.cfg.ChannelID,
			Kind:              model.ChannelKindTelegram,
			ChatID:            strconv.FormatInt(cb.Message.Chat.ID, 10),
			ChatType:          telegramChatType(cb.Message.Chat.Type),
			UserID:            telegramUserID(cb.Sender),
			MessageID:         strconv.Itoa(cb.Message.ID),
			Text:              value,
			IsCallback:        true,
			CallbackToolUseID: toolUseID,
		}
		go handler(context.Background(), *inbound)
		return nil
	})

	go func() {
		bot.Start() // 阻塞至 Stop
	}()
	zlog.Infof(nil, "[gateway.telegram] 渠道 %s 长轮询已启动: bot=@%s", t.cfg.ChannelID, t.username)
	return nil
}

// telegramBotCommands 原生命令菜单（网关侧命令处理见 gateway.go handleInbound）。
func telegramBotCommands() []telebot.Command {
	return []telebot.Command{
		{Text: "new", Description: "开启新会话（可带首条消息）"},
		{Text: "sessions", Description: "查看本聊天最近的会话"},
		{Text: "cancel", Description: "取消当前执行中的任务"},
		{Text: "help", Description: "使用说明"},
	}
}

func (t *telegramChannel) Stop(ctx context.Context) error {
	t.stopOnce.Do(func() {
		if t.bot != nil {
			t.bot.Stop()
		}
	})
	return nil
}

// PushText 推送文本；超长自动分片（每片以前一片为回复锚点形成线程），
// 返回最后一片的平台消息 ID（写入消息绑定，回复末片即续聊）。
// 先按 markdown 解析发送，Telegram 解析报错（语法不兼容）时回退纯文本（保留锚点）。
func (t *telegramChannel) PushText(ctx context.Context, addr PushAddr, text string) (string, error) {
	if t.bot == nil {
		return "", errChannelNotStarted
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(addr.ChatID), 10, 64)
	if err != nil {
		return "", err
	}
	lastID := ""
	replyID := addr.ReplyMessageID
	for _, chunk := range splitTelegramText(text) {
		sent, err := t.sendText(chatID, chunk, replyID, nil)
		if err != nil {
			return "", err
		}
		lastID = strconv.Itoa(sent.ID)
		replyID = lastID
	}
	if lastID == "" {
		return "", nil
	}
	return lastID, nil
}

// PushInteractive 推送带 inline keyboard 的消息（ask_question 选项/危险确认）；
// markdown 解析失败回退纯文本（按钮保留）。
func (t *telegramChannel) PushInteractive(ctx context.Context, addr PushAddr, iv InteractivePayload) (string, error) {
	if t.bot == nil {
		return "", errChannelNotStarted
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(addr.ChatID), 10, 64)
	if err != nil {
		return "", err
	}
	keyboard := make([][]telebot.InlineButton, 0, len(iv.Rows))
	for _, row := range iv.Rows {
		buttons := make([]telebot.InlineButton, 0, len(row))
		for _, btn := range row {
			buttons = append(buttons, telebot.InlineButton{Text: btn.Text, Data: btn.CallbackData})
		}
		keyboard = append(keyboard, buttons)
	}
	markup := &telebot.ReplyMarkup{InlineKeyboard: keyboard}
	sent, err := t.sendText(chatID, iv.Text, addr.ReplyMessageID, markup)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(sent.ID), nil
}

// EditText 原位编辑已推送消息（占位消息终态替换）；markdown 解析失败回退纯文本。
// 编辑失败（超长/消息不可编辑等）返回错误，调用方回退推送新消息。
func (t *telegramChannel) EditText(ctx context.Context, chatIDStr, messageIDStr, text string) error {
	if t.bot == nil {
		return errChannelNotStarted
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(chatIDStr), 10, 64)
	if err != nil {
		return err
	}
	messageID, err := strconv.Atoi(strings.TrimSpace(messageIDStr))
	if err != nil {
		return err
	}
	editable := &telebot.Message{ID: messageID, Chat: &telebot.Chat{ID: chatID}}
	if _, err := t.bot.Edit(editable, text, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
		if _, plainErr := t.bot.Edit(editable, text); plainErr != nil {
			return plainErr
		}
	}
	return nil
}

// sendText 单条发送：markdown 优先，解析报错回退纯文本（回复锚点与按钮保留）。
func (t *telegramChannel) sendText(chatID int64, text, replyMessageID string, markup *telebot.ReplyMarkup) (*telebot.Message, error) {
	opts := &telebot.SendOptions{ParseMode: telebot.ModeMarkdown, ReplyMarkup: markup}
	if replyMessageID != "" {
		if id, err := strconv.Atoi(replyMessageID); err == nil {
			opts.ReplyTo = &telebot.Message{ID: id}
		}
	}
	sent, err := t.bot.Send(telebot.ChatID(chatID), text, opts)
	if err != nil {
		fallback := &telebot.SendOptions{ReplyMarkup: markup}
		if opts.ReplyTo != nil {
			fallback.ReplyTo = opts.ReplyTo
		}
		return t.bot.Send(telebot.ChatID(chatID), text, fallback)
	}
	return sent, nil
}

// splitTelegramText 把超长文本切成单条上限内的分片：优先在换行处断开
//（向前回看窗口内最近一个 \n），否则硬切；不追加分片序号，保持内容纯净。
func splitTelegramText(text string) []string {
	runes := []rune(text)
	if len(runes) <= telegramChunkRunes {
		return []string{text}
	}
	chunks := make([]string, 0, len(runes)/telegramChunkRunes+1)
	for len(runes) > telegramChunkRunes {
		cut := telegramChunkRunes
		lookback := telegramChunkRunes - 500
		if lookback < 0 {
			lookback = 0
		}
		for i := cut - 1; i >= lookback; i-- {
			if runes[i] == '\n' {
				cut = i + 1
				break
			}
		}
		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}

// allowsChat 白名单判定（候选：chat ID、发送者 user ID、@username）。
// 空清单 = 不限（M1 兼容）；群 chat ID 命中即放行该群。
func (t *telegramChannel) allowsChat(chat *telebot.Chat, sender *telebot.User) bool {
	allowed := parseAllowedUsers(t.cfg.AllowedUsers)
	if len(allowed) == 0 {
		return true
	}
	candidates := make([]string, 0, 3)
	if chat != nil {
		candidates = append(candidates, strconv.FormatInt(chat.ID, 10))
	}
	if sender != nil {
		candidates = append(candidates, strconv.FormatInt(sender.ID, 10))
		if sender.Username != "" {
			candidates = append(candidates, "@"+sender.Username)
		}
	}
	return channelAllowsSender(allowed, candidates...)
}

// inboundChatOf / inboundSenderOf 安全取消息的 chat/sender（nil 消息容错）。
func inboundChatOf(m *telebot.Message) *telebot.Chat {
	if m == nil {
		return nil
	}
	return m.Chat
}

func inboundSenderOf(m *telebot.Message) *telebot.User {
	if m == nil {
		return nil
	}
	return m.Sender
}

// telegramChatType 平台会话形态归一（telebot 常量 → p2p/group）。
func telegramChatType(chatType telebot.ChatType) string {
	switch chatType {
	case telebot.ChatGroup, telebot.ChatSuperGroup:
		return "group"
	default:
		return "p2p"
	}
}

// telegramUserID 发送者 ID 归一化字符串。
func telegramUserID(sender *telebot.User) string {
	if sender == nil {
		return ""
	}
	return strconv.FormatInt(sender.ID, 10)
}

// toInbound 把 telebot 消息转成网关入站消息；私聊全量响应，群聊仅响应 @机器人
// （剥离提及文本）；ReplyTo 消息映射为 ParentMessageID 命中续聊绑定；
// 命令词的 @bot 后缀（群聊客户端自动附加，如 /cancel@xx_bot）剥离归一。
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
		text = stripCommandMentionSuffix(text)
	default:
		return nil
	}
	parentID := ""
	if m.ReplyTo != nil {
		parentID = strconv.Itoa(m.ReplyTo.ID)
	}
	return &InboundMessage{
		ChannelID:       t.cfg.ChannelID,
		Kind:            model.ChannelKindTelegram,
		ChatID:          strconv.FormatInt(m.Chat.ID, 10),
		ChatType:        chatType,
		UserID:          telegramUserID(m.Sender),
		MessageID:       strconv.Itoa(m.ID),
		ParentMessageID: parentID,
		Text:            text,
	}
}

// stripCommandMentionSuffix 剥离私聊命令词的 @bot 后缀（/cancel@xx_bot → /cancel）；
// 群聊路径经提及剥离已无后缀，非命令文本原样返回。
func stripCommandMentionSuffix(text string) string {
	if !strings.HasPrefix(text, "/") {
		return text
	}
	parts := strings.SplitN(text, " ", 2)
	if i := strings.Index(parts[0], "@"); i > 1 {
		parts[0] = parts[0][:i]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return parts[0] + " " + parts[1]
}
