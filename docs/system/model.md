---
title: model 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: model
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/llmmodel/model.go
  - controllers/http/llmmodel/whitelist.go
  - controllers/http/llmmodel/user_model.go
  - service/llmmodel/user_model.go
  - service/llmmodel/whitelist.go
  - models/llm/user_model.go
  - components/params/llmmodel.go
summary: 模型目录查询、连通性检测、白名单管控与用户自建模型/平台默认模型的 CRUD，以及 Run 侧按 ModelHash 解析模型凭证
---

# model 模块功能文档

## 1. 模块概述

model 模块管理 ReAct Runtime 可用的模型凭证。模型分两类，同存一张 `tblLlmUserModel` 表：

| 类型 | `is_platform_default` | `user_name` | 可见范围 | 积分概念 |
|---|---|---|---|---|
| 平台默认模型 | `1` | 空串 | 全部用户 | 有（见 credits 模块） |
| 个人模型 | `0` | 创建者英文名 | 创建者本人 + 白名单用户 | 无 |

`modelKey` 使用厂商新枚举（`OpenAI`/`DeepSeek`/`xAI`/`Moonshot`/`通义千问`/`智谱`/`火山方舟（字节）`/`Google`/`MiniMax`/`文心一言`/`Anthropic`/`自建网关`），由 `api/llm` 包的 `IsValidModelKey` 校验；`NormalizeModelKey` 将其归一到 `gpt`/`claude` 两类客户端，国内厂商 endpoint 加 `-cn` 后缀查找。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/models` | GET | 返回配置内支持的模型目录 | `llmmodel.GetModels` |
| `/react-base-service/model/whitelist` | GET | 返回模型管理白名单 | `llmmodel.GetWhitelist` |
| `/react-base-service/model/check-connectivity` | POST | 验证 apiKey + 模型组合可用 | `llmmodel.CheckConnectivity` |
| `/react-base-service/model/create` | POST | 创建模型 | `llmmodel.CreateUserModel` |
| `/react-base-service/model/update` | POST | 编辑模型 | `llmmodel.UpdateUserModel` |
| `/react-base-service/model/delete` | POST | 软删除模型 | `llmmodel.DeleteUserModel` |
| `/react-base-service/model/list` | POST | 模型列表（含积分信息） | `llmmodel.ListUserModels` |
| `/react-base-service/model/detail` | POST | 模型详情（apiKey 不脱敏） | `llmmodel.GetUserModelDetail` |

## 3. 核心逻辑

### 3.1 白名单与模型目录

白名单来自 `custom.yaml` 的 `llm.model_whitelist`，`service/llmmodel` 的 `IsWhitelisted` 逐项字符串比较；列表为空时任何用户都不具备平台级权限，不存在隐式放行。白名单能力：创建/编辑/删除平台默认模型、查看全量模型、查看任意模型详情、将模型在平台默认与个人之间切换、调整积分。

`GET /models` 与数据库无关，返回 `llm.GetSupportedModels()` 读取的 `custom.yaml` `llm.models` 目录（`key`/`versions`/`default_version`）；不返回用户自建模型。

### 3.2 连通性检查语义

`CheckConnectivity` 的判定流程：

1. `modelKey` 必须通过 `llm.IsValidModelKey`，否则返回 `ErrorUserModelCategoryNotSupported`；
2. `llm.GetClientWithUserModel(apiKey, modelKey)` 构建客户端，endpoint 按 `clientType`（或 `clientType-cn`）从 `api.yaml` endpoints 查找；
3. 以 15 秒超时（`context.WithTimeout`）调用 `ChatStream` 发送单条消息 `hi`；
4. 流式读取中收到首个非空 `Content` 片段即判定连通成功并立即返回，不读完整个流；`chunk.Error` 或流结束仍无有效内容则返回 `ErrorUserModelConnectivityFailed`。

创建模型时强制执行该检查；编辑时仅当 `model_key`/`model_version`/`api_key` 任一发生变更才重新检查。

### 3.3 创建、编辑与删除约束

创建（`CreateUserModel`）：

- `bizScenes` 至少 1 项，序列化为 JSON 数组字符串存入 `biz_scenes`；
- `isPlatformDefault=1` 仅白名单用户可建，且落库时 `user_name` 置空串；
- 同一 `user_name` 范围内 `model_name` 唯一（`ExistUserModelByUserAndName`）；
- 通过连通性检查后才插入；`model_hash` 生成规则为 `"model_" + uuid 去横线`。

编辑（`UpdateUserModel`）：权限为平台默认模型需白名单、个人模型需创建者本人。逐字段与当前记录比较生成 updates，无变更直接返回。`isPlatformDefault` 变更仅白名单用户可操作：平台默认转个人时 `user_name` 写为当前操作者，个人转平台默认时置空，并按切换后的范围重做重名校验。请求各字段经 `validateUpdateReq` 全量非空校验，空串无法表达"不修改"。

删除（`DeleteUserModel`）：创建者本人或白名单用户可删；物理上为软删除（`SoftDeleteUserModelByID`）。

### 3.4 列表与详情的可见性

列表（`ListUserModels`）：白名单用户返回全量，普通用户返回 `is_platform_default = 1 OR user_name = 自己`；`bizScene` 过滤走 SQL `JSON_CONTAINS(biz_scenes, JSON_QUOTE(?))`，`modelName` 模糊搜索为内存中 `strings.Contains` 过滤；排序 `is_platform_default DESC, created_at DESC`。仅平台默认模型项附带 `baseCredits`（经 `creditsService.GetOrInit`，会触发基础积分惰性初始化）与 `bonusCredits`。apiKey 经 `maskApiKey` 脱敏：长度大于 8 时前 4 位 + `****` + 后 4 位，否则整体 `****`。

详情（`GetUserModelDetail`）：白名单用户可查任意模型；普通用户仅可查平台默认模型或本人模型；`apiKey` 原样返回用于编辑回显。

### 3.5 Run 侧凭证解析

`service/react` 的 `prepareRuntimeRequest` 按以下优先级解析模型凭证：

- `payload.ModelHash` 非空：`model.GetUserModelByHash` 取整条记录，`apiKey`/`modelKey`/`modelVersion` 全部来自记录，payload 中的 `modelKey`/`modelVersion` 被忽略；查无记录报 `ErrorUserModelNotFound`。该路径不校验请求者与模型归属，任何拿到 hash 的用户均可引用他人个人模型的凭证。
- `ModelHash` 为空：`modelKey` 必填；`apiKey` 由 `apikeyService.ResolveApiKey(callerKey, routeValues)` 从 apikey 注册表解析（查不到报 `ErrorApiKeyNotFound`）；版本经 `llm.ResolveModelVersion` 回退到目录 `default_version`。

`executeReactLoop` 用 `llm.GetClientWithUserModel(req.apiKey, currentModel.ModelKey)` 构建客户端；`configuredReactModelRouting` 计算的互备切换（`service/react/model_failover.go`）在切换目标时复用同一 `apiKey` 重建客户端。即：带 `modelHash` 的 Run 使用模型自带凭证，不带 `modelHash` 的 Run 使用 caller 路由级 API Key。

`UserModel.ApiKey` 字段标注 `json:"-"`，不随结构体序列化输出；接口输出统一走 `params.UserModelItem`。

## 4. 数据模型

`tblLlmUserModel`（`models/llm/user_model.go`）：

| 列 | 说明 |
|---|---|
| `id` | 主键 |
| `model_hash` | 对外引用标识，`model_` + uuid |
| `user_name` | 平台默认模型为空串，个人模型为创建者 |
| `model_name` | 同一 `user_name` 范围内唯一 |
| `model_key` | 厂商新枚举 |
| `model_version` | 具体版本号 |
| `api_key` | 明文存储，无加密 |
| `biz_scenes` | JSON 数组字符串 |
| `is_platform_default` | 1 平台默认 / 0 个人 |
| `deleted_at` | 软删除标记 |

DTO 定义在 `components/params/llmmodel.go`：请求侧 `ConnCheckReq`、`CreateUserModelReq`、`UpdateUserModelReq`、`DeleteUserModelReq`、`GetUserModelDetailReq`、`ListUserModelsReq`，响应侧 `UserModelItem`（`baseCredits`/`bonusCredits` 为可选指针字段）。

## 5. 配置项

| 配置 | 来源 | 用途 |
|---|---|---|
| `llm.models` | `custom.yaml` | 模型目录（版本列表、默认版本），`GET /models` 与版本回退使用 |
| `llm.model_whitelist` | `custom.yaml` | 平台级操作白名单 |
| `api.endpoints` | `api.yaml` | 按 `clientType` / `clientType-cn` 提供厂商 endpoint |

连通性检测超时为代码内固定值 15 秒，非配置项。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 初始版本 |
