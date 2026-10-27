package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
	react "react-base-service/service/react"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"react-base-service/golib/base"
	"react-base-service/golib/zlog"
)

// RunInvoker 与 react.RunWithClientReaderContext 同构的调用签名（测试可注入替身）。
type RunInvoker func(ctx *gin.Context, parent context.Context, payload params.ReactRunPayload, sessionID string, write react.EventWriter, readClient react.ClientMessageReader) (*react.RunResult, error)

// service 是网关单例：持有渠道 Manager、chat 级活跃 adapter 注册表，
// 承担入站路由（续聊三分支）与 run 编排。
type service struct {
	manager *Manager

	mu       sync.Mutex
	adapters map[string]*imAdapter // key: channelID + "/" + chatID

	runInvoker RunInvoker
	// resolveDeliveryTargets 跨端投递目标解析（默认查库；测试注入替身）。
	resolveDeliveryTargets func(ctx *gin.Context, userName, excludeChannelID string) ([]model.ChannelSession, error)
}

var (
	impl *service
	once sync.Once
)

// Start 启动网关：llm.gateway.enabled 时加载渠道并拉起；未启用时空转（不报错）。
// 由 router.Tasks 调用（与 workflow.Start 同级）。
func Start(engine *gin.Engine) {
	_ = engine
	if !conf.CustomConf.LLM.Gateway.Enabled {
		return
	}
	getService().start()
}

// Stop 停止网关（停全部渠道；进行中的 run 由各自 context 收敛）。
func Stop() {
	if impl == nil {
		return
	}
	impl.manager.Stop(context.Background())
}

// Reload 渠道配置变更后重建渠道连接（CRUD 控制器调用）。
func Reload() error {
	if impl == nil {
		// 网关未启用时配置仍可管理，只是不拉连接；Reload 无副作用。
		return nil
	}
	return impl.manager.Reload(context.Background())
}

func getService() *service {
	once.Do(func() {
		s := &service{
			adapters:               map[string]*imAdapter{},
			runInvoker:             react.RunWithClientReaderContext,
			resolveDeliveryTargets: defaultResolveDeliveryTargets,
		}
		s.manager = newManager(s.handleInbound)
		impl = s
	})
	return impl
}

func (s *service) start() {
	if err := s.manager.Start(context.Background()); err != nil {
		zlog.Errorf(nil, "[gateway] 启动失败: %v", err)
		return
	}
	zlog.Infof(nil, "[gateway] 已启动")
}

// newGatewayGinContext 构造网关侧无 HTTP 请求的 gin.Context（模型层取用户名等需要）。
func newGatewayGinContext() *gin.Context {
	return react.NewHeadlessGinContext("gateway")
}

// pushText 经渠道推送文本（adapter 与服务自身共用出口）。
func (s *service) pushText(ctx context.Context, addr PushAddr, text string) (string, error) {
	ch := s.manager.Get(addr.ChannelID)
	if ch == nil {
		return "", fmt.Errorf("渠道 %s 未运行", addr.ChannelID)
	}
	return ch.PushText(ctx, addr, text)
}

// pushBounded 按渠道能力推送：分片渠道（LongTextSplitter）发全量自行拆条，
// 其余渠道按 push_max_runes 截断兜底（跨端投递与终态推送共用口径）。
func (s *service) pushBounded(ctx context.Context, addr PushAddr, text string) (string, error) {
	if ch := s.manager.Get(addr.ChannelID); ch != nil && !channelSplitsLongText(ch) {
		if maxRunes := conf.CustomConf.LLM.Gateway.GatewayPushMaxRunes(); len([]rune(text)) > maxRunes {
			text = string([]rune(text)[:maxRunes]) + "\n\n…(内容过长已截断，完整结果见会话)"
		}
	}
	return s.pushText(ctx, addr, text)
}

// ---------------------------------------------------------------------------
// 入站路由：续聊三分支 + run 编排
// ---------------------------------------------------------------------------

