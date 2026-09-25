package react

// 模型调用重试（Phase 2：Provider Retry Limit）单元测试。

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/conf"
)

func TestClassifyModelFailureHTTPStatus(t *testing.T) {
	// 分类必须作用在未包装的原始错误上（components 错误信封只做文本拼接，不保留 Unwrap 链），
	// 与 callModelRound 的 classifyTarget 用法一致。
	retryableStatuses := []int{408, 409, 429, 500, 502, 503, 504, 529}
	for _, status := range retryableStatuses {
		class, reason := classifyModelFailure(&llm.APIError{Provider: "test", StatusCode: status})
		if class != modelFailureRetryable {
			t.Fatalf("status %d should be retryable, got %s", status, class)
		}
		if reason != fmt.Sprintf("http_%d", status) {
			t.Fatalf("unexpected reason %q for status %d", reason, status)
		}
	}
	fatalStatuses := []int{400, 401, 403, 404, 422}
	for _, status := range fatalStatuses {
		if class, _ := classifyModelFailure(&llm.APIError{Provider: "test", StatusCode: status}); class != modelFailureFatal {
			t.Fatalf("status %d should be fatal, got %s", status, class)
		}
	}
}

func TestClassifyModelFailureCancelNeverRetries(t *testing.T) {
	cases := []error{
		ErrReactRunCancelled,
		fmt.Errorf("wrapped: %w", context.Canceled),
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
		ErrReactRunTimeout,
		ErrReactClientDisconnected,
	}
	for _, err := range cases {
		if class, _ := classifyModelFailure(err); class != modelFailureCancelled {
			t.Fatalf("error %v must be cancelled class, got %s", err, class)
		}
	}
}

func TestClassifyModelFailureStreamAndUnknown(t *testing.T) {
	class, reason := classifyModelFailure(newRetryableModelError("stream_idle_timeout", errors.New("idle")))
	if class != modelFailureRetryable || reason != "stream_idle_timeout" {
		t.Fatalf("stream idle timeout should be retryable, got %s/%s", class, reason)
	}
	class, reason = classifyModelFailure(errors.New("connection reset by peer"))
	if class != modelFailureRetryable || reason != "unknown" {
		t.Fatalf("unknown network error should default to retryable, got %s/%s", class, reason)
	}
}

func TestModelRetryDelayHonorsRetryAfterAndCaps(t *testing.T) {
	// Retry-After 头优先
	err := &llm.APIError{Provider: "test", StatusCode: 429, RetryAfter: 3 * time.Second}
	if got := modelRetryDelay(1, err); got != 3*time.Second {
		t.Fatalf("Retry-After must take priority, got %v", got)
	}
	// 超过 5 分钟的 Retry-After 被忽略，回退退避曲线
	huge := &llm.APIError{Provider: "test", StatusCode: 429, RetryAfter: 30 * time.Minute}
	if got := modelRetryDelay(1, huge); got > time.Duration(conf.GetReactRuntimeConfig().ModelRetry.MaxDelayMs)*time.Millisecond+time.Second {
		t.Fatalf("delay must stay within backoff bounds, got %v", got)
	}
	// 指数退避封顶
	for attempt := 1; attempt <= 8; attempt++ {
		got := modelRetryDelay(attempt, errors.New("boom"))
		maxDelay := time.Duration(conf.GetReactRuntimeConfig().ModelRetry.MaxDelayMs) * time.Millisecond
		if got <= 0 || got > maxDelay {
			t.Fatalf("attempt %d delay %v out of bounds (max %v)", attempt, got, maxDelay)
		}
	}
}

func TestIsRetryableHTTPStatus(t *testing.T) {
	for _, status := range []int{408, 429, 500, 502, 503, 504, 529, 526} {
		if !isRetryableHTTPStatus(status) {
			t.Fatalf("status %d should be retryable", status)
		}
	}
	for _, status := range []int{400, 401, 403, 404, 422, 501} {
		if isRetryableHTTPStatus(status) {
			t.Fatalf("status %d should not be retryable", status)
		}
	}
}

func TestModelRetryMaxAttemptsFromConfig(t *testing.T) {
	original := conf.CustomConf.LLM.React.ModelRetry
	defer func() { conf.CustomConf.LLM.React.ModelRetry = original }()
	conf.CustomConf.LLM.React.ModelRetry.MaxAttempts = 5
	if got := modelRetryMaxAttempts(); got != 5 {
		t.Fatalf("expected 5 attempts from config, got %d", got)
	}
}

func TestSleepModelRetryBackoffReturnsOnCancel(t *testing.T) {
	runCtx, cancel := context.WithCancelCause(context.Background())
	state := &reactEngineState{runCtx: runCtx}
	cancel(ErrReactRunCancelled)
	if err := state.sleepModelRetryBackoff(time.Hour); !IsReactRunCancelled(err) {
		t.Fatalf("cancel during backoff must return immediately with cancel error, got %v", err)
	}
}
