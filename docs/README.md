# react-base-service 文档索引

已有文档保持原路径和内容。本索引在其上新增受管系统文档与变更记录入口。

## 受管文档

- [文档规范](CONVENTION.md)
- [系统文档索引](system/README.md)
- [变更记录索引](changelog/README.md)
- [ReAct Runtime](system/react_runtime.md)
- [Skill](system/skill.md)
- [Memory](system/memory.md)

## 核心架构与接入

- [系统架构](architecture.md)
- [接入指南](integration-guide.md)
- [部署说明](deployment.md)
- [MCP](mcp.md)
- [Runtime 模块化](runtime-modularization.md)
- [多端服务形态接入方案](多端服务形态接入方案.md)
- [本地工作台与服务端基座的执行边界](本地工作台与服务端基座的执行边界.md)

## Agent、记忆与工作台专题

- [Memory 专题](memory.md)
- [多 Agent 编排](multi-agent-orchestration.md)
- [子 Agent 委派 P1 实现说明](子Agent委派P1实现说明.md)
- [知识库与长期记忆集成改造方案](知识库与长期记忆集成改造方案.md)
- [知识库集成、长期记忆、检索](知识库集成、长期记忆、检索.md)
- [服务端 AI 工作台思路和讨论](服务端AI工作台思路和讨论.md)
- [服务端 AI 工作台改造方案](服务端AI工作台改造方案.md)
- [定时触发工作流实现方案](定时触发工作流实现方案.md)
- [连载侦探社评测项目方案](连载侦探社评测项目方案.md)

## 调研与学习记录

- [ast-grep 学习](ast-grep学习.md)
- [Coze Studio 学习](coze-studio学习.md)
- [Graphiti 学习](graphiti学习.md)
- [OpenHands Automation 学习](openhands-automation学习.md)
- [OpenHands SDK 学习](openhands-sdk学习.md)
- [Pokemon Chat 学习](pokemon-chat学习.md)
- [RAGFlow Admin 学习](ragflow-admin学习.md)
- [Prompt Reference](prompt-reference.md)

## 维护入口

- 文档基线：`.agents/skills/bootstrap-project-docs/SKILL.md`
- 变更记录：`.agents/skills/writing-changelog/SKILL.md`
- 校验：`bash scripts/check-docs.sh`