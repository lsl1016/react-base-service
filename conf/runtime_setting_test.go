package conf

import (
	"testing"
)

func TestApplySubAgentOverrideFieldLevel(t *testing.T) {
	enabled := false
	depth := 3
	base := ReactSubAgentConfig{} // 全字段未配置（yaml 基线为空）

	merged := applySubAgentOverride(base, &SubAgentSettingOverride{
		Enabled:  &enabled,
		MaxDepth: &depth,
	})

	if merged.Enabled == nil || *merged.Enabled {
		t.Fatalf("enabled 覆盖失败: %+v", merged.Enabled)
	}
	if merged.MaxDepth != 3 {
		t.Fatalf("maxDepth 覆盖失败: %d", merged.MaxDepth)
	}
	// 未覆盖字段必须保留 yaml 基线值，不被误改。
	if merged.MaxParallel != 0 || merged.DefaultMaxSteps != 0 {
		t.Fatalf("未覆盖字段被误改: %+v", merged)
	}
}

func TestApplySubAgentOverrideNilIsNoop(t *testing.T) {
	yamlEnabled := true
	base := ReactSubAgentConfig{Enabled: &yamlEnabled, MaxDepth: 2}
	merged := applySubAgentOverride(base, nil)
	if merged.Enabled == nil || !*merged.Enabled {
		t.Fatalf("nil 覆盖必须原样保留基线: %+v", merged)
	}
	if merged.MaxDepth != 2 {
		t.Fatalf("nil 覆盖必须保留 maxDepth: %d", merged.MaxDepth)
	}
}

func TestGetReactRuntimeConfigAppliesOverride(t *testing.T) {
	t.Cleanup(func() { SetRuntimeSettingOverride(RuntimeSettingOverride{}) })

	depth := 4
	steps := 16
	SetRuntimeSettingOverride(RuntimeSettingOverride{
		SubAgent: &SubAgentSettingOverride{MaxDepth: &depth, DefaultMaxSteps: &steps},
	})

	cfg := GetReactRuntimeConfig()
	if cfg.SubAgent.MaxDepth != depth {
		t.Fatalf("覆盖未生效: maxDepth=%d, want %d", cfg.SubAgent.MaxDepth, depth)
	}
	if cfg.SubAgent.DefaultMaxSteps != steps {
		t.Fatalf("覆盖未生效: defaultMaxSteps=%d, want %d", cfg.SubAgent.DefaultMaxSteps, steps)
	}
	// 覆盖值必须绕过「<=0 回落默认」归一化：显式覆盖 1 是合法值。
	one := 1
	SetRuntimeSettingOverride(RuntimeSettingOverride{
		SubAgent: &SubAgentSettingOverride{MaxDepth: &one},
	})
	if got := GetReactRuntimeConfig().SubAgent.MaxDepth; got != 1 {
		t.Fatalf("覆盖值 1 被默认值归一化改写: %d", got)
	}
}

func TestApplyMemoryOverrideFieldLevel(t *testing.T) {
	disabled := false
	budget := 4000
	userScope := false
	base := ReactMemoryConfig{ResidentMaxItems: 16} // yaml 基线

	merged := applyMemoryOverride(base, &MemorySettingOverride{
		Enabled:             &disabled,
		ResidentBudgetChars: &budget,
		AllowUserScope:      &userScope,
	})

	if merged.Enabled == nil || *merged.Enabled {
		t.Fatalf("enabled 覆盖失败: %+v", merged.Enabled)
	}
	if merged.ResidentBudgetChars != 4000 {
		t.Fatalf("residentBudgetChars 覆盖失败: %d", merged.ResidentBudgetChars)
	}
	if merged.AllowUserScope == nil || *merged.AllowUserScope {
		t.Fatalf("allowUserScope 覆盖失败: %+v", merged.AllowUserScope)
	}
	// 未覆盖字段必须保留 yaml 基线值，不被误改。
	if merged.ResidentMaxItems != 16 {
		t.Fatalf("未覆盖字段被误改: %+v", merged)
	}
}

func TestApplyMemoryOverrideNilIsNoop(t *testing.T) {
	yamlEnabled := true
	base := ReactMemoryConfig{Enabled: &yamlEnabled, IndexMaxItems: 64}
	merged := applyMemoryOverride(base, nil)
	if merged.Enabled == nil || !*merged.Enabled {
		t.Fatalf("nil 覆盖必须原样保留基线: %+v", merged.Enabled)
	}
	if merged.IndexMaxItems != 64 {
		t.Fatalf("nil 覆盖必须保留 indexMaxItems: %d", merged.IndexMaxItems)
	}
}

