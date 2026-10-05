# 接入方式

本文说明如何作为调用方接入 react-base-service：前置资源注册、WebSocket 运行协议、HTTP API、工具/Skill 接入、回放与 SDK。

所有接口免鉴权：请求可携带 `X-User-Name` 头指定操作人（用于会话归属过滤），缺省为 `anonymous`。

统一前缀：`/react-base-service`。

## 1. 前置注册（一次性）

按顺序注册四类资源（均为 POST）：

### 1.1 Caller

```
POST /caller/register
{ "callerKey": "my-app", "name": "我的应用", "description": "...", "platform": "web", "allowPlan": 1 }
```

`allowPlan`（可选，caller 级 Plan 能力开关）：不传或 `null`=跟随全局配置 `llm.react.allow_plan`（默认开）；`0`=该 caller 强制关闭 create_plan 工具与 `executionMode: "plan"` 入口；`1`=强制开启。`/caller/update` 可修改（不传=不更新）。典型用法：纯文本端（QQ 机器人等）注册时设 `allowPlan: 0`。

### 1.2 API Key（caller 级模型凭证）

```
POST /apikey/register
{ "callerKey": "my-app", "routeValues": [], "name": "默认", "apiKey": "<LLM网关key>" }
```

routeValues 为空数组表示通用；解析时按 callerKey + 路由前缀最长匹配。

### 1.3 系统提示词（可选，强烈建议）

```
POST /system-prompt/register
{ "callerKey": "my-app", "routeValues": [], "name": "主提示词", "content": "你是一个……的助手。" }
```

多条提示词按路由从短到长（通用→具体）拼接为最终 system 前缀。

### 1.4 工具与 Skill（按需）

见第 4、5 节。

## 2. WebSocket 运行协议

### 2.1 建连

```
GET ws://<host>:8080/react-base-service/react/ws?connection_attempt_id=<可选排查ID>
```

连接后同一时刻只允许一个活跃 run；并发提交第二个 run 会收到 error（"react run is active"）。

### 2.2 客户端 → 服务端消息

信封：`{ "type": "...", "runId": "...", "sessionId": "...", "payload": {...} }`

| type | 时机 | payload |
|---|---|---|
| `run` | 发起/继续一轮对话 | `ReactRunPayload`（见下） |
| `cancel` | 取消当前 run | 可选 `{reason}` |
| `tool_use_answer` | 回答 `ask_question` 工具的提问 | `{toolUseId, answer}` |
| `client_tool_use_end` | 回填 client 工具执行结果 | `{toolOutputs: [{toolUseId, content, meta, isError}]}` |

`ReactRunPayload`：

```jsonc
{
  "callerKey": "my-app",           // 必填
  "routeValues": ["pageA"],        // 路由（决定提示词/工具/Skill 可见范围）
  "type": "chat",                  // 会话类型，默认 chat
  "userPrompt": "帮我分析……",       // 必填，本轮用户输入
  "sessionId": "session_xxx",      // 不传=新建会话；传=复用（归属校验）
  "attachments": [                 // 可选，附件引用（先经上传接口拿 fileId）
    {"fileId": "file_xxx", "fileName": "data.csv", "description": "销售数据"}
  ],
  "controlContext": {"requestSource": "..."},  // 运行控制参数（不入模）
  "llmContext": {...},             // 业务上下文，随用户消息入模（JSON 对象或字符串）
  "modelKey": "DeepSeek",          // 模型种类（与 modelHash 二选一）
  "modelVersion": "deepseek-v3",   // 可省，走目录默认版本
  "modelHash": "...",              // 用户自定义模型 hash（playground 个人模型）
  "maxSteps": 8                    // 可省，走配置默认
}
```

### 2.3 服务端 → 客户端事件

信封：`{ "type": "...", "seq": n, "runId": "...", "sessionId": "...", "stepIndex": n, "payload": {...} }`

| type | payload 要点 |
|---|---|
| `run` | 回放/直播同形：本轮完整 run payload |
| `thought_start/delta/end` | 真实 reasoning 增量（仅模型输出 reasoning 时出现）；end 带 token 用量 |
| `content_start/delta/end` | 正文增量；end 带完整正文与 token 用量 |
| `tool_use_start` | `{toolUseId, toolName, toolInput, description, executedBy: server\|internal, status}` |
| `tool_use_end` | `{toolUseId, content(预览), resultRef, truncated, isError, status, durationMs, meta}` |
| `client_tool_use_start` | 前端执行的工具：`{toolUseId, toolName, toolInput, frontendHint, status: waiting}`，收到后前端执行并以 `client_tool_use_end` 回填 |
| `client_tool_use_end` | 服务端回声（含标准化结果），前端据此收敛工具卡片 |
| `todo_update` | `{todoState}`（todo_write 触发） |
| `compact_start/end` | 上下文压缩开始/完成（end 带摘要） |
| `model_fallback` | 模型互备切换 `{fromModelKey, toModelKey, reason, resetCurrentOutput}` |
| `done` | run 成功收敛，带累计 token 与 contextUsedTokens/maxContextTokens |
| `error` | `{errNo, errMsg}` |
| `cancelled` | 用户取消收敛 |
| `heartbeat` | 每 20s 应用层保活（协议层另有 5s ping，浏览器自动回 pong） |

### 2.4 最小接入示例（浏览器）

