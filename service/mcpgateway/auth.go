package mcpgateway

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"react-base-service/golib/zlog"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

const (
	// bearerPrefix 标准 Authorization 前缀。
	bearerPrefix = "Bearer "
	// credentialSeparator app_key 与 app_secret 的分隔符。
	credentialSeparator = ":"
	// mcpUserHeader 可选的调用方用户标识，仅用于审计展示，不参与鉴权。
	mcpUserHeader = "X-MCP-User"
)

// Auth 校验 MCP 网关调用方身份（挂载在 /mcp 端点前）。
//
// MCP 面向 AI 客户端开放，必须校验 app_secret 才能确认「你是谁」；
// 认证通过后由 registry 层继续做「你能调什么」（caller 作用域工具可见性）。
//
// 失败一律返回 HTTP 403 而非 401：多数 MCP 客户端把 401 解释为「本服务需要 OAuth」，
// 会丢弃响应体转去做 OAuth 发现，最终把网关返回的错误页当 JSON 解析而报错。
func Auth(c *gin.Context) {
	appKey, appSecret, ok := parseBearerCredential(c.GetHeader("Authorization"))
	if !ok {
		abortUnauthorized(c, "缺少或格式错误的 Authorization 头，应为 Bearer <app_key>:<app_secret>")
		return
	}

	app, err := model.GetMcpAppByAppKey(c, appKey)
	if err != nil {
		zlog.Errorf(c, "[MCPGW] 查询应用凭证失败: appKey=%s err=%v", appKey, err)
		abortUnauthorized(c, "应用凭证校验失败，请稍后重试")
		return
	}
	if app == nil {
		zlog.Warnf(c, "[MCPGW] app_key 无效或应用已停用: %s", appKey)
		abortUnauthorized(c, "app_key 无效或应用已停用")
		return
	}

	// 常量时间比较，避免通过响应耗时差异推断 secret
	if subtle.ConstantTimeCompare([]byte(app.AppSecret), []byte(appSecret)) != 1 {
		zlog.Warnf(c, "[MCPGW] app_secret 不正确: appKey=%s", appKey)
		abortUnauthorized(c, "app_secret 不正确")
		return
	}

	SetIdentity(c, AppIdentity{
		AppID:     app.AppID,
		AppKey:    app.AppKey,
		CallerKey: app.CallerKey,
		UserName:  strings.TrimSpace(c.GetHeader(mcpUserHeader)),
	})
	c.Next()
}

// parseBearerCredential 解析 Bearer <app_key>:<app_secret>。
func parseBearerCredential(header string) (appKey, appSecret string, ok bool) {
	if header == "" {
		return "", "", false
	}
	if !strings.HasPrefix(header, bearerPrefix) {
		return "", "", false
	}

	credential := strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
	// app_secret 本身可能含冒号，只按第一个冒号切分
	index := strings.Index(credential, credentialSeparator)
	if index <= 0 || index == len(credential)-1 {
		return "", "", false
	}
	return credential[:index], credential[index+1:], true
}

func abortUnauthorized(c *gin.Context, reason string) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error": "unauthorized",
		"msg":   reason,
	})
}
