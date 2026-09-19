package mcpgateway

import "testing"

func TestApplyToolGrants(t *testing.T) {
	bindings := []ToolBinding{
		{ToolID: "t1", Name: "weather"},
		{ToolID: "t2", Name: "echo"},
		{ToolID: "t3", Name: "search"},
	}

	// 未授权任何工具 = 空清单（mcp-server 显式白名单语义）
	if got := applyToolGrants(bindings, nil); len(got) != 0 {
		t.Fatalf("空授权应返回空清单，实际 %#v", got)
	}
	if got := applyToolGrants(bindings, []string{}); len(got) != 0 {
		t.Fatalf("空授权应返回空清单，实际 %#v", got)
	}

	// 部分授权：只保留白名单内的工具，顺序保持作用域顺序
	got := applyToolGrants(bindings, []string{"t3", "t1"})
	if len(got) != 2 || got[0].ToolID != "t1" || got[1].ToolID != "t3" {
		t.Fatalf("部分授权过滤不符合预期: %#v", got)
	}

	// 授权了作用域外/已删除的工具 ID 不产生任何条目
	got = applyToolGrants(bindings, []string{"t2", "ghost"})
	if len(got) != 1 || got[0].ToolID != "t2" {
		t.Fatalf("越界授权应被忽略: %#v", got)
	}

	// 全量授权
	if got := applyToolGrants(bindings, []string{"t1", "t2", "t3"}); len(got) != 3 {
		t.Fatalf("全量授权应保留全部工具: %#v", got)
	}
}
