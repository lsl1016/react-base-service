# Codex 沙箱实现学习笔记

> 研究对象:`/Users/admin/Desktop/xm/codex`(codex-rs,Rust 实现)
> 目的:理解 Codex 沙箱的架构与安全设计,为 react-base-service 的 python_exec 沙箱加固提供参考。
> 配套文档:[python_exec沙箱加固方案.md](./python_exec沙箱加固方案.md)

---

## 一、全景:crate 地图与数据流

Codex 的沙箱不是单一组件,而是"策略层 + 平台适配层 + 辅助二进制"的组合:

```
config.toml (sandbox_mode / [sandbox_workspace_write] / [permissions.*])
   │  config/src/config_toml.rs::derive_permission_profile
   ▼
PermissionProfile (protocol/src/models.rs)          ← 运行时权限模型(现行)
SandboxMode (protocol/src/config_types.rs)          ← 配置层枚举(read-only / workspace-write / danger-full-access)
SandboxPolicy (protocol/src/protocol.rs)            ← legacy 运行时模型,仍并存
   │
   ▼
core: ToolOrchestrator::run (core/src/tools/orchestrator.rs)
   ├─ 审批判定 (core/src/tools/sandboxing.rs, AskForApproval)
   ├─ SandboxManager::new() + select_initial (sandboxing/src/manager.rs)
   └─ SandboxManager::transform()   ← 把用户命令 argv 变换成"沙箱包装后的 argv"
        │
        ├─ macOS:  /usr/bin/sandbox-exec -p <生成的sbpl> -- cmd
        ├─ Linux:  codex-linux-sandbox --permission-profile <JSON> -- cmd
        │            └→ 外层 bwrap(mount/pid/net namespace) → 内层 seccomp
        └─ Windows: codex.exe --run-as-windows-sandbox / --__codex-windows-mxc
   ▼
core/src/exec.rs → core/src/spawn.rs  真正 spawn 进程
```

### 各 crate 职责

| crate | 职责 |
|---|---|
| `sandboxing/`(codex-sandboxing) | 平台无关的"策略 → argv 变换"层,核心是 `SandboxManager` |
| `linux-sandbox/` | Linux helper 二进制(bwrap 两阶段 + seccomp + 遗留 Landlock) |
| `bwrap/` | 随包分发的 bubblewrap 源码(系统无 bwrap 时编译捆绑) |
| `mxc-sandbox/` | **Windows** MXC 后端(注意:不是 macOS!mxc = Microsoft 容器化隔离) |
| `windows-sandbox-rs/` | Windows legacy 后端(RestrictedToken + AppContainer 能力 SID) |
| `windows-sandbox-service/` | 管理员安装的提权服务(NamedPipe RPC,负责 provisioning) |
| `core/src/sandboxing/mod.rs` | core 侧执行适配器(`ExecRequest`、环境变量注入) |
| `core/src/sandbox_tags.rs` | 纯诊断标签(明确注释"不得用于鉴权") |
| `utils/sandbox-summary` | 把策略转成人类可读摘要(UI/遥测用) |
| `cli/src/debug_sandbox` | `codex sandbox seatbelt|landlock|windows <cmd>` 调试工具 |

### 三个重要澄清(与网上旧版资料的差异)

1. **没有 `Sandbox` trait**。平台差异收敛在 `SandboxManager::transform` 的 `match SandboxType` 分支 + 各 helper 二进制自己的 CLI 契约上。
2. **没有沙箱进程池/复用池,也没有旧版的 `SeatbeltPolicy` 缓存 + reset**。每次命令执行都全新生成 sbpl 字符串 / bwrap argv。复用只存在于:bwrap 探测结果 `OnceLock` 缓存、shell snapshot 按策略指纹缓存、审批结果缓存。
3. **Linux 主文件系统沙箱是 bubblewrap 而非 Landlock**。Landlock 只是 `--use-legacy-landlock` 遗留选项,且只支持全盘可写策略。

