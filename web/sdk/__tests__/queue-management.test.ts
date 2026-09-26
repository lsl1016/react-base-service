import 'fake-indexeddb/auto';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createAgentClient, type AgentClient } from '../runtime/agent-client';

// 队列管理（S3）与 mid-run steer 发送分支的客户端行为测试。
// 复用 agent-client-sync 的思路：假 WebSocket + mock fetch。
const sentMessages: Array<Record<string, unknown>> = [];

class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  readyState: number = FakeWebSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor() {
    // 模拟异步建连：回调在 handlers 挂载后的宏任务里触发。
    setTimeout(() => {
      this.readyState = FakeWebSocket.OPEN;
      this.onopen?.();
    }, 0);
  }

  send(data: string): void {
    sentMessages.push(JSON.parse(data) as Record<string, unknown>);
  }

  close(): void {
    this.readyState = FakeWebSocket.CLOSED;
    // WsClient 的 onclose 会读取 event.code/reason/wasClean，传最小事件对象。
    this.onclose?.({ code: 1000, reason: '', wasClean: true } as unknown as CloseEvent);
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
      return jsonOk({
        sessions: [{
          sessionId: 'session_server',
          callerKey: body.callerKey,
          routeValues: body.routeValues ?? [],
          type: 'chat',
          title: '服务端会话',
          lastRunId: 'run_server',
          lastMessage: '服务端摘要',
          state: 'active',
          createdAt: '2026-09-25 10:00:00',
          updatedAt: '2026-09-25 10:01:00',
        }],
        total: 1,
        page: 1,
        pageSize: 20,
      });
    }

    if (path === '/react/session/events') {
      return jsonOk({
        sessionId: body.sessionId,
        title: '服务端会话',
        events: [
          {
            type: 'run',
            seq: 1,
            runId: 'run_server',
            sessionId: body.sessionId,
            payload: { callerKey: 'report-editor', routeValues: ['report_abc'], type: 'chat', userPrompt: '历史问题' },
            createdAt: '2026-09-25 10:00:00',
          },
          {
            type: 'content_end',
            seq: 2,
            runId: 'run_server',
            sessionId: body.sessionId,
            stepIndex: 0,
            payload: { content: '历史回答' },
            createdAt: '2026-09-25 10:00:02',
          },
        ],
      });
    }

    if (path === '/react/queue/list') {
      return jsonOk({
        items: [
          { id: 1, pendingInputId: 'pend_1', sessionId: body.sessionId, content: '排队消息一', seq: 1, status: 'queued', createdAt: '2026-09-25 10:00:10' },
          { id: 2, pendingInputId: 'pend_2', sessionId: body.sessionId, content: '排队消息二', seq: 2, status: 'queued', createdAt: '2026-09-25 10:00:20' },
        ],
        queueEnabled: true,
        autoDrain: true,
      });
    }

    if (path === '/react/queue/update' || path === '/react/queue/delete' || path === '/react/queue/reorder') {
      return jsonOk({ claimed: true, queueLength: 2 });
    }

    return { ok: false, status: 404, statusText: 'Not Found', json: async () => ({}) };
  });
}

function fetchPaths(): string[] {
  return ((globalThis.fetch as any).mock.calls as Array<[string]>).map(([url]) => new URL(url, 'http://test').pathname);
}

async function waitForConnected(client: AgentClient): Promise<void> {
  for (let index = 0; index < 50; index += 1) {
    if (client.isConnected) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error('client did not connect');
}

describe('AgentClient 队列管理（S3）与 mid-run steer 发送', () => {
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
    // 不做 indexedDB.deleteDatabase：未 await 的删除请求会在 fake-indexeddb 里排队，
    // 堵住后续用例的 ledger open（表现为测试 5s 超时）；用例间共享同一份本地账本
    // 数据无碍断言（fixtures 一致），disconnect 已保证客户端彼此隔离。
  });

  it('listQueue/updateQueueItem/reorderQueue/deleteQueueItem 走对应 REST 端点并携带会话五元组', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor', routeValues: ['report_abc'] });
    await client.switchSession('session_server');

    const list = await client.listQueue();
    expect(list.items).toHaveLength(2);
    expect(list.items[0]).toMatchObject({ id: 1, pendingInputId: 'pend_1', content: '排队消息一', seq: 1 });
    expect(list.queueEnabled).toBe(true);
    expect(list.autoDrain).toBe(true);

    const update = await client.updateQueueItem(1, '新内容');
    expect(update).toEqual({ claimed: true, queueLength: 2 });
    const reorder = await client.reorderQueue([2, 1]);
    expect(reorder.claimed).toBe(true);
    const del = await client.deleteQueueItem(1);
    expect(del.claimed).toBe(true);

    const paths = fetchPaths();
    expect(paths).toContain('/react/queue/list');
    expect(paths).toContain('/react/queue/update');
    expect(paths).toContain('/react/queue/reorder');
    expect(paths).toContain('/react/queue/delete');
  });

  it('无会话时 listQueue 返回空队列而不发请求', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    // 构造后立即调用：初始同步尚未回填 sessionId
    const list = await client.listQueue();
    expect(list).toEqual({ items: [], queueEnabled: false, autoDrain: false });
  });

  it('空闲时 sendQueuedMessage 发送 queue_send（带 sessionId 与 pendingInputId）', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);
    await client.switchSession('session_server');

    client.sendQueuedMessage('pend_9');
    const send = sentMessages.at(-1);
    expect(send?.type).toBe('queue_send');
    expect(send?.sessionId).toBe('session_server');
    expect(send?.payload).toEqual({ sessionId: 'session_server', pendingInputId: 'pend_9' });
  });

  it('run 活跃时 sendQueuedMessage 不发送（后端同样会拒绝）', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);
    await client.switchSession('session_server');

    client.run('第一条');
    expect(client.getState().status).toBe('running');
    client.sendQueuedMessage('pend_9');
    expect(sentMessages.filter((msg) => msg.type === 'queue_send')).toHaveLength(0);
  });

  it('run 活跃时再次 run() 走 steer 分支：不重置运行态、不插乐观气泡、照发 WS run 消息', async () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    client.connect();
    await waitForConnected(client);
    await client.switchSession('session_server');

    client.run('第一条');
    const state1 = client.getState();
    expect(state1.status).toBe('running');
    expect(state1.steps.filter((step) => step.role === 'user').at(-1)?.content).toBe('第一条');

    client.run('第二条');

    const state2 = client.getState();
    // steer 路径不插乐观用户气泡：用户消息仍只有历史回放一条 + 乐观的第一条。
    const userSteps = state2.steps.filter((step) => step.role === 'user');
    expect(userSteps).toHaveLength(2);
    expect(userSteps.at(-1)?.content).toBe('第一条');
    // 运行态不被重置（重置会把 status 打回 running 但清空 currentRunId 等游标，
    // 这里用"未新增用户 step + 状态未闪断"作为不重置的可观察证据）。
    expect(state2.status).toBe('running');
    // 两条 run 消息都发给后端，第二条由 HandleWSSteer 准入。
    const runSends = sentMessages.filter((msg) => msg.type === 'run');
    expect(runSends).toHaveLength(2);
    expect((runSends[1].payload as Record<string, unknown>).userPrompt).toBe('第二条');
  });
});
