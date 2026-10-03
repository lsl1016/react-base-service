#!/usr/bin/env python3
"""R1 网络隔离回归探针（P0-1 的验收手段）。

背景：AST 静态扫描（sandbox/server.py 的 static_scan）可以被 `sys.modules["os"]` 绕过，
这是**已知且接受**的——P0-1 之后它只是"降噪第一层"，真正的边界是网络层。本探针就是
把"网络层确实拦住了"变成一条可重复执行、可进 CI 的断言，而不是靠人工相信 compose 配置。

断言逻辑（三层，缺一不可）：
  1. R1 PoC 必须能**通过静态扫描**——否则测到的是软防线，不是硬边界；
  2. PoC 逃逸后，沙箱内子进程必须**连不上**外部公网与业务内网（mysql/redis）；
  3. 判定按异常类型区分：DNS 解析失败/超时/无路由 = BLOCKED；
     ConnectionRefused 也算 REACHABLE（端口虽然关闭，但网络路径是通的，说明隔离没生效）。

用法：
  # 容器全栈形态（唯一隔离生效的形态；sandbox 未发布宿主机端口，故从容器内发起）
  docker compose exec -T sandbox python - < sandbox/tests/network_isolation_probe.py

  # 本机开发形态（叠加了 docker-compose.dev.yml）：预期**失败**并明确提示，
  # 因为 dev 覆盖层为了让 go run 直连，故意把沙箱接回了可出网的 backend
  SANDBOX_URL=http://127.0.0.1:18190 python3 sandbox/tests/network_isolation_probe.py

退出码：0 = 隔离生效；1 = 隔离不成立（或探针自身失败）。
"""

import base64
import json
import os
import sys
import urllib.error
import urllib.request

SANDBOX_URL = os.environ.get("SANDBOX_URL", "http://127.0.0.1:8190").rstrip("/")
EXEC_PATH = "/api/pythonexec/execute"
PROBE_TIMEOUT = float(os.environ.get("PROBE_TIMEOUT", "90"))
LOG_ID = os.environ.get("PROBE_LOG_ID", "network-isolation-probe")

# 探针目标：外部公网 + 业务内网。mysql/redis 只挂 backend 网，沙箱应在 DNS 层就解析不到。
TARGETS = [
    ("1.1.1.1", 443, "外部公网"),
    ("mysql", 3306, "业务内网 MySQL"),
    ("redis", 6379, "业务内网 Redis"),
]

# 在沙箱内执行的载荷：先绕过静态扫描拿到 os，再用子进程 python 尝试真实建连。
INNER_TEMPLATE = """import socket
for host, port in {targets}:
    try:
        conn = socket.create_connection((host, port), 3)
        conn.close()
        print("OK-CONNECTED %s:%d" % (host, port))
    except Exception as exc:
        kind = type(exc).__name__
        if kind == "ConnectionRefusedError":
            print("OK-REFUSED %s:%d" % (host, port))
        else:
            print("BLOCKED %s:%d %s" % (host, port, kind))
"""

REJECT_MARKER = "静态安全扫描拒绝执行"
BYPASS_MARKER = "R1-BYPASS-OK"


def build_poc_code() -> str:
    """构造 R1 PoC：sys.modules['os'] 逃逸 + 子进程建连。

    载荷用 base64 打包后再拼进 `python -c '...'`：base64 字母表不含单引号，
    从而彻底规避引号/换行与 sh 的转义纠缠（早期用 repr 生成目标列表，
    其中的单引号会截断 sh 的单引号串，导致载荷语法错误而误报"无结果"）。
    """
    inner = INNER_TEMPLATE.format(targets=[[h, p] for h, p, _ in TARGETS])
    b64 = base64.b64encode(inner.encode("utf-8")).decode("ascii")
    launcher = 'import base64;exec(base64.b64decode("%s"))' % b64
    assert "'" not in launcher, "launcher 含单引号会破坏 sh 引号串"
    return "\n".join([
        "import sys",
        "launcher = " + repr(launcher),
        "cmd = sys.executable + \" -c '\" + launcher + \"'\"",
        "print('%s: 静态扫描已绕过，进入硬边界测试')" % BYPASS_MARKER,
        "print('inner-rc=%d' % sys.modules['os'].system(cmd))",
    ])


