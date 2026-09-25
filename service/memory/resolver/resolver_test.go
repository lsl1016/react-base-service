package resolver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	model "react-base-service/models/llm"
	memory "react-base-service/service/memory"
	"react-base-service/service/memory/extractor"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// mockLLM 是 resolver 侧的可编程 LLMInvoker 替身：按序返回脚本输出，记录收到的 prompt。
type mockLLM struct {
	responses []string
	prompts   []string
	calls     int
}

func (m *mockLLM) ChatOnce(ctx context.Context, system, user string) (string, error) {
	m.calls++
	m.prompts = append(m.prompts, user)
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

// recordingApply 记录写核心入参的替身；按脚本返回结果或错误。
type recordingApply struct {
	inputs []memory.MutationInput
	result map[string]interface{}
	err    error
}

func (r *recordingApply) apply(ctx *gin.Context, input memory.MutationInput) (map[string]interface{}, error) {
	r.inputs = append(r.inputs, input)
	if r.err != nil {
		return nil, r.err
	}
	return map[string]interface{}{"action": input.Action, "itemId": uint(99), "version": 2}, nil
}

// stubLikeRecall 打桩 LIKE 兜底召回（避免单测触库），测试结束自动还原。
func stubLikeRecall(t *testing.T) {
	t.Helper()
	original := likeRecall
	likeRecall = func(ctx *gin.Context, owner model.MemoryOwner, keywords []string, limit int) ([]model.MemoryItem, error) {
		return nil, nil
	}
	t.Cleanup(func() { likeRecall = original })
}

func TestResolveAddsCandidateWithoutSimilarWithoutLLM(t *testing.T) {
	stubLikeRecall(t)
	llm := &mockLLM{}
	apply := &recordingApply{}
	candidates := []extractor.Candidate{{
		Type: model.MemoryTypePreference, Title: "称呼", Content: "用户希望被称呼为老李",
		Description: "交流称呼", Confidence: 0.9, Importance: 5, Reason: "用户明确表述",
	}}
	decisions, err := Resolve(nil, llm, apply.apply, testResolveInput(), candidates)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, ActionAdd, decisions[0].Action)
	require.True(t, decisions[0].Applied)
	require.Zero(t, llm.calls, "无相似条目的候选不需要消解 LLM 调用")
	require.Len(t, apply.inputs, 1)
	input := apply.inputs[0]
	require.Equal(t, "create", input.Action)
	require.Equal(t, model.MemorySourceExtractor, input.Source)
	require.Equal(t, model.MemoryTypePreference, input.MemoryType)
	require.True(t, input.RespectLocked)
	require.Equal(t, "caller_user|zhangsan", input.Owner.OwnerKey)
	require.Contains(t, input.Reason, "用户明确表述")
}

