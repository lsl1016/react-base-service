import { describe, expect, it } from 'vitest';
import type { ReactEvent } from '../protocol/types';
import { EventReducer } from '../runtime/event-reducer';

describe('EventReducer', () => {
  it('should initialize with idle status', () => {
    const reducer = new EventReducer();
    const state = reducer.getState();
    expect(state.status).toBe('idle');
    expect(state.steps).toHaveLength(0);
    expect(state.todos).toHaveLength(0);
    expect(state.usage.totalInputTokens).toBe(0);
  });

  it('should not notify subscribers for heartbeat events', () => {
    const reducer = new EventReducer();
    let notifyCount = 0;
    reducer.subscribe(() => {
      notifyCount += 1;
    });

    reducer.applyEvent({ type: 'heartbeat', seq: 1 });

    expect(notifyCount).toBe(0);
  });

  it('should notify subscribers once when replaying history events', () => {
    const reducer = new EventReducer();
    let notifyCount = 0;
    reducer.subscribe(() => {
      notifyCount += 1;
    });

    reducer.replayEvents([
      {
        type: 'run',
        seq: 1,
        sessionId: 'session_1',
        runId: 'run_1',
        payload: {
          callerKey: 'report-editor',
          type: 'chat',
          userPrompt: '历史问题',
        },
      },
      { type: 'content_start', seq: 2, sessionId: 'session_1', runId: 'run_1', stepIndex: 0 },
      {
        type: 'content_end',
        seq: 3,
        sessionId: 'session_1',
        runId: 'run_1',
        stepIndex: 0,
        payload: { content: '历史回答' },
      },
      { type: 'done', seq: 4, sessionId: 'session_1', runId: 'run_1', payload: {} },
    ]);

    expect(notifyCount).toBe(1);
    expect(reducer.getState().status).toBe('idle');
  });

  it('should preserve user display parts in state snapshots', () => {
    const reducer = new EventReducer();
    const displayParts = [
      { type: 'text' as const, text: '分析 ' },
      {
        type: 'shortcut' as const,
        id: 'field-gmv',
        label: '/gmv',
        group: 'Fields',
        description: 'GMV 字段',
        data: {
          tag: 'field',
          proto: { id: 'gmv' },
        },
      },
    ];

    reducer.addUserStep('分析 <field id="gmv">/gmv</field>', undefined, displayParts);
    const state = reducer.getState();

    expect(state.steps[0].content).toBe('分析 <field id="gmv">/gmv</field>');
    expect(state.steps[0].displayParts).toEqual(displayParts);

    state.steps[0].displayParts?.push({ type: 'text', text: '污染' });
    expect(reducer.getState().steps[0].displayParts).toEqual(displayParts);
  });

  it('should handle thought_start event', () => {
    const reducer = new EventReducer();
    const event: ReactEvent = {
      type: 'thought_start',
      seq: 1,
      runId: 'run_1',
      sessionId: 'session_1',
      stepIndex: 0,
      payload: {},
    };
    reducer.applyEvent(event);
    const state = reducer.getState();
    expect(state.status).toBe('running');
    expect(state.sessionId).toBe('session_1');
    expect(state.currentRunId).toBe('run_1');
    expect(state.steps).toHaveLength(1);
    expect(state.steps[0].thoughts).toBe('');
    expect(state.steps[0].thoughtComplete).toBe(false);
  });

  it('should accumulate thought_delta events', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({
      type: 'thought_delta',
      seq: 2,
      stepIndex: 0,
      payload: { contentDelta: '思考' },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'thought_delta',
      seq: 3,
      stepIndex: 0,
      payload: { contentDelta: '中...' },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.steps[0].thoughts).toBe('思考中...');
  });

  it('should handle thought_end event', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({
      type: 'thought_end',
      seq: 2,
      stepIndex: 0,
      payload: { content: '完整思考内容' },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.steps[0].thoughts).toBe('完整思考内容');
    expect(state.steps[0].thoughtComplete).toBe(true);
  });

  it('should handle content_start/delta/end events', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'content_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({
      type: 'content_delta',
      seq: 2,
      stepIndex: 0,
      payload: { contentDelta: '你好' },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'content_delta',
      seq: 3,
      stepIndex: 0,
      payload: { contentDelta: '世界' },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'content_end',
      seq: 4,
      stepIndex: 0,
      payload: { content: '你好世界' },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.steps[0].contentStarted).toBe(true);
    expect(state.steps[0].content).toBe('你好世界');
    expect(state.steps[0].contentComplete).toBe(true);
  });

  it('marks tool calls after content_start with afterContent', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'thought_end', seq: 1, stepIndex: 0, runId: 'run_1', payload: { content: 't' } } as ReactEvent);
    reducer.applyEvent({ type: 'content_start', seq: 2, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({ type: 'content_end', seq: 3, stepIndex: 0, runId: 'run_1', payload: { content: 'c' } } as ReactEvent);
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 4,
      stepIndex: 0,
      runId: 'run_1',
      payload: {
        toolUseId: 'tool_1',
        toolName: 'ask_question',
        toolInput: {},
        executedBy: 'internal',
        status: 'waiting',
      },
    } as ReactEvent);

    const tc = reducer.getState().steps.find((s) => s.role === 'assistant')?.toolCalls[0];
    expect(tc?.afterContent).toBe(true);
  });

  it('should replay run event as user step without overwriting assistant step', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'run',
      seq: 1,
      sessionId: 'session_1',
      runId: 'run_1',
      payload: {
        callerKey: 'report-editor',
        routeValues: ['report_abc'],
        type: 'chat',
        userPrompt: '历史问题',
      },
    });
    reducer.applyEvent({ type: 'content_start', seq: 2, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({
      type: 'content_end',
      seq: 3,
      stepIndex: 0,
      runId: 'run_1',
      payload: { content: '历史回答' },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.steps).toHaveLength(2);
    expect(state.steps[0].role).toBe('user');
    expect(state.steps[0].content).toBe('历史问题');
    expect(state.steps[1].role).toBe('assistant');
    expect(state.steps[1].content).toBe('历史回答');
  });

  it('should settle stale ask_question when replaying a finished session', () => {
    const reducer = new EventReducer();

    reducer.replayEvents([
      {
        type: 'run',
        seq: 1,
        sessionId: 'session_1',
        runId: 'run_1',
        payload: { userPrompt: '历史问题' },
      },
      {
        type: 'tool_use_start',
        seq: 2,
        sessionId: 'session_1',
        runId: 'run_1',
        stepIndex: 0,
        payload: {
          toolUseId: 'tool_1',
          toolName: 'ask_question',
          toolInput: {},
          executedBy: 'internal',
          status: 'waiting',
        },
      },
    ]);

    const state = reducer.getState();
    expect(state.status).toBe('idle');
    expect(state.currentRunId).toBeNull();
    expect(state.steps[1].toolCalls[0].status).toBe('cancelled');
  });

  it('should not render internal compact run as a user step', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'run',
      seq: 1,
      sessionId: 'session_1',
      runId: 'run_compact',
      payload: {
        callerKey: 'report-editor',
        routeValues: ['report_abc'],
        type: 'chat',
        userPrompt: 'You are compacting conversation history for future model turns.\nProduce a concise plain-text summary.',
      },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'compact_end',
      seq: 2,
      sessionId: 'session_1',
      runId: 'run_compact',
      stepIndex: 0,
      payload: { beforeMessageCount: 10, afterMessageCount: 3, summary: '摘要' },
    } as ReactEvent);

    const state = reducer.getState();
    // 内部压缩 run 不产生 user 步骤；compact_end 会插入一条 compact 分隔步骤
    expect(state.steps.filter((step) => step.role === 'user')).toHaveLength(0);
    expect(state.steps.filter((step) => step.role === 'compact')).toHaveLength(1);
    expect(state.compactState?.summary).toBe('摘要');
  });

  it('should handle tool_use_start event', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 1,
      stepIndex: 0,
      runId: 'run_1',
      payload: {
        toolUseId: 'tool_1',
        toolName: 'MutateChart',
        toolInput: { chartType: 'bar' },
        executedBy: 'server',
        status: 'running',
        riskLevel: 'medium',
        readOnly: true,
      },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.status).toBe('running');
    expect(state.steps[0].toolCalls).toHaveLength(1);
    expect(state.steps[0].toolCalls[0].toolName).toBe('MutateChart');
    expect(state.steps[0].toolCalls[0].status).toBe('running');
    expect(state.steps[0].toolCalls[0].executedBy).toBe('server');
    // ToolMeta 徽标字段（主 run 路径与 plan 路径同口径）
    expect(state.steps[0].toolCalls[0].riskLevel).toBe('medium');
    expect(state.steps[0].toolCalls[0].readOnly).toBe(true);
  });

  it('should handle tool_use_end event', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 1,
      stepIndex: 0,
      runId: 'run_1',
      payload: { toolUseId: 'tool_1', toolName: 'Test', executedBy: 'server' },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'tool_use_end',
      seq: 2,
      payload: {
        toolUseId: 'tool_1',
        content: '执行成功',
        isError: false,
        executedBy: 'server',
        status: 'success',
        durationMs: 500,
        meta: { note: 'meta-passthrough' },
        riskLevel: 'medium',
        readOnly: true,
      },
    } as ReactEvent);

    const state = reducer.getState();
    const tc = state.steps[0].toolCalls[0];
    expect(tc.status).toBe('done');
    expect(tc.result).toBe('执行成功');
    expect(tc.isError).toBe(false);
    expect(tc.durationMs).toBe(500);
    // ToolMeta 徽标与 UI 旁路字段（主 run 路径与 plan 路径同口径）
    expect(tc.riskLevel).toBe('medium');
    expect(tc.readOnly).toBe(true);
    expect(tc.meta?.note).toBe('meta-passthrough');
  });

  it('should handle client_tool_use_start event', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'client_tool_use_start',
      seq: 1,
      stepIndex: 0,
      runId: 'run_1',
      payload: {
        toolUseId: 'client_tool_1',
        toolName: 'ClientTool',
        toolInput: {},
        frontendHint: 'client_tool_alias',
        status: 'waiting',
      },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.status).toBe('waiting_client_tool');
    const tc = state.steps[0].toolCalls[0];
    expect(tc.status).toBe('waiting');
    expect(tc.executedBy).toBe('client');
    expect(tc.frontendHint).toBe('client_tool_alias');
  });

  it('should handle client_tool_use_end event', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'client_tool_use_start',
      seq: 1,
      stepIndex: 0,
      runId: 'run_1',
      payload: {
        toolUseId: 'call_d64bac195d6241df91f3681c',
        toolName: 'tool_cc878eae37504ccfae91c66af1bc4847',
        toolInput: {},
        description: '请你吃饭',
        frontendHint: 'tool_cc878eae37504ccfae91c66af1bc4847',
        status: 'waiting',
      },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'client_tool_use_end',
      seq: 2,
      stepIndex: 0,
      runId: 'run_1',
      payload: {
        toolOutputs: [
          {
            toolUseId: 'call_d64bac195d6241df91f3681c',
            content: 'Tool not registered: tool_cc878eae37504ccfae91c66af1bc4847',
            meta: { panel: { expanded: true } },
            isError: true,
          },
        ],
      },
    } as ReactEvent);

    const state = reducer.getState();
    const tc = state.steps[0].toolCalls[0];
    expect(tc.status).toBe('error');
    expect(tc.result).toBe('Tool not registered: tool_cc878eae37504ccfae91c66af1bc4847');
    expect(tc.meta).toEqual({ panel: { expanded: true } });
    expect(tc.isError).toBe(true);
  });

  it('should handle done event and accumulate usage', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({
      type: 'done',
      seq: 2,
      payload: {
        inputTokens: 1000,
        outputTokens: 500,
        cacheReadTokens: 200,
        cacheCreateTokens: 100,
      },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.status).toBe('done');
    expect(state.currentRunId).toBe('run_1');
    expect(state.usage.totalInputTokens).toBe(1000);
    expect(state.usage.totalOutputTokens).toBe(500);
    expect(state.usage.totalCacheReadTokens).toBe(200);
    expect(state.usage.totalCacheCreateTokens).toBe(100);
    expect(state.usage.runCount).toBe(1);
  });

  it('should publish the terminal run id to subscribers', () => {
    const reducer = new EventReducer();
    const snapshots: ReturnType<EventReducer['getState']>[] = [];
    reducer.subscribe((state) => snapshots.push(state));

    reducer.applyEvent({
      type: 'run',
      seq: 1,
      runId: 'run_terminal',
      sessionId: 'session_terminal',
      payload: { userPrompt: '问题' },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'done',
      seq: 2,
      runId: 'run_terminal',
      sessionId: 'session_terminal',
      payload: {},
    } as ReactEvent);

    expect(snapshots[snapshots.length - 1]).toMatchObject({
      status: 'done',
      currentRunId: 'run_terminal',
    });
  });

  it('should accumulate usage across multiple runs', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'done',
      seq: 1,
      payload: { inputTokens: 100, outputTokens: 50, cacheReadTokens: 10, cacheCreateTokens: 5 },
    } as ReactEvent);
    reducer.resetForNewRun();
    reducer.applyEvent({
      type: 'done',
      seq: 1,
      payload: { inputTokens: 200, outputTokens: 100, cacheReadTokens: 20, cacheCreateTokens: 10 },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.usage.totalInputTokens).toBe(300);
    expect(state.usage.totalOutputTokens).toBe(150);
    expect(state.usage.runCount).toBe(2);
  });

  it('should capture lastRunStats on done event', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'done',
      seq: 1,
      payload: {
        inputTokens: 2000,
        outputTokens: 500,
        cacheReadTokens: 1500,
        cacheCreateTokens: 100,
        contextUsedTokens: 32000,
        maxContextTokens: 170000,
      },
    } as ReactEvent);

    const stats = reducer.getState().lastRunStats;
    expect(stats).not.toBeNull();
    expect(stats?.inputTokens).toBe(2000);
    expect(stats?.cacheReadTokens).toBe(1500);
    expect(stats?.contextUsedTokens).toBe(32000);
    expect(stats?.maxContextTokens).toBe(170000);
  });

  it('should update lastRunStats from thought_end token fields', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({
      type: 'thought_end',
      seq: 2,
      stepIndex: 0,
      runId: 'run_1',
      payload: {
        content: '思考完毕',
        inputTokens: 900,
        cacheReadTokens: 600,
        contextUsedTokens: 12000,
        maxContextTokens: 170000,
      },
    } as ReactEvent);

    const stats = reducer.getState().lastRunStats;
    expect(stats?.inputTokens).toBe(900);
    expect(stats?.cacheReadTokens).toBe(600);
    expect(stats?.contextUsedTokens).toBe(12000);
    expect(stats?.maxContextTokens).toBe(170000);
  });

  it('should update lastRunStats from content_end token fields (live and history)', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'content_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({
      type: 'content_end',
      seq: 2,
      stepIndex: 0,
      runId: 'run_1',
      payload: {
        content: '阶段回答',
        inputTokens: 1800,
        outputTokens: 400,
        cacheReadTokens: 1200,
        cacheCreateTokens: 50,
        contextUsedTokens: 25000,
        maxContextTokens: 170000,
      },
    } as ReactEvent);

    const stats = reducer.getState().lastRunStats;
    expect(stats?.inputTokens).toBe(1800);
    expect(stats?.cacheReadTokens).toBe(1200);
    expect(stats?.contextUsedTokens).toBe(25000);
    expect(stats?.maxContextTokens).toBe(170000);
  });

  it('should not overwrite lastRunStats from a content_end that carries no token fields', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'content_end',
      seq: 1,
      stepIndex: 0,
      runId: 'run_1',
      payload: {
        content: '阶段回答',
        inputTokens: 1800,
        cacheReadTokens: 1200,
        contextUsedTokens: 25000,
        maxContextTokens: 170000,
      },
    } as ReactEvent);
    // 兜底：万一某个 content_end 没带 token，只带 content，不应把已有指标清零
    reducer.applyEvent({
      type: 'content_end',
      seq: 2,
      stepIndex: 1,
      runId: 'run_1',
      payload: { content: '无 token 的回答' },
    } as ReactEvent);

    const stats = reducer.getState().lastRunStats;
    expect(stats?.contextUsedTokens).toBe(25000);
    expect(stats?.maxContextTokens).toBe(170000);
    expect(stats?.inputTokens).toBe(1800);
  });

  it('should only update context window on error, keeping previous cache stats', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'done',
      seq: 1,
      payload: {
        inputTokens: 2000,
        outputTokens: 500,
        cacheReadTokens: 1500,
        cacheCreateTokens: 100,
        contextUsedTokens: 32000,
        maxContextTokens: 170000,
      },
    } as ReactEvent);
    reducer.resetForNewRun();
    reducer.applyEvent({
      type: 'error',
      seq: 2,
      payload: { errNo: 500, errMsg: '出错了', contextUsedTokens: 40000, maxContextTokens: 170000 },
    } as ReactEvent);

    const stats = reducer.getState().lastRunStats;
    expect(stats?.contextUsedTokens).toBe(40000);
    expect(stats?.maxContextTokens).toBe(170000);
    // input/cache 沿用上一轮成功 run 的值
    expect(stats?.inputTokens).toBe(2000);
    expect(stats?.cacheReadTokens).toBe(1500);
  });

  it('should only update context window on cancelled, keeping previous cache stats', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'done',
      seq: 1,
      payload: {
        inputTokens: 1000,
        outputTokens: 300,
        cacheReadTokens: 800,
        cacheCreateTokens: 50,
        contextUsedTokens: 10000,
        maxContextTokens: 170000,
      },
    } as ReactEvent);
    reducer.resetForNewRun();
    reducer.applyEvent({
      type: 'cancelled',
      seq: 2,
      payload: { ok: true, reason: 'cancelled_by_user', contextUsedTokens: 15000, maxContextTokens: 170000 },
    } as ReactEvent);

    const stats = reducer.getState().lastRunStats;
    expect(stats?.contextUsedTokens).toBe(15000);
    expect(stats?.inputTokens).toBe(1000);
    expect(stats?.cacheReadTokens).toBe(800);
  });

  it('should leave lastRunStats null when error carries no context window', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'error',
      seq: 1,
      payload: { errNo: 500, errMsg: '出错了' },
    } as ReactEvent);

    expect(reducer.getState().lastRunStats).toBeNull();
  });

  it('should handle error event', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'error',
      seq: 1,
      payload: { errNo: 500, errMsg: 'Internal server error' },
    } as ReactEvent);

    // status 切到 error 让输入框回到可发送态，但后端原始错误串不进聊天流
    const state = reducer.getState();
    expect(state.status).toBe('error');
    expect(state.steps).toHaveLength(0);
  });

  it('断连导致的 run 失败同样不留下任何消息', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'error',
      seq: 1,
      payload: { errNo: 500, errMsg: 'client disconnected' },
    } as ReactEvent);

    expect(reducer.getState().steps).toHaveLength(0);
  });

  it('自动续跑的 run 事件不渲染用户消息，普通 run 照常渲染', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'run',
      seq: 1,
      runId: 'run_1',
      payload: { userPrompt: '继续', controlContext: { recoveryContinue: true } },
    } as unknown as ReactEvent);

    expect(reducer.getState().steps).toHaveLength(0);

    reducer.applyEvent({
      type: 'run',
      seq: 1,
      runId: 'run_2',
      payload: { userPrompt: '继续' },
    } as unknown as ReactEvent);

    const steps = reducer.getState().steps;
    expect(steps).toHaveLength(1);
    expect(steps[0].role).toBe('user');
    expect(steps[0].content).toBe('继续');
  });

  it('should handle cancelled event', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 0,
      runId: 'run_1',
      stepIndex: 0,
      payload: {
        toolUseId: 'tool_1',
        toolName: 'ask_question',
        toolInput: {},
        executedBy: 'internal',
        status: 'waiting',
      },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'cancelled',
      seq: 1,
      payload: { ok: true, reason: 'cancelled_by_user' },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.status).toBe('cancelled');
    expect(state.currentRunId).toBe('run_1');
    expect(state.steps[0].toolCalls[0].status).toBe('cancelled');
    const lastStep = state.steps[state.steps.length - 1];
    expect(lastStep.role).toBe('assistant');
    expect(lastStep.isError).toBe(true);
    expect(lastStep.content).toBe('cancelled_by_user');
  });

  it('should settle pending tool calls on error', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 1,
      runId: 'run_1',
      stepIndex: 0,
      payload: {
        toolUseId: 'tool_1',
        toolName: 'ask_question',
        toolInput: {},
        executedBy: 'internal',
        status: 'waiting',
      },
    } as ReactEvent);

    reducer.applyEvent({
      type: 'error',
      seq: 2,
      payload: { errMsg: 'no active react run' },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.status).toBe('error');
    expect(state.currentRunId).toBe('run_1');
    expect(state.steps[0].toolCalls[0].status).toBe('cancelled');
  });

  it('should mark an in-progress run as interrupted', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    expect(reducer.getState().status).toBe('running');

    reducer.markRunInterrupted('连接已断开，本次回答已中断');

    const state = reducer.getState();
    expect(state.status).toBe('error');
    expect(state.currentRunId).toBe('run_1');
    const lastStep = state.steps[state.steps.length - 1];
    expect(lastStep.role).toBe('assistant');
    expect(lastStep.isError).toBe(true);
    expect(lastStep.content).toBe('连接已断开，本次回答已中断');
  });

  it('should mark interrupted while waiting for a client tool', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'client_tool_use_start',
      seq: 1,
      stepIndex: 0,
      runId: 'run_1',
      payload: { toolUseId: 't1', toolName: 'ClientTool', toolInput: {}, status: 'waiting' },
    } as ReactEvent);
    expect(reducer.getState().status).toBe('waiting_client_tool');

    reducer.markRunInterrupted('连接已断开，本次回答已中断');
    const state = reducer.getState();
    expect(state.status).toBe('error');
    expect(state.currentRunId).toBe('run_1');
    expect(state.steps[0].toolCalls[0].status).toBe('cancelled');
  });

  it('should be a no-op when marking interrupted while not running', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'done',
      seq: 1,
      payload: { inputTokens: 100, outputTokens: 50, cacheReadTokens: 0, cacheCreateTokens: 0 },
    } as ReactEvent);
    expect(reducer.getState().status).toBe('done');

    const stepsBefore = reducer.getState().steps.length;
    reducer.markRunInterrupted('连接已断开，本次回答已中断');

    const state = reducer.getState();
    expect(state.status).toBe('done');
    expect(state.steps).toHaveLength(stepsBefore);
  });

  it('should handle compact_start and compact_end events', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'compact_start', seq: 1, payload: { messageCount: 10, estimatedSize: 5000 } } as ReactEvent);
    expect(reducer.getState().status).toBe('compacting');

    reducer.applyEvent({
      type: 'compact_end',
      seq: 2,
      payload: { beforeMessageCount: 10, afterMessageCount: 3, summary: '压缩后的摘要' },
    } as ReactEvent);
    const state = reducer.getState();
    expect(state.status).toBe('running');
    expect(state.compactState?.beforeMessageCount).toBe(10);
    expect(state.compactState?.afterMessageCount).toBe(3);
    expect(state.compactState?.summary).toBe('压缩后的摘要');
  });

  it('should push a compact step on compact_end for the message stream', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'compact_start', seq: 1, runId: 'run_1', payload: { messageCount: 10, estimatedSize: 5000 } } as ReactEvent);
    reducer.applyEvent({
      type: 'compact_end',
      seq: 2,
      runId: 'run_1',
      payload: { beforeMessageCount: 10, afterMessageCount: 3, summary: '压缩后的摘要' },
    } as ReactEvent);

    const state = reducer.getState();
    const compactSteps = state.steps.filter((step) => step.role === 'compact');
    expect(compactSteps).toHaveLength(1);
    expect(compactSteps[0].compact?.summary).toBe('压缩后的摘要');
    expect(compactSteps[0].compact?.beforeMessageCount).toBe(10);
    expect(compactSteps[0].compact?.afterMessageCount).toBe(3);
  });

  it('should handle todo_update event', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'todo_update',
      seq: 1,
      payload: {
        merge: false,
        todoState: {
          items: [
            { id: '1', content: '任务1', status: 'pending', createdAt: 1000, updatedAt: 1000 },
            { id: '2', content: '任务2', status: 'pending', createdAt: 1000, updatedAt: 1000 },
          ],
        },
      },
    } as ReactEvent);

    reducer.applyEvent({
      type: 'todo_update',
      seq: 2,
      payload: {
        merge: true,
        todos: [
          { id: '1', content: '任务1', status: 'completed', createdAt: 1000, updatedAt: 2000 },
        ],
      },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.todos).toHaveLength(2);
    expect(state.todos[0].content).toBe('任务1');
    expect(state.todos[0].status).toBe('completed');
    expect(state.todos[1].status).toBe('pending');
  });

  it('should sync todo_update state into the latest full Todo card', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 1,
      stepIndex: 0,
      runId: 'run_1',
      payload: {
        toolUseId: 'todo_create',
        toolName: 'todo_write',
        toolInput: {
          merge: false,
          todos: [
            { id: '1', content: '任务1', status: 'pending' },
            { id: '2', content: '任务2', status: 'pending' },
          ],
        },
        executedBy: 'internal',
        status: 'running',
      },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'todo_update',
      seq: 2,
      payload: {
        todoState: {
          items: [
            { id: '1', content: '任务1', status: 'pending', createdAt: 1000, updatedAt: 1000 },
            { id: '2', content: '任务2', status: 'pending', createdAt: 1000, updatedAt: 1000 },
          ],
        },
      },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 3,
      stepIndex: 1,
      runId: 'run_1',
      payload: {
        toolUseId: 'todo_progress',
        toolName: 'todo_write',
        toolInput: {
          merge: true,
          todos: [{ id: '1', status: 'in_progress' }],
        },
        executedBy: 'internal',
        status: 'running',
      },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'todo_update',
      seq: 4,
      payload: {
        todoState: {
          items: [
            { id: '1', content: '任务1', status: 'in_progress', createdAt: 1000, updatedAt: 2000 },
            { id: '2', content: '任务2', status: 'pending', createdAt: 1000, updatedAt: 1000 },
          ],
        },
      },
    } as ReactEvent);

    const state = reducer.getState();
    const fullCard = state.steps[0].toolCalls[0];
    const progressCard = state.steps[1].toolCalls[0];
    expect(state.todos.map((todo) => todo.status)).toEqual(['in_progress', 'pending']);
    expect(fullCard.todoItems?.map((todo) => todo.status)).toEqual(['in_progress', 'pending']);
    expect(progressCard.todoItems).toBeUndefined();

    if (fullCard.todoItems) fullCard.todoItems[0].status = 'cancelled';
    expect(reducer.getState().steps[0].toolCalls[0].todoItems?.[0].status).toBe('in_progress');
  });

  it('should replace todo list when todo_update merge is false', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'todo_update',
      seq: 1,
      payload: {
        merge: false,
        todos: [
          { id: '1', content: '任务1', status: 'pending', createdAt: 1000, updatedAt: 1000 },
        ],
      },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'todo_update',
      seq: 2,
      payload: {
        merge: false,
        todos: [
          { id: '2', content: '任务2', status: 'in_progress', createdAt: 2000, updatedAt: 2000 },
        ],
      },
    } as ReactEvent);

    const state = reducer.getState();
    expect(state.todos).toHaveLength(1);
    expect(state.todos[0].id).toBe('2');
    expect(state.todos[0].status).toBe('in_progress');
  });

  it('should handle multiple steps', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({ type: 'thought_end', seq: 2, stepIndex: 0, payload: { content: 'Step 0' } } as ReactEvent);
    reducer.applyEvent({ type: 'thought_start', seq: 3, stepIndex: 1, runId: 'run_1' });
    reducer.applyEvent({ type: 'thought_end', seq: 4, stepIndex: 1, payload: { content: 'Step 1' } } as ReactEvent);

    const state = reducer.getState();
    expect(state.steps).toHaveLength(2);
    expect(state.steps[0].thoughts).toBe('Step 0');
    expect(state.steps[1].thoughts).toBe('Step 1');
  });

  it('should notify subscribers on state change', () => {
    const reducer = new EventReducer();
    const states: any[] = [];
    reducer.subscribe((s) => states.push(s));

    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({
      type: 'thought_delta',
      seq: 2,
      stepIndex: 0,
      payload: { contentDelta: 'test' },
    } as ReactEvent);

    expect(states).toHaveLength(2);
    expect(states[1].steps[0].thoughts).toBe('test');
  });

  it('should unsubscribe correctly', () => {
    const reducer = new EventReducer();
    const states: any[] = [];
    const unsub = reducer.subscribe((s) => states.push(s));

    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    unsub();
    reducer.applyEvent({ type: 'content_start', seq: 2, stepIndex: 0, runId: 'run_1' });

    expect(states).toHaveLength(1);
  });

  it('should replayEvents correctly', () => {
    const reducer = new EventReducer();
    const events: ReactEvent[] = [
      { type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1', sessionId: 'session_1' },
      { type: 'thought_delta', seq: 2, stepIndex: 0, payload: { contentDelta: '历史思考' } } as ReactEvent,
      { type: 'thought_end', seq: 3, stepIndex: 0, payload: { content: '历史思考' } } as ReactEvent,
      { type: 'done', seq: 4, payload: { inputTokens: 100, outputTokens: 50, cacheReadTokens: 0, cacheCreateTokens: 0 } } as ReactEvent,
    ];

    reducer.replayEvents(events);
    const state = reducer.getState();
    expect(state.status).toBe('idle');
    expect(state.sessionId).toBe('session_1');
    expect(state.steps[0].thoughts).toBe('历史思考');
    expect(state.usage.totalInputTokens).toBe(100);
  });

  it('should resetForNewRun without clearing visible conversation', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1', sessionId: 'session_1' });
    reducer.applyEvent({
      type: 'done',
      seq: 2,
      payload: { inputTokens: 100, outputTokens: 50, cacheReadTokens: 0, cacheCreateTokens: 0 },
    } as ReactEvent);

    reducer.resetForNewRun();
    const state = reducer.getState();
    expect(state.status).toBe('running');
    expect(state.steps).toHaveLength(1);
    expect(state.steps[0].runId).toBe('run_1');
    expect(state.sessionId).toBe('session_1');
    expect(state.currentRunId).toBeNull();
    expect(state.usage.totalInputTokens).toBe(100);
  });

  it('should append a new run without overwriting previous assistant content', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'run', seq: 1, stepIndex: 0, runId: 'run_1', sessionId: 'session_1', payload: { userPrompt: '第一问' } } as ReactEvent);
    reducer.applyEvent({ type: 'content_start', seq: 2, stepIndex: 0, runId: 'run_1', sessionId: 'session_1' });
    reducer.applyEvent({ type: 'content_end', seq: 3, stepIndex: 0, runId: 'run_1', sessionId: 'session_1', payload: { content: '第一答' } } as ReactEvent);
    reducer.applyEvent({ type: 'done', seq: 4, runId: 'run_1', sessionId: 'session_1', payload: {} } as ReactEvent);

    reducer.resetForNewRun();
    reducer.addUserStep('第二问');
    reducer.applyEvent({ type: 'content_start', seq: 1, stepIndex: 0, runId: 'run_2', sessionId: 'session_1' });
    reducer.applyEvent({ type: 'content_end', seq: 2, stepIndex: 0, runId: 'run_2', sessionId: 'session_1', payload: { content: '第二答' } } as ReactEvent);

    const state = reducer.getState();
    expect(state.steps.map((step) => step.content)).toEqual(['第一问', '第一答', '第二问', '第二答']);
    expect(state.steps[1].runId).toBe('run_1');
    expect(state.steps[3].runId).toBe('run_2');
  });

  it('should move the latest plan from pending to submitting and restore it after send failure', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 1,
      stepIndex: 0,
      runId: 'run_plan',
      payload: {
        toolUseId: 'tool_plan',
        toolName: 'create_plan',
        toolInput: { title: '实施计划', steps: [{ id: 'a', outline: '检查实现', message: '检查代码' }] },
        executedBy: 'internal',
        status: 'running',
      },
    } as ReactEvent);

    expect(reducer.getState().steps[0].toolCalls[0].planConfirmationStatus).toBe('pending');
    reducer.markPendingPlanSubmitting();
    expect(reducer.getState().steps[0].toolCalls[0].planConfirmationStatus).toBe('submitting');
    reducer.markSubmittingPlanSendFailed();
    expect(reducer.getState().steps[0].toolCalls[0].planConfirmationStatus).toBe('pending');
  });

  it('should permanently accept a plan after any event from a later run', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 1,
      stepIndex: 0,
      runId: 'run_plan',
      payload: {
        toolUseId: 'tool_plan',
        toolName: 'create_plan',
        toolInput: { title: '实施计划', steps: [{ id: 'a', outline: '检查实现', message: '检查代码' }] },
        executedBy: 'internal',
        status: 'running',
      },
    } as ReactEvent);
    reducer.markPendingPlanSubmitting();
    reducer.applyEvent({
      type: 'run',
      seq: 1,
      runId: 'run_confirm',
      sessionId: 'session_1',
      payload: { userPrompt: '开始任务' },
    } as ReactEvent);
    reducer.applyEvent({
      type: 'error',
      seq: 2,
      runId: 'run_confirm',
      sessionId: 'session_1',
      payload: { errNo: 5000, errMsg: '执行失败' },
    } as ReactEvent);

    expect(reducer.getState().steps[0].toolCalls[0].planConfirmationStatus).toBe('accepted');
  });

  it('should recover accepted plan state while replaying history', () => {
    const reducer = new EventReducer();
    reducer.replayEvents([
      {
        type: 'tool_use_start',
        seq: 1,
        stepIndex: 0,
        runId: 'run_plan',
        payload: {
          toolUseId: 'tool_plan',
          toolName: 'create_plan',
          toolInput: { title: '实施计划', steps: [{ id: 'a', outline: '检查实现', message: '检查代码' }] },
          executedBy: 'internal',
          status: 'running',
        },
      },
      {
        type: 'run',
        seq: 1,
        runId: 'run_later',
        sessionId: 'session_1',
        payload: { userPrompt: '讨论一下' },
      },
    ] as ReactEvent[]);

    expect(reducer.getState().steps[0].toolCalls[0].planConfirmationStatus).toBe('accepted');
  });

  it('should clear conversation and local run state', () => {
    const reducer = new EventReducer();
    reducer.setConnected(true);
    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1', sessionId: 'session_1' });
    reducer.applyEvent({
      type: 'done',
      seq: 2,
      payload: { inputTokens: 100, outputTokens: 50, cacheReadTokens: 0, cacheCreateTokens: 0 },
    } as ReactEvent);

    reducer.clearConversation();
    const state = reducer.getState();
    expect(state.connected).toBe(true);
    expect(state.status).toBe('idle');
    expect(state.sessionId).toBeNull();
    expect(state.currentRunId).toBeNull();
    expect(state.steps).toHaveLength(0);
    expect(state.todos).toHaveLength(0);
    expect(state.usage.totalInputTokens).toBe(0);
  });

  it('should expose immutable state snapshots', () => {
    const reducer = new EventReducer();
    const states: any[] = [];
    reducer.subscribe((s) => states.push(s));

    const initialState = reducer.getState();
    reducer.setConnected(true);
    reducer.applyEvent({ type: 'thought_start', seq: 1, stepIndex: 0, runId: 'run_1' });
    reducer.applyEvent({
      type: 'thought_delta',
      seq: 2,
      stepIndex: 0,
      payload: { contentDelta: 'test' },
    } as ReactEvent);

    expect(initialState.connected).toBe(false);
    expect(initialState.steps).toHaveLength(0);
    expect(states[0].connected).toBe(true);
    expect(states[0].steps).toHaveLength(0);
    expect(states[1].steps).toHaveLength(1);
    expect(states[1].steps[0].thoughts).toBe('');
    expect(states[2].steps[0].thoughts).toBe('test');
    expect(states[1]).not.toBe(states[2]);
  });

  it('should reset the failed streaming output when model fallback requests it', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'content_start', seq: 1, runId: 'run_1', stepIndex: 0 });
    reducer.applyEvent({ type: 'content_delta', seq: 2, runId: 'run_1', stepIndex: 0, payload: { contentDelta: '失败模型半截输出' } });
    reducer.applyEvent({
      type: 'model_fallback',
      seq: 3,
      runId: 'run_1',
      stepIndex: 0,
      payload: {
        fromModelKey: 'DeepSeek',
        fromModelVersion: 'deepseek-v4-pro',
        toModelKey: '通义千问',
        toModelVersion: 'qwen3.8-max',
        reason: 'stream_idle_timeout',
        resetCurrentOutput: true,
      },
    });

    const state = reducer.getState();
    expect(state.steps[0].content).toBe('');
    expect(state.steps[0].contentStarted).toBe(false);
    expect(state.lastModelFallback?.toModelVersion).toBe('qwen3.8-max');
  });

  it('should ignore heartbeat event', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({ type: 'heartbeat', seq: 1, payload: { ts: 1234567890 } } as ReactEvent);
    const state = reducer.getState();
    expect(state.status).toBe('idle');
  });
});
// Additional tests for addUserStep and addErrorStep
describe('EventReducer - user and error steps', () => {
  it('should add user step with addUserStep', () => {
    const reducer = new EventReducer();
    reducer.addUserStep('你好，帮我创建一个柱状图');

    const state = reducer.getState();
    expect(state.steps).toHaveLength(1);
    expect(state.steps[0].role).toBe('user');
    expect(state.steps[0].content).toBe('你好，帮我创建一个柱状图');
    expect(state.steps[0].contentComplete).toBe(true);
  });

  it('should add error step with addErrorStep', () => {
    const reducer = new EventReducer();
    reducer.addErrorStep('网络连接失败', 'run_123');

    const state = reducer.getState();
    expect(state.steps).toHaveLength(1);
    expect(state.steps[0].role).toBe('assistant');
    expect(state.steps[0].isError).toBe(true);
    expect(state.steps[0].content).toBe('网络连接失败');
    expect(state.steps[0].runId).toBe('run_123');
  });
});
