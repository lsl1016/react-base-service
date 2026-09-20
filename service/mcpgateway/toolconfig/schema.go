package toolconfig

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// ParseObjectSchema 解析并展开 MCP SDK 支持的 JSON Schema 对象。
func ParseObjectSchema(raw string, required bool) (*jsonschema.Schema, *jsonschema.Resolved, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return nil, nil, fmt.Errorf("Schema 不能为空")
		}
		return nil, nil, nil
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		return nil, nil, fmt.Errorf("解析 Schema 失败: %w", err)
	}
	if schema.Type != "object" {
		return nil, nil, fmt.Errorf("Schema 顶层类型必须是 object")
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("展开 Schema 失败: %w", err)
	}
	return &schema, resolved, nil
}

func ValidateObjectSchema(raw string, required bool) error {
	_, _, err := ParseObjectSchema(raw, required)
	return err
}
