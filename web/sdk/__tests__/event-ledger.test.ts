/**
 * EventLedger 测试
 *
 * 使用 jsdom 环境中内置的 IndexedDB fake（由 vitest 配置提供）。
 * 如果没有内置 IndexedDB，则使用 fake-indexeddb polyfill。
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { ReactEvent } from '../protocol/types';
import type { HistoryEvent, SessionItem } from '../session/types';
import { EventLedger } from '../storage/event-ledger';

import 'fake-indexeddb/auto';

describe('EventLedger', () => {
  let ledger: EventLedger;

  beforeEach(() => {
    ledger = new EventLedger();
  });

  afterEach(async () => {
    await ledger.close();
    indexedDB.deleteDatabase('agent-sdk-ledger');
  });

  function makeEvent(seq: number, sessionId: string, type: string = 'content_end', runId: string = 'run_1'): ReactEvent {
    return {
      type: type as any,
      seq,
      sessionId,
      runId,
      stepIndex: 0,
      payload: type === 'content_end' ? { content: `content_${seq}` } : { contentDelta: `chunk_${seq}` },
    };
  }

  it('should append complete local events into one session events array', async () => {
    const sid = 'session_1';
    ledger.append(makeEvent(99, sid));
    ledger.append(makeEvent(100, sid));

    await new Promise((r) => setTimeout(r, 100));

    const events = await ledger.getEvents(sid);
    expect(events).toHaveLength(2);
    expect(events.map((event) => event.seq)).toEqual([1, 2]);
    expect(events.every((event) => !('source' in event))).toBe(true);
    expect(events.every((event) => !('cachedAt' in event))).toBe(true);
  });

  it('should append a complete event group with local /session/events style seq', async () => {
    const sid = 'session_group';
    ledger.appendEvents([
      { type: 'content_start', seq: 10, sessionId: sid, runId: 'run_1', stepIndex: 0 },
      { type: 'content_delta', seq: 11, sessionId: sid, runId: 'run_1', stepIndex: 0, payload: { contentDelta: '完整回答' } },
      { type: 'content_end', seq: 12, sessionId: sid, runId: 'run_1', stepIndex: 0, payload: { content: '完整回答' } },
    ] as ReactEvent[]);

    await new Promise((r) => setTimeout(r, 100));

    const events = await ledger.getEvents(sid);
    expect(events.map((event) => event.type)).toEqual(['content_start', 'content_delta', 'content_end']);
    expect(events.map((event) => event.seq)).toEqual([1, 2, 3]);
    expect(events[1].payload).toEqual({ contentDelta: '完整回答' });
  });

  it('should overwrite local events when server session events arrive', async () => {
    const sid = 'session_overwrite';
    ledger.append(makeEvent(1, sid, 'content_end', 'run_local'));
    await new Promise((r) => setTimeout(r, 100));

    const serverEvents: HistoryEvent[] = [
      {
        type: 'run',
        seq: 1,
        runId: 'run_server',
        sessionId: sid,
        payload: { userPrompt: '历史问题', type: 'chat', appKey: 'report-editor' },
        createdAt: '2026-03-14 10:00:00',
      },
      {
        type: 'content_end',
        seq: 2,
        runId: 'run_server',
        sessionId: sid,
        stepIndex: 0,
        payload: { content: '历史回答' },
        createdAt: '2026-03-14 10:00:01',
      },
    ];

    await ledger.replaceSessionEvents(sid, serverEvents, {
      sessionId: sid,
      appKey: 'report-editor',
      routeValues: ['report_abc'],
      title: '历史会话',
    });

    const events = await ledger.getEvents(sid);
    expect(events).toEqual(serverEvents);
    expect(events.some((event) => event.runId === 'run_local')).toBe(false);

    const meta = await ledger.getSession(sid);
    expect(meta!.title).toBe('历史会话');
    expect(meta!.lastSeq).toBe(2);
    expect(meta!.eventCount).toBe(2);
  });

  it('should append local complete events after server overwrite until next overwrite', async () => {
    const sid = 'session_append_after_server';
    await ledger.replaceSessionEvents(sid, [
      { type: 'run', seq: 1, runId: 'run_1', sessionId: sid, payload: { userPrompt: '问题' } },
    ]);

    ledger.append({ type: 'content_end', seq: 99, runId: 'run_1', sessionId: sid, stepIndex: 0, payload: { content: '回答' } });
    await new Promise((r) => setTimeout(r, 100));

    const events = await ledger.getEvents(sid);
    expect(events.map((event) => event.seq)).toEqual([1, 2]);
    expect(events[1].payload).toEqual({ content: '回答' });
  });

  it('should return empty array for unknown session', async () => {
    const events = await ledger.getEvents('unknown');
    expect(events).toHaveLength(0);
  });

  it('should track lastSeq from the local events array', async () => {
    const sid = 'session_1';
    ledger.append(makeEvent(10, sid));
    ledger.append(makeEvent(20, sid));

    await new Promise((r) => setTimeout(r, 100));

    const lastSeq = await ledger.getLastSeq(sid);
    expect(lastSeq).toBe(2);
  });

  it('should upsertSession and getSession', async () => {
    await ledger.upsertSession({
      sessionId: 'session_1',
      appKey: 'report-editor',
      routeValues: ['report_abc'],
      title: 'Test Session',
    });

    const meta = await ledger.getSession('session_1');
    expect(meta).not.toBeNull();
    expect(meta!.appKey).toBe('report-editor');
    expect(meta!.title).toBe('Test Session');
    expect(meta!.type).toBe('chat');
    expect(meta!.state).toBe('active');
  });

  it('should upsert server sessions with backend fields', async () => {
    const sessions: SessionItem[] = [
      {
        sessionId: 'server_session_1',
        appKey: 'report-editor',
        routeValues: ['report_abc'],
        type: 'chat',
        title: 'Server Session',
        lastRunId: 'run_1',
        lastMessage: '最新回复摘要',
        state: 'active',
        createdAt: '2026-03-14 10:00:00',
        updatedAt: '2026-03-14 10:01:00',
      },
    ];

    await ledger.upsertServerSessions(sessions);

    const meta = await ledger.getSession('server_session_1');
    expect(meta).not.toBeNull();
    expect(meta!.type).toBe('chat');
    expect(meta!.lastRunId).toBe('run_1');
    expect(meta!.lastMessage).toBe('最新回复摘要');
    expect(meta!.state).toBe('active');
    expect(meta!.createdAt).toBe('2026-03-14 10:00:00');
    expect(meta!.updatedAt).toBe('2026-03-14 10:01:00');
  });

  it('should getLatestSession by appKey', async () => {
    await ledger.upsertSession({ sessionId: 's1', appKey: 'report-editor', routeValues: ['r1'] });
    await new Promise((r) => setTimeout(r, 50));
    await ledger.upsertSession({ sessionId: 's2', appKey: 'report-editor', routeValues: ['r1'] });

    const latest = await ledger.getLatestSession('report-editor', ['r1']);
    expect(latest).not.toBeNull();
    expect(latest!.sessionId).toBe('s2');
  });

  it('should listSessions', async () => {
    await ledger.upsertSession({ sessionId: 's1', appKey: 'a', routeValues: ['x'] });
    await ledger.upsertSession({ sessionId: 's2', appKey: 'a', routeValues: ['x'] });
    await ledger.upsertSession({ sessionId: 's3', appKey: 'b', routeValues: ['y'] });

    const list = await ledger.listSessions('a', ['x']);
    expect(list).toHaveLength(2);
  });

  it('should clearSessions by appKey and routeValues', async () => {
    ledger.append(makeEvent(1, 's1'));
    ledger.append(makeEvent(1, 's2'));
    ledger.append(makeEvent(1, 's3'));
    await new Promise((r) => setTimeout(r, 100));

    await ledger.upsertSession({ sessionId: 's1', appKey: 'a', routeValues: ['x'] });
    await ledger.upsertSession({ sessionId: 's2', appKey: 'a', routeValues: ['x'] });
    await ledger.upsertSession({ sessionId: 's3', appKey: 'a', routeValues: ['y'] });

    await ledger.clearSessions('a', ['x']);

    expect(await ledger.listSessions('a', ['x'])).toHaveLength(0);
    expect(await ledger.getEvents('s1')).toHaveLength(0);
    expect(await ledger.getEvents('s2')).toHaveLength(0);
    expect(await ledger.getEvents('s3')).toHaveLength(1);
  });

  it('should clearSession', async () => {
    const sid = 'session_1';
    ledger.append(makeEvent(1, sid));
    ledger.append(makeEvent(2, sid));

    await new Promise((r) => setTimeout(r, 100));

    await ledger.clearSession(sid);

    const events = await ledger.getEvents(sid);
    expect(events).toHaveLength(0);

    const meta = await ledger.getSession(sid);
    expect(meta).toBeNull();
  });

  it('should auto-create session meta on append', async () => {
    const sid = 'auto_session';
    ledger.append(makeEvent(1, sid, 'content_end'));

    await new Promise((r) => setTimeout(r, 100));

    const meta = await ledger.getSession(sid);
    expect(meta).not.toBeNull();
    expect(meta!.lastSeq).toBe(1);
    expect(meta!.eventCount).toBe(1);
  });

  it('should skip events without sessionId', async () => {
    const event: ReactEvent = {
      type: 'heartbeat',
      seq: 1,
      payload: { ts: 123 },
    };

    ledger.append(event);

    await new Promise((r) => setTimeout(r, 100));
    expect(await ledger.getEvents('')).toHaveLength(0);
  });
});