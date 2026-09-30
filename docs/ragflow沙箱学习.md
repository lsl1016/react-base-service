# RAGFlow 沙箱学习（用于优化 react-base-service 的 python_exec 沙箱）

> 对照对象：`ragflow-deploy` 仓库的 `agent/sandbox/`（Python 实现，执行内核）与 `internal/agent/sandbox/`（Go 实现，客户端）。
> 本文与 [python_exec沙箱加固方案.md](./python_exec沙箱加固方案.md)（参考 Codex）互补：那份讲"边界怎么守"，这份讲"工程怎么组织"——执行内核、结果协议、错误分类、并发治理、测试策略，并逐条映射回我们的 P0/P1/P2 计划。
> 行号以 2026-09-30 的代码为准。

---

## 0. 结论速览

RAGFlow 沙箱最值得搬的不是某个技术，而是四个协议级决策：

| # | 决策 | 对我们的价值 |
|---|------|------------|
| 1 | **结构化返回走独立 marker 行，不走 stdout**：约定代码必须定义 `main(**args)`，驱动器把返回值 base64-JSON 打成 `__RAGFLOW_RESULT__:` 前缀行，stdout 永远保持人类可读 | 消灭"整个 stdout 必须是单 JSON"的脆弱约定 |
| 2 | **产物走文件通道，不走 stdout**：代码把文件写到 `artifacts/` 目录，沙箱服务端收集（扩展名白名单 + 数量/大小上限 + 文件名消毒），以 `artifacts: [{name, mime_type, size, content_b64}]` 独立字段回传 | **消灭 `scrubPythonExecBase64` 这层补丁存在的必要性**——base64 从源头上不进 stdout |
| 3 | **机器可读的错误分类**：每次执行必带 `status`（success/program_error/resource_limit_exceeded/unauthorized_access/program_runner_error）+ 细分类型；退出码语义化（124=超时，137=OOM） | 就是我们 P2-3 想要的 denyReason，RAGFlow 给了完整taxonomy |
| 4 | **fail-closed 的共享密钥鉴权 + 双层限流**：没配 token 时 `/run` 直接 503 拒绝服务（要显式 opt-in 才开放）；认证前小限额 + 认证后大限额 | 直接回应我们的 R7（沙箱免鉴权）和 R5（无并发治理） |

另外两点工程组织上的收获：**Provider 抽象**（协议与实现解耦，self_managed/e2b/aliyun/ssh/local 可切换，Go/Python 双端共享同一 wire format）和**安全即回归测试**（鉴权矩阵、限流、网络默认值都有行为测试）。

---

## 1. 全景架构：三层结构

```
┌─────────────────────────────────────────────────────────────────┐
│  调用方（agent 组件 / CodeExec 工具）                              │
│    Python: agent/sandbox/client.py  execute_code()               │
│    Go:     internal/agent/sandbox/manager_client.go              │
├─────────────────────────────────────────────────────────────────┤
│  Provider 抽象层（客户端库，随主服务进程跑）                        │
│    providers/base.py SandboxProvider ABC（7 个实现可切换）          │
│    internal/agent/sandbox/provider.go SandboxProvider interface   │
│    └─ SelfManagedProvider ──HTTP──→ executor_manager             │
├─────────────────────────────────────────────────────────────────┤
│  executor_manager（独立 FastAPI 服务，容器池管理器，可信面）         │
│    agent/sandbox/executor_manager/  POST /run + GET /healthz     │
│    ┌────────────────────────────────────────────┐               │
│    │ 预热容器池：sandbox_python_0..N / sandbox_nodejs_0..N │       │
│    │ 每个容器：gVisor(runsc) + read-only + tmpfs + nobody │        │
│    │           + --network none + --memory 256m           │        │
│    └────────────────────────────────────────────┘               │
└─────────────────────────────────────────────────────────────────┘
```

