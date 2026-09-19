# 系统文档索引

| 文档 | 模块 | 版本 | 最近更新 | 说明 |
|---|---|---|---|---|
| [ReAct Runtime](react_runtime.md) | `react_runtime` | v1.0 | 2026-09-19 | Agent Run 主循环、WebSocket、Tool/HITL、上下文与持久化边界 |
| [Tool](tool.md) | `tool` | v1.0 | 2026-09-19 | 业务 Tool 注册表、白名单可见性、HTTP/MCP 转发执行、get_tool/execute_tool 两阶段协议 |
| [Skill](skill.md) | `skill` | v1.1 | 2026-09-19 | Skill 注册、SKILL.md 导入、路由作用域与运行期加载 |
| [Memory](memory.md) | `memory` | v1.0 | 2026-09-19 | 长期记忆作用域、分层注入、写入审计、管理接口与限制 |
| [Graph Memory](graph_memory.md) | `graph_memory` | v1.0 | 2026-09-19 | Graphiti 时序事实图谱：作用域分区、run 启动注入、检索/写入 Meta Tool |
| [Agent](agent.md) | `agent` | v1.0 | 2026-09-19 | 子 Agent 注册表、Markdown 导入、可见性解析与 delegate_agent 委派边界 |
| [Workspace](workspace.md) | `workspace` | v1.0 | 2026-09-19 | 代码工作区静态解析、bare mirror 缓存、每 run worktree 分配释放 |
| [Async Task](async_task.md) | `async_task` | v1.0 | 2026-09-19 | 异步任务记录、pending 提醒注入、resolve/get 元工具、TTL 与 Provider 同步框架 |
| [MCP](mcp.md) | `mcp` | v1.0 | 2026-09-19 | MCP 服务器登记与连接管理、工具清单同步进 Business Tool 注册表 |
| [Bundle](bundle.md) | `bundle` | v1.0 | 2026-09-19 | 插件包安装展开写入注册表、按快照逆序回滚卸载 |
| [Attachment](attachment.md) | `attachment` | v1.0 | 2026-09-19 | 对话附件上传转 UTF-8 存 COS、fileId 引用与 Run 内按需读取 |
| [Caller](caller.md) | `caller` | v1.0 | 2026-09-19 | 接入方注册与配置聚合管理、单事务整体复制、级联软删除 |
| [Model](model.md) | `model` | v1.0 | 2026-09-19 | 模型目录、连通性检测、白名单管控、用户自建模型与凭证解析 |
| [System Prompt](system_prompt.md) | `system_prompt` | v1.0 | 2026-09-19 | 路由作用域系统提示词 CRUD、default 合并与 Run 启动一次性解析 |
| [API Key](api_key.md) | `api_key` | v1.0 | 2026-09-19 | Caller 路由级 API Key 管理、Run 侧最长前缀解析 |
| [Credits](credits.md) | `credits` | v1.0 | 2026-09-19 | 两层积分模型（基础 + 赠送）、白名单手动调整与按 token 用量扣减核算 |
