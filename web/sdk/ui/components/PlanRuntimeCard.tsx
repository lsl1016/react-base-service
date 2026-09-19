import { For, Show, createEffect, createMemo, createSignal } from 'solid-js';
import IconMdiChevronDown from '~icons/mdi/chevron-down';
import IconMdiChevronRight from '~icons/mdi/chevron-right';
import type { PlanPublicView } from '../../protocol/types';
import type { PlanAttemptState, PlanRuntimeState, ToolCallState } from '../../runtime/types';
import type { ClientTool } from '../../tools/types';
import { ContentBlock } from './ContentBlock';
import { ThoughtBlock } from './ThoughtBlock';
import { ToolCallView } from './ToolCallView';

export interface PlanRuntimeCardProps {
  toolCall: ToolCallState;
  /** Plan 的共享最新视图和懒加载执行详情。 */
  plan?: PlanRuntimeState;
  /** 该卡片是否使用共享 Plan 的最新公开视图；旧卡片只使用自己的工具结果快照。 */
  useLiveView?: boolean;
  disabled?: boolean;
  resolveTool?: (toolName: string, frontendHint?: string) => ClientTool | undefined;
  onResume?: (planExecutionId: string, waitRequestId: string, response: Record<string, unknown>) => void;
  onRetry?: (planExecutionId: string, stepId: string) => void;
  onSkip?: (planExecutionId: string, stepId: string) => void;
  onCancel?: (planExecutionId: string) => void;
  onLoadPlan?: (planExecutionId: string) => void | Promise<void>;
  onLoadAttempt?: (planExecutionId: string, stepAttemptId: string) => void | Promise<void>;
}

const STATUS_LABELS: Record<string, string> = {
  CREATED: '准备中',
  PLANNING: '规划中',
  RUNNING: '执行中',
  PENDING: '等待执行',
  WAITING: '等待中',
  WAIT_USER_INPUT: '等待补充信息',
  WAIT_USER_ACTION: '等待确认',
  WAIT_EXTERNAL_TASK: '等待外部任务',
  STOPPED: '已停止',
  CANCELLED: '已取消',
  SKIPPED: '已跳过',
  SUCCEEDED: '已完成',
  FAILED: '执行失败',
  INVALIDATED: '结果已失效',
};

function parseView(result?: string): PlanPublicView | undefined {
  if (!result) return undefined;
  try {
    const value = JSON.parse(result) as PlanPublicView;
    return value?.plan_execution_id && Array.isArray(value.steps) ? value : undefined;
  } catch {
    return undefined;
  }
}

function shouldInitiallyCollapse(status?: string): boolean {
  return status === 'SUCCEEDED';
}

function statusMark(status?: string): string {
  switch (status) {
    case 'SUCCEEDED': return '✓';
    case 'FAILED': return '×';
    case 'RUNNING': return '●';
    case 'WAIT_USER_INPUT':
    case 'WAIT_USER_ACTION': return '?';
    case 'WAIT_EXTERNAL_TASK':
    case 'WAITING': return '…';
    case 'STOPPED':
    case 'CANCELLED': return 'Ⅱ';
    case 'SKIPPED': return '↷';
    case 'INVALIDATED': return '↺';
    default: return '○';
  }
}