关键信任拓扑：**不可信的只有 runner 容器内执行的代码**。executor_manager 本身是特权进程（挂 docker.sock、privileged），所以它用"只绑回环地址 + token + 限流"保护自己（`docker-compose.yml:11` 的 `127.0.0.1:9385:9385`）。

我们现在的 `sandbox/server.py` 相当于把 executor_manager 和 runner 合并成了一个容器：管理面（HTTP 服务）和执行面（fork 出的 python 子进程）同容器。这个模型没问题，但要意识到**我们的可信边界只能在容器网络层划**——这正是加固方案 P0-1 做的事。

### 容器安全参数（`executor_manager/core/container.py:84-100`）

```python
create_args = [
    "docker", "run", "-d",
    "--runtime=runsc",            # gVisor：用户态内核，拦截系统调用
    "--name", name,
    "--read-only",                # 根文件系统只读
    "--tmpfs", "/workspace:rw,exec,size=100M,uid=65534,gid=65534",  # 工作区 tmpfs，属主 nobody
    "--tmpfs", "/tmp:rw,exec,size=50M",
    "--user", "nobody",           # 非 root
    "--workdir", "/workspace",
    "--network", network,         # 默认 "none"（container.py:104-105）
    "--memory", "256m",           # cgroup 级内存限制
]
# 可选：--security-opt seccomp=/app/seccomp-profile-default.json（默认关）
```

对照我们的 compose（`docker-compose.yml:53-58`）：非 root ✅、read_only ✅、tmpfs ✅、mem/cpus ✅，**缺网络隔离（R1）和 gVisor runtime（P2-4 第 2 步）**。RAGFlow 证明 gVisor 只是 compose/K8s 里一行 `runtime:` 配置的事，代价是宿主要装 runsc 且只支持 Linux。

RAGFlow 对"沙箱代码需要网络"的答案值得借鉴：**默认 `--network none`，需要时用 `SANDBOX_CONTAINER_NETWORK=bridge` 显式 opt-in，且官方建议把依赖预烘进 base image 而不是运行时 pip install**（README "Network Isolation" 一节）。我们的等价物：matplotlib/numpy 预装在镜像里（已做），绝不为 `pip install` 开网络。

---

## 2. 执行内核：一次 /run 的完整生命周期

`executor_manager/services/execution.py:196-314`，按顺序：

1. **鉴权与限流**（`api/routes.py:31`）：preauth 限流（30/min，认证前就限，让错误 token 洪水也能拿到 429）→ token 校验 → 认证后限流（120/min）。每语言信号量 `async with _CONTAINER_EXECUTION_SEMAPHORES[lang]` 先占坑（`api/handlers.py:34`）。
2. **AST 静态扫描**（`services/security.py`）：Python 黑名单 import（os/subprocess/sys/socket/ctypes/pickle/threading…）+ 危险调用；JS 正则黑名单（child_process/fs/worker_threads/eval/Function/process.binding）。不安全直接返回 `exit_code=-999, detail="Code is unsafe"`，**不碰容器**。
3. **分配容器**（`core/container.py:173-187`）：从语言队列 `get_nowait()`，10 秒内轮询重试；取到的容器先 `docker inspect` 确认活着，死了就地重建。池耗尽 → 立即返回 `exit_code=-10, stderr="Container pool is busy"`（快速失败，不排队堆积）。
4. **构建执行包**（`_build_execution_bundle`，execution.py:59-176）：三个文件——用户代码 `main.py`、驱动器 `runner.py`、参数 `args.json`。**驱动器和用户代码分离**，runner 只做一件事：
   ```python
   sys.path.insert(0, os.path.dirname(__file__))
   from main import main
   args = json.load(open("args.json"))
   result = main(**args)
   emit_result(result)   # 打 __RAGFLOW_RESULT__: <base64 JSON>
   ```
   且 runner 会先 `os.makedirs("artifacts", exist_ok=True)`，给产物一个标准落点。
