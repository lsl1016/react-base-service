# 场景 02：USER_INPUT 等待 → WS 恢复

- **场景函数**：`scenarioS2`（`node run_scenarios.js s2`）
- **维度**：持久化等待、wait_request 协议、WS `plan_resume`、等待期并发互斥

## 目的

验证 USER_INPUT 步骤进入持久化等待（不依赖原 goroutine），用户经 WS `plan_resume`
提交答案后计划续跑至完成。

## 触发

```
我想生成一份定制化的技术学习路线报告，但在开始之前必须先确认我的技术背景。
请把「询问用户最熟悉的编程语言与从业年限」安排为一个 USER_INPUT 步骤放在计划开头
（步骤类型必须是 USER_INPUT），拿到我的回答后，再用 python_exec 工具生成一份对比
学习路线表（含每周时间投入估算），最后输出中文总结报告。
```

驱动策略：收到 `WAIT_USER_INPUT` 视图（带 `wait_request.request_id` 与默认 `answer`
必填 schema）后，等待期先发同会话并发探针，再以 `{"answer": "…Go…5 年…"}` 恢复。

## 预期

1. USER_INPUT 步骤落 WaitRequest（表 tblLlmPlanWait），外层 run 置 `waiting_plan`
2. 视图序列出现 `WAIT_USER_INPUT` + `can_resume`，wait_request 携带 response_schema
3. 等待期间同会话普通 run 被拒（errNo 6000100，`waiting_plan` 计入活跃互斥）
   ——注意探针 sessionId 必须放在 WS 消息顶层而非 payload（详见 README 备注）
4. `plan_resume` 后后续 AGENT 步骤（含 python_exec）执行，Plan SUCCEEDED，
   USER_INPUT 步骤终态 SUCCEEDED 且答案进入后续步骤 Prompt
5. Finalizer 总结写回会话历史

## 结果

见 [replays/manifest.md](../replays/manifest.md) 编号 02（修复后复测，PASS 438s）。
