package react

import (
	"context"
	"time"
)

// 模型重试的失败分类、退避曲线与预算已下沉 internal/core（modelretry.go）；
// 本文件只保留需要引擎 runCtx 的退避休眠方法（取消感知）。

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
