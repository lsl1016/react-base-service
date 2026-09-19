---
title: system_prompt 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: system_prompt
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/systemprompt/systemprompt.go
  - service/systemprompt/systemprompt.go
  - models/llm/system_prompt.go
  - components/params/skill.go
  - service/react/runtime.go
summary: Caller 路由作用域系统提示词的 CRUD、default 默认作用域合并与 Run 启动时的一次性解析拼接
---

# system_prompt 模块功能文档

## 1. 模块概述

system_prompt 模块维护按 `callerKey + routeValues` 作用域挂载的系统提示词。Run 启动时由 `prepareRuntimeRequest` 一次性解析并拼装进 system 消息首段；解析支持跨 caller 的 `default` 默认作用域与多级路由前缀叠加。DTO 定义在 `components/params/skill.go` 的 `---------- 系统提示词 ----------` 段。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/system-prompt/register` | POST | 注册系统提示词 | `systemprompt.RegisterSystemPrompt` |
| `/react-base-service/system-prompt/update` | POST | 更新系统提示词 | `systemprompt.UpdateSystemPrompt` |
| `/react-base-service/system-prompt/delete` | POST | 删除系统提示词 | `systemprompt.DeleteSystemPrompt` |
| `/react-base-service/system-prompt/list` | POST | 按 caller+route 查询（callerKey 空为全量） | `systemprompt.ListSystemPrompts` |
| `/react-base-service/system-prompt/detail` | POST | 按 id 查询详情 | `systemprompt.GetSystemPromptDetail` |

## 3. 核心逻辑

### 3.1 作用域与注册约束

`caller_key` 支持保留值 `default`（`models/llm` 的 `DefaultCallerKey`）：作为默认作用域伪 caller，对其注册不要求真实 caller 存在，且解析时对全部 caller 生效；`default` 不允许注册为真实 caller。其余 `callerKey` 注册时须通过 `GetActiveCallerByKey` 校验（存在且 `status=1`）。

`routeValues` 序列化为 JSON 串（nil 归一为 `[]`）；同一 `caller_key + route_values` 组合不允许重复（`ExistSystemPromptByCallerAndRoute`）。更新为部分更新语义：`name`/`content` 非空串才更新，`routeValues` 非 nil 触发更新并对新组合查重，`status` 为 `*int` 非 nil 时可改；`updated_by` 每次记录。删除为软删除（`SoftDeleteSystemPromptByID`），删除前先落 `updated_by`。列表：`callerKey` 为空时走 `ListAllSystemPrompts` 跨 caller 全量（管理控制台「全部」视图，忽略路由过滤）；否则按 `route_values` 精确匹配（不过滤 `status`）。

### 3.2 Run 侧解析与组装时机

`service/react` 的 `prepareRuntimeRequest` 在构建 Run 运行快照时调用 `systempromptService.ResolveSystemPrompt(callerKey, routeValues)`，每个 Run 解析一次：

1. `route.BuildRoutePrefixes` 生成全部路由前缀（含根 `[]`）；
2. `FindSystemPromptsByCallerAndRoutes` 按 `caller_key IN (callerKey, "default") AND status = 1 AND route_values IN 前缀集` 查询，已停用记录不参与；
3. 排序：`default` 作用域记录排最前，其余按 `route_values` 字符串长度升序；
4. 全部命中记录的 `content` 以 `\n\n` 拼接为单字符串，从通用到具体。

解析失败仅记 `zlog.Warnf` 日志，不阻断 Run，`systemPrompt` 以空串继续装配。

拼接结果经 `buildReactSystemContent` 进入 system 消息：`systemPrompt` 为首段，其后依次为工具索引摘要（`renderToolIndexSummary`）、技能索引摘要（`renderSkillIndexSummary`）、记忆上下文，段间以 `\n\n` 连接，空段跳过；全部为空时不生成 system 消息。

### 3.3 变量替换与 Caller 绑定

`content` 原样注入，不存在模板变量替换或占位符渲染；运行时也不向提示词注入用户名、路由值等动态字段。与 Caller 的绑定完全由 `caller_key` 列决定：真实 caller 记录仅对该 caller 的 Run 生效，`default` 记录对全部 caller 生效，二者同时命中时按 3.2 的排序先后拼接。提示词不感知 `userName` 维度。

## 4. 数据模型

`tblLlmSystemPrompt`（`models/llm/system_prompt.go`）：

| 列 | 说明 |
|---|---|
| `id` | 主键 |
| `caller_key` | 真实 caller 或保留值 `default` |
| `route_values` | JSON 数组字符串，与 `caller_key` 组合唯一（注册/改路由时校验） |
| `name` | 展示名 |
| `content` | 提示词正文，原样使用 |
| `status` | 1 启用 / 0 停用，仅运行时解析过滤 |
| `created_by` / `updated_by` | 操作者记录 |
| `deleted_at` | 软删除标记 |

## 5. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 初始版本 |
