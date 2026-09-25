// Package mcpclient 实现 MCP stdio 客户端：把配置声明的 MCP 服务器作为子进程拉起，
// 完成 initialize 握手后代理 tools/list 与 tools/call，并把工具清单同步进
// tblLlmTool 注册表（tool_type=mcp），从而复用 ReAct 运行时的两段式工具加载
//（get_tool → execute_tool）。传输与注册均无鉴权，仅限本机受信环境。
//
// 安全模型：可执行文件走代码内适配器白名单（kind → 字面量命令，按 GOOS 固定路径），
// 配置只能选择 kind 与注入环境变量，不接收任意 command/args；子进程以参数列表启动，
// 不经过 shell 解析。
//
// 超时语义（终止边界治理 Phase 4）：单次请求超时不再杀掉子进程——pump 按请求 id 分发
// 响应，超时请求只放弃自己的 future，迟到响应被丢弃；仅当连续多次请求超时（判定子进程
// 整体卡死）才重启。避免"慢工具反复拉起子进程 + 重新握手"的放大效应。
package mcpclient

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"react-base-service/golib/zlog"
)

// ServerConfig 描述一个 MCP 服务器的拉起配置：kind=repo 走 stdio 适配器白名单，
// kind=http 走 Streamable HTTP（Endpoint 必填，经 SSRF 校验）。
type ServerConfig struct {
	Name      string            `yaml:"name"`
	Kind      string            `yaml:"kind"`
	Env       map[string]string `yaml:"env"`
	Endpoint  string            `yaml:"endpoint"`
	TimeoutMs int               `yaml:"timeout_ms"`
	// Headers 附加到每个 HTTP 请求（如 Authorization），仅 kind=http 支持。
	Headers map[string]string `yaml:"headers"`
	// AllowPrivate 允许环回/私网端点（本机开发环境经 mcp.allow_private_endpoint 开启）。
	AllowPrivate bool `yaml:"-"`
}

// adapter 是白名单中一个可拉起的 MCP 适配器；command 为固定字面量路径。
type adapter struct {
	description string
}

// adapterWhitelist 声明可选的适配器集合。可执行文件约定部署在仓库 bin/ 目录下。
var adapterWhitelist = map[string]adapter{
	"repo": {description: "只读本地仓库适配器（cmd/repo-mcp 构建，env REPO_ROOT 指定仓库根）"},
}

// spawnCommand 按适配器白名单返回该平台下的固定可执行路径（字面量，不经 shell）。
func spawnCommand(kind string) (string, error) {
	if _, ok := adapterWhitelist[kind]; !ok {
		return "", fmt.Errorf("unknown mcp server kind %q (whitelist: %v)", kind, adapterNames())
	}
	if runtime.GOOS == "windows" {
		return `bin\repo-mcp.exe`, nil
	}
	return "bin/repo-mcp", nil
}

func adapterNames() []string {
	names := make([]string, 0, len(adapterWhitelist))
	for name := range adapterWhitelist {
		names = append(names, name)
	}
	return names
}

// validateServerName 校验服务器名（用于注册表 toolId 前缀）。
func validateServerName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > 32 {
		return fmt.Errorf("mcp server name must be 1-32 chars")
	}
	for _, char := range trimmed {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return fmt.Errorf("mcp server name only allows letters/digits/_/-")
	}
	return nil
}

// stdioMaxConsecutiveTimeouts 是连续请求超时多少次后判定子进程整体卡死并重启；
// 单次超时保留进程（pump 会丢弃迟到响应，不会串包）。
const stdioMaxConsecutiveTimeouts = 3

// stdioRPCResult 是一次 JSON-RPC 响应或读通道错误的投递载体。
type stdioRPCResult struct {
	resp rpcResponse
	err  error
}

// Client 管理单个 MCP 服务器子进程的连接。
// 请求仍串行化（mu 内 写请求+等待响应），与 stdio 服务器的单线程处理模型一致；
// stdout 由唯一 pump goroutine 读取并按 JSON-RPC id 分发，杜绝多请求竞争读缓冲。
type Client struct {
	name string
	kind string
	env  []string

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader // 仅 pump goroutine 读取
	nextID int

	pendingMu sync.Mutex
	pending   map[int]chan stdioRPCResult
	// pumpGeneration 每次（重）启动 +1：旧 pump 退出时凭代数判断是否由自己清理状态，
	// 防止重启后旧 pump 的 EOF 错误清掉新代请求。
	pumpGeneration int
	// readBroken 标记 pump 已因读错误退出：下一次请求 ensureStarted 会重启子进程。
	readBroken bool
	// consecutiveTimeouts 连续请求超时计数；达到 stdioMaxConsecutiveTimeouts 后重启子进程。
	consecutiveTimeouts int
}

