package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/conf"

	"react-base-service/golib/zlog"
)

// defaultHTTPToolTimeoutMs 返回 HTTP 工具未配置 timeout_ms 时的统一默认超时：
// react.tool.default_timeout_ms > 内置兜底 10s（终止边界治理：替代分散硬编码）。
func defaultHTTPToolTimeoutMs() int {
	if ms := conf.GetReactRuntimeConfig().Tool.DefaultTimeoutMs; ms > 0 {
		return ms
	}
	return 10000
}

// ToolConfig 是 tblLlmTool.config 的兼容配置结构，包含通用 schema 字段，以及 HTTP/client 等执行端可选字段。
type ToolConfig struct {
	URL          string                 `json:"url"`
	Method       string                 `json:"method"`
	TimeoutMs    int                    `json:"timeout_ms"`
	Headers      map[string]string      `json:"headers"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  map[string]interface{} `json:"inputSchema,omitempty"`
	OutputSchema map[string]interface{} `json:"outputSchema,omitempty"`
	FrontendHint string                 `json:"frontendHint,omitempty"`
	// MCPServer/MCPTool 标识 mcp 类型工具的路由目标（service/mcpclient 同步写入）。
	MCPServer string `json:"mcpServer,omitempty"`
	MCPTool   string `json:"mcpTool,omitempty"`
	// Async 标识提交型异步工具：调用成功仅代表任务受理，结果需异步获取。
	Async bool `json:"async,omitempty"`
	// AsyncHint 描述旧模式下由模型查询结果的方式，随提醒注入给模型。
	AsyncHint string `json:"asyncHint,omitempty"`
	// AsyncTask 声明由后端调度系统 Provider 自动同步状态；为空时保留旧的模型提醒机制。
	AsyncTask *AsyncTaskConfig `json:"asyncTask,omitempty"`
	// RiskPatterns 是 confirm_risky 模式的自定义风险正则（大小写不敏感），
	// 匹配目标为工具名 + 序列化入参；为空时使用内置默认风险表（drop/alter/kill 等）。
	RiskPatterns []string `json:"riskPatterns,omitempty"`
}

// AsyncTaskConfig 描述异步 Tool 所属的调度系统，不暴露 MQ、接口或状态查询 Tool 等实现细节。
type AsyncTaskConfig struct {
	SchedulerType string `json:"schedulerType"`
}

// ParseToolConfig 只解析 tool 的 config JSON，不做具体执行端的必填校验和默认值填充。
// 规范命名为 camelCase；存量数据可能仍是 snake_case，缺少 camelCase 字段时回退兼容解析。
func ParseToolConfig(configJSON string) (*ToolConfig, error) {
	var cfg ToolConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("解析工具配置失败: %w", err)
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(configJSON), &rawFields); err != nil {
		return nil, fmt.Errorf("解析工具配置失败: %w", err)
	}
	if _, ok := rawFields["inputSchema"]; !ok {
		var compat struct {
			InputSchema map[string]interface{} `json:"input_schema"`
		}
		if err := json.Unmarshal([]byte(configJSON), &compat); err != nil {
			return nil, fmt.Errorf("解析工具配置失败: %w", err)
		}
		cfg.InputSchema = compat.InputSchema
	}
	if _, ok := rawFields["outputSchema"]; !ok {
		var compat struct {
			OutputSchema map[string]interface{} `json:"output_schema"`
		}
		if err := json.Unmarshal([]byte(configJSON), &compat); err != nil {
			return nil, fmt.Errorf("解析工具配置失败: %w", err)
		}
		cfg.OutputSchema = compat.OutputSchema
	}
	if _, ok := rawFields["frontendHint"]; !ok {
		var compat struct {
			FrontendHint string `json:"frontend_hint"`
		}
		if err := json.Unmarshal([]byte(configJSON), &compat); err != nil {
			return nil, fmt.Errorf("解析工具配置失败: %w", err)
		}
		cfg.FrontendHint = compat.FrontendHint
	}
	return &cfg, nil
}

// ExecuteHTTPTool 执行 HTTP 类型的 tool，cookies 从上游请求上下文透传
func ExecuteHTTPTool(ctx context.Context, configJSON string, input interface{}, cookies map[string]string) (string, error) {
	cfg, err := ParseToolConfig(configJSON)
	if err != nil {
		return "", components.ErrorToolExecFailed.Sprintf(err.Error())
	}

	if strings.TrimSpace(cfg.URL) == "" {
		return "", components.ErrorToolExecFailed.Sprintf("工具配置缺少url")
	}
	// HTTP method 大小写归一：接入方可能注册小写 "post"，原样发送会被严格解析器按非法方法拒绝。
	cfg.Method = strings.ToUpper(strings.TrimSpace(cfg.Method))
	if cfg.Method == "" {
		cfg.Method = http.MethodPost
	}
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = defaultHTTPToolTimeoutMs()
	}

	if cfg.Method == http.MethodGet {
		return executeHTTPToolGET(ctx, cfg, input, cookies)
	}

	body, err := json.Marshal(input)
	if err != nil {
		return "", components.ErrorToolExecFailed.Sprintf("序列化输入参数失败: " + err.Error())
	}

	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	httpCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, cfg.Method, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return "", components.ErrorToolExecFailed.Sprintf("创建HTTP请求失败: " + err.Error())
	}

	if cfg.Headers != nil {
		for k, v := range cfg.Headers {
			req.Header.Set(k, v)
		}
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookieHeader := components.BuildCookieHeader(cookies); cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	components.ApplyHTTPToolHeaders(ctx, req.Header)
	// 从上游 context 透传链路追踪信息（context 可能由 gin.Context 经 WithTimeout 包裹而来）
	if logID, _ := ctx.Value("logID").(string); logID != "" {
		req.Header.Set("X-Log-Id", logID)
	}
	if requestID, _ := ctx.Value("requestId").(string); requestID != "" {
		req.Header.Set("Uber-Trace-Id", requestID)
	}

	logHTTPRequest(ctx, req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", components.ErrorToolExecTimeout.Sprintf(cfg.URL)
		}
		return "", components.ErrorToolExecFailed.Sprintf("HTTP请求失败: " + err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", components.ErrorToolExecFailed.Sprintf("读取响应失败: " + err.Error())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		zlog.Warnf(nil, "[ExecuteHTTPTool] HTTP %d, url=%s, body=%s", resp.StatusCode, cfg.URL, string(respBody))
		return "", components.ErrorToolExecFailed.Sprintf(
			fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)))
	}

	return string(respBody), nil
}

func executeHTTPToolGET(ctx context.Context, cfg *ToolConfig, input interface{}, cookies map[string]string) (string, error) {
	requestURL, err := appendQueryParams(cfg.URL, input)
	if err != nil {
		return "", components.ErrorToolExecFailed.Sprintf("构造GET请求参数失败: " + err.Error())
	}

	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	httpCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, requestURL, bytes.NewReader(nil))
	if err != nil {
		return "", components.ErrorToolExecFailed.Sprintf("创建HTTP请求失败: " + err.Error())
	}

	if cfg.Headers != nil {
		for k, v := range cfg.Headers {
			req.Header.Set(k, v)
		}
	}
	if cookieHeader := components.BuildCookieHeader(cookies); cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	components.ApplyHTTPToolHeaders(ctx, req.Header)

	// 从上游 context 透传链路追踪信息（context 可能由 gin.Context 经 WithTimeout 包裹而来）
	if logID, _ := ctx.Value("logID").(string); logID != "" {
		req.Header.Set("X-Log-Id", logID)
	}
	if requestID, _ := ctx.Value("requestId").(string); requestID != "" {
		req.Header.Set("Uber-Trace-Id", requestID)
	}

	logHTTPRequest(ctx, req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", components.ErrorToolExecTimeout.Sprintf(cfg.URL)
		}
		return "", components.ErrorToolExecFailed.Sprintf("HTTP请求失败: " + err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", components.ErrorToolExecFailed.Sprintf("读取响应失败: " + err.Error())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		zlog.Warnf(nil, "[ExecuteHTTPTool] HTTP %d, url=%s, body=%s", resp.StatusCode, cfg.URL, string(respBody))
		return "", components.ErrorToolExecFailed.Sprintf(
			fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)))
	}

	return string(respBody), nil
}

func appendQueryParams(rawURL string, input interface{}) (string, error) {
	queryInput, err := normalizeQueryInputObject(input)
	if err != nil {
		return "", err
	}
	if len(queryInput) == 0 {
		return rawURL, nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	for key, value := range queryInput {
		appendQueryValue(query, key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func normalizeQueryInputObject(input interface{}) (map[string]interface{}, error) {
	if input == nil {
		return nil, nil
	}
	if m, ok := input.(map[string]interface{}); ok {
		return m, nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("GET 工具入参必须为 object")
	}
	return result, nil
}

func appendQueryValue(query url.Values, key string, value interface{}) {
	if value == nil {
		return
	}
	rv := reflect.ValueOf(value)
	if rv.IsValid() {
		switch rv.Kind() {
		case reflect.Slice, reflect.Array:
			for i := 0; i < rv.Len(); i++ {
				query.Add(key, fmt.Sprint(rv.Index(i).Interface()))
			}
			return
		}
	}
	query.Set(key, fmt.Sprint(value))
}

func logHTTPRequest(ctx context.Context, req *http.Request) {
	zlog.Infof(nil, "[ExecuteHTTPTool] request method=%s url=%s headers=%v", req.Method, req.URL.String(), maskedHeadersForLog(req.Header))
}

func maskedHeadersForLog(headers http.Header) http.Header {
	cloned := headers.Clone()
	for key, values := range cloned {
		if !isSensitiveHeader(key) {
			continue
		}
		maskedValues := make([]string, len(values))
		for i, value := range values {
			maskedValues[i] = maskHeaderValueKeepFirstLast(value)
		}
		cloned[key] = maskedValues
	}
	return cloned
}

func isSensitiveHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "cookie", "set-cookie", "x-service-token":
		return true
	default:
		return false
	}
}

func maskHeaderValueKeepFirstLast(value string) string {
	runes := []rune(value)
	if len(runes) <= 2 {
		return value
	}
	return string(runes[0]) + strings.Repeat("*", len(runes)-2) + string(runes[len(runes)-1])
}
