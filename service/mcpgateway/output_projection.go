package mcpgateway

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// 本文件是 mcp-server 项目的输出投影器，原样移植：
// outputSchema 声明 "x-output-projection": true 时，按 schema 裁剪上游响应
// （未声明的字段丢弃），并把 schema 里的根/字段 description 渲染进返回文本。

const maxOutputProjectionDepth = 64
const maxOutputDescriptions = 200

type outputFieldDescription struct {
	Path        string
	Description string
}

type outputProjection struct {
	Value           any
	RootDescription string
	Fields          []outputFieldDescription
}

type outputProjectionState struct {
	root         map[string]any
	descriptions []outputFieldDescription
	described    map[string]bool
}

func projectStructuredOutput(rawSchema string, value any) (*outputProjection, bool, error) {
	if strings.TrimSpace(rawSchema) == "" {
		return nil, false, nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(rawSchema), &root); err != nil {
		return nil, false, fmt.Errorf("解析 outputSchema 失败: %w", err)
	}
	enabled, ok := root["x-output-projection"].(bool)
	if !ok || !enabled {
		return nil, false, nil
	}

	state := &outputProjectionState{root: root, described: map[string]bool{}}
	projected, err := state.project(root, value, "", 0, map[string]bool{})
	if err != nil {
		return nil, true, err
	}
	rootDescription, _ := root["description"].(string)
	return &outputProjection{
		Value:           projected,
		RootDescription: strings.TrimSpace(rootDescription),
		Fields:          state.descriptions,
	}, true, nil
}

func (s *outputProjectionState) project(node map[string]any, value any, path string, depth int, refs map[string]bool) (any, error) {
	if depth > maxOutputProjectionDepth {
		return nil, fmt.Errorf("输出投影超过 %d 层: %s", maxOutputProjectionDepth, displayOutputPath(path))
	}
	effective, err := s.effectiveNode(node, value, depth, refs)
	if err != nil {
		return nil, err
	}
	s.addDescription(path, effective)

	switch typed := value.(type) {
	case map[string]any:
		return s.projectObject(effective, typed, path, depth, refs)
	case []any:
		return s.projectArray(effective, typed, path, depth, refs)
	default:
		return cloneJSONValue(value), nil
	}
}

func (s *outputProjectionState) projectObject(node map[string]any, value map[string]any, path string, depth int, refs map[string]bool) (map[string]any, error) {
	properties, propertiesDeclared := objectKeyword(node, "properties")
	patterns, patternsDeclared := objectKeyword(node, "patternProperties")
	additional, additionalDeclared := node["additionalProperties"]
	if (!propertiesDeclared || len(properties) == 0) && (!patternsDeclared || len(patterns) == 0) && !additionalDeclared {
		return cloneJSONValue(value).(map[string]any), nil
	}

	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	projected := make(map[string]any)
	for _, key := range keys {
		fieldValue := value[key]
		var schemas []map[string]any
		if property, exists := properties[key]; exists {
			if propertyNode, allowed, err := schemaObject(property); err != nil {
				return nil, fmt.Errorf("字段 %s 的投影定义无效: %w", joinOutputPath(path, key), err)
			} else if allowed {
				schemas = append(schemas, propertyNode)
			}
		}
		for pattern, rawPatternSchema := range patterns {
			matched, err := regexp.MatchString(pattern, key)
			if err != nil {
				return nil, fmt.Errorf("字段 %s 的 patternProperties 无效: %w", joinOutputPath(path, key), err)
			}
			if !matched {
				continue
			}
			patternNode, allowed, err := schemaObject(rawPatternSchema)
			if err != nil {
				return nil, err
			}
			if allowed {
				schemas = append(schemas, patternNode)
			}
		}
		if len(schemas) == 0 {
			if !additionalDeclared {
				continue
			}
			additionalNode, allowed, err := schemaObject(additional)
			if err != nil {
				return nil, fmt.Errorf("字段 %s 的 additionalProperties 无效: %w", joinOutputPath(path, key), err)
			}
			if !allowed {
				continue
			}
			if additionalNode == nil {
				projected[key] = cloneJSONValue(fieldValue)
				continue
			}
			schemas = append(schemas, additionalNode)
		}

		fieldSchema := mergeProjectionSchemas(schemas...)
		fieldProjected, err := s.project(fieldSchema, fieldValue, joinOutputPath(path, key), depth+1, refs)
		if err != nil {
			return nil, err
		}
		projected[key] = fieldProjected
	}
	return projected, nil
}

