package workspace

// 代码工作区共享化集成测试（WORKSPACE_IT=1，真实网络 + 真实 repo-mcp 子进程）：
//
//	WORKSPACE_IT=1 go test ./service/workspace/ -run TestWorkspaceSharingIntegration -v -count=1 -timeout 600s
//
// 与单测（mcpclient 全替身）不同，本测试走完整真实路径：
//   - git clone --mirror 两个开源仓库（sirupsen/logrus、google/uuid），uuid 另用 v1.0.0
//     tag 模拟不同 env → 不同 commit 的多实例并存；
//   - ensureRepoServer/removeRepoServer 用真实 mcpclient 实现（仅包裹计数），
//     bin/repo-mcp 子进程真实拉起，ListTools/CallTool 走真实 stdio JSON-RPC；
//   - 仅 syncCallerTools/removeCallerTools（注册表 DB）用替身；
//   - 多会话（run）/多 caller 并发 Load、并发 CallTool 同一共享 server，
//     验证共享单实例、真实检索隔离、两级释放与回收后再加载。
//
// 前置：仓库根目录存在可执行的 bin/repo-mcp（go build -o bin/repo-mcp ./cmd/repo-mcp）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"react-base-service/conf"
	"react-base-service/service/mcpclient"

	"github.com/gin-gonic/gin"
)

const (
	itSvcLogrus = "logrus"
	itSvcUUID   = "uuid"
)

// itCounters 包裹真实 ensure/remove 计数（并发安全）。
type itCounters struct {
	mu      sync.Mutex
	ensure  []string
	remove  []string
	synced  map[string]int
	cleaned map[string]int
}

// setupIntegration 注册两个开源仓库的白名单、包裹真实 server 生命周期触点（仅计数）、
// 桩掉 DB 注册表同步，并把 cwd 切到仓库根（bin/repo-mcp 为相对路径）。
func setupIntegration(t *testing.T) (*Manager, *itCounters) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// service/workspace → 仓库根（与 react e2e 同一约定）。
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	if _, err := os.Stat(filepath.Join("bin", "repo-mcp")); err != nil {
		t.Fatalf("bin/repo-mcp 不存在（先 go build -o bin/repo-mcp ./cmd/repo-mcp）: %v", err)
	}

	enabled := true
	origWS := conf.CustomConf.LLM.React.Workspace
	conf.CustomConf.LLM.React.Workspace = conf.ReactWorkspaceConfig{
		Enabled:       &enabled,
		GitTimeoutSec: 300, // 真实网络 clone/fetch
		Resolvers: []conf.ReactWorkspaceResolverConf{
			{Service: itSvcLogrus, RepoURL: "https://github.com/sirupsen/logrus.git"},
			{Service: itSvcUUID, RepoURL: "https://github.com/google/uuid.git", Refs: map[string]string{"old": "v1.0.0"}},
		},
	}
	t.Cleanup(func() { conf.CustomConf.LLM.React.Workspace = origWS })

	counters := &itCounters{synced: map[string]int{}, cleaned: map[string]int{}}
	origEnsure, origRemove, origSync, origClean := ensureRepoServer, removeRepoServer, syncCallerTools, removeCallerTools
	ensureRepoServer = func(cfg mcpclient.ServerConfig) (mcpclient.Server, error) {
		server, err := mcpclient.EnsureServer(cfg) // 真实子进程
		counters.mu.Lock()
		counters.ensure = append(counters.ensure, cfg.Name)
		counters.mu.Unlock()
		return server, err
	}
	removeRepoServer = func(name string) error {
		err := mcpclient.RemoveServer(name) // 真实停进程
		counters.mu.Lock()
		counters.remove = append(counters.remove, name)
		counters.mu.Unlock()
		return err
	}
	syncCallerTools = func(callerKey, serverName string, primary bool) (int, error) {
		counters.mu.Lock()
		counters.synced[callerKey]++
		counters.mu.Unlock()
		return 0, nil
	}
	removeCallerTools = func(ctx *gin.Context, serverName, callerKey string) (int, error) {
		counters.mu.Lock()
		counters.cleaned[callerKey]++
		counters.mu.Unlock()
		return 0, nil
	}
	t.Cleanup(func() {
		ensureRepoServer, removeRepoServer, syncCallerTools, removeCallerTools = origEnsure, origRemove, origSync, origClean
	})

	return newTestManager(t), counters
}

func (c *itCounters) ensureCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.ensure)
}

func (c *itCounters) removeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.remove)
}

// itCallTool 在指定共享 server 上执行一次真实工具调用。
func itCallTool(t *testing.T, server, tool string, args map[string]any) string {
	t.Helper()
	client := mcpclient.GetServer(server)
	if client == nil {
		t.Fatalf("server %s 不在运行", server)
	}
	raw, _ := json.Marshal(args)
	out, err := client.CallTool(tool, raw, 30*time.Second)
	if err != nil {
		t.Fatalf("CallTool %s/%s 失败: %v\n%s", server, tool, err, out)
	}
	return out
}

