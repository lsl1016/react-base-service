import copy from 'copy-to-clipboard';
import { createComponent, createSignal } from 'solid-js';
import { render } from 'solid-js/web';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { PlanAttemptState, PlanRuntimeState, Step } from '../runtime/types';
import { MessageList } from '../ui/components/MessageList';

vi.mock('copy-to-clipboard', () => ({ default: vi.fn(() => true) }));
vi.mock('../ui/components/SmartScroll', () => ({
  SmartScroll: (props: { children: unknown }) => props.children,
}));

function createStep(overrides: Partial<Step>): Step {
  return {
    index: 0,
    runId: 'run-1',
    role: 'assistant',
    thoughts: '',
    content: '',
    toolCalls: [],
    thoughtComplete: true,
    contentStarted: true,
    contentComplete: true,
    ...overrides,
  };
}

afterEach(() => {
  document.body.innerHTML = '';
  vi.clearAllMocks();
});

describe('message UI events', () => {
  it('renders a user message without copy or restore actions', () => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(MessageList, {
        steps: [createStep({ index: -1, role: 'user', content: '生成日报 SQL' })],
        isRunning: false,
      }),
      host,
    );

    expect(host.querySelector('.agent-ui-message-user-content')?.textContent).toBe('生成日报 SQL');
    expect(host.querySelector('.agent-ui-message-user-actions')).toBeNull();
    expect(host.querySelector('[aria-label="复制"]')).toBeNull();
    expect(host.querySelector('[aria-label="回填到输入框"]')).toBeNull();
    dispose();
  });

  it('reports opening the problem feedback form', () => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const onProblemFeedbackOpen = vi.fn();
    const dispose = render(
      () => createComponent(MessageList, {
        steps: [
          createStep({ index: -1, role: 'user', content: '问题' }),
          createStep({ content: '回答' }),
        ],
        isRunning: false,
        onFeedback: vi.fn(),
        onProblemFeedbackOpen,
      }),
      host,
    );

    host.querySelector<HTMLButtonElement>('[aria-label="问题反馈"]')?.click();
    expect(onProblemFeedbackOpen).toHaveBeenCalledWith('run-1');
    dispose();
  });

  it('copies the full assistant content for the current turn and shows copied state', () => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(MessageList, {
        steps: [
          createStep({ index: -1, role: 'user', content: '问题' }),
          createStep({ index: 0, content: '工具调用前的说明' }),
          createStep({ index: 1, content: '工具调用后的最终结论' }),
        ],
        sessionId: 'session-228872646',
        appKey: 'demo-app',
        routeValues: ['database-1', 'worksheet-2'],
        isRunning: false,
        onFeedback: vi.fn(),
      }),
      host,
    );

    const copyButton = host.querySelector<HTMLButtonElement>('.agent-ui-feedback-copy-btn');
    expect(copyButton?.textContent).toContain('复制');
    copyButton?.click();

    expect(copy).toHaveBeenCalledWith('工具调用前的说明\n\n工具调用后的最终结论');
    expect(copyButton?.textContent).toContain('已复制');
    expect(copyButton?.getAttribute('aria-label')).toBe('已复制');
    expect(copyButton?.classList.contains('is-active')).toBe(false);

    const copyIdButton = host.querySelector<HTMLButtonElement>('.agent-ui-feedback-copy-id-btn');
    expect(copyIdButton?.textContent).toContain('Copy ID');
    expect(copyIdButton?.querySelector('svg')).not.toBeNull();
    copyIdButton?.click();

    expect(copy).toHaveBeenLastCalledWith(JSON.stringify({
      sessionId: 'session-228872646',
      appKey: 'demo-app',
      routeValues: ['database-1', 'worksheet-2'],
    }, null, 2));
    expect(copyIdButton?.textContent).toContain('已复制');
    expect(copyIdButton?.classList.contains('is-active')).toBe(false);
    dispose();
  });

  it('只让同一 Plan 的最新卡片订阅实时状态', () => {
    const planExecutionId = 'plan_shared';
    const waitingView = {
      plan_execution_id: planExecutionId,
      status: 'WAIT_EXTERNAL_TASK',
      summary: '等待旧任务完成',
      steps: [{
        step_id: 'external_task',
        step_order: 1,
        step_name: '执行外部任务',
        status: 'WAIT_EXTERNAL_TASK',
        summary: '等待旧任务完成',
      }],
      wait_request: {
        request_id: 'wait_old',
        type: 'EXTERNAL_TASK',
        question: '旧任务仍在外部执行',
      },
      can_resume: true,
      updated_at: '2026-09-04T20:00:00+08:00',
    };
    const runningPlan: PlanRuntimeState = {
      planExecutionId,
      view: {
        plan_execution_id: planExecutionId,
        status: 'RUNNING',
        summary: '新 Run 正在恢复执行',
        steps: [{
          step_id: 'external_task',
          step_order: 1,
          step_name: '执行外部任务',
          status: 'RUNNING',
          summary: '新 Run 正在执行',
        }],
        can_resume: false,
        updated_at: '2026-09-04T20:01:00+08:00',
      },
      attempts: {},
      attemptOrder: [],
    };
    const [plans, setPlans] = createSignal<Record<string, PlanRuntimeState>>({
      [planExecutionId]: runningPlan,
    });
    const loadedAttempt: PlanAttemptState = {
      planExecutionId,
      stepId: 'external_task',
      stepOrder: 1,
      stepAttemptId: 'attempt_old',
      attemptNo: 1,
      stepRunId: 'step-run-old',
      status: 'WAIT_EXTERNAL_TASK',
      steps: [],
    };
    const onLoadPlan = vi.fn(() => {
      setPlans((current) => ({
        ...current,
        [planExecutionId]: {
          ...current[planExecutionId],
          attempts: { attempt_old: loadedAttempt },
          attemptOrder: ['attempt_old'],
        },
      }));
    });
    const steps = [
      createStep({ index: -1, runId: 'run-old', role: 'user', content: '开始任务' }),
      createStep({
        index: 0,
        runId: 'run-old',
        contentStarted: false,
        toolCalls: [{
          toolUseId: 'start-plan',
          toolName: 'start_template_plan',
          input: {},
          status: 'done',
          result: JSON.stringify(waitingView),
          executedBy: 'internal',
          planExecutionId,
        }],
      }),
      createStep({ index: -1, runId: 'run-new', role: 'user', content: '继续任务' }),
      createStep({
        index: 0,
        runId: 'run-new',
        contentStarted: false,
        toolCalls: [{
          toolUseId: 'resume-plan',
          toolName: 'resume_template_plan',
          input: { plan_execution_id: planExecutionId },
          status: 'done',
          result: JSON.stringify(runningPlan.view),
          executedBy: 'internal',
          planExecutionId,
        }],
      }),
    ];
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(
      () => createComponent(MessageList, {
        steps,
        isRunning: false,
        get plans() { return plans(); },
        onLoadPlan,
      }),
      host,
    );

    let cards = host.querySelectorAll('.agent-ui-plan-runtime-card');
    expect(cards).toHaveLength(2);
    expect(cards[0].textContent).toContain('等待旧任务完成');
    expect(cards[0].textContent).toContain('旧任务仍在外部执行');
    expect(cards[0].textContent).toContain('等待外部任务');
    expect(cards[1].textContent).toContain('新 Run 正在恢复执行');
    expect(cards[1].textContent).toContain('执行中');

    cards[0].querySelector<HTMLButtonElement>('.agent-ui-plan-step-header')?.click();
    expect(onLoadPlan).toHaveBeenCalledWith(planExecutionId);
    expect(cards[0].textContent).toContain('第 1 次执行');

    setPlans({
      [planExecutionId]: {
        ...runningPlan,
        view: {
          ...runningPlan.view,
          status: 'SUCCEEDED',
          summary: '新 Run 已执行完成',
          can_resume: false,
          updated_at: '2026-09-04T20:02:00+08:00',
        },
      },
    });

    cards = host.querySelectorAll('.agent-ui-plan-runtime-card');
    expect(cards[0].textContent).toContain('等待旧任务完成');
    expect(cards[0].textContent).toContain('旧任务仍在外部执行');
    expect(cards[0].textContent).not.toContain('新 Run 已执行完成');
    expect(cards[1].textContent).toContain('新 Run 已执行完成');
    expect(cards[1].textContent).toContain('已完成');
    dispose();
  });
});