---

## 二、策略模型:三层结构

### 1. 配置层 `SandboxMode`(`protocol/src/config_types.rs:104`)

```
read-only(默认)/ workspace-write / danger-full-access
```

`config_toml.rs::derive_permission_profile` 决定默认值:目录受信任时默认 workspace-write;Windows 沙箱被禁用时 workspace-write 自动降级 read-only(安全降级,不会反向升级);受 `allowed_sandbox_modes` 约束时回退 read-only。

### 2. 运行时层 `PermissionProfile`(`protocol/src/models.rs:422`)

```rust
Managed { file_system, network } | Disabled | External { network }
```

- 序列化为 snake_case JSON,直接通过 `--permission-profile` 参数传给 Linux helper——**策略即数据**,跨进程边界传递。
- `FileSystemSandboxPolicy { kind: Restricted|Unrestricted, glob_scan_max_depth, entries }`,每条 entry 是 `{ path: Path|GlobPattern|Special, access: Read|Write|Deny }`。
- 可写根带保护性 carveout:`WritableRoot { root, read_only_subpaths, protected_metadata_names }`——`.git`/`.codex` 等可被利用提权的路径即使位于可写根内也保持只读。

### 3. 后端层 `SandboxType`(`protocol/src/sandbox.rs:10`)

```
None | MacosSeatbelt | LinuxSeccomp | WindowsRestrictedToken | WindowsMxc
```

由 `select_initial` 依据平台 + 策略要求 + 后端可用性决定。含 deny-read 的策略会强制要求平台沙箱,且**禁止脱离沙箱重试**(否则丢掉唯一的 deny-read 执行机制,见 `core/src/tools/sandboxing.rs:276`)。

---

## 三、一次命令执行的完整流水线

`ToolOrchestrator::run`(core/src/tools/orchestrator.rs:122)五步:

1. **审批判定**:`AskForApproval::{Never, OnRequest, Granular, UnlessTrusted}`。审批结果按 JSON key 缓存(`ApprovalStore`),同 key 会话内不再重复询问。
2. **沙箱选择**:决定首试是否绕过沙箱(execpolicy 允许时)、选择 `SandboxType`。
3. **首次尝试**:`transform()` 把命令包装成沙箱 argv,注入环境变量后 spawn。
4. **失败判定**:结构化错误 `SandboxErr::Denied`;文本启发式 `is_likely_sandbox_denied`(退出码非 2/126/127、Linux 128+SIGSYS、输出含 "operation not permitted / permission denied / read-only file system / seccomp / landlock" 等关键词)识别"这是被沙箱拦了,不是代码本身报错"。
5. **提权重试**:弹审批,用户同意后在 `SandboxType::None` 下重跑。**关键守卫**:策略含 deny-read 时禁止无沙箱重试;网络被代理拦截的拒绝走单独的网络审批通道。

这个"先沙箱内尝试 → 识别拒绝 → 审批后提权重试"的循环,是 Codex 交互模型的核心:**默认安全,失败可升级,升级必须有人批准**。

---

## 四、平台实现深潜

### 4.1 macOS:Seatbelt

- **固定二进制路径**:`/usr/bin/sandbox-exec`(seatbelt.rs:58-62,注释明说"只信 /usr/bin,防 PATH 注入;如果它被篡改,攻击者已经有 root 了")。
- **profile 模板**(编译期内嵌的 `.sbpl` 文件):
  - `seatbelt_base_policy.sbpl`:Chrome 沙箱风格,**`(deny default)` 起步**——默认全拒,再逐条放行 `process-exec/process-fork`、`signal (target same-sandbox)`、`/dev/null` 写、白名单 sysctl(为 Node/Python/Java 的 CPU 探测)、posix-sem(PyTorch)、`__KMP_REGISTERED_LIB_*` shm(OpenMP)……每条放行都有明确理由。
  - `seatbelt_network_policy.sbpl`:网络开启时附加 mach-lookup 与 `AF_SYSTEM socket-protocol 2`。
  - 动态段:读策略 + 写策略 + glob deny + 受保护祖先 rename deny + `fcntl 80/110` deny(堵"通过只读 fd 改文件"的旁路)+ 拒绝所有 XPC mach-lookup 前缀。
