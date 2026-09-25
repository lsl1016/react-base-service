/**
 * 事件流聚合器
 *
 * 将后端 WebSocket 推送的离散事件流（ReactEvent）聚合成 UI 可用的 AgentState。
 *
 * 事件处理映射表：
 * ┌──────────────────────┬─────────────────────────────────────────────────┐
 * │ 事件类型              │ 状态变更                                         │
 * ├──────────────────────┼─────────────────────────────────────────────────┤
 * │ thought_start        │ 新建 step，status → running                     │
 * │ thought_delta        │ 追加内容到 step.thoughts                         │
 * │ thought_end          │ 用完整内容覆盖 step.thoughts，标记 complete        │
 * │ content_start        │ 确保 step 存在，status → running                 │
 * │ content_delta        │ 追加内容到 step.content                          │
 * │ content_end          │ 用完整内容覆盖 step.content，标记 complete         │
 * │ tool_use_start       │ 往 step.toolCalls 推入 ToolCallState(running)    │
 * │ tool_use_end         │ 更新对应 ToolCallState 的 status/result/duration │
 * │ client_tool_use_start│ 往 step.toolCalls 推入 ToolCallState(waiting)    │
 * │                      │ status → waiting_client_tool                     │
 * │ client_tool_use_end  │ 更新对应 ToolCallState 的 status/result           │
 * │ compact_start        │ status → compacting                             │
 * │ compact_end          │ 记录压缩结果，status → running                    │
 * │ todo_update          │ 更新 todos，并同步最近一次完整 Todo 卡片            │
 * │ done                 │ status → done，累计 token 到 usage               │
 * │ error                │ status → error，记录 error                       │
 * │ cancelled            │ status → cancelled                              │
 * │ heartbeat            │ 无状态变更（心跳保活）                              │
 * └──────────────────────┴─────────────────────────────────────────────────┘
 *
 * 状态生命周期：
 * - 新 run 开始时调用 resetForNewRun()：保留已有消息，只重置当前运行状态
 * - 切换会话时调用 replayEvents()：从头重放历史事件，最终标记 status=idle
 * - 每次状态变更后通过 notify() 通知所有 subscribers
 */
import type {
    CancelledPayload,
    ClientToolUseEndPayload,
    ClientToolUseStartPayload,
  ReactToolConfirmRequestPayload,
    CompactEndPayload,
    ContentDeltaPayload,
    ContentEndPayload,
    DonePayload,
    ErrorPayload,
    ModelFallbackPayload,
    PlanAttemptItem,
    PlanPublicView,
    PlanStepEventPayload,
    PlanViewUpdatePayload,
    ReactEvent,
    ReactNoticeDrainedPayload,
    ReactSteerDiscardedPayload,
    ReactSteerDrainedPayload,
    ReactSteerReceiptPayload,
    RunPayload,
    ThoughtDeltaPayload,
    ThoughtEndPayload,
    TodoItem,
    TodoUpdatePayload,
    ToolUseEndPayload,
    ToolUseStartPayload,
} from '../protocol/types';
import { RECOVERY_CONTINUE_FLAG } from '../protocol/types';
import type { AgentState, PlanAttemptState, PlanRuntimeState, Step, ToolCallState } from './types';

function createInitialState(): AgentState {
  return {
    status: 'idle',
    connected: false,
    sessionId: null,
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
    lastModelFallback: null,
    steerState: null,
    plans: {},
  };
}

function cloneStep(step: Step): Step {
  return {
    ...step,
    displayParts: step.displayParts?.map((part) => ({ ...part })),
    compact: step.compact ? { ...step.compact } : undefined,
    notice: step.notice ? { ...step.notice } : undefined,
    toolCalls: step.toolCalls.map((toolCall) => ({
      ...toolCall,
      input: { ...toolCall.input },
      meta: toolCall.meta ? { ...toolCall.meta } : undefined,
      todoItems: toolCall.todoItems?.map((todo) => ({ ...todo })),
    })),
  };
}

/** 把 steer 系列与 notice_drained 事件渲染为 main lane 的系统标记 step（不透传 agentPath）。 */
function buildSteerNoticeStep(index: number, event: ReactEvent, text: string, detail?: string, count?: number): Step {
  return {
    index,
    runId: event.runId ?? '',
    role: 'notice',
    thoughts: '',
    content: '',
    toolCalls: [],
    thoughtComplete: true,
    contentStarted: false,
    contentComplete: true,
    notice: { text, detail, count },
  };
}

/** steer 准入回执事件的卡片文案。 */
function steerReceiptNoticeText(type: string, p?: ReactSteerReceiptPayload): string {
  switch (type) {
    case 'steer_guided':
      return '你的消息已作为引导注入运行中的对话，将在下一个模型轮生效';
    case 'steer_queued':
      return `当前对话正忙，消息已加入队列${readNumber(p?.queueLength, 0) > 0 ? `（第 ${p?.queueLength} 位）` : ''}`;
    case 'steer_rejected':
      return `消息未被接收：${steerReasonText(p?.reason)}`;
    default:
      return type;
  }
}

/** 准入拒绝/排队原因的中文文案。 */
function steerReasonText(reason?: string): string {
  switch (reason) {
    case 'run_not_steerable':
      return '对话正在等待你的输入或收尾，暂不能引导';
    case 'soft_landing':
      return '对话正在收尾，暂不能引导';
    case 'attachments_unsupported':
      return '带附件的消息不支持引导注入';
    default:
      return reason ?? 'unknown';
  }
}

function clonePlanPublicView(view: PlanPublicView): PlanPublicView {
  return {
    ...view,
    steps: view.steps.map((step) => ({
      ...step,
      public_fields: step.public_fields ? { ...step.public_fields } : undefined,
      step_result_ref: step.step_result_ref ? { ...step.step_result_ref } : undefined,
    })),
    current_step: view.current_step ? { ...view.current_step } : undefined,
    wait_request: view.wait_request
      ? { ...view.wait_request, response_schema: view.wait_request.response_schema ? { ...view.wait_request.response_schema } : undefined, pending_tool_use_ids: [...(view.wait_request.pending_tool_use_ids ?? [])] }
      : undefined,
    result: view.result ? { ...view.result } : undefined,
    error: view.error ? { ...view.error } : undefined,
  };
}

