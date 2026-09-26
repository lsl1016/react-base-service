package conf

import "sync/atomic"

// 运行时设置覆盖（管理面板「运行时配置」）：
//
// conf 是全仓库最底层的包（helpers/models 均依赖它），因此本文件只承载「内存覆盖快照」，
// 不访问任何存储；DB 读取与 TTL 刷新在 service/setting 包完成并经 SetRuntimeSettingOverride
// 注入。GetReactRuntimeConfig 在归一化默认值前应用覆盖，yaml 始终是兜底基线。
//
// 覆盖粒度是「字段级」：指针为 nil = 该字段未覆盖，回落 custom.yaml（未配置再回落内置默认）；
// 非 nil = 以 DB 值为准。写侧（service/setting）已做范围校验，此处不做重复校验。

// RuntimeSettingOverride 是全部运行时设置的覆盖快照（subagent / context 两组）。
type RuntimeSettingOverride struct {
	SubAgent       *SubAgentSettingOverride       `json:"subagent,omitempty"`
	ContextCompact *ContextCompactSettingOverride `json:"context,omitempty"`
}

// SubAgentSettingOverride 是 subagent 委派策略的 DB 覆盖字段（与 ReactSubAgentConfig 一一对应）。
type SubAgentSettingOverride struct {
	Enabled         *bool `json:"enabled,omitempty"`
	MaxParallel     *int  `json:"maxParallel,omitempty"`
	DefaultMaxSteps *int  `json:"defaultMaxSteps,omitempty"`
	MaxDepth        *int  `json:"maxDepth,omitempty"`
}

// ContextCompactSettingOverride 是上下文压缩策略的 DB 覆盖字段
//（与 ReactContextCompactConfig 的运营期可调子集一一对应）。
type ContextCompactSettingOverride struct {
	Enabled                *bool `json:"enabled,omitempty"`
	TokenTrigger           *int  `json:"tokenTrigger,omitempty"`
	TokenTarget            *int  `json:"tokenTarget,omitempty"`
	SummaryLimit           *int  `json:"summaryLimit,omitempty"`
	OutputReserveTokens    *int  `json:"outputReserveTokens,omitempty"`
	BufferTokens           *int  `json:"bufferTokens,omitempty"`
	MicrocompactEnabled    *bool `json:"microcompactEnabled,omitempty"`
	MicrocompactKeepRecent *int  `json:"microcompactKeepRecent,omitempty"`
}

// runtimeSettingOverrideHolder 存 RuntimeSettingOverride 值；零值快照（nil 各字段）= 无覆盖。
var runtimeSettingOverrideHolder atomic.Value

// SetRuntimeSettingOverride 整体替换覆盖快照（service/setting 在启动加载与管理面板写入后调用）。
func SetRuntimeSettingOverride(override RuntimeSettingOverride) {
	runtimeSettingOverrideHolder.Store(override)
}

// GetRuntimeSettingOverride 返回当前覆盖快照；从未注入过时返回零值。
func GetRuntimeSettingOverride() RuntimeSettingOverride {
	if stored, ok := runtimeSettingOverrideHolder.Load().(RuntimeSettingOverride); ok {
		return stored
	}
	return RuntimeSettingOverride{}
}

// applySubAgentOverride 把 DB 覆盖合并进 yaml 基线：仅覆盖非 nil 字段。
// 在默认值归一化之前调用，覆盖值（写侧已保证 >=1）不会被默认值回填改写。
func applySubAgentOverride(base ReactSubAgentConfig, override *SubAgentSettingOverride) ReactSubAgentConfig {
	if override == nil {
		return base
	}
	if override.Enabled != nil {
		base.Enabled = override.Enabled
	}
	if override.MaxParallel != nil {
		base.MaxParallel = *override.MaxParallel
	}
	if override.DefaultMaxSteps != nil {
		base.DefaultMaxSteps = *override.DefaultMaxSteps
	}
	if override.MaxDepth != nil {
		base.MaxDepth = *override.MaxDepth
	}
	return base
}

