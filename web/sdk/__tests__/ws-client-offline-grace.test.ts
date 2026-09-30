/**
 * WsClient 断网宽限（offline grace）测试
 *
 * 验证 offline 事件后的缓拆行为：
 * - 宽限期内网络恢复：连接原样保留，上层无感知
 * - 宽限超时：走原有拆连接流程并安排重连
 * - 宽限期内 onclose 自然到达：只拆一次，宽限计时器被清理
 * - offlineGraceMs=0：恢复"立刻拆"旧行为
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { WsClient } from '../client/ws-client';
import type { WsClientCallbacks, WsClientOptions } from '../client/types';
import { track } from '../analytics';

vi.mock('../analytics', () => ({
  track: vi.fn(),
}));

class MockWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static instances: MockWebSocket[] = [];

  url: string;
  readyState = MockWebSocket.CONNECTING;
  closeCalls = 0;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: ((event: unknown) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  send(_data: string): void {}

  close(): void {
    this.closeCalls++;
    this.readyState = MockWebSocket.CLOSED;
  }

  /** 模拟服务端握手完成 */
  open(): void {
    this.readyState = MockWebSocket.OPEN;
    this.onopen?.();
  }

  /** 模拟对端关闭（触发 onclose 回调） */
  serverClose(event: Partial<CloseEvent> = {}): void {
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.(event as CloseEvent);
  }
}

function createClient(options: WsClientOptions = {}) {
  const connectionChanges: boolean[] = [];
  const callbacks: WsClientCallbacks = {
    onConnectionChange: (connected) => connectionChanges.push(connected),
  };
  const client = new WsClient('ws://test/react/ws', callbacks, options);
  client.connect();
  MockWebSocket.instances[0].open();
  return { client, connectionChanges, callbacks };
}