func TestResolveUpdateSupersedeAndSkip(t *testing.T) {
	stubLikeRecall(t)
	items := []model.MemoryItem{
		{ID: 12, OwnerType: model.MemoryOwnerTypeCallerUser, OwnerKey: "caller_user|zhangsan", Layer: model.MemoryLayerDetached,
			MemoryType: model.MemoryTypePreference, Title: "回答风格", Content: "用户喜欢简短回答", Confidence: 0.8, Importance: 3, Version: 4},
		{ID: 13, OwnerType: model.MemoryOwnerTypeCallerUser, OwnerKey: "caller_user|zhangsan", Layer: model.MemoryLayerDetached,
			MemoryType: model.MemoryTypeFact, Title: "技术栈", Content: "项目使用 Go", Confidence: 0.8, Importance: 3, Version: 2},
	}
	input := testResolveInput()
	input.Items = items
	candidates := []extractor.Candidate{
		{Type: model.MemoryTypePreference, Title: "回答风格", Content: "用户希望详细分析", Confidence: 0.95, SimilarIDs: []uint64{12}},
		{Type: model.MemoryTypeFact, Title: "技术栈", Content: "项目使用 Go 与 Vue", Confidence: 0.9, SimilarIDs: []uint64{13}},
		{Type: model.MemoryTypeFact, Title: "无关", Content: "用户在杭州办公", Confidence: 0.9},
	}
	llm := &mockLLM{responses: []string{`{"decisions": [
		{"candidate": "C1", "decision": "supersede", "target": "M12", "mergedContent": "", "reason": "偏好从简短改为详细"},
		{"candidate": "C2", "decision": "update", "target": "M13", "mergedContent": "项目使用 Go 与 Vue，前端为 Vue3", "reason": "补充技术栈细节"},
		{"candidate": "C3", "decision": "skip", "target": "", "reason": ""}
	]}`}}
	apply := &recordingApply{}
	decisions, err := Resolve(nil, llm, apply.apply, input, candidates)
	require.NoError(t, err)
	require.Len(t, decisions, 3)

	require.Equal(t, ActionSupersede, decisions[0].Action)
	require.True(t, decisions[0].Applied)
	require.Equal(t, uint(12), apply.inputs[0].ItemID)
	require.Equal(t, int64(4), int64(apply.inputs[0].Version), "update 使用清单中的乐观锁版本")
	require.Equal(t, "用户希望详细分析", apply.inputs[0].Content, "supersede 用候选内容替换")
	require.Contains(t, apply.inputs[0].Reason, "取代旧事实")

	require.Equal(t, ActionUpdate, decisions[1].Action)
	require.True(t, decisions[1].Applied)
	require.Equal(t, "项目使用 Go 与 Vue，前端为 Vue3", apply.inputs[1].Content)
	require.Equal(t, int64(2), int64(apply.inputs[1].Version))

	require.Equal(t, ActionSkip, decisions[2].Action, "等价候选被 skip")
	require.False(t, decisions[2].Applied)
	require.Len(t, apply.inputs, 2)
	require.Equal(t, 1, llm.calls, "有相似条目的候选拼进一次批量消解调用")
}

func TestResolveGuardsLockedTargetAndHallucinatedID(t *testing.T) {
	stubLikeRecall(t)
	items := []model.MemoryItem{
		{ID: 21, OwnerType: model.MemoryOwnerTypeCallerUser, OwnerKey: "caller_user|zhangsan",
			MemoryType: model.MemoryTypeFact, Title: "锁定条目", Content: "内容A", Tags: "locked", Version: 1},
	}
	input := testResolveInput()
	input.Items = items
	candidates := []extractor.Candidate{
		{Type: model.MemoryTypeFact, Title: "锁定", Content: "内容A 的新版本", Confidence: 0.9, SimilarIDs: []uint64{21}},
		{Type: model.MemoryTypeFact, Title: "幻觉", Content: "指向不存在条目的候选", Confidence: 0.9, SimilarIDs: []uint64{999}},
	}
	llm := &mockLLM{responses: []string{`{"decisions": [
		{"candidate": "C1", "decision": "supersede", "target": "M21", "mergedContent": "x", "reason": "r"},
		{"candidate": "C2", "decision": "update", "target": "M404", "mergedContent": "y", "reason": "r"}
	]}`}}
	apply := &recordingApply{}
	decisions, err := Resolve(nil, llm, apply.apply, input, candidates)
	require.NoError(t, err)
	require.False(t, decisions[0].Applied)
	require.Contains(t, decisions[0].Note, "locked")
	require.Equal(t, ActionSkip, decisions[1].Action)
	require.Contains(t, decisions[1].Note, "不存在")
	require.Empty(t, apply.inputs)
}