func TestGetReactRuntimeConfigAppliesMemoryOverride(t *testing.T) {
	t.Cleanup(func() { SetRuntimeSettingOverride(RuntimeSettingOverride{}) })

	items := 32
	disabled := false
	SetRuntimeSettingOverride(RuntimeSettingOverride{
		Memory: &MemorySettingOverride{Enabled: &disabled, ResidentMaxItems: &items},
	})

	cfg := GetReactRuntimeConfig()
	if cfg.Memory.MemoryEnabled() {
		t.Fatal("memory enabled 覆盖未生效：仍为开启")
	}
	if cfg.Memory.ResidentMaxItems != 32 {
		t.Fatalf("residentMaxItems 覆盖未生效: %d, want 32", cfg.Memory.ResidentMaxItems)
	}
	// 未覆盖字段仍取 yaml 基线 + 归一化默认（yaml 未配 detached_max_items 时回落 500）。
	if cfg.Memory.DetachedMaxItems != defaultReactMemoryDetachedMaxItems {
		t.Fatalf("未覆盖字段应回落默认值: %d", cfg.Memory.DetachedMaxItems)
	}
	// EffectiveMemoryConfig（面板生效视图口径）与引擎消费口径一致。
	effective := EffectiveMemoryConfig(GetRuntimeSettingOverride().Memory)
	if effective.ResidentMaxItems != cfg.Memory.ResidentMaxItems || effective.MemoryEnabled() != cfg.Memory.MemoryEnabled() {
		t.Fatalf("面板口径与引擎口径不一致: effective=%+v engine=%+v", effective, cfg.Memory)
	}
}

func TestApplyWebAccessOverride(t *testing.T) {
	// 单一覆盖同时作用于两个工具配置：覆盖关闭、nil 覆盖保留 yaml 基线。
	yamlEnabled := true
	base := ReactWebFetchConfig{Enabled: &yamlEnabled, TimeoutSec: 30}
	disabled := false

	merged := applyWebFetchOverride(base, &WebAccessSettingOverride{Enabled: &disabled})
	if merged.Enabled == nil || *merged.Enabled {
		t.Fatalf("web_fetch enabled 覆盖失败: %+v", merged.Enabled)
	}
	if merged.TimeoutSec != 30 {
		t.Fatalf("未覆盖字段被误改: %+v", merged)
	}
	if noop := applyWebFetchOverride(base, nil); noop.Enabled == nil || !*noop.Enabled {
		t.Fatalf("nil 覆盖必须原样保留基线: %+v", noop.Enabled)
	}

	// web_search：同枚覆盖，且连接参数不受覆盖影响。
	searchBase := ReactWebSearchConfig{Enabled: &yamlEnabled, Kind: "searxng", BaseURL: "http://searxng:8080"}
	mergedSearch := applyWebSearchOverride(searchBase, &WebAccessSettingOverride{Enabled: &disabled})
	if mergedSearch.Enabled == nil || *mergedSearch.Enabled {
		t.Fatalf("web_search enabled 覆盖失败: %+v", mergedSearch.Enabled)
	}
	if mergedSearch.Kind != "searxng" || mergedSearch.BaseURL != "http://searxng:8080" {
		t.Fatalf("连接参数被覆盖误改: %+v", mergedSearch)
	}
}

func TestGetReactRuntimeConfigAppliesWebAccessOverride(t *testing.T) {
	t.Cleanup(func() { SetRuntimeSettingOverride(RuntimeSettingOverride{}) })

	// 同一枚开关覆盖两个工具：关闭即同时关，开启即同时开（状态同步由单一覆盖值保证）。
	disabled := false
	SetRuntimeSettingOverride(RuntimeSettingOverride{
		Web: &WebAccessSettingOverride{Enabled: &disabled},
	})

	cfg := GetReactRuntimeConfig()
	if cfg.WebFetch.WebFetchEnabled() {
		t.Fatal("web_fetch enabled=false 覆盖未生效")
	}
	if cfg.WebSearch.WebSearchEnabled() {
		t.Fatal("web_search enabled=false 覆盖未生效（两工具应同步关闭）")
	}

	enabled := true
	SetRuntimeSettingOverride(RuntimeSettingOverride{
		Web: &WebAccessSettingOverride{Enabled: &enabled},
	})
	cfg = GetReactRuntimeConfig()
	if !cfg.WebFetch.WebFetchEnabled() || !cfg.WebSearch.WebSearchEnabled() {
		t.Fatal("两工具应同步开启")
	}

	// 面板生效视图口径与引擎消费口径一致（两工具共用同一覆盖入参）。
	web := GetRuntimeSettingOverride().Web
	fetchEffective := EffectiveWebFetchConfig(web)
	searchEffective := EffectiveWebSearchConfig(web)
	if fetchEffective.WebFetchEnabled() != cfg.WebFetch.WebFetchEnabled() {
		t.Fatalf("web_fetch 面板口径与引擎口径不一致: %+v vs %+v", fetchEffective, cfg.WebFetch)
	}
	if searchEffective.WebSearchEnabled() != cfg.WebSearch.WebSearchEnabled() {
		t.Fatalf("web_search 面板口径与引擎口径不一致: %+v vs %+v", searchEffective, cfg.WebSearch)
	}
}

