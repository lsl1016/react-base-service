import { createComponent } from 'solid-js';
import { render } from 'solid-js/web';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AgentClient } from '../runtime/agent-client';
import type { AgentState } from '../runtime/types';
import type { ClientTool, ClientToolUIRenderer } from '../tools/types';
import type { AgentInputCommand } from '../ui/input-api';
import { AgentPanel } from '../ui/components/AgentPanel';

vi.mock('../ui/components/SmartScroll', () => ({
  SmartScroll: (props: { children: unknown }) => props.children,
}));
vi.mock('../ui/components/ScrollArea', () => ({
  ScrollArea: (props: { children: unknown }) => props.children,
}));

const initialState: AgentState = {
  status: 'idle',
  connected: true,
  sessionId: 'session-current',
  currentRunId: null,
  steps: [],
  todos: [],
  usage: {
    totalInputTokens: 0,
    totalOutputTokens: 0,
    totalCacheReadTokens: 0,
    totalCacheCreateTokens: 0,
    runCount: 0,
  },
  lastRunStats: null,
  compactState: null,
};

afterEach(() => {
  document.body.innerHTML = '';
});

describe('AgentPanel UI events', () => {
  // 暂时下线 create_plan，恢复工具时取消 skip。
  it.skip('renders replay state without mutation controls in read-only mode', () => {
    const readOnlyState: AgentState = {
      ...initialState,
      connected: false,
      steps: [{
        index: 0,
        runId: 'run-replay',
        role: 'assistant',
        thoughts: '',
        content: '历史回复\n[next-button:继续]',
        toolCalls: [{
          toolUseId: 'tool-plan',
          toolName: 'create_plan',
          input: {
            title: '历史计划',
            steps: [{ id: 'step-1', outline: '检查实现', message: '读取代码' }],
          },
          status: 'done',
          executedBy: 'internal',
          result: JSON.stringify({ planId: 'plan-replay' }),
          planConfirmationStatus: 'pending',
        }],
        thoughtComplete: true,
        contentStarted: true,
        contentComplete: true,
      }],
    };
    const client = {
      getState: () => readOnlyState,
      subscribe: (listener: (state: AgentState) => void) => {
        listener(readOnlyState);
        return () => undefined;
      },
      getRegisteredTool: vi.fn(),
      confirmPlan: vi.fn(),
    } as unknown as AgentClient;
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(AgentPanel, { client, readOnly: true }),
      host,
    );

    expect(host.textContent).toContain('历史回复');
    expect(host.textContent).toContain('只读');
    expect(host.querySelector('.agent-ui-panel-footer')).toBeNull();
    expect(host.querySelector('.agent-ui-panel-feedback')).toBeNull();
    expect(host.querySelector('button[title="新会话"]')).toBeNull();
    expect(host.querySelector('button[title="历史记录"]')).toBeNull();
    expect(host.querySelector<HTMLButtonElement>('.agent-ui-next-button')?.disabled).toBe(true);
    expect(host.querySelector<HTMLButtonElement>('.agent-ui-plan-confirmation-button')?.disabled).toBe(true);
    host.querySelector<HTMLButtonElement>('.agent-ui-plan-confirmation-button')?.click();
    expect(client.confirmPlan).not.toHaveBeenCalled();

    dispose();
  });

  it('reports next-button origin and sends it with the new run', () => {
    const nextButtonState: AgentState = {
      ...initialState,
      steps: [{
        index: 3,
        runId: 'run-source',
        role: 'assistant',
        thoughts: '',
        content: '请选择下一步\n[next-button:继续分析]\n[next-button:生成 SQL]',
        toolCalls: [],
        thoughtComplete: true,
        contentStarted: true,
        contentComplete: true,
      }],
    };
    const run = vi.fn();
    const client = {
      getState: () => nextButtonState,
      subscribe: (listener: (state: AgentState) => void) => {
        listener(nextButtonState);
        return () => undefined;
      },
      run,
      listCachedSessions: vi.fn(async () => []),
      syncSessions: vi.fn(async () => []),
      getRegisteredTool: vi.fn(),
      uploadAttachment: vi.fn(),
      cancel: vi.fn(),
      sendAskQuestionAnswer: vi.fn(),
    } as unknown as AgentClient;
    const onUIEvent = vi.fn();
    const onAfterSend = vi.fn();
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(AgentPanel, { client, onUIEvent, onAfterSend }),
      host,
    );

    const buttons = host.querySelectorAll<HTMLButtonElement>('.agent-ui-next-button');
    buttons[1].click();

    const action = {
      sourceRunId: 'run-source',
      sourceStepIndex: 3,
      buttonText: '生成 SQL',
      buttonIndex: 1,
    };
    const inputOrigin = { type: 'next_button', ...action };
    expect(onUIEvent).toHaveBeenCalledWith({
      type: 'next_button_click',
      sessionId: 'session-current',
      ...action,
    });
    expect(run).toHaveBeenCalledWith('生成 SQL', {
      displayParts: [{ type: 'text', text: '生成 SQL' }],
      attachments: undefined,
      executionMode: 'react',
      inputOrigin,
    });
    expect(onAfterSend).toHaveBeenCalledWith('生成 SQL', { inputOrigin });
    expect(onUIEvent.mock.invocationCallOrder[0]).toBeLessThan(run.mock.invocationCallOrder[0]);

    dispose();
  });

  it('reports new-session attempts and successful history selection', async () => {
    const switchSession = vi.fn(async () => undefined);
    const newSession = vi.fn(() => true);
    const session = {
      sessionId: 'session-history',
      callerKey: 'test',
      routeValues: [],
      title: '历史会话',
      state: 'done',
      createdAt: '2026-07-14 10:00:00',
      updatedAt: '2026-07-14 10:00:00',
      eventCount: 2,
    };
    const client = {
      getState: () => initialState,
      subscribe: (listener: (state: AgentState) => void) => {
        listener(initialState);
        return () => undefined;
      },
      newSession,
      switchSession,
      listCachedSessions: vi.fn(async () => [session]),
      syncSessions: vi.fn(async () => [session]),
      getRegisteredTool: vi.fn(),
      uploadAttachment: vi.fn(),
      run: vi.fn(),
      cancel: vi.fn(),
      sendAskQuestionAnswer: vi.fn(),
    } as unknown as AgentClient;
    const onUIEvent = vi.fn();
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(AgentPanel, { client, onUIEvent }),
      host,
    );

    host.querySelector<HTMLButtonElement>('button[title="新会话"]')?.click();
    expect(newSession).toHaveBeenCalledTimes(1);
    expect(onUIEvent).toHaveBeenCalledWith({
      type: 'new_session',
      previousSessionId: 'session-current',
      accepted: true,
    });

    host.querySelector<HTMLButtonElement>('button[title="历史记录"]')?.click();
    await vi.waitFor(() => expect(host.textContent).toContain('历史会话'));
    host.querySelector<HTMLButtonElement>('.agent-ui-session-item')?.click();
    await vi.waitFor(() => expect(switchSession).toHaveBeenCalledWith('session-history'));
    await vi.waitFor(() => expect(onUIEvent).toHaveBeenCalledWith({
      type: 'session_select',
      previousSessionId: 'session-current',
      sessionId: 'session-history',
    }));

    dispose();
  });

  it('reports ask_question answers before resuming the current run', async () => {
    const askQuestionState: AgentState = {
      ...initialState,
      status: 'running',
      currentRunId: 'run-ask-question',
      steps: [{
        index: 0,
        runId: 'run-ask-question',
        role: 'assistant',
        thoughts: '',
        content: '',
        toolCalls: [{
          toolUseId: 'tool-ask-question',
          toolName: 'ask_question',
          input: {
            questions: [{
              id: 'question-engine',
              prompt: '请选择查询引擎',
              options: [{ id: 'spark', label: 'Spark' }],
            }],
          },
          status: 'waiting',
          executedBy: 'internal',
        }],
        thoughtComplete: true,
        contentStarted: false,
        contentComplete: false,
      }],
    };
    const sendAskQuestionAnswer = vi.fn();
    const client = {
      getState: () => askQuestionState,
      subscribe: (listener: (state: AgentState) => void) => {
        listener(askQuestionState);
        return () => undefined;
      },
      sendAskQuestionAnswer,
      listCachedSessions: vi.fn(async () => []),
      syncSessions: vi.fn(async () => []),
      getRegisteredTool: vi.fn(),
      uploadAttachment: vi.fn(),
      run: vi.fn(),
      cancel: vi.fn(),
    } as unknown as AgentClient;
    const onUIEvent = vi.fn();
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(AgentPanel, { client, onUIEvent }),
      host,
    );

    host.querySelector<HTMLInputElement>('input[type="radio"]')?.click();
    host.querySelector<HTMLButtonElement>('.agent-ui-ask-question-btn-primary')?.click();

    const content = {
      answers: [{
        questionId: 'question-engine',
        selectedOptionIds: ['spark'],
        freeText: '',
      }],
      skipped: false,
    };
    expect(onUIEvent).toHaveBeenCalledWith({
      type: 'ask_question_submit',
      sessionId: 'session-current',
      runId: 'run-ask-question',
      toolUseId: 'tool-ask-question',
      content,
      skipped: false,
      questionCount: 1,
    });
    expect(sendAskQuestionAnswer).toHaveBeenCalledWith('tool-ask-question', content);
    expect(onUIEvent.mock.invocationCallOrder[0]).toBeLessThan(
      sendAskQuestionAnswer.mock.invocationCallOrder[0],
    );

    dispose();
  });


  it('mounts a waiting ClientTool interaction above the input without replacing its message card', async () => {
    const waitingState: AgentState = {
      ...initialState,
      status: 'waiting_client_tool',
      currentRunId: 'run-confirm-write',
      steps: [{
        index: 0,
        runId: 'run-confirm-write',
        role: 'assistant',
        thoughts: '',
        content: '',
        toolCalls: [{
          toolUseId: 'tool-confirm-write',
          toolName: 'confirm_write',
          input: { value: 'next' },
          status: 'waiting',
          executedBy: 'client',
        }],
        thoughtComplete: true,
        contentStarted: false,
        contentComplete: false,
      }],
    };
    const cardRenderer: ClientToolUIRenderer = {
      mount(container) {
        container.textContent = 'persistent-card';
        return { update: vi.fn(), unmount: vi.fn() };
      },
    };
    const interactionUnmount = vi.fn();
    const interactionRenderer: ClientToolUIRenderer = {
      mount(container) {
        container.textContent = 'confirm-actions';
        return { update: vi.fn(), unmount: interactionUnmount };
      },
    };
    const shouldRenderInteraction = vi.fn(() => true);
    const tool: ClientTool = {
      name: 'confirm_write',
      uiPlacement: 'root',
      ui: { type: 'custom', renderer: cardRenderer },
      interactionUI: {
        placement: 'feedback',
        shouldRender: shouldRenderInteraction,
        renderer: interactionRenderer,
      },
      execute: async () => ({ content: 'done' }),
    };
    let notifyState: ((state: AgentState) => void) | undefined;
    const client = {
      getState: () => waitingState,
      subscribe: (listener: (state: AgentState) => void) => {
        notifyState = listener;
        listener(waitingState);
        return () => undefined;
      },
      getRegisteredTool: vi.fn(() => tool),
      listCachedSessions: vi.fn(async () => []),
      syncSessions: vi.fn(async () => []),
      uploadAttachment: vi.fn(),
      run: vi.fn(),
      cancel: vi.fn(),
      sendAskQuestionAnswer: vi.fn(),
    } as unknown as AgentClient;
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(AgentPanel, { client }),
      host,
    );

    expect(host.querySelector('.agent-ui-panel-chat')?.textContent).toContain('persistent-card');
    expect(host.querySelector('.agent-ui-panel-feedback')?.textContent).toContain('confirm-actions');
    expect(shouldRenderInteraction).toHaveBeenCalledWith(expect.objectContaining({
      toolUseId: 'tool-confirm-write',
      status: 'waiting',
    }));

    notifyState?.({
      ...waitingState,
      status: 'idle',
      currentRunId: null,
      steps: [{
        ...waitingState.steps[0],
        toolCalls: [{
          ...waitingState.steps[0].toolCalls[0],
          status: 'done',
          result: 'confirmed',
        }],
      }],
    });

    await vi.waitFor(() => expect(host.querySelector('.agent-ui-panel-feedback')).toBeNull());
    expect(interactionUnmount).toHaveBeenCalledTimes(1);
    expect(host.querySelector('.agent-ui-panel-chat')?.textContent).toContain('persistent-card');

    dispose();
  });

  it('shows upload errors and dismisses them automatically', async () => {
    vi.useFakeTimers();
    const client = {
      getState: () => initialState,
      subscribe: (listener: (state: AgentState) => void) => {
        listener(initialState);
        return () => undefined;
      },
      listCachedSessions: vi.fn(async () => []),
      syncSessions: vi.fn(async () => []),
      getRegisteredTool: vi.fn(),
      uploadAttachment: vi.fn(),
      run: vi.fn(),
      cancel: vi.fn(),
      sendAskQuestionAnswer: vi.fn(),
    } as unknown as AgentClient;
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(AgentPanel, { client }),
      host,
    );

    const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(input, 'files', {
      configurable: true,
      value: Array.from({ length: 6 }, (_, index) => new File(['x'], `${index}.txt`)),
    });
    input.dispatchEvent(new Event('change', { bubbles: true }));

    expect(host.querySelector('[role="alert"]')?.textContent).toBe('最多上传 5 个文件');
    await vi.advanceTimersByTimeAsync(3000);
    expect(host.querySelector('[role="alert"]')).toBeNull();

    dispose();
    vi.useRealTimers();
  });

  it('exposes host input commands for filling and clearing the editor draft', async () => {
    const client = {
      getState: () => initialState,
      subscribe: (listener: (state: AgentState) => void) => {
        listener(initialState);
        return () => undefined;
      },
      listCachedSessions: vi.fn(async () => []),
      syncSessions: vi.fn(async () => []),
      getRegisteredTool: vi.fn(),
      uploadAttachment: vi.fn(),
      run: vi.fn(),
      cancel: vi.fn(),
      sendAskQuestionAnswer: vi.fn(),
    } as unknown as AgentClient;
    let command: AgentInputCommand | null = null;
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(AgentPanel, {
        client,
        bindInputCommand: (next) => {
          command = next;
        },
      }),
      host,
    );

    await vi.waitFor(() => expect(command).not.toBeNull());
    await expect(command!.fillInput('宿主预填内容')).resolves.toEqual({ status: 'filled' });
    await vi.waitFor(() => {
      expect(host.querySelector('[role="textbox"]')?.textContent).toBe('宿主预填内容');
    });

    await expect(command!.fillInput('')).resolves.toEqual({ status: 'filled' });
    await vi.waitFor(() => {
      expect(host.querySelector('[role="textbox"]')?.textContent).toBe('');
    });

    dispose();
    expect(command).toBeNull();
  });

  it('submits host input, ignores submit while running, and rejects invalid states', async () => {
    let currentState = initialState;
    let stateListener: ((state: AgentState) => void) | undefined;
    const run = vi.fn();
    const onAfterSend = vi.fn();
    const client = {
      getState: () => currentState,
      subscribe: (listener: (state: AgentState) => void) => {
        stateListener = listener;
        listener(currentState);
        return () => undefined;
      },
      listCachedSessions: vi.fn(async () => []),
      syncSessions: vi.fn(async () => []),
      getRegisteredTool: vi.fn(),
      uploadAttachment: vi.fn(),
      run,
      cancel: vi.fn(),
      sendAskQuestionAnswer: vi.fn(),
    } as unknown as AgentClient;
    let command: AgentInputCommand | null = null;
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(AgentPanel, {
        client,
        onAfterSend,
        bindInputCommand: (next) => {
          command = next;
        },
      }),
      host,
    );

    await vi.waitFor(() => expect(command).not.toBeNull());
    await expect(command!.fillInput('直接发送', { submit: true })).resolves.toEqual({ status: 'submitted' });
    expect(run).toHaveBeenCalledWith('直接发送', {
      displayParts: [{ type: 'text', text: '直接发送' }],
      attachments: undefined,
      executionMode: 'react',
      inputOrigin: { type: 'manual' },
    });
    expect(onAfterSend).toHaveBeenCalledWith('直接发送', {
      inputOrigin: { type: 'manual' },
    });

    currentState = { ...initialState, status: 'running' };
    stateListener?.(currentState);
    await expect(command!.fillInput('运行中仅预填', { submit: true })).resolves.toEqual({
      status: 'filled',
    });
    await vi.waitFor(() => {
      expect(host.querySelector('[role="textbox"]')?.textContent).toBe('运行中仅预填');
    });
    await expect(command!.fillInput('', { submit: true })).resolves.toEqual({
      status: 'filled',
    });
    await vi.waitFor(() => {
      expect(host.querySelector('[role="textbox"]')?.textContent).toBe('');
    });
    expect(run).toHaveBeenCalledTimes(1);

    currentState = { ...initialState, status: 'idle', connected: false };
    stateListener?.(currentState);
    await expect(command!.fillInput('断线发送', { submit: true })).resolves.toEqual({
      status: 'rejected',
      reason: 'disconnected',
    });

    currentState = { ...initialState, status: 'idle', connected: true };
    stateListener?.(currentState);
    await expect(command!.fillInput('   ', { submit: true })).resolves.toEqual({
      status: 'rejected',
      reason: 'empty',
    });
    expect(run).toHaveBeenCalledTimes(1);

    dispose();
  });
});
