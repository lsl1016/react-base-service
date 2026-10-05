// Package workspace 实现服务端代码工作区（P2-1，方案 §4.2.1；共享化改造见
// docs/todo/20261004_代码工作区共享化改造方案.md）：
//
//	A. RuntimeSourceResolver：service+env → repo_url+ref。P2 首版为静态配置白名单
//	   （llm.react.workspace.resolvers），线上镜像 digest → commit 的真实解析后续接公司基建；
//	B. RepoMirrorCache：bare mirror 单副本缓存（git clone --mirror / fetch --prune），
//	   全部 worktree 共享对象库，避免每次完整 clone；同一 mirror 的 clone/fetch 按
//	   fetchMu 串行化，防止并发 fetch 抢 git .lock 报错；
//	C. WorkspaceAllocator：(service, commit) 粒度共享分配——同一 commit 的 worktree 与
//	   repo MCP 子进程全局仅一份，多 run / 多 caller 引用计数复用（检索只读无写，共享安全）；
//	   run 终态减计数，caller 归零清其工具副本、总归零才回收 worktree 与 MCP 子进程；
//	D. 挂载：共享实例动态挂载只读 repo MCP（复用 mcpclient stdio 适配器，REPO_ROOT=worktree，
//	   工具同步为 caller 名下副本，工具名前缀 = ws_<service>_<commit8>_——名字带 commit 天然
//	   唯一，不再触达 EnsureServer 的同名替换语义，多 run 并发加载互不顶替）。
//
// 安全面：service/env 仅允许字母数字下划线中划线（防路径穿越）；ref 来自配置白名单并经
// refPattern 字符校验；git 子命令有白名单；全部动态参数进入子进程前均做过字符正则校验
// （禁止空白与 shell 元字符），且不经 shell 执行；resolver 静态白名单不接收任意仓库地址；
// worktree 只读语义由 repo-mcp 工具集保证（无写工具），共享不引入并发写风险。
package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"react-base-service/components"
	"react-base-service/conf"
	"react-base-service/golib/zlog"
	"react-base-service/service/mcpclient"

	"github.com/gin-gonic/gin"
)

var (
	servicePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	// refPattern 是允许进入 git argv 的 ref 字符（分支/tag/commit 常见字符），拒绝空白与元字符。
	refPattern = regexp.MustCompile(`^[a-zA-Z0-9._/\-]+$`)
	// commitPattern 是完整 commit 哈希格式（rev-parse 输出校验）。
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// gitSubcommands 是允许执行的 git 子命令白名单。
	gitSubcommands = map[string]bool{
		"clone":     true,
		"fetch":     true,
		"worktree":  true,
		"rev-parse": true,
	}
)

// mcpclient 触点抽成变量便于单测注入替身（与 pyexec.pythonExecArtifactUploader 同一模式）；
// 生产路径就是直连 mcpclient 包函数。
var (
	ensureRepoServer  = mcpclient.EnsureServer
	syncCallerTools   = mcpclient.SyncServerRegistryScoped
	removeCallerTools = mcpclient.RemoveRegistryToolsForCaller
	removeRepoServer  = mcpclient.RemoveServer
)

// Allocation 是一次成功的工作区分配（共享实例的快照视图）。
type Allocation struct {
	Service string `json:"service"`
	Env     string `json:"env"`
	RepoURL string `json:"repoUrl"`
	Ref     string `json:"ref"`
	Commit  string `json:"commit"`
	Path    string `json:"path"`
	// MCPServerName 是共享实例挂载的 repo MCP 名（工具名前缀 = <name>_）。
	MCPServerName string `json:"mcpServerName"`
	// CallerKey 是本次分配的归属 caller（caller 计数归零时清理其工具副本）。
	CallerKey string `json:"callerKey"`
	// LoadedAt 是共享实例冷启动完成时间（管理面运行视图展示）。
	LoadedAt *time.Time `json:"loadedAt"`
}

