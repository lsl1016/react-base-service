# 业务工具两段式调用规范

业务工具（Business Tool，HTTP/MCP 类型）不会把全部 schema 一次性塞进系统提示词，采用按需加载协议。直接猜测参数调用会失败。

## 协议

1. **get_tool**：按 `toolId` 或 `name` 加载目标工具，返回 `parameters`（入参 schema）、`outputSchema`、`callName`。系统的工具摘要索引在系统提示词里，先用它定位候选工具。
2. **execute_tool**：顶层传 `description`（名词短语，本次调用目的）+ `toolId`，业务参数放进 `input`，必须符合 get_tool 返回的 parameters。

## 要点

- **不要直接调用 callName**：callName 只出现在 get_tool 的返回里，直接调用会被拒绝（未激活）。
- **入参必须符合 schema**：execute_tool 前对照 parameters 检查字段名与类型；校验失败会整次拒绝。
- **定义变更自愈**：管理员在 run 期间改了工具定义时，execute_tool 会要求重新 get_tool；看到 "definition has changed" 提示就重新加载一次。
- **历史轮次里出现过的工具**：新 run 已自动恢复加载（定义未变时），可直接 execute_tool；被拒绝时按提示重新 get_tool。
- **工具选型**：摘要索引里的 description 决定用哪个工具；两个工具都可能适用时优先选 outputSchema 与目标产出更接近的那个。
