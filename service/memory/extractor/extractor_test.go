package extractor

import (
	"context"
	"strings"
	"testing"

	model "react-base-service/models/llm"
	memory "react-base-service/service/memory"

	"github.com/stretchr/testify/require"
)

// mockLLM 是可编程的 LLMInvoker 替身：按序返回脚本输出，记录收到的 prompt。
type mockLLM struct {
	responses []string
	err       error
	prompts   []string
	calls     int
}

func (m *mockLLM) ChatOnce(ctx context.Context, system, user string) (string, error) {
	m.calls++
	m.prompts = append(m.prompts, user)
	if m.err != nil {
		return "", m.err
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

func TestExtractJSONVariants(t *testing.T) {
	raw, err := ExtractJSON(`{"candidates": []}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"candidates": []}`, raw)

	raw, err = ExtractJSON("```json\n{\"candidates\": []}\n```")
	require.NoError(t, err)
	require.JSONEq(t, `{"candidates": []}`, raw)

	raw, err = ExtractJSON("好的，以下是抽取结果：\n{\"candidates\": [{\"content\": \"x\"}]}\n以上。")
	require.NoError(t, err)
	require.Contains(t, raw, "\"content\"")

	_, err = ExtractJSON("")
	require.Error(t, err)
	_, err = ExtractJSON("模型故障时可能输出这种没有 JSON 的文本")
	require.Error(t, err)
	_, err = ExtractJSON("{\"candidates\": [}")
	require.Error(t, err)
}

func TestExtractParsesAndNormalizesCandidates(t *testing.T) {
	llm := &mockLLM{responses: []string{`{"candidates": [
		{"type": "preference", "title": "称呼", "content": "用户希望被称呼为老李", "description": "交流称呼", "tags": ["称呼"], "confidence": 0.9, "importance": 5, "similarIds": [7], "reason": "用户明确表述"},
		{"type": "prefernce", "title": "", "content": "类型非法且无标题，类型应回退 fact 并以正文为标题"},
		{"content": ""},
		{"type": "fact", "title": "超长标题应该被截断到三十二个字以内避免写核心校验失败超长标题应该被截断", "content": "内容"}
	]}`}}
	candidates, err := Extract(context.Background(), llm, ExtractInput{
		Summary:       "摘要",
		Transcript:    "转录",
		Manifest:      "清单",
		CurrentDate:   "2026-09-25",
		MaxCandidates: 8,
	})
	require.NoError(t, err)
	require.Len(t, candidates, 3)
	require.Equal(t, model.MemoryTypePreference, candidates[0].Type)
	require.Equal(t, uint64(7), candidates[0].SimilarIDs[0])
	require.Equal(t, model.MemoryTypeFact, candidates[1].Type, "非法类型回退 fact")
	require.Equal(t, "类型非法且无标题，类型应回退 fact 并以正文为标题", candidates[1].Title, "空标题回退为正文")
	require.Equal(t, model.MemoryTypeFact, candidates[2].Type)
	require.True(t, len([]rune(candidates[2].Title)) <= memory.MaxTitleRunes, "标题截断到写核心约束内")
	require.True(t, strings.Contains(llm.prompts[0], "记忆类型定义"), "prompt 应包含类型定义")
	require.True(t, strings.Contains(llm.prompts[0], "2026-09-25"), "prompt 应包含观察日期")
}

func TestExtractLLMFailureIsErrorNotSilentEmpty(t *testing.T) {
	llm := &mockLLM{err: context.DeadlineExceeded}
	_, err := Extract(context.Background(), llm, ExtractInput{MaxCandidates: 8})
	require.Error(t, err, "LLM 故障必须返回 error，不能等价于无候选")
}

func TestPrefilterCandidatesDedupThresholdAndCap(t *testing.T) {
	existing := []string{"用户负责数据平台项目", "另一个存量事实"}
	existingKeys := make(map[string]struct{}, len(existing))
	for _, content := range existing {
		existingKeys[memory.ItemKey(content)] = struct{}{}
	}
	candidates := []Candidate{
		{Type: model.MemoryTypeFact, Title: "重复存量", Content: "用户负责数据平台项目", Confidence: 0.99},
		{Type: model.MemoryTypeFact, Title: "批内重复A", Content: "批内重复的事实", Confidence: 0.9},
		{Type: model.MemoryTypeFact, Title: "批内重复B", Content: "批内重复的事实", Confidence: 0.8},
		{Type: model.MemoryTypeFact, Title: "低置信", Content: "低置信度候选", Confidence: 0.3},
		{Type: model.MemoryTypeFact, Title: "保留一", Content: "候选一", Confidence: 0.95},
		{Type: model.MemoryTypeFact, Title: "保留二", Content: "候选二", Confidence: 0.7},
	}
	result := PrefilterCandidates(context.Background(), candidates, existingKeys, 0.5, 2)
	require.Len(t, result, 2, "存量去重+批内去重+阈值过滤后按上限截断")
	require.Equal(t, "保留一", result[0].Title, "按 confidence 降序保留")
	require.Equal(t, "候选一", result[0].Content)
}

func TestBuildManifestRendersAndTruncates(t *testing.T) {
	require.Equal(t, "（当前没有长期记忆）", BuildManifest(nil))
	items := []model.MemoryItem{
		{ID: 7, MemoryType: model.MemoryTypePreference, Confidence: 0.9, Title: "称呼", Description: "交流称呼", Content: strings.Repeat("长", 200)},
	}
	manifest := BuildManifest(items)
	require.Contains(t, manifest, "- #7 [preference|0.90] 称呼 — 交流称呼 :: ")
	require.True(t, len([]rune(manifest)) < 300, "正文预览应截断")
}

func TestTruncateRunes(t *testing.T) {
	require.Equal(t, "abc", TruncateRunes("abc", 0))
	require.Equal(t, "ab…", TruncateRunes("abc", 2))
	require.Equal(t, "张三丰", TruncateRunes("张三丰", 3))
}
