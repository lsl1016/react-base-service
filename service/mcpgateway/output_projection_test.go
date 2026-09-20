package mcpgateway

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"react-base-service/service/mcpgateway/toolconfig"
)

func TestProjectLargeHTTPJSONResponse(t *testing.T) {
	items := make([]any, 20)
	for index := range items {
		items[index] = map[string]any{
			"id":                   230743819 + index,
			"task_id":              136023,
			"process_id":           0,
			"run_user":             "tester",
			"script_type":          5,
			"belong_type":          477,
			"begin_time":           1786899606,
			"execute_time":         1786901106,
			"finish_time":          1786901396,
			"level":                3,
			"warning_count":        0,
			"status":               5,
			"execute_machine":      0,
			"retry_count":          0,
			"session_id":           "",
			"result_ftp":           "",
			"log_ftp":              "",
			"log_path":             "",
			"run_index":            1786809600,
			"queue_name":           "",
			"advance":              0,
			"task_name":            "load_ods_example_tbl_routine",
			"task_create_user":     "tester",
			"execute_machine_name": "",
			"script":               strings.Repeat("INSERT OVERWRITE TABLE target SELECT * FROM source;\n", 20),
			"routine":              86400,
			"task_version_id":      4700078,
			"auth_tag":             "demo",
			"auth_tag_two":         "demo-core",
			"after_tran_sql":       "",
			"alias":                "load_ods_example_tbl_routine",
			"result_file_type":     []any{"FT_COMPATIBLE"},
		}
	}
	rawResponse, err := json.Marshal(map[string]any{
		"errNo":  0,
		"errStr": "",
		"data": map[string]any{
			"code":                      0,
			"msg":                       "成功",
			"version":                   1,
			"task_instance_total_count": 366,
			"task_instance_count":       len(items),
			"task_instance_list":        items,
		},
	})
	if err != nil {
		t.Fatalf("构造上游响应失败: %v", err)
	}
	if len(rawResponse) < 20000 {
		t.Fatalf("测试响应不足以覆盖大 JSON 场景: %d bytes", len(rawResponse))
	}

	decoded, err := decodeUpstreamJSON(rawResponse)
	if err != nil {
		t.Fatalf("解析上游大 JSON 失败: %v", err)
	}
	upstream := normalizeDecodedResponse(decoded)
	schema := `{
      "type":"object",
      "description":"任务实例查询结果",
      "x-output-projection":true,
      "properties":{
        "errNo":{"type":"integer"},
        "data":{
          "type":"object",
          "properties":{
            "task_instance_total_count":{"type":"integer","description":"符合条件的任务实例总数"},
            "task_instance_count":{"type":"integer","description":"本次返回数量"},
            "task_instance_list":{
              "type":"array",
              "description":"任务实例列表",
              "items":{
                "type":"object",
                "properties":{
                  "id":{"type":"integer","description":"任务实例 ID"},
                  "task_id":{"type":"integer","description":"任务 ID"},
                  "run_user":{"type":"string","description":"执行用户"},
                  "begin_time":{"type":"integer","description":"开始时间"},
                  "finish_time":{"type":"integer","description":"结束时间"},
                  "status":{"type":"integer","description":"任务状态"},
                  "task_name":{"type":"string","description":"任务名称"},
                  "auth_tag":{"type":"string","description":"一级权限标签"},
                  "auth_tag_two":{"type":"string","description":"二级权限标签"}
                },
                "required":["id","task_id","status","task_name"]
              }
            }
          },
          "required":["task_instance_total_count","task_instance_count","task_instance_list"]
        }
      },
      "required":["errNo","data"]
    }`

	projection, enabled, err := projectStructuredOutput(schema, upstream.Raw)
	if err != nil || !enabled {
		t.Fatalf("投影上游大 JSON 失败: enabled=%v err=%v", enabled, err)
	}
	if err := validateStructuredOutput(schema, projection.Value); err != nil {
		t.Fatalf("投影结果未通过 outputSchema 校验: %v", err)
	}

	projected := projection.Value.(map[string]any)
	projectedData := projected["data"].(map[string]any)
	projectedItems := projectedData["task_instance_list"].([]any)
	if len(projectedItems) != 20 {
		t.Fatalf("任务实例数量不符合预期: %d", len(projectedItems))
	}
	first := projectedItems[0].(map[string]any)
	for _, retained := range []string{"id", "task_id", "run_user", "begin_time", "finish_time", "status", "task_name", "auth_tag", "auth_tag_two"} {
		if _, exists := first[retained]; !exists {
			t.Fatalf("投影结果缺少字段 %s: %#v", retained, first)
		}
	}
	for _, removed := range []string{"script", "result_ftp", "log_ftp", "log_path", "execute_machine", "task_version_id"} {
		if _, exists := first[removed]; exists {
			t.Fatalf("投影结果仍包含无用字段 %s: %#v", removed, first)
		}
	}
	if _, exists := projected["errStr"]; exists {
		t.Fatal("outputSchema 未定义的顶层 errStr 应被删除")
	}
	if _, exists := projectedData["version"]; exists {
		t.Fatal("outputSchema 未定义的 data.version 应被删除")
	}

	projectedPayload, err := json.Marshal(projection.Value)
	if err != nil {
		t.Fatalf("序列化大 JSON 投影结果失败: %v", err)
	}
	text := renderProjectedOutput(projection, projectedPayload)
	t.Logf("投影后的 Content 文本：\n%s", text)
	if strings.Contains(text, "INSERT OVERWRITE") || strings.Contains(text, `"script"`) {
		t.Fatal("旧客户端文本结果不应包含已裁剪的脚本内容")
	}
	for _, description := range []string{"任务实例查询结果", "任务实例 ID", "任务状态", "任务名称"} {
		if !strings.Contains(text, description) {
			t.Fatalf("旧客户端文本结果缺少字段说明 %q", description)
		}
	}
}