- **路径注入防御**:所有用户可控路径通过 `-DKEY=value` 参数 + profile 内 `(param "KEY")` 引用传入,**绝不内联拼接进 sbpl**——避免转义攻击。
- **策略自保护**:可写根锚点目录加 `(deny file-write-unlink …)`——防止沙箱内进程删除目录本身,破坏下次策略构建;可写根含 symlink 组件直接报错,除非显式 opt-in。

### 4.2 Linux:bwrap(默认)+ seccomp 两阶段

入口二进制 `codex-linux-sandbox` 通过 **arg0 trick** 由主程序分发(检测 argv0 别名),流程:

1. **外层**:构建 bwrap argv——user/pid/ipc namespace、`--unshare-pid --as-pid-1`、文件系统视图(`--ro-bind / /` 或受限读时 `--tmpfs /` + 逐路径 ro-bind、mask 不可读祖先、`--bind` 可写根、symlink/缺失保护路径挂 `/dev/null`),`execv` bwrap。
2. **内层**:bwrap 内自重入本二进制并带 `--apply-seccomp-then-exec`,先 `capget` 校验无残留 capabilities,再设置 `PR_SET_NO_NEW_PRIVS` + seccomp 过滤器,`fork` 出用户命令并 `waitpid(-1)` 循环收割全部孙进程。

**部署细节**:
- 优先用系统 bwrap(必须在 PATH 上且不在 cwd 内,防劫持);否则用随包分发的捆绑版,**执行前做 SHA-256 校验**,失败退出码 8。
- bwrap 能力探测结果(`OnceLock`)每进程只做一次。

**网络三模式**:`FullAccess`(不 unshare-net)/ `Isolated`(`--unshare-net`)/ `ProxyOnly`(unshare-net + helper 内 TCP→UDS→TCP 桥,只允许连代理)。

**seccomp 细节**(值得抄的清单):默认 Allow、命中返回 EPERM;`Restricted` 模式拒绝 `connect/bind/listen/sendto`,只允许 `socket(AF_UNIX)`;禁 `ptrace`、`process_vm_readv/writev`;**禁 `io_uring_*`**——因为 io_uring 可以不经 `socket()` 建立 AF_VSOCK 连接,绕过 socket 过滤。

### 4.3 Windows:三后端并存

1. **RestrictedToken(legacy)**:`CreateRestrictedToken` + capability SIDs + DACL + 独立沙箱账户 + 私有 desktop。
2. **Elevated 服务**:管理员安装的 Windows 服务,经 NamedPipe + 帧协议 RPC provisioning。管道 SDDL 只允许沙箱 SID、验证客户端进程身份、请求 ≤4096B、响应 ≤512B、空闲 5s 超时——**IPC 边界最小化**的典范。
3. **MXC**:微软原生容器化隔离,请求经 launcher-only 环境变量分块编码传入。

---

## 五、网络访问控制(管理式网络)

Codex 不是简单"开/关网络",而是 **managed proxy 模式**:

- 网络受限时,子进程的网络出口只有本地代理(loopback 端口),代理按白名单转发;
- 沙箱策略里只放行 `localhost:<代理端口>` + DNS `*:53`,白名单外的连接在 OS 层被拒;
- 环境变量注入代理地址;同时设 `CODEX_SANDBOX_NETWORK_DISABLED=1` 让进程内库(如 lmstudio client)自检网络错误并给出友好提示——**沙箱强制 + 应用感知**双层配合;
- **fail-closed**:有代理配置但解析不出端口 → 生成空网络策略(全拒),宁可误杀不可放过(seatbelt.rs:360-364)。