function cloneAgentState(state: AgentState): AgentState {
  return {
    ...state,
    steps: state.steps.map((step) => ({
      ...step,
      displayParts: step.displayParts?.map((part) => ({ ...part })),
      compact: step.compact ? { ...step.compact } : undefined,
      toolCalls: step.toolCalls.map((toolCall) => ({
        ...toolCall,
        input: { ...toolCall.input },
        todoItems: toolCall.todoItems?.map((todo) => ({ ...todo })),
      })),
    })),
    todos: state.todos.map((todo) => ({ ...todo })),
    usage: { ...state.usage },
    lastRunStats: state.lastRunStats ? { ...state.lastRunStats } : null,
    compactState: state.compactState ? { ...state.compactState } : null,
    lastModelFallback: state.lastModelFallback ? { ...state.lastModelFallback } : null,
    steerState: state.steerState ? { ...state.steerState } : null,
    plans: Object.fromEntries(Object.entries(state.plans).map(([planExecutionId, plan]) => [
      planExecutionId,
      {
        ...plan,
        view: clonePlanPublicView(plan.view),
        attemptOrder: [...plan.attemptOrder],
        attempts: Object.fromEntries(Object.entries(plan.attempts).map(([attemptId, attempt]) => [
          attemptId,
          {
            ...attempt,
            steps: attempt.steps.map(cloneStep),
          },
        ])),
      },
    ])),
  };
}

function isInternalCompactPrompt(prompt?: string): boolean {
  return typeof prompt === 'string'
    && prompt.startsWith('You are compacting conversation history for future model turns.');
}

/** 这条 run 是否为掉线自动续跑发出的：其用户消息不展示，使恢复过程对用户无感。 */
function isRecoveryContinueRun(payload?: RunPayload): boolean {
  return payload?.controlContext?.[RECOVERY_CONTINUE_FLAG] === true;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

function isTodoStatus(value: unknown): value is TodoItem['status'] {
  return value === 'pending'
    || value === 'in_progress'
    || value === 'completed'
    || value === 'cancelled';
}

function readNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback;
}

function resolveToolCallStatus(status: string | undefined, isError: boolean | undefined): ToolCallState['status'] {
  switch (status) {
    case 'cancelled':
      return 'cancelled';
    case 'error':
      return 'error';
    case 'success':
      return 'done';
    default:
      return isError ? 'error' : 'done';
  }
}

