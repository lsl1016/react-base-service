import 'fake-indexeddb/auto';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createAgentClient, type AgentClient } from '../runtime/agent-client';

/**
 * 多会话架构（每会话独立 WS 连接 + 状态分片）测试：
 * - 切换会话 = 换视图指针，原会话连接与 run 不受影响（后台事件继续累积）
 * - 多会话可并行发 run（各连接各发各的）
 * - 运行中允许新建会话
 * - 空闲后台会话延迟断开，且绝不误杀运行中的会话
 * - getSessionStatuses 反映各会话运行状态
 */
let connectionSeq = 0;
const sentMessages: Array<Record<string, unknown>> = [];

class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  readonly id = ++connectionSeq;
  readyState: number = FakeWebSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor() {
    setTimeout(() => {
      if (this.readyState === FakeWebSocket.CONNECTING) {
        this.readyState = FakeWebSocket.OPEN;
        this.onopen?.();
      }
    }, 0);
  }

  send(data: string): void {
    sentMessages.push(JSON.parse(data) as Record<string, unknown>);
  }

  close(): void {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code: 1000, reason: '', wasClean: true } as unknown as CloseEvent);
  }

  /** 测试辅助：以该连接的身份注入一个服务端事件。 */
  emit(event: Record<string, unknown>): void {
    this.onmessage?.({ data: JSON.stringify(event) });
  }

  addEventListener(): void {}
  removeEventListener(): void {}
  dispatchEvent(): boolean { return false; }
}
vi.stubGlobal('WebSocket', FakeWebSocket);

function jsonOk(data: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => ({ errNo: 0, errMsg: 'succ', data }),
  };
}

function createMockFetch() {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const path = new URL(url, 'http://test').pathname;
    const body = init?.body && typeof init.body === 'string' ? JSON.parse(init.body) : {};

    if (path === '/react/session/list') {
      return jsonOk({ sessions: [], total: 0, page: 1, pageSize: 20 });
    }

    if (path === '/react/session/events') {
      return jsonOk({
        sessionId: body.sessionId,
        title: '会话',
        events: [
          {
            type: 'run',
            seq: 1,
            runId: `run_${body.sessionId}`,
            sessionId: body.sessionId,
            payload: { callerKey: 'report-editor', routeValues: [], type: 'chat', userPrompt: '历史问题' },
            createdAt: '2026-09-30 10:00:00',
          },
        ],
      });
    }

    if (path === '/react/queue/list') {
      return jsonOk({ items: [], queueEnabled: true, autoDrain: true });
    }

    if (path === '/react/session/delete') {
      return jsonOk({
        deleted: true, runs: 1, messages: 2, toolResults: 0, pendingInputs: 0,
        feedbacks: 0, artifacts: 0, asyncTasks: 0, planRows: 0,
      });
    }

    if (path === '/react/session/fork') {
      return jsonOk({
        sessionId: `session_forked_${String(body.throughRunId ?? 'msg')}`,
        forkedFrom: body.sessionId,
        cutRunId: String(body.throughRunId ?? ''),
        inclusive: body.inclusive !== false,
        runs: 2,
        messages: 5,
      });
    }

    if (path === '/react/session/rename') {
      return jsonOk({ renamed: true, title: String(body.title ?? '').trim() });
    }

    return { ok: false, status: 404, statusText: 'Not Found', json: async () => ({}) };
  });
}

