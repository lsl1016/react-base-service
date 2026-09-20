package mcpgateway

import (
	"encoding/json"
	"testing"

	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

const testOutputSchema = `{
  "type":"object",
  "properties":{
    "errNo":{"type":"integer"},
    "data":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}
  },
  "required":["errNo","data"]
}`

func TestBuildToolDefinitionIncludesOutputSchema(t *testing.T) {
	binding := ToolBinding{
		Name:         "tool",
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: testOutputSchema,
	}
	definition := buildToolDefinition(&gin.Context{}, binding)
	if definition == nil {
		t.Fatal("合法工具定义被跳过")
	}
	raw, ok := definition.OutputSchema.(json.RawMessage)
	if !ok || string(raw) != testOutputSchema {
		t.Fatalf("outputSchema 不符合预期: %#v", definition.OutputSchema)
	}
}

func TestBuildToolDefinitionSkipsInvalidSchemas(t *testing.T) {
	binding := ToolBinding{
		Name:         "bad_output",
		InputSchema:  `{"type":"object"}`,
		OutputSchema: `{"type":"object","x-output-projection":true,"patternProperties":{"[":{"type":"string"}}}`,
	}
	if buildToolDefinition(&gin.Context{}, binding) != nil {
		t.Fatal("非法投影语法应跳过工具")
	}
	binding.OutputSchema = ""
	binding.InputSchema = `{"type":"array"}`
	if buildToolDefinition(&gin.Context{}, binding) != nil {
		t.Fatal("顶层非 object 的 inputSchema 应跳过工具")
	}
}

func TestBuildToolDefinitionAnnotatesWriteOps(t *testing.T) {
	readOnly := buildToolDefinition(&gin.Context{}, ToolBinding{
		Name: "get_thing", InputSchema: `{"type":"object"}`, ReadOnly: true,
	})
	if readOnly == nil || !readOnly.Annotations.ReadOnlyHint || readOnly.Annotations.DestructiveHint != nil {
		t.Fatalf("只读工具标注不符合预期: %#v", readOnly.Annotations)
	}
	write := buildToolDefinition(&gin.Context{}, ToolBinding{
		Name: "create_thing", InputSchema: `{"type":"object"}`, ReadOnly: false,
	})
	if write == nil || write.Annotations.ReadOnlyHint || write.Annotations.DestructiveHint == nil || !*write.Annotations.DestructiveHint {
		t.Fatalf("写操作工具标注不符合预期: %#v", write.Annotations)
	}
}

func TestUpstreamResultReturnsStructuredContent(t *testing.T) {
	raw := map[string]interface{}{
		"errNo": float64(0),
		"data":  map[string]interface{}{"id": "record-1"},
	}
	result := upstreamResult("tool", testOutputSchema, &UpstreamResult{Success: true, Raw: raw})
	if result.IsError || result.StructuredContent == nil {
		t.Fatalf("结构化结果不符合预期: %#v", result)
	}
	structured, ok := result.StructuredContent.(map[string]interface{})
	if !ok || structured["data"] == nil {
		t.Fatalf("structuredContent 不符合预期: %#v", result.StructuredContent)
	}
}

func TestUpstreamResultRejectsSchemaMismatch(t *testing.T) {
	result := upstreamResult("tool", testOutputSchema, &UpstreamResult{
		Success: true,
		Raw: map[string]interface{}{
			"errNo": float64(0),
			"data":  map[string]interface{}{},
		},
	})
	if !result.IsError || result.StructuredContent != nil {
		t.Fatalf("结果与 Schema 不匹配时应返回工具错误: %#v", result)
	}
}

func TestBuildToolBindingFromToolRow(t *testing.T) {
	record := model.Tool{
		Name:        "demo_query",
		Description: "演示工具",
		Config: `{"url":"https://api.example.com/q","method":"get","timeout_ms":5000,` +
			`"headers":{"X-Token":"t"},"inputSchema":{"type":"object","properties":{"id":{"type":"string"}}},` +
			`"outputSchema":{"type":"object","x-output-projection":true}}`,
	}
	binding, err := buildToolBinding(record)
	if err != nil {
		t.Fatalf("展开工具绑定失败: %v", err)
	}
	if binding.URL != "https://api.example.com/q" || binding.Request.Method != "GET" || !binding.ReadOnly {
		t.Fatalf("请求配置不符合预期: %#v", binding.Request)
	}
	if binding.Request.Timeout() != 5000*1e6 || binding.Request.Headers["X-Token"] != "t" {
		t.Fatalf("请求头/超时不符合预期: %#v", binding.Request)
	}
	if binding.InputSchema == "" || binding.OutputSchema == "" {
		t.Fatalf("schema 原文缺失: %q / %q", binding.InputSchema, binding.OutputSchema)
	}

	// 缺省 inputSchema 的存量工具应兜底为任意对象；URL 非法则拒绝。
	legacy, err := buildToolBinding(model.Tool{Name: "legacy", Config: `{"url":"https://api.example.com/x"}`})
	if err != nil || legacy.InputSchema != defaultInputSchema {
		t.Fatalf("缺省 inputSchema 应兜底: binding=%#v err=%v", legacy, err)
	}
	if _, err := buildToolBinding(model.Tool{Name: "bad", Config: `{"url":"ftp://x"}`}); err == nil {
		t.Fatal("非法 URL 应拒绝构建绑定")
	}
}
