package bundle

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/conf"
)

// fetch 相关限额与格式约束（防滥用；解析纯内存，不落盘中间产物）。
const (
	maxBundleFileBytes         = 1 << 20  // 单个 agent/skill markdown 上限 1MB
	maxBundleMcpJSONBytes      = 256 << 10 // .mcp.json 上限 256KB
	maxBundleDefinitionFiles   = 64        // agents+skills markdown 总数上限
	maxBundleManifestBytes     = 64 << 10  // manifest 上限 64KB
)

// bundleNamePattern 是 manifest.name 的强校验（OH InstallationManager 同款 kebab-case）。
var bundleNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// refPattern 允许进入 git argv 的 ref 字符（与 workspace 一致，拒绝空白与元字符）。
var refPattern = regexp.MustCompile(`^[a-zA-Z0-9._/\-]+$`)

// commitPattern 是完整 commit 哈希格式（rev-parse 输出校验）。
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// gitSubcommands 是 bundle 拉取允许的 git 子命令白名单（与 workspace 同集：
// clone/fetch/worktree/rev-parse 覆盖 mirror 缓存 + worktree 检出 + ref 解析）。
var gitSubcommands = map[string]bool{
	"clone":     true,
	"fetch":     true,
	"worktree":  true,
	"rev-parse": true,
}

type bundleManifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

type bundleFile struct {
	Name     string // agent：文件基名（去 .md）；skill：目录名
	Markdown string
}

// bundleContent 是一次拉取解析后的内存形态。
type bundleContent struct {
	Manifest    bundleManifest
	ManifestRaw string
	Agents      []bundleFile
	Skills      []bundleFile
	McpJSON     string // 空串 = 包内无 .mcp.json
	commit      string // git 来源钉住的 commit（本地路径为空）
}

// validateBundleSource 校验安装来源命中白名单前缀（git URL 与本地路径同一张白名单）；
// 另放行 /react/bundle/upload 的服务端暂存区（cache_dir/uploads，清洗后的严格前缀，
// 其内容只可能来自上传的 zip，不是用户任意指定的路径）。
func validateBundleSource(cfg conf.ReactBundleConfig, source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return components.ErrorParamInvalid.Sprintf("source 不能为空")
	}
	for _, prefix := range cfg.AllowedSourcePrefixes {
		if strings.HasPrefix(source, strings.TrimSpace(prefix)) {
			return nil
		}
	}
	if strings.TrimSpace(cfg.CacheDir) != "" {
		staging := bundleUploadRoot(cfg)
		cleaned := filepath.Clean(source)
		if cleaned == staging || strings.HasPrefix(cleaned, staging+string(filepath.Separator)) {
			return nil
		}
	}
	return components.ErrorParamInvalid.Sprintf("安装来源不在白名单内（llm.react.bundle.allowed_source_prefixes）: %s", source)
}

// isLocalSource 判定是否本地路径来源（无 URL scheme 即视为本地路径）。
func isLocalSource(source string) bool {
	return !strings.Contains(source, "://")
}

// fetchBundle 拉取并解析 bundle：本地路径直接读目录；git URL 走 mirror 缓存 + 临时 worktree
//（ref 解析为 commit 钉住，读毕即删 worktree）。
func fetchBundle(cfg conf.ReactBundleConfig, source, ref, repoPath string) (*bundleContent, error) {
	source = strings.TrimSpace(source)
	repoPath = strings.TrimSpace(repoPath)
	if repoPath != "" {
		cleaned := filepath.Clean(repoPath)
		if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") || strings.HasPrefix(repoPath, "/") {
			return nil, components.ErrorParamInvalid.Sprintf("repoPath 必须是包内相对子目录: %q", repoPath)
		}
	}
	if isLocalSource(source) {
		info, err := os.Stat(source)
		if err != nil || !info.IsDir() {
			return nil, components.ErrorBundleImportInvalid.Sprintf("本地来源不是有效目录: %s", source)
		}
		root := source
		if repoPath != "" {
			root = filepath.Join(source, filepath.FromSlash(repoPath))
		}
		return readBundleDir(root)
	}

	if ref != "" && !refPattern.MatchString(ref) {
		return nil, components.ErrorParamInvalid.Sprintf("ref 含非法字符: %q", ref)
	}
	cache, err := filepath.Abs(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, err
	}
	repoName := strings.TrimSuffix(filepath.Base(strings.TrimRight(source, "/")), ".git")
	mirror := filepath.Join(cache, repoName+".git")
	if info, statErr := os.Stat(filepath.Join(mirror, "HEAD")); statErr == nil && !info.IsDir() {
		if _, err := runBundleGit(cfg, mirror, "fetch", "--prune", "origin"); err != nil {
			return nil, components.ErrorBundleImportInvalid.Sprintf("mirror 刷新失败(%s): %v", source, err)
		}
	} else if _, err := runBundleGit(cfg, "", "clone", "--mirror", source, mirror); err != nil {
		return nil, components.ErrorBundleImportInvalid.Sprintf("mirror 克隆失败(%s): %v", source, err)
	}


	// ref 钉住为 commit（空 ref 用 HEAD）；worktree 检出到临时目录，读毕清理。
	revArgs := []string{"rev-parse"}
	if ref == "" {
		revArgs = append(revArgs, "HEAD")
	} else {
		revArgs = append(revArgs, ref)
	}
	out, err := runBundleGit(cfg, mirror, revArgs...)
	if err != nil {
		return nil, components.ErrorBundleImportInvalid.Sprintf("ref %q 解析失败: %v", ref, err)
	}
	commit := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if !commitPattern.MatchString(commit) {
		return nil, components.ErrorBundleImportInvalid.Sprintf("ref %q 解析结果非法: %q", ref, commit)
	}

	worktree, err := os.MkdirTemp(cache, "bundle-wt-")
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = runBundleGit(cfg, mirror, "worktree", "remove", "--force", worktree)
		_ = os.Remove(worktree)
	}()
	if _, err := runBundleGit(cfg, mirror, "worktree", "add", "--detach", worktree, commit); err != nil {
		return nil, components.ErrorBundleImportInvalid.Sprintf("worktree 检出失败(%s@%s): %v", source, commit, err)
	}

	root := worktree
	if repoPath != "" {
		root = filepath.Join(worktree, filepath.FromSlash(repoPath))
	}
	content, err := readBundleDir(root)
	if err != nil {
		return nil, err
	}
	content.commit = commit
	return content, nil
}

