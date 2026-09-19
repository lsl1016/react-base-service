---
title: credits 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: credits
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/llmmodel/credits.go
  - service/credits/credits.go
  - models/llm/user_base_credits.go
  - models/llm/user_bonus_credits.go
  - service/llmmodel/user_model.go
  - components/params/llmmodel.go
summary: 平台默认模型的两层积分（月度基础积分 + 管理员赠送积分）、白名单手动调整与按 token 用量的扣减核算
---

# credits 模块功能文档

## 1. 模块概述

credits 模块核算用户在平台默认模型上的用量额度，粒度为 `user_name × model_hash`。仅 `is_platform_default = 1` 的模型有积分概念，个人模型无积分。积分分两层：

| 层 | 表 | 初始值 | 重置 |
|---|---|---|---|
| 基础积分（月度） | `tblLlmUserBaseCredits` | 1000 | 每月惰性重置为 1000（`reset_month` 比对） |
| 赠送积分（管理员调整） | `tblLlmUserBonusCredits` | 0 | 不重置 |

扣减顺序为赠送积分优先，不足部分落基础积分，基础积分允许扣为负数。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/model/credits/adjust` | POST | 调整指定用户的赠送积分 | `llmmodel.AdjustCredits` |

请求体 `params.AdjustCreditsReq`：`userName`（目标用户英文名）、`modelHash`（平台默认模型 hash）、`delta`（调整量，正负均可）。

## 3. 核心逻辑

### 3.1 基础积分的惰性初始化与月度重置

`GetOrInit(userName, modelHash)` 是基础积分的唯一入口：记录不存在则插入 `credits=1000`、`reset_month=当月`（格式 `2006-01`）；存在但 `reset_month` 非当月则重置为 1000 并更新 `reset_month`；否则返回现值。无定时任务，重置发生在首次访问时。

当前触发点：`service/llmmodel` 的 `ListUserModels` 对每个平台默认模型调用 `GetOrInit` 并把返回值填入列表项 `baseCredits`；赠送积分同路径经 `model.GetBonusCredits` 读取（无记录按 0）。即用户查看模型列表就会完成本月初始化/重置。

### 3.2 扣减核算与检查

换算常量：`initialCredits = 1000`、`inputTokensPerCredit = 5000`、`outputTokensPerCredit = 1000`。

`CalcDeduction(inputTokens, outputTokens)` 按 `input/5000 + output/1000` 整除相加计算扣减量，结果不足 1 时按 1 计。

`DeductCredits(userName, modelHash, inputTokens, outputTokens)` 为异步扣减：起 goroutine 以 `context.Background()` 执行 `deductWithTransaction`，事务内：

1. `GetBonusCreditsForUpdate` 对赠送积分行加 `FOR UPDATE` 锁（行不存在返回 nil）；
2. 赠送积分足够时全额从赠送积分扣（`credits - amount`）；
3. 不足时先将赠送积分清零，剩余量从基础积分扣（`credits - remainder`），基础积分可为负；
4. 基础积分行不存在时该 `UpdateColumn` 影响 0 行，本次扣减不落库、不报错。

`CheckCredits(userName, modelHash)` 判断 `base + bonus <= 0` 则返回 `ErrorCreditsInsufficient`，同时触发 `GetOrInit`。

否定事实：`DeductCredits` 与 `CheckCredits` 在当前主仓库内没有任何调用点——Run 执行链路（`service/react`）不引用 credits 包，也不检查、不扣减积分；模块当前实际生效的写路径只有 adjust 与模型列表触发的初始化。

### 3.3 adjust 的权限与审计

`controllers/http/llmmodel` 的 `AdjustCredits` 依次执行：

1. 操作者必须在 `llm.model_whitelist` 白名单内（`IsWhitelisted`），否则拒绝（`ErrorUserModelNoPermission`）；
2. `model.GetUserModelByHash` 校验目标模型存在，且 `IsPlatformDefault == 1`，否则拒绝（`ErrorModelNotPlatformDefault`）；
3. 调用 `creditsService.AdjustCredits` → `model.UpsertBonusCredits`。

`UpsertBonusCredits` 以两条非事务语句实现：`FirstOrCreate` 建行后 `UpdateColumn("credits", credits + delta)` 增量累加，delta 为负即扣减，可将赠送积分调为负数；并发调整依赖数据库行级更新，不设锁。

审计仅依赖日志：成功时 `zlog.Infof` 记录 `operator / targetUser / modelHash / delta`，无落库审计表。`delta` 为 0 时请求被 `binding:"required"` 拒绝（int 零值不通过校验），即无法提交零调整。

## 4. 数据模型

`tblLlmUserBaseCredits`（`models/llm/user_base_credits.go`）：`user_name`、`model_hash`、`credits`（默认 1000）、`reset_month`（`YYYY-MM`）。无软删除列。

`tblLlmUserBonusCredits`（`models/llm/user_bonus_credits.go`）：`user_name`、`model_hash`、`credits`（默认 0）。无软删除列。

两表均以 `user_name + model_hash` 定位记录，代码层未建唯一索引约束。

## 5. 配置项与限制

积分模块无 YAML 配置，初始额度与换算比例均为 `service/credits/credits.go` 内的编译期常量：

| 常量 | 值 |
|---|---|
| `initialCredits` | 1000 |
| `inputTokensPerCredit` | 5000 |
| `outputTokensPerCredit` | 1000 |

白名单复用 model 模块的 `llm.model_whitelist` 配置。连通性检测等模型侧限制见 model 模块文档。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 初始版本 |
