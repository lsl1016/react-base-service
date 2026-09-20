// mcp-gateway 是 MCP 服务端网关的独立启动器：只加载配置/日志/MySQL 并挂载 /mcp
// 端点，不启动 ReAct 运行时与其余业务路由，适合把网关单独部署成一个进程。
//
// 与主服务共享同一个 llm 库：工具来自 tblLlmTool（http 类型、启用行），
// 凭证来自 tblLlmMcpApp；调用审计写 tblLlmMcpCallLog。
//
// 启动：
//
//	go run ./cmd/mcp-gateway                 # 默认监听 :8090
//	MCP_GATEWAY_ADDR=:9000 go run ./cmd/mcp-gateway
//	go run ./cmd/mcp-gateway -addr :9000
//
// 接入（外部 MCP 客户端）：URL http://<host>:<addr>/mcp，
// Authorization: Bearer <app_key>:<app_secret>（凭证在主服务 playground「MCP 应用」页创建）。
package main

import (
	"flag"
	"os"

	golibhttp "react-base-service/golib/server/http"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	"react-base-service/service/mcpgateway"

	"github.com/gin-gonic/gin"
)

func main() {
	addr := flag.String("addr", "", "监听地址（默认 :8090，环境变量 MCP_GATEWAY_ADDR 可覆盖）")
	flag.Parse()
	if *addr == "" {
		*addr = os.Getenv("MCP_GATEWAY_ADDR")
	}
	if *addr == "" {
		*addr = ":8090"
	}

	helpers.PreInit()
	defer helpers.Clear()
	helpers.InitMysql()
	if helpers.MysqlClientLLM == nil {
		zlog.Errorf(nil, "[MCPGW] MySQL(llm) 未初始化，检查 resource.yaml 的 mysql.llm 配置")
		os.Exit(1)
	}

	engine := gin.New()
	engine.Use(gin.Recovery())
	if err := mcpgateway.Bootstrap(engine, "/mcp"); err != nil {
		zlog.Errorf(nil, "[MCPGW] 网关启动失败: %v", err)
		os.Exit(1)
	}
	defer mcpgateway.Shutdown()

	zlog.Infof(nil, "[MCPGW] MCP 网关独立进程启动: %s/mcp", *addr)
	if err := golibhttp.Start(engine, golibhttp.ServerConfig{Address: *addr}); err != nil {
		zlog.Errorf(nil, "[MCPGW] HTTP 服务退出: %v", err)
		os.Exit(1)
	}
}
