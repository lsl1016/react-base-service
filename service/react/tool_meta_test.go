package react

import (
	"strings"
	"testing"

	llm "react-base-service/api/llm"
	agentService "react-base-service/service/agent"
	model "react-base-service/models/llm"
)

// TestAgentToolRefAllows（WP2 白名单硬执行）：get_tool 激活路径的判定语义与
// FilterToolIndexSnapshot 对齐——nil 不限制、@none 全拒、@readonly 仅只读、具名精确匹配。
func TestAgentToolRefAllows(t *testing.T) {
	writableTool := model.Tool{ToolID: "tool_w", Name: "update_row", Config: `{}`}
	readOnlyTool := model.Tool{ToolID: "tool_r", Name: "query_log", Config: `{"readOnly":true}`}

	// nil（外层 run / 白名单空=继承全部）：不限制
	if !agentToolRefAllows(nil, writableTool) {
		t.Fatalf("nil refs 不应限制")
	}
	// @none：全部拒绝
	if agentToolRefAllows([]string{agentService.AgentToolRefNone}, readOnlyTool) {
		t.Fatalf("@none 应拒绝一切业务工具")
	}
	// @readonly：只读放行、可写拒绝
	if !agentToolRefAllows([]string{agentService.AgentToolRefReadOnly}, readOnlyTool) {
		t.Fatalf("@readonly 应放行只读工具")
	}
	if agentToolRefAllows([]string{agentService.AgentToolRefReadOnly}, writableTool) {
		t.Fatalf("@readonly 应拒绝可写工具")
	}
	// 具名：按 name/toolId 精确匹配
	if !agentToolRefAllows([]string{"update_row"}, writableTool) {
		t.Fatalf("具名白名单应按 name 放行")
	}
	if !agentToolRefAllows([]string{"tool_w"}, writableTool) {
		t.Fatalf("具名白名单应按 toolId 放行")
	}
	if agentToolRefAllows([]string{"other_tool"}, writableTool) {
		t.Fatalf("具名白名单外的工具应拒绝")
	}
}

// TestReadOnlyViolationResult（WP2 验收）：只读执行域内非只读业务工具被硬拦截，
// 只读工具放行；执行域未开启时一律放行。
func TestReadOnlyViolationResult(t *testing.T) {
	s := &reactEngineState{}
	writableTool := model.Tool{ToolID: "tool_w", Name: "update_row", Config: `{}`}
	readOnlyTool := model.Tool{ToolID: "tool_r", Name: "query_log", Config: `{"readOnly":true}`}
	call := llm.ToolCall{ID: "call_1"}

	// 执行域未开启：全部放行
	if s.readOnlyViolationResult(call, writableTool, executedByServer) != nil {
		t.Fatalf("执行域未开启时不应拦截")
	}

	s.req = &runtimeRequest{}
	s.req.enforceReadOnlyTools = true
	if blocked := s.readOnlyViolationResult(call, writableTool, executedByServer); blocked == nil {
		t.Fatalf("只读执行域应拦截非只读工具")
	} else if !blocked.IsError || !strings.Contains(blocked.Content, "只读执行域") {
		t.Fatalf("拦截结果应为错误并说明只读域: %+v", blocked)
	}
	if blocked := s.readOnlyViolationResult(call, readOnlyTool, executedByServer); blocked != nil {
		t.Fatalf("只读工具应放行: %+v", blocked)
	}
}

// TestBusinessToolMetaFromConfig（WP1）：业务工具 config 的执行语义解析与保守缺省。
func TestBusinessToolMetaFromConfig(t *testing.T) {
	readOnly := businessToolMeta(model.Tool{Config: `{"readOnly":true,"maxOutputBytes":4096,"timeout_ms":5000}`})
	if !readOnly.ReadOnly || !readOnly.ConcurrentSafe || readOnly.MaxOutputBytes != 4096 || readOnly.TimeoutMs != 5000 {
		t.Fatalf("readOnly 工具元数据解析不符: %+v", readOnly)
	}
	if readOnly.SideEffect != toolSideEffectNone || readOnly.RiskLevel != toolRiskLevelLow {
		t.Fatalf("readOnly 工具副作用/风险应为 none/low: %+v", readOnly)
	}

	writable := businessToolMeta(model.Tool{Config: `{"url":"https://x"}`})
	if writable.ReadOnly || writable.ConcurrentSafe {
		t.Fatalf("未声明 readOnly 应保守按可写串行: %+v", writable)
	}
	if writable.SideEffect != toolSideEffectSystem || writable.RiskLevel != toolRiskLevelMedium {
		t.Fatalf("可写工具缺省应为 system/medium: %+v", writable)
	}

	// config 非法时保守缺省
	broken := businessToolMeta(model.Tool{Config: `{invalid`})
	if broken.ReadOnly || broken.ConcurrentSafe {
		t.Fatalf("config 非法应保守按可写: %+v", broken)
	}
}

// TestWebFetchContentExtraction：HTML 去脚本/样式/标签与实体解码；文本类原样返回。
func TestWebFetchContentExtraction(t *testing.T) {
	html := `<html><head><style>body{color:red}</style><script>var x=1;</script></head>` +
		`<body><h1>标题</h1><p>第一段&amp;符号</p><p>第二段</p><!-- 注释 --></body></html>`
	got := extractWebFetchContent("text/html; charset=utf-8", html)
	for _, banned := range []string{"var x=1", "color:red", "<p>", "注释"} {
		if strings.Contains(got, banned) {
			t.Fatalf("提取结果不应包含 %q: %q", banned, got)
		}
	}
	for _, want := range []string{"标题", "第一段&符号", "第二段"} {
		if !strings.Contains(got, want) {
			t.Fatalf("提取结果缺少 %q: %q", want, got)
		}
	}
	if plain := extractWebFetchContent("application/json", `{"k":1}`); plain != `{"k":1}` {
		t.Fatalf("JSON 应原样返回: %q", plain)
	}
}

// TestIsPrivateHost：web_fetch 的内网/环回字面量拒绝。
func TestIsPrivateHost(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.0.9", "169.254.1.1", "0.0.0.0", "::1"} {
		if !isPrivateHost(host) {
			t.Fatalf("%s 应判为内网/环回", host)
		}
	}
	for _, host := range []string{"example.com", "8.8.8.8", "docs.example.org"} {
		if isPrivateHost(host) {
			t.Fatalf("%s 不应判为内网", host)
		}
	}
}
