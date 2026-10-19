package gateway

import (
	"context"
	"fmt"
	"sync"

	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
)

// Manager 管理渠道实例生命周期：从 tblLlmChannelConfig 加载启用的渠道，
// 逐个构造 Channel 并拉起；配置变更后 Reload 重建（先停旧再起新）。
type Manager struct {
	mu       sync.Mutex
	channels map[string]Channel
	handler  MessageHandler
}

func newManager(handler MessageHandler) *Manager {
	return &Manager{
		channels: map[string]Channel{},
		handler:  handler,
	}
}

// Start 加载启用的渠道配置并全部拉起。没有任何可用渠道时返回 nil（空转，不报错）。
// 单个渠道启动失败只记日志、不阻断其余渠道（如 Telegram 无代理时飞书照常可用）；
// 失败渠道可经配置保存触发的 Reload 重试。
func (m *Manager) Start(ctx context.Context) error {
	ginCtx := newGatewayGinContext()
	configs, err := model.ListChannelConfigs(ginCtx, true)
	if err != nil {
		return fmt.Errorf("gateway 加载渠道配置失败: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cfg := range configs {
		if _, err := m.startLocked(ctx, &cfg); err != nil {
			zlog.Errorf(nil, "[gateway] 渠道启动失败(跳过,可重载重试): channel=%s kind=%s err=%v", cfg.ChannelID, cfg.Kind, err)
		}
	}
	return nil
}

// startLocked 构造并启动单个渠道（调用方持锁）。已存在同 ID 渠道时先忽略（Reload 负责）。
func (m *Manager) startLocked(ctx context.Context, cfg *model.ChannelConfig) (Channel, error) {
	if _, exists := m.channels[cfg.ChannelID]; exists {
		return nil, nil
	}
	ch, err := buildChannel(cfg)
	if err != nil {
		return nil, err
	}
	if err := ch.Start(ctx, m.handler); err != nil {
		return nil, fmt.Errorf("渠道 %s(%s) 启动失败: %w", cfg.ChannelID, cfg.Kind, err)
	}
	m.channels[cfg.ChannelID] = ch
	return ch, nil
}

// Reload 全量重建：停掉全部渠道，按当前 DB 配置重新拉起（CRUD 后调用）。
func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	old := m.channels
	m.channels = map[string]Channel{}
	m.mu.Unlock()
	for _, ch := range old {
		_ = ch.Stop(ctx)
	}
	return m.Start(ctx)
}

// Stop 停掉全部渠道。
func (m *Manager) Stop(ctx context.Context) {
	m.mu.Lock()
	old := m.channels
	m.channels = map[string]Channel{}
	m.mu.Unlock()
	for _, ch := range old {
		_ = ch.Stop(ctx)
	}
}

// Get 按渠道配置 ID 取运行中的渠道实例。
func (m *Manager) Get(channelID string) Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.channels[channelID]
}

// buildChannel 按渠道类型构造 Channel；不支持的类型返回错误（建配置时已校验，这里兜底）。
func buildChannel(cfg *model.ChannelConfig) (Channel, error) {
	switch cfg.Kind {
	case model.ChannelKindFeishu:
		return newFeishuChannel(cfg), nil
	case model.ChannelKindTelegram:
		return newTelegramChannel(cfg), nil
	default:
		return nil, fmt.Errorf("不支持的渠道类型: %s", cfg.Kind)
	}
}
