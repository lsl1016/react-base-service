package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSONSchema(t *testing.T, raw string) JSONSchema {
	t.Helper()
	var schema JSONSchema
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatalf("解析测试 schema 失败: %v", err)
	}
	return schema
}

// oneOf/anyOf 组合分支：MCP 工具多形态字段（如 filterValues[].value 字符串或数组）。
// 复现线上死循环场景：Bw_new_space_sql_chart_data 的 filterValues[].value 为
// oneOf [string, array]，旧校验器因无顶层 type 一律拒绝。
func TestValidateJSONSchemaOneOfAnyOf(t *testing.T) {
	filterValuesSchema := `{
		"type": "array",
		"items": {
			"type": "object",
			"required": ["conditionHash", "value"],
			"properties": {
				"conditionHash": {"type": "string"},
				"value": {
					"oneOf": [
						{"type": "string"},
						{"type": "array", "items": {"type": "string"}}
					]
				}
			}
		}
	}`

	cases := []struct {
		name      string
		schema    string
		valueJSON string
		wantErr   string
	}{
		{"oneOf 字符串分支通过", filterValuesSchema, `[{"conditionHash":"con_1","value":"45"}]`, ""},
		{"oneOf 数组分支通过", filterValuesSchema, `[{"conditionHash":"con_1","value":["零期","一期"]}]`, ""},
		{"oneOf 多元素混合通过", filterValuesSchema, `[{"conditionHash":"con_1","value":"2026暑"},{"conditionHash":"con_2","value":["一年级"]}]`, ""},
		{"oneOf 无分支匹配", filterValuesSchema, `[{"conditionHash":"con_1","value":45}]`, "must match one of oneOf branches"},
		{"anyOf 任一分支通过", `{"anyOf":[{"type":"string"},{"type":"number"}]}`, `3.14`, ""},
		{"anyOf 字符串分支通过", `{"anyOf":[{"type":"string"},{"type":"number"}]}`, `"abc"`, ""},
		{"anyOf 无分支匹配", `{"anyOf":[{"type":"string"},{"type":"number"}]}`, `true`, "must match one of anyOf branches"},
		{"分支错误拼接供模型纠正", `{"oneOf":[{"type":"string"},{"type":"number"}]}`, `true`, "must be string; $ must be number"},
		{"oneOf 嵌套在 items 中", `{"type":"array","items":{"oneOf":[{"type":"string"},{"type":"boolean"}]}}`, `["a",true]`, ""},
		{"oneOf 分支带完整 object 结构", `{"oneOf":[{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}},{"type":"null"}]}`, `{"name":"x"}`, ""},
		{"oneOf 非法分支结构", `{"oneOf":"bad"}`, `"x"`, "schema.oneOf must be non-empty array"},
		{"oneOf 空分支数组", `{"oneOf":[]}`, `"x"`, "schema.oneOf must be non-empty array"},
		{"回归：无 type 且无组合分支仍报错", `{"minLength":1}`, `"x"`, "schema.type is required"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.valueJSON), &value); err != nil {
				t.Fatalf("解析测试值失败: %v", err)
			}
			err := ValidateJSONSchemaValue(mustJSONSchema(t, tc.schema), value)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，实际报错: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望报错 %q，实际通过", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息 %q 未包含期望片段 %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// 回归：原有 type/required/properties/items/enum 语义不受影响。
func TestValidateJSONSchemaPlainTypes(t *testing.T) {
	cases := []struct {
		name      string
		schema    string
		valueJSON string
		wantPass  bool
	}{
		{"object 基本校验", `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`, `{"a":"x"}`, true},
		{"缺 required 字段", `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`, `{}`, false},
		{"enum 命中", `{"type":"string","enum":["x","y"]}`, `"x"`, true},
		{"enum 未命中", `{"type":"string","enum":["x","y"]}`, `"z"`, false},
		{"integer 拒绝小数", `{"type":"integer"}`, `1.5`, false},
		{"空 schema 报错", `{}`, `"x"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.valueJSON), &value); err != nil {
				t.Fatalf("解析测试值失败: %v", err)
			}
			err := ValidateJSONSchemaValue(mustJSONSchema(t, tc.schema), value)
			if tc.wantPass && err != nil {
				t.Fatalf("期望通过，实际报错: %v", err)
			}
			if !tc.wantPass && err == nil {
				t.Fatalf("期望报错，实际通过")
			}
		})
	}
}