// EffectiveSubAgentConfig 返回 subagent 委派策略的最终生效值（DB 覆盖 > custom.yaml > 内置默认）。
// GetReactRuntimeConfig 的引擎消费口径与此函数完全一致；管理面板响应用它渲染，
// 保证「面板读到的 = 下一次快照刷新后引擎将消费的」，不依赖全局快照的实时性。
func EffectiveSubAgentConfig(override *SubAgentSettingOverride) ReactSubAgentConfig {
	cfg := applySubAgentOverride(CustomConf.LLM.React.SubAgent, override)
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = defaultReactSubAgentMaxParallel
	}
	if cfg.DefaultMaxSteps <= 0 {
		cfg.DefaultMaxSteps = defaultReactSubAgentMaxSteps
	}
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = defaultReactSubAgentMaxDepth
	}
	return cfg
}

// applyContextCompactOverride 把 DB 覆盖合并进 yaml 基线：仅覆盖非 nil 字段。
// 在 GetReactRuntimeConfig 的默认值归一化之前调用。
func applyContextCompactOverride(base ReactContextCompactConfig, override *ContextCompactSettingOverride) ReactContextCompactConfig {
	if override == nil {
		return base
	}
	if override.Enabled != nil {
		base.Enabled = override.Enabled
	}
	if override.TokenTrigger != nil {
		base.TokenTrigger = *override.TokenTrigger
	}
	if override.TokenTarget != nil {
		base.TokenTarget = *override.TokenTarget
	}
	if override.SummaryLimit != nil {
		base.SummaryLimit = *override.SummaryLimit
	}
	if override.OutputReserveTokens != nil {
		base.OutputReserveTokens = *override.OutputReserveTokens
	}
	if override.BufferTokens != nil {
		base.BufferTokens = *override.BufferTokens
	}
	if override.MicrocompactEnabled != nil {
		base.MicrocompactEnabled = override.MicrocompactEnabled
	}
	if override.MicrocompactKeepRecent != nil {
		base.MicrocompactKeepRecent = *override.MicrocompactKeepRecent
	}
	return base
}

// EffectiveContextCompactConfig 返回上下文压缩策略的最终生效值（DB 覆盖 > custom.yaml > 内置默认），
// 含与 GetReactRuntimeConfig 相同的归一化；管理面板响应用它渲染。
func EffectiveContextCompactConfig(override *ContextCompactSettingOverride) ReactContextCompactConfig {
	cfg := applyContextCompactOverride(CustomConf.LLM.React.ContextCompact, override)
	if cfg.TokenTrigger <= 0 {
		cfg.TokenTrigger = defaultReactCompactTokenTrigger
	}
	if cfg.TokenTarget <= 0 {
		cfg.TokenTarget = defaultReactCompactTokenTarget
	}
	if cfg.TokenTarget >= cfg.TokenTrigger {
		cfg.TokenTarget = cfg.TokenTrigger * 60 / 100
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = defaultReactCompactTimeoutSec
	}
	if cfg.SummaryLimit <= 0 {
		cfg.SummaryLimit = defaultReactCompactSummaryLimit
	}
	if cfg.FallbackMessagePreviewLimit <= 0 {
		cfg.FallbackMessagePreviewLimit = defaultReactCompactFallbackPreviewLimit
	}
	if cfg.OutputReserveTokens <= 0 {
		cfg.OutputReserveTokens = defaultReactCompactOutputReserve
	}
	if cfg.BufferTokens <= 0 {
		cfg.BufferTokens = defaultReactCompactBufferTokens
	}
	if cfg.MicrocompactKeepRecent <= 0 {
		cfg.MicrocompactKeepRecent = defaultReactMicrocompactKeepRecent
	}
	if cfg.CompactFailureBreaker <= 0 {
		cfg.CompactFailureBreaker = defaultReactCompactFailureBreaker
	}
	if cfg.Enabled == nil {
		enabled := true
		cfg.Enabled = &enabled
	}
	return cfg
}
