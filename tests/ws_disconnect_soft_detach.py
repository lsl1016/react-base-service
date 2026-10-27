#!/usr/bin/env python3
"""D-2 断连软脱管 E2E 验证（TEST-REPORT-2026-10-27 D-2）。

流程：连 WS 发起一个带 python_exec（sleep 20s）的 run → 观察到工具开始执行后
直接断开 TCP（不发关闭帧，模拟网络掉线/页面崩溃）→ 轮询 DB 断言 run 终态为
finished（而非 error: client disconnected）且最终回答已落库。

用法：python3 tests/ws_disconnect_soft_detach.py [base_url] [app_key]
依赖：服务运行中；本机 docker exec react-base-mysql 可查库。
"""
import base64
import hashlib
import json
import os
import socket
import struct
import subprocess
import sys
import time
import urllib.request

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8180"
APP_KEY = sys.argv[2] if len(sys.argv) > 2 else "demo-app"
MODE = sys.argv[3] if len(sys.argv) > 3 else "detach"  # detach=D-2 断连 | inputs=D-1 输入注入
WS_PATH = "/react-base-service/react/ws"

if MODE == "inputs":
    PROMPT = ("请严格按以下要求执行，不要追问：用 python_exec 的 inputs 输入源完成词频统计——"
              "alias 取名 words、type 用 text、value 填 \"苹果 香蕉 苹果 橙子 苹果\"；"
              "脚本从 stdin 读 inputs 后统计各词次数，输出单个 JSON 对象，最后回复「词频完成」。")
    FINAL_MARK = "词频完成"
else:
    PROMPT = ("请严格按以下要求执行，不要追问：用 python_exec 执行代码 "
              "\"import time,json; time.sleep(20); print(json.dumps({'slept': 20}))\"，"
              "拿到结果后回复「睡眠完成」四个字即可。")
    FINAL_MARK = "睡眠完成"


def ws_connect(host, port, path):
    sock = socket.create_connection((host, port), timeout=10)
    key = base64.b64encode(os.urandom(16)).decode()
    req = (f"GET {path} HTTP/1.1\r\nHost: {host}:{port}\r\n"
           "Upgrade: websocket\r\nConnection: Upgrade\r\n"
           f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n"
           "X-User-Name: tester\r\n\r\n")
    sock.sendall(req.encode())
    resp = b""
    while b"\r\n\r\n" not in resp:
        chunk = sock.recv(4096)
        if not chunk:
            raise RuntimeError("handshake failed: " + resp.decode(errors="replace"))
        resp += chunk
    status = resp.split(b"\r\n", 1)[0].decode()
    if "101" not in status:
        raise RuntimeError("handshake rejected: " + status)
    return sock


def ws_send_text(sock, text):
    payload = text.encode()
    mask = os.urandom(4)
    header = bytearray([0x81])
    n = len(payload)
    if n < 126:
        header.append(0x80 | n)
    elif n < 65536:
        header.append(0x80 | 126)
        header += struct.pack(">H", n)
    else:
        header.append(0x80 | 127)
        header += struct.pack(">Q", n)
    header += mask
    masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
    sock.sendall(bytes(header) + masked)


def ws_recv_text(sock, timeout=30):
    sock.settimeout(timeout)

    def read_exact(n):
        buf = b""
        while len(buf) < n:
            chunk = sock.recv(n - len(buf))
            if not chunk:
                raise ConnectionError("connection closed")
            buf += chunk
        return buf

    while True:
        b1, b2 = read_exact(2)
        opcode = b1 & 0x0F
        length = b2 & 0x7F
        if length == 126:
            length = struct.unpack(">H", read_exact(2))[0]
        elif length == 127:
            length = struct.unpack(">Q", read_exact(8))[0]
        payload = read_exact(length) if length else b""
        if opcode == 0x9:  # 服务端协议层 ping：回 pong 刷新读超时，保持连接可控断开
            mask = os.urandom(4)
            header = bytearray([0x8A, 0x80 | len(payload)]) + mask
            sock.sendall(bytes(header) + bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))
            continue
        if opcode == 0x1:
            return json.loads(payload.decode())
        # pong/close 等其余控制帧：跳过


def mysql_query(sql):
    out = subprocess.run(
        ["docker", "exec", "react-base-mysql", "sh", "-c",
         f'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -D llm -N -e "{sql}"'],
        capture_output=True, text=True)
    return out.stdout.strip()


