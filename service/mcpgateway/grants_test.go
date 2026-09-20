package mcpgateway

import "testing"

func TestApplyToolGrants(t *testing.T) {
	bindings := []ToolBinding{
		{ToolID: "t1", Name: "weather"},
		{ToolID: "t2", Name: "echo"},
		{ToolID: "t3", Name: "search"},
	}

	// 未绑定任何工具 = 空清单（显式白名单语义）
	if got := applyToolGrants(bindings, nil); len(got) != 0 {
		t.Fatalf("空绑定应返回空清单，实际 %#v", got)
	}
	if got := applyToolGrants(bindings, []string{}); len(got) != 0 {
		t.Fatalf("空绑定应返回空清单，实际 %#v", got)
	}

	// 部分绑定：只保留白名单内的工具，顺序保持基础集合顺序
	got := applyToolGrants(bindings, []string{"t3", "t1"})
	if len(got) != 2 || got[0].ToolID != "t1" || got[1].ToolID != "t3" {
		t.Fatalf("部分绑定过滤不符合预期: %#v", got)
	}

	// 绑定了已删除/不存在的基础集合外工具 ID 不产生任何条目
	got = applyToolGrants(bindings, []string{"t2", "ghost"})
	if len(got) != 1 || got[0].ToolID != "t2" {
		t.Fatalf("越界绑定应被忽略: %#v", got)
	}

	// 全量绑定
	if got := applyToolGrants(bindings, []string{"t1", "t2", "t3"}); len(got) != 3 {
		t.Fatalf("全量绑定应保留全部工具: %#v", got)
	}
}

// TestDedupeToolBindings 验证基础集合同名去重：MCP 工具名须全局唯一，
// 多 caller 名下同名 http 工具只保留排序靠前的一条（输入按 name,id 升序）。
func TestDedupeToolBindings(t *testing.T) {
	got := dedupeToolBindings(nil, []ToolBinding{
		{ToolID: "t3", Name: "search"},
		{ToolID: "t5", Name: "weather"},
		{ToolID: "t2", Name: "weather"},
		{ToolID: "t9", Name: "weather"},
	})
	if len(got) != 2 {
		t.Fatalf("同名应去重: %#v", got)
	}
	if got[0].Name != "search" || got[0].ToolID != "t3" {
		t.Fatalf("去重不应破坏原有顺序: %#v", got)
	}
	if got[1].Name != "weather" || got[1].ToolID != "t5" {
		t.Fatalf("同名冲突应保留排序靠前的一条: %#v", got)
	}
}
