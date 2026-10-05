package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"react-base-service/conf"
	"react-base-service/service/mcpclient"

	"github.com/gin-gonic/gin"
)

// newTestRepo 在临时目录创建一个含两个 commit 的本地 git 仓库（main 分支），
// 并在首 commit 上建 release 分支，供 env → ref 差异化场景使用。
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("-C", dir, "init", "-b", "main")
	run("-C", dir, "config", "user.email", "test@example.com")
	run("-C", dir, "config", "user.name", "test")
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello alpha"), 0o644)
	run("-C", dir, "add", ".")
	run("-C", dir, "commit", "-m", "first")
	_ = os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\nfunc Beta() {}\n"), 0o644)
	run("-C", dir, "add", ".")
	run("-C", dir, "commit", "-m", "second")
	run("-C", dir, "branch", "release", "HEAD~1")
	return dir
}

// newTestManager 构造不触发单例的测试 Manager（独立临时目录）。
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return &Manager{
		root:    filepath.Join(t.TempDir(), "ws"),
		mirrors: filepath.Join(t.TempDir(), "mirror"),
		entries: map[string]*wsEntry{},
		runRefs: map[string]map[string]*runRef{},
		fetchMu: map[string]*sync.Mutex{},
		keyMu:   map[string]*sync.Mutex{},
	}
}

// enableWorkspace 覆盖 workspace 配置为单服务白名单（测试结束还原）。
func enableWorkspace(t *testing.T, repoURL string, refs map[string]string) {
	t.Helper()
	enabled := true
	original := conf.CustomConf.LLM.React.Workspace
	conf.CustomConf.LLM.React.Workspace = conf.ReactWorkspaceConfig{
		Enabled:   &enabled,
		Resolvers: []conf.ReactWorkspaceResolverConf{{Service: "svc", RepoURL: repoURL, Refs: refs}},
	}
	t.Cleanup(func() { conf.CustomConf.LLM.React.Workspace = original })
}

// mcpCallLog 记录 mcpclient 触点替身的调用情况。
type mcpCallLog struct {
	mu           sync.Mutex
	ensureNames  []string
	removeNames  []string
	repoRoots    map[string]string
	synced       map[string]int
	toolsCleaned map[string]int
}

// stubMcpTouchpoints 把 mcpclient 触点替换为记录型替身（测试结束还原），git 全部真实执行。
func stubMcpTouchpoints(t *testing.T) *mcpCallLog {
	t.Helper()
	log := &mcpCallLog{
		repoRoots:    map[string]string{},
		synced:       map[string]int{},
		toolsCleaned: map[string]int{},
	}
	origEnsure, origSync, origRemoveTools, origRemoveServer := ensureRepoServer, syncCallerTools, removeCallerTools, removeRepoServer
	ensureRepoServer = func(cfg mcpclient.ServerConfig) (mcpclient.Server, error) {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.ensureNames = append(log.ensureNames, cfg.Name)
		log.repoRoots[cfg.Name] = cfg.Env["REPO_ROOT"]
		return nil, nil
	}
	syncCallerTools = func(callerKey, serverName string, primary bool) (int, error) {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.synced[callerKey]++
		return 0, nil
	}
	removeCallerTools = func(ctx *gin.Context, serverName, callerKey string) (int, error) {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.toolsCleaned[callerKey]++
		return 0, nil
	}
	removeRepoServer = func(name string) error {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.removeNames = append(log.removeNames, name)
		return nil
	}
	t.Cleanup(func() {
		ensureRepoServer, syncCallerTools, removeCallerTools, removeRepoServer = origEnsure, origSync, origRemoveTools, origRemoveServer
	})
	return log
}

func (l *mcpCallLog) ensureCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ensureNames)
}

func (l *mcpCallLog) removeCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.removeNames)
}