func (s *outputProjectionState) projectArray(node map[string]any, value []any, path string, depth int, refs map[string]bool) ([]any, error) {
	prefix, _ := node["prefixItems"].([]any)
	items, itemsDeclared := node["items"]
	projected := make([]any, 0, len(value))
	for index, item := range value {
		var itemSchema map[string]any
		if index < len(prefix) {
			prefixSchema, allowed, err := schemaObject(prefix[index])
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			itemSchema = prefixSchema
		} else if itemsDeclared {
			parsed, allowed, err := schemaObject(items)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			itemSchema = parsed
		}
		if itemSchema == nil {
			projected = append(projected, cloneJSONValue(item))
			continue
		}
		itemProjected, err := s.project(itemSchema, item, path+"[]", depth+1, refs)
		if err != nil {
			return nil, err
		}
		projected = append(projected, itemProjected)
	}
	return projected, nil
}

func (s *outputProjectionState) effectiveNode(node map[string]any, value any, depth int, refs map[string]bool) (map[string]any, error) {
	if !hasProjectionLogic(node) {
		return node, nil
	}
	parts := []map[string]any{withoutProjectionLogic(node)}
	if ref, ok := node["$ref"].(string); ok {
		if !strings.HasPrefix(ref, "#/") {
			return nil, fmt.Errorf("输出投影只支持本地 $ref: %s", ref)
		}
		if refs[ref] {
			return nil, fmt.Errorf("输出投影存在循环引用: %s", ref)
		}
		target, err := resolveOutputRef(s.root, ref)
		if err != nil {
			return nil, err
		}
		refs[ref] = true
		effective, err := s.effectiveNode(target, value, depth+1, refs)
		delete(refs, ref)
		if err != nil {
			return nil, err
		}
		parts = append(parts, effective)
	}
	if branches, ok := node["allOf"].([]any); ok {
		for _, rawBranch := range branches {
			branch, allowed, err := schemaObject(rawBranch)
			if err != nil || !allowed {
				return nil, fmt.Errorf("allOf 包含无效投影分支")
			}
			effective, err := s.effectiveNode(branch, value, depth+1, refs)
			if err != nil {
				return nil, err
			}
			parts = append(parts, effective)
		}
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		branches, ok := node[keyword].([]any)
		if !ok {
			continue
		}
		matches := make([]map[string]any, 0, len(branches))
		for _, rawBranch := range branches {
			branch, allowed, err := schemaObject(rawBranch)
			if err != nil || !allowed {
				continue
			}
			if s.matches(branch, value) {
				matches = append(matches, branch)
				if keyword == "anyOf" {
					break
				}
			}
		}
		if len(matches) == 0 || keyword == "oneOf" && len(matches) != 1 {
			return nil, fmt.Errorf("输出无法唯一匹配 %s 投影分支", keyword)
		}
		effective, err := s.effectiveNode(matches[0], value, depth+1, refs)
		if err != nil {
			return nil, err
		}
		parts = append(parts, effective)
	}
	if condition, ok := node["if"].(map[string]any); ok {
		keyword := "else"
		if s.matches(condition, value) {
			keyword = "then"
		}
		if rawBranch, exists := node[keyword]; exists {
			branch, allowed, err := schemaObject(rawBranch)
			if err != nil {
				return nil, err
			}
			if allowed && branch != nil {
				effective, err := s.effectiveNode(branch, value, depth+1, refs)
				if err != nil {
					return nil, err
				}
				parts = append(parts, effective)
			}
		}
	}
	return mergeProjectionSchemas(parts...), nil
}

func (s *outputProjectionState) matches(node map[string]any, value any) bool {
	wrapper := map[string]any{}
	for _, keyword := range []string{"$schema", "$defs", "definitions"} {
		if value, exists := s.root[keyword]; exists {
			wrapper[keyword] = value
		}
	}
	wrapper["allOf"] = []any{node}
	data, err := json.Marshal(wrapper)
	if err != nil {
		return false
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return false
	}
	resolved, err := schema.Resolve(nil)
	return err == nil && resolved.Validate(value) == nil
}