function formatClientToolOutputContent(value: unknown): string {
  if (typeof value === 'string') return value;
  if (value == null) return '';
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function normalizeTodoItem(value: unknown, index: number, existing?: TodoItem): TodoItem | null {
  if (!isRecord(value)) return null;

  const now = Date.now();
  const id = typeof value.id === 'string' && value.id ? value.id : existing?.id ?? `todo_${index}`;
  const content = typeof value.content === 'string' ? value.content : existing?.content;
  if (!content) return null;

  return {
    id,
    content,
    status: isTodoStatus(value.status) ? value.status : existing?.status ?? 'pending',
    createdAt: readNumber(value.createdAt, existing?.createdAt ?? now),
    updatedAt: readNumber(value.updatedAt, now),
  };
}

function readTodoItems(payload: TodoUpdatePayload): unknown[] {
  const directItems = (payload as unknown as Record<string, unknown>).todos;
  if (Array.isArray(directItems)) return directItems;

  const todoState = payload.todoState;
  const stateItems = isRecord(todoState) ? todoState.items : undefined;
  return Array.isArray(stateItems) ? stateItems : [];
}

function readTodoMerge(payload: TodoUpdatePayload): boolean {
  const record = payload as unknown as Record<string, unknown>;
  if (typeof record.merge === 'boolean') return record.merge;

  const todoState = payload.todoState;
  return isRecord(todoState) && typeof todoState.merge === 'boolean' ? todoState.merge : false;
}

function replaceTodoItems(items: unknown[]): TodoItem[] {
  return items
    .map((item, index) => normalizeTodoItem(item, index))
    .filter((item): item is TodoItem => !!item);
}

function mergeTodoItems(currentItems: TodoItem[], incomingItems: unknown[]): TodoItem[] {
  const incomingById = new Map<string, TodoItem>();
  const incomingOrder: TodoItem[] = [];

  incomingItems.forEach((item, index) => {
    const record = isRecord(item) && typeof item.id === 'string'
      ? currentItems.find((current) => current.id === item.id)
      : undefined;
    const normalized = normalizeTodoItem(item, index, record);
    if (!normalized) return;

    incomingById.set(normalized.id, normalized);
    incomingOrder.push(normalized);
  });

  const merged = currentItems.map((item) => incomingById.get(item.id) ?? item);
  const existingIds = new Set(merged.map((item) => item.id));

  incomingOrder.forEach((item) => {
    if (!existingIds.has(item.id)) {
      merged.push(item);
      existingIds.add(item.id);
    }
  });

  return merged;
}

function isFullTodoWriteToolCall(toolCall: ToolCallState): boolean {
  const name = toolCall.toolName.toLowerCase().replace(/[\s_-]/g, '');
  return name === 'todowrite' && toolCall.input.merge === false;
}

function syncLatestTodoCardItems(state: AgentState): void {
  for (let stepIndex = state.steps.length - 1; stepIndex >= 0; stepIndex -= 1) {
    const toolCalls = state.steps[stepIndex].toolCalls;
    for (let toolIndex = toolCalls.length - 1; toolIndex >= 0; toolIndex -= 1) {
      const toolCall = toolCalls[toolIndex];
      if (!isFullTodoWriteToolCall(toolCall)) continue;
      toolCall.todoItems = state.todos.map((todo) => ({ ...todo }));
      return;
    }
  }
}

function isCreatePlanToolCall(toolCall: ToolCallState): boolean {
  return toolCall.toolName === 'create_plan';
}

function markPendingPlansAcceptedByRun(state: AgentState, runId?: string): void {
  if (!runId) return;
  for (const step of state.steps) {
    if (step.runId === runId) continue;
    for (const toolCall of step.toolCalls) {
      if (isCreatePlanToolCall(toolCall) && toolCall.planConfirmationStatus !== 'accepted') {
        toolCall.planConfirmationStatus = 'accepted';
      }
    }
  }
}

function isPlanExecutionToolName(name: string): boolean {
  return name === 'start_template_plan' || name === 'resume_template_plan';
}

function parsePlanPublicView(content?: string): PlanPublicView | undefined {
  if (!content) return undefined;
  try {
    const value = JSON.parse(content) as unknown;
    if (!isRecord(value) || typeof value.plan_execution_id !== 'string' || !Array.isArray(value.steps)) {
      return undefined;
    }
    return value as unknown as PlanPublicView;
  } catch {
    return undefined;
  }
}

function upsertPlanView(state: AgentState, view: PlanPublicView, outerRunId?: string): PlanRuntimeState {
  const planExecutionId = view.plan_execution_id;
  const current = state.plans[planExecutionId];
  const plan: PlanRuntimeState = current ?? {
    planExecutionId,
    outerRunId,
    view: clonePlanPublicView(view),
    attempts: {},
    attemptOrder: [],
  };
  if (outerRunId) plan.outerRunId = outerRunId;
  plan.view = clonePlanPublicView(view);
  for (const step of view.steps) {
    for (const attempt of Object.values(plan.attempts)) {
      if (attempt.stepId === step.step_id) attempt.status = step.status;
    }
  }
  state.plans[planExecutionId] = plan;
  return plan;
}

function ensurePlanAttempt(state: AgentState, payload: PlanStepEventPayload): PlanAttemptState {
  let plan = state.plans[payload.planExecutionId];
  if (!plan) {
    plan = {
      planExecutionId: payload.planExecutionId,
      view: {
        plan_execution_id: payload.planExecutionId,
        status: 'RUNNING',
        summary: 'Plan 正在执行',
        steps: [],
        can_resume: false,
        updated_at: new Date().toISOString(),
      },
      attempts: {},
      attemptOrder: [],
    };
    state.plans[payload.planExecutionId] = plan;
  }
  let attempt = plan.attempts[payload.stepAttemptId];
  if (!attempt) {
    attempt = {
      planExecutionId: payload.planExecutionId,
      stepId: payload.stepId,
      stepOrder: payload.stepOrder,
      stepAttemptId: payload.stepAttemptId,
      attemptNo: payload.attemptNo,
      stepRunId: payload.stepRunId,
      status: 'RUNNING',
      steps: [],
    };
    plan.attempts[payload.stepAttemptId] = attempt;
    plan.attemptOrder.push(payload.stepAttemptId);
  }
  return attempt;
}

function ensureAttemptStep(attempt: PlanAttemptState, stepIndex?: number, runId?: string): Step {
  const index = stepIndex ?? attempt.steps.length;
  let step = attempt.steps.find((item) => item.index === index && item.runId === (runId ?? attempt.stepRunId));
  if (!step) {
    step = {
      index,
      runId: runId ?? attempt.stepRunId,
      role: 'assistant',
      thoughts: '',
      content: '',
      toolCalls: [],
      thoughtComplete: false,
      contentStarted: false,
      contentComplete: false,
    };
    attempt.steps.push(step);
  }
  return step;
}

function findAttemptTool(attempt: PlanAttemptState, toolUseId: string): ToolCallState | undefined {
  for (let index = attempt.steps.length - 1; index >= 0; index -= 1) {
    const tool = attempt.steps[index].toolCalls.find((item) => item.toolUseId === toolUseId);
    if (tool) return tool;
  }
  return undefined;
}

function reducePlanAttemptEvent(state: AgentState, payload: PlanStepEventPayload): void {
  const attempt = ensurePlanAttempt(state, payload);
  const event = payload.event;
  const step = ensureAttemptStep(attempt, event.stepIndex, event.runId ?? payload.stepRunId);
  switch (event.type) {
    case 'thought_start':
      break;
    case 'thought_delta':
      step.thoughts += (event.payload as unknown as ThoughtDeltaPayload | undefined)?.contentDelta ?? '';
      break;
    case 'thought_end': {
      const value = event.payload as unknown as ThoughtEndPayload | undefined;
      if (value?.content != null) step.thoughts = value.content;
      step.thoughtComplete = true;
      break;
    }
    case 'content_start':
      step.contentStarted = true;
      break;
    case 'content_delta':
      step.content += (event.payload as unknown as ContentDeltaPayload | undefined)?.contentDelta ?? '';
      break;
    case 'content_end': {
      const value = event.payload as unknown as ContentEndPayload | undefined;
      if (value?.content != null) step.content = value.content;
      step.contentComplete = true;
      break;
    }
    case 'tool_use_start': {
      const value = event.payload as unknown as ToolUseStartPayload;
      if (!findAttemptTool(attempt, value.toolUseId)) {
        step.toolCalls.push({
          toolUseId: value.toolUseId,
          toolName: value.toolName,
          description: value.description,
          input: value.toolInput ?? {},
          status: value.status === 'waiting' ? 'waiting' : 'running',
          executedBy: value.executedBy ?? 'server',
          afterContent: step.contentStarted,
          planExecutionId: payload.planExecutionId,
          stepAttemptId: payload.stepAttemptId,
        });
      }
      break;
    }
    case 'tool_use_end': {
      const value = event.payload as unknown as ToolUseEndPayload;
      const tool = findAttemptTool(attempt, value.toolUseId);
      if (tool) {
        tool.status = resolveToolCallStatus(value.status, value.isError);
        tool.result = value.content;
        tool.isError = value.isError;
        tool.durationMs = value.durationMs;
      }
      break;
    }
    case 'compact_end': {
      const value = event.payload as unknown as CompactEndPayload;
      step.compact = {
        beforeMessageCount: value.beforeMessageCount ?? 0,
        afterMessageCount: value.afterMessageCount ?? 0,
        summary: value.summary ?? '',
      };
      break;
    }
    case 'model_fallback': {
      const value = event.payload as unknown as ModelFallbackPayload | undefined;
      if (value?.resetCurrentOutput) {
        step.thoughts = '';
        step.content = '';
        step.thoughtComplete = false;
        step.contentStarted = false;
        step.contentComplete = false;
      }
      break;
    }
    case 'done':
      break;
    case 'error':
      step.isError = true;
      break;
    case 'cancelled':
      break;
    default:
      break;
  }
}

function reducePlanClientToolStart(state: AgentState, event: ReactEvent, payload: ClientToolUseStartPayload): void {
  if (!payload.planExecutionId || !payload.stepAttemptId) return;
  const plan = state.plans[payload.planExecutionId];
  const existing = plan?.attempts[payload.stepAttemptId];
  const attempt = existing ?? ensurePlanAttempt(state, {
    planExecutionId: payload.planExecutionId,
    planVersionId: '',
    stepId: plan?.view.current_step?.step_id ?? '',
    stepOrder: plan?.view.current_step?.step_order ?? 0,
    stepAttemptId: payload.stepAttemptId,
    attemptNo: 1,
    stepRunId: event.runId ?? '',
    event,
  });
  const step = ensureAttemptStep(attempt, event.stepIndex, event.runId);
  if (!findAttemptTool(attempt, payload.toolUseId)) {
    step.toolCalls.push({
      toolUseId: payload.toolUseId,
      toolName: payload.toolName,
      description: payload.description,
      frontendHint: payload.frontendHint,
      input: payload.toolInput ?? {},
      status: 'waiting',
      executedBy: 'client',
      afterContent: step.contentStarted,
      planExecutionId: payload.planExecutionId,
      stepAttemptId: payload.stepAttemptId,
    });
  }
}

function reducePlanClientToolEnd(state: AgentState, payload: ClientToolUseEndPayload): void {
  if (!payload.planExecutionId || !payload.stepAttemptId) return;
  const attempt = state.plans[payload.planExecutionId]?.attempts[payload.stepAttemptId];
  if (!attempt) return;
  for (const output of payload.toolOutputs ?? []) {
    const tool = findAttemptTool(attempt, output.toolUseId);
    if (!tool) continue;
    tool.status = resolveToolCallStatus(output.status, output.isError);
    tool.result = formatClientToolOutputContent(output.content);
    tool.meta = output.meta;
    tool.isError = output.isError;
  }
}

export class EventReducer {
  private state: AgentState;
  private listeners = new Set<(_state: AgentState) => void>();
  /** toolUseId → { stepIndex, toolIndex }，用于 tool_use_end 时快速定位对应的 ToolCallState */
  private toolCallIndex = new Map<string, { stepIndex: number; toolIndex: number }>();

  constructor(initial?: Partial<AgentState>) {
    this.state = { ...createInitialState(), ...initial };
  }

  getState(): AgentState {
    return cloneAgentState(this.state);
  }

  subscribe(fn: (state: AgentState) => void): () => void {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  }

  /**
   * 更新连接状态
   *
   * 由 AgentClient 在 WebSocket 连接/断开时调用。
   */
  setConnected(connected: boolean): void {
    if (this.state.connected === connected) {
      return;
    }
    this.state.connected = connected;
    this.notify();
  }

  /**
   * 为新的 run 准备状态
   *
   * 保留已有消息、sessionId、累计 usage、todos，只重置当前运行状态。
   * 每次调用 agent.run() 前应先调用此方法。
   */
  resetForNewRun(): void {
    this.state = {
      ...this.state,
      status: 'running',
      currentRunId: null,
      compactState: null,
      lastModelFallback: null,
    };
    this.toolCallIndex.clear();
    this.notify();
  }

  /** 将当前待确认计划标记为发送中。手动输入和计划按钮共用该入口。 */
  markPendingPlanSubmitting(): void {
    let changed = false;
    for (let stepIndex = this.state.steps.length - 1; stepIndex >= 0; stepIndex -= 1) {
      const toolCalls = this.state.steps[stepIndex].toolCalls;
      for (let toolIndex = toolCalls.length - 1; toolIndex >= 0; toolIndex -= 1) {
        const toolCall = toolCalls[toolIndex];
        if (!isCreatePlanToolCall(toolCall) || toolCall.planConfirmationStatus === 'accepted') {
          continue;
        }
        toolCall.planConfirmationStatus = 'submitting';
        changed = true;
        if (changed) {
          this.notify();
        }
        return;
      }
    }
  }

  /** 未拿到 runId 且发送重试耗尽时，恢复本次计划确认按钮。 */
  markSubmittingPlanSendFailed(): void {
    let changed = false;
    for (const step of this.state.steps) {
      for (const toolCall of step.toolCalls) {
        if (isCreatePlanToolCall(toolCall) && toolCall.planConfirmationStatus === 'submitting') {
          toolCall.planConfirmationStatus = 'pending';
          changed = true;
        }
      }
    }
    if (changed) {
      this.notify();
    }
  }

  /**
   * 清空当前对话。
   *
   * 用于用户主动清理面板时，重置所有对话内容和运行态，只保留连接状态。
   */
  clearConversation(): void {
    this.state = {
      ...createInitialState(),
      connected: this.state.connected,
    };
    this.toolCallIndex.clear();
    this.notify();
  }

  /**
   * 重放历史事件
   *
   * 用于切换会话时，从 session/events 接口获取历史事件后重建状态。
   * 重放完毕后强制 status=idle（历史会话已结束）。
   */
  replayEvents(events: ReactEvent[]): void {
    const currentConnected = this.state.connected;
    this.state = createInitialState();
    this.state.connected = currentConnected;
    this.toolCallIndex.clear();
    for (const event of events) {
      this.reduceEvent(event);
    }
    this.settlePendingToolCalls();
    this.state.status = 'idle';
    this.state.currentRunId = null;
    this.notify();
  }

  /**
   * 应用单个事件并通知 subscribers
   */
  applyEvent(event: ReactEvent): void {
    if (event.type === 'heartbeat') {
      return;
    }
    this.reduceEvent(event);
    this.notify();
  }

  /** Plan Resume/Retry/Skip 通过 WS 启动新的执行片段时，先把输入区和 Plan 卡片置为运行态。 */
  markPlanCommandRunning(): void {
    this.state.status = 'running';
    this.state.compactState = null;
    this.notify();
  }

  /** 合并 HTTP 详情接口返回的 Plan 最新视图和 Attempt 索引。 */
  applyPlanDetail(view: PlanPublicView, attempts: PlanAttemptItem[]): void {
    const plan = upsertPlanView(this.state, view);
    for (const item of attempts) {
      if (!plan.attempts[item.stepAttemptId]) {
        plan.attempts[item.stepAttemptId] = {
          planExecutionId: item.planExecutionId,
          stepId: item.stepId,
          stepOrder: view.steps.find((step) => step.step_id === item.stepId)?.step_order ?? 0,
          stepAttemptId: item.stepAttemptId,
          attemptNo: item.attemptNo,
          stepRunId: item.stepRunId ?? '',
          status: item.status,
          steps: [],
        };
        plan.attemptOrder.push(item.stepAttemptId);
      } else {
        plan.attempts[item.stepAttemptId].status = item.status;
      }
    }
    this.notify();
  }

  /** 将持久化 StepAttempt 事件回放到 Plan 独立详情状态，不污染 outer 对话。 */
  applyPlanStepEvents(planExecutionId: string, stepId: string, stepAttemptId: string, attemptNo: number, events: ReactEvent[]): void {
    const plan = this.state.plans[planExecutionId];
    const stepOrder = plan?.view.steps.find((step) => step.step_id === stepId)?.step_order ?? 0;
    const existing = plan?.attempts[stepAttemptId];
    if (existing) existing.steps = [];
    const orderedEvents = events
      .map((event, index) => ({ event, index }))
      .sort((left, right) => left.event.seq - right.event.seq || left.index - right.index)
      .map(({ event }) => event);
    for (const event of orderedEvents) {
      reducePlanAttemptEvent(this.state, {
        planExecutionId,
        planVersionId: '',
        stepId,
        stepOrder,
        stepAttemptId,
        attemptNo,
        stepRunId: event.runId ?? existing?.stepRunId ?? '',
        event,
      });
    }
    this.notify();
  }

  /**
   * 添加用户消息步骤到账本
   *
   * 由 AgentClient 在发送 run 消息前调用，确保用户输入先成为账本事实。
   */
  addUserStep(content: string, runId?: string, displayParts?: Step['displayParts']): void {
    const step: Step = {
      index: this.state.steps.length,
      runId: runId ?? this.state.currentRunId ?? '',
      role: 'user',
      thoughts: '',
      content,
      displayParts,
      toolCalls: [],
      thoughtComplete: false,
      contentStarted: false,
      contentComplete: true,
    };
    this.state.steps.push(step);
    this.notify();
  }

  /**
   * 将进行中的 run 标记为中断（终止态）
   *
   * 仅在当前确有 run 进行中时生效：把状态打成 error，并插入一条中断提示步骤。
   * 用于 WS 断连等场景——本次回答无法继续，按用户预期视为中断，
   * 按钮回到发送态。非进行中状态调用此方法为 no-op。
   */
  markRunInterrupted(message: string): void {
    if (
      this.state.status !== 'running'
      && this.state.status !== 'waiting_client_tool'
      && this.state.status !== 'compacting'
      && this.state.status !== 'recovering'
    ) {
      return;
    }

    this.state.status = 'error';
    this.settlePendingToolCalls();
    const step: Step = {
      index: this.state.steps.length,
      runId: this.state.currentRunId ?? '',
      role: 'assistant',
      thoughts: '',
      content: message,
      toolCalls: [],
      thoughtComplete: false,
      contentStarted: false,
      contentComplete: true,
      isError: true,
    };
    this.state.steps.push(step);
    this.notify();
  }

  /**
   * 将进行中的 run 标记为「恢复中」
   *
   * WS 异常断连且有 run 在进行时调用：不打成终止态、不清空已生成内容，
   * 只把 status 切到 recovering，由 AgentClient 在重连后尝试自动续跑。
   * 保留 currentRunId，供续跑流程识别需要检查终态的旧 run。非进行中状态为 no-op。
   */
  markRecovering(): void {
    if (
      this.state.status !== 'running'
      && this.state.status !== 'waiting_client_tool'
      && this.state.status !== 'compacting'
    ) {
      return;
    }
    // 旧 run 已随连接断开而失效，收敛其未完成的工具调用，避免恢复期间/续跑后残留旋转态。
    this.settlePendingToolCalls();
    this.state.status = 'recovering';
    this.notify();
  }

  /**
   * 中止「恢复中」态（用户在恢复期间点击停止）
   *
   * 收敛未完成的工具调用，回到 idle 发送态，保留已生成内容。仅在 recovering 时生效。
   */
  abortRecovering(): void {
    if (this.state.status !== 'recovering') {
      return;
    }
    this.settlePendingToolCalls();
    this.state.status = 'idle';
    this.state.currentRunId = null;
    this.notify();
  }

  /**
   * 添加错误步骤到账本
   *
   * 由 AgentClient 在捕获本地异常时调用。
   */
  addErrorStep(message: string, runId?: string): void {
    const step: Step = {
      index: this.state.steps.length,
      runId: runId ?? this.state.currentRunId ?? '',
      role: 'assistant',
      thoughts: '',
      content: message,
      toolCalls: [],
      thoughtComplete: false,
      contentStarted: false,
      contentComplete: true,
      isError: true,
    };
    this.state.steps.push(step);
    this.notify();
  }

  private reduceEvent(event: ReactEvent): void {
    markPendingPlansAcceptedByRun(this.state, event.runId);
    if (event.sessionId) {
      this.state.sessionId = event.sessionId;
    }
    // 子 Agent run（agentPath 非空）不移动外层 run 游标：外层状态判定始终以父 run 事件为准。
    if (event.runId && !event.agentPath) {
      this.state.currentRunId = event.runId;
    }

    const stepIndex = event.stepIndex;

    switch (event.type) {
      case 'run': {
        const p = event.payload as unknown as RunPayload;
        if (p?.userPrompt && !isInternalCompactPrompt(p.userPrompt) && !isRecoveryContinueRun(p)) {
          const step: Step = {
            index: this.state.steps.length,
            runId: event.runId ?? this.state.currentRunId ?? '',
            agentPath: event.agentPath,
            role: 'user',
            thoughts: '',
            content: p.userPrompt,
            toolCalls: [],
            thoughtComplete: false,
            contentStarted: false,
            contentComplete: true,
          };
          this.state.steps.push(step);
        }
        this.state.status = 'running';
        break;
      }

      case 'thought_start':
        this.ensureStep(stepIndex, event.runId!, event.agentPath);
        this.state.status = 'running';
        break;

      case 'thought_delta': {
        const step = this.ensureStep(stepIndex, event.runId, event.agentPath);
        const p = event.payload as unknown as ThoughtDeltaPayload;
        if (p?.contentDelta) {
          step.thoughts += p.contentDelta;
        }
        break;
      }

      case 'thought_end': {
        const step = this.ensureStep(stepIndex, event.runId, event.agentPath);
        const p = event.payload as unknown as ThoughtEndPayload;
        if (p?.content != null) {
          step.thoughts = p.content;
        }
        step.thoughtComplete = true;
        if (p) {
          this.applyRunStatsSnapshot(p);
        }
        break;
      }

      case 'content_start': {
        const step = this.ensureStep(stepIndex, event.runId!, event.agentPath);
        step.contentStarted = true;
        this.state.status = 'running';
        break;
      }

      case 'content_delta': {
        const step = this.ensureStep(stepIndex, event.runId, event.agentPath);
        const p = event.payload as unknown as ContentDeltaPayload;
        if (p?.contentDelta) {
          step.content += p.contentDelta;
        }
        break;
      }

      case 'content_end': {
        const step = this.ensureStep(stepIndex, event.runId, event.agentPath);
        const p = event.payload as unknown as ContentEndPayload;
        if (p?.content != null) {
          step.content = p.content;
        }
        step.contentComplete = true;
        if (p) {
          this.applyRunStatsSnapshot(p);
        }
        break;
      }

      case 'tool_use_start': {
        const step = this.ensureStep(stepIndex, event.runId!, event.agentPath);
        const p = event.payload as unknown as ToolUseStartPayload;
        const tc: ToolCallState = {
          toolUseId: p.toolUseId,
          toolName: p.toolName,
          description: p.description,
          input: p.toolInput ?? {},
          // ask_question 等待用户作答时后端下发 status='waiting'，其余服务端/内置工具为 running
          status: p.status === 'waiting' ? 'waiting' : 'running',
          executedBy: p.executedBy ?? 'server',
          afterContent: step.contentStarted,
          planConfirmationStatus: p.toolName === 'create_plan' ? 'pending' : undefined,
          agentPath: event.agentPath,
        };
        step.toolCalls.push(tc);
        this.toolCallIndex.set(p.toolUseId, {
          stepIndex: this.state.steps.indexOf(step),
          toolIndex: step.toolCalls.length - 1,
        });
        this.state.status = 'running';
        break;
      }

      case 'tool_confirm_request': {
        const p = event.payload as unknown as ReactToolConfirmRequestPayload;
        const loc = this.toolCallIndex.get(p.toolUseId);
        if (loc) {
          const step = this.state.steps[loc.stepIndex];
          if (step) {
            const tc = step.toolCalls[loc.toolIndex];
            if (tc) {
              tc.status = 'waiting';
              tc.confirmReason = p.reason || p.mode;
            }
          }
        }
        this.state.status = 'waiting_client_tool';
        break;
      }

      case 'tool_use_end': {
        const p = event.payload as unknown as ToolUseEndPayload;
        const loc = this.toolCallIndex.get(p.toolUseId);
        if (loc) {
          const step = this.state.steps[loc.stepIndex];
          if (step) {
            const tc = step.toolCalls[loc.toolIndex];
            tc.status = resolveToolCallStatus(p.status, p.isError);
            tc.result = p.content;
            tc.isError = p.isError;
            tc.durationMs = p.durationMs;
            tc.confirmReason = undefined;
            if (isPlanExecutionToolName(tc.toolName)) {
              const view = parsePlanPublicView(p.content);
              if (view) {
                tc.planExecutionId = view.plan_execution_id;
                upsertPlanView(this.state, view);
              }
            }
          }
        }
        break;
      }

      case 'client_tool_use_start': {
        const p = event.payload as unknown as ClientToolUseStartPayload;
        if (p.planExecutionId && p.stepAttemptId) {
          reducePlanClientToolStart(this.state, event, p);
          this.state.status = 'waiting_client_tool';
          break;
        }
        const step = this.ensureStep(stepIndex, event.runId!, event.agentPath);
        const tc: ToolCallState = {
          toolUseId: p.toolUseId,
          toolName: p.toolName,
          description: p.description,
          frontendHint: p.frontendHint,
          input: p.toolInput ?? {},
          status: 'waiting',
          executedBy: 'client',
          afterContent: step.contentStarted,
        };
        step.toolCalls.push(tc);
        this.toolCallIndex.set(p.toolUseId, {
          stepIndex: this.state.steps.indexOf(step),
          toolIndex: step.toolCalls.length - 1,
        });
        this.state.status = 'waiting_client_tool';
        break;
      }

      case 'client_tool_use_end': {
        const p = event.payload as unknown as ClientToolUseEndPayload;
        if (p.planExecutionId && p.stepAttemptId) {
          reducePlanClientToolEnd(this.state, p);
          this.state.status = 'running';
          break;
        }
        for (const output of p?.toolOutputs ?? []) {
          const loc = this.toolCallIndex.get(output.toolUseId);
          if (!loc) continue;

          const step = this.state.steps[loc.stepIndex];
          const tc = step?.toolCalls[loc.toolIndex];
          if (!tc) continue;

          tc.status = resolveToolCallStatus(output.status, output.isError);
          tc.result = formatClientToolOutputContent(output.content);
          tc.meta = output.meta;
          tc.isError = output.isError;
        }
        this.state.status = 'running';
        break;
      }

      case 'compact_start':
        this.state.status = 'compacting';
        break;

      case 'compact_end': {
        const p = event.payload as unknown as CompactEndPayload;
        this.state.compactState = {
          beforeMessageCount: p.beforeMessageCount,
          afterMessageCount: p.afterMessageCount,
          summary: p.summary,
        };
        // 在消息流里插入一条压缩分隔标记，实时与历史回放都能看到压缩发生过
        const compactStep: Step = {
          index: this.state.steps.length,
          runId: event.runId ?? this.state.currentRunId ?? '',
          role: 'compact',
          thoughts: '',
          content: '',
          toolCalls: [],
          thoughtComplete: true,
          contentStarted: false,
          contentComplete: true,
          compact: {
            beforeMessageCount: p.beforeMessageCount ?? 0,
            afterMessageCount: p.afterMessageCount ?? 0,
            summary: p.summary ?? '',
          },
        };
        this.state.steps.push(compactStep);
        this.state.status = 'running';
        break;
      }

      // ─── Steering / 通知邮箱（S1-S3/Q1-A1）───────────────────────────
      // 系统标记 step 一律不透传 event.agentPath：无论事件来自哪个 lane 都落 main，
      // 保证引导/队列/通知的可见性不依赖事件归属（子 run 亦然）。
      case 'steer_guided':
      case 'steer_queued':
      case 'steer_rejected': {
        const p = event.payload as unknown as ReactSteerReceiptPayload;
        this.state.steerState = {
          kind: p?.kind ?? event.type.replace('steer_', ''),
          queueLength: p?.queueLength,
          reason: p?.reason,
          pendingInputId: p?.pendingInputId,
          runId: event.runId,
        };
        this.state.steps.push(buildSteerNoticeStep(this.state.steps.length, event, steerReceiptNoticeText(event.type, p)));
        break;
      }

      case 'steer_drained': {
        const p = event.payload as unknown as ReactSteerDrainedPayload;
        this.state.steerState = {
          kind: 'drained',
          pendingInputId: p?.pendingInputId,
          runId: event.runId,
        };
        this.state.steps.push(
          buildSteerNoticeStep(this.state.steps.length, event, `已注入新一轮对话${p?.pendingInputId ? `（${p.pendingInputId}）` : ''}`),
        );
        break;
      }

      case 'steer_delivery_changed': {
        const p = event.payload as unknown as ReactSteerDiscardedPayload;
        this.state.steerState = { kind: 'delivery_changed', reason: p?.reason, runId: event.runId };
        this.state.steps.push(
          buildSteerNoticeStep(this.state.steps.length, event, `${readNumber(p?.count, 0)} 条未消费引导已转入队列（本轮对话结束）`),
        );
        break;
      }

      case 'steer_discarded': {
        const p = event.payload as unknown as ReactSteerDiscardedPayload;
        this.state.steerState = { kind: 'discarded', reason: p?.reason, runId: event.runId };
        this.state.steps.push(
          buildSteerNoticeStep(this.state.steps.length, event, `${readNumber(p?.count, 0)} 条未消费输入已作废（${p?.reason ?? 'unknown'}）`),
        );
        break;
      }

      case 'notice_drained': {
        const p = event.payload as unknown as ReactNoticeDrainedPayload;
        this.state.steerState = { kind: 'notice', runId: event.runId };
        this.state.steps.push(
          buildSteerNoticeStep(
            this.state.steps.length,
            event,
            `后台任务通知已送达${readNumber(p?.count, 0) > 1 ? `（${p?.count} 条）` : ''}`,
            p?.content,
            readNumber(p?.count, 0),
          ),
        );
        break;
      }

      case 'todo_update': {
        const p = event.payload as unknown as TodoUpdatePayload;
        if (p) {
          const items = readTodoItems(p);
          this.state.todos = readTodoMerge(p)
            ? mergeTodoItems(this.state.todos, items)
            : replaceTodoItems(items);
          syncLatestTodoCardItems(this.state);
        }
        break;
      }

      case 'plan_view_update': {
        const p = event.payload as unknown as PlanViewUpdatePayload;
        if (p?.view?.plan_execution_id) {
          if (event.runId) {
            for (let index = this.state.steps.length - 1; index >= 0; index -= 1) {
              const candidate = this.state.steps[index];
              if (candidate.role !== 'user') continue;
              if (!candidate.runId) candidate.runId = event.runId;
              break;
            }
          }
          upsertPlanView(this.state, p.view, event.runId);
          // Plan WAIT 是持久化暂停点：服务端当前 goroutine 已结束，不应继续把输入区锁在 running。
          // Resume/Retry/Skip 会显式把 SDK 重新置为 running；Plan 完成仍由 done/error/cancelled 收敛。
          if (p.view.status === 'WAIT_USER_INPUT'
            || p.view.status === 'WAIT_USER_ACTION'
            || p.view.status === 'WAIT_EXTERNAL_TASK') {
            this.state.status = 'idle';
          } else if (p.view.status === 'PLANNING' || p.view.status === 'RUNNING') {
            this.state.status = 'running';
          }
          for (let stepPos = this.state.steps.length - 1; stepPos >= 0; stepPos -= 1) {
            const calls = this.state.steps[stepPos].toolCalls;
            const call = [...calls].reverse().find((item) => (
              isPlanExecutionToolName(item.toolName)
              && (!item.planExecutionId || item.planExecutionId === p.planExecutionId)
            ));
            if (call) {
              call.planExecutionId = p.planExecutionId;
              break;
            }
          }
        }
        break;
      }

      case 'plan_step_event': {
        const p = event.payload as unknown as PlanStepEventPayload;
        if (p?.planExecutionId && p?.stepAttemptId && p.event) {
          reducePlanAttemptEvent(this.state, p);
          if (p.event.type === 'thought_end' || p.event.type === 'content_end') {
            const stats = p.event.payload as unknown as ThoughtEndPayload | ContentEndPayload | undefined;
            if (stats) {
              this.applyRunStatsSnapshot(stats);
            }
          }
        }
        break;
      }

      case 'model_fallback': {
        const p = event.payload as unknown as ModelFallbackPayload;
        this.state.lastModelFallback = p ? { ...p } : null;
        if (p?.resetCurrentOutput) {
          const step = this.ensureStep(stepIndex, event.runId, event.agentPath);
          step.thoughts = '';
          step.content = '';
          step.thoughtComplete = false;
          step.contentStarted = false;
          step.contentComplete = false;
        }
        this.state.status = 'running';
        break;
      }

      case 'done': {
        const p = event.payload as unknown as DonePayload;
        if (event.agentPath) {
          // 子 Agent run 终态：只收敛该子 run 自己的工具卡片；token 并入会话总量，
          // 不改变外层 status / lastRunStats / runCount（外层 run 仍会继续产生事件）。
          this.settlePendingToolCalls(event.runId);
          if (p) {
            this.state.usage = {
              ...this.state.usage,
              totalInputTokens: this.state.usage.totalInputTokens + readNumber(p.inputTokens, 0),
              totalOutputTokens: this.state.usage.totalOutputTokens + readNumber(p.outputTokens, 0),
            };
          }
          break;
        }
        this.settlePendingToolCalls();
        this.state.status = 'done';
        if (p) {
          this.state.usage = {
            totalInputTokens: this.state.usage.totalInputTokens + (p.inputTokens ?? 0),
            totalOutputTokens: this.state.usage.totalOutputTokens + (p.outputTokens ?? 0),
            totalCacheReadTokens: this.state.usage.totalCacheReadTokens + (p.cacheReadTokens ?? 0),
            totalCacheCreateTokens: this.state.usage.totalCacheCreateTokens + (p.cacheCreateTokens ?? 0),
            runCount: this.state.usage.runCount + 1,
          };
          this.state.lastRunStats = {
            inputTokens: readNumber(p.inputTokens, 0),
            outputTokens: readNumber(p.outputTokens, 0),
            cacheReadTokens: readNumber(p.cacheReadTokens, 0),
            cacheCreateTokens: readNumber(p.cacheCreateTokens, 0),
            contextUsedTokens: readNumber(p.contextUsedTokens, 0),
            maxContextTokens: readNumber(p.maxContextTokens, 0),
          };
        }
        break;
      }

      case 'error': {
        const p = event.payload as unknown as ErrorPayload;
        if (event.agentPath) {
          // 子 Agent run 失败：收敛其卡片并记日志，不打断外层 run 状态。
          this.settlePendingToolCalls(event.runId);
          if (p?.errMsg) {
            console.warn('[AgentClient] sub-agent run failed:', p.errMsg);
          }
          break;
        }
        this.settlePendingToolCalls();
        this.state.status = 'error';
        this.applyContextOnlyStats(p?.contextUsedTokens, p?.maxContextTokens);
        // run 失败不再往聊天流插报错消息：后端 errMsg 是原始错误串，对用户没有
        // 可操作性；断连失败更是会在任意一次全量回放中被重建出来，展示它等于把
        // 一次本可无感的掉线暴露给用户。status='error' 已让输入框回到可发送态。
        if (p?.errMsg) {
          console.warn('[AgentClient] run failed:', p.errMsg);
        }
        break;
      }

      case 'cancelled': {
        const p = event.payload as unknown as CancelledPayload;
        if (event.agentPath) {
          this.settlePendingToolCalls(event.runId);
          break;
        }
        this.state.status = 'cancelled';
        this.settlePendingToolCalls();
        this.applyContextOnlyStats(p?.contextUsedTokens, p?.maxContextTokens);
        if (p?.reason) {
          const cancelStep: Step = {
            index: this.state.steps.length,
            runId: this.state.currentRunId ?? '',
            role: 'assistant',
            thoughts: '',
            content: p.reason,
            toolCalls: [],
            thoughtComplete: false,
            contentStarted: false,
            contentComplete: true,
            isError: true,
          };
          this.state.steps.push(cancelStep);
        }
        break;
      }

      case 'heartbeat':
        break;
    }
  }

  /**
   * 用一份携带 token 的事件载荷刷新最近一轮统计（content_end / done 使用）
   *
   * 实时流与历史回放的 content_end 都带 token，正常会更新；
   * 仅当载荷确实没有 token 数据（maxContextTokens<=0 且 inputTokens<=0）时跳过，
   * 作为兜底避免个别无 token 的快照把已有指标清零。
   */
  private applyRunStatsSnapshot(p: {
    inputTokens?: number;
    outputTokens?: number;
    cacheReadTokens?: number;
    cacheCreateTokens?: number;
    contextUsedTokens?: number;
    maxContextTokens?: number;
  }): void {
    if (readNumber(p.maxContextTokens, 0) <= 0 && readNumber(p.inputTokens, 0) <= 0) {
      return;
    }
    this.state.lastRunStats = {
      inputTokens: readNumber(p.inputTokens, 0),
      outputTokens: readNumber(p.outputTokens, 0),
      cacheReadTokens: readNumber(p.cacheReadTokens, 0),
      cacheCreateTokens: readNumber(p.cacheCreateTokens, 0),
      contextUsedTokens: readNumber(p.contextUsedTokens, 0),
      maxContextTokens: readNumber(p.maxContextTokens, 0),
    };
  }

  /**
   * 仅更新上下文窗口统计（error/cancelled 终态使用）
   *
   * 这类终态拿不到本轮的 input/cache token，只能拿到上下文窗口占用；
   * 因此仅覆盖上下文窗口字段，缓存/输入相关字段沿用上一轮成功 run 的值，
   * 这样输入框上的缓存占比不会因为一次报错或取消而被清零。
   */
  private applyContextOnlyStats(contextUsedTokens?: number, maxContextTokens?: number): void {
    if (contextUsedTokens == null && maxContextTokens == null) {
      return;
    }
    const prev = this.state.lastRunStats;
    this.state.lastRunStats = {
      inputTokens: prev?.inputTokens ?? 0,
      outputTokens: prev?.outputTokens ?? 0,
      cacheReadTokens: prev?.cacheReadTokens ?? 0,
      cacheCreateTokens: prev?.cacheCreateTokens ?? 0,
      contextUsedTokens: readNumber(contextUsedTokens, prev?.contextUsedTokens ?? 0),
      maxContextTokens: readNumber(maxContextTokens, prev?.maxContextTokens ?? 0),
    };
  }

  /**
   * 按 runId + stepIndex 查找或创建助手 Step
   *
   * 后端的 stepIndex 在每个 run 内独立递增，因此不能当成全局展示顺序。
   * 消息展示顺序依赖追加顺序，避免新一轮回复覆盖或插到旧消息前面。
   */
  private ensureStep(index: number | undefined, runId?: string, agentPath?: string): Step {
    const idx = index ?? this.state.steps.length;
    const normalizedRunId = runId ?? this.state.currentRunId ?? '';
    let step = this.state.steps.find((s) => {
      if (s.role !== 'assistant' || s.index !== idx) return false;
      return normalizedRunId ? s.runId === normalizedRunId : true;
    });
    if (!step) {
      step = {
        index: idx,
        runId: normalizedRunId,
        agentPath,
        role: 'assistant',
        thoughts: '',
        content: '',
        toolCalls: [],
        thoughtComplete: false,
        contentStarted: false,
        contentComplete: false,
      };
      this.state.steps.push(step);
    }
    return step;
  }

  private settlePendingToolCalls(runId?: string): void {
    // runId 非空时只收敛该子 run 自己的步骤/工具（子 Agent 终态不得误收敛外层在途卡片，
    // 如正在执行的 delegate_agent 调用卡片）。
    for (const step of this.state.steps) {
      if (runId && step.runId !== runId) continue;
      for (const toolCall of step.toolCalls) {
        if (toolCall.status === 'running' || toolCall.status === 'waiting') {
          toolCall.status = 'cancelled';
        }
      }
    }
    for (const plan of Object.values(this.state.plans)) {
      for (const attempt of Object.values(plan.attempts)) {
        for (const step of attempt.steps) {
          if (runId && step.runId !== runId) continue;
          for (const toolCall of step.toolCalls) {
            if (toolCall.status === 'running' || toolCall.status === 'waiting') {
              toolCall.status = 'cancelled';
            }
          }
        }
      }
    }
  }

  private notify(): void {
    const snapshot = cloneAgentState(this.state);
    for (const fn of this.listeners) {
      fn(snapshot);
    }
  }
}
