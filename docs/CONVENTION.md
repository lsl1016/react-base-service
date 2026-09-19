---
title: react-base-service 文档规范
date: 2026-09-19
version: v1.1
type: system
module: docs
maintainer: react-base-service 项目组
status: active
related_code:
  - scripts/check-docs.sh
  - .agents/skills/bootstrap-project-docs/SKILL.md
  - .agents/skills/writing-changelog/SKILL.md
summary: 定义项目系统文档、变更记录、索引和校验规则
---

# react-base-service 文档规范

本文件是项目新增和维护文档时的统一规范。现有 `docs/` 根目录下的架构、方案、调研和学习记录继续保留，不要求一次性改写；新写的模块主文档进入 `docs/system/`，代码变更记录进入 `docs/changelog/`。

## 1. 文档分类

| 类型 | 目录 | 职责 | 生命周期 |
|---|---|---|---|
| 系统功能文档 | `docs/system/` | 描述模块当前功能、接口、状态和数据模型 | 长期维护 |
| 变更记录 | `docs/changelog/` | 记录一次变更的背景、改动、验证和风险 | 永久保留，整合后改 `status` |
| 架构/方案/调研 | `docs/` 根目录 | 保存历史设计、学习记录、评审材料和专题说明 | 原路径保留，逐步治理 |

## 2. 分层路径与模块枚举

| 层 | 路径 |
|---|---|
| 路由 | `router/` |
| 控制器 | `controllers/http/<模块>/` |
| DTO | `components/params/` |
| 服务 | `service/<模块>/` |
| 数据 | `models/llm/` |

当前模块枚举：`docs`、`react_runtime`、`plan`、`tool`、`skill`、`memory`、`graph_memory`、`agent`、`workspace`、`async_task`、`mcp`、`bundle`、`caller`、`model`、`system_prompt`、`api_key`、`credits`、`attachment`。

## 3. 元信息规范

受管文档必须包含以下 9 个字段：

```yaml
---
title: <文档标题>
date: YYYY-MM-DD
version: vX.Y
type: system | changelog
module: <单一业务模块名>
maintainer: <维护人>
status: active | merged | archived
related_code:
  - path/to/file.go
summary: <一句话摘要>
---
```

`related_code` 至少一项；`module` 使用单一业务模块名。

## 4. 命名规范

- 系统文档：`docs/system/<模块名>.md`，同模块只维护一篇主文档。
- 变更记录：`docs/changelog/YYYYMMDD_vX.Y_<主题说明>.md`。新主题从 `v1.0` 开始。

## 5. 文档结构

系统文档固定包含“模块概述 / 接口清单 / 核心逻辑 / 历史版本”；数据模型、配置、测试等章节按实际情况增减，“历史版本”始终最后。

变更文档固定包含“背景与目标 / 代码改动 / 影响范围 / 测试验证 / 风险与兼容性 / 历史版本”。测试验证必须记录真实执行命令和结果。

## 6. 定期整合

同模块 `status: active` 的 changelog 超过 5 篇时，把事实融入对应系统文档章节，递增系统文档版本，并把已复核 changelog 逐篇改为 `merged`。整合不删除原 changelog。

## 7. 既有文档治理

- 本项目已有文档不因初始化文档体系而删除或搬迁。
- 现有根目录文档先在 `docs/README.md` 分类索引。
- 发现失效或重复内容时先保留原文件，记录证据后再决定是否归档或合并。
- 未经明确要求，不删除任何已有文档。

## 8. 维护工作流

项目文档需要盘点或补基线时使用 `skills/bootstrap-project-docs/`；代码行为变更后使用 `skills/writing-changelog/`，并同步对应系统文档。

## 9. 校验

```bash
bash scripts/check-docs.sh
```

校验范围只覆盖 `docs/system/` 与 `docs/changelog/`，历史平铺文档不强制 front-matter。

## 10. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.1 | 2026-09-19 | react-base-service 项目组 | 模块枚举新增 `plan`（Plan Runtime 上线） |
| v1.0 | 2026-09-19 | react-base-service 项目组 | 建立文档分类、模块枚举、保留策略和机器校验规则 |