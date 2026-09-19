# 场景 06：WAIT 态并发互斥 + 自定义 responseSchema

- **场景函数**：`scenarioS6`（`node run_scenarios.js s6`；独立探针 `mutex_probe.js <sessionId>`）
- **维度**：会话并发互斥（waiting_plan）、Planner 自定义响应 schema 校验

## 目的

两件事：① 等待期（外层 run=`waiting_plan`）同会话普通消息被拒的互斥语义；
② Planner 可自定义 USER_INPUT 的必填字段（非默认 `answer`），Resume 按 schema 强制校验。

## 触发

```
请帮我制定一份阅读计划。计划第一步必须是一个 USER_INPUT 步骤（步骤类型必须是 USER_INPUT），
询问我每月的阅读时长偏好，拿到回答后用 python_exec 计算一年可读完的书籍数量
（按每本 300 页估算），输出简短中文结论。
```

驱动策略：`WAIT_USER_INPUT` 后用**正确构造**的探针（sessionId 置于 WS 消息顶层）
发同会话普通 run；随后 HTTP resume 先故意用错误字段、再用 Planner 自定义字段恢复。

## 预期

1. 外层 run 状态 `waiting_plan` 属活跃态：并发普通 run 被拒
   `errNo=6000100 当前会话已有运行中的 ReAct run`
2. Planner 自定义 schema（如必填 `monthly_reading_hours`）：错误字段被
   "missing required wait response field" 拒绝，正确字段通过
3. 恢复后计划续跑至 SUCCEEDED

## 历史教训（驱动侧）

早期两轮互斥探针把 sessionId 误放 payload（协议要求在消息顶层），服务端按新会话开跑，
得出"未拒"的假阴性——并发类断言必须用 `mutex_probe.js` 的正确构造。

## 结果

见 [replays/manifest.md](../replays/manifest.md) 编号 06（修复前基线；本场景最终经
HTTP 恢复完整跑完，Plan SUCCEEDED）。