// sessionSnapshot 是路由解析出的会话上下文（五元组粘滞来源）。
type sessionSnapshot struct {
	SessionID   string
	UserName    string
	AppKey      string
	RouteValues string
	SessionType string
	ChatRowID   uint
}

func (s *service) handleInbound(ctx context.Context, msg InboundMessage) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	// 斜杠命令（渠道无关）：/new /sessions /cancel /help /start（Telegram START 按钮即 /start）。
	// 命令在本层消化，不进入会话路由；/cancel 或「取消」中止当前 run。
	cmd, rest := parseInboundCommand(text)
	forceNew := false
	switch cmd {
	case "/cancel":
		s.cancelActiveRun(msg)
		return
	case "/help", "/start":
		s.pushNotice(msg, gatewayHelpText)
		return
	case "/sessions":
		s.pushSessionList(newGatewayGinContext(), msg)
		return
	case "/new":
		if strings.TrimSpace(rest) == "" {
			ginCtx := newGatewayGinContext()
			if _, err := s.snapshotFromConfig(ginCtx, msg, generateGatewaySessionID(), true); err != nil {
				zlog.Errorf(ginCtx, "[gateway] 新会话建立失败: channel=%s chat=%s err=%v", msg.ChannelID, msg.ChatID, err)
				s.pushNotice(msg, "❌ 新会话建立失败："+err.Error())
				return
			}
			s.pushNotice(msg, "🆕 已开启新会话，直接发消息开始。")
			return
		}
		// /new 带正文：强制新会话并以正文作为首条消息立即执行。
		forceNew = true
		text = strings.TrimSpace(rest)
	default:
		if cmd == "" && strings.EqualFold(text, "取消") {
			s.cancelActiveRun(msg)
			return
		}
	}

	ginCtx := newGatewayGinContext()

	// 分支 0：当前 run 正在等待 IM 作答（ask_question/危险确认）——本条回复直接投给它。
	if a := s.activeAdapter(msg.ChannelID, msg.ChatID); a != nil {
		if p := a.takePending(); p != nil {
			// 按钮回调带 toolUseId：过期点击（问题已被取代/已作答）不投递，提示后丢弃。
			if msg.IsCallback && msg.CallbackToolUseID != "" && msg.CallbackToolUseID != p.ToolUseID {
				s.pushNotice(msg, "⚠️ 该问题已失效，请按最新消息作答")
				return
			}
			a.deliverReply(text)
			return
		}
	}

	// 路由三分支：消息绑定命中 → 平台会话映射 → 新建（/new 强制走新建并改绑）。
	snapshot, err := s.routeSession(ginCtx, msg, forceNew)
	if err != nil {
		zlog.Errorf(ginCtx, "[gateway] 会话路由失败: channel=%s chat=%s err=%v", msg.ChannelID, msg.ChatID, err)
		return
	}
	_ = model.TouchChannelSessionActive(ginCtx, snapshot.ChatRowID, truncateForDisplay(text))

	payload := buildRunPayload(snapshot, text)
	// 身份注入：五元组粘滞的 userName 进 gin context，resolveReactUserName 现有分支直接命中。
	runGinCtx := react.NewHeadlessGinContext(snapshot.UserName)
	runCtx, cancel := context.WithCancel(context.Background())

	adapter := newIMAdapter(s, msg.ChannelID, msg.ChatID, msg.MessageID, snapshot.SessionID, snapshot.UserName, cancel)
	// 回放信息（M2 跨端投递链接用）：appKey/routeValues 进投递消息的 replay 链接参数。
	adapter.setReplayInfo(snapshot.AppKey, snapshot.RouteValues)
	s.mountAdapter(msg.ChannelID, msg.ChatID, adapter)

	// 占位消息（渠道支持编辑时）：终态 editMessageText 原位替换为结果，减少消息噪音。
	if phID, ok := s.sendPlaceholder(msg); ok {
		adapter.setPlaceholder(phID)
	}

	go func() {
		defer func() {
			cancel()
			adapter.finish()
			s.unmountAdapter(msg.ChannelID, msg.ChatID, adapter)
		}()
		result, err := s.runInvoker(runGinCtx, runCtx, payload, snapshot.SessionID, adapter.writer(), adapter.reader())
		if err != nil {
			// 准入回执（引导注入/排队）不是失败：活动 run 的 writer 会在收敛时推送结果。
			if receipt, ok := react.SteerReceiptFromError(err); ok {
				zlog.Infof(runGinCtx, "[gateway] 输入已准入: kind=%s run=%s queue=%d", receipt.Kind, receipt.RunID, receipt.QueueLength)
				adapter.notifyIfPlaceholder("📨 输入已并入当前执行中的任务")
				return
			}
			if react.IsReactRunCancelled(err) {
				zlog.Infof(runGinCtx, "[gateway] run 已取消: session=%s", snapshot.SessionID)
				// 停机收敛保持不推送新消息（对齐 workflow notify 决策），仅原位收尾占位消息。
				adapter.notifyIfPlaceholder("🛑 已取消")
				return
			}
			// Steering 关闭时会话活跃时直接拒绝：给用户可读提示而非报错堆栈。
			if isReactRunActiveError(err) {
				adapter.notify("⏳ 当前任务仍在执行中，请稍候再发，或回复「取消」中止。")
				return
			}
			// 启动/运行失败且没有任何终态事件可推送时兜底告知用户。
			if !adapter.pushedTerminal() {
				adapter.notify("❌ 执行失败：" + err.Error())
			}
			zlog.Errorf(runGinCtx, "[gateway] run 失败: session=%s err=%v", snapshot.SessionID, err)
			return
		}
		zlog.Infof(runGinCtx, "[gateway] run 完成: session=%s run=%s", result.SessionID, result.RunID)
	}()
}

