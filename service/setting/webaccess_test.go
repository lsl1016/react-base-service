package setting

import (
	"testing"

	"react-base-service/components/params"
	"react-base-service/conf"
)

func TestValidateWebAccessUpdate(t *testing.T) {
	cases := []struct {
		name        string
		clearFields []string
		wantErr     bool
	}{
		{"空请求合法(等价无变更)", nil, false},
		{"clear enabled 合法", []string{"enabled"}, false},
		{"clear 未知字段", []string{"unknown"}, true},
		{"clear 混入未知字段", []string{"enabled", "maxResults"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWebAccessUpdate(tc.clearFields)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateWebAccessUpdate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestMergeWebAccessUpdate(t *testing.T) {
	current := &webAccessOverrideJSON{Enabled: boolPtr(true)}

	// 增量关闭：覆盖为 false 而非清空。
	merged := mergeWebAccessUpdate(current, boolPtr(false), nil)
	if merged.Enabled == nil || *merged.Enabled {
		t.Fatalf("显式关闭应写入 false 覆盖: %+v", merged.Enabled)
	}

	// clear 优先于同请求的字段值：语义是「回落 yaml」。
	merged = mergeWebAccessUpdate(current, boolPtr(false), []string{"enabled"})
	if merged.Enabled != nil {
		t.Fatalf("clear 应清空 enabled 覆盖: %+v", merged.Enabled)
	}

	// 未传字段保留现状。
	merged = mergeWebAccessUpdate(current, nil, nil)
	if merged.Enabled == nil || !*merged.Enabled {
		t.Fatalf("未涉及的 enabled 被误改: %+v", merged.Enabled)
	}

	// 现状为 nil（DB 无行）：合并结果只含请求字段。
	merged = mergeWebAccessUpdate(nil, boolPtr(true), nil)
	if merged.Enabled == nil || !*merged.Enabled {
		t.Fatalf("空现状合并失败: %+v", merged)
	}
}

func TestBuildWebSettingRespSources(t *testing.T) {
	override := &webAccessOverrideJSON{Enabled: boolPtr(true)}

	// 期望基线与配置完整性均从 CustomConf 实际状态推导（测试进程可能未加载 yaml），保证断言与 yaml 状态无关。
	fetchYamlEnabled := conf.CustomConf.LLM.React.WebFetch.Enabled
	wantBaseline := fetchYamlEnabled != nil && *fetchYamlEnabled
	searchYaml := conf.CustomConf.LLM.React.WebSearch

	resp := buildWebSettingResp(override, "alice", "2026-09-30 10:00:00")
	if !resp.Effective.Enabled {
		t.Fatal("effective.enabled 应取覆盖值 true（两工具共用）")
	}
	src := resp.Sources["enabled"]
	if src.Source != "override" {
		t.Fatalf("有覆盖时 source 应为 override: %s", src.Source)
	}
	if src.Value != true {
		t.Fatalf("source.value 应为生效值: %+v", src.Value)
	}
	// 清除覆盖后的回落值 = yaml 开关（未配置时默认 false）。
	if src.Baseline != wantBaseline {
		t.Fatalf("baseline 应为 yaml 回落值: %+v, want %v", src.Baseline, wantBaseline)
	}
	if resp.Override == nil || resp.Override.Enabled == nil || !*resp.Override.Enabled {
		t.Fatalf("override 原文应回显: %+v", resp.Override)
	}
	// configured 只由 yaml 连接参数决定（kind=searxng + base_url 非空），与覆盖无关。
	if resp.Effective.Configured != searchYaml.WebSearchConfigured() {
		t.Fatalf("configured 应与 yaml 连接参数一致: %v", resp.Effective.Configured)
	}

	// 无覆盖：source 回落 yaml/default，override 为 null。
	resp = buildWebSettingResp(nil, "", "")
	if resp.Override != nil {
		t.Fatalf("无覆盖时 override 应为 null: %+v", resp.Override)
	}
	if resp.Sources["enabled"].Source == "override" {
		t.Fatalf("无覆盖时 source 不应为 override: %+v", resp.Sources["enabled"])
	}
}

// UpdateWebSettingReq 与共用合并逻辑的参数适配冒烟：确认请求字段能正确进入 mergeWebAccessUpdate。
func TestWebSettingReqAdaptsMerge(t *testing.T) {
	req := &params.UpdateWebSettingReq{Enabled: boolPtr(false)}
	merged := mergeWebAccessUpdate(&webAccessOverrideJSON{Enabled: boolPtr(true)}, req.Enabled, req.ClearFields)
	if merged.Enabled == nil || *merged.Enabled {
		t.Fatalf("UpdateWebSettingReq 字段适配失败: %+v", merged.Enabled)
	}

	clearReq := &params.UpdateWebSettingReq{ClearFields: []string{"enabled"}}
	merged = mergeWebAccessUpdate(&webAccessOverrideJSON{Enabled: boolPtr(true)}, clearReq.Enabled, clearReq.ClearFields)
	if merged.Enabled != nil {
		t.Fatalf("UpdateWebSettingReq clearFields 适配失败: %+v", merged.Enabled)
	}
}
