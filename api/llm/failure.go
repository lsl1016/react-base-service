package llm

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxAPIErrorMessageLimit 限制 APIError 内嵌的响应体长度，避免大响应体进入错误链路与日志。
const maxAPIErrorMessageLimit = 1024

// APIError 携带 provider HTTP 状态码与 Retry-After 的模型调用错误。
// 上层失败分类器按结构化字段区分可重试与致命错误，不依赖错误文本匹配
//（终止边界治理 Phase 2：Provider Retry Limit 的判定基础）。
type APIError struct {
	Provider   string // claude / gpt 等客户端类型
	StatusCode int
	Message    string
	// RetryAfter 来自响应 Retry-After 头；0 表示 provider 未提供。
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s api error (status %d, retry-after %s): %s", e.Provider, e.StatusCode, e.RetryAfter, e.Message)
	}
	return fmt.Sprintf("%s api error (status %d): %s", e.Provider, e.StatusCode, e.Message)
}

// newAPIError 构造带 Retry-After 的 APIError；message 截断到 maxAPIErrorMessageLimit。
func newAPIError(provider string, statusCode int, retryAfter time.Duration, message string) *APIError {
	if len(message) > maxAPIErrorMessageLimit {
		message = message[:maxAPIErrorMessageLimit]
	}
	return &APIError{Provider: provider, StatusCode: statusCode, RetryAfter: retryAfter, Message: message}
}

// ParseRetryAfter 解析 Retry-After 响应头（延迟秒数或 HTTP 日期两种形态）。
// 无法解析、负值或超过 5 分钟（视为 provider 异常值）时返回 0。
func ParseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil {
		return clampRetryAfter(time.Duration(seconds) * time.Second)
	}
	if retryAt, err := http.ParseTime(header); err == nil {
		return clampRetryAfter(time.Until(retryAt))
	}
	return 0
}

func clampRetryAfter(d time.Duration) time.Duration {
	if d <= 0 || d > 5*time.Minute {
		return 0
	}
	return d
}
