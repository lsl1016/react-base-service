package mcpclient

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"react-base-service/conf"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// stderrWriter 把子进程 stderr 透传到服务日志。
type stderrWriter struct{ server string }

func (w stderrWriter) Write(p []byte) (int, error) {
	text := strings.TrimRight(string(p), "\r\n")
	if text != "" {
		zlog.Infof(nil, "[MCP.%s] %s", w.server, text)
	}
	return len(p), nil
}

// RegistryTool 是 tools/list 拿到的一个 MCP 工具定义。
type RegistryTool struct {
	Server      string
	Tool        string
	Description string
	InputSchema map[string]any
	// OutputSchema 是服务器声明的结构化输出 schema（MCP 2025-06-18 起可选），无则为 nil。
	OutputSchema map[string]any
}

// ToolConfigJSON 生成写入 tblLlmTool.config 的 JSON（tool_type=mcp）。
// inputSchema 供 get_tool 校验与参数透出；outputSchema（如有）透出给模型理解返回结构；
// mcpServer/mcpTool 供执行分发。
func ToolConfigJSON(server, tool string, inputSchema, outputSchema map[string]any) (string, error) {
	payload := map[string]any{
		"mcpServer":   server,
		"mcpTool":     tool,
		"inputSchema": orEmptyObject(inputSchema),
	}
	if len(outputSchema) > 0 {
		payload["outputSchema"] = outputSchema
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func orEmptyObject(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return schema
}

// Server 是一个 MCP 服务器的统一抽象：stdio（kind=repo）与 HTTP（kind=http）实现同一接口，
// 注册表同步与 execute_tool 分发对传输形式无感知。
type Server interface {
	Name() string
	Start() error
	Stop() error
	ListTools() ([]RegistryTool, error)
	CallTool(name string, arguments json.RawMessage, timeout time.Duration) (string, error)
}

// Manager 管理全部配置声明的 MCP 服务器客户端。
type Manager struct {
	mu      sync.RWMutex
	clients map[string]Server
}

var defaultManager = &Manager{clients: map[string]Server{}}

// Default 返回全局 Manager。
func Default() *Manager { return defaultManager }

// Bootstrap 按配置拉起全部 MCP 服务器并同步工具注册表。
// 任一服务器失败只记录日志，不影响服务启动与其余服务器。
func Bootstrap(engine *gin.Engine) {
	mcpConf := conf.CustomConf.MCP
	if strings.TrimSpace(mcpConf.CallerKey) == "" || len(mcpConf.Servers) == 0 {
		zlog.Infof(nil, "[MCP] 未配置 mcp.servers，跳过 MCP 客户端启动")
	}

	for _, serverCfg := range mcpConf.Servers {
		client, err := newServer(ConfServerConfig(serverCfg))
		if err != nil {
			zlog.Errorf(nil, "[MCP] 服务器配置无效: %v", err)
			continue
		}
		if err := client.Start(); err != nil {
			zlog.Errorf(nil, "[MCP] 启动 %s 失败: %v", serverCfg.Name, err)
			continue
		}
		defaultManager.mu.Lock()
		defaultManager.clients[client.Name()] = client
		defaultManager.mu.Unlock()
		zlog.Infof(nil, "[MCP] 服务器 %s（kind=%s）已启动", serverCfg.Name, serverCfg.Kind)
	}

	if strings.TrimSpace(mcpConf.CallerKey) != "" && len(mcpConf.Servers) > 0 {
		if err := SyncRegistry(mcpConf.CallerKey); err != nil {
			zlog.Errorf(nil, "[MCP] 工具注册表同步失败: %v", err)
		}
	}

	// 继续拉起管理接口登记的 MCP 连接（tblLlmMcpServer，跨 caller）。
	bootstrapDBServers()
}

// bootstrapDBServers 启动时按注册表记录拉起数据库登记的 MCP 连接。
// 单条失败不影响其余连接；失败连接的全部注册工具标记停用并把失败原因写回 last_check_*，
// 与「测试连接」/「刷新」的失败语义一致（失联服务器的工具不进入会话工具索引）。
func bootstrapDBServers() {
	if helpers.MysqlClientLLM == nil {
		return
	}
	ctx := &gin.Context{}
	servers, err := model.ListEnabledMcpServers(ctx)
	if err != nil {
		zlog.Errorf(nil, "[MCP] 读取连接注册表失败: %v", err)
		return
	}
	for _, server := range servers {
		if ok, message := ConnectServer(ctx, server); !ok {
			zlog.Errorf(nil, "[MCP] 启动注册表连接 %s 失败: %s", server.Name, message)
		}
	}
	if len(servers) > 0 {
		zlog.Infof(nil, "[MCP] 注册表连接启动完成: %d 个", len(servers))
	}
}

// McpServerConfig 把注册表记录转成客户端拉起配置（headers/env 为 JSON 字符串）。
func McpServerConfig(server model.McpServer) (*ServerConfig, error) {
	cfg := ServerConfig{
		Name:         server.Name,
		Kind:         server.Kind,
		Endpoint:     server.Endpoint,
		TimeoutMs:    server.TimeoutMs,
		AllowPrivate: conf.CustomConf.MCP.AllowPrivateEndpoint,
	}
	if strings.TrimSpace(server.Headers) != "" {
		if err := json.Unmarshal([]byte(server.Headers), &cfg.Headers); err != nil {
			return nil, fmt.Errorf("headers 非法 JSON: %w", err)
		}
	}
	if strings.TrimSpace(server.Env) != "" {
		if err := json.Unmarshal([]byte(server.Env), &cfg.Env); err != nil {
			return nil, fmt.Errorf("env 非法 JSON: %w", err)
		}
	}
	return &cfg, nil
}

// ConfServerConfig 把 yaml 静态声明转成客户端拉起配置。
func ConfServerConfig(serverCfg conf.MCPServerConf) ServerConfig {
	return ServerConfig{
		Name:         serverCfg.Name,
		Kind:         serverCfg.Kind,
		Env:          serverCfg.Env,
		Endpoint:     serverCfg.Endpoint,
		TimeoutMs:    serverCfg.TimeoutMs,
		Headers:      serverCfg.Headers,
		AllowPrivate: conf.CustomConf.MCP.AllowPrivateEndpoint,
	}
}

// last_check_status 取值：connected=最近一次连接成功；disconnected=最近一次失败；unknown=尚未检测。
const (
	CheckStatusConnected    = "connected"
	CheckStatusDisconnected = "disconnected"
	CheckStatusUnknown      = "unknown"
)

// ConnectionCallers 返回连接工具同步到的全部 caller：属主在首位，其后为绑定 caller（去重）。
// 绑定 caller 不存在/已停用（且非保留作用域）由 ConnectServer 逐个跳过。
func ConnectionCallers(ctx *gin.Context, server *model.McpServer) []string {
	callers := []string{server.CallerKey}
	bound, err := model.ListMcpServerCallers(ctx, server.ServerID)
	if err != nil {
		zlog.Errorf(ctx, "[MCP] 查询连接绑定 caller 失败: serverId=%s err=%v", server.ServerID, err)
		return callers
	}
	seen := map[string]bool{server.CallerKey: true}
	for _, key := range bound {
		if seen[key] {
			continue
		}
		seen[key] = true
		callers = append(callers, key)
	}
	return callers
}

// OfflineServerTools 把一个服务器同步进注册表的全部工具标记停用（全部 caller 副本），
// 供连接检测失败时下线，确保失联服务器的工具不再进入会话工具索引（FindToolsByCallerAndRoutes
// 只取 status=1）。返回停用行数。
func OfflineServerTools(ctx *gin.Context, serverName string) (int64, error) {
	return model.OfflineMCPServerTools(ctx, serverName, "", nil)
}

// ConnectServer 对一条注册表连接执行完整连接流程：EnsureServer 拉起（或同名替换）→
// 把 tools/list 结果同步到属主与全部绑定 caller 名下（更新描述与入参/出参 schema、恢复
// 停用行、下线服务器端已下架的工具）→ 结果写回 last_check_*。
// 任一步失败即整体判失联：该服务器全部注册工具标记停用（status=0，不软删，恢复连接后
// 下次同步自动恢复），避免「服务器连不上但工具仍进会话」。
// 返回（是否连接成功, 展示消息：成功提示或失败原因）。
func ConnectServer(ctx *gin.Context, server model.McpServer) (bool, string) {
	fail := func(err error) (bool, string) {
		if offlined, offErr := OfflineServerTools(ctx, server.Name); offErr != nil {
			zlog.Errorf(ctx, "[MCP] 失联下线 %s 注册工具失败: %v", server.Name, offErr)
		} else if offlined > 0 {
			zlog.Infof(ctx, "[MCP] 连接 %s 失败，已下线其注册工具 %d 个", server.Name, offlined)
		}
		message := err.Error()
		if len(message) > 500 {
			message = message[:500]
		}
		recordCheckResult(ctx, server.ServerID, CheckStatusDisconnected, message)
		return false, message
	}
	cfg, err := McpServerConfig(server)
	if err != nil {
		return fail(err)
	}
	if _, err := EnsureServer(*cfg); err != nil {
		return fail(err)
	}
	// 属主用历史 toolId 格式，绑定 caller 用带后缀的副本；绑定 caller 不存在/停用则静默跳过。
	for i, callerKey := range ConnectionCallers(ctx, &server) {
		if i > 0 && !model.IsReservedCallerKey(callerKey) {
			caller, err := model.GetActiveCallerByKey(ctx, callerKey)
			if err != nil || caller == nil {
				continue
			}
		}
		if _, err := SyncServerRegistryScoped(callerKey, server.Name, i == 0); err != nil {
			return fail(err)
		}
	}
	message := "连接成功，工具清单已同步"
	recordCheckResult(ctx, server.ServerID, CheckStatusConnected, message)
	return true, message
}

// recordCheckResult 把连接检测结果写回 tblLlmMcpServer.last_check_*（消息截 500 字）。
func recordCheckResult(ctx *gin.Context, serverID, status, message string) {
	if len(message) > 500 {
		message = message[:500]
	}
	if err := model.UpdateMcpServerByServerID(ctx, serverID, map[string]interface{}{
		"last_check_status":  status,
		"last_check_message": message,
		"last_check_at":      time.Now(),
	}); err != nil {
		zlog.Errorf(ctx, "[MCP] 写回连接检测结果失败: serverId=%s err=%v", serverID, err)
	}
}

// EnsureServer 按配置拉起（或替换）一个 MCP 服务器客户端并注册进 Manager。
// 供管理接口动态新增/更新连接使用；返回客户端句柄供后续 tools/list。
func EnsureServer(cfg ServerConfig) (Server, error) {
	client, err := newServer(cfg)
	if err != nil {
		return nil, err
	}
	if err := client.Start(); err != nil {
		return nil, err
	}
	defaultManager.mu.Lock()
	old := defaultManager.clients[client.Name()]
	defaultManager.clients[client.Name()] = client
	defaultManager.mu.Unlock()
	if old != nil {
		_ = old.Stop()
	}
	return client, nil
}

// RemoveServer 停止并移除一个已注册的服务器客户端；不存在时为空操作。
func RemoveServer(name string) error {
	defaultManager.mu.Lock()
	client := defaultManager.clients[name]
	delete(defaultManager.clients, name)
	defaultManager.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Stop()
}

// GetServer 返回当前运行的服务器客户端；未运行返回 nil。
func GetServer(name string) Server {
	defaultManager.mu.RLock()
	defer defaultManager.mu.RUnlock()
	return defaultManager.clients[name]
}

// newServer 按 kind 构造对应传输的客户端：
// http=手写 Streamable HTTP；http_sdk=官方 MCP Go SDK 版；其余走 stdio 适配器白名单。
func newServer(cfg ServerConfig) (Server, error) {
	kind := strings.ToLower(strings.TrimSpace(cfg.Kind))
	switch kind {
	case "http":
		return NewHTTPClientOpts(strings.TrimSpace(cfg.Name), cfg.Endpoint, cfg.TimeoutMs, cfg.Headers, cfg.AllowPrivate)
	case "http_sdk", "sdk":
		return NewSDKClient(strings.TrimSpace(cfg.Name), cfg.Endpoint, cfg.TimeoutMs)
	default:
		return NewClient(cfg)
	}
}

// Shutdown 停止全部服务器（stdio 子进程 / HTTP 客户端）。
func Shutdown() {
	defaultManager.mu.Lock()
	defer defaultManager.mu.Unlock()
	for name, client := range defaultManager.clients {
		if err := client.Stop(); err != nil {
			zlog.Errorf(nil, "[MCP] 停止 %s 失败: %v", name, err)
		}
	}
	defaultManager.clients = map[string]Server{}
}

// Call 经全局 Manager 调用某服务器上的工具。
func Call(server, tool string, arguments json.RawMessage, timeout time.Duration) (string, error) {
	defaultManager.mu.RLock()
	client := defaultManager.clients[server]
	defaultManager.mu.RUnlock()
	if client == nil {
		return "", fmt.Errorf("mcp server %q is not configured/running", server)
	}
	return client.CallTool(tool, arguments, timeout)
}

// SyncRegistry 把全部 MCP 服务器的工具清单 upsert 进 tblLlmTool。
// toolId 固定为 mcp_<server>_<tool>，按 toolId 增量更新；routeValues 为 "[]"（空路由通用）。
func SyncRegistry(callerKey string) error {
	defaultManager.mu.RLock()
	clients := make([]Server, 0, len(defaultManager.clients))
	for _, client := range defaultManager.clients {
		clients = append(clients, client)
	}
	defaultManager.mu.RUnlock()

	ctx := &gin.Context{}
	synced := 0
	for _, client := range clients {
		tools, err := client.ListTools()
		if err != nil {
			zlog.Errorf(nil, "[MCP] %s tools/list 失败: %v", client.Name(), err)
			continue
		}
		for _, tool := range tools {
			if err := upsertRegistryTool(ctx, callerKey, client.Name(), tool, true); err != nil {
				zlog.Errorf(nil, "[MCP] 注册工具 %s_%s 失败: %v", client.Name(), tool.Tool, err)
				continue
			}
			synced++
		}
		offlineStaleTools(ctx, callerKey, client.Name(), tools)
	}
	zlog.Infof(nil, "[MCP] 注册表同步完成: callerKey=%s, 共 %d 个 MCP 工具", callerKey, synced)
	return nil
}

// offlineStaleTools 把该 caller 名下、不在服务器当前清单内的注册工具标记停用
// （服务器端已下架/改名；status=0，不软删，重新出现在清单时下次同步自动恢复）。
func offlineStaleTools(ctx *gin.Context, callerKey, serverName string, tools []RegistryTool) {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, fmt.Sprintf("%s_%s", serverName, tool.Tool))
	}
	offlined, err := model.OfflineMCPServerTools(ctx, serverName, callerKey, names)
	if err != nil {
		zlog.Errorf(ctx, "[MCP] 清理 %s 在 caller %s 名下失效工具失败: %v", serverName, callerKey, err)
		return
	}
	if offlined > 0 {
		zlog.Infof(ctx, "[MCP] %s 在 caller %s 名下 %d 个服务器端已下架工具标记停用", serverName, callerKey, offlined)
	}
}

