# 场景 07：步骤超时分类（P1/P3/P8 修复验证）

- **场景函数**：`scenarioT7`（`node run_scenarios.js t7`）
- **维度**：步骤超时 vs 用户取消的分类、timeoutSeconds 可配、失败级联终态、Retry 复活

## 目的

一箭三雕验证 2026-09-19 修复的三处 Plan Runtime 缺陷（编号对应
[docs/plan模式E2E测试报告.md §6](../../../docs/plan模式E2E测试报告.md)）：

- **P1**：步骤超时（尤其截止落在工具执行阶段、中断错误被包装为 `context.Canceled`）
  必须按 Attempt 失败处理并自动重试，不再误判为"用户取消"级联取消整个 Plan
- **P3**：`timeoutSeconds` 可由 Planner 显式设置并持久化（60–3600 钳制）
- **P8**：Plan FAILED 后续 PENDING 步骤级联置 CANCELLED；HTTP Retry 复活计划时
  这些步骤与外层 run 一并复位（顺带覆盖 reviveOuterRun 修复）

## 触发

```
请制定两步计划：
1）用 python_exec 执行一段 Python 脚本：time.sleep(90) 后打印 "sleep done"
   （这一步必须显式设置 timeoutSeconds=60，用于验证超时行为）；
2）总结第一步结果。第一步的重试次数（maxAttempts）设为 2。
```

驱动策略：等待计划收敛（两次 60s 超时 ≈ 3 分钟）→ 断言 FAILED 与级联 →
派出 HTTP Retry → 12s 后查 detail 断言后续步骤复位 PENDING → 取消清理。

## 预期

1. DB 中 sleep 步骤 `timeout_seconds=60`（Planner 设置 + 持久化）
2. Attempt 1/2 错误摘要含「步骤超时（上限 60 秒）」，Attempt 2 为超时后自动重试
3. Plan 终态 **FAILED**（修复前会被误判 CANCELLED），后续步骤 CANCELLED"Plan 失败，后续步骤未执行"
4. Retry 后：sleep 步骤新建 Attempt 续跑、后续步骤回 PENDING、外层 run 回 running
   （修复前 Retry 被 error 态外层 run 守卫直接拒绝）
5. 对比样本：[replays/98-bug-evidence-timeout-misclassified.json](../replays/98-bug-evidence-timeout-misclassified.json)
   为修复前同一缺陷的实跑证据（步骤恰 600s 整被级联 CANCELLED）

## 结果

见 [replays/manifest.md](../replays/manifest.md) 编号 07（修复后复测，PASS 166s；
回放终态 CANCELLED 为验证完成后的清理取消，非误判）。
