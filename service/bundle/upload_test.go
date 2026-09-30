package bundle

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"react-base-service/conf"

	"github.com/gin-gonic/gin"
)

// bundleFilesForm 把「目录内相对路径 → 内容」包成 multipart 表单并解析回
//（files[] 文件 + paths 同顺序相对路径 JSON，等价于前端 webkitdirectory 上传）。
func bundleFilesForm(t *testing.T, files map[string]string) ([]*multipart.FileHeader, []string) {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	pathsJSON, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	field, err := form.CreateFormField("paths")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := field.Write(pathsJSON); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		fileField, err := form.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fileField.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	parsed, err := multipart.NewReader(bytes.NewReader(buf.Bytes()), form.Boundary()).ReadForm(int64(len(buf.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parsed.RemoveAll() })
	var paths []string
	if err := json.Unmarshal([]byte(parsed.Value["paths"][0]), &paths); err != nil {
		t.Fatal(err)
	}
	return parsed.File["files"], paths
}

// withUploadConfig 临时替换全局 bundle 配置（UploadFiles 内部读 GetReactRuntimeConfig）。
func withUploadConfig(t *testing.T, cfg conf.ReactBundleConfig) {
	t.Helper()
	original := conf.CustomConf.LLM.React.Bundle
	conf.CustomConf.LLM.React.Bundle = cfg
	t.Cleanup(func() { conf.CustomConf.LLM.React.Bundle = original })
}

func uploadTestConfig(t *testing.T) conf.ReactBundleConfig {
	enabled := true
	return conf.ReactBundleConfig{
		Enabled:               &enabled,
		CacheDir:              t.TempDir(),
		GitTimeoutSec:         30,
		AllowedSourcePrefixes: nil, // 上传路线不依赖白名单
	}
}

func testCtx() *gin.Context {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	return ctx
}

func TestUploadFilesHappyPath(t *testing.T) {
	cfg := uploadTestConfig(t)
	withUploadConfig(t, cfg)
	// 常见形态：所选目录（wrapper/）下再包一层才是 bundle；混入 macOS 垃圾文件。
	files, paths := bundleFilesForm(t, map[string]string{
		"wrapper/my-bundle/.plugin/plugin.json": `{"name":"upload-probe","version":"1.0.0"}`,
		"wrapper/my-bundle/agents/dba.md":       "---\nagent_key: upload-dba\ndescription: t\n---\n你是 DBA。",
		"wrapper/my-bundle/skills/run/SKILL.md": "---\nname: bundle-run\n---\n排障流程。",
		"__MACOSX/wrapper/._junk":               "垃圾",
		"wrapper/my-bundle/.DS_Store":           "垃圾",
	})
	resp, err := UploadFiles(testCtx(), files, paths)
	if err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	if resp.Name != "wrapper" {
		t.Fatalf("回显目录名不符合预期: %q", resp.Name)
	}
	// 暂存目录必须落在 cache_dir/uploads 下，且作为 source 通过白名单校验、可直接解析。
	if want := bundleUploadRoot(cfg); !strings.HasPrefix(resp.Path, want+string(filepath.Separator)) {
		t.Fatalf("暂存目录不在 uploads 下: %s（want 前缀 %s）", resp.Path, want)
	}
	if err := validateBundleSource(cfg, resp.Path); err != nil {
		t.Fatalf("上传暂存目录应免白名单放行: %v", err)
	}
	content, err := fetchBundle(cfg, resp.Path, "", "")
	if err != nil {
		t.Fatalf("暂存目录应可直接作为安装来源: %v", err)
	}
	if content.Manifest.Name != "upload-probe" || len(content.Agents) != 1 || len(content.Skills) != 1 {
		t.Fatalf("解析结果不符合预期: %+v", content.Manifest)
	}
	if _, err := os.Stat(filepath.Join(resp.Path, "agents", "dba.md")); err != nil {
		t.Fatalf("agents 文件应已落地: %v", err)
	}
	// __MACOSX/.DS_Store 垃圾不应落地（检查暂存目录顶层）。
	entries, err := os.ReadDir(filepath.Dir(filepath.Dir(resp.Path)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "__MACOSX") || strings.Contains(entry.Name(), ".DS_Store") {
			t.Fatalf("垃圾条目不应落地: %s", entry.Name())
		}
	}
}

func TestUploadFilesRejectsInvalid(t *testing.T) {
	// 空文件列表。
	cfg := uploadTestConfig(t)
	withUploadConfig(t, cfg)
	if _, err := UploadFiles(testCtx(), nil, nil); err == nil || !strings.Contains(err.Error(), "请选择") {
		t.Fatalf("空上传应被拒绝: %v", err)
	}

	// 误选单个 zip：定向提示。
	cfg2 := uploadTestConfig(t)
	withUploadConfig(t, cfg2)
	zipFiles, zipPaths := bundleFilesForm(t, map[string]string{"my-bundle.zip": "PK"})
	if _, err := UploadFiles(testCtx(), zipFiles, zipPaths); err == nil || !strings.Contains(err.Error(), "解压") {
		t.Fatalf("单个 zip 应定向提示解压: %v", err)
	}

	// paths 与 files 数量不一致。
	moreFiles, _ := bundleFilesForm(t, map[string]string{"a/x.md": "1", "a/y.md": "2"})
	if _, err := UploadFiles(testCtx(), moreFiles, []string{"a/x.md"}); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("数量不一致应被拒绝: %v", err)
	}

	// 缺 manifest：报错且暂存区不留垃圾目录。
	cfg3 := uploadTestConfig(t)
	withUploadConfig(t, cfg3)
	files, paths := bundleFilesForm(t, map[string]string{
		"my-bundle/agents/x.md": "---\nagent_key: only-agent\ndescription: t\n---\n正文",
	})
	_, err := UploadFiles(testCtx(), files, paths)
	if err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("缺 manifest 应被拒绝并提示: %v", err)
	}
	leftover, readErr := os.ReadDir(bundleUploadRoot(cfg3))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(leftover) != 0 {
		t.Fatalf("失败的上传不应留下暂存目录: %d 个残留", len(leftover))
	}

	// cache_dir 未配置：直接拒绝。
	cfg4 := uploadTestConfig(t)
	cfg4.CacheDir = ""
	withUploadConfig(t, cfg4)
	dFiles, dPaths := bundleFilesForm(t, map[string]string{"a/b.md": "x"})
	if _, err := UploadFiles(testCtx(), dFiles, dPaths); err == nil || !strings.Contains(err.Error(), "cache_dir") {
		t.Fatalf("cache_dir 缺失应被拒绝: %v", err)
	}
}

func TestWriteBundleFilesRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../evil.txt", "/abs/evil.txt", "C:/evil.txt", "a/../../evil.txt"} {
		files, paths := bundleFilesForm(t, map[string]string{name: "x"})
		stage := t.TempDir()
		if err := writeBundleFiles(files, paths, stage); err == nil || !strings.Contains(err.Error(), "非法") {
			t.Fatalf("逃逸路径 %q 应被拒绝: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(stage, "evil.txt")); !os.IsNotExist(err) {
			t.Fatalf("逃逸路径 %q 不应落地", name)
		}
	}
}

func TestValidateBundleSourceUploadStaging(t *testing.T) {
	cfg := uploadTestConfig(t)
	staging := bundleUploadRoot(cfg)
	valid := []string{
		staging,
		filepath.Join(staging, "my-bundle-123"),
	}
	for _, source := range valid {
		if err := validateBundleSource(cfg, source); err != nil {
			t.Fatalf("暂存来源 %q 应放行: %v", source, err)
		}
	}
	invalid := []string{
		filepath.Clean(cfg.CacheDir),                            // cache 根（mirror 所在地）不放行
		filepath.Join(filepath.Clean(cfg.CacheDir), "repo.git"), // uploads 之外的兄弟目录
		staging + string(filepath.Separator) + ".." + string(filepath.Separator) + "evil",
	}
	for _, source := range invalid {
		if err := validateBundleSource(cfg, source); err == nil {
			t.Fatalf("来源 %q 应被拒绝", source)
		}
	}
	// cache_dir 未配置时无暂存放行。
	empty := uploadTestConfig(t)
	empty.CacheDir = ""
	if err := validateBundleSource(empty, bundleUploadRoot(cfg)); err == nil {
		t.Fatal("cache_dir 未配置时不应放行任何暂存路径")
	}
}