// SyncServerRegistry 同步单个服务器的工具清单进 tblLlmTool（属主 caller，toolId 历史格式），返回同步的工具数。
// 服务器不在运行（未拉起）时返回错误，由调用方决定是否标记连接异常。
func SyncServerRegistry(callerKey, serverName string) (int, error) {
	return SyncServerRegistryScoped(callerKey, serverName, true)
}

// SyncServerRegistryScoped 把服务器工具同步到指定 caller 名下：
// primary=true（属主）toolId=mcp_<server>_<tool>；primary=false（绑定 caller）追加 `__<callerKey>`
// 后缀避开全局唯一键，工具名不变（名字按 caller 内唯一约束）。
func SyncServerRegistryScoped(callerKey, serverName string, primary bool) (int, error) {
	client := GetServer(serverName)
	if client == nil {
		return 0, fmt.Errorf("mcp server %q is not running", serverName)
	}
	tools, err := client.ListTools()
	if err != nil {
		return 0, err
	}
	ctx := &gin.Context{}
	for _, tool := range tools {
		if err := upsertRegistryTool(ctx, callerKey, client.Name(), tool, primary); err != nil {
			zlog.Errorf(nil, "[MCP] 注册工具 %s_%s 失败: %v", client.Name(), tool.Tool, err)
			continue
		}
	}
	offlineStaleTools(ctx, callerKey, client.Name(), tools)
	return len(tools), nil
}

