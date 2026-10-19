package core

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// JSONSchema 是 Business Tool inputSchema 使用的 JSON Schema 子集表示。
// 原实现位于 planruntime 包；Plan 模式移除后，工具参数校验能力保留在 Runtime 内。
type JSONSchema map[string]any

// ValidateJSONSchemaValue 校验值是否满足工具入参使用的 JSON Schema 子集。
// 当前支持 object/array/string/number/integer/boolean/null、required、properties、items、enum、minLength、additionalProperties，以及 oneOf/anyOf 组合分支。
func ValidateJSONSchemaValue(schema JSONSchema, value any) error {
	return validateSchemaAt(schema, value, "$")
}

func validateSchemaAt(schema JSONSchema, value any, path string) error {
	if len(schema) == 0 {
		return fmt.Errorf("%s schema is empty", path)
	}
	// oneOf/anyOf 组合分支：MCP 工具常用 oneOf 表达"字符串或数组"这类多形态字段。
	// 任一分支通过即通过（oneOf 严格语义要求"恰好一个"匹配，这里按 anyOf 宽松处理，
	// 避免分支偶有重叠时误杀合法入参）；全不通过时拼出各分支错误，供模型自我纠正。
	for _, keyword := range []string{"oneOf", "anyOf"} {
		if rawBranches, exists := schema[keyword]; exists {
			return validateSchemaBranches(rawBranches, value, path, keyword)
		}
	}
	typeName, _ := schema["type"].(string)
	if typeName == "" {
		return fmt.Errorf("%s schema.type is required", path)
	}
	if value == nil {
		if typeName == "null" {
			return nil
		}
		return fmt.Errorf("%s must be %s, got null", path, typeName)
	}
	if rawEnum, exists := schema["enum"]; exists {
		enumValues, ok := asSlice(rawEnum)
		if !ok || !containsJSONValue(enumValues, value) {
			return fmt.Errorf("%s is not in enum", path)
		}
	}

	switch typeName {
	case "object":
		object, ok := asStringMap(value)
		if !ok {
			return fmt.Errorf("%s must be object", path)
		}
		required := asStringSlice(schema["required"])
		for _, key := range required {
			if _, exists := object[key]; !exists {
				return fmt.Errorf("%s.%s is required", path, key)
			}
		}
		properties, _ := asStringMap(schema["properties"])
		allowAdditional := true
		if configured, ok := schema["additionalProperties"].(bool); ok {
			allowAdditional = configured
		}
		for key, item := range object {
			rawProperty, exists := properties[key]
			if !exists {
				if !allowAdditional {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
				continue
			}
			propertySchema, ok := asStringMap(rawProperty)
			if !ok {
				return fmt.Errorf("%s.properties.%s must be schema object", path, key)
			}
			if err := validateSchemaAt(JSONSchema(propertySchema), item, path+"."+key); err != nil {
				return err
			}
		}
	case "array":
		items, ok := asSlice(value)
		if !ok {
			return fmt.Errorf("%s must be array", path)
		}
		itemSchemaMap, ok := asStringMap(schema["items"])
		if !ok {
			return fmt.Errorf("%s.items must be schema object", path)
		}
		for index, item := range items {
			if err := validateSchemaAt(JSONSchema(itemSchemaMap), item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be string", path)
		}
		if minLength, ok := numberAsInt(schema["minLength"]); ok && len([]rune(text)) < minLength {
			return fmt.Errorf("%s length must be >= %d", path, minLength)
		}
	case "number":
		if _, ok := asFloat64(value); !ok {
			return fmt.Errorf("%s must be number", path)
		}
	case "integer":
		number, ok := asFloat64(value)
		if !ok || math.Trunc(number) != number {
			return fmt.Errorf("%s must be integer", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be boolean", path)
		}
	case "null":
		return fmt.Errorf("%s must be null", path)
	default:
		return fmt.Errorf("%s has unsupported schema type %q", path, typeName)
	}
	return nil
}

// validateSchemaBranches 校验 oneOf/anyOf 组合分支，返回 nil 表示任一分支通过。
func validateSchemaBranches(rawBranches any, value any, path string, keyword string) error {
	branches, ok := asSlice(rawBranches)
	if !ok || len(branches) == 0 {
		return fmt.Errorf("%s schema.%s must be non-empty array", path, keyword)
	}
	branchErrs := make([]string, 0, len(branches))
	for _, branch := range branches {
		branchSchema, ok := asStringMap(branch)
		if !ok {
			branchErrs = append(branchErrs, "branch must be schema object")
			continue
		}
		if err := validateSchemaAt(JSONSchema(branchSchema), value, path); err != nil {
			branchErrs = append(branchErrs, err.Error())
			continue
		}
		return nil
	}
	return fmt.Errorf("%s must match one of %s branches: %s", path, keyword, strings.Join(branchErrs, "; "))
}

func containsJSONValue(values []any, target any) bool {
	targetJSON, _ := json.Marshal(target)
	for _, value := range values {
		valueJSON, _ := json.Marshal(value)
		if string(valueJSON) == string(targetJSON) {
			return true
		}
	}
	return false
}

func asStringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case JSONSchema:
		return map[string]any(typed), true
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return nil, false
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, false
		}
		return result, true
	}
}

func asStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func asSlice(value any) ([]any, bool) {
	if typed, ok := value.([]any); ok {
		return typed, true
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) {
		return nil, false
	}
	result := make([]any, reflected.Len())
	for i := 0; i < reflected.Len(); i++ {
		result[i] = reflected.Index(i).Interface()
	}
	return result, true
}

func asFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

func numberAsInt(value any) (int, bool) {
	number, ok := asFloat64(value)
	if !ok || math.Trunc(number) != number {
		if text, textOK := value.(string); textOK {
			parsed, err := strconv.Atoi(text)
			return parsed, err == nil
		}
		return 0, false
	}
	return int(number), true
}
