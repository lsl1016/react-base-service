package core

import (
	"context"
	"errors"
)

// Runtime 错误哨兵与判定：取消/断连/超时/交互超时与可重试模型错误的唯一权威定义处。
// 哨兵值全局唯一，react 门面经 core_bridge 以同名变量别名复用（errors.Is 语义不受影响）。

var (
	ErrReactRunCancelled       = errors.New("react run cancelled")
	ErrReactClientDisconnected = errors.New("react client disconnected")
	ErrReactRunTimeout         = errors.New("react run timeout")
	// ErrInteractionTimeout 交互等待（ask_question / client tool / 工具确认）超过配置时限；
	// 以错误工具结果回灌模型继续循环，不终止 run。
	ErrInteractionTimeout = errors.New("react interaction wait timeout")
)

func IsReactRunCancelled(err error) bool {
	return errors.Is(err, ErrReactRunCancelled) || errors.Is(err, context.Canceled)
}

func IsReactClientDisconnected(err error) bool {
	return errors.Is(err, ErrReactClientDisconnected)
}

func IsReactRunTimeout(err error) bool {
	return errors.Is(err, ErrReactRunTimeout)
}

func IsErrInteractionTimeout(err error) bool {
	return errors.Is(err, ErrInteractionTimeout)
}

type retryableModelError struct {
	reason string
	err    error
}

func (e *retryableModelError) Error() string { return e.err.Error() }
func (e *retryableModelError) Unwrap() error { return e.err }

func NewRetryableModelError(reason string, err error) error {
	return &retryableModelError{reason: reason, err: err}
}

func RetryableModelErrorInfo(err error) (reason string, ok bool) {
	var target *retryableModelError
	if !errors.As(err, &target) {
		return "", false
	}
	return target.reason, true
}

// UnwrapRetryableModelError 剥掉可重试标记外壳，返回内层原始错误（非可重试错误原样返回）。
func UnwrapRetryableModelError(err error) error {
	var target *retryableModelError
	if errors.As(err, &target) {
		return target.err
	}
	return err
}
