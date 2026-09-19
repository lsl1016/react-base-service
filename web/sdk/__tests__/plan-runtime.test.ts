import { createComponent, createSignal } from 'solid-js';
import { render } from 'solid-js/web';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { EventReducer } from '../runtime/event-reducer';
import type { PlanRuntimeState, ToolCallState } from '../runtime/types';
import { PlanRuntimeCard, splitPlanRoundToolCalls } from '../ui/components/PlanRuntimeCard';

describe('EventReducer - Plan runtime', () => {
  afterEach(() => {
    document.body.innerHTML = '';
  });
  it('聚合 Plan 公开进度和隔离的 Step 执行详情', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'plan_view_update',
      seq: 1,
      runId: 'run_outer',
      sessionId: 'session_1',
      payload: {
        planExecutionId: 'plan_1',
        view: {
          plan_execution_id: 'plan_1',
          status: 'RUNNING',
          summary: '正在生成 SQL',
          steps: [{ step_id: 'generate_sql', step_order: 1, step_name: '生成 SQL', status: 'RUNNING', summary: '正在执行' }],
          current_step: { step_id: 'generate_sql', step_order: 1, step_name: '生成 SQL', status: 'RUNNING', summary: '正在执行' },
          can_resume: true,
          updated_at: '2026-09-01T19:00:00+08:00',
        },
      },
    });
    reducer.applyEvent({
      type: 'plan_step_event',
      seq: 2,
      runId: 'run_outer',
      sessionId: 'session_1',
      payload: {
        planExecutionId: 'plan_1',
        planVersionId: 'version_1',
        stepId: 'generate_sql',
        stepOrder: 1,
        stepAttemptId: 'attempt_1',
        attemptNo: 1,
        stepRunId: 'run_step_1',
        event: {
          type: 'tool_use_start',
          seq: 1,
          runId: 'run_step_1',
          sessionId: 'session_1',
          stepIndex: 0,
          payload: { toolUseId: 'tool_1', toolName: 'execute_tool', toolInput: { name: '查询元数据' }, executedBy: 'internal', status: 'running' },
        },
      },
    });

    const state = reducer.getState();
    expect(state.plans.plan_1.view.status).toBe('RUNNING');
    expect(state.plans.plan_1.attempts.attempt_1.steps[0].toolCalls[0].toolName).toBe('execute_tool');
    expect(state.steps).toHaveLength(0);
  });

  it('原生 Plan 等待态保留 outerRunId 并回到 idle（输入锁定由 hasBlockingPlanWait 承担）', () => {
    const reducer = new EventReducer();
    reducer.resetForNewRun();
    reducer.addUserStep('请检查任务并在修改前确认');

    reducer.applyEvent({
      type: 'plan_view_update',
      seq: 1,
      runId: 'run_plan_outer',
      sessionId: 'session_plan',
      payload: {
        planExecutionId: 'plan_wait_action',
        view: {
          plan_execution_id: 'plan_wait_action',
          status: 'WAIT_USER_ACTION',
          summary: '等待确认修改方案',
          steps: [{
            step_id: 'approve_fix',
            step_order: 2,
            step_name: '确认修改方案',
            step_type: 'USER_ACTION',
            required: true,
            status: 'WAIT_USER_ACTION',
            summary: '',
          }],
          current_step: {
            step_id: 'approve_fix',
            step_order: 2,
            step_name: '确认修改方案',
            step_type: 'USER_ACTION',
            required: true,
            status: 'WAIT_USER_ACTION',
            summary: '',
          },
          wait_request: {
            request_id: 'wait_confirm',
            type: 'USER_ACTION',
            question: '确认执行上述修改吗？',
          },
          can_resume: true,
          updated_at: '2026-09-19T13:00:00+08:00',
        },
      },
    });

    const state = reducer.getState();
    // 服务端 WAIT 时执行 goroutine 已结束，reducer 置回 idle；输入区锁定由 AgentPanel 的
    // hasBlockingPlanWait（plans 派生）承担，不再使用全局 waiting_plan 状态。
    expect(state.status).toBe('idle');
    expect(state.currentRunId).toBe('run_plan_outer');
    expect(state.plans.plan_wait_action.outerRunId).toBe('run_plan_outer');
    expect(state.steps[0].runId).toBe('run_plan_outer');
  });

  it('历史回放遇到持久化等待 Plan 后仍恢复等待视图并回到 idle', () => {
    const reducer = new EventReducer();
    reducer.replayEvents([
      {
        type: 'run',
        seq: 1,
        runId: 'run_plan_history',
        sessionId: 'session_plan_history',
        payload: {
          callerKey: 'demo',
          type: 'chat',
          userPrompt: '执行一个需要确认的任务',
          executionMode: 'plan',
        },
      },
      {
        type: 'plan_view_update',
        seq: 2,
        runId: 'run_plan_history',
        sessionId: 'session_plan_history',
        payload: {
          planExecutionId: 'plan_history',
          view: {
            plan_execution_id: 'plan_history',
            status: 'WAIT_USER_INPUT',
            summary: '等待补充环境',
            steps: [],
            wait_request: {
              request_id: 'wait_env',
              type: 'USER_INPUT',
              question: '目标环境是什么？',
            },
            can_resume: true,
            updated_at: '2026-09-19T13:00:00+08:00',
          },
        },
      },
    ]);

    const state = reducer.getState();
    // 回放结束后会话处于 idle，currentRunId 已收敛为空；等待视图经 plans 记录与 outerRunId 归属还原。
    expect(state.status).toBe('idle');
    expect(state.plans.plan_history.outerRunId).toBe('run_plan_history');
  });

  it('内层 thought_end 和 content_end 沿用现有方式更新用量统计', () => {
    const reducer = new EventReducer();
    const basePayload = {
      planExecutionId: 'plan_stats',
      planVersionId: 'version_1',
      stepId: 'execute',
      stepOrder: 1,
      stepAttemptId: 'attempt_stats',
      attemptNo: 1,
      stepRunId: 'run_step_stats',
    };

    reducer.applyEvent({
      type: 'plan_step_event',
      seq: 1,
      runId: 'run_outer',
      sessionId: 'session_1',
      payload: {
        ...basePayload,
        event: {
          type: 'thought_end',
          seq: 1,
          runId: 'run_step_stats',
          sessionId: 'session_1',
          stepIndex: 0,
          payload: {
            content: '内层思考',
            inputTokens: 900,
            outputTokens: 100,
            cacheReadTokens: 600,
            cacheCreateTokens: 20,
            contextUsedTokens: 1000,
            maxContextTokens: 170000,
          },
        },
      },
    });
    expect(reducer.getState().lastRunStats).toMatchObject({
      inputTokens: 900,
      cacheReadTokens: 600,
      contextUsedTokens: 1000,
    });

    reducer.applyEvent({
      type: 'plan_step_event',
      seq: 2,
      runId: 'run_outer',
      sessionId: 'session_1',
      payload: {
        ...basePayload,
        event: {
          type: 'content_end',
          seq: 2,
          runId: 'run_step_stats',
          sessionId: 'session_1',
          stepIndex: 0,
          payload: {
            content: '内层正文',
            inputTokens: 1200,
            outputTokens: 200,
            cacheReadTokens: 800,
            cacheCreateTokens: 30,
            contextUsedTokens: 1400,
            maxContextTokens: 170000,
          },
        },
      },
    });
    expect(reducer.getState().lastRunStats).toEqual({
      inputTokens: 1200,
      outputTokens: 200,
      cacheReadTokens: 800,
      cacheCreateTokens: 30,
      contextUsedTokens: 1400,
      maxContextTokens: 170000,
    });
  });

  it('从 Plan Meta Tool 结果恢复 WAIT_USER_INPUT 视图', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 1,
      runId: 'run_outer',
      stepIndex: 0,
      payload: { toolUseId: 'plan_tool', toolName: 'start_template_plan', toolInput: {}, executedBy: 'internal', status: 'running' },
    });
    reducer.applyEvent({
      type: 'tool_use_end',
      seq: 2,
      runId: 'run_outer',
      stepIndex: 0,
      payload: {
        toolUseId: 'plan_tool',
        content: JSON.stringify({
          plan_execution_id: 'plan_wait',
          status: 'WAIT_USER_INPUT',
          summary: '缺少统计口径',
          steps: [],
          wait_request: {
            request_id: 'wait_1',
            type: 'USER_INPUT',
            question: '按什么口径？',
            response_schema: {
              type: 'object',
              properties: { metric_definition: { type: 'string' } },
              required: ['metric_definition'],
              additionalProperties: false,
            },
          },
          can_resume: true,
          updated_at: '2026-09-01T19:00:00+08:00',
        }),
        truncated: false,
        isError: false,
        executedBy: 'internal',
        status: 'success',
        durationMs: 10,
      },
    });

    const state = reducer.getState();
    expect(state.plans.plan_wait.view.wait_request?.request_id).toBe('wait_1');
    expect(state.steps[0].toolCalls[0].planExecutionId).toBe('plan_wait');
  });

  it('按 seq 恢复 Plan Step 多轮事件且不合并思考和正文', () => {
    const reducer = new EventReducer();
    const events = [
      { type: 'tool_use_end', seq: 13, runId: 'run_step', stepIndex: 1, payload: { toolUseId: 'tool_after', content: 'after result', isError: false } },
      { type: 'tool_use_start', seq: 12, runId: 'run_step', stepIndex: 1, payload: { toolUseId: 'tool_after', toolName: 'after_tool', toolInput: {}, executedBy: 'server' } },
      { type: 'content_end', seq: 11, runId: 'run_step', stepIndex: 1, payload: { content: '第二段正文' } },
      { type: 'content_delta', seq: 10, runId: 'run_step', stepIndex: 1, payload: { contentDelta: '第二段正文' } },
      { type: 'content_start', seq: 9, runId: 'run_step', stepIndex: 1 },
      { type: 'thought_end', seq: 8, runId: 'run_step', stepIndex: 1, payload: { content: '第二次思考' } },
      { type: 'thought_delta', seq: 7, runId: 'run_step', stepIndex: 1, payload: { contentDelta: '第二次思考' } },
      { type: 'thought_start', seq: 6, runId: 'run_step', stepIndex: 1 },
      { type: 'tool_use_end', seq: 5, runId: 'run_step', stepIndex: 0, payload: { toolUseId: 'tool_before', content: 'before result', isError: false } },
      { type: 'tool_use_start', seq: 4, runId: 'run_step', stepIndex: 0, payload: { toolUseId: 'tool_before', toolName: 'before_tool', toolInput: {}, executedBy: 'server' } },
      { type: 'thought_end', seq: 3, runId: 'run_step', stepIndex: 0, payload: { content: '第一次思考' } },
      { type: 'thought_delta', seq: 2, runId: 'run_step', stepIndex: 0, payload: { contentDelta: '第一次思考' } },
      { type: 'thought_start', seq: 1, runId: 'run_step', stepIndex: 0 },
    ] as const;

    reducer.applyPlanStepEvents('plan_order', 'execute', 'attempt_order', 1, [...events]);

    const rounds = reducer.getState().plans.plan_order.attempts.attempt_order.steps;
    expect(rounds).toHaveLength(2);
    expect(rounds[0].thoughts).toBe('第一次思考');
    expect(rounds[0].content).toBe('');
    expect(rounds[0].toolCalls.map((tool) => tool.toolName)).toEqual(['before_tool']);
    expect(rounds[1].thoughts).toBe('第二次思考');
    expect(rounds[1].content).toBe('第二段正文');
    expect(rounds[1].toolCalls[0].afterContent).toBe(true);
    expect(rounds[1].toolCalls[0].result).toBe('after result');
  });

  it('Plan 轮次按正文前后拆分工具并保持各自顺序', () => {
    const tools = [
      { toolUseId: 'before_1', toolName: 'before_1', input: {}, status: 'done', executedBy: 'server', afterContent: false },
      { toolUseId: 'after_1', toolName: 'after_1', input: {}, status: 'done', executedBy: 'server', afterContent: true },
      { toolUseId: 'before_2', toolName: 'before_2', input: {}, status: 'done', executedBy: 'server' },
      { toolUseId: 'after_2', toolName: 'after_2', input: {}, status: 'done', executedBy: 'server', afterContent: true },
    ] as ToolCallState[];

    const ordered = splitPlanRoundToolCalls(tools);
    expect(ordered.beforeContent.map((tool) => tool.toolUseId)).toEqual(['before_1', 'before_2']);
    expect(ordered.afterContent.map((tool) => tool.toolUseId)).toEqual(['after_1', 'after_2']);
  });

  it('USER_ACTION 等待展示确认和拒绝，并显式提交 approved', () => {
    const onResume = vi.fn();
    const plan: PlanRuntimeState = {
      planExecutionId: 'plan_action',
      view: {
        plan_execution_id: 'plan_action',
        status: 'WAIT_USER_ACTION',
        summary: '等待权限申请',
        steps: [],
        wait_request: { request_id: 'wait_action', type: 'USER_ACTION', question: '请确认是否执行修复' },
        can_resume: true,
        updated_at: '2026-09-03T20:00:00+08:00',
      },
      attempts: {},
      attemptOrder: [],
    };
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(() => createComponent(PlanRuntimeCard, {
      toolCall: { toolUseId: 'plan_tool', toolName: 'plan_runtime', input: {}, status: 'running', executedBy: 'internal' },
      plan,
      onResume,
    }), host);

    expect(host.textContent).toContain('需要完成操作');
    const confirmButton = [...host.querySelectorAll<HTMLButtonElement>('button')]
      .find((button) => button.textContent?.includes('确认执行'))!;
    const rejectButton = [...host.querySelectorAll<HTMLButtonElement>('button')]
      .find((button) => button.textContent?.includes('拒绝'))!;
    expect(confirmButton.disabled).toBe(false);
    expect(rejectButton.disabled).toBe(false);

    confirmButton.click();
    expect(onResume).toHaveBeenCalledWith('plan_action', 'wait_action', { approved: true });
    dispose();

    // 提交后卡片进入 submitting 态，同一实例不再响应后续点击；拒绝路径在新实例上验证。
    const onReject = vi.fn();
    const rejectHost = document.createElement('div');
    document.body.appendChild(rejectHost);
    const rejectDispose = render(() => createComponent(PlanRuntimeCard, {
      toolCall: { toolUseId: 'plan_tool', toolName: 'plan_runtime', input: {}, status: 'running', executedBy: 'internal' },
      plan,
      onResume: onReject,
    }), rejectHost);
    const freshRejectButton = [...rejectHost.querySelectorAll<HTMLButtonElement>('button')]
      .find((button) => button.textContent?.includes('拒绝'))!;
    freshRejectButton.click();
    expect(onReject).toHaveBeenCalledWith('plan_action', 'wait_action', { approved: false });
    rejectDispose();
  });

  it('EXTERNAL_TASK 展示等待文案并支持手动恢复继续执行', () => {
    const onResume = vi.fn();
    const plan: PlanRuntimeState = {
      planExecutionId: 'plan_external',
      view: {
        plan_execution_id: 'plan_external',
        status: 'WAIT_EXTERNAL_TASK',
        summary: '',
        steps: [],
        wait_request: { request_id: 'wait_external', type: 'EXTERNAL_TASK' },
        can_resume: true,
        updated_at: '2026-09-03T20:00:00+08:00',
      },
      attempts: {},
      attemptOrder: [],
    };
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(() => createComponent(PlanRuntimeCard, {
      toolCall: { toolUseId: 'plan_tool', toolName: 'start_template_plan', input: {}, status: 'done', executedBy: 'internal' },
      plan,
      onResume,
    }), host);

    expect(host.textContent).toContain('外部任务执行中');
    expect(host.textContent).toContain('任务正在执行，请等待任务完成。');
    // 第一版不自动轮询外部任务：提供手动恢复按钮，点击后以空响应 resume。
    const resumeButton = host.querySelector<HTMLButtonElement>('.agent-ui-plan-resume-button');
    expect(resumeButton).not.toBeNull();
    expect(resumeButton?.textContent).toContain('任务已完成，继续执行');
    resumeButton!.click();
    expect(onResume).toHaveBeenCalledWith('plan_external', 'wait_external', {});
    dispose();
  });

  it('失败 Plan 默认展开并直接展示错误摘要', () => {
    const summary = '步骤“生成 SQL”已达到模板配置的最大模型轮次（20 轮），请调高 max_rounds 后重新发起任务。';
    const plan: PlanRuntimeState = {
      planExecutionId: 'plan_failed',
      view: {
        plan_execution_id: 'plan_failed',
        status: 'FAILED',
        summary,
        steps: [{ step_id: 'generate_sql', step_order: 1, step_name: '生成 SQL', status: 'FAILED', summary }],
        error: { code: 'TOOL_CONTRACT_ERROR', summary },
        can_resume: false,
        updated_at: '2026-09-03T20:00:00+08:00',
      },
      attempts: {},
      attemptOrder: [],
    };
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(() => createComponent(PlanRuntimeCard, {
      toolCall: { toolUseId: 'plan_tool', toolName: 'start_template_plan', input: {}, status: 'done', executedBy: 'internal' },
      plan,
    }), host);

    expect(host.querySelector('.agent-ui-plan-runtime-body')).not.toBeNull();
    expect(host.querySelector('.agent-ui-plan-error')?.textContent).toContain('max_rounds');
    expect(host.querySelector('.agent-ui-plan-error')?.textContent).toContain('重新发起任务');
    dispose();
  });

  it('Plan 恢复命令失败时从运行态切换为明确失败态', () => {
    const [toolCall, setToolCall] = createSignal<ToolCallState>({
      toolUseId: 'resume_failed',
      toolName: 'resume_template_plan',
      description: '恢复归因分析 Plan',
      input: { plan_execution_id: 'plan_external' },
      status: 'running',
      executedBy: 'internal',
    });
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(() => createComponent(PlanRuntimeCard, {
      get toolCall() { return toolCall(); },
    }), host);

    expect(host.textContent).toContain('恢复归因分析 Plan');
    expect(host.textContent).toContain('执行中');

    setToolCall({
      ...toolCall(),
      status: 'error',
      isError: true,
      result: 'WAIT_EXTERNAL_TASK resume requires current wait_request_id',
    });

    expect(host.textContent).toContain('Plan 恢复失败');
    expect(host.textContent).toContain('执行失败');
    expect(host.textContent).toContain('WAIT_EXTERNAL_TASK resume requires current wait_request_id');
    expect(host.textContent).not.toContain('Plan 正在初始化');
    dispose();
  });

  it('Plan 工具取消后优先展示已停止的 Plan 视图', () => {
    const plan: PlanRuntimeState = {
      planExecutionId: 'plan_stopped',
      view: {
        plan_execution_id: 'plan_stopped',
        status: 'STOPPED',
        summary: 'Plan 已停止',
        steps: [{ step_id: 'inspect', step_order: 1, step_name: '检查数据', status: 'STOPPED', summary: '用户已取消' }],
        can_resume: true,
        updated_at: '2026-09-07T12:00:00+08:00',
      },
      attempts: {},
      attemptOrder: [],
    };
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(() => createComponent(PlanRuntimeCard, {
      toolCall: {
        toolUseId: 'plan_tool_cancelled',
        toolName: 'start_template_plan',
        input: {},
        status: 'cancelled',
        result: '用户取消了本次运行，工具未完成执行。',
        executedBy: 'internal',
        planExecutionId: 'plan_stopped',
      },
      plan,
      useLiveView: true,
    }), host);

    expect(host.textContent).toContain('Plan 已停止');
    expect(host.textContent).toContain('已停止');
    expect(host.textContent).not.toContain('Plan 启动失败');
    expect(host.textContent).not.toContain('工具未完成执行');
    dispose();
  });

  it('运行中的恢复卡片不复用旧的 USER_INPUT 表单', () => {
    const plan: PlanRuntimeState = {
      planExecutionId: 'plan_resuming',
      view: {
        plan_execution_id: 'plan_resuming',
        status: 'WAIT_USER_INPUT',
        summary: '等待集群选择',
        steps: [],
        wait_request: {
          request_id: 'wait_input',
          type: 'USER_INPUT',
          question: '请选择计算集群',
          response_schema: {
            type: 'object',
            properties: { cluster_name: { type: 'string' } },
            required: ['cluster_name'],
          },
        },
        can_resume: true,
        updated_at: '2026-09-07T12:00:00+08:00',
      },
      attempts: {},
      attemptOrder: [],
    };
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(() => createComponent(PlanRuntimeCard, {
      toolCall: {
        toolUseId: 'resume_running',
        toolName: 'resume_template_plan',
        description: '恢复 Plan',
        input: { plan_execution_id: 'plan_resuming' },
        status: 'running',
        executedBy: 'internal',
      },
      plan,
      useLiveView: true,
      disabled: true,
    }), host);

    expect(host.textContent).toContain('正在恢复执行');
    expect(host.textContent).toContain('等待请求已处理，Plan 正在继续执行。');
    expect(host.textContent).not.toContain('需要补充信息');
    expect(host.querySelector('.agent-ui-plan-wait-field')).toBeNull();
    expect(host.querySelector('.agent-ui-plan-resume-button')).toBeNull();
    dispose();
  });

  it('USER_INPUT 根据 response_schema 收集当前步骤临时回答', () => {
    const onResume = vi.fn();
    const plan: PlanRuntimeState = {
      planExecutionId: 'plan_input',
      view: {
        plan_execution_id: 'plan_input',
        status: 'WAIT_USER_INPUT',
        summary: '等待集群选择',
        steps: [],
        wait_request: {
          request_id: 'wait_input',
          type: 'USER_INPUT',
          question: '请选择计算集群',
          response_schema: {
            type: 'object',
            properties: { cluster_name: { type: 'string', description: '计算集群' } },
            required: ['cluster_name'],
            additionalProperties: false,
          },
        },
        can_resume: true,
        updated_at: '2026-09-03T20:00:00+08:00',
      },
      attempts: {},
      attemptOrder: [],
    };
    const host = document.createElement('div');
    document.body.appendChild(host);
    const dispose = render(() => createComponent(PlanRuntimeCard, {
      toolCall: { toolUseId: 'plan_tool', toolName: 'start_template_plan', input: {}, status: 'done', executedBy: 'internal' },
      plan,
      onResume,
    }), host);

    const input = host.querySelector<HTMLInputElement>('.agent-ui-plan-wait-field input')!;
    const button = host.querySelector<HTMLButtonElement>('.agent-ui-plan-resume-button')!;
    expect(button.disabled).toBe(true);
    input.value = 'pangu-prod';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    expect(button.disabled).toBe(false);
    button.click();
    expect(onResume).toHaveBeenCalledWith('plan_input', 'wait_input', { cluster_name: 'pangu-prod' });
    dispose();
  });

  it('取消后重放服务端历史仍保留 Plan 已执行进度', () => {
    const reducer = new EventReducer();
    reducer.replayEvents([
      {
        type: 'tool_use_start',
        seq: 1,
        runId: 'run_outer',
        sessionId: 'session_1',
        stepIndex: 0,
        payload: { toolUseId: 'plan_tool', toolName: 'start_template_plan', toolInput: {}, executedBy: 'internal', status: 'running' },
      },
      {
        type: 'plan_view_update',
        seq: 2,
        runId: 'run_outer',
        sessionId: 'session_1',
        payload: {
          planExecutionId: 'plan_stopped',
          view: {
            plan_execution_id: 'plan_stopped',
            status: 'STOPPED',
            summary: 'Plan 已停止',
            steps: [{ step_id: 'inspect', step_order: 1, step_name: '检查数据', status: 'SUCCEEDED', summary: '检查完成' }],
            can_resume: true,
            updated_at: '2026-09-02T00:00:00+08:00',
          },
        },
      },
      {
        type: 'tool_use_end',
        seq: 3,
        runId: 'run_outer',
        sessionId: 'session_1',
        stepIndex: 0,
        payload: { toolUseId: 'plan_tool', content: 'react run cancelled', isError: true, executedBy: 'internal', status: 'error' },
      },
      {
        type: 'cancelled',
        seq: 4,
        runId: 'run_outer',
        sessionId: 'session_1',
        payload: { ok: true, reason: '本次运行已被用户取消' },
      },
    ]);

    const state = reducer.getState();
    expect(state.steps[0].toolCalls[0].planExecutionId).toBe('plan_stopped');
    expect(state.plans.plan_stopped.view.status).toBe('STOPPED');
    expect(state.plans.plan_stopped.view.steps[0].status).toBe('SUCCEEDED');
  });

  it('按服务端 status 将普通和 Plan Step 工具取消态映射为 cancelled', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'tool_use_start',
      seq: 1,
      runId: 'run_outer',
      stepIndex: 0,
      payload: { toolUseId: 'tool_outer', toolName: 'python_exec', toolInput: {}, executedBy: 'internal', status: 'running' },
    });
    reducer.applyEvent({
      type: 'tool_use_end',
      seq: 2,
      runId: 'run_outer',
      stepIndex: 0,
      payload: { toolUseId: 'tool_outer', content: '用户取消了本次运行', isError: true, status: 'cancelled' },
    });
    reducer.applyEvent({
      type: 'plan_step_event',
      seq: 3,
      runId: 'run_outer',
      payload: {
        planExecutionId: 'plan_cancelled',
        planVersionId: 'version_1',
        stepId: 'execute',
        stepOrder: 1,
        stepAttemptId: 'attempt_1',
        attemptNo: 1,
        stepRunId: 'run_step',
        event: {
          type: 'tool_use_start',
          seq: 1,
          runId: 'run_step',
          stepIndex: 0,
          payload: { toolUseId: 'tool_step', toolName: 'execute_tool', toolInput: {}, executedBy: 'server', status: 'running' },
        },
      },
    });
    reducer.applyEvent({
      type: 'plan_step_event',
      seq: 4,
      runId: 'run_outer',
      payload: {
        planExecutionId: 'plan_cancelled',
        planVersionId: 'version_1',
        stepId: 'execute',
        stepOrder: 1,
        stepAttemptId: 'attempt_1',
        attemptNo: 1,
        stepRunId: 'run_step',
        event: {
          type: 'tool_use_end',
          seq: 2,
          runId: 'run_step',
          stepIndex: 0,
          payload: { toolUseId: 'tool_step', content: '用户取消了本次运行', isError: true, status: 'cancelled' },
        },
      },
    });

    const state = reducer.getState();
    expect(state.steps[0].toolCalls[0].status).toBe('cancelled');
    expect(state.plans.plan_cancelled.attempts.attempt_1.steps[0].toolCalls[0].status).toBe('cancelled');
  });

  it('按服务端 status 将普通和 Plan Step 客户端工具取消态映射为 cancelled', () => {
    const reducer = new EventReducer();
    reducer.applyEvent({
      type: 'client_tool_use_start',
      seq: 1,
      runId: 'run_outer',
      stepIndex: 0,
      payload: { toolUseId: 'client_outer', toolName: 'edit_sql', toolInput: {}, status: 'waiting' },
    });
    reducer.applyEvent({
      type: 'client_tool_use_end',
      seq: 2,
      runId: 'run_outer',
      stepIndex: 0,
      payload: { toolOutputs: [{ toolUseId: 'client_outer', content: '用户取消了本次运行', isError: true, status: 'cancelled' }] },
    });
    reducer.applyEvent({
      type: 'client_tool_use_start',
      seq: 3,
      runId: 'run_step',
      stepIndex: 0,
      payload: {
        toolUseId: 'client_step', toolName: 'edit_sql', toolInput: {}, status: 'waiting',
        planExecutionId: 'plan_client_cancelled', stepAttemptId: 'attempt_client',
      },
    });
    reducer.applyEvent({
      type: 'client_tool_use_end',
      seq: 4,
      runId: 'run_step',
      stepIndex: 0,
      payload: {
        planExecutionId: 'plan_client_cancelled', stepAttemptId: 'attempt_client',
        toolOutputs: [{ toolUseId: 'client_step', content: '用户取消了本次运行', isError: true, status: 'cancelled' }],
      },
    });

    const state = reducer.getState();
    expect(state.steps[0].toolCalls[0].status).toBe('cancelled');
    expect(state.plans.plan_client_cancelled.attempts.attempt_client.steps[0].toolCalls[0].status).toBe('cancelled');
  });
});