// parseInboundCommand 拆出入站文本开头的斜杠命令：非 "/" 开头返回空 cmd；
// Telegram 群聊命令带 @bot 后缀（/cancel@xx_bot），在命令词上剥离归一。
func parseInboundCommand(text string) (cmd, rest string) {
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	parts := strings.SplitN(text, " ", 2)
	cmd = strings.ToLower(parts[0])
	if i := strings.Index(cmd, "@"); i > 1 {
		cmd = cmd[:i]
	}
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}
	return cmd, rest
}

// gatewayHelpText /help /start（Telegram START 按钮）的使用说明。
const gatewayHelpText = `🤖 使用说明

直接发消息即可对话；回复任意一条结果消息，可续聊该消息所在会话（跨话题互不串扰）。

/new [消息] — 开启新会话（可带首条消息，如「/new 帮我查库存」）
/sessions — 查看本聊天最近的会话
/cancel 或「取消」 — 中止当前执行中的任务
/help — 本说明

需要补充信息时回复选项序号（多选用逗号分隔）或直接输入文本；危险操作会请求「允许/拒绝」确认。`

// pushNotice 推送过程性通知（命令回执等），以触发消息为回复锚点。
func (s *service) pushNotice(msg InboundMessage, text string) {
	_, _ = s.pushText(context.Background(), PushAddr{
		ChannelID:      msg.ChannelID,
		ChatID:         msg.ChatID,
		ReplyMessageID: msg.MessageID,
	}, text)
}

// pushSessionList 推送该聊天最近的会话映射（/sessions 命令）。
func (s *service) pushSessionList(ginCtx *gin.Context, msg InboundMessage) {
	rows, err := model.ListChannelSessionsByChat(ginCtx, msg.ChannelID, msg.ChatID, 5)
	if err != nil {
		zlog.Errorf(ginCtx, "[gateway] 会话列表查询失败: %v", err)
		s.pushNotice(msg, "❌ 会话查询失败")
		return
	}
	current, err := model.GetChannelSessionByChat(ginCtx, msg.ChannelID, msg.ChatID)
	if err != nil {
		zlog.Errorf(ginCtx, "[gateway] 当前会话查询失败: %v", err)
		s.pushNotice(msg, "❌ 会话查询失败")
		return
	}
	currentSession := ""
	if current != nil {
		currentSession = current.SessionID
	}
	s.pushNotice(msg, buildSessionListText(rows, currentSession))
}