```js
const ws = new WebSocket(`ws://${location.host}/react-base-service/react/ws`);
ws.onopen = () => ws.send(JSON.stringify({
  type: 'run',
  payload: { callerKey: 'my-app', userPrompt: '你好，介绍下你能做什么' },
}));
ws.onmessage = (e) => {
  const evt = JSON.parse(e.data);
  switch (evt.type) {
    case 'content_delta': appendText(evt.payload.contentDelta); break;
    case 'done':          finish(); break;
    case 'client_tool_use_start': runFrontendTool(evt.payload); break; // 完成后回发 client_tool_use_end
    case 'error':         showError(evt.payload.errMsg); break;
  }
};
```

也可直接嵌入服务自带的 TypeScript SDK：`<script src="/react-base-service/react/sdk/0.0.1.js">`（源码在 `web/sdk/`，封装了连接管理、会话恢复、工具协议）。

## 3. HTTP API 一览

### ReAct 周边能力

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/react/ws` | ReAct WebSocket 单入口 |
| GET | `/react/models` | 运行时可选模型列表（配置驱动） |
| POST | `/react/session/list` | 会话列表（分页 + 关键词） |
| POST | `/react/session/events` | 历史事件流（回放） |
| POST | `/react/session/feedback` | 会话维度轮次反馈回显 |
| POST | `/react/run/feedback` | 轮次点赞/点踩与问题反馈 |
| POST | `/react/async_task/list` | 会话异步任务状态（游标分页） |
| GET | `/react/artifact/:artifactId` | python_exec 产物下载 |
| POST | `/api/chat/files/upload` | 附件上传（csv/md/txt，≤50MB）→ fileId |

### 注册类管理接口

`/caller`（register/update/list/copy_config/batch_delete）、`/skill`（create/update/delete/list/detail）、`/tool`（register/update/delete/list/detail + `/tool/whitelist` CRUD）、`/apikey`（CRUD）、`/system-prompt`（CRUD）、`/model`（用户模型 CRUD、whitelist、check-connectivity、credits/adjust）、`/models`（模型目录）。

## 4. 工具接入

### 4.1 HTTP 工具（后端代理执行）

```
POST /tool/register
{
  "name": "查询订单",
  "description": "按订单号查询订单详情",
  "toolType": "http",
  "callerKey": "my-app",
  "routeValues": [],
  "config": {
    "url": "https://internal.example.com/api/order/detail",
    "method": "post",
    "headers": {"X-Api-Version": "v2"},
    "inputSchema": {            // JSON Schema 子集：type/properties/required/enum/minLength/items/additionalProperties
      "type": "object",
      "properties": {
        "orderId": {"type": "string", "minLength": 1}
      },
      "required": ["orderId"]
    },
    "outputSchema": {...},      // 仅供模型理解输出结构，不做强制校验
    "description": "覆盖 description 的展示文案（可选）"
  }
}
```

执行时后端透传当前请求 Cookie 与调用方运行时上下文头（X-Session-Id / X-User-Name 等）。

### 4.2 Client 工具（前端执行）

`toolType: "client"`，`config.frontendHint` 为前端展示提示。运行时模型调用后服务端发 `client_tool_use_start`，前端执行完回发 `client_tool_use_end`，run 转 `waiting_client_message` 等待。

### 4.3 异步提交型工具

`config.async: true` + `config.asyncHint: "用 xxx 工具查询结果，taskId 放 body.query.taskId"`。提交成功后任务进入 `<async_tasks>` 提醒循环，模型确认完结后调用 `resolve_async_task` 清除。

### 4.4 白名单

`POST /tool/whitelist/create`：`{toolId, whiteUserList: ["user1"]}`。配置后仅白名单用户可见该工具；未配置则对 caller 内所有用户可见。

### 4.5 运行时调用模式（不变）

模型侧统一走 `get_tool`（加载 schema）→ `execute_tool`（执行）；工具定义变更自动触发重新加载；大结果自动转 resultRef 分片读取。

## 5. Skill 接入

```
POST /skill/create
{
  "name": "周报生成流程",
  "description": "当用户要求生成周报时使用",
  "triggerCondition": "用户提到周报、weekly report",
  "forbiddenCondition": "用户只是询问周报模板时",
  "executionSteps": "1. 收集本周数据 2. 按模板生成 3. ……",
  "businessContext": "……",
  "promptSupplement": "……",
  "callerKey": "my-app",
  "routeValues": [],
  "isDefault": 0
}
```

注入模式（不变）：run 启动时全部可见 Skill 的**摘要索引**写入 system 前缀（`## 当前 run 可用 Skill 摘要索引`）；模型判断适用后调用 `get_skill` 加载完整说明，加载记录落 run。

## 6. 会话管理与回放接入

- **多轮对话**：首轮回执事件中取 `sessionId`，后续 run 带上即可延续上下文；切换用户/caller/路由会被拒绝（`sessionId 无权访问或上下文不匹配`）。
- **历史侧边栏**：`POST /react/session/list`（按 callerKey+routeValues+登录用户过滤）。
- **回放**：`POST /react/session/events` 拿到事件流后，用与实时 WS 完全相同的解析逻辑渲染；或直接跳转内置回放页 `/react-base-service/react/replay?sessionId=...`。
- **反馈**：每轮回执后调 `POST /react/run/feedback`（feedback: 1/-1，problemFeedback 文本）。

## 7. 鉴权与身份

服务不做登录鉴权，操作人身份按以下方式确定：

| 场景 | 方式 |
|---|---|
| 任意调用方 | 请求头 `X-User-Name`（缺省 `anonymous`），用于会话归属与列表过滤 |
| 静态资源 | playground / replay / SDK 直接访问 |

playground 页面访问可用 `custom.yaml llm.react.playground_whitelist` 限制；为空不限制。