func TestResolveWriteLimitAndAddFailure(t *testing.T) {
	stubLikeRecall(t)
	input := testResolveInput()
	input.MaxWrites = 1
	candidates := []extractor.Candidate{
		{Type: model.MemoryTypeFact, Title: "一", Content: "候选一", Confidence: 0.9},
		{Type: model.MemoryTypeFact, Title: "二", Content: "候选二", Confidence: 0.8},
	}
	llm := &mockLLM{}
	apply := &recordingApply{}
	decisions, err := Resolve(nil, llm, apply.apply, input, candidates)
	require.NoError(t, err)
	require.True(t, decisions[0].Applied)
	require.Equal(t, ActionSkip, decisions[1].Action)
	require.Contains(t, decisions[1].Note, "限额")

	// 写入失败（如敏感拦截）不占用成功写入，也不得标记 Applied。
	failing := &recordingApply{err: fmt.Errorf("内容包含疑似敏感信息")}
	candidates = []extractor.Candidate{{Type: model.MemoryTypeFact, Title: "敏感", Content: "sk-abc", Confidence: 0.9}}
	decisions, err = Resolve(nil, llm, failing.apply, input, candidates)
	require.NoError(t, err)
	require.False(t, decisions[0].Applied)
	require.Contains(t, decisions[0].Note, "写入失败")
}

func TestResolveLLMFailureReturnsError(t *testing.T) {
	stubLikeRecall(t)
	items := []model.MemoryItem{
		{ID: 31, OwnerType: model.MemoryOwnerTypeCallerUser, OwnerKey: "caller_user|zhangsan",
			MemoryType: model.MemoryTypeFact, Title: "相关", Content: "相关存量内容", Version: 1},
	}
	input := testResolveInput()
	input.Items = items
	candidates := []extractor.Candidate{{Type: model.MemoryTypeFact, Title: "候选", Content: "相关存量内容的候选", Confidence: 0.9, SimilarIDs: []uint64{31}}}
	llm := &mockLLM{responses: []string{"不是 JSON"}}
	_, err := Resolve(nil, llm, (&recordingApply{}).apply, input, candidates)
	require.Error(t, err)
	require.Contains(t, err.Error(), "解析失败")
}

func TestResolveKeywordRecallMergesWithSimilarIDs(t *testing.T) {
	stubLikeRecall(t)
	items := []model.MemoryItem{
		{ID: 41, OwnerType: model.MemoryOwnerTypeCallerUser, OwnerKey: "caller_user|zhangsan",
			MemoryType: model.MemoryTypeFact, Title: "自报条目", Content: "自报内容", Version: 1},
	}
	input := testResolveInput()
	input.Items = items
	input.Owners = []model.MemoryOwner{{OwnerType: model.MemoryOwnerTypeCallerUser, OwnerKey: "caller_user|zhangsan"}}
	candidates := []extractor.Candidate{{Type: model.MemoryTypeFact, Title: "候选", Content: "自报内容 相关候选", Confidence: 0.9, SimilarIDs: []uint64{41}}}
	llm := &mockLLM{responses: []string{`{"decisions": [{"candidate": "C1", "decision": "add"}]}`}}
	decisions, err := Resolve(nil, llm, (&recordingApply{}).apply, input, candidates)
	require.NoError(t, err)
	require.Equal(t, ActionAdd, decisions[0].Action)
	require.True(t, strings.Contains(llm.prompts[0], "M41"), "自报条目应出现在消解 prompt 中")
}

func TestParseNumberedRef(t *testing.T) {
	id, ok := parseNumberedRef("C1", "C")
	require.True(t, ok)
	require.Equal(t, 1, id)
	id, ok = parseNumberedRef("m12", "M")
	require.True(t, ok)
	require.Equal(t, 12, id)
	_, ok = parseNumberedRef("M0", "M")
	require.False(t, ok)
	_, ok = parseNumberedRef("M", "M")
	require.False(t, ok)
}

func testResolveInput() ResolveInput {
	return ResolveInput{
		Owners:      []model.MemoryOwner{{OwnerType: model.MemoryOwnerTypeCallerUser, OwnerKey: "caller_user|zhangsan"}},
		WriteOwner:  model.MemoryOwner{OwnerType: model.MemoryOwnerTypeCallerUser, OwnerKey: "caller_user|zhangsan"},
		Items:       nil,
		SimilarTopK: 5,
		MaxWrites:   10,
		CreatedBy:   "run-test",
	}
}
