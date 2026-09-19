---
title: GraphMemory 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: graph_memory
maintainer: react-base-service 项目组
status: active
related_code:
  - service/react/graph_memory.go
  - service/react/meta_tools.go
  - service/react/runtime.go
  - service/graphmemory/client.go
  - service/graphmemory/scope.go
  - conf/config.go
  - components/metrics/metrics.go
summary: Graphiti 时序事实图谱记忆的作用域分区、run 启动事实注入、检索与写入 Meta Tool 及外部 REST 存储边界
---

# GraphMemory 模块功能文档

## 1. 模块概述

GraphMemory 是 Layer2 时序事实图谱记忆：一个指向外部 Graphiti REST 服务的瘦 HTTP 客户端，加上基座作用域到 Graphiti `group_id` 的映射。它记录实体间关系、历史事件与随时间变化的状态，写入按 episode 异步抽取（受理即返回，数秒到数十秒后可检索），事实带 `valid/invalid` 时间窗。

与 Memory 模块（见 `docs/system/memory.md`）的分工：Memory 是 Layer1 偏好与约定层，MySQL 落库、同步生效、带修订审计与回滚；GraphMemory 是 Layer2 事实图谱层，数据存于外部 Graphiti、异步生效、无修订流水。注入块与工具描述均声明两者冲突时以 `<memory>` 为准，偏好类写入用 `memory_write`。

## 2. 接口清单

本模块无独立 HTTP 接口，是 ReAct 引擎内部能力。引擎侧入口如下：

| 入口 | 类型 | 功能 | 实现 |
|---|---|---|---|
| `graph_memory_search` | Meta Tool | 按自然语言查询检索图谱事实 | `executeGraphMemorySearch` |
| `graph_memory_write` | Meta Tool（需 `write.enabled`） | 把事件/结论写入图谱 | `executeGraphMemoryWrite` |
| run 初始化注入 | 引擎钩子 | 按用户输入装配 `<graph_memory>` 注入块 | `buildGraphMemoryContextForRun`（`prepareRuntimeRequest` 调用） |
| `graphmemory.Client` | HTTP 客户端 | Graphiti REST 调用 | `Search`（POST /search）、`AddEpisode`（POST /messages）、`Healthcheck`（GET /healthcheck） |

两个 Meta Tool 仅在 `graph_memory.enabled=true` 时注册（`internalMetaToolDefinitions`），并受执行档口 `AllowGraphMemory` 二次校验。

## 3. 核心逻辑

### 3.1 作用域与 group_id

`ResolveScope` 解析 run 的图谱分区：caller 级组做公共底座（检索可见、默认写入目标）；`group_scope=caller_user`（默认）时追加 `caller__user` 组并把写入目标切到 user 组。`SanitizeGroupID` 只保留 `[a-zA-Z0-9_]`（FalkorDB 全文检索解析器不接受连字符），清洗造成信息损失时追加 8 位 sha256 后缀防碰撞，总长截断到 64，空串回落 `default`。`group_ids` 由服务端按 run 作用域强制注入，模型入参不暴露组概念。

### 3.2 注入时机

`prepareRuntimeRequest` 阶段（`graph_memory.enabled` 且 `inject.enabled` 时）以本次 `UserPrompt` 为查询检索 `max_facts` 条事实，渲染成 `<graph_memory>` 块进入 system 内容（`buildReactSystemContent` 与 `<memory>` 并列）。每条事实一行（关系名 + 事实 + 时间窗），条数/字符双预算截断，超出部分提示改用 `graph_memory_search`。注入是增强不是依赖：检索失败、超时或空结果一律返回空串（token 零增量），绝不阻塞 run 启动。

### 3.3 工具读写

`graph_memory_search`：`query` 必填，`limit` 默认 10、上限 30；返回 `facts[]{fact, name, validAt, invalidAt}`，时间裁到日期（`2006-01-02`）。`graph_memory_write`：`name`/`content` 必填，`content` 上限 4000 字（rune 计）；组、来源描述（`react-base run <runID>`）、参考时间全部由服务端补齐，`RoleType=user`；Graphiti 受理即成功（202 异步抽取），返回 `queued=true` 并提示无需等待确认。指标：`GraphMemorySearchTotal`（ok/error/inject_ok/inject_error）、`GraphMemoryInjectFacts`、`GraphMemoryEpisodeTotal`。

### 3.4 存储边界

基座只依赖 Graphiti 官方 REST 契约（`POST /search`、`POST /messages`、`GET /healthcheck`），不镜像图数据、不直连图数据库。`SharedClient` 按 `(endpoint, timeout_ms)` 缓存进程级客户端，endpoint 为空返回 nil（视为未启用）。错误响应体读取限制 512 字节。

## 4. 数据模型

本模块不新增数据表：图谱数据（实体、关系、episode、事实时间窗）全部存于外部 Graphiti 服务，按 `group_id` 分区；基座侧无本地持久化，进程内仅共享客户端缓存。

## 5. 配置项

`llm.react.graph_memory`（`conf/mount/custom.yaml`）：

| 配置 | 默认值 | 说明 |
|---|---|---|
| `enabled` | false | 总开关；未配置不注册工具、不注入 |
| `endpoint` | 空 | Graphiti REST 服务地址（如 `http://graphiti:8000`） |
| `timeout_ms` | 15000 | 工具调用单次请求超时 |
| `group_scope` | caller_user | 分区作用域：`caller`（同 caller 共享一张图）或 `caller_user` |
| `inject.enabled` | true | 是否注入 `<graph_memory>` 块 |
| `inject.max_facts` | 8 | 单次注入事实条数上限 |
| `inject.max_chars` | 1200 | 注入块字符预算 |
| `inject.timeout_ms` | 1500 | 注入检索超时预算，超时放弃注入 |
| `write.enabled` | false | 是否注册 `graph_memory_write`（灰度） |

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 GraphMemory 模块文档基线 |
