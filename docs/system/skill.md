---
title: Skill 模块功能文档
date: 2026-09-19
version: v1.1
type: system
module: skill
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/skill/skill.go
  - service/skill/skill.go
  - service/react/skill_trigger.go
  - models/llm/skill.go
  - components/params/skill.go
summary: Skill 注册、SKILL.md 导入、作用域匹配、关键词触发和运行期按需加载机制
---

# Skill 模块功能文档

## 1. 模块概述

Skill 模块维护 Agent 可复用的行为说明。Skill 可通过结构化接口创建，也可从 `SKILL.md` 的 front-matter + Markdown 正文导入。运行时先注入轻量摘要索引，再由模型通过 `get_skill` 按需加载完整说明。

Skill 按 `callerKey + routeValues` 建立作用域，并支持保留 caller `default` 作为跨 caller 默认作用域。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/skill/create` | POST | 创建 Skill | `skill.CreateSkill` |
| `/react-base-service/skill/update` | POST | 更新 Skill | `skill.UpdateSkill` |
| `/react-base-service/skill/delete` | POST | 软删除 Skill | `skill.DeleteSkill` |
| `/react-base-service/skill/list` | POST | 按 caller/route 查询 | `skill.ListSkills` |
| `/react-base-service/skill/detail` | POST | 查询详情 | `skill.GetSkillDetail` |
| `/react-base-service/skill/import` | POST | 导入一份 `SKILL.md` | `skill.ImportSkill` |
| `/react-base-service/skill/import_zip` | POST | 批量导入 zip 内的 `SKILL.md` | `skill.ImportSkillZip` |

## 3. 核心逻辑

### 3.1 作用域解析

精确 route 返回全部 Skill，上级 route 只回落 `isDefault=1` 的 Skill。运行时还会并入 `caller_key=default` 下命中的启用 Skill。同一 `callerKey + routeValues` 只允许一条默认 Skill。

### 3.2 SKILL.md 导入

导入 front-matter 支持 `name`、`description`、`triggers`、`caller_key`、`route_values`，正文写入 `content`。未指定 caller/route 时使用请求体兜底值。

同 `caller_key + name` 已存在时采用同名覆盖，沿用 `skill_id`，只更新文件形态拥有的字段。zip 导入逐条 best-effort 执行，单项失败不影响其他条目。

### 3.3 运行期发现与加载

Run 初始化时注入可见 Skill 的摘要索引。模型判断适用后调用 `get_skill` 读取完整内容；`triggers` 还可在用户输入命中时走关键词触发路径。

## 4. 数据模型

`tblLlmSkill` / `Skill` 保存标识、行为说明、`triggers_json`、`content`、`caller_key`、`route_values`、`is_default`、`status` 和审计字段；删除采用软删除。

## 5. 配置项与限制

本模块无独立 YAML 开关。可见性由 Caller、Route、`status` 与 `is_default` 决定。`SKILL.md` 要求 front-matter 和正文均非空，触发词数量与长度在 `service/skill` 内限制。

项目的文档维护技能存放于 `.agents/skills/`（不纳入版本控制）；是否导入运行时数据库由部署方决定。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.1 | 2026-09-19 | react-base-service 项目组 | 修正维护技能存放路径的表述（`skills/` → `.agents/skills/`） |
| v1.0 | 2026-09-19 | react-base-service 项目组 | 从 main 分支代码建立 Skill 模块文档基线 |