def wait_run_terminal(session_id, timeout_sec=180):
    deadline = time.time() + timeout_sec
    while time.time() < deadline:
        row = mysql_query(
            f"select state, coalesce(error_message,'') from tblLlmReactRun "
            f"where session_id = '{session_id}' order by id desc limit 1")
        parts = (row or "").split("\t")
        if parts and parts[0] and parts[0] not in ("running",):
            while len(parts) < 2:
                parts.append("")
            return tuple(parts[:2])
        time.sleep(3)
    return None


def main():
    from urllib.parse import urlparse
    u = urlparse(BASE)
    host, port = u.hostname, u.port or 80

    print(f"[1] 连接 WS ws://{host}:{port}{WS_PATH}")
    sock = ws_connect(host, port, WS_PATH)

    print("[2] 发起 run（python_exec sleep 20s）")
    msg = {"type": "run", "payload": {
        "appKey": APP_KEY, "routeValues": [], "type": "chat",
        "userPrompt": PROMPT, "executionMode": "react"}}
    ws_send_text(sock, json.dumps(msg, ensure_ascii=False))

    session_id = run_id = None
    saw_tool_start = False
    py_results = []
    print("[3] 读事件直到 python_exec 开始执行…")
    while True:
        ev = ws_recv_text(sock, timeout=60)
        t = ev.get("type")
        if not run_id and ev.get("runId"):
            run_id = ev.get("runId")
            session_id = ev.get("sessionId")
            print(f"    runId={run_id} sessionId={session_id}")
        payload = ev.get("payload") or {}
        if t == "tool_use_end" and payload.get("content"):
            try:
                r = json.loads(payload["content"])
                if "exitCode" in r:
                    py_results.append(r)
            except (ValueError, TypeError):
                pass
        if t == "done":
            print("    run 正常完成（inputs 模式不断连）")
            break
        if t == "error":
            raise SystemExit(f"FAIL: 收到 error 终态: {json.dumps(payload, ensure_ascii=False)[:300]}")
        if t == "tool_use_start" and payload.get("toolName") == "python_exec" and MODE == "detach":
            saw_tool_start = True
            print(f"    python_exec 开始（toolUseId={payload.get('toolUseId')}），立刻硬断开 TCP")
            break
    if MODE == "inputs":
        # 断言：存在一次带 inputs 的成功执行（exitCode=0 且统计结果正确）。
        # 首调可能因模型代码选了白名单外库被沙箱拒绝（与注入无关），不算注入失败。
        ok = [r for r in py_results if r.get("exitCode") == 0 and "苹果" in r.get("stdout", "")]
        if not ok:
            raise SystemExit(f"FAIL: D-1 输入注入未成功: {py_results}")
        print(f"[4] PASS(D-1): python_exec stdin 注入生效，stdout={ok[0].get('stdout','')[:120].strip()}")
        return

    if not saw_tool_start or not run_id:
        raise SystemExit(f"未观察到 python_exec 开始（runId={run_id}）")

    # 硬断开：不发 close 帧，直接 RST/关闭，模拟掉线
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
    sock.close()
    print("[4] TCP 已硬断开，等待 run 脱管续跑至终态…")

    result = wait_run_terminal(session_id)
    if result is None:
        raise SystemExit("等待终态超时（180s）")
    state, err = result
    print(f"[5] run 终态: state={state} error={err!r}")
    if state != "finished":
        raise SystemExit(f"FAIL: 期望 finished，实际 {state}（{err}）")

    # 校验最终回答已落库（events 重建里应包含「睡眠完成」）
    req = urllib.request.Request(
        f"{BASE}/react-base-service/react/session/events",
        data=json.dumps({"sessionId": session_id, "appKey": APP_KEY}).encode(),
        headers={"Content-Type": "application/json", "X-User-Name": "tester"}, method="POST")
    body = json.loads(urllib.request.urlopen(req, timeout=10).read())
    texts = " ".join(str(e.get("payload", {}).get("content", "")) for e in body.get("data", {}).get("events", []))
    if FINAL_MARK not in texts:
        raise SystemExit("FAIL: 终态 finished 但最终回答未在持久化事件中找到")
    print("[6] PASS: 断连后 run 继续执行至 finished，最终回答已持久化")


if __name__ == "__main__":
    main()