func TestProjectStructuredOutputCommonStructures(t *testing.T) {
	schema := `{
      "type":"object",
      "description":"查询结果",
      "x-output-projection":true,
      "properties":{
        "data":{
          "type":"object",
          "description":"业务数据",
          "properties":{
            "id":{"type":"string","description":"记录标识"},
            "profile":{"type":"object","properties":{"name":{"type":"string"}}},
            "documents":{"type":"array","description":"文档列表","items":{"type":"object","properties":{"title":{"type":"string","description":"文档标题"}}}},
            "scores":{"type":"array","items":{"type":"number"}},
            "matrix":{"type":"array","items":{"type":"array","items":{"type":"integer"}}}
          }
        }
      }
    }`
	input := map[string]any{
		"traceId": "internal",
		"data": map[string]any{
			"id":      "1",
			"profile": map[string]any{"name": "张三", "secret": "hidden"},
			"documents": []any{
				map[string]any{"title": "A", "internal": "hidden"},
				map[string]any{"title": "B", "internal": "hidden"},
			},
			"scores": []any{1.5, 2.5},
			"matrix": []any{[]any{float64(1), float64(2)}},
			"unused": true,
		},
	}
	original := cloneJSONValue(input)

	projection, enabled, err := projectStructuredOutput(schema, input)
	if err != nil || !enabled {
		t.Fatalf("投影失败: enabled=%v err=%v", enabled, err)
	}
	want := map[string]any{
		"data": map[string]any{
			"id":      "1",
			"profile": map[string]any{"name": "张三"},
			"documents": []any{
				map[string]any{"title": "A"},
				map[string]any{"title": "B"},
			},
			"scores": []any{1.5, 2.5},
			"matrix": []any{[]any{float64(1), float64(2)}},
		},
	}
	if !reflect.DeepEqual(projection.Value, want) {
		t.Fatalf("投影结果不符合预期:\n got=%#v\nwant=%#v", projection.Value, want)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatal("投影不应修改原始上游结果")
	}

	payload, err := json.Marshal(projection.Value)
	if err != nil {
		t.Fatalf("序列化投影结果失败: %v", err)
	}
	text := renderProjectedOutput(projection, payload)
	for _, expected := range []string{
		"结果说明：查询结果",
		"- data：业务数据",
		"- data.id：记录标识",
		"- data.documents：文档列表",
		"- data.documents[].title：文档标题",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("投影文本缺少 %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "internal") || strings.Contains(text, "unused") || strings.Contains(text, "secret") {
		t.Fatalf("投影文本泄露未声明字段: %s", text)
	}
}

func TestProjectStructuredOutputDynamicObjects(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		input  map[string]any
		want   map[string]any
	}{
		{
			name:   "无 properties 的对象完整保留",
			schema: `{"type":"object","x-output-projection":true,"properties":{"stats":{"type":"object"}}}`,
			input:  map[string]any{"stats": map[string]any{"total": float64(3), "cost": float64(10)}},
			want:   map[string]any{"stats": map[string]any{"total": float64(3), "cost": float64(10)}},
		},
		{
			name:   "显式禁止额外字段得到空对象",
			schema: `{"type":"object","x-output-projection":true,"properties":{"stats":{"type":"object","properties":{},"additionalProperties":false}}}`,
			input:  map[string]any{"stats": map[string]any{"total": float64(3)}},
			want:   map[string]any{"stats": map[string]any{}},
		},
		{
			name:   "additionalProperties Schema 投影动态键",
			schema: `{"type":"object","x-output-projection":true,"properties":{"teams":{"type":"object","additionalProperties":{"type":"object","properties":{"name":{"type":"string"}}}}}}`,
			input:  map[string]any{"teams": map[string]any{"a": map[string]any{"name": "A", "secret": "x"}}},
			want:   map[string]any{"teams": map[string]any{"a": map[string]any{"name": "A"}}},
		},
		{
			name:   "patternProperties 保留匹配动态键",
			schema: `{"type":"object","x-output-projection":true,"properties":{"metrics":{"type":"object","patternProperties":{"^x_":{"type":"number"}}}}}`,
			input:  map[string]any{"metrics": map[string]any{"x_cost": float64(1), "internal": float64(2)}},
			want:   map[string]any{"metrics": map[string]any{"x_cost": float64(1)}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection, enabled, err := projectStructuredOutput(test.schema, test.input)
			if err != nil || !enabled {
				t.Fatalf("投影失败: enabled=%v err=%v", enabled, err)
			}
			if !reflect.DeepEqual(projection.Value, test.want) {
				t.Fatalf("got=%#v want=%#v", projection.Value, test.want)
			}
		})
	}
}

