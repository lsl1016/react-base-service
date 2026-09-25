package react

// 模型调用同模型重试（终止边界治理 Phase 2：Provider Retry Limit）。
//
// 设计对齐 ZCode retry-policy/failure-classifier：
//   - 失败先按结构化字段分类（HTTP 状态码 / retryable 标记），不做错误文本模糊匹配；
//   - 可重试失败在当前模型上指数退避重试，重试预算耗尽后才切换互备模型；
//   - Retry-After 头 ≤5 分钟视为合理值优先采用；
//   - 取消/断连/超时绝不重试，原样上抛。

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/components/metrics"
	"react-base-service/conf"
)

// maxModelRetryAfterCap 是 Retry-After 头可被直接采用的上限；超过视为 provider 异常值，
// 回退到本地退避曲线。
const maxModelRetryAfterCap = 5 * time.Minute

// modelFailureClass 模型调用失败类别。
type modelFailureClass string

const (
	// modelFailureRetryable 瞬态失败：同模型退避重试，预算耗尽后可切互备。
	modelFailureRetryable modelFailureClass = "retryable"
	// modelFailureFatal 客户端/鉴权/参数类错误：重试无意义，直接切换互备或终止。
	modelFailureFatal modelFailureClass = "fatal"
	// modelFailureCancelled 取消/断连/run 超时：原样上抛，绝不重试。
	modelFailureCancelled modelFailureClass = "cancelled"
)

// isRetryableHTTPStatus 按 provider 语义划分可重试的 HTTP 状态码：
// 408/409/429/5xx（含 529 overloaded）瞬态可重试；4xx 其余为客户端错误不重试。
func isRetryableHTTPStatus(statusCode int) bool {
	switch statusCode {
	case httpStatusRequestTimeout, httpStatusConflict, httpStatusTooManyRequests,
		httpStatusInternalServerError, httpStatusBadGateway, httpStatusServiceUnavailable,
		httpStatusGatewayTimeout:
		return true
	}
	return statusCode >= 500 && statusCode != httpStatusNotImplemented
}

const (
	httpStatusRequestTimeout      = 408
	httpStatusConflict            = 409
	httpStatusTooManyRequests     = 429
	httpStatusInternalServerError = 500
	httpStatusNotImplemented      = 501
	httpStatusBadGateway          = 502
	httpStatusServiceUnavailable  = 503
	httpStatusGatewayTimeout      = 504
)

// classifyModelFailure 对模型调用错误分类；reason 用于事件与日志（http_429 / stream_idle_timeout 等）。
func classifyModelFailure(err error) (modelFailureClass, string) {
	if err == nil {
		return modelFailureFatal, ""
	}
	if IsReactRunCancelled(err) || IsReactClientDisconnected(err) || IsReactRunTimeout(err) ||
		errors.Is(err, context.DeadlineExceeded) {
		return modelFailureCancelled, ""
	}
	var apiErr *llm.APIError
	if errors.As(err, &apiErr) {
		if isRetryableHTTPStatus(apiErr.StatusCode) {
			return modelFailureRetryable, fmt.Sprintf("http_%d", apiErr.StatusCode)
		}
		return modelFailureFatal, fmt.Sprintf("http_%d", apiErr.StatusCode)
	}
	if info, ok := retryableModelErrorInfo(err); ok {
		return modelFailureRetryable, info.reason
	}
	// 未知错误（网络抖动、连接重置、TLS 等）按可重试处理：重试有预算上限，
	// 预算耗尽仍会切换互备或终止，不会无限循环。
	return modelFailureRetryable, "unknown"
}

// modelRetryDelay 计算第 attempt 次失败后的重试延迟：Retry-After 优先，否则指数退避 + 抖动。
func modelRetryDelay(attempt int, err error) time.Duration {
	var apiErr *llm.APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 && apiErr.RetryAfter <= maxModelRetryAfterCap {
		return apiErr.RetryAfter
	}
	cfg := conf.GetReactRuntimeConfig().ModelRetry
	base := time.Duration(cfg.BaseDelayMs) * time.Millisecond
	maxDelay := time.Duration(cfg.MaxDelayMs) * time.Millisecond
	delay := base
	for i := 1; i < attempt && delay < maxDelay; i++ {
		delay *= 2
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	// ±30% 抖动，避免互备组内多 run 同步重试形成节拍性冲击。
	jitter := time.Duration(float64(delay) * (0.7 + 0.6*rand.Float64()))
	if jitter > maxDelay {
		jitter = maxDelay
	}
	return jitter
}

// modelRetryMaxAttempts 返回同模型重试预算（含首次调用）。
func modelRetryMaxAttempts() int {
	return conf.GetReactRuntimeConfig().ModelRetry.MaxAttempts
}

// recordModelRetry 记录重试指标。
func recordModelRetry(reason string) {
	metrics.ModelRetriesTotal.WithLabelValues(reason).Inc()
}

// sleepModelRetryBackoff 可取消的退避等待；run 被取消时立即返回取消错误。
func (s *reactEngineState) sleepModelRetryBackoff(delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-s.runCtx.Done():
		if cause := context.Cause(s.runCtx); cause != nil {
			return cause
		}
		return ErrReactRunCancelled
	}
}