// wsEntry 是一个 (service, commit) 粒度的共享工作区实例：worktree 与 repo MCP 子进程
// 全局仅此一份，holders 记录 callerKey → 引用它的活跃 run 数。检索只读，多 run 共享安全。
type wsEntry struct {
	key        string
	service    string
	env        string
	repoURL    string
	ref        string
	commit     string
	mirrorPath string
	worktree   string
	serverName string
	loadedAt   time.Time

	// started 由 keyMu（entry key 生命周期锁）保护；holders 由 Manager.mu 保护。
	started bool
	holders map[string]int
}

// allocation 生成对外快照。
func (e *wsEntry) allocation() *Allocation {
	loaded := e.loadedAt
	return &Allocation{
		Service:       e.service,
		Env:           e.env,
		RepoURL:       e.repoURL,
		Ref:           e.ref,
		Commit:        e.commit,
		Path:          e.worktree,
		MCPServerName: e.serverName,
		LoadedAt:      &loaded,
	}
}

// runRef 是 runID 名下的一次引用登记。
type runRef struct {
	entry     *wsEntry
	callerKey string
}

// Manager 管理共享工作区实例与引用计数；进程内单例（Default()）。
type Manager struct {
	mu      sync.Mutex
	root    string
	mirrors string
	entries map[string]*wsEntry           // service@commit → 共享实例
	runRefs map[string]map[string]*runRef // runID → service → 引用（ReleaseRun 索引）
	fetchMu map[string]*sync.Mutex        // mirrorPath → clone/fetch 互斥
	keyMu   map[string]*sync.Mutex        // entry key → 生命周期互斥（冷启动与回收串行）
}

var (
	defaultManager *Manager
	defaultOnce    sync.Once
)

// Default 返回进程级单例（首次调用时清理上次进程遗留的孤儿 worktree 目录）。
func Default() *Manager {
	defaultOnce.Do(func() {
		cfg := conf.GetReactRuntimeConfig().Workspace
		root, rootErr := filepath.Abs(cfg.RootDir)
		mirrors, mirrorsErr := filepath.Abs(cfg.MirrorDir)
		if rootErr != nil || mirrorsErr != nil {
			zlog.Warnf(nil, "[Workspace] 目录绝对化失败(使用原值): rootErr=%v, mirrorsErr=%v", rootErr, mirrorsErr)
			root, mirrors = cfg.RootDir, cfg.MirrorDir
		}
		defaultManager = &Manager{
			root:    root,
			mirrors: mirrors,
			entries: map[string]*wsEntry{},
			runRefs: map[string]map[string]*runRef{},
			fetchMu: map[string]*sync.Mutex{},
			keyMu:   map[string]*sync.Mutex{},
		}
		defaultManager.cleanupOrphans()
	})
	return defaultManager
}

