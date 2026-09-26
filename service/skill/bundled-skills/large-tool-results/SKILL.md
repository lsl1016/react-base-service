# 大数据结果处理规范

工具的大结果不会整段进入上下文：回填给你的是预览 + resultRef 引用，完整内容保存在后端（默认 24 小时过期）。看到以下信号说明结果是"外置"的：

- 回填内容带 `resultRef` 字段；
- `truncated: true` 且 `omittedChars > 0`；
- 内容含 `hasMore` / `nextOffset`。

## 处理流程

1. **先看预览**：预览通常足够判断结构（JSON 数组？日志行？HTML？）。结构清晰且答案就在开头时，不要读取全文。
2. **结构不明用 inspect_data**：传 `source.type=tool_result` 和 `source.ref=<resultRef>`，返回字段路径、类型与样例，比盲读原文省得多。
3. **需要数据用 python_exec 引用**：对大结果做统计/过滤/聚合时，把 `ref=<resultRef>` 作为 python_exec.inputs 的输入源，数据不经过上下文直接进沙箱。
4. **确需原文时用 read_tool_result 分页读**：传 `resultRef` + `offset`/`limit`；从预览处续读时 `offset` 用已读字符数，按 `nextOffset` 翻页。

## 禁忌

- 禁止反复用 read_tool_result 把大结果整段读进上下文（上下文预算会被单一结果吃光）。
- 禁止对 resultRef 已过期（24h 后）的结果做读取——过期后重新执行原工具。
- 多个 resultRef 需要联动分析时，分别作为 python_exec.inputs 的不同 alias 传入，而不是逐个读进上下文再拼接。
