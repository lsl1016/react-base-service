# Plan 模式 E2E 回放记录索引

> 导出时间：2026-09-19T13:35:00.800Z ｜ 来源：POST /react/session/events（与 /react/replay 回放页同源）
> 回放 JSON 内含会话标题、全量会话级事件与最终 plan_view_update 快照；步骤级原始事件流体积大（单次可达 5 万+条），不入库，可按 manifest 中的 planExecutionId 从 DB / plan_execution/events 接口重建。

| 编号 | 场景 | 基线 | Plan / Session | 回放终态 | 事件数 | 文件 |
|---|---|---|---|---|---|---|
| 01 | S1 复杂正常流（repo 检索 + python 统计 + 中文报告） | 修复前基线 | `plan_exec_e69dfa14743c4b9a933338ee4fe5be2e`<br>`session_0a46b9df40f74eecbba15e5618ea92df` | SUCCEEDED | 6 | [01-complex-happy-path.json](01-complex-happy-path.json) |
| 02 | S2 USER_INPUT 等待 → WS 恢复 → 续跑完成 | 修复后复测 | `plan_exec_18b332a1dd89498385a54c07223b740d`<br>`session_b336b506bae74631be9b70cf579a7307` | SUCCEEDED | 6 | [02-user-input-ws-resume.json](02-user-input-ws-resume.json) |
| 03 | S3 USER_ACTION 拒绝 → 级联取消 | 修复前基线 | `plan_exec_c498cac741e145ffa8502864db389f67`<br>`session_d0efd363848a4c7299c344aed5a7c6ab` | CANCELLED | 3 | [03-user-action-reject-cancel.json](03-user-action-reject-cancel.json) |
| 04 | S4 运行中取消（HTTP）+ WS 命令窗口探针 | 修复后复测 | `plan_exec_471a2ae0e9f84676b206644f177fd84a`<br>`session_b018d48d7d704022bcc6284ec000a227` | CANCELLED | 3 | [04-midrun-cancel.json](04-midrun-cancel.json) |
| 05 | S5 关闭 WS 后 HTTP 恢复（连接无关性） | 修复前基线 | `plan_exec_8a74af971124434aaf1e231fecd4041c`<br>`session_b39b6b6bea7e4feeb1223910d64b8538` | SUCCEEDED | 6 | [05-http-resume-durable.json](05-http-resume-durable.json) |
| 06 | S6 WAIT 态并发互斥 + 自定义 responseSchema 校验 | 修复前基线 | `plan_exec_2b9ed379c2284605a561d8f0b28e2488`<br>`session_0356104f4bba449eb11667c4ef2cb36d` | SUCCEEDED | 6 | [06-wait-mutex-and-schema.json](06-wait-mutex-and-schema.json) |
| 07 | T7 步骤超时分类（timeoutSeconds=60 + sleep90，验证 P1/P3/P8 修复） | 修复后复测 | `plan_exec_03588727b41c4098a826a162b5c8b28d`<br>`session_da522769f3064d249d8cc8fedb76dce7` | CANCELLED | 3 | [07-timeout-classification.json](07-timeout-classification.json) |
| 98 | Bug 证据：修复前步骤超时被误判为用户取消（步骤 3 恰 600s 整被级联 CANCELLED，对应测试报告 §6-P1） | Bug 证据（修复前） | `plan_exec_b373129cc5934860b9ccf154b99baa0e`<br>`session_72f9ad5573a8445b8146cb76fd4b8f0c` | CANCELLED | 3 | [98-bug-evidence-timeout-misclassified.json](98-bug-evidence-timeout-misclassified.json) |

## 预期结论对照

- **01**：预期 SUCCEEDED，回放终态 SUCCEEDED
- **02**：预期 SUCCEEDED，回放终态 SUCCEEDED
- **03**：预期 CANCELLED，回放终态 CANCELLED
- **04**：预期 CANCELLED，回放终态 CANCELLED
- **05**：预期 SUCCEEDED，回放终态 SUCCEEDED
- **06**：预期 SUCCEEDED，回放终态 SUCCEEDED
- **07**：预期 CANCELLED（验证完成后清理取消），回放终态 CANCELLED
- **98**：预期 CANCELLED（当时的错误终态；修复后应为 FAILED），回放终态 CANCELLED
