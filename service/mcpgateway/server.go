package mcpgateway

import (
	"net/http"

	"react-base-service/conf"
	"react-base-service/golib/zlog"

	"github.com/gin-gonic/gin"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultServerName    = "react-base-service"
	defaultServerVersion = "1.0.0"
)

// Handler 返回挂载到 Gin 的 MCP HTTP 处理器（自 mcp-server 移植）。
//
// 采用 Stateless + JSONResponse：每个 POST 自成完整请求生命周期，
// 服务端无需保存会话状态，天然支持多副本水平扩容。
func Handler() gin.HandlerFunc {
	streamable := sdk.NewStreamableHTTPHandler(func(req *http.Request) *sdk.Server {
		return buildServer(req)
	}, &sdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})

	return func(c *gin.Context) {
		// 把 *gin.Context 与调用方身份挂到 request context 上，
		// 供工具 handler 复用现有 DAO 与日志能力
		ctx := WithGinContext(c.Request.Context(), c)
		if identity, ok := IdentityFromGin(c); ok {
			ctx = WithAppIdentity(ctx, identity)
		}
		streamable.ServeHTTP(c.Writer, c.Request.WithContext(ctx))
	}
}

// buildServer 为每个请求构造 MCP Server，并按应用绑定白名单注册工具。
//
// 工具目录是动态的（tblLlmTool + tblLlmMcpAppTool）：只注册当前应用绑定的
// 启用 http 工具，白名单外的工具不会出现在 tools/list 里。
func buildServer(req *http.Request) *sdk.Server {
	server := sdk.NewServer(
		&sdk.Implementation{
			Name:    serverName(),
			Version: serverVersion(),
		},
		&sdk.ServerOptions{
			Instructions: buildInstructions(),
		},
	)
	server.AddReceivingMiddleware(ObserveMiddleware)

	ctx := req.Context()
	ginCtx, ok := GinContextFrom(ctx)
	if !ok {
		return server
	}
	identity, ok := AppIdentityFrom(ctx)
	if !ok {
		return server
	}

	bindings, err := LoadTools(ginCtx, identity.AppID)
	if err != nil {
		zlog.Errorf(ginCtx, "[MCPGW] load tools fail, app: %s, err: %s", identity.AppKey, err.Error())
		return server
	}

	for _, binding := range bindings {
		definition := buildToolDefinition(ginCtx, binding)
		if definition == nil {
			continue
		}
		server.AddTool(definition, buildToolHandler(binding))
	}
	if len(bindings) > 0 {
		zlog.Debugf(ginCtx, "[MCPGW] registered %d tools for app: %s", len(bindings), identity.AppKey)
	}
	return server
}

func serverName() string {
	if conf.CustomConf.MCPServer.Name != "" {
		return conf.CustomConf.MCPServer.Name
	}
	return defaultServerName
}

func serverVersion() string {
	if conf.CustomConf.MCPServer.Version != "" {
		return conf.CustomConf.MCPServer.Version
	}
	return defaultServerVersion
}
