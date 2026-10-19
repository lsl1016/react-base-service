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
			adapters:   map[string]*imAdapter{},
			runInvoker: react.RunWithClientReaderContext,
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
	// 群聊只响应 @ 机器人 的消息（lark SDK 归一化已提供 MentionedBot 标志，
	// 由渠道侧在 OnMessage 里置入 Text 前——见 feishu.go 的群聊过滤）。
	if strings.EqualFold(text, "/cancel") || strings.EqualFold(text, "取消") {
		s.cancelActiveRun(msg)
		return
	}

	ginCtx := newGatewayGinContext()

	// 分支 0：当前 run 正在等待 IM 作答（ask_question/危险确认）——本条回复直接投给它。
	if a := s.activeAdapter(msg.ChannelID, msg.ChatID); a != nil {
		if a.takePending() != nil {
			a.deliverReply(text)
			return
		}
	}

	// 路由三分支：消息绑定命中 → 平台会话映射 → 新建。
	snapshot, err := s.routeSession(ginCtx, msg)
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
	s.mountAdapter(msg.ChannelID, msg.ChatID, adapter)

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
				return
			}
			if react.IsReactRunCancelled(err) {
				zlog.Infof(runGinCtx, "[gateway] run 已取消: session=%s", snapshot.SessionID)
				return
			}
			// Steering 关闭时会话活跃时直接拒绝：给用户可读提示而非报错堆栈。
			if isReactRunActiveError(err) {
				_, _ = adapter.pushText("⏳ 当前任务仍在执行中，请稍候再发，或回复「取消」中止。")
				return
			}
			// 启动/运行失败且没有任何终态事件可推送时兜底告知用户。
			if !adapter.pushedTerminal() {
				_, _ = adapter.pushText("❌ 执行失败：" + err.Error())
			}
			zlog.Errorf(runGinCtx, "[gateway] run 失败: session=%s err=%v", snapshot.SessionID, err)
			return
		}
		zlog.Infof(runGinCtx, "[gateway] run 完成: session=%s run=%s", result.SessionID, result.RunID)
	}()
}

// routeSession 解析本条消息归属的会话（三分支）与五元组快照。
func (s *service) routeSession(ginCtx *gin.Context, msg InboundMessage) (*sessionSnapshot, error) {
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
			return s.snapshotFromConfig(ginCtx, msg, binding.SessionID)
		}
	}

	// 分支 2：平台会话映射——该 chat 已绑定会话，直接续聊（粘滞五元组）。
	if row, err := model.GetChannelSessionByChat(ginCtx, msg.ChannelID, msg.ChatID); err != nil {
		return nil, err
	} else if row != nil {
		return snapshotFromRow(row), nil
	}

	// 分支 3：新建——按渠道配置默认路由生成会话并落映射（幂等：并发首条消息由唯一键收敛）。
	return s.snapshotFromConfig(ginCtx, msg, generateGatewaySessionID())
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

// snapshotFromConfig 按渠道默认路由构造快照，并在新建（sessionID 非空且 chat 无映射）时落绑定。
func (s *service) snapshotFromConfig(ginCtx *gin.Context, msg InboundMessage, sessionID string) (*sessionSnapshot, error) {
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
	row := &model.ChannelSession{
		ChannelID:    msg.ChannelID,
		ChatID:       msg.ChatID,
		SessionID:    sessionID,
		UserName:     cfg.DefaultUserName,
		AppKey:       cfg.DefaultAppKey,
		RouteValues:  normalizeRouteValuesJSON(cfg.DefaultRouteValues),
		SessionType:  "chat",
		// MySQL 严格模式拒绝零值时间（Error 1292），建行即写活跃时刻。
		LastActiveAt: time.Now(),
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
