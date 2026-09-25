package memory

import (
	"strings"
	"testing"
	"time"

	"react-base-service/conf"
	model "react-base-service/models/llm"

	"github.com/stretchr/testify/require"
)

func TestNormalizeMemoryType(t *testing.T) {
	require.Equal(t, model.MemoryTypePreference, model.NormalizeMemoryType("preference"))
	require.Equal(t, model.MemoryTypeFact, model.NormalizeMemoryType("fact"))
	require.Equal(t, model.MemoryTypeFact, model.NormalizeMemoryType(""))
	require.Equal(t, model.MemoryTypeFact, model.NormalizeMemoryType("Perference"))
	require.Equal(t, model.MemoryTypeFact, model.NormalizeMemoryType("memo"))
	require.True(t, model.IsValidMemoryType(model.MemoryTypeEvent))
	require.True(t, model.IsValidMemoryType(model.MemoryTypeProcedure))
	require.False(t, model.IsValidMemoryType(""))
	require.False(t, model.IsValidMemoryType("memo"))
}

func TestNormalizeMemoryConfidenceAndImportance(t *testing.T) {
	require.Equal(t, model.MemoryConfidenceDefault, model.NormalizeMemoryConfidence(0))
	require.Equal(t, model.MemoryConfidenceDefault, model.NormalizeMemoryConfidence(1.2))
	require.InDelta(t, 0.92, model.NormalizeMemoryConfidence(0.92), 1e-9)
	// <=0 视为未提供，回退默认；正向越界 clamp 到 [1,5]。
	require.Equal(t, model.MemoryImportanceDefault, model.NormalizeMemoryImportance(0))
	require.Equal(t, model.MemoryImportanceDefault, model.NormalizeMemoryImportance(-2))
	require.Equal(t, model.MemoryImportanceMax, model.NormalizeMemoryImportance(9))
	require.Equal(t, 4, model.NormalizeMemoryImportance(4))
}

// preferencePrefersImportanceThenRecency 是偏好节排序的期望顺序构造器。
func memoryItemForRender(id uint, ownerType, layer, memoryType, title, content string, importance int, updatedAt time.Time) model.MemoryItem {
	return model.MemoryItem{
		ID:         id,
		OwnerType:  ownerType,
		OwnerKey:   "caller",
		Layer:      layer,
		MemoryType: memoryType,
		Title:      title,
		Content:    content,
		ItemKey:    "key-" + title,
		Importance: importance,
		UpdatedAt:  updatedAt,
	}
}

func TestRenderRuntimeContextPreferenceSectionFirst(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.Local)
	items := []model.MemoryItem{
		memoryItemForRender(1, model.MemoryOwnerTypeCaller, model.MemoryLayerResident, model.MemoryTypeFact, "项目口径", "系统是 AI 基座", 3, base),
		memoryItemForRender(2, model.MemoryOwnerTypeCaller, model.MemoryLayerDetached, model.MemoryTypePreference, "称呼", "用户希望被称呼为老李", 5, base),
		memoryItemForRender(3, model.MemoryOwnerTypeCaller, model.MemoryLayerDetached, model.MemoryTypeEvent, "方案决定", "截至 2026-09-20 确定采用 V4 协议", 3, base),
	}

	rendered := RenderRuntimeContext(items, defaultRenderTestConfig())
	require.Contains(t, rendered, "### 用户偏好（始终生效）")
	require.Contains(t, rendered, "用户希望被称呼为老李")
	require.Contains(t, rendered, "### 常驻")
	require.Contains(t, rendered, "系统是 AI 基座")
	require.Contains(t, rendered, "### 记忆目录")
	// preference 全文注入后不得再出现在目录中；非 fact 类型目录行带类型标记。
	require.Contains(t, rendered, "- #3 [event] 方案决定 — ")
	require.NotContains(t, rendered, "- #2 [")
	require.NotContains(t, rendered, "称呼 —")
	// 偏好节在常驻节之前。
	require.Less(t, strings.Index(rendered, "### 用户偏好"), strings.Index(rendered, "### 常驻"))
}

func TestRenderRuntimeContextPreferenceOrderAndBudgetFallback(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.Local)
	high := memoryItemForRender(11, model.MemoryOwnerTypeCaller, model.MemoryLayerDetached, model.MemoryTypePreference, "格式偏好", "用户喜欢先总结后展开", 5, base)
	low := memoryItemForRender(12, model.MemoryOwnerTypeCaller, model.MemoryLayerDetached, model.MemoryTypePreference, "旧偏好", "这条偏好更新的时间更早且重要度更低", 2, base.Add(-time.Hour))

	// 预算充足：两条按 importance 降序全文注入。
	rendered := RenderRuntimeContext([]model.MemoryItem{low, high}, defaultRenderTestConfig())
	highIdx := strings.Index(rendered, "先总结后展开")
	lowIdx := strings.Index(rendered, "重要度更低")
	require.Greater(t, highIdx, -1)
	require.Greater(t, lowIdx, highIdx, "importance 高的偏好应排在前")

	// 预算极小：偏好回退为目录条目并带类型标记，常驻事实被挤出。
	cfg := defaultRenderTestConfig()
	cfg.ResidentBudgetChars = 5
	tight := RenderRuntimeContext([]model.MemoryItem{high, low}, cfg)
	require.Contains(t, tight, "- #11 [preference] 格式偏好 — ")
	require.Contains(t, tight, "- #12 [preference] 旧偏好 — ")
	require.Contains(t, tight, "超出字符预算")
}

func TestRenderRuntimeContextFactDirectoryKeepsV1Format(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.Local)
	fact := memoryItemForRender(21, model.MemoryOwnerTypeCaller, model.MemoryLayerDetached, model.MemoryTypeFact, "部署形态", "服务部署在 K8s", 3, base)
	rendered := RenderRuntimeContext([]model.MemoryItem{fact}, defaultRenderTestConfig())
	require.Contains(t, rendered, "- #21 [部署形态] 服务部署在 K8s")
	require.NotContains(t, rendered, "[fact]")
}

func TestRenderRuntimeContextEmptyReturnsEmpty(t *testing.T) {
	require.Empty(t, RenderRuntimeContext(nil, defaultRenderTestConfig()))
}

func defaultRenderTestConfig() conf.ReactMemoryConfig {
	cfg := conf.ReactMemoryConfig{
		ResidentBudgetChars: 2000,
		ResidentMaxItems:    16,
		IndexMaxItems:       64,
		DetachedMaxItems:    500,
	}
	return cfg
}
