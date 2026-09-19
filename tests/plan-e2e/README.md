# Plan 模式执行正确性：端到端测试场景与回放档案

本目录是 Plan Runtime（`executionMode=plan`）的**E2E 场景库与回放档案**：
`scenarios/` 描述每个场景的目的、触发方式与预期；`run_scenarios.js` 是可执行驱动；
`replays/` 存放从服务导出的权威回放记录（与 `/react/replay` 回放页同源）及索引。

完整测试结论与问题归因见 [docs/plan模式E2E测试报告.md](../../docs/plan模式E2E测试报告.md)。

## 场景总览

| # | 场景 | 验证维度 | 场景文档 |
|---|---|---|---|
| 01 | 复杂正常流 | 多 AGENT 步骤串联、repo 检索 + python_exec 工具充分调用、Scoped Run 隔离、Finalizer 写回 | [01-complex-happy-path.md](scenarios/01-complex-happy-path.md) |
| 02 | USER_INPUT 等待/恢复 | 持久化等待、wait_request 协议、WS `plan_resume`、等待期并发互斥 | [02-user-input-ws-resume.md](scenarios/02-user-input-ws-resume.md) |
| 03 | USER_ACTION 拒绝 | `approved:false` → 取消级联（当前+后续步骤、Wait、Plan），已成功步骤不回滚 | [03-user-action-reject-cancel.md](scenarios/03-user-action-reject-cancel.md) |
| 04 | 运行中取消 | HTTP cancel 级联 + WS 控制命令运行态被拒的协议窗口 | [04-midrun-cancel.md](scenarios/04-midrun-cancel.md) |
| 05 | HTTP 恢复/连接无关 | 关闭 WS 后 HTTP resume 续跑至终态（等待态与连接解耦） | [05-http-resume-durable.md](scenarios/05-http-resume-durable.md) |
| 06 | WAIT 态互斥 + schema | waiting_plan 计入活跃互斥；Planner 自定义 responseSchema 被 Resume 强制 | [06-wait-mutex-and-schema.md](scenarios/06-wait-mutex-and-schema.md) |
| 07 | 超时分类（修复验证） | 超时≠取消（P1）、timeoutSeconds 可配（P3）、失败级联+Retry 复活（P8） | [07-timeout-classification.md](scenarios/07-timeout-classification.md) |
| 98 | Bug 证据样本 | 修复前「超时被误判为取消」实跑回放（报告 §6-P1 证据） | 见 [replays/manifest.md](replays/manifest.md) |

## 文件说明

| 文件 | 职责 |
|---|---|
| `driver.js` | WS 客户端封装：事件流消费、视图快照提取、waitUntil 轮询断言、HTTP 工具 |
| `run_scenarios.js` | 场景与断言（文件头有场景清单注释），产出 `artifacts/results.json` |
| `mutex_probe.js` | 会话并发互斥独立探针（**sessionId 必须置于 WS 消息顶层**） |
| `export_replays.js` | 从服务导出各场景回放到 `replays/` 并生成 manifest 索引 |
| `scenarios/` | 单场景文档（目的/触发/预期/结果，沿用 tests/multi-agent 格式） |
| `replays/` | 回放记录归档：`<编号>-<场景>.json` + `manifest.md` 索引 |
| `artifacts*/`（gitignore） | 每次运行的原始事件流 JSON（单次可达 5 万+条/数十 MB，不入库） |

## 运行方式

需要真实环境：依赖容器（`docker compose up -d mysql redis sandbox`）+ 服务本地直跑
（`go run main.go`，:8180）+ glm-4.6 模型 + `demo-app` caller。repo 检索工具依赖
`data/repo-mirrors` 镜像库；mcpgw 网关类工具不在本套件依赖内（未启动仅记日志）。

```bash
cd tests/plan-e2e

# 全部场景（S1 视任务规模 10~30 分钟，其余各 1~8 分钟；可加超时余量）
node run_scenarios.js

# 指定场景（s1 s2 s3 s4 s5 s6 t7）
node run_scenarios.js s4 t7

# 并发互斥独立探针（对任意活跃/等待中的会话）
node mutex_probe.js <sessionId>

# 重新导出回放记录（服务运行中，按需更新脚本内 REPLAYS 映射）
node export_replays.js
```

## 驱动侧已知坑（写断言前必读）

1. **sessionId 位置**：并发探针的 sessionId 必须放 WS 消息顶层（`{type, sessionId, payload}`），
   放进 payload 会被忽略并开新会话，得出"未拒"假阴性（场景 06 的历史教训）。
2. **取消后的终态信号**：HTTP cancel 的 emitter 不接 WS，客户端权威信号是
   `plan_view_update` 视图状态翻转；且 WS 探针被拒的 error 事件会污染
   `done/cancelled/error` 三态判定（场景 04 的历史教训）。
3. **HTTP Retry 是同步阻塞语义**：重试步骤再次失败时错误随响应返回（errNo=-1），
   不代表命令被拒；判定复活成功要看新 Attempt 与步骤状态复位。
4. **run payload 必带 modelKey/modelVersion**，否则在参数校验即被拒。

## 结果归档约定

每次整批验证后：① 更新 `docs/plan模式E2E测试报告.md`（结论与归因）；
② 把代表性运行更新进 `export_replays.js` 的 REPLAYS 映射并重跑导出；
③ `artifacts/` 仅本地保留，不入库。