// buildSessionListText 渲染会话列表文案（当前绑定会话打标，含续聊指引）。
func buildSessionListText(rows []model.ChannelSession, currentSession string) string {
	if len(rows) == 0 {
		return "📭 本聊天还没有会话记录，直接发消息即可开始。"
	}
	var sb strings.Builder
	sb.WriteString("📋 本聊天最近的会话：\n")
	for i, row := range rows {
		marker := "  "
		if row.SessionID == currentSession {
			marker = "👉"
		}
		lastMessage := row.LastMessage
		if strings.TrimSpace(lastMessage) == "" {
			lastMessage = "（无消息）"
		}
		if runes := []rune(lastMessage); len(runes) > 40 {
			lastMessage = string(runes[:40]) + "…"
		}
		sb.WriteString(fmt.Sprintf("%s %d. %s\n     %s · %s\n", marker, i+1,
			shortSessionID(row.SessionID), lastMessage, row.LastActiveAt.Format("01-02 15:04")))
	}
	sb.WriteString("\n直接发消息续聊当前会话；回复任意一条结果消息可续聊其对应会话。")
	return sb.String()
}

// shortSessionID 会话 ID 展示缩写（session_ 前缀截断，保序可辨识）。
func shortSessionID(sessionID string) string {
	if len(sessionID) > 16 {
		return sessionID[:16] + "…"
	}
	return sessionID
}

// gatewayPlaceholderText 占位消息文案（终态原位编辑替换）。
const gatewayPlaceholderText = "⏳ 正在处理…"

