import 'fake-indexeddb/auto';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createAgentClient, type AgentClient } from '../runtime/agent-client';

// jsdom 的 WebSocket 实现会委托 npm ws 包，浏览器态直接抛 "ws does not work in the browser"，
// 并以未处理拒绝形式拖红整个 CI 运行。本文件验证的是同步与恢复逻辑，不需要真实连接，
// 用最小假 WebSocket 类替换全局实现。
class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  readyState: number = FakeWebSocket.CONNECTING;
  send(): void {}
  close(): void { this.readyState = FakeWebSocket.CLOSED; }
  addEventListener(): void {}
  removeEventListener(): void {}
  dispatchEvent(): boolean { return false; }
}
vi.stubGlobal('WebSocket', FakeWebSocket);

function createMockFetch() {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const path = new URL(url, 'http://test').pathname;
    const body = init?.body && typeof init.body === 'string' ? JSON.parse(init.body) : {};

    if (path === '/api/chat/files/upload') {
      const formData = init?.body as FormData;
      const file = formData.get('file') as File;
      return {
        ok: true,
        status: 200,
        json: async () => ({
          errNo: 0,
          errMsg: 'succ',
          data: {
            fileId: 'file_uploaded',
            fileName: file.name,
            ext: 'md',
            size: file.size,
            uploadedAt: '2026-07-13 10:00:00',
          },
        }),
      };
    }

    if (path === '/react/session/list') {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          errNo: 0,
          errMsg: 'succ',
          data: {
            sessions: [
              {
                sessionId: 'session_server',
                callerKey: body.callerKey,
                routeValues: body.routeValues ?? [],
                type: 'chat',
                title: '服务端会话',
                lastRunId: 'run_server',
                lastMessage: '服务端摘要',
                state: 'active',
                createdAt: '2026-03-14 10:00:00',
                updatedAt: '2026-03-14 10:01:00',
              },
            ],
            total: 1,
            page: 1,
            pageSize: 20,
          },
        }),
      };
    }

    if (path === '/react/session/events') {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          errNo: 0,
          errMsg: 'succ',
          data: {
            sessionId: body.sessionId,
            title: '服务端会话',
            events: [
              {
                type: 'run',
                seq: 1,
                runId: 'run_server',
                sessionId: body.sessionId,
                payload: {
                  callerKey: 'report-editor',
                  routeValues: ['report_abc'],
                  type: 'chat',
                  userPrompt: '历史问题',
                },
                createdAt: '2026-03-14 10:00:00',
              },
              {
                type: 'content_start',
                seq: 2,
                runId: 'run_server',
                sessionId: body.sessionId,
                stepIndex: 0,
                createdAt: '2026-03-14 10:00:01',
              },
              {
                type: 'content_end',
                seq: 3,
                runId: 'run_server',
                sessionId: body.sessionId,
                stepIndex: 0,
                payload: { content: '历史回答' },
                createdAt: '2026-03-14 10:00:02',
              },
            ],
          },
        }),
      };
    }

    return {
      ok: false,
      status: 404,
      statusText: 'Not Found',
      json: async () => ({}),
    };
  });
}

async function waitForFetchPath(path: string): Promise<void> {
  for (let index = 0; index < 20; index += 1) {
    const paths = ((globalThis.fetch as any).mock.calls as Array<[string, RequestInit]>).map(([url]) => {
      return new URL(url, 'http://test').pathname;
    });
    if (paths.includes(path)) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`Expected fetch path not called: ${path}`);
}

