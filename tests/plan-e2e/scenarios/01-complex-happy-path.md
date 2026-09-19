# 场景 01：复杂正常流（检索 + 沙箱计算 + 报告）

- **场景函数**：`scenarioS1`（tests/plan-e2e/run_scenarios.js，`node run_scenarios.js s1`）
- **维度**：多 AGENT 步骤串联、工具充分调用、步骤隔离、Finalizer 总结写回

## 目的

用真实模型 + 真实工具（repo 检索 MCP、python_exec 沙箱、read_tool_result）走通
Plan 模式最长的正常流：结构化规划 → 逐步隔离执行 → 总结落库，验证步骤间数据
传递（摘要 + result_ref）与两段式工具加载在 Scoped Run 中可用。

## 触发

```
请分析 adk-go 仓库，严格按以下三步执行：
1）只调用一次 repo_get_repo_map 获取 adk-go 的整体结构（不要调用其他检索工具、不要逐目录展开）；
2）基于步骤 1 的结果，用 python_exec 做统计：把步骤 1 摘要里出现的目录名和模块数量直接写成
   Python 字面量（不要通过引用或文件传参），输出一张简单的统计表；
3）用中文总结该仓库的定位与架构特点（200 字以内）。每步都要快速完成，不要过度展开。
```

## 预期

1. Planner 产出 ≥3 个 AGENT 步骤，全部 SUCCEEDED，Plan 终态 SUCCEEDED
2. 工具调用同时覆盖代码检索（`repo_*`）与 `python_exec`，且步骤内先 `get_tool` 解析再 `execute_tool` 执行
3. Scoped Run 落库带 `parent_run_id=outer`、`agent_path=plan/<stepKey>`
4. Finalizer 中文报告作为外层 assistant 消息落库；detail/events 管理接口可用
5. 事件流含 `plan_view_update` 快照序列与 `plan_step_event` 包装的步骤原生事件

## 已知边界

- 重型探索任务（无预算约束的逐目录穷举）单步易超默认 600s——本场景靠提示词显式限流；
  超长步骤应让 Planner 设置 `timeoutSeconds`（见场景 07）
- 历史三轮失败样本与归因见 [docs/plan模式E2E测试报告.md §5.5](../../../docs/plan模式E2E测试报告.md)

## 结果

见 [replays/manifest.md](../replays/manifest.md) 编号 01（修复前基线，PASS 520s）。
