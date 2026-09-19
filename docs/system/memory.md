---
title: Memory 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: memory
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/react/memory.go
  - service/memory/runtime.go
  - service/memory/memory.go
  - service/react/memory.go
  - models/llm/memory.go
  - conf/mount/custom.yaml
summary: 跨会话长期记忆的作用域、分层注入、工具读写、审计修订和管理面行为
---

# Memory 模块功能文档

## 1. 模块概述

Memory 模块提供跨会话长期记忆。`resident` 内容直接注入 system 上下文，`detached` 只注入目录并通过 `memory_read` 按需读取。模型工具、Reflection 和管理接口共享统一写核心。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/react/memory/list` | POST | 分页查询记忆 | `react.ListMemories` |
| `/react-base-service/react/memory/create` | POST | 新增记忆 | `react.CreateMemory` |
| `/react-base-service/react/memory/update` | POST | 修订记忆 | `react.UpdateMemory` |
| `/react-base-service/react/memory/delete` | POST | 软删除记忆 | `react.DeleteMemory` |
| `/react-base-service/react/memory/revisions` | POST | 查询修订流水 | `react.ListMemoryRevisions` |
| `/react-base-service/react/memory/rollback` | POST | 恢复历史快照 | `react.RollbackMemory` |

运行期还内置 `memory_list`、`memory_read`、`memory_write`，不走普通 Business Tool 两阶段协议。

## 3. 核心逻辑

### 3.1 作用域与覆盖

基础作用域是 `caller`。`allow_user_scope=true` 时加入 `caller_user`，并作为默认写入目标。同 `itemKey` 冲突时 `caller_user` 覆盖 `caller`。

### 3.2 resident 与 detached

`resident` 按字符预算直接注入；`detached` 注入标题、描述和 itemId 目录。`memory_list` 默认 20 条、最多 50 条；`memory_read` 一次最多 10 条，并逐条校验作用域。

### 3.3 统一写入与审计

`ApplyMutation` 支持 `create/update/delete`，`reason` 必填；更新和删除可使用版本号做乐观锁。同 owner 下内容指纹相同的 create 会收敛为更新，避免重复事实。

每次 create、update、delete、rollback 都追加 `MemoryRevision`。修订流水只增不改；rollback 读取目标修订的 `before` 快照并生成新版本。

### 3.4 安全与限制

写入前扫描常见凭证/密钥、JWT、私钥、身份证号和手机号等形态。Reflection 可通过 `locked` 标签把人工钉死条目视为只读。

## 4. 数据模型

| 表 | 模型 | 说明 |
|---|---|---|
| `tblLlmMemoryItem` | `MemoryItem` | 当前长期记忆条目，状态使用 `active/deleted` |
| `tblLlmMemoryRevision` | `MemoryRevision` | 不可变的 create/update/delete/rollback 前后快照与原因 |

层级使用 `resident/detached`，来源使用 `model/reflection/admin`。

## 5. 配置项与限制

`conf/mount/custom.yaml` 的 `llm.react.memory` 当前示例配置：`enabled=true`、`resident_max_items=16`、`resident_budget_chars=2000`、`index_max_items=64`、`detached_max_items=500`、`allow_user_scope=true`，Reflection 默认关闭。

字段限制：标题最多 32 字、正文最多 500 字、描述最多 512 字、原因最多 512 字。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 Memory 模块文档基线 |