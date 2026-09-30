// Package repotools 提供对本地仓库根目录的只读访问（列目录 / 读文件 / 词法搜索）。
// 所有路径均限制在配置的仓库根内，含符号链接逃逸校验；结果数量与文件大小有上限。
package repotools

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Repo 绑定一个只读仓库根目录。
type Repo struct{ root string }

// New 打开并校验仓库根目录。
// root 经 EvalSymlinks 归一化后再保存：safeExisting 会把候选路径解析到底层真实路径，
// 若 root 保留未解析形式（如 macOS 的 /var → /private/var），解析后的路径会被误判逃逸。
func New(root string) (*Repo, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("repo root:%s: %w", abs, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root:%s: %w", abs, err)
	}
	return &Repo{root: resolved}, nil
}

// ListFiles 列出某子目录下的直接条目（忽略 .git/node_modules 等）。
func (r *Repo) ListFiles(rel string, maxEntries int) ([]string, error) {
	root, err := r.safeExisting(rel)
	if err != nil {
		return nil, err
	}
	if maxEntries <= 0 || maxEntries > 1000 {
		maxEntries = 200
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if ignored(e.Name()) {
			continue
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		out = append(out, name)
		if len(out) >= maxEntries {
			break
		}
	}
	sort.Strings(out)
	return out, nil
}

// ReadFile 读取文件的有界行区间（默认 200 行，单文件上限 2MB）。
func (r *Repo) ReadFile(rel string, start, end int) (map[string]any, error) {
	path, err := r.safeExisting(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is directory:%s", rel)
	}
	if info.Size() > 2<<20 {
		return nil, fmt.Errorf("file too large:%d", info.Size())
	}
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end-start > 400 {
		end = start + 199
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	lines := []string{}
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if lineNo < start {
			continue
		}
		if lineNo > end {
			break
		}
		lines = append(lines, fmt.Sprintf("%d\t%s", lineNo, scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"path": rel, "start_line": start, "end_line": lineNo, "content": strings.Join(lines, "\n")}, nil
}

// SearchCode 在仓库内做不区分大小写的包含匹配，返回有界候选。
func (r *Repo) SearchCode(query, rel string, maxResults int) ([]map[string]any, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if maxResults <= 0 || maxResults > 200 {
		maxResults = 50
	}
	needle := strings.ToLower(query)
	results := []map[string]any{}
	err := r.searchLines(rel, func(relPath string, line int, text string) bool {
		if strings.Contains(strings.ToLower(text), needle) {
			results = append(results, map[string]any{"path": relPath, "line": line, "content": strings.TrimSpace(text)})
		}
		return len(results) < maxResults
	})
	return results, err
}

// searchLines 逐行扫描 rel 子树内不超限的文件，每行回调一次；回调返回 false 即终止。
func (r *Repo) searchLines(rel string, fn func(relPath string, line int, text string) bool) error {
	base, err := r.safeExisting(rel)
	if err != nil {
		return err
	}
	return filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path != base && ignored(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 2<<20)
		line := 0
		for scanner.Scan() && fn(filepath.ToSlash(mustRel(r.root, path)), line+1, scanner.Text()) {
			line++
		}
		if scanner.Err() != nil {
			return nil
		}
		return nil
	})
}

// RepoMap 返回有界的仓库地图：.go 文件带顶层声明摘要（Aider 风格），其余为路径清单。
func (r *Repo) RepoMap(maxFiles int) ([]string, error) {
	if maxFiles <= 0 || maxFiles > 2000 {
		maxFiles = 300
	}
	out := []string{}
	err := filepath.WalkDir(r.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// 地图预算优先给源码：隐藏目录（含 .github）整棵跳过
			if path != r.root && (ignored(d.Name()) || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) >= maxFiles {
			return fs.SkipAll
		}
		// 地图预算优先给源码：隐藏目录/文件（含 .github）与 _test.go 不占条目
		if strings.HasPrefix(d.Name(), ".") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") {
			if line, ok := GoRepoMapLine(r.root, path, 12); ok {
				out = append(out, line)
				return nil
			}
		}
		out = append(out, filepath.ToSlash(mustRel(r.root, path)))
		return nil
	})
	sort.Strings(out)
	return out, err
}

// safeExisting 把相对路径解析到仓库根内的绝对路径，拒绝穿越与符号链接逃逸。
func (r *Repo) safeExisting(rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	cleanRel := filepath.Clean(rel)
	if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("path escapes repository root")
	}
	candidate := filepath.Join(r.root, cleanRel)
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	if !within(r.root, abs) {
		return "", fmt.Errorf("path escapes repository root")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if !within(r.root, resolved) {
		return "", fmt.Errorf("symlink escapes repository root")
	}
	return resolved, nil
}

func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func ignored(name string) bool {
	switch name {
	case ".git", ".idea", ".vscode", "node_modules", "vendor", "dist", "build", "target", "coverage", "__pycache__":
		return true
	default:
		return strings.HasPrefix(name, ".") && name != ".github"
	}
}