describe('AgentClient server cache sync', () => {
  let client: AgentClient | null = null;
  let originalFetch: typeof fetch | undefined;

  beforeEach(() => {
    originalFetch = globalThis.fetch;
    globalThis.fetch = createMockFetch() as any;
  });

  afterEach(async () => {
    client?.disconnect();
    client = null;
    if (originalFetch) {
      globalThis.fetch = originalFetch;
    }
    indexedDB.deleteDatabase('agent-sdk-ledger');
  });

  it('should call session list and events during initial sync', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    await waitForFetchPath('/react/session/list');
    await waitForFetchPath('/react/session/events');

    const paths = ((globalThis.fetch as any).mock.calls as Array<[string, RequestInit]>).map(([url]) => {
      return new URL(url, 'http://test').pathname;
    });
    expect(paths).toContain('/react/session/list');
    expect(paths).toContain('/react/session/events');
  });

  it('should sync server sessions into local cache', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const sessions = await client.syncSessions();
    const cachedSessions = await client.listCachedSessions();

    expect(sessions).toHaveLength(1);
    expect(cachedSessions).toHaveLength(1);
    expect(cachedSessions[0].sessionId).toBe('session_server');
    expect(cachedSessions[0].lastRunId).toBe('run_server');
    expect(cachedSessions[0].lastMessage).toBe('服务端摘要');
  });

  it('should sync server events before replaying a session', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    await client.switchSession('session_server');

    const state = client.getState();
    expect(state.sessionId).toBe('session_server');
    expect(state.steps).toHaveLength(2);
    expect(state.steps[0].role).toBe('user');
    expect(state.steps[0].content).toBe('历史问题');
    expect(state.steps[1].role).toBe('assistant');
    expect(state.steps[1].content).toBe('历史回答');

    const events = await client.loadEvents('session_server');
    expect(events).toHaveLength(3);
    expect(events[0].createdAt).toBe('2026-03-14 10:00:00');
  });

  it('should enter a draft session without clearing cached history', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const clearSessions = vi.fn();
    (client as any).ledger.clearSessions = clearSessions;

    const created = client.newSession();

    expect(created).toBe(true);
    expect(client.getState().sessionId).toBeNull();
    expect(client.getState().steps).toHaveLength(0);
    expect(clearSessions).not.toHaveBeenCalled();
  });

  it('should send the first draft message without the previous session id', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    (client as any).handleEvent({
      type: 'done',
      seq: 1,
      runId: 'run_existing',
      sessionId: 'session_existing',
      payload: {},
    });
    client.newSession();

    const send = vi.fn();
    (client as any).wsClient.send = send;

    client.run('新问题');

    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0]).toMatchObject({
      type: 'run',
      sessionId: undefined,
    });
    expect(client.getState().sessionId).toBeNull();
    expect(client.getState().steps[0].content).toBe('新问题');
  });

  it('should resend a pending run after the existing socket disconnects', () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const wsClient = (client as any).wsClient;
    const send = vi.fn();
    wsClient.send = send;
    Object.defineProperty(wsClient, 'isConnected', { value: true, configurable: true });

    client.run('断网后继续的问题');
    expect(send).toHaveBeenCalledTimes(1);

    (client as any).handleConnectionChange(false);
    (client as any).handleWsOpen();

    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[1][0]).toMatchObject({
      type: 'run',
      payload: { userPrompt: '断网后继续的问题' },
    });
  });

  it('should retain a terminal run id without sending stale control messages', () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const send = vi.fn();
    (client as any).wsClient.send = send;
    (client as any).reducer.applyEvent({
      type: 'run',
      seq: 1,
      runId: 'run_terminal',
      sessionId: 'session_terminal',
      payload: { userPrompt: '问题' },
    });
    (client as any).reducer.applyEvent({
      type: 'done',
      seq: 2,
      runId: 'run_terminal',
      sessionId: 'session_terminal',
      payload: {},
    });

    expect(client.getState()).toMatchObject({
      status: 'done',
      currentRunId: 'run_terminal',
    });

    client.cancel();
    client.sendAskQuestionAnswer('tool_terminal', { answers: [], skipped: true });

    expect(send).not.toHaveBeenCalled();
  });

  it('should send controlContext and llmContext with run payload', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
      controlContext: { scope: 'default', debug: true },
      llmContext: { page: 'report', reportId: 'r1' },
    });

    const send = vi.fn();
    (client as any).wsClient.send = send;

    client.run('新问题', {
      llmContext: { page: 'detail', reportId: 'r2' },
    });

    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0]).toMatchObject({
      type: 'run',
      payload: {
        callerKey: 'report-editor',
        routeValues: ['report_abc'],
        type: 'chat',
        userPrompt: '新问题',
        controlContext: { scope: 'default', debug: true },
        llmContext: { page: 'detail', reportId: 'r2' },
      },
    });
  });

  it('should use the beforeRun object result as the current llmContext', () => {
    const beforeRun = vi.fn(() => ({
      currentSql: 'SELECT * FROM user',
      currentTab: 'query-1',
    }));
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'demo-app',
      routeValues: ['query-1'],
      llmContext: { source: 'static' },
      hooks: { beforeRun },
    });
    const send = vi.fn();
    (client as any).wsClient.send = send;

    client.run('分析当前 SQL', {
      inputOrigin: { type: 'manual' },
      llmContext: { source: 'run-option' },
    });

    expect(beforeRun).toHaveBeenCalledWith({
      userPrompt: '分析当前 SQL',
      sessionId: null,
      inputOrigin: { type: 'manual' },
    });
    expect(send.mock.calls[0][0].payload.llmContext).toEqual({
      currentSql: 'SELECT * FROM user',
      currentTab: 'query-1',
    });
  });

  it('should send the beforeRun string result as a plain llmContext value', () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'demo-app',
      hooks: {
        beforeRun: () => '<system_prompt>用户正在编辑sql</system_prompt>',
      },
    });
    const send = vi.fn();
    (client as any).wsClient.send = send;

    client.run('继续');

    expect(send.mock.calls[0][0].payload.llmContext).toBe(
      '<system_prompt>用户正在编辑sql</system_prompt>',
    );
  });

  it('should wait for an async beforeRun result before sending the run', async () => {
    let resolveContext!: (value: { latestExecutions: number[] }) => void;
    const beforeRun = vi.fn(() => new Promise<{ latestExecutions: number[] }>((resolve) => {
      resolveContext = resolve;
    }));
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'demo-app',
      hooks: { beforeRun },
    });
    const send = vi.fn();
    (client as any).wsClient.send = send;

    const runPromise = client.run('分析最近执行记录');
    expect(send).not.toHaveBeenCalled();

    resolveContext({ latestExecutions: [101, 102] });
    await runPromise;

    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0].payload.llmContext).toEqual({
      latestExecutions: [101, 102],
    });
  });

  it('should fall back to run llmContext when beforeRun returns undefined', () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'demo-app',
      llmContext: { source: 'static' },
      hooks: { beforeRun: () => undefined },
    });
    const send = vi.fn();
    (client as any).wsClient.send = send;

    client.run('继续', { llmContext: '<context>本轮上下文</context>' });

    expect(send.mock.calls[0][0].payload.llmContext).toBe('<context>本轮上下文</context>');
  });

  it('should upload chat attachments through the service upload endpoint', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const file = new File(['# context'], 'context.md', { type: 'text/markdown' });
    const uploaded = await client.uploadAttachment(file);

    expect(uploaded).toMatchObject({
      fileId: 'file_uploaded',
      fileName: 'context.md',
      ext: 'md',
    });
    const uploadCall = ((globalThis.fetch as any).mock.calls as Array<[string, RequestInit]>)
      .find(([url]) => new URL(url, 'http://test').pathname === '/api/chat/files/upload');
    expect(uploadCall).toBeDefined();
    expect(uploadCall?.[1].method).toBe('POST');
    expect(uploadCall?.[1].credentials).toBe('include');
    expect((uploadCall?.[1].body as FormData).get('file')).toBe(file);
  });

  it('should include attachments in the ReAct run payload', () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });
    const send = vi.fn();
    (client as any).wsClient.send = send;

    client.run('分析附件', {
      attachments: [{ fileId: 'file_uploaded', fileName: 'context.md' }],
    });

    expect(send.mock.calls[0][0].payload.attachments).toEqual([
      { fileId: 'file_uploaded', fileName: 'context.md' },
    ]);
  });

  it('should include next-button origin in the ReAct run payload', () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });
    const send = vi.fn();
    (client as any).wsClient.send = send;

    client.run('继续分析', {
      inputOrigin: {
        type: 'next_button',
        sourceRunId: 'run_source',
        sourceStepIndex: 3,
        buttonText: '继续分析',
        buttonIndex: 0,
      },
    });

    expect(send.mock.calls[0][0].payload.inputOrigin).toEqual({
      type: 'next_button',
      sourceRunId: 'run_source',
      sourceStepIndex: 3,
      buttonText: '继续分析',
      buttonIndex: 0,
    });
  });

  it('should append a new message without clearing previous visible steps', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    (client as any).handleEvent({
      type: 'run',
      seq: 1,
      runId: 'run_existing',
      sessionId: 'session_existing',
      payload: { userPrompt: '上一轮问题' },
    });
    (client as any).handleEvent({
      type: 'content_start',
      seq: 2,
      runId: 'run_existing',
      sessionId: 'session_existing',
      stepIndex: 0,
    });
    (client as any).handleEvent({
      type: 'content_end',
      seq: 3,
      runId: 'run_existing',
      sessionId: 'session_existing',
      stepIndex: 0,
      payload: { content: '上一轮回答' },
    });
    (client as any).handleEvent({
      type: 'done',
      seq: 4,
      runId: 'run_existing',
      sessionId: 'session_existing',
      payload: {},
    });

    const send = vi.fn();
    (client as any).wsClient.send = send;
    Object.defineProperty((client as any).wsClient, 'isConnected', { value: true, configurable: true });
    (client as any).ledger.append = vi.fn();
    (client as any).ledger.appendEvents = vi.fn();
    (client as any).ledger.upsertSession = vi.fn();

    client.run('新问题');

    expect(client.getState().steps.map((step) => step.content)).toEqual(['上一轮问题', '上一轮回答', '新问题']);
    expect(client.getState().status).toBe('running');
    expect(send).toHaveBeenCalledWith(expect.objectContaining({
      type: 'run',
      sessionId: 'session_existing',
    }));
  });

  it('should persist a local run event when the first live event returns real ids', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const send = vi.fn();
    const append = vi.fn();
    const appendEvents = vi.fn();
    (client as any).wsClient.send = send;
    (client as any).ledger.append = append;
    (client as any).ledger.appendEvents = appendEvents;
    (client as any).ledger.upsertSession = vi.fn();

    client.run('新问题');
    (client as any).handleEvent({
      type: 'content_start',
      seq: 1,
      runId: 'run_new',
      sessionId: 'session_new',
      stepIndex: 0,
    });

    expect(append).toHaveBeenNthCalledWith(1, expect.objectContaining({
      type: 'run',
      seq: 0,
      runId: 'run_new',
      sessionId: 'session_new',
      payload: expect.objectContaining({ userPrompt: '新问题' }),
    }));
    expect(appendEvents).not.toHaveBeenCalled();
  });

  it('should persist a completed content segment instead of raw chunks', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const append = vi.fn();
    const appendEvents = vi.fn();
    (client as any).ledger.append = append;
    (client as any).ledger.appendEvents = appendEvents;
    (client as any).ledger.upsertSession = vi.fn();

    (client as any).handleEvent({
      type: 'content_start',
      seq: 1,
      runId: 'run_segment',
      sessionId: 'session_segment',
      stepIndex: 0,
    });
    (client as any).handleEvent({
      type: 'content_delta',
      seq: 2,
      runId: 'run_segment',
      sessionId: 'session_segment',
      stepIndex: 0,
      payload: { contentDelta: '你' },
    });
    (client as any).handleEvent({
      type: 'content_delta',
      seq: 3,
      runId: 'run_segment',
      sessionId: 'session_segment',
      stepIndex: 0,
      payload: { contentDelta: '好' },
    });

    expect(appendEvents).not.toHaveBeenCalled();

    (client as any).handleEvent({
      type: 'content_end',
      seq: 4,
      runId: 'run_segment',
      sessionId: 'session_segment',
      stepIndex: 0,
      payload: { content: '你好' },
    });

    expect(appendEvents).toHaveBeenCalledWith([
      expect.objectContaining({ type: 'content_start', runId: 'run_segment' }),
      expect.objectContaining({ type: 'content_delta', payload: { contentDelta: '你好' } }),
      expect.objectContaining({ type: 'content_end', payload: { content: '你好' } }),
    ]);
    expect(append).not.toHaveBeenCalled();
  });

  it('should bind a draft session after receiving the server session id', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const send = vi.fn();
    const append = vi.fn();
    const upsertSession = vi.fn();
    (client as any).wsClient.send = send;
    (client as any).ledger.append = append;
    (client as any).ledger.upsertSession = upsertSession;

    client.newSession();
    client.run('新问题');
    (client as any).handleEvent({
      type: 'content_start',
      seq: 1,
      runId: 'run_new',
      sessionId: 'session_new',
      stepIndex: 0,
    });

    expect(client.getState().sessionId).toBe('session_new');
    expect(append).toHaveBeenCalledWith(expect.objectContaining({ sessionId: 'session_new' }));
    expect(upsertSession).toHaveBeenCalledWith(expect.objectContaining({ sessionId: 'session_new' }));
  });

  it('should not treat reused seq in a different run as a gap', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const handleSeqGap = vi.fn();
    const appendEvents = vi.fn();
    (client as any).handleSeqGap = handleSeqGap;
    (client as any).ledger.append = vi.fn();
    (client as any).ledger.appendEvents = appendEvents;
    (client as any).ledger.upsertSession = vi.fn();

    (client as any).handleEvent({
      type: 'done',
      seq: 1,
      runId: 'run_1',
      sessionId: 'session_existing',
      payload: {},
    });
    (client as any).handleEvent({
      type: 'content_start',
      seq: 1,
      runId: 'run_2',
      sessionId: 'session_existing',
      stepIndex: 0,
    });

    expect(handleSeqGap).not.toHaveBeenCalled();
    expect(appendEvents).toHaveBeenCalledTimes(1);
    expect(appendEvents).toHaveBeenCalledWith([expect.objectContaining({ type: 'done', runId: 'run_1' })]);
  });

  it('should skip server events overwrite while current session is running', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const getEvents = vi.fn();
    (client as any).sessionManager.getEvents = getEvents;
    (client as any).ledger.getEvents = vi.fn(async () => []);
    (client as any).ledger.appendEvents = vi.fn();
    (client as any).ledger.upsertSession = vi.fn();

    (client as any).handleEvent({
      type: 'content_start',
      seq: 1,
      runId: 'run_running',
      sessionId: 'session_running',
      stepIndex: 0,
    });

    await client.syncSessionEvents('session_running');

    expect(getEvents).not.toHaveBeenCalled();
  });

  // 暂时下线 create_plan，恢复工具时取消 skip。
  it.skip('should accept the latest plan when the new run receives a runId', () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    const wsClient = (client as any).wsClient;
    wsClient.send = vi.fn();
    Object.defineProperty(wsClient, 'isConnected', { value: true, configurable: true });
    (client as any).reducer.applyEvent({
      type: 'tool_use_start',
      seq: 1,
      stepIndex: 0,
      runId: 'run_plan',
      payload: {
        toolUseId: 'tool_plan',
        toolName: 'create_plan',
        toolInput: { title: '计划', steps: [{ id: 'a', outline: '检查', message: '检查实现' }] },
        executedBy: 'internal',
        status: 'running',
      },
    });
    (client as any).reducer.applyEvent({ type: 'done', seq: 2, runId: 'run_plan', payload: {} });

    client.run('开始实施');
    expect(client.getState().steps[0].toolCalls[0].planConfirmationStatus).toBe('submitting');
    (client as any).handleEvent({
      type: 'content_start',
      seq: 1,
      runId: 'run_confirm',
      sessionId: 'session_1',
      stepIndex: 0,
    });

    expect(client.getState().steps[0].toolCalls[0].planConfirmationStatus).toBe('accepted');
  });

  // 暂时下线 create_plan，恢复工具时取消 skip。
  it.skip('should restore the plan after pending run reconnect attempts are exhausted', () => {
    client = createAgentClient({ baseUrl: '/react', callerKey: 'report-editor' });
    const wsClient = (client as any).wsClient;
    wsClient.send = vi.fn();
    wsClient.clearPendingMessages = vi.fn();
    Object.defineProperty(wsClient, 'isConnected', { value: true, configurable: true });
    (client as any).reducer.applyEvent({
      type: 'tool_use_start',
      seq: 1,
      stepIndex: 0,
      runId: 'run_plan',
      payload: {
        toolUseId: 'tool_plan',
        toolName: 'create_plan',
        toolInput: { title: '计划', steps: [{ id: 'a', outline: '检查', message: '检查实现' }] },
        executedBy: 'internal',
        status: 'running',
      },
    });
    (client as any).reducer.applyEvent({ type: 'done', seq: 2, runId: 'run_plan', payload: {} });

    client.run('开始实施');
    (client as any).handleConnectionChange(false);
    for (let attempt = 1; attempt <= 5; attempt += 1) {
      (client as any).handleReconnectAttempt(attempt);
    }

    expect(client.getState().steps[0].toolCalls[0].planConfirmationStatus).toBe('pending');
    expect(wsClient.clearPendingMessages).toHaveBeenCalled();
  });

  // 暂时下线 create_plan，恢复工具时取消 skip。
  it.skip('should confirm a plan through plain user text without changing host llmContext', () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      llmContext: { page: 'report', reportId: 'r1' },
    });
    const send = vi.fn();
    const wsClient = (client as any).wsClient;
    wsClient.send = send;
    Object.defineProperty(wsClient, 'isConnected', { value: true, configurable: true });

    client.confirmPlan('plan_abc');

    expect(send.mock.calls[0][0]).toMatchObject({
      type: 'run',
      payload: {
        userPrompt: '开始执行刚才的计划',
        llmContext: { page: 'report', reportId: 'r1' },
      },
    });
  });

  // 暂时下线 create_plan，恢复工具时取消 skip。
  it.skip('should preserve string llmContext unchanged when confirming a plan', () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      llmContext: '<context>宿主上下文</context>',
    });
    const send = vi.fn();
    const wsClient = (client as any).wsClient;
    wsClient.send = send;
    Object.defineProperty(wsClient, 'isConnected', { value: true, configurable: true });

    client.confirmPlan('plan_string');

    expect(send.mock.calls[0][0].payload.llmContext).toBe('<context>宿主上下文</context>');
  });

  it('should not refetch history during an active run when a seq gap appears', async () => {
    client = createAgentClient({
      baseUrl: '/react',
      callerKey: 'report-editor',
      routeValues: ['report_abc'],
    });

    const syncSessionEvents = vi.fn();
    const appendEvents = vi.fn();
    (client as any).syncSessionEvents = syncSessionEvents;
    (client as any).ledger.append = vi.fn();
    (client as any).ledger.appendEvents = appendEvents;
    (client as any).ledger.upsertSession = vi.fn();

    (client as any).handleEvent({
      type: 'content_start',
      seq: 1,
      runId: 'run_gap',
      sessionId: 'session_gap',
      stepIndex: 0,
    });
    (client as any).handleEvent({
      type: 'content_delta',
      seq: 3,
      runId: 'run_gap',
      sessionId: 'session_gap',
      stepIndex: 0,
      payload: { contentDelta: '跳号后的内容' },
    });

    expect(syncSessionEvents).not.toHaveBeenCalled();
    expect(appendEvents).not.toHaveBeenCalled();
    expect(client.getState().steps[0].content).toContain('跳号后的内容');
  });
});
