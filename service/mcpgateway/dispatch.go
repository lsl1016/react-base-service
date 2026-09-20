package mcpgateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"react-base-service/conf"
	"react-base-service/golib/zlog"
	"react-base-service/service/mcpgateway/toolconfig"

	"github.com/gin-gonic/gin"
)

// UpstreamResult 是 MCP 工具上游响应归一化后的统一形态（自 mcp-server 移植）。
type UpstreamResult struct {
	Success bool
	Code    int
	Message string
	Data    interface{}
	Raw     map[string]interface{}
}

// Dispatch 执行工具定义声明的通用 JSON HTTP 调用（自 mcp-server 移植）。
//
// GET/DELETE 的入参转为 query string，其余方法序列化为 JSON body；
// 响应按常见信封（errNo/errno/code + errStr/errMsg/message/msg）归一识别成败。
func Dispatch(ctx *gin.Context, binding *ToolBinding, args map[string]interface{}) (*UpstreamResult, error) {
	if binding == nil {
		return nil, fmt.Errorf("工具请求配置为空")
	}
	request, err := buildToolHTTPRequest(ctx, binding.URL, binding.Request, args)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: binding.Request.Timeout(),
	}
	response, err := client.Do(request)
	if err != nil {
		zlog.Errorf(ctx, "[MCPGW] 通用工具调用失败，工具: %s，错误: %s", binding.Name, err.Error())
		return nil, fmt.Errorf("上游 HTTP 请求失败: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("读取上游响应失败: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return &UpstreamResult{
			Success: false,
			Code:    response.StatusCode,
			Message: fmt.Sprintf("上游 HTTP 状态码 %d", response.StatusCode),
		}, nil
	}

	decoded, err := decodeUpstreamJSON(body)
	if err != nil {
		zlog.Errorf(ctx, "[MCPGW] 通用工具响应不是合法 JSON，工具: %s，错误: %s", binding.Name, err.Error())
		return nil, err
	}
	return normalizeDecodedResponse(decoded), nil
}

func decodeUpstreamJSON(body []byte) (interface{}, error) {
	var decoded interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("上游响应不是合法 JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("上游响应包含多余的 JSON 内容")
	}
	return decoded, nil
}

func buildToolHTTPRequest(ctx *gin.Context, requestURL string, config toolconfig.RequestConfig, args map[string]interface{}) (*http.Request, error) {
	var body io.Reader
	if config.Method == http.MethodGet || config.Method == http.MethodDelete {
		parsed, err := url.Parse(requestURL)
		if err != nil {
			return nil, err
		}
		query := parsed.Query()
		for key, value := range args {
			appendQueryValue(query, key, value)
		}
		parsed.RawQuery = query.Encode()
		requestURL = parsed.String()
	} else {
		payload, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("序列化上游请求失败: %w", err)
		}
		body = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(ctx.Request.Context(), config.Method, requestURL, body)
	if err != nil {
		return nil, fmt.Errorf("创建上游请求失败: %w", err)
	}
	for key, value := range config.Headers {
		request.Header.Set(key, value)
	}
	// 按配置把指定 Cookie 透传给上游：用于上游接口依赖调用方登录态的场景
	for _, name := range conf.CustomConf.MCPServer.ForwardCookies {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if value, err := ctx.Cookie(name); err == nil && value != "" {
			request.AddCookie(&http.Cookie{Name: name, Value: value})
		}
	}
	return request, nil
}

func appendQueryValue(query url.Values, key string, value interface{}) {
	switch typed := value.(type) {
	case []interface{}:
		for _, item := range typed {
			appendQueryValue(query, key, item)
		}
	case string:
		query.Add(key, typed)
	case bool:
		query.Add(key, strconv.FormatBool(typed))
	case float64:
		query.Add(key, strconv.FormatFloat(typed, 'f', -1, 64))
	case nil:
		return
	default:
		encoded, err := json.Marshal(typed)
		if err == nil {
			query.Add(key, string(encoded))
		}
	}
}

// normalizeUpstreamResponse 从常见响应结构中提取错误码与错误文案。
// errNo/errno、errStr/errMsg/errmsg/message 均为常见命名，全部兼容。
func normalizeUpstreamResponse(raw map[string]interface{}) *UpstreamResult {
	code, hasCode := integerField(raw, "errNo", "errno", "code")
	message := firstStringField(raw, "errStr", "errMsg", "errmsg", "message", "msg")
	success := !hasCode || code == 0
	if value, exists := raw["success"]; exists {
		if flag, ok := value.(bool); ok {
			success = success && flag
		}
	}
	return &UpstreamResult{
		Success: success,
		Code:    code,
		Message: message,
		Data:    raw["data"],
		Raw:     raw,
	}
}

func normalizeDecodedResponse(decoded interface{}) *UpstreamResult {
	if raw, ok := decoded.(map[string]interface{}); ok {
		return normalizeUpstreamResponse(raw)
	}
	raw := map[string]interface{}{"data": decoded}
	return &UpstreamResult{
		Success: true,
		Data:    decoded,
		Raw:     raw,
	}
}

func integerField(data map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		switch value := data[key].(type) {
		case float64:
			return int(value), true
		case json.Number:
			parsed, err := value.Int64()
			return int(parsed), err == nil
		case int:
			return value, true
		}
	}
	return 0, false
}

func firstStringField(data map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
