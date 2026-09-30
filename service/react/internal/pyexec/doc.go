package pyexec

import (
	"context"

	"github.com/gin-gonic/gin"
)

// RunContext 是从引擎状态提取的最小执行面：分析执行域只关心身份与请求上下文，
// 不触碰引擎对话状态。由 react 门面在 meta tool 分发处构造。
type RunContext struct {
	GinCtx    *gin.Context
	RunCtx    context.Context
	RunID     string
	SessionID string
	UserName  string
	CallerKey string
}