// Load 解析服务代码并接入 (service, commit) 共享工作区。同一 run 内同一 service 幂等；
// 热路径（实例已就绪）零 git 操作、零 MCP 拉起，冷路径仅首个加载方创建 worktree 与 repo MCP。
func (m *Manager) Load(ctx *gin.Context, runID, callerKey, service, env string) (*Allocation, error) {
	if !conf.GetReactRuntimeConfig().Workspace.WorkspaceEnabled() {
		return nil, components.ErrorParamInvalid.Sprintf("workspace 未启用（llm.react.workspace.enabled）")
	}
	service = strings.TrimSpace(service)
	env = strings.TrimSpace(env)
	if service == "" || !servicePattern.MatchString(service) {
		return nil, components.ErrorParamInvalid.Sprintf("service 仅允许字母数字下划线中划线: %q", service)
	}
	if env != "" && !servicePattern.MatchString(env) {
		return nil, components.ErrorParamInvalid.Sprintf("env 仅允许字母数字下划线中划线: %q", env)
	}

	m.mu.Lock()
	if ref, ok := m.runRefs[runID][service]; ok { // 同 run 同 service：幂等复用，不重复计数
		m.mu.Unlock()
		alloc := ref.entry.allocation()
		alloc.CallerKey = callerKey
		return alloc, nil
	}
	m.mu.Unlock()

	repoURL, ref, err := Resolve(service, env)
	if err != nil {
		return nil, err
	}
	mirrorPath, err := m.ensureMirror(repoURL)
	if err != nil {
		return nil, err
	}
	commit, err := m.resolveCommit(mirrorPath, ref)
	if err != nil {
		return nil, err
	}
	// worktree 必须用绝对路径：git 在 mirror 目录内执行，相对路径会相对 mirror 解析。
	worktree, err := filepath.Abs(filepath.Join(m.root, service, commit[:12]))
	if err != nil {
		return nil, err
	}
	// server 名受 mcpclient validateServerName 的 32 字符上限约束（ws_ + service + _ + commit8），
	// 超长 service 提前给出可读错误，而不是挂载阶段才报晦涩失败。
	serverName := fmt.Sprintf("ws_%s_%s", service, commit[:8])
	if len(serverName) > 32 {
		return nil, components.ErrorParamInvalid.Sprintf("service 名过长: %q（server 名 %q 超出 MCP 32 字符上限，service 须 ≤ 20 字符）", service, serverName)
	}

	key := service + "@" + commit
	m.mu.Lock()
	if ref, ok := m.runRefs[runID][service]; ok { // 解析期间同 run 已加载：复用，避免双计数
		m.mu.Unlock()
		alloc := ref.entry.allocation()
		alloc.CallerKey = callerKey
		return alloc, nil
	}
	e, ok := m.entries[key]
	if !ok {
		e = &wsEntry{
			key:        key,
			service:    service,
			env:        env,
			repoURL:    repoURL,
			ref:        ref,
			commit:     commit,
			mirrorPath: mirrorPath,
			worktree:   worktree,
			serverName: serverName,
			loadedAt:   time.Now(),
			holders:    map[string]int{},
		}
		m.entries[key] = e
	}
	if m.runRefs[runID] == nil {
		m.runRefs[runID] = map[string]*runRef{}
	}
	m.runRefs[runID][service] = &runRef{entry: e, callerKey: callerKey}
	e.holders[callerKey]++
	m.mu.Unlock()

	if err := e.ensureStarted(m); err != nil {
		m.rollbackRegistration(runID, callerKey, service, e)
		return nil, err
	}
	// 每个 caller 一份工具副本（名字 ws_<service>_<commit8>_<tool>）。
	if _, err := syncCallerTools(callerKey, e.serverName, false); err != nil {
		zlog.Warnf(ctx, "[Workspace] 工具同步失败(尝试继续): server=%s, err=%v", e.serverName, err)
	}

	zlog.Infof(ctx, "[Workspace] 工作区就绪: runId=%s, service=%s@%s(ref=%s), path=%s, tools=%s_*", runID, service, commit, ref, worktree, e.serverName)
	alloc := e.allocation()
	alloc.CallerKey = callerKey
	return alloc, nil
}

// ReleaseRun 释放一个 run 的全部工作区引用（幂等，run 终态统一调用）：
// caller 计数归零时清理该 caller 的工具副本；总引用归零时回收 worktree 与 repo MCP。
// IO（DB/进程/目录）在锁外执行，不阻塞其它 Load/Release。
func (m *Manager) ReleaseRun(ctx *gin.Context, runID string) {
	m.mu.Lock()
	refs := m.runRefs[runID]
	delete(m.runRefs, runID)

	type toolCleanup struct{ server, caller string }
	var toolCleanups []toolCleanup
	var teardowns []*wsEntry
	for _, ref := range refs {
		e := ref.entry
		if n, ok := e.holders[ref.callerKey]; ok {
			if n <= 1 {
				delete(e.holders, ref.callerKey)
				toolCleanups = append(toolCleanups, toolCleanup{server: e.serverName, caller: ref.callerKey})
			} else {
				e.holders[ref.callerKey] = n - 1
			}
		}
		if len(e.holders) == 0 {
			if _, live := m.entries[e.key]; live {
				delete(m.entries, e.key)
				teardowns = append(teardowns, e)
			}
		}
	}
	m.mu.Unlock()

	for _, c := range toolCleanups {
		// 先摘工具副本（模型侧 get_tool 不再命中），再由 teardown 回收 server 与 worktree。
		if _, err := removeCallerTools(ctx, c.server, c.caller); err != nil {
			zlog.Warnf(ctx, "[Workspace] 清理工具副本失败(忽略): server=%s, err=%v", c.server, err)
		}
	}
	for _, e := range teardowns {
		m.teardown(ctx, e)
	}
}

