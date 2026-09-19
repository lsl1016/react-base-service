---
title: Workspace 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: workspace
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/react/workspace_view.go
  - service/workspace/workspace.go
  - service/react/load_runtime_code.go
  - service/react/meta_tools.go
  - conf/config.go
summary: 服务端代码工作区的静态解析、bare mirror 缓存、每 run git worktree 分配释放与只读 repo MCP 工具挂载机制
---

# Workspace 模块功能文档

## 1. 模块概述

Workspace 模块为 ReAct Runtime 提供服务端代码工作区：模型调用 `load_runtime_code` 后，按静态解析表把目标服务代码落到本次 run 专属的 git worktree，并动态挂载只读 repo MCP，产出 `ws_<service>_*` 前缀的代码检索工具。引擎不直接理解文件系统、Git 或仓库协议，代码检索能力以普通 Business Tool 形式经 `get_tool`/`execute_tool` 使用。

模块由三部分组成：`service/workspace` 负责 Git/worktree/MCP 生命周期；`service/react/load_runtime_code.go` 是引擎侧薄适配入口；`/react/workspace/active` 提供只读运行视图。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/react/workspace/active` | POST | 查询当前进程内活跃 worktree 清单 | `react.ListActiveWorkspaces` |

运行期入口是 Meta Tool `load_runtime_code`（`workspace.enabled=true` 时注册），不占 HTTP 路由。

## 3. 核心逻辑

### 3.1 分配流程

`Manager.Load` 按 runID 加互斥锁执行：先校验 `service`/`env` 仅允许字母数字下划线中划线，同一 run 内同一 service 幂等（直接返回既有 `Allocation`）；随后 `Resolve` 查 `workspace.resolvers` 静态白名单（env 命中 `refs` 则用之，否则回退 `default_ref`，空为 HEAD），`ensureMirror` 保证 bare mirror 存在并 `fetch --prune` 刷新，`resolveCommit` 用 `rev-parse` 把 ref 解析为 40 位十六进制 commit；最后在 `<root_dir>/<runID>/<service>` 执行 `worktree add --detach`（异常残留先 force remove 再重试一次），并调用 `mcpclient.EnsureServer` 拉起名为 `ws_<service>` 的 repo MCP（`REPO_ROOT` 指向 worktree），`SyncServerRegistryScoped` 把工具副本同步到该 caller（toolId 带 `__<callerKey>` 后缀）。

### 3.2 释放语义

worktree 生命周期不超过单个 run：run 终态时 `runtime.go` 的 run 收尾与 `delegate.go` 的子 run 收尾均调用 `ReleaseRun`（幂等，重复调用无副作用）。单个释放顺序为：先 `RemoveRegistryToolsForCaller` 摘除工具副本、`RemoveServer` 停掉 repo MCP（模型侧 `get_tool` 不再命中），再 `worktree remove --force` 回收目录（失败兜底 `os.RemoveAll` + `worktree prune`），顺带删除空的 runID 目录。进程重启意味着全部 run 已终态，`Default()` 首次调用时 `cleanupOrphans` 直接清空整个 root 目录。

### 3.3 代码检索作为普通 Business Tool

挂载的 `ws_<service>_*` 工具共 8 个：`list_files`、`read_file`、`search_code`、`search_pattern`、`find_symbol`、`get_file_symbols`、`find_references`、`get_repo_map`，其中符号类工具为 go/ast 声明感知实现，由 `cmd/repo-mcp` 构建的 stdio 适配器提供。这些工具与手工登记的 Business Tool 同构，仍走 `get_tool` 加载 → `execute_tool` 执行两段式协议，受 caller 工具可见性约束；ReAct 主循环无任何 Workspace 特殊执行分支。只读语义由 repo-mcp 工具集保证（无写工具）。

### 3.4 未启用行为与安全约束

`workspace.enabled` 未配置默认 false：不注册 `load_runtime_code`（模型不可见），`Load` 返回参数错误"workspace 未启用"，`/react/workspace/active` 恒返回空数组（空列表为正常态）。安全约束：git 子命令白名单仅 `clone`/`fetch`/`worktree`/`rev-parse`；ref 字符校验 `^[a-zA-Z0-9._/\-]+$`（拒绝空白与元字符）；`exec.Command` argv 直传不经 shell；resolver 为静态白名单，不接收任意仓库地址；单次 git 子操作超时 `git_timeout_sec`（默认 180 秒）超时 kill 进程。

## 4. 数据模型

本模块不新增数据表。运行态仅存在于进程内：`Manager.active` 以 runID → `[]*Allocation` 索引；`ActiveSnapshot` 输出值拷贝供管理面展示（runId、service、ref、commit、path、工具前缀、callerKey、loadedAt）。文件系统布局：`<root_dir>/<runID>/<service>` 为 worktree，`<mirror_dir>/<repo>.git` 为共享对象库的 bare mirror（全服务单副本）。

## 5. 配置项

`llm.react.workspace`（`conf/mount/custom.yaml`）：

| 配置 | 默认值 | 说明 |
|---|---|---|
| `enabled` | false | 总开关，未配置时不注册 `load_runtime_code` |
| `root_dir` | `./data/workspaces` | 每 run worktree 分配根目录 |
| `mirror_dir` | `./data/repo-cache` | bare mirror 缓存目录 |
| `git_timeout_sec` | 180 | 单次 git 子操作超时秒数 |
| `resolvers[]` | 空 | 静态解析表：`service`、`repo_url`、`refs`（env → ref 映射）、`default_ref` |

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 Workspace 模块文档基线 |