func TestWorkspaceSharingIntegration(t *testing.T) {
	if os.Getenv("WORKSPACE_IT") == "" {
		t.Skip("set WORKSPACE_IT=1 to run workspace integration test (network + bin/repo-mcp required)")
	}
	m, counters := setupIntegration(t)
	ctx := &gin.Context{}

	// ---- 阶段 1：两个会话（不同 caller）加载同一 service → 共享单实例 ----
	allocA, err := m.Load(ctx, "run-a1", "caller-a", itSvcLogrus, "")
	if err != nil {
		t.Fatalf("run-a1 加载 logrus 失败: %v", err)
	}
	allocB, err := m.Load(ctx, "run-b1", "caller-b", itSvcLogrus, "")
	if err != nil {
		t.Fatalf("run-b1 加载 logrus 失败: %v", err)
	}
	if allocA.MCPServerName != allocB.MCPServerName || allocA.Path != allocB.Path {
		t.Fatalf("两会话应共享同一实例: %+v vs %+v", allocA, allocB)
	}
	if counters.ensureCount() != 1 {
		t.Fatalf("共享实例应只冷启动一次: %d", counters.ensureCount())
	}
	if mcpclient.GetServer(allocA.MCPServerName) == nil {
		t.Fatalf("共享 server 应在运行: %s", allocA.MCPServerName)
	}

	// ---- 阶段 2：同 service 不同 env（tag v1.0.0）→ 独立实例并存 ----
	allocUUIDNew, err := m.Load(ctx, "run-a2", "caller-a", itSvcUUID, "")
	if err != nil {
		t.Fatalf("加载 uuid 默认分支失败: %v", err)
	}
	allocUUIDOld, err := m.Load(ctx, "run-b2", "caller-b", itSvcUUID, "old")
	if err != nil {
		t.Fatalf("加载 uuid v1.0.0 失败: %v", err)
	}
	if allocUUIDNew.Commit == allocUUIDOld.Commit || allocUUIDNew.Path == allocUUIDOld.Path {
		t.Fatalf("不同 env 应解析到不同 commit/实例: %s vs %s", allocUUIDNew.Commit, allocUUIDOld.Commit)
	}
	if want := 3; counters.ensureCount() != want {
		t.Fatalf("冷启动数应恰为 %d（logrus + uuid 默认 + uuid old）: %d", want, counters.ensureCount())
	}

	// ---- 阶段 3：真实 repo-mcp 工具调用（共享实例上的真实检索） ----
	tools, err := mcpclient.GetServer(allocA.MCPServerName).ListTools()
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	if len(tools) < 8 {
		t.Fatalf("repo-mcp 应提供至少 8 个检索工具: %d", len(tools))
	}
	if out := itCallTool(t, allocA.MCPServerName, "find_symbol", map[string]any{"name": "Formatter", "max_results": 5}); !strings.Contains(out, "Formatter") {
		t.Fatalf("find_symbol Formatter 结果异常: %s", truncateForLogIT(out, 300))
	}
	if out := itCallTool(t, allocA.MCPServerName, "read_file", map[string]any{"path": "entry.go", "start_line": 1, "end_line": 20}); !strings.Contains(out, "logrus") && !strings.Contains(out, "package") {
		t.Fatalf("read_file entry.go 结果异常: %s", truncateForLogIT(out, 300))
	}
	// commit 隔离：v1.0.0 实例读的是旧快照（uuid.go 早期版本），默认分支实例读新快照。
	if out := itCallTool(t, allocUUIDOld.MCPServerName, "read_file", map[string]any{"path": "uuid.go", "start_line": 1, "end_line": 30}); !strings.Contains(out, "uuid") {
		t.Fatalf("uuid v1.0.0 read_file 结果异常: %s", truncateForLogIT(out, 300))
	}

	// ---- 阶段 4：并发多会话 Load（既有实例全热路径）+ 并发 CallTool 同一共享 server ----
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runID := fmt.Sprintf("run-c%d", i)
			caller := "caller-a"
			if i%2 == 1 {
				caller = "caller-b"
			}
			svc, env := itSvcLogrus, ""
			if i%3 == 0 {
				svc, env = itSvcUUID, ""
			}
			if _, err := m.Load(ctx, runID, caller, svc, env); err != nil {
				t.Errorf("并发 Load %s 失败: %v", runID, err)
			}
		}(i)
	}
	// 同一共享 logrus server 上并发真实检索（stdio 客户端按 c.mu 串行化请求）。
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			query := "Formatter"
			if i%2 == 1 {
				query = "Entry"
			}
			out, err := mcpclient.GetServer(allocA.MCPServerName).CallTool(
				"search_code", mustJSONIT(map[string]any{"query": query, "max_results": 5}), 30*time.Second)
			if err != nil {
				t.Errorf("并发 search_code 失败: %v", err)
				return
			}
			if !strings.Contains(out, query) {
				t.Errorf("并发 search_code(%s) 结果异常: %s", query, truncateForLogIT(out, 200))
			}
		}(i)
	}
	wg.Wait()
	if want := 3; counters.ensureCount() != want {
		t.Fatalf("热路径不应新增冷启动: want %d, got %d", want, counters.ensureCount())
	}
	totalLoads := 2 + 2 + 6
	t.Logf("共 %d 次加载共享 %d 个实例（%d 次冷启动）", totalLoads, 3, counters.ensureCount())

	// ---- 阶段 5：两级释放（caller 归零清副本、总归零回收实例） ----
	// 持有者账目：logrus = {run-a1, run-b1, run-c1, run-c2, run-c4, run-c5}；
	// uuid 默认 = {run-a2, run-c0, run-c3}；uuid old = {run-b2}。
	m.ReleaseRun(ctx, "run-a1")
	m.ReleaseRun(ctx, "run-b1")
	if mcpclient.GetServer(allocA.MCPServerName) == nil || counters.removeCount() != 0 {
		t.Fatalf("run-c* 仍持有 logrus，server 不应被回收: remove=%d", counters.removeCount())
	}
	// 逐个释放 logrus 持有者：倒数第二个释放后仍存活，最后一个释放后回收。
	for _, runID := range []string{"run-c1", "run-c2", "run-c4"} {
		m.ReleaseRun(ctx, runID)
		if mcpclient.GetServer(allocA.MCPServerName) == nil {
			t.Fatalf("%s 释放后 run-c5 仍持有 logrus，不应回收", runID)
		}
	}
	m.ReleaseRun(ctx, "run-c5")
	if mcpclient.GetServer(allocA.MCPServerName) != nil {
		t.Fatal("最后一个持有者释放后 logrus server 应被回收")
	}
	if _, err := os.Stat(allocA.Path); !os.IsNotExist(err) {
		t.Fatalf("logrus worktree 应已删除: %v", err)
	}
	if want := 1; counters.removeCount() != want {
		t.Fatalf("此刻应只回收 logrus 一个实例: want %d, got %d", want, counters.removeCount())
	}
	// uuid old 仅 run-b2 持有：释放即回收；uuid 默认仍有三个持有者，不受影响。
	m.ReleaseRun(ctx, "run-b2")
	if mcpclient.GetServer(allocUUIDOld.MCPServerName) != nil {
		t.Fatal("uuid v1.0.0 唯一持有者释放后应回收")
	}
	if mcpclient.GetServer(allocUUIDNew.MCPServerName) == nil {
		t.Fatal("uuid 默认分支实例不应被连带回收")
	}
	// 清空 uuid 默认分支的三个持有者。
	m.ReleaseRun(ctx, "run-a2")
	m.ReleaseRun(ctx, "run-c0")
	if mcpclient.GetServer(allocUUIDNew.MCPServerName) == nil {
		t.Fatal("run-c3 仍持有 uuid 默认实例，不应回收")
	}
	m.ReleaseRun(ctx, "run-c3")
	if snap := m.ActiveSnapshot(); len(snap) != 0 {
		t.Fatalf("全部释放后快照应为空: %+v", snap)
	}
	if entries, _ := os.ReadDir(m.root); len(entries) != 0 {
		t.Fatalf("全部释放后根目录应为空: %v", entries)
	}
	if want := 3; counters.removeCount() != want {
		t.Fatalf("应回收全部 3 个实例: want %d, got %d", want, counters.removeCount())
	}

	// ---- 阶段 6：回收后再加载 → 干净的重新冷启动（teardown/重建路径回归） ----
	allocZ, err := m.Load(ctx, "run-z", "caller-a", itSvcLogrus, "")
	if err != nil {
		t.Fatalf("回收后再加载失败: %v", err)
	}
	if want := 4; counters.ensureCount() != want {
		t.Fatalf("回收后再加载应重新冷启动: want %d, got %d", want, counters.ensureCount())
	}
	if out := itCallTool(t, allocZ.MCPServerName, "search_code", map[string]any{"query": "TextFormatter", "max_results": 3}); !strings.Contains(out, "TextFormatter") {
		t.Fatalf("重建实例检索异常: %s", truncateForLogIT(out, 300))
	}
	m.ReleaseRun(ctx, "run-z")
	if want := 4; counters.removeCount() != want {
		t.Fatalf("run-z 释放后累计回收应为 4: got %d", counters.removeCount())
	}
	t.Logf("集成验证通过：10 次加载共享 3 个实例（冷启动 3 次 + 回收后重建 1 次），全部实例回收，检索隔离正确")
}

func mustJSONIT(v map[string]any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func truncateForLogIT(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
