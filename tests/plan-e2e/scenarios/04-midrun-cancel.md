# 场景 04：运行中取消 + WS 命令窗口探针

- **场景函数**：`scenarioS4`（`node run_scenarios.js s4`）
- **维度**：运行中取消级联、WS 控制命令适用窗口、双通道语义

## 目的

验证步骤执行中（RUNNING）取消整个计划的级联收敛，以及 WS `plan_cancel` 在
run 活跃期被控制器拒绝的协议约束（运行中取消应走 HTTP 或普通 `cancel` 消息）。

## 触发

```
请制定一个多步骤纯计算任务，每个计算步骤都要调用 python_exec 工具：
1）计算 1000 以内的素数个数与总和；2）计算斐波那契数列第 40 项的值；
3）用蒙特卡洛方法（10 万次采样）估算圆周率；4）汇总以上三项结果输出中文报告。
```

驱动策略：等首个 AGENT 步骤 RUNNING → 先发 WS `plan_cancel` 探针（应被拒）→
再走 `POST /react/plan_execution/cancel` → 等视图终态翻转。

## 预期

1. WS `plan_cancel` 探针返回 error："plan command rejected while a run is active"
   （等待态才接受 WS 控制命令——协议约束，见 docs/system/plan.md v1.1 接口清单加注）
2. HTTP cancel 成功（errNo 0）；当前 RUNNING 步骤与全部后续 PENDING 步骤置 CANCELLED，
   Plan=CANCELLED，无孤儿 PENDING 行
3. 取消的权威信号是 `plan_view_update` 视图状态翻转（HTTP cancel 的 emitter 不接 WS，
   不能用 done/cancelled/error 事件判定——驱动侧曾因此在终态断言上翻车，见 README 备注）

## 结果

见 [replays/manifest.md](../replays/manifest.md) 编号 04（修复后复测，PASS 71s）。
