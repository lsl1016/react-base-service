# 变更记录索引

| 文档 | 模块 | 状态 | 说明 |
|---|---|---|---|
| [20260919_v1.0_新增文档维护技能与文档基线](20260919_v1.0_新增文档维护技能与文档基线.md) | `docs` | active | 引入两个文档维护 Skill，并建立第一批系统文档基线 |
| [20260919_v1.0_新增Plan模式运行时](20260919_v1.0_新增Plan模式运行时.md) | `plan` | active | 新增 executionMode=plan 独立执行范式：结构化规划、Scoped ReactRun 逐步执行、持久化等待与双通道恢复 |
| [20260919_v1.0_修复Plan模式步骤超时误判与规划容错](20260919_v1.0_修复Plan模式步骤超时误判与规划容错.md) | `plan` | active | 修复超时误判取消、stepKey 判死、超时不可配、失败不级联，解锁 Retry/Skip |
| [20260919_v1.0_新增Plan模式端到端测试档案与回放记录](20260919_v1.0_新增Plan模式端到端测试档案与回放记录.md) | `plan` | active | 六场景 E2E 驱动 + 场景文档库 + 服务端权威回放归档 + 完整测试报告 |
| [20260919_v1.0_新增运行时设置在线配置](20260919_v1.0_新增运行时设置在线配置.md) | `setting` | active | subagent 委派策略支持管理面板在线覆盖 custom.yaml（DB > yaml > 默认） |
| [20260925_v2.0_长期记忆V2类型化与自动沉淀](20260925_v2.0_长期记忆V2类型化与自动沉淀.md) | `memory` | active | 记忆类型化（memory_type/confidence/importance）+ compact_end 触发 Extractor/Resolver 自动沉淀 |
| [20260925_v1.0_修复工具结果闭合不变量与tool_call去重](20260925_v1.0_修复工具结果闭合不变量与tool_call去重.md) | `react_runtime` | active | 闭合不变量三道防线（中断登记制/引擎单点落库/重建孤儿回填）+ tool_call id 双层去重 |
| [20260925_v1.0_新增Steering引导注入与运行中消息准入账本](20260925_v1.0_新增Steering引导注入与运行中消息准入账本.md) | `react_runtime` | active | S1：运行中消息准入账本 tblLlmReactPendingInput、guide 唯一注入点（tool_result 落库后/纯文本 finish 前）、三路结算与 steer_* 回执事件 |
| [20260925_v1.0_新增Steering排队与run结束自动续跑](20260925_v1.0_新增Steering排队与run结束自动续跑.md) | `react_runtime` | active | S2：不可引导时排队入账（payload 快照）、run 结束自动续跑队首（claim-once 晋升原子化）、guide 降级排队、失败暂停语义 |