func TestMirrorAndWorktreeLifecycle(t *testing.T) {
	repo := newTestRepo(t)
	m := newTestManager(t)

	mirror, err := m.ensureMirror(repo)
	if err != nil {
		t.Fatalf("mirror 克隆失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); err != nil {
		t.Fatalf("mirror 缺少 HEAD: %v", err)
	}

	// 再次 ensureMirror 走 fetch 刷新路径。
	if _, err := m.ensureMirror(repo); err != nil {
		t.Fatalf("mirror 刷新失败: %v", err)
	}

	// ref 解析：空（HEAD）与分支名。
	commit, err := m.resolveCommit(mirror, "")
	if err != nil || len(commit) != 40 {
		t.Fatalf("HEAD 解析失败: %q, %v", commit, err)
	}
	branchCommit, err := m.resolveCommit(mirror, "main")
	if err != nil || branchCommit != commit {
		t.Fatalf("main 解析失败: %q vs %q, %v", branchCommit, commit, err)
	}
	if _, err := m.resolveCommit(mirror, "main; rm -rf /"); err == nil {
		t.Fatal("含元字符的 ref 必须被拒绝")
	}

	// worktree 分配与释放。
	worktree := filepath.Join(m.root, "run_x", "svc")
	if err := m.git(mirror, "worktree", "add", "--detach", worktree, commit); err != nil {
		t.Fatalf("worktree 创建失败: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(worktree, "a.txt")); err != nil || string(data) != "hello alpha" {
		t.Fatalf("worktree 内容不符: %q, %v", data, err)
	}
	if err := m.git(mirror, "worktree", "remove", "--force", worktree); err != nil {
		t.Fatalf("worktree 移除失败: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree 目录应已移除: %v", err)
	}
}

func TestGitSubcommandWhitelist(t *testing.T) {
	m := newTestManager(t)
	if err := m.git("", "push", "origin", "main"); err == nil {
		t.Fatal("白名单外子命令必须被拒绝")
	}
	if _, err := m.gitOutput("", "log"); err == nil {
		t.Fatal("白名单外子命令（log）必须被拒绝")
	}
}

func TestResolveWhitelist(t *testing.T) {
	original := conf.CustomConf.LLM.React.Workspace
	defer func() { conf.CustomConf.LLM.React.Workspace = original }()
	conf.CustomConf.LLM.React.Workspace = conf.ReactWorkspaceConfig{
		Resolvers: []conf.ReactWorkspaceResolverConf{
			{Service: "svc-a", RepoURL: "https://git.example.com/svc-a.git", DefaultRef: "main", Refs: map[string]string{"prod": "release-1.0"}},
		},
	}

	if url, ref, err := Resolve("svc-a", "prod"); err != nil || url != "https://git.example.com/svc-a.git" || ref != "release-1.0" {
		t.Fatalf("env 命中解析失败: %s %s %v", url, ref, err)
	}
	if _, ref, err := Resolve("svc-a", ""); err != nil || ref != "main" {
		t.Fatalf("默认 ref 解析失败: %s %v", ref, err)
	}
	if _, ref, err := Resolve("svc-a", "unknown-env"); err != nil || ref != "main" {
		t.Fatalf("未知 env 应回退默认 ref: %s %v", ref, err)
	}
	if _, _, err := Resolve("svc-not-exist", ""); err == nil {
		t.Fatal("白名单外 service 必须被拒绝")
	}
}

func TestMirrorPathSanitized(t *testing.T) {
	m := newTestManager(t)
	if got := m.mirrorPath("https://git.example.com/weird/../name.git"); strings.Contains(got, "..") {
		t.Fatalf("mirror 路径未净化: %s", got)
	}
	if got := m.mirrorPath("https://git.example.com/ok.git"); !strings.HasSuffix(got, "ok.git") {
		t.Fatalf("mirror 路径不符: %s", got)
	}
}

// 共享与两级释放：不同 run/caller 加载同 service 同 ref 只冷启动一份；
// caller 归零清工具副本、总归零才回收实例。
func TestLoadSharesAcrossRunsAndCallers(t *testing.T) {
	repo := newTestRepo(t)
	enableWorkspace(t, repo, nil)
	log := stubMcpTouchpoints(t)
	m := newTestManager(t)
	ctx := &gin.Context{}

	allocA, err := m.Load(ctx, "run-a", "caller-a", "svc", "")
	if err != nil {
		t.Fatalf("run-a 加载失败: %v", err)
	}
	allocB, err := m.Load(ctx, "run-b", "caller-b", "svc", "")
	if err != nil {
		t.Fatalf("run-b 加载失败: %v", err)
	}

	if log.ensureCount() != 1 {
		t.Fatalf("同 service 同 commit 应只冷启动一个 MCP 实例: %d", log.ensureCount())
	}
	if allocA.Path != allocB.Path || allocA.MCPServerName != allocB.MCPServerName {
		t.Fatalf("两个 run 应共享同一 worktree/server: %s vs %s", allocA.Path, allocB.Path)
	}
	if !strings.HasPrefix(allocA.MCPServerName, "ws_svc_") {
		t.Fatalf("server 名应带 commit 段: %s", allocA.MCPServerName)
	}
	if want := allocA.Path; log.repoRoots[allocA.MCPServerName] != want {
		t.Fatalf("REPO_ROOT 应指向共享 worktree: %s vs %s", log.repoRoots[allocA.MCPServerName], want)
	}
	if _, err := os.Stat(filepath.Join(allocA.Path, "a.txt")); err != nil {
		t.Fatalf("worktree 内容缺失: %v", err)
	}

	snap := m.ActiveSnapshot()
	if len(snap) != 1 || len(snap[0].Runs) != 2 ||
		snap[0].Callers["caller-a"] != 1 || snap[0].Callers["caller-b"] != 1 {
		t.Fatalf("快照引用不符: %+v", snap)
	}

	// run-a 释放：只清 caller-a 工具副本；caller-b 仍引用，实例与 worktree 保留。
	m.ReleaseRun(ctx, "run-a")
	if log.removeCount() != 0 {
		t.Fatalf("仍有引用时不应回收 server: %v", log.removeNames)
	}
	if log.toolsCleaned["caller-a"] != 1 || log.toolsCleaned["caller-b"] != 0 {
		t.Fatalf("caller-a 工具副本清理时机不符: %+v", log.toolsCleaned)
	}
	if _, err := os.Stat(allocA.Path); err != nil {
		t.Fatalf("worktree 不应被回收: %v", err)
	}

	// run-b 释放：总归零，实例回收、目录清空。
	m.ReleaseRun(ctx, "run-b")
	if log.removeCount() != 1 {
		t.Fatalf("总归零应回收 server: %v", log.removeNames)
	}
	if _, err := os.Stat(allocA.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree 应已移除: %v", err)
	}
	// 幂等：重复释放不重复回收。
	m.ReleaseRun(ctx, "run-b")
	if log.removeCount() != 1 {
		t.Fatalf("重复 ReleaseRun 不应再次回收: %v", log.removeNames)
	}
	if entries, _ := os.ReadDir(m.root); len(entries) != 0 {
		t.Fatalf("全部释放后根目录应为空: %v", entries)
	}
}

// 同 run 同 service 幂等：重复 Load 不重复冷启动、不重复计数。
func TestLoadSameRunIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	enableWorkspace(t, repo, nil)
	log := stubMcpTouchpoints(t)
	m := newTestManager(t)
	ctx := &gin.Context{}

	alloc1, err := m.Load(ctx, "run-1", "caller-a", "svc", "")
	if err != nil {
		t.Fatalf("首次加载失败: %v", err)
	}
	alloc2, err := m.Load(ctx, "run-1", "caller-a", "svc", "")
	if err != nil {
		t.Fatalf("幂等加载失败: %v", err)
	}
	if alloc1.MCPServerName != alloc2.MCPServerName {
		t.Fatalf("同 run 重复加载应返回同一实例: %s vs %s", alloc1.MCPServerName, alloc2.MCPServerName)
	}
	if log.ensureCount() != 1 || log.synced["caller-a"] != 1 {
		t.Fatalf("幂等路径不应重复冷启动/同步: ensure=%d synced=%d", log.ensureCount(), log.synced["caller-a"])
	}
	snap := m.ActiveSnapshot()
	if len(snap) != 1 || snap[0].Callers["caller-a"] != 1 {
		t.Fatalf("幂等加载不应双计数: %+v", snap)
	}
	// 双计数回归闸：一次 ReleaseRun 应彻底回收（若双计数，holders 剩 1 不会回收）。
	m.ReleaseRun(ctx, "run-1")
	if log.removeCount() != 1 {
		t.Fatalf("单次释放应彻底回收: %v", log.removeNames)
	}
}

// 同 service 不同 env（不同 commit）→ 两个共享实例并存，互不干扰。
func TestLoadDifferentEnvSeparateEntries(t *testing.T) {
	repo := newTestRepo(t)
	enableWorkspace(t, repo, map[string]string{"stable": "release"})
	log := stubMcpTouchpoints(t)
	m := newTestManager(t)
	ctx := &gin.Context{}

	allocMain, err := m.Load(ctx, "run-1", "caller-a", "svc", "")
	if err != nil {
		t.Fatalf("默认 env 加载失败: %v", err)
	}
	allocStable, err := m.Load(ctx, "run-2", "caller-b", "svc", "stable")
	if err != nil {
		t.Fatalf("stable env 加载失败: %v", err)
	}
	if allocMain.Commit == allocStable.Commit {
		t.Fatalf("release 分支指向不同 commit，解析结果不应相同: %s", allocMain.Commit)
	}
	if allocMain.Path == allocStable.Path || allocMain.MCPServerName == allocStable.MCPServerName {
		t.Fatalf("不同 commit 应是不同实例: %s vs %s", allocMain.Path, allocStable.Path)
	}
	// stable 锁在首 commit：a.txt 在而 b.go 不在。
	if _, err := os.Stat(filepath.Join(allocStable.Path, "b.go")); !os.IsNotExist(err) {
		t.Fatalf("stable worktree 不应包含第二个 commit 的文件: %v", err)
	}
	if snap := m.ActiveSnapshot(); len(snap) != 2 {
		t.Fatalf("应有两个并存实例: %+v", snap)
	}

	m.ReleaseRun(ctx, "run-1")
	m.ReleaseRun(ctx, "run-2")
	if log.removeCount() != 2 {
		t.Fatalf("两个实例应分别回收: %v", log.removeNames)
	}
	if entries, _ := os.ReadDir(m.root); len(entries) != 0 {
		t.Fatalf("全部释放后根目录应为空: %v", entries)
	}
}

// P1 修复回归：同 caller 两个 run，先结束的 run 不得清掉该 caller 名下工具副本。
func TestSameCallerTwoRunsToolCleanupOnLastRelease(t *testing.T) {
	repo := newTestRepo(t)
	enableWorkspace(t, repo, nil)
	log := stubMcpTouchpoints(t)
	m := newTestManager(t)
	ctx := &gin.Context{}

	if _, err := m.Load(ctx, "run-1", "caller-x", "svc", ""); err != nil {
		t.Fatalf("run-1 加载失败: %v", err)
	}
	if _, err := m.Load(ctx, "run-2", "caller-x", "svc", ""); err != nil {
		t.Fatalf("run-2 加载失败: %v", err)
	}

	m.ReleaseRun(ctx, "run-1")
	if log.toolsCleaned["caller-x"] != 0 {
		t.Fatalf("同 caller 仍有活跃 run 时不应清工具副本: %+v", log.toolsCleaned)
	}
	if log.removeCount() != 0 {
		t.Fatalf("仍有引用时不应回收 server: %v", log.removeNames)
	}

	m.ReleaseRun(ctx, "run-2")
	if log.toolsCleaned["caller-x"] != 1 || log.removeCount() != 1 {
		t.Fatalf("最后一个 run 释放时应清副本并回收: tools=%d remove=%d",
			log.toolsCleaned["caller-x"], log.removeCount())
	}
}

// 并发加载（不同 run/caller 混合）：fetch 锁 + entry 锁下全部成功，
// 冷启动仅一次，引用计数精确回收。
func TestConcurrentLoadSingleColdStart(t *testing.T) {
	repo := newTestRepo(t)
	enableWorkspace(t, repo, nil)
	log := stubMcpTouchpoints(t)
	m := newTestManager(t)
	ctx := &gin.Context{}

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runID := fmt.Sprintf("run-%d", i)
			caller := fmt.Sprintf("caller-%d", i%2)
			alloc, err := m.Load(ctx, runID, caller, "svc", "")
			if err != nil {
				t.Errorf("并发加载 %s 失败: %v", runID, err)
				return
			}
			if _, err := os.Stat(filepath.Join(alloc.Path, "a.txt")); err != nil {
				t.Errorf("并发加载 %s worktree 异常: %v", runID, err)
			}
		}(i)
	}
	wg.Wait()

	if log.ensureCount() != 1 {
		t.Fatalf("并发加载应只冷启动一次: %d", log.ensureCount())
	}
	snap := m.ActiveSnapshot()
	if len(snap) != 1 || snap[0].Callers["caller-0"] != n/2 || snap[0].Callers["caller-1"] != n/2 {
		t.Fatalf("并发后引用计数不符: %+v", snap)
	}

	for i := 0; i < n; i++ {
		m.ReleaseRun(ctx, fmt.Sprintf("run-%d", i))
	}
	if log.removeCount() != 1 {
		t.Fatalf("全部释放后应恰好回收一次: %d", log.removeCount())
	}
	if entries, _ := os.ReadDir(m.root); len(entries) != 0 {
		t.Fatalf("全部释放后根目录应为空: %v", entries)
	}
}