## 六、文件系统控制要点

- workspace-write 的可写根 = cwd + 显式列表 + `$TMPDIR`;macOS 额外隐式 scratch `/private/tmp`、`/private/var/tmp`(但显式限制仍约束它们);
- macOS 解析可写根时**不跟随攻击者可控的 symlink**(preserving 变体),Linux 继承根元数据——两个平台对 TOCTOU/符号链接的处理刻意不同;
- 不可读 glob 在 Linux 侧先用 ripgrep 展开(上限 8192 匹配)再逐个 mask;
- `.git`/`.codex` 元数据保护:即使目录可写,这些子路径也保持只读,防止篡改仓库存档/会话数据提权。

## 七、工程细节精选

**性能/并发**
- exec 输出读取带 `IO_DRAIN_TIMEOUT_MS = 2000`:防御孙进程持有 stdout 管道导致 agent 挂死;
- 输出聚合上限 + stdout/stderr 各 1/3-2/3 配额,防单边撑爆上下文;
- `kill_on_drop` + 超时杀进程组;Linux `PR_SET_PTHREAD_DEATHSIG`(父死信号)。

**fail-closed 设计(贯穿全局)**
- 代理配置异常 → 空网络策略;
- 畸形 deny-read glob → `FailClosed` 匹配器;
- seccomp 阶段断言无残留 capabilities,有则拒绝执行;
- Landlock `RulesetStatus::NotEnforced` 视为错误而非静默降级。

**进程加固(沙箱之外的配套)**
- Linux `PR_SET_DUMPABLE=0` + 清 `LD_*`;macOS `PT_DENY_ATTACH` + 清 `DYLD_*`;`RLIMIT_CORE=0`;
- 注意:Codex **没有**设置 RLIMIT_NPROC/RLIMIT_AS 防 fork 炸弹——进程治理靠 pid namespace(沙箱内进程只见自己)+ 杀进程组。

**测试策略**
- 大量真实行为测试(seatbelt_tests.rs 116KB):真的在沙箱里跑命令断言被拒;
- 嵌套沙箱下测试自我检测 `sandbox_apply: Operation not permitted` 即跳过——因为"沙箱里不能再套沙箱";
- 平台门控 `#[cfg(target_os)]` + 可跨平台单测(策略翻译逻辑)+ 平台冒烟(enforcement)分层。

## 八、设计哲学提炼(可直接迁移的原则)

1. **默认全拒,逐条放行**(`(deny default)`):每条放行都有明确理由(注释引用 Chrome/为某框架适配)。白名单制而非黑名单制。
2. **纵深防御,静态检查只是辅助**:Codex 完全不做代码静态扫描——OS 级强制隔离才是边界,启发式只用于"识别被拒后的用户体验"。
3. **fail-closed**:任何配置/环境异常,宁可拒绝执行也不静默降级。
4. **策略即数据**:权限模型序列化为 JSON 跨进程传递,可审计、可 diff、可按环境差异化。
5. **最小化 IPC 与注入面**:路径经 `-D` 参数而非拼接;环境变量白名单;管道 SDDL + 进程身份验证 + 帧大小/超时上限。
6. **失败 → 识别 → 审批 → 提权**的闭环:沙箱拒绝是常态而非异常,关键在拒绝后给用户一条受控的升级通道。
7. **边界自保护**:沙箱不能破坏构建下一次沙箱所依赖的东西(锚点 unlink-deny、祖先 rename-deny、保护路径挂 /dev/null)。
8. **不给进程内逃逸留后门**:禁 io_uring(绕 socket 过滤)、禁 ptrace、禁 fcntl 80/110(绕只读 fd)、拒 XPC 前缀——每个"已知旁路"都有针对性封堵。