5. **注入文件**：宿主临时目录（0700）写三个文件 → `tar czf - | docker exec -i … tar xzf - -C /workspace/{task_id}`。用 tar 流而不是 `docker cp` 是有原因的——代码注释明确写了 **docker cp 在 gVisor tmpfs 下不工作**（execution.py:417）。
6. **执行 + 三层超时防线**：
   - 容器内：`timeout {N} python -I -B runner.py`（`timeout` 命令，124 退出码）
   - 容器外：`async_run_command(..., timeout=TIMEOUT+5)`（asyncio 兜底）
   - 兜底的兜底：asyncio 超时后额外 `docker exec … pkill -9 python`（execution.py:296-297），防止容器内还有僵尸子进程占着池
7. **退出码语义化**（execution.py:263-294）：
   - `0` → SUCCESS，走结果提取 + 产物收集
   - `124` → RESOURCE_LIMIT_EXCEEDED / time
   - `137` → RESOURCE_LIMIT_EXCEEDED / memory（OOM 被 cgroup 杀）
   - 其它 → `analyze_error_result(stderr)` 按 stderr 模式归类：`Permission denied`→文件访问越权，`Operation not permitted`→被禁系统调用，`MemoryError`→内存，其余→程序错误
8. **产物收集**（详见 §4）
9. **清理**（finally，execution.py:311-314）：容器内 `rm -rf /workspace/{task_id}` + 宿主 `rm -rf workdir` + 容器归还池。归还时再查一次活状态，死了就地重建（`release_container`，container.py:160-170）——**池的自愈闭环**。

### 容器池的取舍

池化 = 预热 N 个常驻容器（`sleep infinity`），代码用 `docker exec` 注入，省掉每请求的容器启动开销。代价是**容器跨请求、跨任务复用**，隔离性靠每任务的独立工作区 `/workspace/{task_id}` + finally 清理保证。

对我们的启示：我们现在是 fork 模型（同容器内起子进程），延迟已经够好，**不必引入池**；但 RAGFlow 的三层超时、退出码语义、快速失败（busy 而不是排队）、执行后清理这四件事，fork 模型下全部适用且我们都缺或只有一半：

| 机制 | RAGFlow | react-base-service 现状 |
|------|---------|------------------------|
| 超时 | timeout 命令 + asyncio+5s + pkill 三层 | communicate(timeout) + killpg 两层 ✅ 基本够 |
| 退出码分类 | 124/137 → 类型化 status | 只有 timedOut 布尔 + 原始 exitCode |
| 池/并发耗尽 | 立即 busy 失败（exit_code=-10） | 无上限排队（R5） |
| 执行后清理 | finally 双端 rm | tmpdir 留在 /tmp（次要） |

---

## 3. 结果协议：marker 行 + main() 契约（最值得抄的一块）

`agent/sandbox/result_protocol.py` 与 `internal/agent/sandbox/result_protocol.go`（两份等价实现，注释里明确声明 marker 是 wire contract，改名即破坏协议）：

**约定**：用户代码必须定义 `main(**args)`（Python）/ 导出 `main(args)`（JS）。驱动器调用它，把返回值包成 `{"present": true, "value": …, "type": "json"}`，JSON 编码后 base64，打出一行：

```
__RAGFLOW_RESULT__:eyJwcmVzZW50Ijp0cnNlLCJ2YWx1ZSI6MTN9
```

**提取**（`ExtractStructuredResult`）：逐行扫 stdout，认前缀行、base64 解码、JSON 解析；解码失败的 marker 行**原样留在 stdout 里**（让用户看到原始数据）；清掉 marker 行后的 stdout 原样返回。多行 marker 时最后一行生效。

**参数注入的细节**（result_protocol.go:64-76）：Go 侧把 args JSON 先 base64 再拼进 wrapper，运行时 `json.loads(base64.b64decode(...))`。注释写明了动机——**避免 raw JSON 直接拼进 Python 源码**（true/false/null vs True/False/None 的差异、引号转义），同时把"用户数据 → 代码"的注入面收敛到纯 base64 字母表。RAGFlow 的 server 端实现（args.json 文件）更彻底：参数走文件，完全不拼代码。

