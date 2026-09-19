package memory

import (
	"strings"
	"testing"
	"time"

	"react-base-service/conf"
	model "react-base-service/models/llm"
)

func TestRuntimeScopePrefersUserOwnerForWrites(t *testing.T) {
	scope := DefaultRuntime().ResolveScope("demo-app", "alice", true)
	if len(scope.Owners) != 2 {
		t.Fatalf("expected caller + caller_user owners, got %d", len(scope.Owners))
	}
	if scope.WriteOwner.OwnerType != model.MemoryOwnerTypeCallerUser {
		t.Fatalf("expected caller_user write owner, got %s", scope.WriteOwner.OwnerType)
	}
	if _, ok := scope.AllowedKeys[OwnerScopeKey(scope.Owners[0].OwnerType, scope.Owners[0].OwnerKey)]; !ok {
		t.Fatalf("caller scope should remain visible")
	}
}

func TestMergeRuntimeItemsPrefersUserScope(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	items := []model.MemoryItem{
		{ID: 1, ItemKey: "same", OwnerType: model.MemoryOwnerTypeCaller, UpdatedAt: base.Add(time.Hour)},
		{ID: 2, ItemKey: "same", OwnerType: model.MemoryOwnerTypeCallerUser, UpdatedAt: base},
	}
	merged := MergeRuntimeItems(items)
	if len(merged) != 1 || merged[0].ID != 2 {
		t.Fatalf("caller_user should override caller item, got %+v", merged)
	}
}

func TestRenderRuntimeContextKeepsResidentAndDetachedSemantics(t *testing.T) {
	items := []model.MemoryItem{
		{ID: 1, Layer: model.MemoryLayerResident, Title: "称呼", Content: "用户希望称为陈总"},
		{ID: 2, Layer: model.MemoryLayerDetached, Title: "偏好", Description: "回答格式偏好"},
	}
	got := RenderRuntimeContext(items, conf.ReactMemoryConfig{ResidentBudgetChars: 2000, IndexMaxItems: 64})
	for _, want := range []string{"<memory>", "### 常驻", "用户希望称为陈总", "#2 [偏好]", "</memory>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered context missing %q: %s", want, got)
		}
	}
}