describe('WsClient offline grace', () => {
  let client: WsClient | null = null;

  beforeEach(() => {
    vi.useFakeTimers();
    MockWebSocket.instances = [];
    vi.stubGlobal('WebSocket', MockWebSocket);
    // 重连退避带 ±20% 抖动（多会话防同步重连风暴）：本组用例依赖确定性延迟，固定随机数
    vi.spyOn(Math, 'random').mockReturnValue(0);
  });

  afterEach(() => {
    client?.disconnect();
    client = null;
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it('宽限期内网络恢复：不拆连接、不通知断开', () => {
    const created = createClient();
    client = created.client;
    const socket = MockWebSocket.instances[0];

    window.dispatchEvent(new Event('offline'));
    vi.advanceTimersByTime(3_000);
    window.dispatchEvent(new Event('online'));
    vi.advanceTimersByTime(10_000);

    expect(socket.closeCalls).toBe(0);
    expect(MockWebSocket.instances).toHaveLength(1);
    expect(created.connectionChanges).toEqual([true]);
    expect(client.isConnected).toBe(true);
  });

  it('宽限超时：拆连接、通知断开并安排重连', () => {
    const created = createClient();
    client = created.client;
    const socket = MockWebSocket.instances[0];

    window.dispatchEvent(new Event('offline'));
    vi.advanceTimersByTime(4_999);
    expect(socket.closeCalls).toBe(0);

    vi.advanceTimersByTime(1);
    expect(socket.closeCalls).toBe(1);
    expect(created.connectionChanges).toEqual([true, false]);

    // 首次重连退避 1s
    vi.advanceTimersByTime(1_000);
    expect(MockWebSocket.instances).toHaveLength(2);
  });

  it('宽限期内 onclose 自然到达：只拆一次，宽限计时器被清理', () => {
    const created = createClient();
    client = created.client;
    const socket = MockWebSocket.instances[0];

    window.dispatchEvent(new Event('offline'));
    vi.advanceTimersByTime(1_000);
    socket.serverClose();
    expect(created.connectionChanges).toEqual([true, false]);

    // 1s 后重连建立新 socket；原宽限计时器（剩 3s）不得再拆新连接
    vi.advanceTimersByTime(1_000);
    expect(MockWebSocket.instances).toHaveLength(2);
    const next = MockWebSocket.instances[1];
    next.open();

    vi.advanceTimersByTime(10_000);
    expect(next.closeCalls).toBe(0);
    expect(created.connectionChanges).toEqual([true, false, true]);
  });

  it('自动重连时通知上层重连尝试次数', () => {
    const reconnectAttempts: number[] = [];
    const created = createClient();
    client = created.client;
    created.callbacks.onReconnectAttempt = (attempt) => {
      reconnectAttempts.push(attempt);
    };
    const socket = MockWebSocket.instances[0];

    socket.serverClose();
    vi.advanceTimersByTime(1_000);

    expect(reconnectAttempts).toEqual([1]);
  });

  it('offlineGraceMs=0：offline 立刻拆连接（旧行为）', () => {
    const created = createClient({ offlineGraceMs: 0 });
    client = created.client;
    const socket = MockWebSocket.instances[0];

    window.dispatchEvent(new Event('offline'));

    expect(socket.closeCalls).toBe(1);
    expect(created.connectionChanges).toEqual([true, false]);
  });

  it('关闭时输出可按 connectionId 关联的 JSON 诊断信息', () => {
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});
    const created = createClient();
    client = created.client;

    MockWebSocket.instances[0].serverClose({ code: 1006, reason: '', wasClean: false });

    const closeLog = logSpy.mock.calls
      .map(([message]) => String(message))
      .find((message) => message.startsWith('[agent-sdk][ws] connect:close '));
    expect(closeLog).toBeDefined();

    const details = JSON.parse(closeLog!.slice(closeLog!.indexOf('{'))) as Record<string, unknown>;
    expect(details).toMatchObject({
      connectionId: 1,
      trigger: 'close_event',
      code: 1006,
      reason: '',
      wasClean: false,
    });
    expect(details.connectionAttemptId).toEqual(expect.any(String));
    expect(new URL(MockWebSocket.instances[0].url).searchParams.get('connection_attempt_id')).toBe(
      details.connectionAttemptId,
    );
  });

  it('将控制台 JSON 原样通过 KKT_001 上报', () => {
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});
    const created = createClient();
    client = created.client;

    const firstLog = logSpy.mock.calls
      .map(([message]) => String(message))
      .find((message) => message.startsWith('[agent-sdk][ws] connect:start '));
    expect(firstLog).toBeDefined();
    const printedPayload = JSON.parse(firstLog!.slice(firstLog!.indexOf('{')));
    expect(track).toHaveBeenCalledWith('KKT_001', printedPayload);
  });

  it('每条控制台日志和上报都携带当前 runId 与运行状态', () => {
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});
    const runtimeContext = { runId: 'run_123', runStatus: 'running' as const };
    const callbacks: WsClientCallbacks = {
      getRuntimeContext: () => runtimeContext,
    };
    const created = new WsClient('ws://test/react/ws', callbacks);
    client = created;

    created.connect();

    const startLog = logSpy.mock.calls
      .map(([message]) => String(message))
      .find((message) => message.startsWith('[agent-sdk][ws] connect:start '));
    expect(startLog).toBeDefined();
    const printedPayload = JSON.parse(startLog!.slice(startLog!.indexOf('{')));
    expect(printedPayload).toMatchObject({
      runId: 'run_123',
      runStatus: 'running',
    });
    expect(track).toHaveBeenCalledWith('KKT_001', printedPayload);
  });

  it('CONNECTING 超时：关闭当前 socket 并安排自动重连', () => {
    const created = new WsClient('ws://test/react/ws', {}, { connectTimeout: 5_000 });
    client = created;
    created.connect();
    const socket = MockWebSocket.instances[0];

    vi.advanceTimersByTime(4_999);
    expect(socket.closeCalls).toBe(0);
    vi.advanceTimersByTime(1);
    expect(socket.closeCalls).toBe(1);

    vi.advanceTimersByTime(1_000);
    expect(MockWebSocket.instances).toHaveLength(2);
  });

  it('连接成功后取消 CONNECTING 超时计时器', () => {
    const created = new WsClient('ws://test/react/ws', {}, { connectTimeout: 5_000 });
    client = created;
    created.connect();
    const socket = MockWebSocket.instances[0];
    socket.open();

    vi.advanceTimersByTime(5_000);

    expect(socket.closeCalls).toBe(0);
    expect(MockWebSocket.instances).toHaveLength(1);
  });

  it('旧 socket 的 CONNECTING 超时不影响后续连接', () => {
    const created = new WsClient('ws://test/react/ws', {}, { connectTimeout: 5_000 });
    client = created;
    created.connect();
    const first = MockWebSocket.instances[0];

    vi.advanceTimersByTime(5_000);
    vi.advanceTimersByTime(1_000);
    const second = MockWebSocket.instances[1];
    second.open();

    vi.advanceTimersByTime(4_000);

    expect(first.closeCalls).toBe(1);
    expect(second.closeCalls).toBe(0);
    expect(MockWebSocket.instances).toHaveLength(2);
  });
  it('页面生命周期事件只记日志，不主动关闭连接', () => {
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});
    const created = createClient();
    client = created.client;
    const socket = MockWebSocket.instances[0];

    window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }));
    document.dispatchEvent(new Event('freeze'));

    expect(socket.closeCalls).toBe(0);
    expect(logSpy.mock.calls.map(([message]) => String(message))).toEqual(
      expect.arrayContaining([
        expect.stringContaining('[agent-sdk][ws] page:hide '),
        expect.stringContaining('[agent-sdk][ws] page:freeze '),
      ]),
    );
  });
});
