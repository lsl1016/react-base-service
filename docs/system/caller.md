---
title: Caller 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: caller
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/caller/caller.go
  - controllers/http/caller/copy_config.go
  - service/caller/caller.go
  - service/caller/copy_config.go
  - service/caller/batch_delete.go
  - models/llm/caller.go
  - models/llm/caller_config.go
summary: Caller（接入方）注册、更新、列表与配置聚合管理：以 callerKey 为资源归属维度，支持单事务整体复制与级联软删除
---

# Caller 模块功能文档

## 1. 模块概述

Caller 模块维护接入方（Caller）注册表。`callerKey` 全局唯一，是 Skill、系统提示词、Tool、Tool 用户策略、API Key、MCP 绑定等资源的归属维度；ReAct Run 启动时按 `callerKey` 校验接入方存在且启用，再解析其名下资源。

模块提供注册、更新、列表三个基础操作，以及两个聚合操作：`copy_config` 以既有 Caller 为模板单事务复制全套配置资源，`batch_delete` 单事务级联软删除单个 Caller 及其全部关联资源。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/caller/register` | POST | 注册 Caller | `caller.RegisterCaller` |
| `/react-base-service/caller/update` | POST | 更新 Caller 名称/描述/平台/状态 | `caller.UpdateCaller` |
| `/react-base-service/caller/list` | POST | 查询全部 Caller | `caller.ListCallers` |
| `/react-base-service/caller/copy_config` | POST | 复制 Caller 及全部配置资源 | `caller.CopyConfig` |
| `/react-base-service/caller/batch_delete` | POST | 软删除单个 Caller 及其全部配置资源 | `caller.BatchDelete` |

## 3. 核心逻辑

### 3.1 Caller 概念定位与作用域

- `callerKey` 标识一个接入方；`tblLlmCaller.status=1` 表示启用，ReAct Run 启动校验走 `model.GetActiveCallerByKey`（`service/react/runtime.go`），未启用或不存在即拒绝 Run。
- `default` 是保留的"默认作用域"伪 caller（`model.DefaultCallerKey`）：资源解析时 `model.CallerScopeKeys` 返回「具体 caller + default」两个键，挂在 default 下的工具/系统提示词/skill 对全部 caller 生效。`IsReservedCallerKey` 判定保留字，注册接口拒绝以 `default` 注册真实 Caller。
- `caller_config.go` 承载的是配置聚合（快照读写与级联删除），不承载功能开关，与运行期能力开关的关系见第 5 节。

### 3.2 注册与更新

`RegisterCaller`：先拒绝保留字 `default`，再用 `GetCallerByKeyUnscoped` 查重（含软删行，软删后同名 callerKey 不允许重新注册），然后落库 `status=1`。注册时自动创建 caller 级兜底 skill 的逻辑（`createDefaultSkill`）当前处于注释停用状态，注册不产生任何附属资源。

`UpdateCaller`：控制器按「字段非空才进 updates」拼接（`name`/`description`/`platform` 非空字符串，`status` 非 nil），目标不存在返回 `ErrorCallerNotFound`。

`ListCallers`：返回全部未软删 Caller，按 `created_at DESC` 排序，无分页参数。

### 3.3 copy_config 复制语义

`callerService.CopyConfig` 在单个数据库事务内完成（`model.GetLLMDB().WithContext(ctx).Transaction`）：

1. 校验：`sourceCallerKey`、`targetCaller.callerKey`、`name`、`platform` 非空；源与目标 callerKey 不相同。
2. 源 Caller 必须存在（`SELECT ... FOR UPDATE`）；目标 callerKey 用 Unscoped 查重，存在（含软删行）即报 `ErrorCallerDuplicate`。
3. `LoadCallerConfigSnapshotWithDB` 加载源 Caller 全部未删除配置资源：Skills、SystemPrompts、Tools、ApiKeys（按 `caller_key` 查），ToolUserPolicies（按源 Tools 的 `tool_id IN` 查）。
4. `cloneCallerConfigSnapshot` 改写快照：skill/tool 生成新主键资源 ID（`skill_`/`tool_` + 无横线 uuid），ToolUserPolicies 的 `tool_id` 按新旧 Tool ID 映射改写，全部资源 `caller_key` 改为目标，审计字段（created_by/updated_by）改为操作人，自增 ID 与时间戳清零。
5. 创建目标 Caller（`status=1`）并整体写入快照，返回各资源复制数量。

复制范围只覆盖快照五类资源；Agent、MCP Server、MCP 绑定、记忆、Run/Session 数据不在复制范围内。目标 Caller 的 `description` 直接取请求体值，不继承源。

### 3.4 batch_delete 删除语义

接口名虽为 batch_delete，请求体是单个 `callerKey`（`DeleteCallerReq`，上限 32 字符），一次删除一个 Caller。`SoftDeleteCallerConfigsWithDB` 在单事务内按依赖顺序级联软删，返回各表受影响行数：

| 删除对象 | 范围 |
|---|---|
| ToolUserPolicy | 先 Pluck 该 caller 全部 `tool_id`，按 `tool_id IN` 删除 |
| Skill / SystemPrompt / Tool / ApiKey | 按 `caller_key` 删除 |
| McpServerCaller（MCP-caller 绑定） | 按 `caller_key` 删除 |
| Caller 自身 | 按 `caller_key` 删除 |

响应统计读取 `stats["planTemplates"]`，而删除实现从不写入该键，响应中 `planTemplates` 恒为 0；`mcpServerCallers` 的删除行数计入 stats 但不出现在响应 DTO 中。删除只软删数据库行，不清理 MCP 客户端等运行时对象。删除不校验 Caller 名下是否仍有活跃 Session/Run。

## 4. 数据模型

`tblLlmCaller`（`model.Caller`）：`caller_key`、`name`、`description`、`platform`、`status`（1 启用）、`created_by` 与审计时间戳，软删。

`models/llm/caller_config.go` 提供 `CallerConfigSnapshot`（Skills/SystemPrompts/Tools/ToolUserPolicies/ApiKeys 五类资源的聚合快照）及事务内读写函数：`LoadCallerConfigSnapshotWithDB`、`CreateCallerConfigWithDB`、`SoftDeleteCallerConfigsWithDB`，全部以 `WithDB(tx)` 形式供复制/删除事务复用。

请求/响应 DTO：注册、更新、列表复用 `components/params/skill.go` 内的 `RegisterCallerReq`/`UpdateCallerReq`/`CallerResp`；复制与删除使用 `components/params/caller.go` 内的 `CopyCallerConfigReq`/`CopyCallerConfigResp`/`DeleteCallerReq`/`DeleteCallerResp` 与资源计数结构 `CallerResourceStats`。

## 5. Caller 与运行期配置开关的关系

Caller 表当前不存储任何功能开关字段。ReAct Runtime 的能力开关（plan、subagent、workspace、memory、graph_memory、bundle 等）全部来自全局 YAML `llm.react.*` 配置，由 `service/react/execution_profile.go` 的 `outerExecutionProfile` 汇总为 `ExecutionProfile`（如 `AllowPlan` 取 `llm.react` 的 `AllowPlanEnabled()`、`AllowSubagent` 取 `subagent.enabled`），对所有 Caller 一致生效；`reflection` 等受限执行域另有收敛的档案。

Caller 维度对 Run 的影响只有两个：`status=1` 的准入校验，以及通过资源归属（`caller_key` + `CallerScopeKeys` 并入 `default` 作用域）决定该接入方可见的 Skill/SystemPrompt/Tool/API Key 集合。不存在按 Caller 差异化开启 plan/subagent 等能力的配置路径。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 初始版本 |
