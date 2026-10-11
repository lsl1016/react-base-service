// Package graphmemory 是 Graphiti 时序事实图谱（Layer2 长期记忆）的接入层：
// 一个指向 Graphiti FastAPI 服务（graphiti/server/graph_service）的瘦 HTTP 客户端，
// 外加基座作用域（app / app_user）到 Graphiti group_id 的映射。
//
// 只依赖官方 REST 契约（POST /search、POST /messages、GET /healthcheck），
// 不镜像图数据、不直连图数据库；检索与写入的语义见 docs/graphiti学习.md。
package graphmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config 描述 Graphiti REST 服务的连接参数（由 conf 层填好默认值后传入）。
type Config struct {
	// Endpoint 是 Graphiti 服务地址，形如 http://graphiti:8000（不带尾斜杠）。
	Endpoint string
	// TimeoutMs 是单次请求超时。
	TimeoutMs int
}

// Client 是 Graphiti REST 客户端；无状态、可并发复用。
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient 构造客户端；endpoint 去尾斜杠、timeout 缺省 15s。
func NewClient(cfg Config) *Client {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{
		cfg:  Config{Endpoint: endpoint, TimeoutMs: int(timeout.Milliseconds())},
		http: &http.Client{Timeout: timeout},
	}
}

// Endpoint 返回规范化后的服务地址。
func (c *Client) Endpoint() string {
	return c.cfg.Endpoint
}

// Fact 对应 Graphiti dto.FactResult：一条带时间窗的事实（EntityEdge 投影）。
type Fact struct {
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Fact     string `json:"fact"`
	ValidAt  string `json:"valid_at"`
	InvalidAt string `json:"invalid_at"`
	CreatedAt string `json:"created_at"`
	ExpiredAt string `json:"expired_at"`
	Episodes []string `json:"episodes"`
}

type searchRequest struct {
	GroupIDs []string `json:"group_ids"`
	Query    string   `json:"query"`
	MaxFacts int      `json:"max_facts"`
}

type searchResponse struct {
	Facts []Fact `json:"facts"`
}

// Search 按组检索相关事实（POST /search）。groupIDs 为空时 Graphiti 会退回默认组，
// 调用方（引擎侧）必须先解析好作用域再进来，避免误读默认组数据。
func (c *Client) Search(ctx context.Context, groupIDs []string, query string, maxFacts int) ([]Fact, error) {
	var resp searchResponse
	if err := c.do(ctx, http.MethodPost, "/search", searchRequest{
		GroupIDs: groupIDs,
		Query:    query,
		MaxFacts: maxFacts,
	}, &resp); err != nil {
		return nil, err
	}
	return resp.Facts, nil
}

// EpisodeMessage 对应 Graphiti dto.Message 的写入子集。
type EpisodeMessage struct {
	Content           string    `json:"content"`
	Name              string    `json:"name"`
	RoleType          string    `json:"role_type"`
	Role              string    `json:"role,omitempty"`
	Timestamp         time.Time `json:"timestamp"`
	SourceDescription string    `json:"source_description"`
}

type addMessagesRequest struct {
	GroupID  string          `json:"group_id"`
	Messages []EpisodeMessage `json:"messages"`
}

type resultResponse struct {
	Message string `json:"message"`
	Success bool   `json:"success"`
}

// AddEpisode 把一段事件写入图谱（POST /messages）。Graphiti 受理即返回 202，
// 实际抽取（实体识别/关系抽取/去重/失效判定）由服务端队列异步执行，通常数秒到数十秒后可检索到。
func (c *Client) AddEpisode(ctx context.Context, groupID string, msg EpisodeMessage) error {
	var resp resultResponse
	if err := c.do(ctx, http.MethodPost, "/messages", addMessagesRequest{
		GroupID:  groupID,
		Messages: []EpisodeMessage{msg},
	}, &resp); err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("graphiti rejected episode: %s", resp.Message)
	}
	return nil
}

// Healthcheck 探活（GET /healthcheck）。
func (c *Client) Healthcheck(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthcheck", nil, nil)
}

// maxErrorBodyBytes 限制错误响应体读取量，避免异常服务返回大页撑爆日志。
const maxErrorBodyBytes = 512

// do 统一请求：JSON 序列化、状态码检查、响应反序列化。
func (c *Client) do(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	if c.cfg.Endpoint == "" {
		return fmt.Errorf("graphmemory endpoint is not configured")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("graphmemory marshal request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.Endpoint+path, reader)
	if err != nil {
		return fmt.Errorf("graphmemory build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("graphmemory request %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return fmt.Errorf("graphmemory %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("graphmemory decode %s response: %w", path, err)
	}
	return nil
}