// rollbackRegistration 撤销 Load 的登记（冷启动失败路径）：减 run/caller 引用；
// 总引用归零时把 entry 从索引摘除（未 started，无 worktree/MCP 需要回收）。
func (m *Manager) rollbackRegistration(runID, callerKey, service string, e *wsEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if refs, ok := m.runRefs[runID]; ok && refs[service] != nil && refs[service].entry == e {
		delete(refs, service)
		if len(refs) == 0 {
			delete(m.runRefs, runID)
		}
	}
	if n, ok := e.holders[callerKey]; ok {
		if n <= 1 {
			delete(e.holders, callerKey)
		} else {
			e.holders[callerKey] = n - 1
		}
	}
	if len(e.holders) == 0 {
		delete(m.entries, e.key)
	}
}

// ActiveEntry 是管理面运行视图条目（/react/workspace/active）：一个 (service, commit)
// 共享实例及其引用方。多 run 共享后，视图按实例聚合而非按 run 罗列。
type ActiveEntry struct {
	Service string         `json:"service"`
	Commit  string         `json:"commit"`
	Ref     string         `json:"ref"`
	Path    string         `json:"path"`
	Server  string         `json:"server"`
	Tools   string         `json:"tools"`
	Runs    []string       `json:"runs"`
	Callers map[string]int `json:"callers"`
	Loaded  *time.Time     `json:"loadedAt"`
}

// ActiveSnapshot 返回当前活跃共享实例清单（值拷贝，含引用 run、caller 计数与挂载时间）。
func (m *Manager) ActiveSnapshot() []ActiveEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	runsByKey := make(map[string][]string, len(m.runRefs))
	for runID, refs := range m.runRefs {
		for _, ref := range refs {
			runsByKey[ref.entry.key] = append(runsByKey[ref.entry.key], runID)
		}
	}
	snapshot := make([]ActiveEntry, 0, len(m.entries))
	for _, e := range m.entries {
		callers := make(map[string]int, len(e.holders))
		for k, v := range e.holders {
			callers[k] = v
		}
		loaded := e.loadedAt
		snapshot = append(snapshot, ActiveEntry{
			Service: e.service,
			Commit:  e.commit,
			Ref:     e.ref,
			Path:    e.worktree,
			Server:  e.serverName,
			Tools:   e.serverName + "_*",
			Runs:    runsByKey[e.key],
			Callers: callers,
			Loaded:  &loaded,
		})
	}
	sort.Slice(snapshot, func(i, j int) bool {
		if snapshot[i].Service != snapshot[j].Service {
			return snapshot[i].Service < snapshot[j].Service
		}
		return snapshot[i].Commit < snapshot[j].Commit
	})
	return snapshot
}

// ensureStarted 冷启动共享实例（幂等）：创建 worktree 与 repo MCP 子进程。
// 生命周期锁按 entry key 序列化（跨 entry 对象）：首个加载方创建，后来者等锁后经 started
// 双检直接返回；也防止与旧实例的 teardown 交叉重建同一 worktree 路径。
// worktree 残留只可能来自异常退出（key 锁下无人同时在用该路径），强删安全。
func (e *wsEntry) ensureStarted(m *Manager) error {
	lock := m.entryLock(e.key)
	lock.Lock()
	defer lock.Unlock()
	if e.started {
		return nil
	}
	if _, err := runGitIn(e.mirrorPath, "worktree", "add", "--detach", e.worktree, e.commit); err != nil {
		_, _ = runGitIn(e.mirrorPath, "worktree", "remove", "--force", e.worktree)
		if _, err := runGitIn(e.mirrorPath, "worktree", "add", "--detach", e.worktree, e.commit); err != nil {
			return components.ErrorToolExecFailed.Sprintf("创建 worktree 失败: %v", err)
		}
	}
	if _, err := ensureRepoServer(mcpclient.ServerConfig{
		Name: e.serverName,
		Kind: "repo",
		Env:  map[string]string{"REPO_ROOT": e.worktree},
	}); err != nil {
		_, _ = runGitIn(e.mirrorPath, "worktree", "remove", "--force", e.worktree)
		return components.ErrorToolExecFailed.Sprintf("挂载 repo MCP 失败: %v", err)
	}
	e.started = true
	return nil
}

