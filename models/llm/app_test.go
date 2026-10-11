package model

import "testing"

// TestAppAllowPlanOverride 三态列规整：NULL→nil（跟随全局）；0→false；1→true。
func TestAppAllowPlanOverride(t *testing.T) {
	if v := (&App{AllowPlan: nil}).AllowPlanOverride(); v != nil {
		t.Fatalf("未配置（NULL）应规整为 nil（跟随全局），got %v", *v)
	}
	zero, one := 0, 1
	if v := (&App{AllowPlan: &zero}).AllowPlanOverride(); v == nil || *v {
		t.Fatalf("0 应规整为 false，got %v", v)
	}
	if v := (&App{AllowPlan: &one}).AllowPlanOverride(); v == nil || !*v {
		t.Fatalf("1 应规整为 true，got %v", v)
	}
}