// sendPlaceholder 渠道支持消息编辑时投占位消息（终态原位替换）；不支持/失败返回 false。
func (s *service) sendPlaceholder(msg InboundMessage) (string, bool) {
	ch := s.manager.Get(msg.ChannelID)
	if ch == nil {
		return "", false
	}
	if _, ok := ch.(MessageEditor); !ok {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := s.pushText(ctx, PushAddr{
		ChannelID:      msg.ChannelID,
		ChatID:         msg.ChatID,
		ReplyMessageID: msg.MessageID,
	}, gatewayPlaceholderText)
	if err != nil || id == "" {
		return "", false
	}
	return id, true
}

// editText 编辑已推送消息（渠道实现 MessageEditor 时可用）；不支持/失败返回错误，调用方回退推送。
func (s *service) editText(ctx context.Context, channelID, chatID, messageID, text string) error {
	ch := s.manager.Get(channelID)
	if ch == nil {
		return fmt.Errorf("渠道 %s 未运行", channelID)
	}
	editor, ok := ch.(MessageEditor)
	if !ok {
		return fmt.Errorf("渠道 %s 不支持消息编辑", channelID)
	}
	return editor.EditText(ctx, chatID, messageID, text)
}

// routeSession 解析本条消息归属的会话（三分支）与五元组快照。
// forceNew（/new 命令）跳过续聊分支，直接按渠道默认路由改绑新会话。
func (s *service) routeSession(ginCtx *gin.Context, msg InboundMessage, forceNew bool) (*sessionSnapshot, error) {
	if !forceNew {
		// 分支 1：消息绑定命中——本条是对已投递结果消息的回复，续进原会话。
		if msg.ParentMessageID != "" {
			binding, err := model.GetChannelMessageByMessageID(ginCtx, msg.ChannelID, msg.ParentMessageID)
			if err != nil {
				return nil, err
			}
			if binding != nil {
				if row, err := model.GetChannelSessionByChat(ginCtx, msg.ChannelID, binding.ChatID); err != nil {
					return nil, err
				} else if row != nil {
					return snapshotFromRow(row), nil
				}
				// 绑定存在但 chat 映射缺失（被清理等边缘）：按绑定会话 + 渠道默认路由兜底。
				return s.snapshotFromConfig(ginCtx, msg, binding.SessionID, false)
			}
		}

		// 分支 2：平台会话映射——该 chat 已绑定会话，直接续聊（粘滞五元组）。
		if row, err := model.GetChannelSessionByChat(ginCtx, msg.ChannelID, msg.ChatID); err != nil {
			return nil, err
		} else if row != nil {
			return snapshotFromRow(row), nil
		}
	}

	// 分支 3：新建——按渠道配置默认路由生成会话并落映射（幂等：并发首条消息由唯一键收敛）。
	return s.snapshotFromConfig(ginCtx, msg, generateGatewaySessionID(), forceNew)
}

func snapshotFromRow(row *model.ChannelSession) *sessionSnapshot {
	return &sessionSnapshot{
		SessionID:   row.SessionID,
		UserName:    row.UserName,
		AppKey:      row.AppKey,
		RouteValues: row.RouteValues,
		SessionType: row.SessionType,
		ChatRowID:   row.ID,
	}
}

// snapshotFromConfig 按渠道默认路由构造快照；sessionID 非空表示新建。
// forceNew（/new）：chat 已有映射时改绑（Rebind 更新既有行到新会话）而非沿用旧会话；
// 普通新建撞并发唯一键时仍回读既有映射（粘滞原则——先落库者赢）。
func (s *service) snapshotFromConfig(ginCtx *gin.Context, msg InboundMessage, sessionID string, forceNew bool) (*sessionSnapshot, error) {
	cfg, err := model.GetChannelConfigByChannelID(ginCtx, msg.ChannelID)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("渠道配置不存在: %s", msg.ChannelID)
	}
	if strings.TrimSpace(cfg.DefaultUserName) == "" {
		return nil, fmt.Errorf("渠道 %s 未配置 default_user_name", msg.ChannelID)
	}
	// 身份解析（M2）：平台用户绑定表 (kind, platform_user_id) 命中时覆盖渠道默认
	// userName（未绑定/查询失败回落 M1 渠道级默认，语义兼容）。解析结果随快照粘滞。
	userName := cfg.DefaultUserName
	if identity, err := model.GetUserIdentity(ginCtx, identityScopeKind(msg.Kind, cfg.Kind), msg.UserID); err != nil {
		zlog.Errorf(ginCtx, "[gateway] 身份绑定查询失败(回落渠道默认): kind=%s user=%s err=%v", msg.Kind, msg.UserID, err)
	} else if identity != nil && strings.TrimSpace(identity.UserName) != "" {
		userName = identity.UserName
	}
	row := &model.ChannelSession{
		ChannelID:    msg.ChannelID,
		ChatID:       msg.ChatID,
		SessionID:    sessionID,
		UserName:     userName,
		AppKey:       cfg.DefaultAppKey,
		RouteValues:  normalizeRouteValuesJSON(cfg.DefaultRouteValues),
		SessionType:  "chat",
		// MySQL 严格模式拒绝零值时间（Error 1292），建行即写活跃时刻。
		LastActiveAt: time.Now(),
	}
	if forceNew {
		if existing, err := model.GetChannelSessionByChat(ginCtx, msg.ChannelID, msg.ChatID); err != nil {
			return nil, err
		} else if existing != nil {
			row.ID = existing.ID
			if err := model.RebindChannelSession(ginCtx, row); err != nil {
				return nil, err
			}
			return snapshotFromRow(row), nil
		}
	}
	if err := model.BindChannelSession(ginCtx, row); err != nil {
		// 并发首条消息撞唯一键：回读既有映射（粘滞原则——先落库者赢）。
		if existing, getErr := model.GetChannelSessionByChat(ginCtx, msg.ChannelID, msg.ChatID); getErr == nil && existing != nil {
			return snapshotFromRow(existing), nil
		}
		return nil, err
	}
	return snapshotFromRow(row), nil
}