// teardown 回收共享实例（总引用归零时调用）：停 repo MCP、删 worktree 与空服务目录。
// 与 ensureStarted 共用 entry key 生命周期锁：旧实例回收与新实例重建同一 worktree 路径
// 不可能交叉执行。
func (m *Manager) teardown(ctx *gin.Context, e *wsEntry) {
	lock := m.entryLock(e.key)
	lock.Lock()
	defer lock.Unlock()
	if err := removeRepoServer(e.serverName); err != nil {
		zlog.Warnf(ctx, "[Workspace] 停止 repo MCP 失败(忽略): server=%s, err=%v", e.serverName, err)
	}
	if err := m.git(e.mirrorPath, "worktree", "remove", "--force", e.worktree); err != nil {
		_ = os.RemoveAll(e.worktree)
		_ = m.git(e.mirrorPath, "worktree", "prune")
	}
	_ = os.Remove(filepath.Dir(e.worktree)) // 服务目录空了顺带删（非空则失败，忽略）
	zlog.Infof(ctx, "[Workspace] 共享工作区已回收: service=%s@%s", e.service, e.commit)
}

// Resolve 按 service+env 查静态白名单：env 命中 refs 则用之，否则回退 default_ref（空为 HEAD）。
func Resolve(service, env string) (repoURL, ref string, err error) {
	cfg := conf.GetReactRuntimeConfig().Workspace
	for _, item := range cfg.Resolvers {
		if item.Service != service {
			continue
		}
		if env != "" {
			if r, ok := item.Refs[env]; ok && strings.TrimSpace(r) != "" {
				return item.RepoURL, strings.TrimSpace(r), nil
			}
		}
		return item.RepoURL, item.DefaultRef, nil
	}
	known := make([]string, 0, len(cfg.Resolvers))
	for _, item := range cfg.Resolvers {
		known = append(known, item.Service)
	}
	sort.Strings(known)
	return "", "", components.ErrorParamInvalid.Sprintf("service %q 未在 workspace.resolvers 白名单中，已知服务: %s", service, strings.Join(known, ", "))
}

// mirrorPath 由仓库 URL 推导 bare mirror 目录（同名仓库稳定映射）。
func (m *Manager) mirrorPath(repoURL string) string {
	name := strings.TrimSuffix(filepath.Base(strings.TrimRight(repoURL, "/")), ".git")
	if name == "" || !servicePattern.MatchString(name) {
		name = fmt.Sprintf("repo%d", len(repoURL))
	}
	return filepath.Join(m.mirrors, name+".git")
}

// fetchLock 返回 mirror 级 clone/fetch 互斥锁（懒创建）。
func (m *Manager) fetchLock(mirror string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.fetchMu[mirror]
	if !ok {
		lock = &sync.Mutex{}
		m.fetchMu[mirror] = lock
	}
	return lock
}

// entryLock 返回 entry key 级生命周期互斥锁（懒创建）：序列化同 key 的冷启动（ensureStarted）
// 与回收（teardown）。锁按 key 而非 entry 对象索引——回收窗口内新建的同 key entry 也要与
// 旧实例的物理回收互斥，防止重建同一 worktree 路径时交叉执行。锁条目不随 entry 回收删除
// （删锁与持锁等待方之间存在竞态），数量以不同 (service, commit) 组合为界，量级可控。
func (m *Manager) entryLock(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.keyMu[key]
	if !ok {
		lock = &sync.Mutex{}
		m.keyMu[key] = lock
	}
	return lock
}

