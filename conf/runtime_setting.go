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

// RuntimeSettingOverride 是全部运行时设置的覆盖快照；当前仅 subagent 一组，预留扩展位。
type RuntimeSettingOverride struct {
	SubAgent *SubAgentSettingOverride `json:"subagent,omitempty"`
}

// SubAgentSettingOverride 是 subagent 委派策略的 DB 覆盖字段（与 ReactSubAgentConfig 一一对应）。
type SubAgentSettingOverride struct {
	Enabled         *bool `json:"enabled,omitempty"`
	MaxParallel     *int  `json:"maxParallel,omitempty"`
	DefaultMaxSteps *int  `json:"defaultMaxSteps,omitempty"`
	MaxDepth        *int  `json:"maxDepth,omitempty"`
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
