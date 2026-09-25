---
title: Memory 模块功能文档
date: 2026-09-25
version: v2.0
type: system
module: memory
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/react/memory.go
  - service/memory/runtime.go
  - service/memory/memory.go
  - service/memory/extractor
  - service/memory/resolver
  - service/react/memory.go
  - service/react/memory_reflection.go
  - service/react/memory_extractor.go
  - models/llm/memory.go
  - conf/mount/custom.yaml
summary: 跨会话长期记忆的类型化、作用域、分层注入、工具读写、自动沉淀、审计修订和管理面行为
---

# Memory 模块功能文档

## 1. 模块概述

Memory 模块提供跨会话长期记忆（V2）。条目带类型（`preference/fact/event/procedure`）；`resident` 内容直接注入 system 上下文，`detached` 只注入目录并通过 `memory_read` 按需读取；`preference` 类型不论层级全文注入「用户偏好」节。模型工具、Reflection、Extractor 自动沉淀和管理接口共享统一写核心。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/react/memory/list` | POST | 分页查询记忆（支持 memoryType 过滤） | `react.ListMemories` |
| `/react-base-service/react/memory/create` | POST | 新增记忆（支持 memoryType/confidence/importance） | `react.CreateMemory` |
| `/react-base-service/react/memory/update` | POST | 修订记忆 | `react.UpdateMemory` |
| `/react-base-service/react/memory/delete` | POST | 软删除记忆 | `react.DeleteMemory` |
| `/react-base-service/react/memory/revisions` | POST | 查询修订流水 | `react.ListMemoryRevisions` |
| `/react-base-service/react/memory/rollback` | POST | 恢复历史快照 | `react.RollbackMemory` |

运行期还内置 `memory_list`（支持 memoryType 过滤）、`memory_read`、`memory_write`（支持 memoryType），不走普通 Business Tool 两阶段协议。

## 3. 核心逻辑

### 3.1 作用域与覆盖

基础作用域是 `caller`。`allow_user_scope=true` 时加入 `caller_user`，并作为默认写入目标。同 `itemKey` 冲突时 `caller_user` 覆盖 `caller`。

### 3.2 类型化（V2）

条目类型 `memory_type`：`preference`（用户稳定偏好）、`fact`（事实，默认）、`event`（带绝对日期的事件）、`procedure`（可复用方法）。辅助字段 `confidence`（0-1，默认 0.80）与 `importance`（1-5，默认 3）。模型工具只暴露 `memoryType`；confidence/importance 由 extractor 与管理面维护。`item_key` 幂等指纹不参与类型计算。

### 3.3 分节渲染

`RenderRuntimeContext` 产出 `<memory>` 块：偏好节（preference 全文，importance 降序，优先占用 `resident_budget_chars` 预算，超预算回退目录）→ 常驻节（非 preference 的 resident 全文，与偏好节共享预算）→ 记忆目录（detached 索引；非 fact 类型带 `[type]` 标记）。

### 3.4 统一写入与审计

`ApplyMutation` 支持 `create/update/delete`，`reason` 必填；更新和删除可使用版本号做乐观锁。同 owner 下内容指纹相同的 create 会收敛为更新。create 的类型化字段空值落默认，update 空值保持原值。每次变更追加不可变 `MemoryRevision`（整条快照含类型化字段）；rollback 对 Phase 1 之前的旧快照自动补默认值。

### 3.5 自动沉淀（extractor，V2 Phase 2）

`compact_end` 后按 `memory.extractor` 配置异步执行：一次 LLM 抽取候选（带现有记忆清单防重复、similarIds 自报）→ 确定性预过滤（ItemKey 去重、`min_confidence` 阈值、条数上限）→ 一次 LLM 批量冲突消解（ADD/UPDATE/SUPERSEDE/SKIP，相似候选经自报+LIKE 召回）→ 统一写核心落库（`source=extractor`、RespectLocked、乐观锁冲突重试一次）。**不提供 DELETE**：被取代事实经修订快照可回溯（失效不删除）。写入目标为 scope 的 WriteOwner。与 reflection 互斥：extractor 开启时 compact_end 只走本流水线。指标 `react_memory_extractor_total{status}`。

### 3.6 自动整理（reflection）

compact_end 后派生受限 agentic 子 run（五阶段整理，工具仅 memory 三件套）。extractor 未开启时的默认路径，详见 `service/react/memory_reflection.go`。

### 3.7 安全与限制

写入前扫描常见凭证/密钥、JWT、私钥、身份证号和手机号等形态。`locked` 标签条目对 reflection/extractor 只读（管理面保留干预权）。

## 4. 数据模型

| 表 | 模型 | 说明 |
|---|---|---|
| `tblLlmMemoryItem` | `MemoryItem` | 当前长期记忆条目，状态 `active/deleted`，类型化字段 `memory_type/confidence/importance` |
| `tblLlmMemoryRevision` | `MemoryRevision` | 不可变的 create/update/delete/rollback 前后快照与原因 |

层级使用 `resident/detached`，来源使用 `model/reflection/admin/extractor`。存量环境增量脚本：`sql/memory_v2_upgrade.sql`。

## 5. 配置项与限制

`conf/mount/custom.yaml` 的 `llm.react.memory`：`enabled=true`、`resident_max_items=16`、`resident_budget_chars=2000`、`index_max_items=64`、`detached_max_items=500`、`allow_user_scope=true`；`reflection` 默认关闭；`extractor` 默认关闭（`max_candidates_per_run=8`、`max_writes_per_run=10`、`similar_top_k=5`、`min_confidence=0.50`）。

字段限制：标题最多 32 字、正文最多 500 字、描述最多 512 字、原因最多 512 字；confidence ∈ [0,1]；importance ∈ [1,5]。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v2.0 | 2026-09-25 | react-base-service 项目组 | V2 Phase 1-2：类型化与 Preference 分节渲染；Extractor/Resolver 自动沉淀流水线；设计文档 docs/plan/长期记忆V2实施计划-Phase1-2.md |
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 Memory 模块文档基线 |
