package gateway

import (
	"context"
	"encoding/json"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkchannel "github.com/larksuite/oapi-sdk-go/v3/channel"
	larktypes "github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
)

// feishuChannel 是飞书渠道实现：基于官方 oapi-sdk-go/v3 的 channel 模块
// （WS 长连接收事件 + 消息归一化 + markdown 发送），本层只做 InboundMessage
// 归一化与网关 Channel 接口的适配。
//
// 前置要求（开发者后台）：应用开启「事件与回调 → 长连接」订阅方式，
// 订阅 im.message.receive_v1 事件，并具备 im:message 发消息权限。
type feishuChannel struct {
	cfg *model.ChannelConfig

	cancel  context.CancelFunc
	channel larktypes.Channel
}

func newFeishuChannel(cfg *model.ChannelConfig) *feishuChannel {
	return &feishuChannel{cfg: cfg}
}

func (f *feishuChannel) Kind() string      { return model.ChannelKindFeishu }
func (f *feishuChannel) ChannelID() string { return f.cfg.ChannelID }

// Start 拉起 WS 长连接（SDK 内部自带断线重连与事件去重）；连接生命周期独立于
// 调用 context，由 Stop 关闭。
//
// 注意：larkws.NewClient 默认不创建事件分发器，而 channel 模块在 wsClient 无分发器时
// 会静默跳过 OnMessage 注册（ensureMessageHandler 的 nil 守卫），消息帧在 ws 层即被丢弃
// （client_message.go: eventHandler == nil → return，仅 Debug 级日志）。因此必须显式
// 携带一个空分发器（长连接模式无需 verificationToken/encryptKey，webhook 模式才需要）。
func (f *feishuChannel) Start(ctx context.Context, handler MessageHandler) error {
	client := lark.NewClient(f.cfg.AppID, f.cfg.AppSecret)
	wsClient := larkws.NewClient(f.cfg.AppID, f.cfg.AppSecret,
		larkws.WithEventHandler(larkevent.NewEventDispatcher("", "")))
	ch := larkchannel.NewChannel(client, wsClient)

	ch.OnMessage(func(msgCtx context.Context, msg *larktypes.NormalizedMessage) error {
		inbound := f.toInbound(msg)
		if inbound == nil {
			return nil
		}
		// SDK 要求 OnMessage 快速返回；run 编排在网关侧 goroutine 进行。
		// msgCtx 随回调返回可能被取消，异步侧一律用独立 context。
		go handler(context.Background(), *inbound)
		return nil
	})
	ch.OnError(func(err error) {
		zlog.Errorf(nil, "[gateway.feishu] 渠道 %s 连接错误: %v", f.cfg.ChannelID, err)
	})
	ch.OnDisconnected(func() {
		zlog.Warnf(nil, "[gateway.feishu] 渠道 %s 连接断开（SDK 将自动重连）", f.cfg.ChannelID)
	})

	runCtx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.channel = ch
	go func() {
		if err := ch.Start(runCtx); err != nil {
			zlog.Errorf(nil, "[gateway.feishu] 渠道 %s 长连接退出: %v", f.cfg.ChannelID, err)
		}
	}()
	return nil
}

func (f *feishuChannel) Stop(ctx context.Context) error {
	if f.cancel != nil {
		f.cancel()
	}
	if f.channel != nil {
		return f.channel.Stop(ctx)
	}
	return nil
}

// PushText 以 markdown（SDK 自动转 post）推送；带回复锚点时以回复方式投递。
func (f *feishuChannel) PushText(ctx context.Context, addr PushAddr, text string) (string, error) {
	if f.channel == nil {
		return "", errChannelNotStarted
	}
	input := &larktypes.SendInput{
		ChatID:   addr.ChatID,
		Markdown: text,
	}
	if addr.ReplyMessageID != "" {
		input.ReplyMessageID = addr.ReplyMessageID
	}
	result, err := f.channel.Send(ctx, input)
	if err != nil {
		return "", err
	}
	if result.Error != nil {
		return "", result.Error
	}
	return result.MessageID, nil
}

// toInbound 把 SDK 归一化消息转成网关入站消息；不可处理（空文本、群聊未 @ 机器人）返回 nil。
func (f *feishuChannel) toInbound(msg *larktypes.NormalizedMessage) *InboundMessage {
	if msg == nil {
		return nil
	}
	text := strings.TrimSpace(msg.Content)
	if text == "" {
		return nil
	}
	// 群聊只响应 @ 机器人；单聊全量响应。
	if msg.ChatType == "group" && !msg.MentionedBot {
		return nil
	}
	return &InboundMessage{
		ChannelID:       f.cfg.ChannelID,
		Kind:            model.ChannelKindFeishu,
		ChatID:          msg.ChatID,
		ChatType:        msg.ChatType,
		UserID:          msg.UserID,
		MessageID:       msg.MessageID,
		ParentMessageID: extractFeishuParentID(msg.RawEvent),
		Text:            text,
	}
}

// extractFeishuParentID 从原始事件提取回复目标的 parent_id（仅回复消息非空）。
// RawEvent 是 SDK 内部事件结构，走 JSON 泛化提取避免绑定具体类型版本。
func extractFeishuParentID(raw interface{}) string {
	if raw == nil {
		return ""
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	var parsed struct {
		Event struct {
			Message struct {
				ParentID string `json:"parent_id"`
			} `json:"message"`
		} `json:"event"`
		Data struct {
			Message struct {
				ParentID string `json:"parent_id"`
			} `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return ""
	}
	if parsed.Event.Message.ParentID != "" {
		return parsed.Event.Message.ParentID
	}
	return parsed.Data.Message.ParentID
}