// NewClient 按配置构造客户端（未启动）。
func NewClient(cfg ServerConfig) (*Client, error) {
	name := strings.TrimSpace(cfg.Name)
	if err := validateServerName(name); err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", cfg.Name, err)
	}
	if _, err := spawnCommand(strings.TrimSpace(cfg.Kind)); err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", name, err)
	}
	c := &Client{
		name:    name,
		kind:    strings.TrimSpace(cfg.Kind),
		pending: make(map[int]chan stdioRPCResult),
	}
	for key, value := range cfg.Env {
		c.env = append(c.env, key+"="+value)
	}
	return c, nil
}

// Name 返回服务器名。
func (c *Client) Name() string { return c.name }

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Start 拉起子进程并完成 initialize 握手；重复调用会先停掉旧进程。
func (c *Client) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startLocked()
}

func (c *Client) startLocked() error {
	if err := c.stopLocked(); err != nil {
		return err
	}

	if _, err := spawnCommand(c.kind); err != nil {
		return err
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command(`bin\repo-mcp.exe`)
	} else {
		cmd = exec.Command("bin/repo-mcp")
	}
	cmd.Env = append(os.Environ(), c.env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp %s stdin pipe: %w", c.name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcp %s stdout pipe: %w", c.name, err)
	}
	// stderr 直接透传到服务日志，便于排查子进程问题。
	cmd.Stderr = stderrWriter{server: c.name}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp %s start: %w", c.name, err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = bufio.NewReaderSize(stdout, 2<<20)
	c.readBroken = false
	c.consecutiveTimeouts = 0
	c.pumpGeneration++
	go c.pump(c.pumpGeneration)

	result, err := c.roundTripLocked("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"clientInfo":      map[string]any{"name": "react-base-service", "version": "1.0"},
	})
	if err != nil {
		_ = c.stopLocked()
		return fmt.Errorf("mcp %s initialize: %w", c.name, err)
	}
	var info struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	_ = json.Unmarshal(result, &info)
	// initialized 通知（无 id，不等待响应）。
	_ = c.writeLocked(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	return nil
}

// Stop 终止子进程。
func (c *Client) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopLocked()
}

func (c *Client) stopLocked() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
	c.cmd = nil
	c.stdin = nil
	c.stdout = nil
	// 重启/停止前让所有在途请求立刻失败，等待者不会悬挂到超时。
	c.failAllPending(fmt.Errorf("mcp server %s restarted or stopped", c.name))
	return nil
}

// ensureStartedLocked 保证子进程与握手已完成；pump 已断（readBroken）时同样重启。
func (c *Client) ensureStartedLocked() error {
	if c.cmd != nil && !c.readBroken {
		return nil
	}
	return c.startLocked()
}