// RemoveRegistryTools 软删除一个服务器同步进注册表的全部工具（config.mcpServer=serverName，
// 含属主与各绑定 caller 名下的副本）。供连接删除/停用时清理注册表；未匹配任何工具不是错误。
func RemoveRegistryTools(ctx *gin.Context, serverName string) (int, error) {
	return RemoveRegistryToolsForCaller(ctx, serverName, "")
}

// RemoveRegistryToolsForCaller 软删除服务器在某一个 caller 名下的工具副本；
// callerKey 为空表示全部 caller（连接删除/停用），非空用于解绑单个 caller。
func RemoveRegistryToolsForCaller(ctx *gin.Context, serverName, callerKey string) (int, error) {
	tools, err := model.ListToolsByType(ctx, "mcp")
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, tool := range tools {
		var cfg struct {
			MCPServer string `json:"mcpServer"`
		}
		if err := json.Unmarshal([]byte(tool.Config), &cfg); err != nil || cfg.MCPServer != serverName {
			continue
		}
		if callerKey != "" && tool.CallerKey != callerKey {
			continue
		}
		if err := model.SoftDeleteToolByToolID(ctx, tool.ToolID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func upsertRegistryTool(ctx *gin.Context, callerKey, server string, tool RegistryTool, primary bool) error {
	toolID := fmt.Sprintf("mcp_%s_%s", server, tool.Tool)
	if !primary {
		toolID = fmt.Sprintf("%s__%s", toolID, callerKey)
	}
	name := fmt.Sprintf("%s_%s", server, tool.Tool)
	description := strings.TrimSpace(tool.Description)
	if description == "" {
		description = fmt.Sprintf("MCP tool %s/%s", server, tool.Tool)
	}
	configJSON, err := ToolConfigJSON(server, tool.Tool, tool.InputSchema, tool.OutputSchema)
	if err != nil {
		return err
	}

	existing, err := model.GetToolByToolID(ctx, toolID)
	if err != nil {
		return err
	}
	if existing != nil {
		updates := map[string]interface{}{
			"name":        name,
			"description": description,
			"config":      configJSON,
			"status":      1,
			"updated_by":  "mcp-sync",
		}
		return model.UpdateToolByToolID(ctx, toolID, updates)
	}
	// 活跃记录不存在时再看软删除行：唯一键仍被占用，只能更新恢复（清除 deleted_at）。
	deleted, err := model.GetToolByToolIDUnscoped(ctx, toolID)
	if err != nil {
		return err
	}
	if deleted != nil {
		updates := map[string]interface{}{
			"name":        name,
			"description": description,
			"config":      configJSON,
			"status":      1,
			"updated_by":  "mcp-sync",
			"deleted_at":  0,
		}
		return model.UpdateToolByToolIDUnscoped(ctx, toolID, updates)
	}
	return model.CreateTool(ctx, &model.Tool{
		ToolID:      toolID,
		Name:        name,
		Description: description,
		ToolType:    "mcp",
		CallerKey:   callerKey,
		RouteValues: "[]",
		Config:      configJSON,
		Status:      1,
		CreatedBy:   "mcp-sync",
		UpdatedBy:   "mcp-sync",
	})
}