// ensureMirror 保证 bare mirror 存在并刷新到远端最新。同一 mirror 的 clone/fetch 由
// fetchMu 串行化：并发 fetch 同一 mirror 会抢 git .lock 直接报错（锁内含网络耗时，
// 不同 mirror 之间仍并行）。
func (m *Manager) ensureMirror(repoURL string) (string, error) {
	mirror := m.mirrorPath(repoURL)
	lock := m.fetchLock(mirror)
	lock.Lock()
	defer lock.Unlock()

	if info, err := os.Stat(filepath.Join(mirror, "HEAD")); err == nil && !info.IsDir() {
		if err := m.git(mirror, "fetch", "--prune", "origin"); err != nil {
			return "", components.ErrorToolExecFailed.Sprintf("mirror 刷新失败(%s): %v", repoURL, err)
		}
		return mirror, nil
	}
	if err := os.MkdirAll(m.mirrors, 0o755); err != nil {
		return "", err
	}
	if err := m.git("", "clone", "--mirror", repoURL, mirror); err != nil {
		return "", components.ErrorToolExecFailed.Sprintf("mirror 克隆失败(%s): %v", repoURL, err)
	}
	return mirror, nil
}

// resolveCommit 在 mirror 内把 ref（分支/commit/tag，空为 HEAD）解析为完整 commit。
// ref 仅作为独立 argv 直传（已过 refPattern 字符校验），rev-parse 输出再按 40 位十六进制校验。
func (m *Manager) resolveCommit(mirrorPath, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref != "" && !refPattern.MatchString(ref) {
		return "", components.ErrorParamInvalid.Sprintf("ref 含非法字符: %q", ref)
	}
	args := make([]string, 0, 2)
	args = append(args, "rev-parse")
	if ref == "" {
		args = append(args, "HEAD")
	} else {
		args = append(args, ref)
	}
	out, err := m.gitOutput(mirrorPath, args...)
	if err != nil {
		return "", components.ErrorToolExecFailed.Sprintf("ref %q 解析失败: %v", ref, err)
	}
	// rev-parse 可能输出多行（如 tag 链），取首行并校验 commit 格式。
	firstLine := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if !commitPattern.MatchString(firstLine) {
		return "", components.ErrorToolExecFailed.Sprintf("ref %q 解析结果非法: %q", ref, firstLine)
	}
	return firstLine, nil
}

// git 在指定目录执行白名单内的 git 子命令。
func (m *Manager) git(dir string, args ...string) error {
	_, err := runGitIn(dir, args...)
	return err
}

func (m *Manager) gitOutput(dir string, args ...string) ([]byte, error) {
	return runGitIn(dir, args...)
}

// runGitIn 执行白名单内的 git 子命令。安全属性：
//   - 可执行文件是编译期常量 "git"，不经 shell（无 sh -c），argv 直传内核；
//   - 子命令必须在 gitSubcommands 白名单内；
//   - 全部动态参数（ref/commit/service/路径）在进入本函数前均已通过字符正则校验
//     （禁止空白与 shell 元字符），路径参数由 filepath.Join 生成且各段已校验。
func runGitIn(dir string, args ...string) ([]byte, error) {
	if len(args) == 0 || !gitSubcommands[args[0]] {
		return nil, fmt.Errorf("git subcommand not allowed: %q", args[0])
	}
	timeout := conf.GetReactRuntimeConfig().Workspace.GitTimeoutSec
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

// cleanupOrphans 清理上次进程遗留的 worktree 目录：worktree 生命周期由引用计数管理，
// 进程重启意味着全部 run 已终态（active 索引丢失），目录级全清即可。
func (m *Manager) cleanupOrphans() {
	if err := os.RemoveAll(m.root); err != nil {
		zlog.Warnf(nil, "[Workspace] 孤儿工作区清理失败(忽略): %v", err)
	}
}
