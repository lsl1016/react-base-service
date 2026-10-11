package react

import (
	"strings"
	"testing"

	"react-base-service/conf"
)

func TestParseMcpServersJSON(t *testing.T) {
	// 标准 mcpServers 包装
	drafts, err := parseMcpServersJSON(`{
		"mcpServers": {
			"mcp-server": {
				"url": "http://127.0.0.1:18080/api/mcp",
				"headers": { "Authorization": "Bearer k:s" }
			}
		}
	}`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("want 1 draft, got %d", len(drafts))
	}
	if drafts[0].Name != "mcp-server" || drafts[0].Kind != "http" || drafts[0].Endpoint != "http://127.0.0.1:18080/api/mcp" {
		t.Fatalf("draft mismatch: %+v", drafts[0])
	}
	if !strings.Contains(drafts[0].HeadersJSON, "Authorization") {
		t.Fatalf("headers not serialized: %s", drafts[0].HeadersJSON)
	}

	// 去掉外层包装的单映射也接受
	drafts, err = parseMcpServersJSON(`{"solo": {"url": "https://mcp.example.com/mcp"}}`)
	if err != nil || len(drafts) != 1 || drafts[0].Name != "solo" {
		t.Fatalf("bare map parse failed: %v %+v", err, drafts)
	}

	// command/args 形式必须明确拒绝（stdio 只开放白名单适配器）
	_, err = parseMcpServersJSON(`{"mcpServers": {"bad": {"command": "npx", "args": ["-y", "x"]}}}`)
	if err == nil || !strings.Contains(err.Error(), "command/args") {
		t.Fatalf("command style should be rejected with reason, got %v", err)
	}

	// 缺 url 拒绝
	if _, err := parseMcpServersJSON(`{"mcpServers": {"bad": {}}}`); err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("missing url should be rejected, got %v", err)
	}

	// 非法 JSON 拒绝
	if _, err := parseMcpServersJSON(`not-json`); err == nil {
		t.Fatal("invalid json should be rejected")
	}
}

func TestParseMcpCreateDrafts_Structured(t *testing.T) {
	// status 未传 → 默认启用（指针语义）
	drafts, err := parseMcpCreateDrafts(mcpCreateRequest{Name: "demo", Kind: "http", Endpoint: "http://127.0.0.1:18090/mcp"})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if drafts[0].Status != 1 || drafts[0].TimeoutMs != 30000 {
		t.Fatalf("default status/timeout mismatch: %+v", drafts[0])
	}

	// 显式停用
	zero := 0
	drafts, err = parseMcpCreateDrafts(mcpCreateRequest{Name: "demo", Kind: "http", Endpoint: "http://127.0.0.1:18090/mcp", Status: &zero})
	if err != nil || drafts[0].Status != 0 {
		t.Fatalf("explicit disable mismatch: %v %+v", err, drafts)
	}

	// 非白名单 kind 拒绝
	if _, err := parseMcpCreateDrafts(mcpCreateRequest{Name: "demo", Kind: "npx"}); err == nil {
		t.Fatal("non-whitelisted kind should be rejected")
	}

	// rawConfig 优先且缺 name 时可用
	drafts, err = parseMcpCreateDrafts(mcpCreateRequest{RawConfig: `{"mcpServers":{"a":{"url":"http://x.example.com/mcp"}}}`})
	if err != nil || len(drafts) != 1 || drafts[0].Name != "a" {
		t.Fatalf("rawConfig precedence failed: %v %+v", err, drafts)
	}

	// 无 rawConfig 且无 name → 报错
	if _, err := parseMcpCreateDrafts(mcpCreateRequest{}); err == nil {
		t.Fatal("missing name without rawConfig should be rejected")
	}
}

func TestNormalizeMcpTimeout(t *testing.T) {
	if got := normalizeMcpTimeout(0); got != 30000 {
		t.Fatalf("default timeout want 30000, got %d", got)
	}
	if got := normalizeMcpTimeout(5000); got != 5000 {
		t.Fatalf("passthrough timeout want 5000, got %d", got)
	}
	if got := normalizeMcpTimeout(999999); got != 300000 {
		t.Fatalf("capped timeout want 300000, got %d", got)
	}
}

func TestMarshalMcpHeaders(t *testing.T) {
	if got, err := marshalMcpHeaders(nil); err != nil || got != "" {
		t.Fatalf("nil headers want empty, got %q err %v", got, err)
	}
	got, err := marshalMcpHeaders(map[string]string{"Authorization": "Bearer k"})
	if err != nil || !strings.Contains(got, "Authorization") {
		t.Fatalf("headers serialize failed: %q %v", got, err)
	}
}

func TestMcpYamlServerViewScaffold(t *testing.T) {
	original := conf.CustomConf.MCP
	defer func() { conf.CustomConf.MCP = original }()
	conf.CustomConf.MCP = conf.MCPConfig{
		AppKey: "demo-app",
		Servers: []conf.MCPServerConf{
			{Name: "mcpgw", Kind: "http", Endpoint: "http://127.0.0.1:18080/api/mcp", Headers: map[string]string{"Authorization": "Bearer k:s"}, TimeoutMs: 30000},
			{Name: "repo", Kind: "repo", Env: map[string]string{"REPO_ROOT": "/tmp/repo"}},
		},
	}

	cfg, ok := findMcpYamlServer("mcpgw")
	if !ok {
		t.Fatal("findMcpYamlServer 应命中 mcpgw")
	}
	view := mcpYamlServerViewScaffold(cfg)
	if view.ServerID != "yaml:mcpgw" {
		t.Fatalf("serverId 应为 yaml:mcpgw: %q", view.ServerID)
	}
	if view.Source != mcpServerSourceYaml {
		t.Fatalf("source 应为 yaml: %q", view.Source)
	}
	if view.Endpoint != "http://127.0.0.1:18080/api/mcp" {
		t.Fatalf("endpoint 应取配置文件值: %q", view.Endpoint)
	}
	if view.Status != 1 || !view.HasHeaders || view.TimeoutMs != 30000 {
		t.Fatalf("静态字段不符合配置: %+v", view)
	}
	if len(view.BoundApps) != 1 || view.BoundApps[0] != "demo-app" {
		t.Fatalf("BoundApps 应为 mcp.app_key: %v", view.BoundApps)
	}
	if view.Running {
		t.Fatal("无运行时客户端时 Running 应为 false")
	}

	if _, ok := findMcpYamlServer("not-exist"); ok {
		t.Fatal("未配置名称不应命中")
	}
	if mcpYamlServerID("repo") != "yaml:repo" {
		t.Fatalf("合成 ID 不符合预期: %q", mcpYamlServerID("repo"))
	}
}
