# 场景 03：USER_ACTION 拒绝 → 级联取消

- **场景函数**：`scenarioS3`（`node run_scenarios.js s3`）
- **维度**：USER_ACTION 等待、approved=false 语义、取消级联（D3）

## 目的

验证高风险动作的确认闸门：`USER_ACTION` 步骤等待用户批准，提交 `approved: false`
后整体计划级联取消，已成功步骤保持原终态。

## 触发

```
本任务包含一个高风险动作：清理一批历史临时数据文件。请这样制定计划：
1）先用 python_exec 工具生成（仅生成，不删除）一份模拟的待清理文件清单（10 行左右，含文件名与大小）；
2）把「请求用户确认是否真正执行删除」安排为一个 USER_ACTION 步骤（步骤类型必须是 USER_ACTION）；
3）若用户批准才会执行清理（本次测试用户会拒绝，因此清理步骤不应真正执行）。
```

驱动策略：收到 `WAIT_USER_ACTION` 后以 `{"approved": false}` 恢复。

## 预期

1. 前置 AGENT 步骤（python_exec 生成清单）SUCCEEDED
2. USER_ACTION 落 WaitRequest；`approved` 必须为布尔（字符串被 schema 校验拒绝）
3. 拒绝后：USER_ACTION 步骤与全部后续 PENDING 步骤置 CANCELLED、Wait 置 CANCELLED
   且 `resolved_at` 落库、Plan=CANCELLED，客户端收到 `cancelled` 事件
4. 已成功步骤不被回滚，级联语义与文档 §3.5 一致

## 结果

见 [replays/manifest.md](../replays/manifest.md) 编号 03（修复前基线，PASS 101s）。