### 对比我们的现状

| | RAGFlow | react-base-service |
|---|---------|-------------------|
| 结构化返回 | marker 行，stdout 纯净 | stdout 必须是单 JSON 对象（`extractPythonExecArtifacts` 乐观解析，混入调试输出/双 JSON/截断就失效） |
| 产物 | 文件写 `artifacts/` 目录，服务端收集 | 模型必须把 base64 塞进 stdout JSON 的 artifacts 数组 |
| 兜底 | 无需兜底 | `scrubPythonExecBase64` 逐字节扫描洗 base64（python_exec.go:220-259，注释自己承认是"最后防线"） |

**建议的改造**（优先级排在我们 P1 之后、和 P2-3 一起做收益最大）：

1. server.py 增加 runner 包装：请求里的 `python` 只允许定义函数，server 拼驱动器调 `main(**args)`，返回值打 marker 行（协议细节可直接抄 `result_protocol.py`，25 行）。
2. 产物改文件通道：server 在 workdir 下建 `artifacts/`，执行后 os.walk 收集，按 RAGFlow 的白名单规则过滤，独立字段回传。Go 侧 `processPythonExecArtifacts` 的 COS 上传/落库/artifactId 流程**原样保留**——我们这块（描述符不含字节、displayFiles 指令、私有桶）设计得比 RAGFlow 还细，不用动。
3. `scrubPythonExecBase64` 保留一个版本周期作为过渡防线，产物通道稳定后降级为纯报警打点。

---

## 4. 产物收集的防御细节（execution.py:317-442）

即使通道独立，收集端也要防恶意文件名和滥用：

- **扩展名白名单**（`.png .jpg .jpeg .svg .pdf .csv .json .html`）→ MIME 映射表，白名单外跳过并告警；
- **数量上限 10 个 / 单文件 10MB**，超限跳过（让模型知道缩小重生成）；
- **文件名消毒**：拒绝含 `/`、`\`、`..`、控制字符（<0x20 或 0x7F）、`.` 开头的名字——防路径穿越和不可见字符；
- **根目录产物提升**（`_promote_root_artifacts`）：模型经常直接 `plt.savefig("chart.png")` 写在工作区根上而不是 artifacts/ 里，RAGFlow 会在收集前把根目录下白名单内的文件**主动挪进 artifacts/**，排除 `main.py/runner.py/args.json` 和隐藏文件。这是很实用的兼容层——工具描述里教模型写 `artifacts/chart.png`，但没写对也能收到；
- 读文件用容器内 `base64 <file>` 而不是 `docker cp`（gVisor 限制，见 §2.5）。

Go 侧还有一份跨 provider 共享的白名单（`internal/agent/sandbox/artifacts.go`），注释强调"加扩展名要同时改 N 处"——**产物契约是全局契约**，和我们在 `python_exec.go:407-423` 维护 `pythonExecArtifactKnownTypes` 表是同一个问题，值得把白名单收敛到一个常量源。

---

## 5. 服务面安全（直接回应我们的 R7/R5）

`services/auth.py` 的 fail-closed 设计：

```python
configured_token = os.getenv("SANDBOX_EXECUTOR_MANAGER_API_TOKEN")
if not configured_token:
    if 显式设置了 ALLOW_UNAUTHENTICATED=true:
        打一次醒目告警，放行          # 不安全状态必须是运营者的显式决定
    else:
        return 503                    # 默认拒绝服务，而不是默认开放
