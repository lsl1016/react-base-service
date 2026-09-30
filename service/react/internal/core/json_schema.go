package core

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
)

// JSONSchema 是 Business Tool inputSchema 使用的 JSON Schema 子集表示。
// 原实现位于 planruntime 包；Plan 模式移除后，工具参数校验能力保留在 Runtime 内。
type JSONSchema map[string]any

// ValidateJSONSchemaValue 校验值是否满足工具入参使用的 JSON Schema 子集。
// 当前支持 object/array/string/number/integer/boolean/null、required、properties、items、enum、minLength 和 additionalProperties。
func ValidateJSONSchemaValue(schema JSONSchema, value any) error {
	return validateSchemaAt(schema, value, "$")
}

func validateSchemaAt(schema JSONSchema, value any, path string) error {
	if len(schema) == 0 {
		return fmt.Errorf("%s schema is empty", path)
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
