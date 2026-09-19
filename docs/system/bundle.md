---
title: Bundle 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: bundle
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/react/bundle.go
  - service/bundle/bundle.go
  - service/bundle/fetch.go
  - models/llm/bundle.go
  - components/params/bundle.go
summary: Agent Bundle 插件包安装：从白名单来源拉取 manifest + agents + skills + .mcp.json 打包，展开写入 Agent/Skill/MCP 注册表并支持按快照逆序回滚卸载
---

# Bundle 模块功能文档

## 1. 模块概述

Bundle 模块实现 Agent Bundle 插件包（P3）的安装、卸载与清单查询。一个 bundle 是一个包含 manifest、`agents/*.md`、`skills/<name>/SKILL.md`、`.mcp.json` 的目录打包；安装时按资源逐条展开写入既有注册表（Agent、Skill、MCP Server），并把每条资源的覆盖前快照记入资源清单；卸载按清单逆序回滚。

来源仅限白名单前缀命中的内部 git URL 或本地路径，不做公网 marketplace。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/react/bundle/install` | POST | 安装 bundle（展开写入注册表，失败自动回滚） | `react.InstallBundle` |
| `/react-base-service/react/bundle/uninstall` | POST | 卸载 bundle（按资源清单逆序回滚） | `react.UninstallBundle` |
| `/react-base-service/react/bundle/list` | POST | 列出已安装 bundle 及资源计数 | `react.ListBundles` |

## 3. 核心逻辑

### 3.1 来源校验与拉取解析

安装入口 `bundleService.Install` 依次执行开关检查（`llm.react.bundle.enabled`，未配置默认关闭）、来源白名单校验（`validateBundleSource`，`allowed_source_prefixes` 空列表拒绝一切来源）、`callerKey` 非空校验（包内 agent/skill 未在 front-matter 声明归属时的兜底属主）。

`fetchBundle` 区分两类来源：

| 来源形态 | 判定 | 拉取方式 |
|---|---|---|
| 本地路径 | 字符串不含 `://` | 直接 `os.Stat` 校验为目录后读取 |
| git URL | 含 `://` | `cache_dir` 下 bare mirror 缓存（已存在则 `fetch --prune`，否则 `clone --mirror`），`rev-parse` 把 ref（空=HEAD）钉住为 40 位 commit，临时 worktree 检出后读取，读毕 `worktree remove --force` |

包布局与解析规则（`readBundleDir`）：

- manifest 取 `.plugin/plugin.json` 或根 `manifest.json`（先命中先用），仅解析 `name`/`version`/`description`；`name` 必须匹配 `^[a-z0-9]+(?:-[a-z0-9]+)*$`（kebab-case），`version` 为空时默认 `1.0.0`。
- `agents/` 收集顶层 `*.md`（跳过子目录、`.` 开头与 `__` 开头条目）；`skills/` 收集子目录下的 `SKILL.md`（跳过 `.` 开头与 `__MACOSX`，无 `SKILL.md` 的子目录跳过）；`.mcp.json` 可选。
- 三类资源全为空时拒绝安装。

解析限额（`fetch.go` 常量）：单个 agent/skill markdown 上限 1MB，`.mcp.json` 上限 256KB，manifest 上限 64KB，agents+skills 文件总数上限 64。解析全程纯内存，不落盘中间产物。

git 执行安全约束：子命令白名单仅 `clone`/`fetch`/`worktree`/`rev-parse`；`ref` 必须匹配 `^[a-zA-Z0-9._/\-]+$`；单次 git 子操作超时取 `git_timeout_sec`（未配置或非正时按 120 秒）。`repoPath`（monorepo 子目录）必须是包内相对路径，禁止绝对路径与 `..`。

### 3.2 安装展开与快照记录

`applyBundleResources` 按固定顺序应用资源并即时落资源清单行：先 agents（`agentService.UpsertFromMarkdownForBundle`），后 skills（`skillService.UpsertFromMarkdownForBundle`），最后 `.mcp.json` 内的 MCP 服务器。每条资源写入一行 `BundleResource`，`PreviousStateJSON` 为覆盖前的整行快照 JSON，空串表示该资源由本次安装新建。

展开只覆盖三类资源：agent、skill、mcp_server。bundle 不展开写入 tool、system_prompt、api_key、memory 等其他注册表资源。

