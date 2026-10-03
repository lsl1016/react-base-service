"""python_exec 本地沙箱服务。

实现 react-base-service 约定的沙箱协议：
    POST /api/pythonexec/execute
    请求: {"logId": "...", "python": "<code>", "data": "<stdin json>"}
          （历史字段 "cookies" 已废弃，服务端显式丢弃，见 do_POST）
    响应: {"code": 0, "message": "ok", "data": {"exitCode": 0, "stdout": "...", "stderr": "...", "timedOut": false}}

安全模型（与基座 meta tool 对模型声明的规则一致）：
1. 请求校验：logId 按字符白名单严格校验后才允许进入环境变量；
2. AST 静态扫描：import 白名单 + 危险调用/属性黑名单，违反即拒绝执行；
3. 子进程隔离：独立会话、隔离模式 python（-I）、最小环境变量、/tmp 工作目录；
4. 资源限制：RLIMIT_CPU / RLIMIT_AS / RLIMIT_FSIZE / RLIMIT_NPROC；
5. 超时强制终止整个进程组（timedOut=true）。

仅监听容器内端口，由外部决定网络暴露范围。
"""

import ast
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

try:
    import resource  # Unix only：CPU/内存等 rlimit；Windows 下退化为无 rlimit
except ImportError:  # pragma: no cover - Windows
    resource = None

LISTEN_HOST = os.environ.get("SANDBOX_HOST", "0.0.0.0")
LISTEN_PORT = int(os.environ.get("SANDBOX_PORT", "8190"))
EXEC_TIMEOUT_SEC = int(os.environ.get("SANDBOX_TIMEOUT_SEC", "60"))
MEM_LIMIT_BYTES = int(os.environ.get("SANDBOX_MEM_LIMIT_MB", "1024")) * 1024 * 1024
STDOUT_LIMIT = int(os.environ.get("SANDBOX_STDOUT_LIMIT_MB", "32")) * 1024 * 1024
STDERR_LIMIT = 256 * 1024

# logId 字符白名单：字母/数字/连字符/下划线，长度 1-128
LOG_ID_PATTERN = re.compile(r"^[A-Za-z0-9_\-]{1,128}$")

# import 白名单（模块根名）；模型侧工具描述与此保持一致
ALLOWED_IMPORT_ROOTS = frozenset({
    "sys", "json", "pandas", "numpy", "math", "statistics",
    "datetime", "io", "base64", "matplotlib",
})

# 禁止调用的内建/名称
FORBIDDEN_CALLS = frozenset({
    "eval", "exec", "compile", "open", "input", "breakpoint",
    "globals", "locals", "vars", "getattr", "setattr", "delattr",
    "__import__", "memoryview",
})

# 允许的双下划线属性（其余 __xxx__ 一律禁止）
ALLOWED_DUNDER_ATTRS = frozenset({"__name__", "__file__", "__doc__", "__len__", "__iter__", "__next__"})


class CodeRejected(Exception):
    """静态安全扫描不通过。"""


def sanitize_log_id(raw) -> str:
    """校验并清洗 logId；不合法返回空串。"""
    if not isinstance(raw, str):
        return ""
    value = raw.strip()
    return value if LOG_ID_PATTERN.match(value) else ""


def _reject(message: str):
    raise CodeRejected(message)


def _check_dunder_attr(node: ast.Attribute) -> None:
    name = node.attr
    if name.startswith("__") and name.endswith("__") and name not in ALLOWED_DUNDER_ATTRS:
        _reject(f"禁止访问双下划线属性: {name}")


def static_scan(code: str) -> None:
    """AST 静态安全扫描；任何违反抛 CodeRejected。"""
    try:
        tree = ast.parse(code)
    except SyntaxError as exc:
        _reject(f"python 语法错误: {exc}")
        return

    allowed = sorted(ALLOWED_IMPORT_ROOTS)
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                root = alias.name.split(".")[0]
                if root not in ALLOWED_IMPORT_ROOTS:
                    _reject(f"禁止 import: {alias.name}（白名单: {allowed}）")
        elif isinstance(node, ast.ImportFrom):
            module = (node.module or "").split(".")[0]
            if node.level != 0 or module not in ALLOWED_IMPORT_ROOTS:
                _reject(f"禁止 from import: {'.' * node.level}{node.module or ''}（白名单: {allowed}）")
        elif isinstance(node, ast.Call):
            func = node.func
            if isinstance(func, ast.Name) and func.id in FORBIDDEN_CALLS:
                _reject(f"禁止调用: {func.id}()")
            if isinstance(func, ast.Attribute):
                _check_dunder_attr(func)
                if func.attr in FORBIDDEN_CALLS:
                    _reject(f"禁止调用: {func.attr}()")
        elif isinstance(node, ast.Attribute):
            _check_dunder_attr(node)
        elif isinstance(node, ast.Name):
            if node.id in {"__builtins__", "__import__"}:
                _reject(f"禁止访问: {node.id}")


