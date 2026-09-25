import { describe, expect, it } from 'vitest';
import type { ReactEvent } from '../protocol/types';
import { EventReducer } from '../runtime/event-reducer';

const ev = (o: Partial<ReactEvent>): ReactEvent =>
  ({ type: 'run', seq: 1, sessionId: 's1', runId: 'r1', ...o } as ReactEvent);

describe('steering / notice 事件投影（S1-S3/Q1-A1）', () => {
  it('steer_queued 更新 steerState 投影并生成 notice step', () => {
    const reducer = new EventReducer();
    reducer.applyEvent(
      ev({ type: 'steer_queued', seq: 2, payload: { kind: 'queued', pendingInputId: 'pend_1', queueLength: 2 } }),
    );
    const state = reducer.getState();
    expect(state.steerState?.kind).toBe('queued');
    expect(state.steerState?.queueLength).toBe(2);
    const notice = state.steps.at(-1);
    expect(notice?.role).toBe('notice');
    expect(notice?.notice?.text).toContain('队列');
    expect(notice?.notice?.text).toContain('2');
    // 系统标记强制落 main lane：不携带 agentPath。
    expect(notice?.agentPath).toBeUndefined();
  });

  it('steer_rejected 文案包含拒绝原因', () => {
    const reducer = new EventReducer();
    reducer.applyEvent(
      ev({ type: 'steer_rejected', seq: 2, payload: { kind: 'rejected', reason: 'run_not_steerable' } }),
    );
    const state = reducer.getState();
    expect(state.steerState?.kind).toBe('rejected');
    expect(state.steps.at(-1)?.notice?.text).toContain('等待你的输入');
  });

  it('steer_drained 与 steer_discarded 生成系统标记并更新投影', () => {
    const reducer = new EventReducer();
    reducer.applyEvent(ev({ type: 'steer_drained', seq: 2, payload: { pendingInputId: 'pend_1', messageId: 'msg_1' } }));
    reducer.applyEvent(ev({ type: 'steer_discarded', seq: 3, payload: { count: 2, reason: 'turn_cancelled' } }));
    const state = reducer.getState();
    expect(state.steerState?.kind).toBe('discarded');
    const notices = state.steps.filter((step) => step.role === 'notice');
    expect(notices).toHaveLength(2);
    expect(notices[0]?.notice?.text).toContain('pend_1');
    expect(notices[1]?.notice?.text).toContain('2 条');
  });

  it('notice_drained 实时事件（无 content）生成通知标记', () => {
    const reducer = new EventReducer();
    reducer.applyEvent(ev({ type: 'notice_drained', seq: 2, payload: { messageId: 'msg_9', count: 1 } }));
    const state = reducer.getState();
    expect(state.steerState?.kind).toBe('notice');
    const notice = state.steps.at(-1);
    expect(notice?.role).toBe('notice');
    expect(notice?.notice?.text).toContain('后台任务通知已送达');
    expect(notice?.notice?.detail).toBeUndefined();
  });

  it('历史回放：notice_drained（带 content）走 replayEvents 同样渲染，子 lane 事件也落 main', () => {
    const reducer = new EventReducer();
    reducer.replayEvents([
      ev({ payload: { userPrompt: 'hi' } }),
      ev({ type: 'notice_drained', seq: 2, agentPath: 'main/echo-agent', payload: { messageId: 'm1', count: 1, content: '<task-notification>...</task-notification>' } }),
    ]);
    const notice = reducer.getState().steps.at(-1);
    expect(notice?.role).toBe('notice');
    expect(notice?.agentPath).toBeUndefined();
    expect(notice?.notice?.detail).toContain('<task-notification>');
  });

  it('steer_guided 生成引导提示标记', () => {
    const reducer = new EventReducer();
    reducer.applyEvent(
      ev({ type: 'steer_guided', seq: 2, payload: { kind: 'guided', pendingInputId: 'pend_2', queueLength: 0 } }),
    );
    const notice = reducer.getState().steps.at(-1);
    expect(notice?.role).toBe('notice');
    expect(notice?.notice?.text).toContain('引导注入');
  });
});
