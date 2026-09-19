> **本文件是从真实项目摘取的范例。** 看结构、详细程度和写作风格，不要照搬业务内容。
> 这是**修复类**变更的典型形态 —— 与功能类的关键差别是 §1 展开为
> `### 问题描述` / `### 根因分析` / `### 影响范围` 三个子节。
> 约一半的变更文档属于这一类。功能/优化类见 `changelog-example.md`。
>
> 注意：这份原文的后半部分章节名与标准模板有偏差（用了「测试结果」「修复效果」
> 而非「测试验证」「风险与兼容性」，且缺「历史版本」节）。这是真实项目中的
> 实际偏差，**你应该按标准模板写**，本范例的价值在于 §1 和 §2 的写法。

---

---
title: 修复新蓝鲸权限过期时间未校验
date: 2026-09-07
version: v1.0
type: changelog
module: auth
maintainer: liwenjiao
status: active
related_code:

- utils/auth.go
- utils/auth_test.go
summary: 修复 getNewPermissions 函数未校验权限有效期（BeginTime/EndTime），导致已过期权限仍被当作有效权限使用的Bug

---

# 修复新蓝鲸权限过期时间未校验

## 1. 背景与目标

### 问题描述

有老师反馈自己在老蓝鲸的权限已过期（EndTime: 2026-09-04），不应该再有 download 权限，但系统仍然返回 `authRole: ["download", "read"]`。

### 根因分析

通过线上日志追踪（requestId: `4509e496370b17dc`），发现 `utils/auth.go` 中的 `getNewPermissions` 函数在处理 authority-svc 返回的权限列表时，**完全没有校验 `EndTime` 和 `BeginTime` 字段**，导致已过期的权限记录仍然被当作有效权限使用。

具体案例：

- 用户：`xuchang15`
- 报表：`r_86aa9a2ce8f24c77b7ec436025f884b3`（飞瓜-抖音商品监控）
- 权限记录：`Action=download, EndTime=2026-09-04 23:59:59`（已过期3天）
- 实际返回：`authRole: ["download", "read"]`（不应包含 download）

### 影响范围

所有通过新蓝鲸权限系统（authority-svc）授权且设置了过期时间的报表权限都可能存在此问题。

## 2. 代码改动

### 文件：`utils/auth.go`

1. **新增 `isPermissionTimeValid` 函数**：提取权限时间有效性校验为独立函数
   - 校验 `EndTime`：不为空时检查当前时间是否已超过过期时间
   - 校验 `BeginTime`：不为空时检查当前时间是否已到达生效时间
   - 时间格式解析失败时，出于安全考虑默认视为有效（避免误拦截）
   - 时间字段为空时，不做限制（视为永久有效）

2. **修改 `getNewPermissions` 函数**：在遍历 `PermissionList` 时增加时间校验
   - 在判断权限类型之后、添加到 `detailedPermissions` 之前，调用 `isPermissionTimeValid` 校验
   - 已过期权限记录 `expiredCount`，未生效权限记录 `notStartedCount`
   - 增加详细的日志输出，包括过期/未生效/有效权限的日志

### 文件：`utils/auth_test.go`

新增 `TestFilterExpiredPermissions` 测试用例，覆盖以下场景：

- 有效权限（在有效期内）
- 过期权限（已超过 EndTime）
- 未生效权限（未到 BeginTime）
- 边界情况（EndTime/BeginTime 为空字符串）
- 格式错误（时间格式不正确应视为有效）
- 线上案例模拟（EndTime 等于3天前）

## 3. 测试结果

```
=== RUN   TestFilterExpiredPermissions
=== RUN   TestFilterExpiredPermissions/有效权限-在有效期内
=== RUN   TestFilterExpiredPermissions/过期权限-已超过EndTime
=== RUN   TestFilterExpiredPermissions/未生效权限-未到BeginTime
=== RUN   TestFilterExpiredPermissions/边界情况-EndTime为空字符串
=== RUN   TestFilterExpiredPermissions/边界情况-BeginTime为空字符串
=== RUN   TestFilterExpiredPermissions/边界情况-BeginTime和EndTime都为空
=== RUN   TestFilterExpiredPermissions/格式错误-EndTime格式不正确应视为有效
=== RUN   TestFilterExpiredPermissions/刚好过期-EndTime等于3天前（模拟线上案例）
--- PASS: TestFilterExpiredPermissions (0.00s)
```

所有8个测试用例全部通过。

## 4. 修复效果

修复后，对于上述线上案例：

- **修复前**：`authRole: ["download", "read"]`（包含已过期的 download 权限）
- **修复后**：`authRole: ["read"]`（过期的 download 权限被正确过滤）

---

## 这份范例值得注意的地方

1. **§1 三段式是修复类的标志** —— 问题描述（现象）→ 根因分析（代码层面的原因）→ 影响范围（波及面）。

2. **根因追到了具体函数和具体缺失** —— "`getNewPermissions` 完全没有校验 `EndTime` 和 `BeginTime` 字段"。不写"权限逻辑有 bug"这种无信息量的话。

3. **给出了可追溯的线上证据** —— requestId、用户名、报表 hash、权限记录的具体字段值、"已过期3天"。这些让后来人能确认这个 bug 是真实发生过的，而不是理论推测。

4. **§2 用编号 + 加粗函数名组织** —— 改动较复杂时，`### 文件：xxx` 二级标题 + 编号列表比平铺的 bullet 更清晰。

5. **记录了安全侧的设计决策** —— "时间格式解析失败时，出于安全考虑默认视为有效（避免误拦截）"。这是个有争议的选择（也可以选择默认拒绝），写下理由，后来人才知道不是疏漏。

6. **测试用例逐条列出覆盖场景** —— 包括边界（空字符串）、异常（格式错误）、以及**线上案例模拟**。

7. **§4 用修复前/修复后对照** 。

8. **偏差提示**：本文用了「测试结果」「修复效果」，缺「风险与兼容性」和「历史版本」。**你写的时候要用标准 6 节模板** —— 把测试输出放 §4 测试验证，把修复前后对照放 §1 或 §5，并补齐 §5 风险与兼容性和 §6 历史版本。