async function waitForConnected(client: AgentClient): Promise<void> {
  for (let index = 0; index < 50; index += 1) {
    if (client.isConnected) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error('client did not connect');
}

/** 取指定会话 runtime 的底层 FakeWebSocket（经 WsClient 内部 ws 字段；测试触达内部结构）。 */
function runtimeWs(client: AgentClient, sessionId: string): FakeWebSocket {
  const runtime = (client as any).runtimes.get(sessionId);
  if (!runtime) throw new Error(`runtime not found: ${sessionId}`);
  return runtime.wsClient.ws as FakeWebSocket;
}

describe('AgentClient 多会话（每会话独立连接 + 状态分片）', () => {
  let client: AgentClient | null = null;
  let originalFetch: typeof fetch | undefined;

  beforeEach(() => {
    sentMessages.length = 0;
    originalFetch = globalThis.fetch;
    globalThis.fetch = createMockFetch() as any;
  });

  afterEach(async () => {
    client?.disconnect();
    client = null;
    if (originalFetch) {
      globalThis.fetch = originalFetch;
    }
  });

  it('多会话切换：各会话独立连接，subscribe 收到切换后的状态', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);

    await client.switchSession('session_multi_a');
    await waitForConnected(client);
    const wsA = runtimeWs(client, 'session_multi_a');

    await client.switchSession('session_multi_b');
    await waitForConnected(client);
    const wsB = runtimeWs(client, 'session_multi_b');

    expect(wsA).not.toBe(wsB);
    expect(client.getState().sessionId).toBe('session_multi_b');
  });

  it('run 活跃时切换会话：原会话连接保持、后台事件继续累积，切回即见', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);
    await client.switchSession('session_multi_a');
    await waitForConnected(client);

    client.run('任务A');
    expect(client.getState().status).toBe('running');
    const wsA = runtimeWs(client, 'session_multi_a');

    // 切到 B：A 的连接不应被断开（run 还在跑）
    await client.switchSession('session_multi_b');
    await waitForConnected(client);
    expect(wsA.readyState).toBe(FakeWebSocket.OPEN);
    expect(client.getState().sessionId).toBe('session_multi_b');

    // A 的 run 事件在后台继续到达（通过 A 自己的连接）
    wsA.emit({ type: 'content_start', seq: 1, runId: 'run_a', sessionId: 'session_multi_a', stepIndex: 0 });
    wsA.emit({
      type: 'content_end', seq: 2, runId: 'run_a', sessionId: 'session_multi_a', stepIndex: 0,
      payload: { content: 'A 的后台输出' },
    });
    wsA.emit({ type: 'done', seq: 3, runId: 'run_a', sessionId: 'session_multi_a', payload: {} });

    // 切回 A：应看到后台累积的完整进展
    await client.switchSession('session_multi_a');
    const state = client.getState();
    expect(state.sessionId).toBe('session_multi_a');
    const content = state.steps.map((step) => step.content ?? '').join('');
    expect(content).toContain('A 的后台输出');
  });

  it('run 活跃时允许新建会话：返回 true 且原会话状态不受影响', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);
    await client.switchSession('session_multi_a');
    await waitForConnected(client);

    client.run('任务A');
    expect(client.getState().status).toBe('running');

    const created = client.newSession();
    expect(created).toBe(true);
    expect(client.getState().sessionId).toBeNull();
    expect(client.getState().steps).toHaveLength(0);

    // 原会话 runtime 仍在运行
    const statuses = client.getSessionStatuses();
    expect(statuses.find((item) => item.sessionId === 'session_multi_a')?.status).toBe('running');
  });

  it('多会话并行 run：各连接发出各自的 run 消息，互不干扰', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);

    await client.switchSession('session_multi_a');
    await waitForConnected(client);
    client.run('任务A');

    await client.switchSession('session_multi_b');
    await waitForConnected(client);
    client.run('任务B');

    const runSends = sentMessages.filter((msg) => msg.type === 'run');
    expect(runSends).toHaveLength(2);
    expect((runSends[0].payload as Record<string, unknown>).userPrompt).toBe('任务A');
    expect((runSends[1].payload as Record<string, unknown>).userPrompt).toBe('任务B');

    // 两个会话都处于 running（各自状态分片）
    const statuses = client.getSessionStatuses();
    expect(statuses.find((item) => item.sessionId === 'session_multi_a')?.status).toBe('running');
    expect(statuses.find((item) => item.sessionId === 'session_multi_b')?.status).toBe('running');
  });

  it('空闲后台会话延迟断开；运行中的后台会话连接保持', async () => {
    // 只 fake 定时器：fake-indexeddb 依赖 microtask/setImmediate，全量 fake 会卡死 await
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
    try {
      client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
      // 手动驱动 FakeWebSocket 的异步 open（fake timers 下 setTimeout 不再自动跑；
      // 不用 runAllTimers：WS 心跳是无限 ticker，会触发 abort）
      client.connect();
      await vi.advanceTimersByTimeAsync(5);

      await client.switchSession('session_multi_a');
      await vi.advanceTimersByTimeAsync(5);
      client.run('任务A');
      const wsA = runtimeWs(client, 'session_multi_a');

      // B 先跑完一个 run 到终态（空闲），再切走
      await client.switchSession('session_multi_b');
      await vi.advanceTimersByTimeAsync(5);
      const wsB = runtimeWs(client, 'session_multi_b');
      client.run('任务B');
      wsB.emit({ type: 'content_start', seq: 1, runId: 'run_b', sessionId: 'session_multi_b', stepIndex: 0 });
      wsB.emit({ type: 'done', seq: 2, runId: 'run_b', sessionId: 'session_multi_b', payload: {} });

      // 切回 A（B 成为空闲后台会话，进入空闲倒计时）
      await client.switchSession('session_multi_a');

      // 空闲倒计时到期前：A、B 连接都在
      expect(wsA.readyState).toBe(FakeWebSocket.OPEN);
      expect(wsB.readyState).toBe(FakeWebSocket.OPEN);

      // 快进 60s+：每 20s 给 A 注入一次服务端心跳事件（模拟真实节奏，刷新读超时；
      // A 是运行中会话，连接必须活着）。累计 60s 后触发 B 的空闲断开。
      for (let elapsed = 0; elapsed <= 60_000; elapsed += 20_000) {
        wsA.emit({ type: 'heartbeat', payload: { timestamp: Date.now() } });
        await vi.advanceTimersByTimeAsync(20_000);
      }
      // 空闲的 B 被断开；运行中的 A 保持
      expect(wsB.readyState).toBe(FakeWebSocket.CLOSED);
      expect(wsA.readyState).toBe(FakeWebSocket.OPEN);
    } finally {
      vi.useRealTimers();
    }
  });

  it('草稿 run 拿到 sessionId 后转正：后续事件与状态归属该会话', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);

    client.run('草稿消息');
    const draftWs = (client as any).currentRuntime.wsClient.ws as FakeWebSocket;
    draftWs.emit({ type: 'content_start', seq: 1, runId: 'run_new', sessionId: 'session_resolved', stepIndex: 0 });

    const statuses = client.getSessionStatuses();
    expect(statuses.find((item) => item.sessionId === 'session_resolved')).toBeTruthy();
    expect(client.getState().sessionId).toBe('session_resolved');
  });

  it('deleteSession：调 REST、dispose 该会话 runtime，删当前会话后切到草稿', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);
    await client.switchSession('session_del_cur');
    await waitForConnected(client);
    const wsCur = runtimeWs(client, 'session_del_cur');

    await client.deleteSession('session_del_cur');

    // REST 已发出且带归属五元组
    const calls = ((globalThis.fetch as any).mock.calls as Array<[string, RequestInit]>)
      .filter(([url]) => new URL(url, 'http://test').pathname === '/react/session/delete');
    expect(calls).toHaveLength(1);
    expect(JSON.parse(calls[0][1].body)).toMatchObject({ sessionId: 'session_del_cur', callerKey: 'report-editor' });

    // runtime 已摘除且连接断开；视图切到草稿（sessionId 置空）
    expect((client as any).runtimes.get('session_del_cur')).toBeUndefined();
    expect(wsCur.readyState).toBe(FakeWebSocket.CLOSED);
    expect(client.getState().sessionId).toBeNull();
  });

  it('deleteSession 删非当前会话：不影响当前视图', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);
    await client.switchSession('session_del_other');
    await waitForConnected(client);
    await client.switchSession('session_del_keep');
    await waitForConnected(client);
    const wsOther = runtimeWs(client, 'session_del_other');

    await client.deleteSession('session_del_other');

    expect((client as any).runtimes.get('session_del_other')).toBeUndefined();
    expect(wsOther.readyState).toBe(FakeWebSocket.CLOSED);
    expect(client.getState().sessionId).toBe('session_del_keep');
  });

  it('forkSession：调 REST 带轮次截断点，成功后切到新会话并恢复历史', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);
    await client.switchSession('session_fork_src');
    await waitForConnected(client);

    const newSessionId = await client.forkSession('session_fork_src', { throughRunId: 'run_cut', inclusive: true });

    // REST 已发出且带 run 级截断点与归属五元组
    const calls = ((globalThis.fetch as any).mock.calls as Array<[string, RequestInit]>)
      .filter(([url]) => new URL(url, 'http://test').pathname === '/react/session/fork');
    expect(calls).toHaveLength(1);
    expect(JSON.parse(calls[0][1].body)).toMatchObject({
      sessionId: 'session_fork_src',
      throughRunId: 'run_cut',
      inclusive: true,
      callerKey: 'report-editor',
    });

    // 视图切到新会话且历史已懒恢复（/session/events mock 的 run 事件还原出用户消息）
    expect(newSessionId).toBe('session_forked_run_cut');
    expect(client.getState().sessionId).toBe('session_forked_run_cut');
    expect(client.getState().steps.some((step) => step.role === 'user')).toBe(true);
    // 源会话 runtime 保留在后台（多会话架构：分叉不销毁源会话）
    expect((client as any).runtimes.get('session_fork_src')).toBeTruthy();
  });

  it('renameSession：调 REST 并把归一化标题写入本地会话缓存', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);

    const title = await client.renameSession('session_rename_target', '  重命名后  ');

    expect(title).toBe('重命名后');
    const calls = ((globalThis.fetch as any).mock.calls as Array<[string, RequestInit]>)
      .filter(([url]) => new URL(url, 'http://test').pathname === '/react/session/rename');
    expect(calls).toHaveLength(1);
    expect(JSON.parse(calls[0][1].body)).toMatchObject({
      sessionId: 'session_rename_target',
      title: '  重命名后  ',
      callerKey: 'report-editor',
    });
    // 本地缓存（调用方作用域列表）已带新标题
    const cached = await client.listCachedSessions();
    const found = cached.find((session) => session.sessionId === 'session_rename_target');
    expect(found?.title).toBe('重命名后');
    expect(found?.callerKey).toBe('report-editor');
  });
});