def _apply_limits() -> None:
    """子进程 preexec：CPU / 内存 / 写文件大小 / 进程数限制（仅 Unix）。"""
    if resource is None:
        return
    cpu = EXEC_TIMEOUT_SEC + 5
    fsize = 64 * 1024 * 1024
    limits = [
        (resource.RLIMIT_CPU, (cpu, cpu)),
        (resource.RLIMIT_AS, (MEM_LIMIT_BYTES, MEM_LIMIT_BYTES)),
        (resource.RLIMIT_FSIZE, (fsize, fsize)),
        # NPROC 按 UID 统计：常驻 server 线程 + 并发 python 子进程 + 线程库共享该配额，
        # 64 会被 OpenBLAS 等多线程库的正常 workload 撞上；128 保留防 fork 炸弹语义。
        (resource.RLIMIT_NPROC, (128, 128)),
        (resource.RLIMIT_CORE, (0, 0)),
    ]
    for which, value in limits:
        try:
            resource.setrlimit(which, value)
        except Exception:
            pass


def _kill_proc_tree(proc: subprocess.Popen) -> None:
    """跨平台终止：优先杀整个进程组（Unix），否则直接 kill。"""
    if hasattr(os, "killpg"):
        try:
            os.killpg(proc.pid, signal.SIGKILL)
            return
        except Exception:
            pass
    proc.kill()


def run_isolated(python_code: str, data: str, log_id: str) -> dict:
    """在受限子进程中执行代码，返回协议 data 结构。

    工作目录是一次性目录：无论正常返回、启动失败还是超时，退出前必定整目录清理。
    /tmp 是 128m tmpfs，不清理会随调用次数累积直到写满，导致沙箱整体不可用。
    """
    workdir = tempfile.mkdtemp(prefix="sandbox-")
    try:
        return _run_in_workdir(python_code, data, log_id, workdir)
    finally:
        # ignore_errors：超时路径下若有子进程仍在写，删不干净不应影响结果返回
        shutil.rmtree(workdir, ignore_errors=True)


def _run_in_workdir(python_code: str, data: str, log_id: str, workdir: str) -> dict:
    """run_isolated 的实际执行体；workdir 由调用方创建并负责清理。"""
    handle = tempfile.NamedTemporaryFile(
        mode="w", encoding="utf-8", suffix=".py",
        prefix="main-", dir=workdir, delete=False,
    )
    with handle:
        handle.write(python_code)
    script = handle.name

    env = {
        "PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"),
        "HOME": workdir,
        "TMPDIR": workdir,
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
        "MPLBACKEND": "Agg",
        "MPLCONFIGDIR": os.path.join(workdir, "mpl"),
        "PYTHONDONTWRITEBYTECODE": "1",
        "PYTHONHASHSEED": "0",
        # BLAS/OMP 单线程：容器 cpus 限额 1.5，多线程 BLAS 只会争抢；
        # 且 RLIMIT_NPROC 按 UID 统计（含常驻 server 自身线程），OpenBLAS 默认按核起 8 线程
        # 会撞 NPROC 上限导致 numpy import 阶段 pthread_create 失败。
        "OPENBLAS_NUM_THREADS": "1",
        "OMP_NUM_THREADS": "1",
        "MKL_NUM_THREADS": "1",
        "NUMEXPR_NUM_THREADS": "1",
        "X_SANDBOX_LOG_ID": log_id,
    }

    stdin_data = (data or "").encode("utf-8")
    popen_kwargs = {}
    if hasattr(os, "setsid"):
        popen_kwargs["start_new_session"] = True
    if resource is not None:
        popen_kwargs["preexec_fn"] = _apply_limits
    try:
        proc = subprocess.Popen(
            [sys.executable, "-I", script],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=workdir,
            env=env,
            **popen_kwargs,
        )
    except Exception as exc:  # noqa: BLE001 - 启动失败也按协议返回
        return {"exitCode": 1, "stdout": "", "stderr": f"sandbox spawn failed: {exc}", "timedOut": False}

    timed_out = False
    try:
        out, err = proc.communicate(input=stdin_data, timeout=EXEC_TIMEOUT_SEC)
    except subprocess.TimeoutExpired:
        timed_out = True
        _kill_proc_tree(proc)
        try:
            out, err = proc.communicate(timeout=10)
        except Exception:
            out, err = b"", b""
        timeout_note = f"\nsandbox: execution timed out after {EXEC_TIMEOUT_SEC}s"
        err = (err or b"") + timeout_note.encode("utf-8")

    exit_code = proc.returncode if not timed_out else -1
    stdout = (out or b"").decode("utf-8", errors="replace")[:STDOUT_LIMIT]
    stderr = (err or b"").decode("utf-8", errors="replace")[:STDERR_LIMIT]
    return {"exitCode": exit_code, "stdout": stdout, "stderr": stderr, "timedOut": timed_out}


