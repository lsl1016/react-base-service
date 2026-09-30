package mem

import (
	"strings"
	"testing"
	"time"

	"react-base-service/conf"
	model "react-base-service/models/llm"
	core "react-base-service/service/react/internal/core"
)

func TestResolveMemoryScopeWriteOwnerAndVisibility(t *testing.T) {
	withUser := ResolveMemoryScope("demo-app", "zhangsan", true)
	if len(withUser.owners) != 2 {
		t.Fatalf("expected caller+caller_user owners, got %d", len(withUser.owners))
	}
	if withUser.writeOwner != withUser.owners[1] || withUser.writeOwner.OwnerType != model.MemoryOwnerTypeCallerUser {
		t.Fatalf("write owner should be caller_user scope, got %+v", withUser.writeOwner)
	}
	if _, ok := withUser.allowedKeys[MemoryOwnerKey(model.BuildCallerMemoryOwner("demo-app"))]; !ok {
		t.Fatalf("caller owner should be visible")
	}

	shared := ResolveMemoryScope("demo-app", "zhangsan", false)
	if len(shared.owners) != 1 || shared.writeOwner.OwnerType != model.MemoryOwnerTypeCaller {
		t.Fatalf("user scope disabled should collapse to caller owner, got %+v", shared.owners)
	}
}

func TestMergeMemoryItemsPrefersLaterOwnerScope(t *testing.T) {
	base := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	// caller 条目更新时间更新，但 caller_user 作用域必须覆盖 caller。
	callerItem := model.MemoryItem{ID: 1, ItemKey: "k1", Title: "caller 版本", Layer: model.MemoryLayerDetached, OwnerType: model.MemoryOwnerTypeCaller, UpdatedAt: base.Add(time.Hour)}
	userItem := model.MemoryItem{ID: 2, ItemKey: "k1", Title: "user 版本", Layer: model.MemoryLayerDetached, OwnerType: model.MemoryOwnerTypeCallerUser, UpdatedAt: base}
	other := model.MemoryItem{ID: 3, ItemKey: "k2", Title: "独立事实", Layer: model.MemoryLayerDetached, OwnerType: model.MemoryOwnerTypeCaller, UpdatedAt: base}

	merged := MergeMemoryItems([]model.MemoryItem{callerItem, userItem, other})
	if len(merged) != 2 {
		t.Fatalf("same itemKey should merge, got %d items", len(merged))
	}
	if merged[0].ID != 2 {
		t.Fatalf("caller_user item should override caller item, got id=%d", merged[0].ID)
	}
	if merged[1].ID != 3 {
		t.Fatalf("unique item should survive, got id=%d", merged[1].ID)
	}
}

func TestRenderMemoryContextEmptyReturnsEmpty(t *testing.T) {
	got := RenderMemoryContext(nil, conf.ReactMemoryConfig{ResidentBudgetChars: 100, IndexMaxItems: 10})
	if got != "" {
		t.Fatalf("no items should render empty, got %q", got)
	}
}

func TestRenderMemoryContextResidentAndDetached(t *testing.T) {
	items := []model.MemoryItem{
		{ID: 11, Layer: model.MemoryLayerDetached, Title: "偏好", Content: "结论先给摘要", Description: "输出偏好相关任务"},
		{ID: 10, Layer: model.MemoryLayerResident, Title: "称呼", Content: "用户希望被称为陈总", Description: "称呼"},
	}
	got := RenderMemoryContext(items, conf.ReactMemoryConfig{ResidentBudgetChars: 2000, IndexMaxItems: 64})
	for _, expected := range []string{
		"<memory>",
		"### 常驻",
		"- [称呼] 用户希望被称为陈总",
		"### 记忆目录（需要时用 memory_read 按 itemId 读取全文）",
		"- #11 [偏好] 输出偏好相关任务",
		"</memory>",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("rendered context missing %q:\n%s", expected, got)
		}
	}
	if strings.Contains(got, "#10") {
		t.Fatalf("resident item should not appear in detached index:\n%s", got)
	}
}

func TestRenderMemoryContextBudgetAndIndexTruncation(t *testing.T) {
	items := []model.MemoryItem{
		{ID: 1, Layer: model.MemoryLayerResident, Title: "a", Content: strings.Repeat("长", 60), Description: "d"},
		{ID: 2, Layer: model.MemoryLayerResident, Title: "b", Content: strings.Repeat("长", 60), Description: "d"},
		{ID: 3, Layer: model.MemoryLayerDetached, Title: "c1", Content: "x", Description: "d"},
		{ID: 4, Layer: model.MemoryLayerDetached, Title: "c2", Content: "x", Description: "d"},
	}
	got := RenderMemoryContext(items, conf.ReactMemoryConfig{ResidentBudgetChars: 80, IndexMaxItems: 1})
	if !strings.Contains(got, "另有 1 条常驻记忆超出字符预算未注入") {
		t.Fatalf("budget overflow note missing:\n%s", got)
	}
	if !strings.Contains(got, "另有 1 条记忆未列出") {
		t.Fatalf("index overflow note missing:\n%s", got)
	}
	if strings.Contains(got, "#4") {
		t.Fatalf("index should cap at IndexMaxItems:\n%s", got)
	}
}

func TestMemoryItemHasTagAndKeywordMatch(t *testing.T) {
	item := model.MemoryItem{Title: "报表偏好", Description: "输出格式相关", Tags: "偏好,报表"}
	if !MemoryItemHasTag(item.Tags, "报表") {
		t.Fatalf("tag should match")
	}
	if MemoryItemHasTag(item.Tags, "不存在的标签") {
		t.Fatalf("unknown tag should not match")
	}
	if !MemoryItemMatchesKeyword(item, "输出格式") {
		t.Fatalf("keyword should match description")
	}
	if !MemoryItemMatchesKeyword(item, "偏好") {
		t.Fatalf("keyword should match title/tags case-insensitively")
	}
	if MemoryItemMatchesKeyword(item, "无关键词") {
		t.Fatalf("unrelated keyword should not match")
	}
}

func TestMemoryWriteToolDefinitionSchema(t *testing.T) {
	def := MemoryWriteToolDefinition()
	if def.Name != core.MetaToolMemoryWrite {
		t.Fatalf("unexpected tool name: %s", def.Name)
	}
	params, ok := def.Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties should be a map")
	}
	for _, key := range []string{"description", "action", "itemId", "version", "layer", "title", "content", "retrievalHint", "tags", "reason"} {
		if _, ok := params[key]; !ok {
			t.Fatalf("write tool schema missing property %q", key)
		}
	}
	required, _ := def.Parameters["required"].([]string)
	joined := strings.Join(required, ",")
	if !strings.Contains(joined, "action") || !strings.Contains(joined, "reason") {
		t.Fatalf("action/reason should be required, got %v", required)
	}
}
