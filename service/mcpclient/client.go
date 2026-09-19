// Package mcpclient 实现 MCP stdio 客户端：把配置声明的 MCP 服务器作为子进程拉起，
// 完成 initialize 握手后代理 tools/list 与 tools/call，并把工具清单同步进
// tblLlmTool 注册表（tool_type=mcp），从而复用 ReAct 运行时的两段式工具加载
//（get_tool → execute_tool）。传输与注册均无鉴权，仅限本机受信环境。
//
// 安全模型：可执行文件走代码内适配器白名单（kind → 字面量命令，按 GOOS 固定路径），
// 配置只能选择 kind 与注入环境变量，不接收任意 command/args；子进程以参数列表启动，
// 不经过 shell 解析。
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

// Client 管理单个 MCP 服务器子进程的连接。请求串行化（互斥锁内 写请求+读响应），
// 与 stdio 服务器的单线程处理模型一致。
type Client struct {
	name string
	kind string
	env  []string

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int
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
	c := &Client{name: name, kind: strings.TrimSpace(cfg.Kind)}
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
	return nil
}

// ensureStartedLocked 保证子进程与握手已完成。
func (c *Client) ensureStartedLocked() error {
	if c.cmd != nil {
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
func (c *Client) CallTool(name string, arguments json.RawMessage, timeout time.Duration) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil {
		return "", fmt.Errorf("mcp server %s is not running", c.name)
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

func (c *Client) roundTripLockedTimeout(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	if err := c.writeLocked(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}

	type readResult struct {
		resp rpcResponse
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		for {
			line, err := c.stdout.ReadString('\n')
			if err != nil {
				done <- readResult{err: err}
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
			if resp.ID != id {
				continue
			}
			done <- readResult{resp: resp}
			return
		}
	}()
	// 超时后读协程可能仍阻塞在 ReadString 上，关闭连接使其退出。
	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		if r.resp.Error != nil {
			return nil, fmt.Errorf("jsonrpc error %d: %s", r.resp.Error.Code, r.resp.Error.Message)
		}
		return r.resp.Result, nil
	case <-time.After(timeout):
		_ = c.stopLocked()
		return nil, fmt.Errorf("timeout after %s", timeout)
	}
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
