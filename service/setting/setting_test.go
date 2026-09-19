package setting

import (
	"testing"

	"react-base-service/components/params"
)

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func TestValidateSubAgentUpdateRanges(t *testing.T) {
	cases := []struct {
		name    string
		req     *params.UpdateSubAgentSettingReq
		wantErr bool
	}{
		{"合法覆盖", &params.UpdateSubAgentSettingReq{MaxDepth: intPtr(3), DefaultMaxSteps: intPtr(32), MaxParallel: intPtr(2)}, false},
		{"空请求合法(等价无变更)", &params.UpdateSubAgentSettingReq{}, false},
		{"maxDepth超上界", &params.UpdateSubAgentSettingReq{MaxDepth: intPtr(5)}, true},
		{"maxDepth低于下界", &params.UpdateSubAgentSettingReq{MaxDepth: intPtr(0)}, true},
		{"defaultMaxSteps超上界", &params.UpdateSubAgentSettingReq{DefaultMaxSteps: intPtr(65)}, true},
		{"maxParallel超上界", &params.UpdateSubAgentSettingReq{MaxParallel: intPtr(9)}, true},
		{"clear字段合法时跳过范围校验", &params.UpdateSubAgentSettingReq{MaxDepth: intPtr(99), ClearFields: []string{"maxDepth"}}, false},
		{"clear未知字段", &params.UpdateSubAgentSettingReq{ClearFields: []string{"unknown"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSubAgentUpdate(tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateSubAgentUpdate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestMergeSubAgentUpdate(t *testing.T) {
	current := &subAgentOverrideJSON{Enabled: boolPtr(true), MaxDepth: intPtr(2)}

	// 增量覆盖：未涉及字段保留现状。
	merged := mergeSubAgentUpdate(current, &params.UpdateSubAgentSettingReq{MaxParallel: intPtr(4)})
	if merged.Enabled == nil || !*merged.Enabled {
		t.Fatalf("未涉及的 enabled 被误改: %+v", merged.Enabled)
	}
	if merged.MaxDepth == nil || *merged.MaxDepth != 2 {
		t.Fatalf("未涉及的 maxDepth 被误改: %+v", merged.MaxDepth)
	}
	if merged.MaxParallel == nil || *merged.MaxParallel != 4 {
		t.Fatalf("maxParallel 覆盖失败: %+v", merged.MaxParallel)
	}

	// clear 优先于同请求的字段值：语义是「该字段回落 yaml」。
	merged = mergeSubAgentUpdate(current, &params.UpdateSubAgentSettingReq{Enabled: boolPtr(false), ClearFields: []string{"enabled"}})
	if merged.Enabled != nil {
		t.Fatalf("clear 应清空 enabled 覆盖: %+v", merged.Enabled)
	}

	// 显式关闭（不带 clear）：覆盖为 false 而非清空。
	merged = mergeSubAgentUpdate(current, &params.UpdateSubAgentSettingReq{Enabled: boolPtr(false)})
	if merged.Enabled == nil || *merged.Enabled {
		t.Fatalf("显式关闭应写入 false 覆盖: %+v", merged.Enabled)
	}

	// 现状为 nil（DB 无行）：合并结果只含请求字段。
	merged = mergeSubAgentUpdate(nil, &params.UpdateSubAgentSettingReq{MaxDepth: intPtr(3)})
	if merged.MaxDepth == nil || *merged.MaxDepth != 3 {
		t.Fatalf("空现状合并失败: %+v", merged)
	}
	if merged.Enabled != nil || merged.MaxParallel != nil || merged.DefaultMaxSteps != nil {
		t.Fatalf("空现状合并引入了多余字段: %+v", merged)
	}
}

func TestBuildSubAgentSettingRespSources(t *testing.T) {
	override := &subAgentOverrideJSON{Enabled: boolPtr(true), MaxDepth: intPtr(3)}
	resp := buildSubAgentSettingResp(override, "alice", "2026-09-19 10:00:00")

	if resp.Effective.MaxDepth != 3 {
		t.Fatalf("effective.maxDepth 应取覆盖值: %d", resp.Effective.MaxDepth)
	}
	if resp.Sources["enabled"].Source != "override" {
		t.Fatalf("enabled 来源应为 override: %s", resp.Sources["enabled"].Source)
	}
	// maxParallel 无覆盖且 yaml 未配置 → default。
	if resp.Sources["maxParallel"].Source != "default" {
		t.Fatalf("maxParallel 来源应为 default: %s", resp.Sources["maxParallel"].Source)
	}
	if resp.UpdatedBy != "alice" {
		t.Fatalf("审计字段丢失: %s", resp.UpdatedBy)
	}
}
