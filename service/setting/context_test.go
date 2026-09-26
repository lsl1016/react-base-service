package setting

import (
	"strings"
	"testing"

	"react-base-service/components/params"
)

// 上下文压缩策略更新校验：范围外的数字与未知 clearFields 必须拒绝。
func TestValidateContextCompactUpdate(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	boolPtr := func(v bool) *bool { return &v }

	if err := validateContextCompactUpdate(&params.UpdateContextCompactSettingReq{
		TokenTrigger: intPtr(500), // 低于下限 8192
	}); err == nil {
		t.Fatalf("expected range error for tokenTrigger=500")
	}
	if err := validateContextCompactUpdate(&params.UpdateContextCompactSettingReq{
		TokenTrigger: intPtr(3_000_000), // 高于上限
	}); err == nil {
		t.Fatalf("expected range error for tokenTrigger=3000000")
	}
	if err := validateContextCompactUpdate(&params.UpdateContextCompactSettingReq{
		BufferTokens: intPtr(-1),
	}); err == nil {
		t.Fatalf("expected range error for bufferTokens=-1")
	}
	if err := validateContextCompactUpdate(&params.UpdateContextCompactSettingReq{
		ClearFields: []string{"notAField"},
	}); err == nil {
		t.Fatalf("expected error for unknown clearField")
	}
	if err := validateContextCompactUpdate(&params.UpdateContextCompactSettingReq{
		TokenTrigger: intPtr(0), // 在 clearFields 中声明的字段不参与校验
		ClearFields:  []string{"tokenTrigger"},
	}); err != nil {
		t.Fatalf("cleared field should skip validation: %v", err)
	}
	if err := validateContextCompactUpdate(&params.UpdateContextCompactSettingReq{
		Enabled: boolPtr(false), MicrocompactEnabled: boolPtr(true),
		TokenTrigger: intPtr(170000), TokenTarget: intPtr(90000),
		SummaryLimit: intPtr(12000), OutputReserveTokens: intPtr(32768),
		BufferTokens: intPtr(13000), MicrocompactKeepRecent: intPtr(5),
	}); err != nil {
		t.Fatalf("valid update should pass: %v", err)
	}
}

// 合并语义：clear 优先清空，非 nil 覆盖，其余保留现状。
func TestMergeContextCompactUpdate(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	boolPtr := func(v bool) *bool { return &v }

	current := &contextCompactOverrideJSON{TokenTrigger: intPtr(150000), Enabled: boolPtr(true)}
	merged := mergeContextCompactUpdate(current, &params.UpdateContextCompactSettingReq{
		TokenTarget: intPtr(80000),
		ClearFields: []string{"tokenTrigger"},
	})
	if merged.TokenTrigger != nil {
		t.Fatalf("cleared tokenTrigger should be nil")
	}
	if merged.TokenTarget == nil || *merged.TokenTarget != 80000 {
		t.Fatalf("tokenTarget should be overridden: %+v", merged.TokenTarget)
	}
	if merged.Enabled == nil || !*merged.Enabled {
		t.Fatalf("enabled should be preserved from current: %+v", merged.Enabled)
	}
}

// 生效视图与存储结构/conf 覆盖结构之间的转换保持字段一一对应。
func TestToConfContextCompactOverride(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	boolPtr := func(v bool) *bool { return &v }
	converted := toConfContextCompactOverride(&contextCompactOverrideJSON{
		Enabled: boolPtr(false), TokenTrigger: intPtr(160000),
	})
	if converted == nil || converted.Enabled == nil || *converted.Enabled {
		t.Fatalf("enabled conversion broken: %+v", converted)
	}
	if converted.TokenTrigger == nil || *converted.TokenTrigger != 160000 {
		t.Fatalf("tokenTrigger conversion broken: %+v", converted.TokenTrigger)
	}
	if toConfContextCompactOverride(nil) != nil {
		t.Fatalf("nil override should stay nil")
	}
	if !strings.HasPrefix(SettingKeyContext, "context") {
		t.Fatalf("unexpected setting key: %s", SettingKeyContext)
	}
}