// ListTools 返回服务器的工具清单（name/description/inputSchema/outputSchema）。
func (c *Client) ListTools() ([]RegistryTool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureStartedLocked(); err != nil {
		return nil, err
	}
	result, err := c.roundTripLocked("tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("mcp %s tools/list: %w", c.name, err)
	}
	var payload struct {
		Tools []struct {
			Name         string         `json:"name"`
			Description  string         `json:"description"`
			InputSchema  map[string]any `json:"inputSchema"`
			OutputSchema map[string]any `json:"outputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("mcp %s tools/list parse: %w", c.name, err)
	}
	tools := make([]RegistryTool, 0, len(payload.Tools))
	for _, tool := range payload.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		tools = append(tools, RegistryTool{
			Server:       c.name,
			Tool:         tool.Name,
			Description:  tool.Description,
			InputSchema:  tool.InputSchema,
			OutputSchema: tool.OutputSchema,
		})
	}
	return tools, nil
}

// CallTool 调用服务器上的一个工具，返回 text 内容。
// 单次超时只放弃本请求（子进程保留），连续超时才重启子进程。
func (c *Client) CallTool(name string, arguments json.RawMessage, timeout time.Duration) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureStartedLocked(); err != nil {
		return "", err
	}
	var args any
	if len(arguments) > 0 {
		_ = json.Unmarshal(arguments, &args)
	}
	result, err := c.roundTripLockedTimeout("tools/call", map[string]any{"name": name, "arguments": args}, timeout)
	if err != nil {
		return "", fmt.Errorf("mcp %s tools/call %s: %w", c.name, name, err)
	}
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return string(result), nil
	}
	texts := make([]string, 0, len(payload.Content))
	for _, item := range payload.Content {
		if item.Type == "text" || item.Type == "" {
			texts = append(texts, item.Text)
		}
	}
	joined := strings.Join(texts, "\n")
	if payload.IsError {
		return joined, fmt.Errorf("%s", joined)
	}
	return joined, nil
}

// roundTripLocked 写一个请求并读取同 id 的响应（跳过通知行）。
func (c *Client) roundTripLocked(method string, params any) (json.RawMessage, error) {
	return c.roundTripLockedTimeout(method, params, 60*time.Second)
}

// roundTripLockedTimeout 注册 pending、写请求并等待 pump 按 id 投递的响应。
// 调用方必须持有 c.mu（请求串行化）；pump 只依赖 pendingMu + channel 投递，不会死锁。
func (c *Client) roundTripLockedTimeout(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	ch := make(chan stdioRPCResult, 1)
	c.pendingMu.Lock()
	if c.readBroken {
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("mcp %s connection broken, will restart on next request", c.name)
	}
	c.pending[id] = ch
	c.pendingMu.Unlock()

	if err := c.writeLocked(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.removePending(id)
		return nil, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-ch:
		return c.settleRoundTripResult(id, result)
	case <-timer.C:
		// 响应可能在超时判定瞬间已投递：先抢一次，抢不到才按超时收敛。
		select {
		case result := <-ch:
			return c.settleRoundTripResult(id, result)
		default:
		}
		c.removePending(id)
		c.consecutiveTimeouts++
		if c.consecutiveTimeouts >= stdioMaxConsecutiveTimeouts {
			zlog.Warnf(nil, "[MCP.Stdio] 连续 %d 次请求超时，重启子进程: server=%s", c.consecutiveTimeouts, c.name)
			_ = c.stopLocked()
		}
		return nil, fmt.Errorf("timeout after %s", timeout)
	}
}

// settleRoundTripResult 结算一次成功投递的响应；成功即清零连续超时计数。
func (c *Client) settleRoundTripResult(id int, result stdioRPCResult) (json.RawMessage, error) {
	c.consecutiveTimeouts = 0
	if result.err != nil {
		return nil, result.err
	}
	if result.resp.Error != nil {
		return nil, fmt.Errorf("jsonrpc error %d: %s", result.resp.Error.Code, result.resp.Error.Message)
	}
	return result.resp.Result, nil
}

// pump 是 stdout 的唯一读取者：按 JSON-RPC id 把响应分发给等待中的请求。
// 通知（无 id）与迟到/未知 id 的响应直接丢弃；读到 EOF/错误时让全部在途请求失败
// 并标记 readBroken，下一次请求经 ensureStarted 重启子进程。
func (c *Client) pump(generation int) {
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			c.pumpBroken(generation, err)
			return
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal([]byte(trimmed), &resp); err != nil {
			continue
		}
		if resp.ID <= 0 {
			continue // 通知（notifications/*）无 id：不等待响应
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.pendingMu.Unlock()
		if !ok {
			continue // 请求方已超时离开或未知 id：丢弃迟到响应，避免串包
		}
		ch <- stdioRPCResult{resp: resp}
	}
}

// pumpBroken 在 pump 读错误退出时清理状态；代数不匹配说明已有新 pump 接管，不操作。
func (c *Client) pumpBroken(generation int, cause error) {
	c.pendingMu.Lock()
	if generation != c.pumpGeneration {
		c.pendingMu.Unlock()
		return
	}
	waiters := c.pending
	c.pending = make(map[int]chan stdioRPCResult)
	c.readBroken = true
	c.pendingMu.Unlock()
	zlog.Warnf(nil, "[MCP.Stdio] 子进程输出流中断: server=%s, err=%v", c.name, cause)
	for _, ch := range waiters {
		ch <- stdioRPCResult{err: fmt.Errorf("mcp %s output stream broken: %w", c.name, cause)}
	}
}

// failAllPending 让全部在途请求立刻失败（stop/restart 路径使用，跨代生效）。
func (c *Client) failAllPending(cause error) {
	c.pendingMu.Lock()
	waiters := c.pending
	c.pending = make(map[int]chan stdioRPCResult)
	c.pendingMu.Unlock()
	for _, ch := range waiters {
		ch <- stdioRPCResult{err: cause}
	}
}

// removePending 注销一个未完成的 pending 请求。
func (c *Client) removePending(id int) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *Client) writeLocked(req rpcRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}