type WaitResponseField = {
  name: string;
  required: boolean;
  schema: Record<string, unknown>;
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

function waitResponseFields(request?: PlanPublicView['wait_request']): WaitResponseField[] {
  const responseSchema = request?.response_schema;
  if (!isRecord(responseSchema) || !isRecord(responseSchema.properties)) return [];
  const required = new Set(Array.isArray(responseSchema.required)
    ? responseSchema.required.filter((item): item is string => typeof item === 'string')
    : []);
  return Object.entries(responseSchema.properties).flatMap(([name, schema]) => (
    isRecord(schema) ? [{ name, required: required.has(name), schema }] : []
  ));
}

function parseWaitResponseValue(field: WaitResponseField, raw: string): unknown {
  const type = field.schema.type;
  if (type === 'boolean') return raw === 'true';
  if (type === 'integer' || type === 'number') return Number(raw);
  if (type === 'array' || type === 'object') return JSON.parse(raw);
  return raw;
}

function isWaitResponseValueValid(field: WaitResponseField, raw: string): boolean {
  if (!raw.trim()) return !field.required;
  try {
    const value = parseWaitResponseValue(field, raw);
    if (field.schema.type === 'number' && !Number.isFinite(value)) return false;
    if (field.schema.type === 'integer' && (!Number.isFinite(value) || !Number.isInteger(value))) return false;
    return true;
  } catch {
    return false;
  }
}

function waitResponseOptions(field: WaitResponseField): unknown[] {
  return Array.isArray(field.schema.enum) ? field.schema.enum : [];
}

function waitResponsePlaceholder(field: WaitResponseField): string {
  if (field.schema.type === 'array') return '请输入 JSON 数组';
  if (field.schema.type === 'object') return '请输入 JSON 对象';
  return `请输入 ${field.name}`;
}

export function splitPlanRoundToolCalls(toolCalls: ToolCallState[]): {
  beforeContent: ToolCallState[];
  afterContent: ToolCallState[];
} {
  return {
    beforeContent: toolCalls.filter((toolCall) => !toolCall.afterContent),
    afterContent: toolCalls.filter((toolCall) => toolCall.afterContent),
  };
}

function PlanAttemptDetail(props: {
  attempt: PlanAttemptState;
  resolveTool?: PlanRuntimeCardProps['resolveTool'];
  onLoad?: () => void | Promise<void>;
}) {
  const [loading, setLoading] = createSignal(false);
  const ensureLoaded = async () => {
    if (props.attempt.steps.length > 0 || !props.onLoad || loading()) return;
    setLoading(true);
    try {
      await props.onLoad();
    } finally {
      setLoading(false);
    }
  };

  return (
    <details class="agent-ui-plan-attempt" onToggle={(event) => {
      if ((event.currentTarget as HTMLDetailsElement).open) void ensureLoaded();
    }}>
      <summary>
        第 {props.attempt.attemptNo} 次执行 · {STATUS_LABELS[props.attempt.status ?? ''] ?? props.attempt.status ?? '未知状态'}
      </summary>
      <div class="agent-ui-plan-attempt-body">
        <Show when={!loading()} fallback={<div class="agent-ui-plan-detail-empty">正在加载执行详情…</div>}>
          <Show when={props.attempt.steps.length > 0} fallback={<div class="agent-ui-plan-detail-empty">暂无可展示的执行详情</div>}>
            <For each={props.attempt.steps}>
              {(round) => {
                const orderedTools = () => splitPlanRoundToolCalls(round.toolCalls);
                return (
                  <div class="agent-ui-plan-round">
                    <Show when={round.thoughts.trim()}>
                      <ThoughtBlock content={round.thoughts} complete={round.thoughtComplete} defaultCollapsed />
                    </Show>
                    <For each={orderedTools().beforeContent}>
                      {(toolCall) => (
                        <ToolCallView toolCall={toolCall} resolveTool={props.resolveTool} />
                      )}
                    </For>
                    <Show when={round.contentStarted || round.content.trim()}>
                      <ContentBlock content={round.content} complete={round.contentComplete} />
                    </Show>
                    <For each={orderedTools().afterContent}>
                      {(toolCall) => (
                        <ToolCallView toolCall={toolCall} resolveTool={props.resolveTool} />
                      )}
                    </For>
                  </div>
                );
              }}
            </For>
          </Show>
        </Show>
      </div>
    </details>
  );
}

export function PlanRuntimeCard(props: PlanRuntimeCardProps) {
  const historicalView = createMemo(() => parseView(props.toolCall.result));
  const view = createMemo(() => (
    props.useLiveView === false
      ? historicalView()
      : props.plan?.view ?? historicalView()
  ));
  const isResumeRunning = createMemo(() => (
    props.useLiveView !== false
    && props.toolCall.toolName === 'resume_template_plan'
    && props.toolCall.status === 'running'
  ));
  const isResumingUserWait = createMemo(() => (
    isResumeRunning()
    && (view()?.status === 'WAIT_USER_INPUT' || view()?.status === 'WAIT_USER_ACTION')
    && !!view()?.wait_request
  ));
  const commandFailed = createMemo(() => props.toolCall.status === 'error' && !view());
  const displayStatus = createMemo(() => {
    if (isResumeRunning()) return 'RUNNING';
    if (commandFailed()) return 'FAILED';
    if (view()?.status) return view()!.status;
    if (props.toolCall.status === 'running') return 'RUNNING';
    if (props.toolCall.status === 'cancelled') return 'STOPPED';
    return 'CREATED';
  });
  const displayTitle = createMemo(() => {
    if (isResumeRunning()) return props.toolCall.description || '正在恢复 Plan';
    if (!commandFailed()) return view()?.summary || props.toolCall.description || '正在创建 Plan';
    return props.toolCall.toolName === 'resume_template_plan' ? 'Plan 恢复失败' : 'Plan 启动失败';
  });
  const [collapsed, setCollapsed] = createSignal(shouldInitiallyCollapse(view()?.status));
  const [expandedSteps, setExpandedSteps] = createSignal<Record<string, boolean>>({});
  const [responseValues, setResponseValues] = createSignal<Record<string, string>>({});
  const [submitting, setSubmitting] = createSignal(false);

  createEffect(() => {
    if (props.useLiveView !== false && !props.disabled
      && (view()?.status === 'WAIT_USER_INPUT' || view()?.status === 'WAIT_USER_ACTION')) {
      setSubmitting(false);
    }
  });

  const attemptsForStep = (stepId: string) => {
    const plan = props.plan;
    if (!plan) return [];
    return plan.attemptOrder
      .map((id) => plan.attempts[id])
      .filter((attempt): attempt is PlanAttemptState => !!attempt && attempt.stepId === stepId)
      .sort((left, right) => right.attemptNo - left.attemptNo);
  };

  const completedCount = () => view()?.steps.filter((step) => step.status === 'SUCCEEDED').length ?? 0;
  const waitRequest = () => view()?.wait_request;
  let lastWaitRequestId = waitRequest()?.request_id;
  createEffect(() => {
    const requestId = waitRequest()?.request_id;
    if (requestId !== lastWaitRequestId) {
      lastWaitRequestId = requestId;
      setResponseValues({});
      setSubmitting(false);
    }
  });
  const responseFields = () => waitResponseFields(waitRequest());
  const isUserAction = () => waitRequest()?.type === 'USER_ACTION';
  const canSubmit = () => (
    props.useLiveView !== false
    && !props.disabled
    && !!props.onResume
    && !submitting()
    && (view()?.status === 'WAIT_USER_INPUT' || view()?.status === 'WAIT_USER_ACTION')
    && !!view()?.plan_execution_id
    && !!waitRequest()?.request_id
    && (isUserAction() || (
      waitRequest()?.type === 'USER_INPUT'
      && responseFields().length > 0
      && responseFields().every((field) => isWaitResponseValueValid(field, responseValues()[field.name] ?? ''))
    ))
  );

  const submitWaitResponse = () => {
    const currentView = view();
    const request = waitRequest();
    if (!currentView || !request?.request_id || !canSubmit()) return;
    const response = isUserAction()
      ? { approved: true }
      : Object.fromEntries(responseFields().flatMap((field) => {
        const raw = (responseValues()[field.name] ?? '').trim();
        return raw ? [[field.name, parseWaitResponseValue(field, raw)]] : [];
      }));
    setSubmitting(true);
    props.onResume?.(currentView.plan_execution_id, request.request_id, response);
  };

  const rejectUserAction = () => {
    const currentView = view();
    const request = waitRequest();
    if (!currentView || !request?.request_id || props.useLiveView === false || props.disabled || submitting()) return;
    setSubmitting(true);
    props.onResume?.(currentView.plan_execution_id, request.request_id, { approved: false });
  };

  const canRetryStep = (status: string) => (
    props.useLiveView !== false
    && !props.disabled
    && !!props.onRetry
    && status === 'FAILED'
  );

  const canSkipStep = (step: PlanPublicView['steps'][number]) => (
    props.useLiveView !== false
    && !props.disabled
    && !!props.onSkip
    && step.required === false
    && ['PENDING', 'FAILED', 'WAITING', 'WAIT_USER_INPUT', 'WAIT_USER_ACTION'].includes(step.status)
  );

  const canCancelPlan = () => (
    props.useLiveView !== false
    && !props.disabled
    && !!props.onCancel
    && !!view()?.plan_execution_id
    && !['SUCCEEDED', 'CANCELLED'].includes(view()?.status ?? '')
  );

  const toggleCollapsed = () => {
    const next = !collapsed();
    setCollapsed(next);
    const planExecutionId = view()?.plan_execution_id;
    if (!next && planExecutionId) void props.onLoadPlan?.(planExecutionId);
  };

  return (
    <div class="agent-ui-plan-runtime-card" data-status={displayStatus()}>
      <button type="button" class="agent-ui-plan-runtime-header" onClick={toggleCollapsed}>
        <span class="agent-ui-plan-runtime-chevron">
          <Show when={collapsed()} fallback={<IconMdiChevronDown width="18" height="18" />}>
            <IconMdiChevronRight width="18" height="18" />
          </Show>
        </span>
        <span class="agent-ui-plan-runtime-heading">
          <span class="agent-ui-plan-runtime-title">{displayTitle()}</span>
          <span class="agent-ui-plan-runtime-subtitle">
            {STATUS_LABELS[displayStatus()] ?? displayStatus()}
            <Show when={(view()?.steps.length ?? 0) > 0}> · {completedCount()}/{view()!.steps.length} 步</Show>
            <Show when={view()?.current_step?.step_name}> · 当前：{view()!.current_step!.step_name}</Show>
          </span>
        </span>
      </button>

      <Show when={!collapsed()}>
        <div class="agent-ui-plan-runtime-body">
          <Show
            when={!commandFailed()}
            fallback={<div class="agent-ui-plan-error">{props.toolCall.result || 'Plan 命令执行失败，请稍后重试。'}</div>}
          >
            <Show when={view()} fallback={<div class="agent-ui-plan-detail-empty">Plan 正在初始化，等待第一份进度数据…</div>}>
              {(currentView) => (
                <>
                <div class="agent-ui-plan-step-list">
                  <For each={currentView().steps}>
                    {(step) => {
                      const expanded = () => expandedSteps()[step.step_id] === true;
                      return (
                        <div class="agent-ui-plan-step" data-status={step.status}>
                          <button type="button" class="agent-ui-plan-step-header" onClick={() => {
                            const nextExpanded = !expanded();
                            setExpandedSteps((current) => ({ ...current, [step.step_id]: nextExpanded }));
                            if (nextExpanded && attemptsForStep(step.step_id).length === 0) {
                              void props.onLoadPlan?.(currentView().plan_execution_id);
                            }
                          }}>
                            <span class="agent-ui-plan-step-mark">{statusMark(step.status)}</span>
                            <span class="agent-ui-plan-step-main">
                              <span class="agent-ui-plan-step-name">{step.step_name || step.step_id}</span>
                              <span class="agent-ui-plan-step-summary">{step.summary || STATUS_LABELS[step.status] || step.status}</span>
                            </span>
                            <span class="agent-ui-plan-step-status">{STATUS_LABELS[step.status] || step.status}</span>
                          </button>
                          <Show when={expanded()}>
                            <div class="agent-ui-plan-step-detail">
                              <Show when={step.public_fields && Object.keys(step.public_fields).length > 0}>
                                <pre>{JSON.stringify(step.public_fields, null, 2)}</pre>
                              </Show>
                              <For each={attemptsForStep(step.step_id)}>
                                {(attempt) => (
                                  <PlanAttemptDetail
                                    attempt={attempt}
                                    resolveTool={props.resolveTool}
                                    onLoad={() => props.onLoadAttempt?.(currentView().plan_execution_id, attempt.stepAttemptId)}
                                  />
                                )}
                              </For>
                              <Show when={canRetryStep(step.status) || canSkipStep(step)}>
                                <div class="agent-ui-plan-step-actions">
                                  <Show when={canRetryStep(step.status)}>
                                    <button
                                      type="button"
                                      onClick={() => props.onRetry?.(currentView().plan_execution_id, step.step_id)}
                                    >
                                      重试此步骤
                                    </button>
                                  </Show>
                                  <Show when={canSkipStep(step)}>
                                    <button
                                      type="button"
                                      onClick={() => props.onSkip?.(currentView().plan_execution_id, step.step_id)}
                                    >
                                      跳过此步骤
                                    </button>
                                  </Show>
                                </div>
                              </Show>
                            </div>
                          </Show>
                        </div>
                      );
                    }}
                  </For>
                </div>

                <Show when={isResumingUserWait()}>
                  <div class="agent-ui-plan-wait-card" data-wait-type="RESUMING">
                    <div class="agent-ui-plan-wait-title">正在恢复执行</div>
                    <div class="agent-ui-plan-detail-empty">等待请求已处理，Plan 正在继续执行。</div>
                  </div>
                </Show>

                <Show when={!isResumingUserWait() && (currentView().status === 'WAIT_USER_INPUT' || currentView().status === 'WAIT_USER_ACTION') && waitRequest()}>
                  {(request) => (
                    <div class="agent-ui-plan-wait-card" data-wait-type={request().type}>
                      <div class="agent-ui-plan-wait-title">
                        {request().type === 'USER_ACTION' ? '需要完成操作' : '需要补充信息'}
                      </div>
                      <div class="agent-ui-plan-wait-question">{request().question || currentView().summary}</div>
                      <Show when={request().type === 'USER_INPUT'}>
                        <For each={responseFields()}>
                          {(field) => (
                            <label class="agent-ui-plan-wait-field">
                              <span>{typeof field.schema.description === 'string' ? field.schema.description : field.name}</span>
                              <Show
                                when={field.schema.type === 'boolean' || waitResponseOptions(field).length > 0}
                                fallback={(
                                  <input
                                    value={responseValues()[field.name] ?? ''}
                                    disabled={props.useLiveView === false || props.disabled || submitting()}
                                    onInput={(event) => setResponseValues((current) => ({ ...current, [field.name]: event.currentTarget.value }))}
                                    placeholder={waitResponsePlaceholder(field)}
                                  />
                                )}
                              >
                                <select
                                  value={responseValues()[field.name] ?? ''}
                                  disabled={props.useLiveView === false || props.disabled || submitting()}
                                  onChange={(event) => setResponseValues((current) => ({ ...current, [field.name]: event.currentTarget.value }))}
                                >
                                  <option value="">请选择</option>
                                  <For each={waitResponseOptions(field).length > 0 ? waitResponseOptions(field) : [true, false]}>
                                    {(option) => <option value={String(option)}>{String(option)}</option>}
                                  </For>
                                </select>
                              </Show>
                            </label>
                          )}
                        </For>
                        <Show when={responseFields().length === 0}>
                          <div class="agent-ui-plan-detail-empty">等待请求未声明可填写的数据结构，无法继续。</div>
                        </Show>
                      </Show>
                      <Show
                        when={request().type === 'USER_ACTION'}
                        fallback={(
                          <button type="button" class="agent-ui-plan-resume-button" disabled={!canSubmit()} onClick={submitWaitResponse}>
                            {submitting() ? '提交中…' : '提交并继续'}
                          </button>
                        )}
                      >
                        <div class="agent-ui-plan-wait-actions">
                          <button
                            type="button"
                            class="agent-ui-plan-secondary-button"
                            disabled={props.disabled || submitting()}
                            onClick={rejectUserAction}
                          >
                            拒绝并取消
                          </button>
                          <button
                            type="button"
                            class="agent-ui-plan-resume-button"
                            disabled={!canSubmit()}
                            onClick={submitWaitResponse}
                          >
                            {submitting() ? '提交中…' : '确认执行'}
                          </button>
                        </div>
                      </Show>
                    </div>
                  )}
                </Show>

                <Show when={currentView().status === 'WAIT_EXTERNAL_TASK'}>
                  <div class="agent-ui-plan-wait-card" data-wait-type="EXTERNAL_TASK">
                    <div class="agent-ui-plan-wait-title">外部任务执行中</div>
                    <div class="agent-ui-plan-wait-question">{waitRequest()?.question || currentView().summary || '任务正在执行，请等待任务完成。'}</div>
                    <Show when={waitRequest()?.request_id && props.onResume && !props.disabled}>
                      <button
                        type="button"
                        class="agent-ui-plan-resume-button"
                        onClick={() => props.onResume?.(
                          currentView().plan_execution_id,
                          waitRequest()!.request_id!,
                          {},
                        )}
                      >
                        任务已完成，继续执行
                      </button>
                    </Show>
                  </div>
                </Show>

                <Show when={currentView().result && Object.keys(currentView().result ?? {}).length > 0}>
                  <details class="agent-ui-plan-result">
                    <summary>查看 Plan 结果</summary>
                    <pre>{JSON.stringify(currentView().result, null, 2)}</pre>
                  </details>
                </Show>
                <Show when={currentView().error}>
                  <div class="agent-ui-plan-error">{currentView().error?.summary}</div>
                </Show>
                <Show when={canCancelPlan()}>
                  <div class="agent-ui-plan-runtime-actions">
                    <button
                      type="button"
                      class="agent-ui-plan-secondary-button"
                      onClick={() => props.onCancel?.(currentView().plan_execution_id)}
                    >
                      取消 Plan
                    </button>
                  </div>
                </Show>
                </>
              )}
            </Show>
          </Show>
        </div>
      </Show>
    </div>
  );
}

export default PlanRuntimeCard;