// identityScopeKind 身份绑定查询的平台键：入站消息自带 kind 优先，缺省回渠道配置。
func identityScopeKind(msgKind, cfgKind string) string {
	if strings.TrimSpace(msgKind) != "" {
		return strings.TrimSpace(msgKind)
	}
	return strings.TrimSpace(cfgKind)
}

// buildRunPayload 按快照构造 run 载荷；requestSource=gateway 仅作观测标注（身份已由
// NewHeadlessGinContext 注入，运行时零改动）。
func buildRunPayload(snapshot *sessionSnapshot, text string) params.ReactRunPayload {
	return params.ReactRunPayload{
		AppKey:      snapshot.AppKey,
		RouteValues: parseRouteValues(snapshot.RouteValues),
		Type:        snapshot.SessionType,
		UserPrompt:  text,
		ControlContext: json.RawMessage(`{"requestSource":"gateway"}`),
	}
}

// ---------------------------------------------------------------------------
// chat 级 adapter 注册表
// ---------------------------------------------------------------------------

func adapterKey(channelID, chatID string) string { return channelID + "/" + chatID }

func (s *service) activeAdapter(channelID, chatID string) *imAdapter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adapters[adapterKey(channelID, chatID)]
}

func (s *service) mountAdapter(channelID, chatID string, a *imAdapter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adapters[adapterKey(channelID, chatID)] = a
}

func (s *service) unmountAdapter(channelID, chatID string, a *imAdapter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adapters[adapterKey(channelID, chatID)] == a {
		delete(s.adapters, adapterKey(channelID, chatID))
	}
}

// cancelActiveRun 取消该 chat 当前进行中的 run（用户回复「取消」/「/cancel」）。
func (s *service) cancelActiveRun(msg InboundMessage) {
	a := s.activeAdapter(msg.ChannelID, msg.ChatID)
	if a == nil {
		return
	}
	a.cancelRun()
	_, _ = s.pushText(context.Background(), PushAddr{ChannelID: msg.ChannelID, ChatID: msg.ChatID}, "🛑 已请求取消当前任务")
}

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

// generateGatewaySessionID 与运行时 generateSessionID 同格式（session_ 前缀）；
// gateway 侧预生成以在 run 启动前落 chat 映射，避免并发首条消息产生两会话。
func generateGatewaySessionID() string {
	return "session_" + strings.ReplaceAll(uuid.New().String(), "-", "")
}

// normalizeRouteValuesJSON 规整路由快照为合法 JSON 数组字符串（空/非法回退 '[]'）。
func normalizeRouteValuesJSON(raw string) string {
	var parsed []string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "[]"
	}
	out, _ := json.Marshal(parsed)
	return string(out)
}

// parseRouteValues 把快照 JSON 数组解析回 []string。
func parseRouteValues(raw string) []string {
	var parsed []string
	_ = json.Unmarshal([]byte(raw), &parsed)
	return parsed
}

// truncateForDisplay 截断展示摘要。
func truncateForDisplay(text string) string {
	runes := []rune(text)
	if len(runes) > 64 {
		return string(runes[:64]) + "…"
	}
	return text
}

// isReactRunActiveError 识别「会话已有活跃 run」错误（Steering 关闭时的并发拒绝）。
func isReactRunActiveError(err error) bool {
	if err == nil {
		return false
	}
	var typed base.Error
	if errors.As(err, &typed) {
		return typed.ErrNo == components.ErrorReactRunActive.ErrNo
	}
	// 错误号兜底识别（ErrNo 出现在 Error() 字符串里）。
	return strings.Contains(err.Error(), fmt.Sprintf("errNo=%d", components.ErrorReactRunActive.ErrNo))
}