def call_sandbox(code: str) -> dict:
    payload = json.dumps({"logId": LOG_ID, "python": code, "data": ""}).encode("utf-8")
    req = urllib.request.Request(
        SANDBOX_URL + EXEC_PATH,
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=PROBE_TIMEOUT) as resp:
        body = json.loads(resp.read().decode("utf-8"))
    return body.get("data") or {}


def main() -> int:
    print("=" * 68)
    print(f"R1 网络隔离回归探针  target={SANDBOX_URL}")
    print("=" * 68)

    code = build_poc_code()
    try:
        data = call_sandbox(code)
    except urllib.error.URLError as exc:
        print(f"[探针失败] 无法连接沙箱 {SANDBOX_URL}: {exc}")
        print("提示：容器全栈形态请用 docker compose exec -T sandbox python - < 本文件")
        return 1
    except Exception as exc:  # noqa: BLE001 - 探针自身异常一律按失败处理
        print(f"[探针失败] 调用沙箱异常: {type(exc).__name__}: {exc}")
        return 1

    stdout = data.get("stdout") or ""
    stderr = data.get("stderr") or ""
    print(f"sandbox exitCode={data.get('exitCode')} timedOut={data.get('timedOut')}")

    failures = []

    # 断言 1：静态扫描必须被绕过（否则测的是软防线，结论无意义）
    if REJECT_MARKER in stderr:
        print(f"[✗ 断言1] 载荷被静态扫描拒绝，未进入硬边界测试: {stderr.strip()[:200]}")
        return 1
    if BYPASS_MARKER not in stdout:
        failures.append("断言1：载荷未按预期绕过静态扫描（stdout 无 R1-BYPASS-OK 标记）")
        print("[✗ 断言1] 未观察到绕过标记，PoC 可能已失效")
    else:
        print("[✓ 断言1] R1 绕过成立：静态扫描已放行，进入硬边界测试")

    # 断言 2：每个目标都不可达
    print("-" * 68)
    for host, port, label in TARGETS:
        key = f"{host}:{port}"
        reachable = f"OK-CONNECTED {key}" in stdout or f"OK-REFUSED {key}" in stdout
        blocked = f"BLOCKED {key}" in stdout
        if reachable:
            detail = "OK-REFUSED" if f"OK-REFUSED {key}" in stdout else "OK-CONNECTED"
            print(f"[✗ 断言2] {label:14s} {key:16s} 可达（{detail}）→ 隔离未生效")
            failures.append(f"{label}({key}) 仍可达")
        elif blocked:
            reason = next((ln.split()[-1] for ln in stdout.splitlines()
                           if ln.startswith(f"BLOCKED {key}")), "?")
            print(f"[✓ 断言2] {label:14s} {key:16s} 不可达（{reason}）")
        else:
            print(f"[✗ 断言2] {label:14s} {key:16s} 无结果（PoC 可能未执行到）")
            failures.append(f"{label}({key}) 未产生探测结果")

    print("-" * 68)
    if stdout.strip():
        print("沙箱内 stdout：")
        for line in stdout.strip().splitlines():
            print("  | " + line)
    if stderr.strip():
        print("沙箱内 stderr：")
        for line in stderr.strip().splitlines():
            print("  | " + line)

    if failures:
        print()
        print("结论：网络隔离未生效 —— " + "；".join(failures))
        print("若这是叠加了 docker-compose.dev.yml 的本机开发形态，属**预期**：")
        print("该覆盖层为让 go run 直连而故意恢复沙箱出网。发布形态（不带覆盖层）应通过。")
        return 1

    print()
    print("结论：网络隔离生效 ✅ —— R1 绕过静态扫描后依然无法出网、无法触达 mysql/redis")
    return 0


if __name__ == "__main__":
    sys.exit(main())
