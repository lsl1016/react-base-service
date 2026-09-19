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
