package pyexec

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	core "react-base-service/service/react/internal/core"
)

const (
	inspectDataSourceToolResult = "tool_result"
	inspectDataFormatJSON       = "json"
	defaultInspectSampleLimit   = 5
	maxInspectSampleLimit       = 20
)

type inspectDataInput struct {
	Source      inspectDataSource `json:"source"`
	FormatHint  string            `json:"formatHint"`
	SampleLimit int               `json:"sampleLimit"`
}

type inspectDataSource struct {
	Type string `json:"type"`
	Ref  string `json:"ref"`
}

type inspectDataOutput struct {
	Source      inspectDataSource      `json:"source"`
	Format      string                 `json:"format"`
	RootType    string                 `json:"rootType"`
	SizeBytes   int                    `json:"sizeBytes"`
	Schema      []jsonPathSummary      `json:"schema"`
	Samples     map[string]interface{} `json:"samples,omitempty"`
	PythonUsage map[string]string      `json:"pythonUsage"`
}

type jsonPathSummary struct {
	Path       string        `json:"path"`
	Types      []string      `json:"types"`
	Count      int           `json:"count"`
	Presence   float64       `json:"presence,omitempty"`
	Examples   []interface{} `json:"examples,omitempty"`
	ChildCount int           `json:"childCount,omitempty"`
}

type jsonPathAccumulator struct {
	Path       string
	Types      map[string]struct{}
	Count      int
	Present    int
	Total      int
	Examples   []interface{}
	ChildCount int
}

func InspectData(rc RunContext, input json.RawMessage) (string, bool, error) {
	var req inspectDataInput
	if err := json.Unmarshal(input, &req); err != nil {
		return "", true, fmt.Errorf("inspect_data input must be valid JSON: %w", err)
	}
	formatHint := strings.ToLower(strings.TrimSpace(req.FormatHint))
	if formatHint != "" && formatHint != inspectDataFormatJSON {
		return "", true, fmt.Errorf("inspect_data only supports json in current version")
	}
	content, source, err := resolveInspectDataSource(rc, req.Source)
	if err != nil {
		return "", true, err
	}

	var value interface{}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", true, fmt.Errorf("inspect_data source is not valid JSON: %w", err)
	}

	sampleLimit := normalizeInspectSampleLimit(req.SampleLimit)
	acc := make(map[string]*jsonPathAccumulator)
	samples := make(map[string]interface{})
	inspectJSONValue(value, "$", acc, samples, sampleLimit)

	output := inspectDataOutput{
		Source:    source,
		Format:    inspectDataFormatJSON,
		RootType:  inspectJSONType(value),
		SizeBytes: len([]byte(content)),
		Schema:    buildJSONPathSummaries(acc),
		Samples:   samples,
		PythonUsage: map[string]string{
			"inspect": "如果 python_exec 依赖多个工具结果，应先对每个结构不明确的 resultRef 分别调用 inspect_data。inspect_data 只用于理解结构，不生成 python_exec 的数据输入。",
			"input":   "调用 python_exec 时，把原始工具 resultRef 放入 inputs 的某个命名输入，例如 inputs.orders={type:'tool_result', ref:'result_ref_orders'}。后端会读取 ref 对应的工具结果，并把解析后的 JSON value 放到该 alias 下；多个工具结果使用多个 alias。",
			"load":    "Python 中使用 import json, sys; inputs = json.loads(sys.stdin.read()) 读取命名输入对象。stdin JSON 的顶层 key 来自 python_exec.inputs 的 alias，建议按业务含义取值，例如 orders = inputs['orders']。inputs['alias'] 已经是对应工具结果的 JSON value，可直接处理，不是 resultRef 字符串。不要假设存在 data/raw/payload 等预置变量。",
			"output":  "建议用 print(json.dumps(result, ensure_ascii=False)) 输出结构化结果。",
		},
	}
	data, _ := json.Marshal(output)
	return string(data), false, nil
}

