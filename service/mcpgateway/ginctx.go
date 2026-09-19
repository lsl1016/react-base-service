// Package mcpgateway 是 MCP 服务端网关：把 tblLlmTool 中启用的 http 类型工具按
// caller 作用域通过 MCP Streamable HTTP 协议对外暴露给外部 MCP 客户端
//（Claude/Cursor 等），核心行为（鉴权、通用 HTTP 分派、输出投影+字段描述渲染、
// 调用审计）自 mcp-server 项目移植。与 service/mcpclient（MCP 客户端）相对。
package mcpgateway

import (
	"context"

	"github.com/gin-gonic/gin"
)

// ginCtxKey 是私有类型，避免与其他包的 context key 冲突。
type ginCtxKey struct{}

// appCtxKey 承载鉴权中间件认证后的应用身份。
type appCtxKey struct{}

// AppIdentity 是认证通过后的调用方身份，由 Auth 中间件写入。
type AppIdentity struct {
	AppID     string
	AppKey    string
	CallerKey string
	UserName  string
}

// WithGinContext 把 *gin.Context 挂到标准 context 上。
//
// 项目内 DAO、zlog 的签名都要求 *gin.Context，而 MCP SDK 的 ToolHandler 只提供
// context.Context。这里让 context 顺带捎上原始 *gin.Context，从而在不改动任何
// 既有函数签名的前提下复用全部现有能力。
func WithGinContext(ctx context.Context, c *gin.Context) context.Context {
	return context.WithValue(ctx, ginCtxKey{}, c)
}

// GinContextFrom 取回请求生命周期内的 *gin.Context。
//
// 注意：返回值只在当前请求内有效，不可存入异步 goroutine 长期持有。
func GinContextFrom(ctx context.Context) (*gin.Context, bool) {
	c, ok := ctx.Value(ginCtxKey{}).(*gin.Context)
	return c, ok
}

// WithAppIdentity 把认证后的应用身份写入 context。
func WithAppIdentity(ctx context.Context, identity AppIdentity) context.Context {
	return context.WithValue(ctx, appCtxKey{}, identity)
}

// AppIdentityFrom 取回当前请求的应用身份。
func AppIdentityFrom(ctx context.Context) (AppIdentity, bool) {
	identity, ok := ctx.Value(appCtxKey{}).(AppIdentity)
	return identity, ok
}

// ginIdentityKey 是身份信息在 gin.Context 中的键。
const ginIdentityKey = "mcp_gateway_app_identity"

// SetIdentity 由 Auth 中间件在认证通过后写入调用方身份。
func SetIdentity(c *gin.Context, identity AppIdentity) {
	c.Set(ginIdentityKey, identity)
}

// IdentityFromGin 从 gin 上下文取回调用方身份。
func IdentityFromGin(c *gin.Context) (AppIdentity, bool) {
	value, exists := c.Get(ginIdentityKey)
	if !exists {
		return AppIdentity{}, false
	}
	identity, ok := value.(AppIdentity)
	return identity, ok
}