func TestProjectStructuredOutputRefsAndBranches(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		input  map[string]any
		want   map[string]any
	}{
		{
			name: "本地 ref",
			schema: `{
              "type":"object","x-output-projection":true,
              "$defs":{"item":{"type":"object","properties":{"id":{"type":"string"}}}},
              "properties":{"data":{"$ref":"#/$defs/item"}}
            }`,
			input: map[string]any{"data": map[string]any{"id": "1", "hidden": true}},
			want:  map[string]any{"data": map[string]any{"id": "1"}},
		},
		{
			name: "allOf 合并字段",
			schema: `{
              "type":"object","x-output-projection":true,
              "allOf":[
                {"properties":{"id":{"type":"string"}}},
                {"properties":{"title":{"type":"string"}}}
              ]
            }`,
			input: map[string]any{"id": "1", "title": "T", "hidden": true},
			want:  map[string]any{"id": "1", "title": "T"},
		},
		{
			name: "oneOf 选择唯一分支",
			schema: `{
              "type":"object","x-output-projection":true,
              "oneOf":[
                {"properties":{"kind":{"const":"a"},"a":{"type":"string"}},"required":["kind","a"]},
                {"properties":{"kind":{"const":"b"},"b":{"type":"string"}},"required":["kind","b"]}
              ]
            }`,
			input: map[string]any{"kind": "b", "b": "value", "hidden": true},
			want:  map[string]any{"kind": "b", "b": "value"},
		},
		{
			name: "条件分支",
			schema: `{
              "type":"object","x-output-projection":true,
              "properties":{"kind":{"type":"string"}},
              "if":{"properties":{"kind":{"const":"detail"}},"required":["kind"]},
              "then":{"properties":{"detail":{"type":"string"}}},
              "else":{"properties":{"summary":{"type":"string"}}}
            }`,
			input: map[string]any{"kind": "detail", "detail": "D", "summary": "S"},
			want:  map[string]any{"kind": "detail", "detail": "D"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection, enabled, err := projectStructuredOutput(test.schema, test.input)
			if err != nil || !enabled {
				t.Fatalf("投影失败: enabled=%v err=%v", enabled, err)
			}
			if !reflect.DeepEqual(projection.Value, test.want) {
				t.Fatalf("got=%#v want=%#v", projection.Value, test.want)
			}
		})
	}
}

func TestProjectStructuredOutputCompatibilityAndErrors(t *testing.T) {
	input := map[string]any{"id": "1", "hidden": true, "nullable": nil}
	for _, schema := range []string{"", `{"type":"object"}`, `{"type":"object","x-output-projection":false}`} {
		projection, enabled, err := projectStructuredOutput(schema, input)
		if err != nil || enabled || projection != nil {
			t.Fatalf("未开启投影时应保持兼容: projection=%#v enabled=%v err=%v", projection, enabled, err)
		}
	}

	ambiguous := `{
      "type":"object","x-output-projection":true,
      "oneOf":[{"properties":{"id":{"type":"string"}}},{"properties":{"id":{"type":"string"}}}]
    }`
	if _, _, err := projectStructuredOutput(ambiguous, input); err == nil {
		t.Fatal("oneOf 无法唯一匹配时应返回错误")
	}

	cyclic := `{
      "type":"object","x-output-projection":true,
      "$defs":{"node":{"$ref":"#/$defs/node"}},
      "properties":{"data":{"$ref":"#/$defs/node"}}
    }`
	if err := toolconfig.ValidateOutputSchema(cyclic, true); err == nil {
		t.Fatal("循环引用应在保存配置时被拒绝")
	}

	external := `{"type":"object","x-output-projection":true,"properties":{"data":{"$ref":"https://example.com/schema.json"}}}`
	if err := toolconfig.ValidateOutputSchema(external, true); err == nil {
		t.Fatal("外部引用应在保存配置时被拒绝")
	}

	invalidPattern := `{"type":"object","x-output-projection":true,"patternProperties":{"[":{"type":"string"}}}`
	if err := toolconfig.ValidateOutputSchema(invalidPattern, true); err == nil {
		t.Fatal("非法 patternProperties 应被拒绝")
	}
}

func TestProjectedOutputRemainsValidJSON(t *testing.T) {
	schema := `{"type":"object","x-output-projection":true,"properties":{"nullable":{"type":["string","null"]}}}`
	projection, _, err := projectStructuredOutput(
		schema,
		map[string]any{"nullable": nil},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStructuredOutput(schema, projection.Value); err != nil {
		t.Fatalf("null 投影未通过 outputSchema 校验: %v", err)
	}
	data, err := json.Marshal(projection.Value)
	if err != nil || string(data) != `{"nullable":null}` {
		t.Fatalf("null 投影不符合预期: %s err=%v", data, err)
	}
}