class SandboxHandler(BaseHTTPRequestHandler):
    server_version = "python-exec-sandbox/1.0"

    def _send_json(self, status: int, code: int, message: str, data: dict) -> None:
        payload = {"code": code, "message": message, "data": data}
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_result(self, data: dict) -> None:
        self._send_json(200, 0, "ok", data)

    def _send_failure(self, stderr: str) -> None:
        self._send_result({"exitCode": 1, "stdout": "", "stderr": stderr, "timedOut": False})

    def do_POST(self) -> None:  # noqa: N802 - http.server 约定
        if self.path.split("?", 1)[0] != "/api/pythonexec/execute":
            self._send_json(404, 404, "not found", {})
            return

        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            self._send_json(400, 400, "invalid content length", {})
            return
        if length <= 0 or length > 8 * 1024 * 1024:
            self._send_json(400, 400, "invalid content length", {})
            return
        try:
            req = json.loads(self.rfile.read(length).decode("utf-8"))
            if not isinstance(req, dict):
                raise ValueError("body must be a json object")
        except Exception as exc:  # noqa: BLE001
            self._send_failure(f"bad request body: {exc}")
            return

        # P0-3：协议里的 cookies 字段已废弃，沙箱一律不使用——凭证不进入沙箱。
        # 显式丢弃而非默默忽略，避免未来实现者"顺手"把它接进执行环境或外发请求。
        req.pop("cookies", None)

        log_id = sanitize_log_id(req.get("logId"))
        python_code = req.get("python")
        data = req.get("data") or ""

        if not log_id:
            self._send_failure("logId is required (allowed: letters, digits, '-', '_', 1-128 chars)")
            return
        if not isinstance(python_code, str) or not python_code.strip():
            self._send_failure("python is required")
            return
        if not isinstance(data, str):
            data = json.dumps(data, ensure_ascii=False)

        try:
            static_scan(python_code)
        except CodeRejected as exc:
            self._send_failure(f"sandbox 静态安全扫描拒绝执行: {exc}")
            return

        self._send_result(run_isolated(python_code, data, log_id))

    def do_GET(self) -> None:  # noqa: N802
        if self.path.split("?", 1)[0] == "/health":
            self._send_json(200, 0, "ok", {"status": "up"})
            return
        self._send_json(404, 404, "not found", {})

    def log_message(self, fmt: str, *args) -> None:  # 精简访问日志
        sys.stderr.write(f"[sandbox] {self.address_string()} {fmt % args}\n")


def main() -> None:
    server = ThreadingHTTPServer((LISTEN_HOST, LISTEN_PORT), SandboxHandler)
    print(f"[sandbox] listening on {LISTEN_HOST}:{LISTEN_PORT}, "
          f"timeout={EXEC_TIMEOUT_SEC}s, mem={MEM_LIMIT_BYTES // (1024 * 1024)}MB", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    threading.current_thread().name = "sandbox-main"
    main()
