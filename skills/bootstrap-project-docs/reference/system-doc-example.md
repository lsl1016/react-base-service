> **本文件是从真实项目摘取的范例。** 看它的结构、详细程度和写作风格，不要照搬业务内容。
> 这是一份典型的中等规模模块文档：完整走通 6 节模板，并按需把"配置项"扩展为"配置项与限制"、
> 额外插入了"测试"一节 —— 说明模板允许按模块特点增减中间章节，但 front-matter、
> "模块概述/接口清单/核心逻辑"三节和末尾的"历史版本"是固定的。

---

---
title: 树形筛选模块功能文档
date: 2026-09-08
version: v1.1
type: system
module: tree_filter
maintainer: bw-go 项目组
status: active
related_code:
  - router/tree.go
  - controllers/http/tree/tree.go
  - components/params/tree_dto.go
  - service/tree/tree.go
  - service/tree/query.go
  - service/tree/permission.go
summary: 树形筛选节点加载、全树搜索、选择解析与权限过滤的完整功能说明
---

# 树形筛选模块功能文档

## 1. 模块概述

树形筛选模块为前端 `feature/filter-tree` 提供三个独立能力：按父路径加载节点、在完整可见树中搜索、把 `include/exclude` 选择意图解析为最终叶子。

模块不新增数据表和业务配置，每次请求都根据前端传入的 `datasetHash`、`dataConfig`、`where` 和 `sqlParams` 查询真实数据。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|------|------|------|--------|
| `/bw-go/tree/nodes` | POST | 获取根节点或父节点的直接子节点 | `tree.Nodes` |
| `/bw-go/tree/search` | POST | 搜索全树并返回含祖先的局部树 | `tree.Search` |
| `/bw-go/tree/selection/resolve` | POST | 校验、规范化选择并返回最终叶子 | `tree.ResolveSelection` |

完整请求和响应格式见 `docs/bw_docs/树形筛选接口文档.md`。

## 3. 核心逻辑

### 3.1 真实数据查询

- 普通数据集：把 `dataConfig.fields` 转换为 `dataset.SelectedField`，复用 `dataset.QueryChartData`。
- SQL 模型：把 `sqlParams` 中的条件、联动、下钻、独享参数和排序透传给 `sqlmodel.Sql`。
- 路径始终使用原始 value，标签优先使用映射或格式化后的 label。
- JSON `null` 作为合法路径段保留，不向前端暴露内部路径编码。

### 3.2 树结构

查询行按 label/value 字段对转换为完整路径，再根据路径去重建树。节点同时计算：

- `hasChildren`：当前权限和条件下是否有子节点。
- `leafCount`：当前节点子树的最终叶子数。
- `path`：从根节点到当前节点的原始 value 数组。

`nodes` 只序列化指定层的直接子节点；`search` 只序列化命中分支及其祖先。

建树完成后递归对每层兄弟节点进行稳定排序：

1. 优先使用当层 label 字段的 ASC、DESC 或 CUSTOM 配置。
2. label 字段未配置时，使用 value 字段的排序配置。
3. 均未配置时按 label 升序，空值节点置后，路径值作为最终稳定排序键。

`nodes`、`search` 和 `resolve` 都基于排序后的同一树结构输出，`resolve.leafValues` 按深度优先顺序与逐层 `nodes` 保持一致。

### 3.3 选择解析

`resolve` 先移除不存在或当前用户无权可见的路径，再对每个叶子应用以下规则：

1. 叶子必须命中某个 `include` 前缀才能入选。
2. 更深层的规则覆盖较浅层规则。
3. 同层级冲突时 `exclude` 优先。

响应中 `selectedCount` 等于 `leafValues` 的最终叶子数，`invalidPaths` 返回失效输入。

### 3.4 权限

- 新报表普通数据集复用数据集行权限和列权限。
- 旧报表普通数据集沿用"禁用新行列权限，应用维度权限"策略。
- SQL 模型树根据当前条件的维度授权值过滤。
- 权限查询失败时安全失败，不回退返回未过滤数据。

## 4. 数据模型

本模块不新增数据表。请求/响应 DTO 定义在 `components/params/tree_dto.go`，不属于 `conf/mount` 配置。

数据来源为现有数据集、SQL 模型、报表条件、用户/用户组和维度权限数据。

## 5. 配置项与限制

- 无新增 YAML 配置。
- 树层级最多 10 级，`fields` 必须按 label/value 交替传入。
- 当前前端契约没有分页字段，底层单次最多获取 100000 行。
- 本阶段图表查询仍消费 `resolve` 返回的叶子路径，尚未直接消费 `include/exclude`。

## 6. 测试

单元测试覆盖节点直接子级、祖先搜索树、父选子排除、深层 include 覆盖、失效路径、`null` 路径段、跨请求稳定排序、字段排序配置和前端 JSON 契约。

## 7. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|------|------|--------|----------|
| v1.1 | 2026-09-08 | bw-go 项目组 | 统一节点与选择解析的稳定排序 |
| v1.0 | 2026-09-07 | bw-go 项目组 | 新增三个树形筛选领域接口和真实数据查询 |

---

## 这份范例值得注意的地方

1. **§1 显式声明否定事实** —— "不新增数据表和业务配置"。读者不用怀疑是作者漏写了。
2. **§2 划清职责边界** —— 完整 wire 格式交给独立 API 文档，主文档不重复。这是"一模块一主文档"原则下允许拆分的正确做法。
3. **§3 只写非平凡逻辑** —— 三个接口的 CRUD 样板一句没写，全部篇幅给了建树、排序、选择解析这些真正复杂的部分。
4. **§3.2 的排序规则用编号列表** —— 有优先级顺序的规则用有序列表，不用散文。
5. **§5 给出真实数值** —— "最多 10 级"、"100000 行"，可验证。不写"层级不宜过深"。
6. **§5 最后一条记录了当前实现的局限** —— "尚未直接消费 include/exclude"。诚实记录未完成状态，比假装完备有用。
7. **§7 历史版本倒序** —— 新版本在上。
8. **正文没有"相关代码"一节** —— 代码链接只在 front-matter 的 `related_code` 里，避免两处不同步。