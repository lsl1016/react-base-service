package mcpgateway

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"react-base-service/golib/zlog"

	"github.com/gin-gonic/gin"
)

// Bootstrap 在 engine 上注册 MCP 网关端点（basePath + GET/POST/DELETE）并启动审计
// 异步落库。主服务（/react-base-service/mcp）与独立网关二进制（cmd/mcp-gateway，/mcp）
// 共用本入口。停机时调用 CloseAuditWriter 排空审计队列。
func Bootstrap(engine *gin.Engine, basePath string) error {
	group := engine.Group(basePath)
	group.GET("", Auth, Handler())
	group.POST("", Auth, Handler())
	group.DELETE("", Auth, Handler())
	if err := StartAuditWriter(); err != nil {
		return fmt.Errorf("启动 MCP 网关审计落库失败: %w", err)
	}
	zlog.Infof(nil, "[MCPGW] 网关端点已挂载: %s", basePath)
	return nil
}

// Shutdown 停止审计写库并排空队列（主服务 StopTasks 与 cmd 停机时调用）。
func Shutdown() {
	CloseAuditWriter()
}

// NewMcpAppID 生成应用唯一标识。
func NewMcpAppID() string {
	return fmt.Sprintf("mcpapp_%s_%x", time.Now().Format("20060102150405"), randomHex(4))
}

// GenerateAppKey 生成接入凭证 key（Bearer 用户名）。
func GenerateAppKey() string {
	return "mcp_" + randomHex(10)
}

// GenerateAppSecret 生成接入凭证 secret（仅创建/重置时完整展示一次）。
func GenerateAppSecret() string {
	return randomHex(24)
}

// MaskSecret 列表/详情展示用打码（保留前 4 位便于人肉比对）。
func MaskSecret(secret string) string {
	if len(secret) <= 8 {
		return "****"
	}
	return secret[:4] + "****"
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand 失败极罕见（系统熵源不可用），以时间兜底保证可用性
		return fmt.Sprintf("%x", time.Now().UnixNano())[:bytes*2]
	}
	return hex.EncodeToString(buffer)
}
