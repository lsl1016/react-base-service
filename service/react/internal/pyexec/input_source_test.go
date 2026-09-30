package pyexec

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResolvePythonExecDataMultiTransport(t *testing.T) {

	// 空 inputs → multi 信封 + 空 inputs 对象。
	out, err := resolvePythonExecData(RunContext{}, pythonExecInput{})
	if err != nil {
		t.Fatalf("empty inputs err: %v", err)
	}
	var empty map[string]interface{}
	if err := json.Unmarshal([]byte(out), &empty); err != nil {
		t.Fatalf("unmarshal empty envelope: %v", err)
	}
	if empty["transport"] != "multi" {
		t.Fatalf("expected transport multi, got %v", empty["transport"])
	}
	if inputs, ok := empty["inputs"].(map[string]interface{}); !ok || len(inputs) != 0 {
		t.Fatalf("expected empty inputs object, got %v", empty["inputs"])
	}

	// raw_json 源 → inline 描述符。
	out, err = resolvePythonExecData(RunContext{}, pythonExecInput{
		Inputs: map[string]pythonExecInputSource{
			"orders": {Type: "raw_json", Value: json.RawMessage(`[{"id":1}]`)},
		},
	})
	if err != nil {
		t.Fatalf("raw_json inputs err: %v", err)
	}
	var env struct {
		Transport string `json:"transport"`
		Inputs    map[string]struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Transport != "multi" {
		t.Fatalf("expected transport multi, got %q", env.Transport)
	}
	orders, ok := env.Inputs["orders"]
	if !ok {
		t.Fatalf("missing orders input: %s", out)
	}
	if orders.Kind != pythonExecKindInline {
		t.Fatalf("expected inline kind, got %q", orders.Kind)
	}
	if !strings.Contains(string(orders.Payload), `"id":1`) {
		t.Fatalf("unexpected payload: %s", orders.Payload)
	}
}
