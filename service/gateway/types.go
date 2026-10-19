// Package gateway 是多渠道接入网关：IM 渠道收发 + 跨端投递 + 同一会话续聊。
//
// 设计（见 docs/多渠道接入网关与跨端续聊实现方案.md）：
//   - 渠道抽象 Channel：每渠道自管传输（飞书 WS 长连接等），收消息归一化为 InboundMessage
//     后交给 Handler；出站统一走 PushText；
//   - 会话路由三分支：消息绑定命中（IM 回复已投递消息）→ 续进原会话；平台会话映射 →
//     复用绑定会话；都没有 → 按渠道配置默认路由新建会话并落映射；
//   - IM 适配器 imAdapter：把 ReAct 运行时投影到 IM——终态折叠推送（写消息绑定）、
//     ask_question/危险确认推成 IM 消息、IM 回复按 toolUseId 回填 tool_use_answer；
//   - 与 ReAct 运行时的接缝只有 RunWithClientReaderContext 一个调用（身份经
//     NewHeadlessGinContext 注入，Steering/排队/HITL/取消全部复用运行时既有机制，
//     运行时核心零改动）。
//
// 依赖方向：gateway → react（单向）；react 不感知 gateway。
package gateway

import (
	"context"
	"errors"
)

// errChannelNotStarted 渠道尚未拉起长连接（配置存在但未启用/未 Start）。
var errChannelNotStarted = errors.New("渠道连接未启动")

// InboundMessage 是渠道归一化后的入站消息。
type InboundMessage struct {
	// ChannelID 是渠道配置标识（tblLlmChannelConfig.channel_id，gwch_ 前缀）。
	ChannelID string
	// Kind 是渠道类型（feishu 等）。
	Kind string
	// ChatID 是平台会话标识（飞书 chat_id）；一个 chat 绑定一个 ReAct 会话。
	ChatID string
	// ChatType 是平台会话形态："p2p" | "group"。
	ChatType string
	// UserID 是发送者平台身份（open_id）。
	UserID string
	// MessageID 是本条消息的平台 ID。
	MessageID string
	// ParentMessageID 非空表示本条是对某条消息的回复（飞书 parent_id）；
	// 命中消息绑定表时走续聊路由。
	ParentMessageID string
	// Text 是归一化文本（Callback 时即按钮携带的作答文本）。
	Text string
	// IsCallback 为 true 表示按钮回调（inline keyboard 点击）而非普通文本消息；
	// 走 handleInbound 分支 0 与文本作答同路，HITL 组装零改动。
	IsCallback bool
	// CallbackToolUseID 是按钮回调指向的交互 ID（callback_data 携带），过期点击校验用。
	CallbackToolUseID string
}

// PushAddr 是出站寻址：投递到哪个 chat、以哪条消息为回复锚点。
type PushAddr struct {
	ChannelID       string
	ChatID          string
	ReplyMessageID  string // 可选：以回复该消息的方式投递（线程感）
}

// Channel 是渠道抽象：一个实例对应一个渠道配置（一个 IM 机器人应用），自管传输。
type Channel interface {
	// Kind 返回渠道类型（feishu）。
	Kind() string
	// ChannelID 返回对应的渠道配置标识。
	ChannelID() string
	// Start 拉起传输（阻塞至连接就绪或失败）；收到的消息归一化后回调 handler。
	Start(ctx context.Context, handler MessageHandler) error
	// Stop 停止传输。
	Stop(ctx context.Context) error
	// PushText 推送文本（markdown 容忍：渠道自行降级），返回平台消息 ID（写入消息绑定）。
	PushText(ctx context.Context, addr PushAddr, text string) (messageID string, err error)
}

// MessageHandler 是入站消息回调；实现方自行决定同步或异步处理。
type MessageHandler func(ctx context.Context, msg InboundMessage)

// ---------------------------------------------------------------------------
// 可选渠道能力接口：渠道按传输能力实现，网关层类型断言后启用对应增强
// （未实现的渠道行为不变，M1 基线兼容）。
// ---------------------------------------------------------------------------

// InteractivePayload 是带按钮组的出站交互（ask_question 选项 / 危险确认）。
type InteractivePayload struct {
	// Text 是交互正文（题面/确认说明），与纯文本推送同构。
	Text string
	// Rows 是按钮行（每行一组横排按钮）。
	Rows [][]InteractiveButton
}

// InteractiveButton 是一枚交互按钮；CallbackData 由网关侧编码（hitl callback_data）。
type InteractiveButton struct {
	Text         string
	CallbackData string
}

// InteractivePusher 可选能力：支持按钮交互推送（Telegram inline keyboard）。
// 按钮点击由渠道归一化为 IsCallback 的 InboundMessage 回流（callback_data 带
// toolUseId + 作答文本），进 handleInbound 分支 0，HITL 组装零改动；
// 飞书交互卡片（M3）将来同模式复用。
type InteractivePusher interface {
	PushInteractive(ctx context.Context, addr PushAddr, iv InteractivePayload) (messageID string, err error)
}

// MessageEditor 可选能力：编辑已推送消息（Telegram editMessageText）。
// 网关在 run 启动时先投「⏳ 正在处理…」占位消息，终态原位替换为结果，减少消息噪音。
type MessageEditor interface {
	EditText(ctx context.Context, chatID, messageID, text string) error
}

// LongTextSplitter 可选能力：PushText 内部对超长文本自动分片发送
// （Telegram 单条 4096 上限）。实现该接口的渠道网关层跳过 push_max_runes 截断
// （截断会破坏分片语义）。
type LongTextSplitter interface {
	SplitsLongText() bool
}

// channelSplitsLongText 判断渠道是否自行分片长文本。
func channelSplitsLongText(ch Channel) bool {
	s, ok := ch.(LongTextSplitter)
	return ok && s.SplitsLongText()
}