同名覆盖语义：agent/skill 的 upsert 复用既有主键 `agent_id`/`skill_id`，只更新定义字段；软删的同名行复活复用（快照取软删行原值，含 `deleted_at`，卸载可还原为软删态）。普通 `/agent/import`、`/skill/import` 的同名报错语义不变，覆盖语义仅在 bundle 安装路径生效。

重装同名 bundle：`Install` 检测到同名活跃行时先调用 `Uninstall` 还原到旧装前状态，再装新包；软删的同名 bundle 行复活复用 `bundle_id`，并物理删除其旧资源清单行（`HardDeleteBundleResourcesByBundleID`，仅此场景物理删）。安装中途任一资源失败时自动执行 `uninstallByBundleID` 清理本次安装，不留半装状态；清理失败仅记日志，原错误照常返回。

MCP 注册约束（`parseBundleMcpServers`/`registerBundleMcpServer`）：`.mcp.json` 接受 `{"mcpServers": {name: {url, headers}}}` 或去掉外层的单个映射；`command`/`args` 形式直接拒绝（仅支持 url 形式 HTTP MCP）；`url` 必须以 `http://` 或 `https://` 开头；headers 序列化后上限 4KB；注册行 `timeout_ms` 固定 30000、`description` 固定 `installed by agent bundle`。MCP 客户端连接/重连失败（`reconnectMcpServer`）只记 Warn 日志，不阻断安装。

`InstallBundle` 控制器安装成功后返回列表项形态（`BundleResp`，复用 `BundleListItemResp`）。

### 3.3 卸载回滚

`Uninstall` 按 `name` 查活跃 bundle 行（不存在返回 `ErrorBundleNotFound`），`uninstallByBundleID` 按资源清单 `id` 逆序回滚（即 mcp → skills → agents，与安装顺序相反），最后软删 bundle 行；资源清单行保留作为历史，不随卸载删除。

单条资源回滚规则：

| 场景 | 行为 |
|---|---|
| `PreviousStateJSON` 为空（安装新建） | agent/skill：软删资源行；mcp_server：停客户端（`mcpclient.RemoveServer`）+ 清注册工具副本（`mcpclient.RemoveRegistryTools`）+ 软删行 |
| 快照非空（覆盖既有） | 按快照整行恢复（`UpdateXxxByXxxIDUnscoped`，含 `deleted_at`）；mcp_server 快照为软删态时回到软删态并停客户端清工具，快照为活跃行时按快照重连（`reconnectMcpServer`） |

任一条资源回滚失败立即返回错误中止卸载（`ErrorBundleImportInvalid`），剩余资源不再处理；bundle 行也不会被软删。

## 4. 数据模型

| 表 / 结构 | 说明 |
|---|---|
| `tblLlmBundle`（`model.Bundle`） | 安装记录：`bundle_id`（`bundle_` + 时间戳 + 纳秒 hex）、`name`（活跃行唯一）、`version`、`source`、`resolved_ref`（git 来源钉住的 commit，本地路径为空）、`manifest_json`（原始 manifest）、`installed_by`；软删 |
| `tblLlmBundleResource`（`model.BundleResource`） | 资源清单：`bundle_id`、`resource_type`（`agent`/`skill`/`mcp_server`）、`resource_key`（agent_key / skill name / mcp name）、`resource_id`、`previous_state_json`（覆盖前整行快照，空=新建）；无软删字段，仅重装复活时被物理删除 |

展开落库的目标表为 `tblLlmAgent`、`tblLlmSkill`、`tblLlmMcpServer`（各自模块维护）。

## 5. 配置项

YAML 配置位于 `llm.react.bundle`（`conf.ReactBundleConfig`）：

| 配置 | 说明 |
|---|---|
| `enabled` | 总开关，未配置默认 false，关闭时 `/react/bundle/*` 返回未启用错误 |
| `cache_dir` | bundle 源仓库 bare mirror 缓存目录 |
| `git_timeout_sec` | 单次 git 子操作超时秒数，未配置或非正按 120 |
| `allowed_source_prefixes` | 来源白名单前缀（git URL 与本地路径同表），空列表拒绝一切来源 |

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 初始版本 |