func (s *outputProjectionState) addDescription(path string, node map[string]any) {
	if path == "" || len(s.descriptions) >= maxOutputDescriptions || s.described[path] {
		return
	}
	description, _ := node["description"].(string)
	description = strings.TrimSpace(description)
	if description == "" {
		return
	}
	s.described[path] = true
	s.descriptions = append(s.descriptions, outputFieldDescription{Path: path, Description: description})
}

func renderProjectedOutput(projection *outputProjection, payload []byte) string {
	if projection.RootDescription == "" && len(projection.Fields) == 0 {
		return string(payload)
	}
	var builder strings.Builder
	if projection.RootDescription != "" {
		builder.WriteString("结果说明：")
		builder.WriteString(projection.RootDescription)
		builder.WriteString("\n\n")
	}
	if len(projection.Fields) > 0 {
		builder.WriteString("字段说明：\n")
		for _, field := range projection.Fields {
			builder.WriteString("- ")
			builder.WriteString(field.Path)
			builder.WriteString("：")
			builder.WriteString(field.Description)
			builder.WriteByte('\n')
		}
		builder.WriteByte('\n')
	}
	builder.WriteString("结果：\n")
	builder.Write(payload)
	return builder.String()
}

func objectKeyword(node map[string]any, keyword string) (map[string]any, bool) {
	raw, exists := node[keyword]
	if !exists {
		return nil, false
	}
	value, ok := raw.(map[string]any)
	return value, ok
}

func schemaObject(raw any) (map[string]any, bool, error) {
	switch typed := raw.(type) {
	case map[string]any:
		return typed, true, nil
	case bool:
		if typed {
			return nil, true, nil
		}
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("不是合法 Schema")
	}
}

func mergeProjectionSchemas(schemas ...map[string]any) map[string]any {
	if len(schemas) == 1 {
		return schemas[0]
	}
	merged := map[string]any{}
	for _, schema := range schemas {
		for key, value := range schema {
			switch key {
			case "properties", "patternProperties":
				target, _ := merged[key].(map[string]any)
				if target == nil {
					target = map[string]any{}
				}
				if source, ok := value.(map[string]any); ok {
					for name, child := range source {
						if current, exists := target[name].(map[string]any); exists {
							if next, ok := child.(map[string]any); ok {
								target[name] = mergeProjectionSchemas(current, next)
								continue
							}
						}
						target[name] = child
					}
				}
				merged[key] = target
			case "required":
				seen := map[string]bool{}
				var values []any
				if current, ok := merged[key].([]any); ok {
					for _, item := range current {
						if name, ok := item.(string); ok {
							seen[name] = true
							values = append(values, name)
						}
					}
				}
				if next, ok := value.([]any); ok {
					for _, item := range next {
						name, ok := item.(string)
						if ok && !seen[name] {
							seen[name] = true
							values = append(values, name)
						}
					}
				}
				merged[key] = values
			default:
				merged[key] = value
			}
		}
	}
	return merged
}

func hasProjectionLogic(node map[string]any) bool {
	for _, keyword := range []string{"$ref", "allOf", "oneOf", "anyOf", "if"} {
		if _, exists := node[keyword]; exists {
			return true
		}
	}
	return false
}

func withoutProjectionLogic(node map[string]any) map[string]any {
	copy := map[string]any{}
	for key, value := range node {
		switch key {
		case "$ref", "allOf", "oneOf", "anyOf", "if", "then", "else", "$defs", "definitions", "x-output-projection":
			continue
		default:
			copy[key] = value
		}
	}
	return copy
}

func resolveOutputRef(root map[string]any, ref string) (map[string]any, error) {
	current := any(root)
	for _, token := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("无法解析本地引用 %s", ref)
		}
		current, ok = object[token]
		if !ok {
			return nil, fmt.Errorf("本地引用不存在: %s", ref)
		}
	}
	target, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("本地引用不是对象 Schema: %s", ref)
	}
	return target, nil
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copy := make(map[string]any, len(typed))
		for key, child := range typed {
			copy[key] = cloneJSONValue(child)
		}
		return copy
	case []any:
		copy := make([]any, len(typed))
		for index, child := range typed {
			copy[index] = cloneJSONValue(child)
		}
		return copy
	default:
		return value
	}
}

func joinOutputPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

func displayOutputPath(path string) string {
	if path == "" {
		return "$"
	}
	return path
}