func resolveInspectDataSource(rc RunContext, source inspectDataSource) (string, inspectDataSource, error) {
	source.Type = strings.ToLower(strings.TrimSpace(source.Type))
	source.Ref = strings.TrimSpace(source.Ref)
	if source.Type != inspectDataSourceToolResult {
		return "", source, fmt.Errorf("unsupported inspect_data source type: %s", source.Type)
	}
	if source.Ref == "" {
		return "", source, fmt.Errorf("inspect_data source.ref is required")
	}
	content, ok, err := core.ReadResultRef(rc.GinCtx, rc.SessionID, rc.RunID, source.Ref)
	if err != nil {
		return "", source, err
	}
	if !ok {
		return "", source, fmt.Errorf("resultRef not found or expired")
	}
	return content, source, nil
}

func normalizeInspectSampleLimit(limit int) int {
	if limit <= 0 {
		return defaultInspectSampleLimit
	}
	if limit > maxInspectSampleLimit {
		return maxInspectSampleLimit
	}
	return limit
}

func inspectJSONValue(value interface{}, path string, acc map[string]*jsonPathAccumulator, samples map[string]interface{}, sampleLimit int) {
	entry := ensureJSONPathAccumulator(acc, path)
	entry.Count++
	entry.Types[inspectJSONType(value)] = struct{}{}
	addJSONExample(entry, value)

	switch typed := value.(type) {
	case map[string]interface{}:
		entry.ChildCount = maxInt(entry.ChildCount, len(typed))
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := path + "." + key
			child := ensureJSONPathAccumulator(acc, childPath)
			child.Total++
			child.Present++
			inspectJSONValue(typed[key], childPath, acc, samples, sampleLimit)
		}
	case []interface{}:
		arrayPath := path + "[]"
		entry.ChildCount = maxInt(entry.ChildCount, len(typed))
		if len(typed) > 0 && samples[arrayPath] == nil {
			samples[arrayPath] = sampleJSONArray(typed, sampleLimit)
		}
		arrayEntry := ensureJSONPathAccumulator(acc, arrayPath)
		arrayEntry.Total += len(typed)
		arrayEntry.Present += len(typed)
		for _, item := range typed {
			inspectJSONValue(item, arrayPath, acc, samples, sampleLimit)
		}
	}
}

func ensureJSONPathAccumulator(acc map[string]*jsonPathAccumulator, path string) *jsonPathAccumulator {
	if item, ok := acc[path]; ok {
		return item
	}
	item := &jsonPathAccumulator{Path: path, Types: make(map[string]struct{})}
	acc[path] = item
	return item
}

func inspectJSONType(value interface{}) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	case string:
		return "string"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	default:
		return "unknown"
	}
}

func addJSONExample(entry *jsonPathAccumulator, value interface{}) {
	if len(entry.Examples) >= 3 || !isJSONScalar(value) {
		return
	}
	for _, existing := range entry.Examples {
		if fmt.Sprintf("%v", existing) == fmt.Sprintf("%v", value) {
			return
		}
	}
	entry.Examples = append(entry.Examples, value)
}

func isJSONScalar(value interface{}) bool {
	switch value.(type) {
	case nil, bool, json.Number, float64, string:
		return true
	default:
		return false
	}
}

func sampleJSONArray(items []interface{}, limit int) []interface{} {
	if len(items) < limit {
		limit = len(items)
	}
	out := make([]interface{}, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, items[i])
	}
	return out
}

func buildJSONPathSummaries(acc map[string]*jsonPathAccumulator) []jsonPathSummary {
	paths := make([]string, 0, len(acc))
	for path := range acc {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]jsonPathSummary, 0, len(paths))
	for _, path := range paths {
		item := acc[path]
		types := make([]string, 0, len(item.Types))
		for typ := range item.Types {
			types = append(types, typ)
		}
		sort.Strings(types)
		summary := jsonPathSummary{
			Path:       path,
			Types:      types,
			Count:      item.Count,
			Examples:   item.Examples,
			ChildCount: item.ChildCount,
		}
		if item.Total > 0 {
			summary.Presence = math.Round(float64(item.Present)/float64(item.Total)*1000) / 1000
		}
		out = append(out, summary)
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
