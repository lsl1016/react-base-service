package toolconfig

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const outputProjectionKey = "x-output-projection"
const maxProjectionSchemaDepth = 64

// ValidateOutputSchema 校验工具输出 Schema；启用投影时额外检查投影器支持的语法。
func ValidateOutputSchema(raw string, required bool) error {
	if err := ValidateObjectSchema(raw, required); err != nil {
		return err
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return fmt.Errorf("解析输出 Schema 失败: %w", err)
	}
	enabled, exists := root[outputProjectionKey]
	if !exists {
		return nil
	}
	projectionEnabled, ok := enabled.(bool)
	if !ok {
		return fmt.Errorf("%s 必须是布尔值", outputProjectionKey)
	}
	if !projectionEnabled {
		return nil
	}
	return validateProjectionNode(root, root, "$", 0, map[string]bool{})
}

// OutputProjectionEnabled 返回输出 Schema 是否显式开启服务端投影。
func OutputProjectionEnabled(raw string) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return false, nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return false, fmt.Errorf("解析输出 Schema 失败: %w", err)
	}
	value, exists := root[outputProjectionKey]
	if !exists {
		return false, nil
	}
	enabled, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s 必须是布尔值", outputProjectionKey)
	}
	return enabled, nil
}

func validateProjectionNode(root, node map[string]any, path string, depth int, refs map[string]bool) error {
	if depth > maxProjectionSchemaDepth {
		return fmt.Errorf("投影 Schema 嵌套超过 %d 层: %s", maxProjectionSchemaDepth, path)
	}
	if ref, ok := node["$ref"].(string); ok {
		if !strings.HasPrefix(ref, "#/") {
			return fmt.Errorf("投影只支持本地 $ref: %s", path)
		}
		if refs[ref] {
			return fmt.Errorf("投影 Schema 存在循环引用: %s", ref)
		}
		target, err := resolveProjectionRef(root, ref)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		refs[ref] = true
		if err := validateProjectionNode(root, target, ref, depth+1, refs); err != nil {
			return err
		}
		delete(refs, ref)
	}

	for keyword, value := range node {
		switch keyword {
		case "properties", "patternProperties", "$defs", "definitions":
			children, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s 必须是对象", path, keyword)
			}
			for name, child := range children {
				childNode, ok := child.(map[string]any)
				if !ok {
					if _, booleanSchema := child.(bool); booleanSchema {
						continue
					}
					return fmt.Errorf("%s.%s.%s 必须是 Schema", path, keyword, name)
				}
				if keyword == "patternProperties" {
					if _, err := regexp.Compile(name); err != nil {
						return fmt.Errorf("%s.patternProperties 的正则 %q 无效", path, name)
					}
				}
				if err := validateProjectionNode(root, childNode, path+"."+keyword+"."+name, depth+1, refs); err != nil {
					return err
				}
			}
		case "items", "additionalProperties", "if", "then", "else":
			if _, ok := value.(bool); ok {
				continue
			}
			child, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s 必须是 Schema", path, keyword)
			}
			if err := validateProjectionNode(root, child, path+"."+keyword, depth+1, refs); err != nil {
				return err
			}
		case "prefixItems", "allOf", "oneOf", "anyOf":
			children, ok := value.([]any)
			if !ok || len(children) == 0 {
				return fmt.Errorf("%s.%s 必须是非空 Schema 数组", path, keyword)
			}
			for index, child := range children {
				childNode, ok := child.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s[%d] 必须是 Schema", path, keyword, index)
				}
				if err := validateProjectionNode(root, childNode, fmt.Sprintf("%s.%s[%d]", path, keyword, index), depth+1, refs); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func resolveProjectionRef(root map[string]any, ref string) (map[string]any, error) {
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
