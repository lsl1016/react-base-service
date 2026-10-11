package components

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	RequestSourceDatamapKnowledgeDingding = "datamap-knowledge-dingding"

	AppRuntimeContextKey = "app-runtime-context"
)

type AppRuntimeContext struct {
	AppKey     string `json:"appKey"`
	RequestSource string `json:"requestSource,omitempty"`
	UserName      string `json:"userName,omitempty"`
	SessionID     string `json:"sessionId,omitempty"`
	UpdateMode    string `json:"updateMode,omitempty"`
}

// NormalizeAppRuntimeContext 归一化调用方运行时上下文，随 run 生命周期注入 context 与 HTTP 工具请求。
func NormalizeAppRuntimeContext(ctx *gin.Context, appKey, requestSource, userName, sessionID, updateMode string) (AppRuntimeContext, error) {
	return AppRuntimeContext{
		AppKey:     strings.TrimSpace(appKey),
		RequestSource: strings.TrimSpace(requestSource),
		UserName:      strings.TrimSpace(userName),
		SessionID:     strings.TrimSpace(sessionID),
		UpdateMode:    strings.TrimSpace(updateMode),
	}, nil
}

func ContextWithAppRuntime(parent context.Context, runtimeCtx AppRuntimeContext) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, AppRuntimeContextKey, runtimeCtx)
}

func AppRuntimeFromContext(ctx context.Context) (AppRuntimeContext, bool) {
	if ctx == nil {
		return AppRuntimeContext{}, false
	}
	if v, ok := ctx.Value(AppRuntimeContextKey).(AppRuntimeContext); ok {
		return v, true
	}
	appKey, _ := ctx.Value("skill-app-key").(string)
	requestSource, _ := ctx.Value("skill-request-source").(string)
	updateMode, _ := ctx.Value("x-skill-update-mode").(string)
	sessionID, _ := ctx.Value("x-skill-session-id").(string)
	userName, _ := ctx.Value("x-skill-user-name").(string)
	if strings.TrimSpace(appKey) == "" && strings.TrimSpace(requestSource) == "" {
		return AppRuntimeContext{}, false
	}
	return AppRuntimeContext{
		AppKey:     strings.TrimSpace(appKey),
		RequestSource: strings.TrimSpace(requestSource),
		UserName:      strings.TrimSpace(userName),
		SessionID:     strings.TrimSpace(sessionID),
		UpdateMode:    strings.TrimSpace(updateMode),
	}, true
}

// ApplyHTTPToolHeaders 将调用方运行时上下文透传给后端 HTTP Business Tool，保持调用态一致。
func ApplyHTTPToolHeaders(ctx context.Context, headers http.Header) {
	if headers == nil {
		return
	}
	runtimeCtx, ok := AppRuntimeFromContext(ctx)
	if !ok {
		return
	}
	if runtimeCtx.UpdateMode != "" {
		headers.Set("X-Update-Mode", runtimeCtx.UpdateMode)
	}
	if runtimeCtx.SessionID != "" {
		headers.Set("X-Session-Id", runtimeCtx.SessionID)
	}
	if runtimeCtx.UserName != "" {
		headers.Set("X-User-Name", runtimeCtx.UserName)
	}
}
