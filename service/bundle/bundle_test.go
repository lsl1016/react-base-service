package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"react-base-service/conf"
)

func testBundleConfig(prefixes ...string) conf.ReactBundleConfig {
	enabled := true
	return conf.ReactBundleConfig{
		Enabled:               &enabled,
		CacheDir:              filepath.Join(os.TempDir(), "bundle-cache-test"),
		GitTimeoutSec:         30,
		AllowedSourcePrefixes: prefixes,
	}
}

func TestValidateBundleSource(t *testing.T) {
	cfg := testBundleConfig("https://git.internal.example.com/", `C:/Users/keke/Desktop/xm/`)
	valid := []string{
		"https://git.internal.example.com/ops/uda-bundle",
		"C:/Users/keke/Desktop/xm/test-bundles/uda",
	}
	for _, source := range valid {
		if err := validateBundleSource(cfg, source); err != nil {
			t.Fatalf("来源 %q 应在白名单内: %v", source, err)
		}
	}
	invalid := []string{"", "https://github.com/evil/bundle", "C:/Windows/system32", "file:///etc"}
	for _, source := range invalid {
		if err := validateBundleSource(cfg, source); err == nil {
			t.Fatalf("来源 %q 应被白名单拒绝", source)
		}
	}
	empty := testBundleConfig()
	if err := validateBundleSource(empty, "https://git.internal.example.com/x"); err == nil {
		t.Fatal("空白名单应拒绝一切来源")
	}
}

// writeTestBundleDir 构造一个标准布局的本地 bundle 目录。
func writeTestBundleDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".plugin/plugin.json", `{"name":"uda-ops","version":"1.2.0","description":"UDA 运维插件包"}`)
	write("agents/dba-agent.md", "---\nagent_key: bundle-dba\napp_key: demo-app\ndescription: 测试\n---\n你是 DBA。")
	write("skills/uda-runbook/SKILL.md", "---\nname: bundle-uda-runbook\ntriggers: [UDA 排障]\n---\n排障流程正文。")
	write(".mcp.json", `{"mcpServers":{"bundle-demo":{"url":"https://mcp.example.com/api","headers":{"Authorization":"Bearer x"}}}}`)
	write("README.md", "说明文件，应被忽略")
	write("agents/__MACOSX.md", "应被忽略") // 隐藏段
	return root
}

func TestFetchBundleLocalDir(t *testing.T) {
	root := writeTestBundleDir(t)
	cfg := testBundleConfig(root)

	content, err := fetchBundle(cfg, root, "", "")
	if err != nil {
		t.Fatalf("本地目录拉取失败: %v", err)
	}
	if content.Manifest.Name != "uda-ops" || content.Manifest.Version != "1.2.0" {
		t.Fatalf("manifest 解析不符合预期: %+v", content.Manifest)
	}
	if len(content.Agents) != 1 || content.Agents[0].Name != "dba-agent" {
		t.Fatalf("agents 解析不符合预期: %+v", content.Agents)
	}
	if len(content.Skills) != 1 || content.Skills[0].Name != "uda-runbook" {
		t.Fatalf("skills 解析不符合预期: %+v", content.Skills)
	}
	if !strings.Contains(content.McpJSON, "bundle-demo") {
		t.Fatalf("mcp.json 解析不符合预期: %s", content.McpJSON)
	}
	if content.commit != "" {
		t.Fatalf("本地来源不应有 commit: %q", content.commit)
	}
}

func TestReadBundleDirManifestValidation(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{"kebab 必须", `{"name":"UDA Ops"}`, "kebab-case"},
		{"name 必填", `{"version":"1.0.0"}`, "kebab-case"},
		{"大写拒绝", `{"name":"UdaOps"}`, "kebab-case"},
		{"合法", `{"name":"uda-ops2"}`, ""},
	}
	for _, tc := range cases {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".plugin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".plugin", "plugin.json"), []byte(tc.manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers":{"x":{"url":"https://e.com"}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := readBundleDir(root)
		if tc.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: 应通过: %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s: 期望错误含 %q: %v", tc.name, tc.wantErr, err)
		}
	}
}

func TestReadBundleDirRejectsEmpty(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"name":"empty-bundle"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readBundleDir(root)
	if err == nil || !strings.Contains(err.Error(), "没有可安装资源") {
		t.Fatalf("空 bundle 应被拒绝: %v", err)
	}
}

func TestReadBundleDirRootManifestFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"name":"root-manifest"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "s1", "SKILL.md"), []byte("---\nname: s1\n---\n正文"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err := readBundleDir(root)
	if err != nil {
		t.Fatalf("根 manifest.json 应被接受: %v", err)
	}
	if content.Manifest.Name != "root-manifest" {
		t.Fatalf("manifest 不符合预期: %+v", content.Manifest)
	}
}

func TestRepoPathTraversalRejected(t *testing.T) {
	cfg := testBundleConfig("C:/x/")
	for _, repoPath := range []string{"../escape", "/abs", `C:\abs`} {
		if _, err := fetchBundle(cfg, "C:/x/bundle", "", repoPath); err == nil {
			t.Fatalf("repoPath %q 应被拒绝", repoPath)
		}
	}
}

func TestParseBundleMcpServers(t *testing.T) {
	servers, err := parseBundleMcpServers(`{"mcpServers":{"a":{"url":"https://a.com"},"b":{"url":"http://b.com","headers":{"X-K":"v"}}}}`)
	if err != nil {
		t.Fatalf("标准格式应通过: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("应解析出 2 个服务器: %+v", servers)
	}

	if _, err := parseBundleMcpServers(`{"mcpServers":{"cmd":{"command":"uvx","args":["x"]}}}`); err == nil {
		t.Fatal("command 形式应被拒绝")
	}
	if _, err := parseBundleMcpServers(`{"mcpServers":{"noscheme":{"url":"ftp://x"}}}`); err == nil {
		t.Fatal("非 http/https scheme 应被拒绝")
	}
	if _, err := parseBundleMcpServers(`{}`); err == nil {
		t.Fatal("空 mcpServers 应被拒绝")
	}
	if _, err := parseBundleMcpServers(`not-json`); err == nil {
		t.Fatal("非法 JSON 应被拒绝")
	}
}
