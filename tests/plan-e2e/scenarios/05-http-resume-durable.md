# 场景 05：关闭 WS 后 HTTP 恢复（连接无关性）

- **场景函数**：`scenarioS5`（`node run_scenarios.js s5`）
- **维度**：双通道恢复、等待持久化、连接无关性（等待/恢复不依赖原 goroutine）

## 目的

验证 USER_INPUT 等待后**彻底关闭 WS 连接**，仅凭 HTTP `POST /plan_execution/resume`
就能让计划续跑至终态——等待态的权威在 PlanExecution 落库，与执行载体解耦。

## 触发

```
请帮我生成一份「服务器容量规划」建议报告。计划中必须先安排一个 USER_INPUT 步骤询问我
当前的日均请求量与峰值 QPS，获得我的输入后，用 python_exec 工具按 30% 年增长做容量
推演并输出估算表，最后给出中文结论。
```

驱动策略：收到 `WAIT_USER_INPUT` 后 `close()` 掉 WS → HTTP resume 提交答案 →
轮询 detail 接口直到终态。

## 预期

1. WS 关闭不影响等待中的计划（无 goroutine 依赖，也无连接依赖）
2. HTTP resume 校验响应 schema（必填字段缺失被拒，换正确字段通过）
3. 后续 AGENT 步骤（python_exec 容量推演）在无 WS 连接下执行至 Plan SUCCEEDED
4. 续跑期间事件不流失（持久化消息），可由 `/plan_execution/events` 按 Attempt 重建

## 结果

见 [replays/manifest.md](../replays/manifest.md) 编号 05（修复前基线，PASS 301s）。
