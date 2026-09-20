package react

import (
	"encoding/json"
	"testing"

	llm "react-base-service/api/llm"
	model "react-base-service/models/llm"
)

func businessToolForBreakdown(toolID, name, toolType string) model.Tool {
	return model.Tool{ToolID: toolID, Name: name, ToolType: toolType}
}

// TestClassifyContextMessages 覆盖上下文构成的六类归因：
// 系统前缀、对话正文、MCP 业务工具（get_tool/execute_tool 轮次）、
// 内置 Meta Tool、技能加载与临时提醒。
func TestClassifyContextMessages(t *testing.T) {
	activeTools := map[string]model.Tool{
		"call_report_query": businessToolForBreakdown("t-1", "report_query", "http"),
		"call_sql_diff":     businessToolForBreakdown("t-2", "sql_diff", "client"),
	}
	tools := []llm.ToolDefinition{
		{Name: metaToolGetTool, Description: "加载业务工具", Parameters: map[string]interface{}{"type": "object"}},
		{Name: metaToolTodoWrite, Description: "更新任务清单", Parameters: map[string]interface{}{"type": "object"}},
	}

	messages := []llm.ChatMessage{
		{Role: "system", Content: "你是数据分析助手"},
		{Role: "user", Content: "帮我查一下本周的订单数据"},
		{Role: "assistant", Parts: []llm.ContentPart{
			{Type: "thinking", Thinking: "需要先加载报表工具"},
			{Type: "tool_use", ID: "tu-1", Name: metaToolGetTool, Input: json.RawMessage(`{"toolId":"t-1"}`)},
		}},
		{Role: "user", Parts: []llm.ContentPart{
			{Type: "tool_result", ToolUseID: "tu-1", Content: `{"parameters":{"type":"object"}}`},
		}},
		{Role: "assistant", Parts: []llm.ContentPart{
			{Type: "tool_use", ID: "tu-2", Name: metaToolExecuteTool, Input: json.RawMessage(`{"toolId":"t-1","input":{"week":"this"}}`)},
			{Type: "tool_use", ID: "tu-3", Name: metaToolTodoWrite, Input: json.RawMessage(`{"todos":[]}`)},
			{Type: "tool_use", ID: "tu-4", Name: metaToolGetSkill, Input: json.RawMessage(`{"name":"周报技能"}`)},
		}},
		{Role: "user", Parts: []llm.ContentPart{
			{Type: "tool_result", ToolUseID: "tu-2", Content: "订单总数 1024"},
			{Type: "tool_result", ToolUseID: "tu-3", Content: "ok"},
			{Type: "tool_result", ToolUseID: "tu-4", Content: "# 周报技能完整说明文档"},
		}},
		{Role: "user", Content: "系统提醒：有 1 个未完结异步任务"},
	}

	breakdown := classifyContextMessages(messages, 1, tools, activeTools)

	if breakdown.SystemPrompt == 0 {
		t.Error("systemPrompt 应计入系统前缀消息")
	}
	if breakdown.Messages == 0 {
		t.Error("messages 应计入用户提问与助手思考")
	}
	if breakdown.McpTools == 0 {
		t.Error("mcpTools 应计入 http 业务工具的 get_tool/execute_tool 轮次")
	}
	if breakdown.SystemTools == 0 {
		t.Error("systemTools 应计入 Meta Tool schema 与 todo_write 轮次")
	}
	if breakdown.Skills == 0 {
		t.Error("skills 应计入 get_skill 加载的技能文档")
	}
	if breakdown.Others == 0 {
		t.Error("others 应计入临时提醒与角色开销")
	}

	// tool_result 依赖 tool_use 先行登记：tu-2(http) 的结果必须归入 mcpTools。
	httpOnly := classifyContextMessages([]llm.ChatMessage{
		{Role: "assistant", Parts: []llm.ContentPart{{Type: "tool_use", ID: "tu-x", Name: metaToolExecuteTool, Input: json.RawMessage(`{"callName":"call_report_query"}`)}}},
		{Role: "user", Parts: []llm.ContentPart{{Type: "tool_result", ToolUseID: "tu-x", Content: "结果内容"}}},
	}, 0, nil, activeTools)
	if httpOnly.McpTools == 0 {
		t.Error("execute_tool 指向 http 业务工具时，其结果应归入 mcpTools")
	}

	// client 业务工具归入系统工具侧。
	clientOnly := classifyContextMessages([]llm.ChatMessage{
		{Role: "assistant", Parts: []llm.ContentPart{{Type: "tool_use", ID: "tu-y", Name: metaToolExecuteTool, Input: json.RawMessage(`{"callName":"call_sql_diff"}`)}}},
	}, 0, nil, activeTools)
	if clientOnly.SystemTools == 0 || clientOnly.McpTools != 0 {
		t.Errorf("client 业务工具应归入 systemTools， got mcp=%d system=%d", clientOnly.McpTools, clientOnly.SystemTools)
	}

	// 未解析到业务工具的调用归入 others，不 panic。
	unresolved := classifyContextMessages([]llm.ChatMessage{
		{Role: "assistant", Parts: []llm.ContentPart{{Type: "tool_use", ID: "tu-z", Name: metaToolExecuteTool, Input: json.RawMessage(`{"toolId":"missing"}`)}}},
		{Role: "user", Parts: []llm.ContentPart{{Type: "tool_result", ToolUseID: "tu-z", Content: "not found"}}},
	}, 0, nil, activeTools)
	if unresolved.Others == 0 {
		t.Error("未解析业务工具的轮次应归入 others")
	}

	// 序列化往返稳定。
 encoded := computeContextBreakdownJSON(messages, 1, tools, activeTools)
	if encoded == "" {
		t.Fatal("computeContextBreakdownJSON 不应返回空串")
	}
	var decoded ContextBreakdown
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("breakdown JSON 反序列化失败: %v", err)
	}
	if decoded != breakdown {
		t.Errorf("序列化往返不一致: %+v != %+v", decoded, breakdown)
	}
}