// runBundleGit 执行白名单内 git 子命令。安全属性与 workspace.runGit 相同：
// 可执行文件是编译期常量、不经 shell、子命令白名单、动态参数先过正则、超时看门狗。
func runBundleGit(cfg conf.ReactBundleConfig, dir string, args ...string) ([]byte, error) {
	if len(args) == 0 || !gitSubcommands[args[0]] {
		return nil, fmt.Errorf("git subcommand not allowed: %q", args[0])
	}
	timeout := cfg.GitTimeoutSec
	if timeout <= 0 {
		timeout = 120
	}
	cmd := exec.Command("git")
	cmd.Args = append(cmd.Args, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	timer := time.AfterFunc(time.Duration(timeout)*time.Second, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// readBundleDir 读取 bundle 根目录的 manifest/agents/skills/.mcp.json（纯内存）。
// 布局（OH Claude Code 兼容）：manifest 取 .plugin/plugin.json 或根 manifest.json。
func readBundleDir(root string) (*bundleContent, error) {
	manifestRaw, err := readBundleManifest(root)
	if err != nil {
		return nil, err
	}
	var manifest bundleManifest
	if err := json.Unmarshal([]byte(manifestRaw), &manifest); err != nil {
		return nil, components.ErrorBundleImportInvalid.Sprintf("manifest 解析失败: %v", err)
	}
	manifest.Name = strings.TrimSpace(manifest.Name)
	if !bundleNamePattern.MatchString(manifest.Name) {
		return nil, components.ErrorBundleImportInvalid.Sprintf("manifest.name 必须是 kebab-case（小写字母数字+中划线）: %q", manifest.Name)
	}
	if manifest.Version = strings.TrimSpace(manifest.Version); manifest.Version == "" {
		manifest.Version = "1.0.0"
	}

	content := &bundleContent{Manifest: manifest, ManifestRaw: manifestRaw}

	agentsDir := filepath.Join(root, "agents")
	if entries, err := os.ReadDir(agentsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") || isHiddenSegment(entry.Name()) || strings.HasPrefix(entry.Name(), "__") {
				continue
			}
			if err := checkBundleFileCount(content); err != nil {
				return nil, err
			}
			data, err := readCapped(filepath.Join(agentsDir, entry.Name()), maxBundleFileBytes)
			if err != nil {
				return nil, components.ErrorBundleImportInvalid.Sprintf("读取 agents/%s 失败: %v", entry.Name(), err)
			}
			content.Agents = append(content.Agents, bundleFile{
				Name:     strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
				Markdown: string(data),
			})
		}
	}

	skillsDir := filepath.Join(root, "skills")
	if entries, err := os.ReadDir(skillsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || isHiddenSegment(entry.Name()) || entry.Name() == "__MACOSX" {
				continue
			}
			data, err := readCapped(filepath.Join(skillsDir, entry.Name(), "SKILL.md"), maxBundleFileBytes)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, components.ErrorBundleImportInvalid.Sprintf("读取 skills/%s/SKILL.md 失败: %v", entry.Name(), err)
			}
			if err := checkBundleFileCount(content); err != nil {
				return nil, err
			}
			content.Skills = append(content.Skills, bundleFile{Name: entry.Name(), Markdown: string(data)})
		}
	}

	if data, err := readCapped(filepath.Join(root, ".mcp.json"), maxBundleMcpJSONBytes); err == nil {
		content.McpJSON = string(data)
	}

	if len(content.Agents) == 0 && len(content.Skills) == 0 && content.McpJSON == "" {
		return nil, components.ErrorBundleImportInvalid.Sprintf("bundle 内没有可安装资源（期待 agents/*.md、skills/<name>/SKILL.md 或 .mcp.json）")
	}
	return content, nil
}

func readBundleManifest(root string) (string, error) {
	for _, candidate := range []string{filepath.Join(root, ".plugin", "plugin.json"), filepath.Join(root, "manifest.json")} {
		data, err := readCapped(candidate, maxBundleManifestBytes)
		if err == nil {
			return string(data), nil
		}
		if !os.IsNotExist(err) {
			return "", components.ErrorBundleImportInvalid.Sprintf("读取 manifest 失败(%s): %v", candidate, err)
		}
	}
	return "", components.ErrorBundleImportInvalid.Sprintf("缺少 manifest（期待 .plugin/plugin.json 或根 manifest.json）")
}

func checkBundleFileCount(content *bundleContent) error {
	if len(content.Agents)+len(content.Skills) >= maxBundleDefinitionFiles {
		return components.ErrorBundleImportInvalid.Sprintf("agents+skills 文件数超过上限 %d", maxBundleDefinitionFiles)
	}
	return nil
}

func readCapped(path string, limit int) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("是目录不是文件: %s", path)
	}
	if info.Size() > int64(limit) {
		return nil, fmt.Errorf("超过 %d 字节上限", limit)
	}
	return os.ReadFile(path)
}

func isHiddenSegment(name string) bool {
	return name == "" || strings.HasPrefix(name, ".")
}
