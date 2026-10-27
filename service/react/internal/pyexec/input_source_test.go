package pyexec

import (
	"encoding/json"
	"strings"
	"testing"
)

// D-1 回归：stdin 契约是平铺的 alias→value 容器（脚本 json.loads 后直接 inputs[alias]），
// 不允许再出现 multi-transport 信封（曾导致所有带 inputs 的脚本首调 KeyError）。
func TestResolvePythonExecDataFlatAliasMap(t *testing.T) {
	// 空 inputs → 空 JSON 对象（不是信封）。
	out, err := resolvePythonExecData(RunContext{}, pythonExecInput{})
	if err != nil {
		t.Fatalf("empty inputs err: %v", err)
	}
	var empty map[string]interface{}
	if err := json.Unmarshal([]byte(out), &empty); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty flat map, got %s", out)
	}

	// text 源 → 顶层字符串值；raw_json 源 → 顶层 JSON 值。
	out, err = resolvePythonExecData(RunContext{}, pythonExecInput{
		Inputs: map[string]pythonExecInputSource{
			"keypoints": {Type: "text", Value: json.RawMessage(`"智谱 GLM-5.3"`)},
			"orders":    {Type: "raw_json", Value: json.RawMessage(`[{"id":1}]`)},
		},
	})
	if err != nil {
		t.Fatalf("inputs err: %v", err)
	}
	var flat map[string]interface{}
	if err := json.Unmarshal([]byte(out), &flat); err != nil {
		t.Fatalf("unmarshal flat: %v", err)
	}
	if got, ok := flat["keypoints"].(string); !ok || got != "智谱 GLM-5.3" {
		t.Fatalf("keypoints should be top-level string, got %#v", flat["keypoints"])
	}
	orders, ok := flat["orders"].([]interface{})
	if !ok || len(orders) != 1 {
		t.Fatalf("orders should be top-level JSON array, got %#v", flat["orders"])
	}
	if strings.Contains(out, `"transport"`) || strings.Contains(out, `"kind"`) {
		t.Fatalf("flat contract broken, envelope leaked: %s", out)
	}
}

func TestResolvePythonExecDataTotalSizeGuard(t *testing.T) {
	big := strings.Repeat("a", pythonExecInputMaxTotalBytes)
	_, err := resolvePythonExecData(RunContext{}, pythonExecInput{
		Inputs: map[string]pythonExecInputSource{
			"big": {Type: "text", Value: mustJSONString(big)},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("expected total size error, got %v", err)
	}
}

func TestResolvePythonExecDataInputValidation(t *testing.T) {
	cases := []struct {
		name  string
		input pythonExecInput
		want  string
	}{
		{"text value must be json string", pythonExecInput{Inputs: map[string]pythonExecInputSource{
			"a": {Type: "text", Value: json.RawMessage(`123`)},
		}}, "must be a JSON string"},
		{"raw_json value required", pythonExecInput{Inputs: map[string]pythonExecInputSource{
			"a": {Type: "raw_json"},
		}}, "value is required"},
		{"attachment fileId required", pythonExecInput{Inputs: map[string]pythonExecInputSource{
			"a": {Type: "attachment"},
		}}, "fileId is required"},
		{"http url required", pythonExecInput{Inputs: map[string]pythonExecInputSource{
			"a": {Type: "http"},
		}}, "url is required"},
		{"http rejects private host", pythonExecInput{Inputs: map[string]pythonExecInputSource{
			"a": {Type: "http", URL: "http://127.0.0.1:8180/healthz"},
		}}, "拒绝访问内网/环回地址"},
		{"unsupported type", pythonExecInput{Inputs: map[string]pythonExecInputSource{
			"a": {Type: "magic"},
		}}, "unsupported source type"},
		{"empty alias", pythonExecInput{Inputs: map[string]pythonExecInputSource{
			" ": {Type: "text", Value: json.RawMessage(`"x"`)},
		}}, "alias is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolvePythonExecData(RunContext{}, tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want err containing %q, got %v", tc.want, err)
			}
		})
	}
}

func mustJSONString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}
