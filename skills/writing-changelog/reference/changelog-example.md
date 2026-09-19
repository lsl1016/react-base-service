> **本文件是从真实项目摘取的范例。** 看结构、详细程度和写作风格，不要照搬业务内容。
> 这是**功能/优化类**变更的典型形态。修复类见 `bugfix-example.md`。
> 注意这是同主题的第二次修订，所以 version 是 v1.1 而非 v1.0。

---

---
title: 树形筛选接口稳定排序
date: 2026-09-08
version: v1.1
type: changelog
module: tree_filter
maintainer: bw-go 项目组
status: active
related_code:
  - service/tree/tree.go
  - service/tree/tree_test.go
  - docs/system/tree_filter.md
  - docs/bw_docs/树形筛选接口文档.md
summary: 修复树节点与选择解析接口跨请求返回顺序不一致问题
---

# 树形筛选接口稳定排序

## 1. 背景与目标

`/bw-go/tree/nodes` 和 `/bw-go/tree/selection/resolve` 会独立查询数据。当底层 SQL 没有完整稳定的排序键时，相同数据的行顺序可能不同，原建树逻辑按首次出现顺序输出，导致树节点顺序与解析后叶子顺序不一致。

本次在共享建树阶段统一完成递归稳定排序，使三个树接口的输出顺序不再依赖数据库行顺序。

## 2. 代码改动

- `service/tree/tree.go`
  - 建树后递归排序每层兄弟节点。
  - 支持 label 和 value 字段的 ASC、DESC、CUSTOM 排序配置。
  - 无配置时按 label 升序，空值置后，同名节点按路径稳定排序。
  - `resolve.leafValues` 继续深度优先遍历，但遍历的是排序后的树。
- `service/tree/tree_test.go`
  - 新增底层行顺序反转时两个接口顺序仍一致的回归测试。
  - 新增 DESC 和 CUSTOM 字段排序测试。

## 3. 影响范围

- 接口：`/bw-go/tree/nodes`、`/bw-go/tree/search`、`/bw-go/tree/selection/resolve`。
- 响应字段不变，仅数组顺序稳定化。
- 不新增数据表、配置项或缓存。

## 4. 测试验证

- `GOCACHE=/tmp/bw-go-tree-order-gocache go test ./service/tree ./components/params ./controllers/http/tree ./router`：通过。
- `GOCACHE=/tmp/bw-go-tree-order-gocache go vet ./service/tree ./components/params ./controllers/http/tree ./router`：通过。
- `GOCACHE=/tmp/bw-go-tree-order-gocache go build ./...`：通过。

## 5. 风险与兼容性

- 未设置排序的树由不确定的数据库行顺序改为 label 升序，前端可见顺序可能发生一次性调整。
- 空值节点在 ASC、DESC 和 CUSTOM 下均固定置后，避免不同数据库空值排序差异。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|------|------|--------|----------|
| v1.1 | 2026-09-08 | bw-go 项目组 | 新增树形筛选统一稳定排序 |

---

## 这份范例值得注意的地方

1. **§1 先讲原有行为为何有问题** —— "按首次出现顺序输出"是根因，"顺序不一致"是现象。两句话交代完，第二段才说本次做法。
2. **§2 按文件组织，写行为不写 diff** —— "建树后递归排序每层兄弟节点"，而不是贴代码。测试文件也列出来，并说明新增了哪些用例。
3. **§3 包含否定事实** —— "响应字段不变"、"不新增数据表、配置项或缓存"。让读者确信影响面已被完整评估。
4. **§4 是真实命令，连 `GOCACHE` 环境变量前缀都保留了** —— 别人可以照着复现。每条都有明确结论"通过"。
5. **§5 写了用户可感知的后果** —— "前端可见顺序可能发生一次性调整"。这是诚实的风险披露，不是"无风险"。
6. **`related_code` 含被更新的文档本身** —— `docs/system/tree_filter.md` 和 API 文档都列入，因为文档是本次变更的交付物。
7. **version 是 v1.1** —— 这是"树形筛选稳定排序"这个主题的第二篇修订。不是因为系统文档是 v1.1，也不是项目发布版本。
8. **全文没有一句"优化了性能"、"提升了体验"这类无法验证的话。**