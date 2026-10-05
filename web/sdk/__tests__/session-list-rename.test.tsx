import { afterEach, describe, expect, it, vi } from 'vitest';
import { createComponent, render } from 'solid-js/web';
import { SessionList } from '../ui/components/SessionList';
import type { SessionMeta } from '../storage/event-ledger';

/**
 * SessionList 重命名交互（行内编辑）：
 * - 点击重命名按钮进入编辑态，回显当前标题
 * - 修改后确认：回调携带新标题，退出编辑态
 * - 取消/空标题：不触发回调
 * - 未提供 onRenameSession 时不渲染重命名入口
 */

function makeSession(overrides: Partial<SessionMeta> = {}): SessionMeta {
  return {
    sessionId: 'session_s1',
    callerKey: 'caller',
    routeValues: [],
    type: 'chat',
    title: '旧标题',
    lastRunId: 'run_1',
    lastMessage: '',
    state: 'active',
    lastSeq: 3,
    eventCount: 3,
    createdAt: '2026-10-01 10:00:00',
    updatedAt: '2026-10-01 10:00:00',
    ...overrides,
  };
}

function renderList(props: Partial<Parameters<typeof SessionList>[0]> = {}) {
  const disposeFns: Array<() => void> = [];
  const ui = render(() => createComponent(SessionList, {
    sessions: [makeSession()],
    activeSessionId: null,
    onSelectSession: () => {},
    onNewSession: () => {},
    ...props,
  } as Parameters<typeof SessionList>[0]), document.body);
  // render 返回清理函数；收集以便 afterEach 统一销毁
  disposeFns.push(ui);
  return () => disposeFns.forEach((fn) => fn());
}

afterEach(() => {
  document.body.innerHTML = '';
});

function querySelector(selector: string): HTMLElement | null {
  return document.body.querySelector(selector);
}

describe('SessionList 重命名', () => {
  it('未提供 onRenameSession 时不渲染重命名入口', () => {
    const dispose = renderList();
    expect(querySelector('.agent-ui-session-rename-button')).toBeNull();
    dispose();
  });

  it('进入编辑态回显当前标题，确认后携带新标题回调', async () => {
    const onRenameSession = vi.fn(async () => {});
    const dispose = renderList({ onRenameSession });

    (querySelector('.agent-ui-session-rename-button') as HTMLButtonElement).click();
    const input = querySelector('.agent-ui-session-rename-input') as HTMLInputElement;
    expect(input).toBeTruthy();
    expect(input.value).toBe('旧标题');

    input.value = '新标题';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    (querySelector('.agent-ui-session-rename-confirm') as HTMLButtonElement).click();
    await Promise.resolve();

    expect(onRenameSession).toHaveBeenCalledWith('session_s1', '新标题');
    expect(querySelector('.agent-ui-session-rename-input')).toBeNull();
    dispose();
  });

  it('取消不触发回调；空白标题确认被拦截', async () => {
    const onRenameSession = vi.fn(async () => {});
    const dispose = renderList({ onRenameSession });

    // 取消
    (querySelector('.agent-ui-session-rename-button') as HTMLButtonElement).click();
    (querySelector('.agent-ui-session-rename-cancel') as HTMLButtonElement).click();
    expect(querySelector('.agent-ui-session-rename-input')).toBeNull();

    // 清空标题后确认按钮禁用，不触发回调
    (querySelector('.agent-ui-session-rename-button') as HTMLButtonElement).click();
    const input = querySelector('.agent-ui-session-rename-input') as HTMLInputElement;
    input.value = '   ';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    const confirmBtn = querySelector('.agent-ui-session-rename-confirm') as HTMLButtonElement;
    expect(confirmBtn.disabled).toBe(true);

    expect(onRenameSession).not.toHaveBeenCalled();
    dispose();
  });
});
