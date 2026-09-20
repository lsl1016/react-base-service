// Package toolconfig 承载 MCP 网关工具的请求配置与 Schema 解析校验，
// 自 mcp-server 项目移植（去掉 local:// 本地工具分支：网关仅暴露 HTTP 上游工具）。
package toolconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultTimeout = 10 * time.Second
	MinTimeout     = 100 * time.Millisecond
	MaxTimeout     = 120 * time.Second
)

var allowedMethods = map[string]struct{}{
	http.MethodGet:    {},
	http.MethodPost:   {},
	http.MethodPut:    {},
	http.MethodPatch:  {},
	http.MethodDelete: {},
}

// RequestConfig 是一个网关工具的 HTTP 执行配置（由 tblLlmTool.config 的
// method/timeout_ms/headers 字段构成）。
type RequestConfig struct {
	Method    string            `json:"method"`
	TimeoutMS int               `json:"timeout_ms"`
	Headers   map[string]string `json:"headers,omitempty"`
}

func (c *RequestConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutMS) * time.Millisecond
}

// Normalize 校验并归一配置：method 大写白名单、超时范围、header 规范化
// （自动补 Content-Type/Accept）。返回错误时配置不可用于上游调用。
func (c *RequestConfig) Normalize() error {
	c.Method = strings.ToUpper(strings.TrimSpace(c.Method))
	if c.Method == "" {
		c.Method = http.MethodPost
	}
	if _, ok := allowedMethods[c.Method]; !ok {
		return fmt.Errorf("不支持请求方法 %q", c.Method)
	}

	if c.TimeoutMS == 0 {
		c.TimeoutMS = int(DefaultTimeout / time.Millisecond)
	}
	timeout := c.Timeout()
	if timeout < MinTimeout || timeout > MaxTimeout {
		return fmt.Errorf("请求超时时间必须在 %s 到 %s 之间", MinTimeout, MaxTimeout)
	}

	normalizedHeaders := make(map[string]string, len(c.Headers))
	for key, value := range c.Headers {
		key = http.CanonicalHeaderKey(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" {
			return fmt.Errorf("请求 Header 名称不能为空")
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("请求 Header %q 不能包含换行符", key)
		}
		normalizedHeaders[key] = value
	}
	if c.Method != http.MethodGet && normalizedHeaders["Content-Type"] == "" {
		normalizedHeaders["Content-Type"] = "application/json"
	}
	if normalizedHeaders["Accept"] == "" {
		normalizedHeaders["Accept"] = "application/json"
	}
	c.Headers = normalizedHeaders
	return nil
}

// ParseRequestConfig 解析独立 JSON 形式的请求配置（批量注册校验用）。
func ParseRequestConfig(raw string) (*RequestConfig, error) {
	config := &RequestConfig{}
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("请求配置不能为空")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(config); err != nil {
		return nil, fmt.Errorf("解析请求配置失败: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("解析请求配置失败: 存在多余的 JSON 内容")
	}
	if err := config.Normalize(); err != nil {
		return nil, err
	}
	return config, nil
}

// NormalizeRequestURL 校验工具上游 URL：仅 http/https 绝对地址，不含用户信息与片段。
func NormalizeRequestURL(raw string) (string, error) {
	requestURL := strings.TrimSpace(raw)
	parsed, err := url.Parse(requestURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("请求 URL 必须是包含协议和域名的绝对地址")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("请求 URL 不能包含用户信息或片段标识")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("请求 URL 只支持 HTTP/HTTPS 协议")
	}
	return requestURL, nil
}
