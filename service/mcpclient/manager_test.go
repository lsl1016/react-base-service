package mcpclient

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolConfigJSON 验证 MCP 工具落库 config 的形状：入参 schema 缺省补空对象，
// 出参 schema 仅在服务器声明时写入（未声明不写 outputSchema 字段，避免覆盖语义）。
func TestToolConfigJSON(t *testing.T) {
	configJSON, err := ToolConfigJSON("demo", "echo",
		map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
	)
	if err != nil {
		t.Fatalf("ToolConfigJSON: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		t.Fatalf("config 非法 JSON: %v", err)
	}
	if cfg["mcpServer"] != "demo" || cfg["mcpTool"] != "echo" {
		t.Fatalf("路由字段缺失: %s", configJSON)
	}
	input, ok := cfg["inputSchema"].(map[string]any)
	if !ok || input["type"] != "object" {
		t.Fatalf("inputSchema 未透传: %s", configJSON)
	}
	output, ok := cfg["outputSchema"].(map[string]any)
	if !ok || output["type"] != "object" {
		t.Fatalf("outputSchema 未透传: %s", configJSON)
	}

	minimalJSON, err := ToolConfigJSON("demo", "boom", nil, nil)
	if err != nil {
		t.Fatalf("ToolConfigJSON(minimal): %v", err)
	}
	if strings.Contains(minimalJSON, "outputSchema") {
		t.Fatalf("未声明 outputSchema 时不应写入该字段: %s", minimalJSON)
	}
	var minimal map[string]any
	if err := json.Unmarshal([]byte(minimalJSON), &minimal); err != nil {
		t.Fatalf("config 非法 JSON: %v", err)
	}
	input, ok = minimal["inputSchema"].(map[string]any)
	if !ok || input["type"] != "object" {
		t.Fatalf("inputSchema 缺省应补空对象: %s", minimalJSON)
	}
}
