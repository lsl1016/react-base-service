---
title: api_key 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: api_key
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/apikey/apikey.go
  - service/apikey/apikey.go
  - models/llm/api_key.go
  - components/params/skill.go
  - components/route/route.go
  - service/react/runtime.go
summary: Caller 路由级 API Key 的注册、更新、删除、查询与 Run 侧最长前缀解析
---

# api_key 模块功能文档

## 1. 模块概述

api_key 模块维护挂在 Caller 路由下的 LLM 凭证。每条记录归属一个 `callerKey + routeValues` 组合，为该路由下的全部 Run 提供统一的模型 API Key；与 model 模块的用户自建模型凭证互为两条独立供证路径。

DTO 定义在 `components/params/skill.go` 的 `---------- API Key ----------` 段（`RegisterApiKeyReq` 等），无独立参数文件。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/apikey/register` | POST | 注册 API Key | `apikey.RegisterApiKey` |
| `/react-base-service/apikey/update` | POST | 更新 API Key | `apikey.UpdateApiKey` |
| `/react-base-service/apikey/delete` | POST | 删除 API Key | `apikey.DeleteApiKey` |
| `/react-base-service/apikey/list` | POST | 按 caller+route 精确查询 | `apikey.ListApiKeys` |
| `/react-base-service/apikey/detail` | POST | 按 id 查询详情 | `apikey.GetApiKeyDetail` |

## 3. 核心逻辑

### 3.1 归属层级与存储方式

API Key 的归属层级是平台级路由（`callerKey + routeValues`），不属于任何用户；`created_by`/`updated_by` 仅记录操作者英文名，不构成访问控制。管理接口不做所有者校验与白名单校验，任何通过 `middleware.AnonymousAuth` 登录态的用户均可 update/delete 任意记录。

`api_key` 列明文存储，无加密。`ApiKey.ApiKeyValue` 字段标注 `json:"-"`；`ToApiKeyResp` 中响应字段 `ApiKey` 恒为空串，即 register/update/delete/list/detail 五个接口均不回传 key 明文。key 值一旦录入只能整值重传覆盖，无法读回。

### 3.2 注册、更新与删除约束

注册（`RegisterApiKey`）：`GetActiveCallerByKey` 校验 caller 存在且 `status=1`；`routeValues` 序列化为 JSON 串（nil 归一为 `[]`）；同一 `caller_key + route_values` 组合不允许重复（`ExistApiKeyByCallerAndRoute`）；新记录 `status=1`。

更新（`UpdateApiKey`）：部分更新语义——`name`/`apiKey` 非空串才更新；`routeValues` 非 nil 触发更新，且新路由组合与现值不同时重做查重；`status` 为 `*int` 非 nil 时可改（用于启停）；`updated_by` 每次更新都记录。

删除（`DeleteApiKey`）：先落 `updated_by` 再软删除（`SoftDeleteApiKeyByID`）。

列表与详情：`ListApiKeys` 走 `ListApiKeysByCallerAndExactRoute` 精确匹配且不过滤 `status`（含已停用记录）；`GetApiKeyDetail` 按 id 查询，不过滤 `status`。

### 3.3 Run 侧消费：最长前缀解析

`ResolveApiKey(ctx, callerKey, routeValues)` 是唯一的运行时消费入口，调用点为 `service/react` 的 `prepareRuntimeRequest`，且仅在 Run payload 不带 `modelHash` 时使用（带 `modelHash` 的 Run 使用 `UserModel` 自带凭证，不查本表）：

1. `route.BuildRoutePrefixes(routeValues)` 生成全部前缀（含根 `[]`）；
2. `FindApiKeysByCallerAndRoutes` 按 `caller_key = ? AND status = 1 AND route_values IN ?` 查询，已停用记录不参与解析；
3. 多条命中时取 `route_values` 字符串最长者（最长前缀优先），返回其 `api_key`；
4. 无命中返回空串，`prepareRuntimeRequest` 据此报 `ErrorApiKeyNotFound`。

## 4. 数据模型

`tblLlmApiKey`（`models/llm/api_key.go`）：

| 列 | 说明 |
|---|---|
| `id` | 主键 |
| `caller_key` | 归属 Caller |
| `route_values` | JSON 数组字符串，与 `caller_key` 组合唯一（注册/改路由时校验） |
| `name` | 展示名 |
| `api_key` | 明文凭证，`json:"-"` |
| `status` | 1 启用 / 0 停用，仅运行时解析过滤 |
| `created_by` / `updated_by` | 操作者记录 |
| `deleted_at` | 软删除标记 |

## 5. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 初始版本 |