provided = request.headers.get("Authorization")[7:] 或 X-Sandbox-Token
secrets.compare_digest(provided, configured_token)   # 防时序攻击
```

配套措施：

- **端口只绑回环**：`127.0.0.1:9385:9385`（docker-compose.yml:11），注释直说"这个端点执行任意沙箱代码"；
- **双层限流**（routes.py:27-31 注释）：preauth（30/min）在 token 校验**之前**，保证错误 token 洪水最终也会收到 429；认证后 120/min 是正常业务配额；
- `/healthz` 不需要 token（探活专用）；
- 启动时打一条鉴权姿态日志（token 模式 / 显式开放模式 / 将拒绝模式），运营者一眼能看出自己处于哪种状态。

对我们的落地：`server.py` 加一个 `SANDBOX_TOKEN` 环境变量校验（`hmac.compare_digest`），未配置时默认 503，Go 侧 `pythonexec.Execute` 带上 `Authorization` 头。配合 P0-1 的 `internal: true` 网络，沙箱的暴露面就只剩 service 一条路径。**注意我们的部署形态**：RAGFlow 是单机 docker（回环绑定可行），我们若走 K8s，等价物是 NetworkPolicy 只允许 service 的 ingress + token。

---

## 6. Go 侧客户端工程（internal/agent/sandbox/）

这份 Go 代码是给我们这种"Go 主服务 + 独立沙箱"形态的直接参考：

- **Provider 接口**（provider.go:135-168）：`Initialize / CreateInstance / ExecuteCode / DestroyInstance / HealthCheck / SupportedLanguages` 六个方法 + `ExecutionResult`/`SandboxInstance` 统一结构。所有 provider（self_managed/e2b/aliyun/ssh/local/tenki/ucloud）可互换，wire format 与 Python 端 1:1 对齐（注释明确"字段镜像 base.py，下游可无差别 pattern match"）。
- **SandboxClient 注入点**（`internal/agent/tool/code_exec_client.go:53`）：工具层只依赖 `ExecuteCode(ctx, SandboxRequest)` 接口，默认是返回哨兵错误的 stub，启动时 `SetSandboxClient` 注入真实现——**工具永远不知道背后是哪个 provider**。我们可以在 `api/pythonexec` 包把自由函数 `Execute` 收敛成同款接口，方便单测注入（和现有 `pythonExecArtifactUploader` 变量的做法一致）。
- **HTTP 重试策略**（http.go:104-172）：只有幂等方法重试；5xx 重试、4xx 不重试；指数退避 + full jitter（200ms 起、3s 封顶）；`context.Canceled/DeadlineExceeded` 不算可重试错误。注意 **POST /run 是非幂等的，一次即止**——对应我们加固方案 P1-3 里"`retry:1` 对 python_exec 关闭"的判断。
- **配置热更新**（manager.go:171-217）：每次执行前从 system_settings 读配置，把 `provider_type + 配置 JSON` 序列化成快照串，**快照没变且 provider 已加载就跳过重建**——管理面板改配置即时生效，又不会每请求重建连接。
- **收尾用 `context.WithoutCancel`**（manager_client.go:38-40）：请求 ctx 取消后 DestroyInstance 仍能用独立的 10 秒超时跑完清理。我们调用沙箱是短连接 HTTP 没有这个生命周期问题，但若未来改成长连接/流式沙箱会用到。
- **防御性双解析**（self_managed.go:355-367）：服务端已提取 marker，客户端仍对 stdout 再跑一遍 `ExtractStructuredResult`，服务端字段优先、本地解析兜底。

---

## 7. 测试策略（回应 R6）

RAGFlow 的沙箱测试分三层，全部是**真实执行断言**：

| 层 | 文件 | 覆盖 |
|----|------|------|
| 静态扫描单测 | `tests/test_security.py` | 危险 import/调用必须被拒 |
| 端点安全 | `executor_manager/tests/test_run_endpoint_security.py`（241 行） | **鉴权矩阵**：缺 token 401 / 错 Bearer 401 / 错 X-Sandbox-Token 401 / 对的两种头 200 / 未配置 token 503 / 空 token 503 / 显式 opt-in 恢复开放 / truthy 拼写变体 / healthz 免鉴权；**限流**：超限 429、未认证洪水最终 429；**容器参数回归**：默认 `--network none`、空环境变量回退 none、白名单 env 覆盖透传、其它加固 flag 仍在 |
| 全链路安全 | `tests/sandbox_security_tests_full.py` | 对着跑起来的 compose 做端到端执行断言 |
| 产物 | `executor_manager/tests/test_artifact_promotion.py` | 根目录文件提升进 artifacts/ 的行为 |

最值得照抄的是 `test_run_endpoint_security.py` 的组织方式：**每个安全决定一个用例，包括"默认值必须是安全值"这种部署回归**（`test_container_defaults_to_network_none` 断言生成的 docker run 参数里 network 是 none——配置漂移会被测试抓住）。我们的 P1-4 拒绝回归集 + 行为集之外，应加一组"配置回归"：断言 compose 里 sandbox 网络隔离配置存在、token 已配置，防止未来改 compose 时无声退化。

---

## 8. 逐项对比总表

| 维度 | RAGFlow | react-base-service 现状 | 差距/动作 |
|------|---------|------------------------|----------|
| 隔离边界 | gVisor + read-only + nobody + network none + cgroup mem | fork + rlimit + 非 root 容器，**无网络隔离** | P0-1 / P2-4 |
| rlimit 失败 | N/A（cgroup 由 docker 保证） | fail-open（R4） | P1-1 |
| 鉴权 | fail-closed token + compare_digest | 无（R7） | **新增，见 §5** |
| 限流/并发 | 双层限流 + 每语言信号量 + 池耗尽快速失败 | 无（R5） | P1-3 |
| 结构化返回 | main() 契约 + marker 行 | stdout 单 JSON | §3 建议 |
| 产物通道 | artifacts/ 目录 + 服务端收集 | stdout 内嵌 base64 + scrub 兜底 | §3/§4 建议 |
| 错误分类 | status + resource_limit_type + …，124/137 语义 | exitCode + timedOut + stderr 文本 | P2-3 |
| 超时 | 三层防线 | 两层 ✅ | 基本够 |
| 输入 | arguments JSON → args.json 文件 | stdin multi-transport 信封（http_ref 有 SSRF 风险） | P0-2；RAGFlow 无 URL 输入源佐证方案 A |
| Cookie | 不透传任何用户凭证 | 透传（R3） | P0-3 |
| 语言 | python + nodejs（语言即模板/镜像） | python | 暂不需要 |
| 产物存储 | 沙箱内即抛（content_b64 回传即丢） | COS 落库 + artifactId + displayFiles | **我们更好，保留** |
| 测试 | 四层全覆盖 | 零（R6） | P1-4 + §7 配置回归 |

---

## 9. 落地建议（与现有计划的映射）

按"改动小、收益大"排序，前四条都是你们加固方案已有方向，RAGFlow 提供了具体实现参数；后三条是本文新增：

1. **沙箱 token 鉴权（对应 R7，新增到第一周）**：server.py 校验 `Authorization: Bearer`，未配置 token 默认 503 + 启动日志打鉴权姿态；Go 侧 client 带头。约 30 行。
2. **P0-1 网络隔离**：RAGFlow 参数佐证——compose 用 `internal: true` 等价于它的 `--network none` 默认 + 回环绑定；需要外网数据的场景一律走后端预拉取（等价于 RAGFlow 的"依赖烘进镜像"哲学：出口收敛到可信组件）。
3. **P1-3 并发治理**：抄 RAGFlow 的组合拳——`threading.Semaphore(MAX_CONCURRENT)`（等价它的每语言信号量）+ 耗尽时立即返回 `{"exitCode": -10, "stderr": "sandbox busy"}`（等价它的 pool busy 快速失败）+ Go 侧对该错误码打点不重试。
4. **P2-3 denyReason**：直接采用 RAGFlow 的枚举全集（ResultStatus 六值 + ResourceLimitType 三值 + 退出码 124/137 映射），比原计划的四分类更完整，且 `analyze_error_result` 的 stderr 模式归类逻辑（Permission denied→file_access 等）可平移。
5. **【新增】产物通道文件化**：server.py 建 `artifacts/` 目录并收集（白名单 + 上限 + 文件名消毒照抄 §4），Go 侧解 `artifacts` 字段替代 stdout 解析；`scrubPythonExecBase64` 转为纯打点。这是消灭一整类线上问题的根治方案。
6. **【新增】main() 契约 + marker 行**：与 5 配套，stdout 回归纯文本，结构化返回走 marker。两份解析器（Go/Python）合计不到 100 行，有现成实现可抄。
7. **【新增】配置回归测试**：在 P1-4 的测试集里加"部署参数断言"（网络隔离存在、token 已配置、read_only 存在），照 `test_run_endpoint_security.py` 的写法。

## 10. 不建议照搬的部分

1. **privileged + 挂 docker.sock**：executor_manager 要管理容器所以必须特权，这是它"独立节点 + 回环绑定 + token"换来的。我们是单容器 fork 模型，引入 docker.sock 等于把最大攻击面搬进家，没有收益。
2. **容器池跨请求复用**：RAGFlow 是单租户自部署（配置全局、无 per-user 概念），池换延迟是合理取舍；我们多租户，按加固方案 P2-4 的路线走"网络隔离 → gVisor → （必要时）每请求容器"即可，池化排最后。
3. **AST 黑名单规则本身**：RAGFlow 用黑名单（DANGEROUS_IMPORTS），拦不住花式绕过（和我们 R1 同病），且 `visit_BinOp` 把任意"常量+常量"拼接都标为可疑，误报明显；我们的 import 白名单模型更严。两边结论一致：**静态扫描只是降噪层，别指望它是边界**。RAGFlow 的 `sys` 在黑名单里、我们的 `sys` 在白名单里——这是 R1 绕过链的第一环，无论是否做网络隔离都该收紧（P1-2 第 1 条）。
4. **FastAPI/slowapi 技术栈**：框架无所谓，`http.server` 完全装得下同样的协议与策略；换框架是把问题搬家不是解决问题。
5. **`memory_used_kb` 字段**：schema 里有、代码从未填充，别照抄这个"看起来有内存计量"的假象。

---

## 附：源码索引（ragflow-deploy）

| 主题 | 文件 |
|------|------|
| 容器池/容器参数 | `agent/sandbox/executor_manager/core/container.py` |
| 执行主流程/产物收集 | `agent/sandbox/executor_manager/services/execution.py` |
| AST/JS 扫描 | `agent/sandbox/executor_manager/services/security.py` |
| fail-closed 鉴权 | `agent/sandbox/executor_manager/services/auth.py` |
| 限流 | `agent/sandbox/executor_manager/services/limiter.py`、`services/preauth.py` |
| 路由组装（限流→鉴权→执行） | `agent/sandbox/executor_manager/api/routes.py` |
| 结果 schema/枚举 | `agent/sandbox/executor_manager/models/schemas.py`、`models/enums.py` |
| marker 协议（Python） | `agent/sandbox/result_protocol.py` |
| Provider 抽象（Python） | `agent/sandbox/providers/base.py`、`manager.py`、`self_managed.py` |
| 部署 | `agent/sandbox/docker-compose.yml`、`sandbox_base_image/{python,nodejs}/Dockerfile` |
| Go Provider 接口/管理器 | `internal/agent/sandbox/provider.go`、`manager.go` |
| Go self_managed 客户端 | `internal/agent/sandbox/self_managed.go` |
| Go marker 协议/产物白名单 | `internal/agent/sandbox/result_protocol.go`、`artifacts.go` |
| Go HTTP 重试 | `internal/agent/sandbox/http.go` |
| Go 工具注入点 | `internal/agent/tool/code_exec_client.go`、`code_exec.go` |
| 安全测试 | `agent/sandbox/tests/`、`agent/sandbox/executor_manager/tests/` |
| 设计规范（1872 行，含管理面板/迁移/成本） | `agent/sandbox/sandbox_spec.md` |
