/**
 * SessionRuntime —— 每会话独立运行时（docs/plan/20260930_每会话独立WS连接与会话状态分片方案.md）
 *
 * 一个会话一份：自己的 WebSocket 连接、自己的事件状态机（EventReducer）、
 * 自己的 seq 游标与掉线续跑状态。AgentClient 退化为 runtime 管理器（共享
 * 配置/REST/工具注册表/IndexedDB 账本），对外 API 作用于"当前视图 runtime"。
 *
 * 由此获得的能力：
 *   - 切换会话 = 换视图指针，不触碰任何连接——原会话 run 继续在后台执行，
 *     事件持续写入本地账本与该 runtime 的状态（切回即见）；
 *   - 单页面多会话并行：每个会话各占一条连接，后端"单连接单 run"语义天然成立，
 *     seq 对齐（per-run 计数）无需任何改动。
 *
 * 生命周期：草稿（sessionId=null）在首个 run 事件带回真实 sessionId 后由
 * AgentClient 转正（rekey）；空闲后台会话由 AgentClient 调度延迟断开
 * （canDisconnectIdle 兜底确认无 run/等待/恢复中，绝不误杀）。
 */
import { WsClient } from '../client/ws-client';
import { RECOVERY_CONTINUE_FLAG } from '../protocol/types';
import type { AskQuestionAnswerContent, ClientToolUseStartPayload, ReactEvent, RunPayload, WsMessageType } from '../protocol/types';
import type { HistoryEvent } from '../session/types';
import type { EventLedger } from '../storage/event-ledger';
import type { ClientToolRegistry } from '../tools/registry';
import { ClientToolExecutor } from '../tools/executor';
import { EventReducer } from './event-reducer';
import { SessionEventAssembler } from './session-event-assembler';
import type { LlmContext } from '../protocol/types';
import type { AgentClientConfig, RunOptions } from './client-types';

function isPromiseLike<T>(value: T | Promise<T>): value is Promise<T> {
  return !!value && typeof (value as Promise<T>).then === 'function';
}

/** 掉线续跑：单次恢复内查询旧 run 终态的最大轮询次数，超过则把本轮打成中断 */
const MAX_RECOVERY_POLL_ATTEMPTS = 6;
/**
 * 掉线续跑：连续自动续跑的最大次数，超过则不再自动续跑、交回用户手动重试。
 *
 * 计的是「连续几次自动续跑都没能让一轮回答跑完」：网络持续抖动时，每次断连都会
 * 开一条新的恢复链，若不设此上限就会无限发「继续」——每次都是一整轮模型推理，
 * 且续跑时模型看不到自己上一次的半截输出（后端不把 AssistantPartial 入模），
 * 往往从头重答，代价远高于一次 WS 重连。
 */
const MAX_CONSECUTIVE_RECOVERY_RUNS = 4;
/** 掉线续跑：每次重试前的退避间隔（ms），首次立即，其后递增；索引即已尝试次数 */
const RECOVERY_BACKOFF_MS = [0, 300, 600, 1200, 2400, 4800];
/** 发送阶段自动重连的最大尝试次数，超过后才将本轮标记为失败 */
const MAX_PENDING_RUN_RECONNECT_ATTEMPTS = 4;
/** 掉线续跑：自动续跑发给后端的提示语 */
const RECOVERY_CONTINUE_PROMPT = '继续';

/** AgentClient 注入给 runtime 的共享依赖与回调 */
export interface SessionRuntimeDeps {
  /** 初始归属会话；null 表示新会话草稿（首个 run 事件带回真实 id 后转正） */
  sessionId: string | null;
  /** WS 端点（各 runtime 同 URL，各自建连） */
  wsUrl: string;
  /** 宿主配置（只读；callerKey/模型/hooks/控制参数） */
  config: AgentClientConfig;
  /** 共享客户端工具注册表（executor per-runtime，注册表全局一份） */
  registry: ClientToolRegistry;
  /** 共享 IndexedDB 账本（按 session 存取，天然分片） */
  ledger: EventLedger;
  /** 会话历史同步（client 级 REST + 账本覆盖，含"目标 runtime run 中则跳过覆盖"保护） */
  syncSessionEvents: (sessionId: string) => Promise<HistoryEvent[]>;
  /** reducer 状态变化回调；client 过滤"是否当前视图 runtime"后分发外部 subscribers */
  onStateChange: (runtime: SessionRuntime) => void;
  /** 草稿 runtime 拿到真实 sessionId 后的转正钩子（client 负责 Map rekey 与冲突合并） */
  onSessionResolved: (runtime: SessionRuntime, sessionId: string) => void;
  /** run 终态事件回调；client 用于刷新当前会话异步任务面板、调度后台会话空闲断开 */
  onTerminalEvent: (runtime: SessionRuntime) => void;
}

export class SessionRuntime {
  /** runtime 在 AgentClient.runtimes Map 中的 key（草稿为 DRAFT_KEY，转正后为 sessionId） */
  key: string;
  /** 是否已完成初始恢复（账本/服务端事件 replay）；决定 switchSession 是否需要 hydrate */
  hydrated = false;

  readonly reducer = new EventReducer();
  readonly wsClient: WsClient;

  /** 本地账本中当前 session 的最大 seq，用于重连后的 seq 连续性检查 */
  localLastSeq = 0;
  /** WS 直播 seq 是 run 内递增，不能跨 run 比较 */
  localLastSeqByRunId = new Map<string, number>();
  /** 会话视图版本，用于让过期的异步恢复失效 */
  sessionViewVersion = 0;
  /** 等待真实 sessionId/runId 回填后写入本地 events[] 的用户输入事件 */
  pendingLiveRunPayload: RunPayload | null = null;
  /** 将 WS 细块聚合成可持久化的 /session/events 风格事件 */
  sessionEventAssembler = new SessionEventAssembler();

  private readonly deps: SessionRuntimeDeps;
  private readonly executor: ClientToolExecutor;
  /** 掉线续跑：正在恢复的旧 runId（null 表示当前无恢复流程） */
  private recoveringRunId: string | null = null;
  /** 掉线续跑：已尝试的 sync 轮询次数（单次恢复内计数，每次进入恢复态归零） */
  private recoveryAttempts = 0;
  /**
   * 掉线续跑：连续自动续跑次数。
   *
   * 只在「一轮回答真正跑完」或「用户主动发新消息」时归零——连接层面的好转
   * （重连成功、进入恢复态）一律不算，否则网络反复抖动时永远清零、限制失效。
   */
  private consecutiveRecoveryRuns = 0;
  /** 掉线续跑：用户中止标志，用于让 in-flight 的轮询失效 */
  private recoveryAborted = false;
  /** 掉线续跑：重试退避定时器 */
  private recoveryTimer: ReturnType<typeof setTimeout> | null = null;
  /** 掉线续跑：轮询进行中标志，防止重连抖动触发并发轮询 */
  private recoveryPolling = false;
  /** 发送阶段尚未拿到 runId 时，等待 WebSocket 重连 */
  private pendingRunWaitingForConnection = false;
  /** 当前待发送 run 已消耗的自动重连次数 */
  private pendingRunReconnectAttempts = 0;
  /** 待发送 run 已经发过但未收到首个服务端事件，需要在新连接上重发 */
  private pendingRunNeedsResend = false;
  /** 上次 onSessionResolved 检查时的 reducer sessionId（转正检测） */
  private lastResolvedSessionId: string | null;
  private disposed = false;

  constructor(deps: SessionRuntimeDeps, key: string) {
    this.deps = deps;
    this.key = key;
    this.lastResolvedSessionId = deps.sessionId;

    this.wsClient = new WsClient(
      deps.wsUrl,
      {
        onEvent: (event) => this.handleEvent(event),
        onOpen: () => this.handleWsOpen(),
        onConnectionChange: (connected) => this.handleConnectionChange(connected),
        onReconnectAttempt: (attempt) => this.handleReconnectAttempt(attempt),
        getRuntimeContext: () => {
          const state = this.reducer.getState();
          return {
            runId: state.currentRunId,
            runStatus: state.status,
          };
        },
      },
      {
        reconnect: deps.config.reconnect,
        heartbeatTimeout: deps.config.heartbeatTimeout,
        offlineGraceMs: deps.config.offlineGraceMs,
      },
    );

    // 工具回包必须回到本会话的连接：executor per-runtime，注册表共享。
    this.executor = new ClientToolExecutor(deps.registry, (msg) => this.wsClient.send(msg));

    this.reducer.subscribe(() => {
      if (!this.disposed) deps.onStateChange(this);
    });
  }

  /** 当前归属会话（reducer 状态优先，回退构造时的草稿归属） */
  get sessionId(): string | null {
    return this.reducer.getState().sessionId ?? this.deps.sessionId;
  }

  get isConnected(): boolean {
    return this.wsClient.isConnected;
  }

  /**
   * 是否处于"安全可断开"状态：无 run、无 HITL 等待、无恢复流程。
   * AgentClient 空闲断开调度前后都会调用本方法兜底，绝不误杀运行中的会话。
   * （Plan WAIT 属可恢复等待——服务端 Plan 状态持久化，重连后 sendPlanCommand 会重连接着发。）
   */
  canDisconnectIdle(): boolean {
    if (this.disposed) return false;
    if (this.recoveringRunId != null) return false;
    const status = this.reducer.getState().status;
    return status !== 'running'
      && status !== 'waiting_client_tool'
      && status !== 'compacting'
      && status !== 'recovering';
  }

  /** 主动断开连接（意向性，不触发自动重连）；状态与账本保留，切回时按需重连。 */
  disconnectWs(): void {
    this.wsClient.disconnect();
  }

  /** 销毁 runtime：断连、清定时器。仅在淘汰/替换时由 AgentClient 调用。 */
  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.recoveryAborted = true;
    this.clearRecoveryTimer();
    this.wsClient.disconnect();
  }

  /** 重置 seq 游标并回放事件（初始恢复 / 服务端全量同步后重建状态）。 */
  replayEvents(events: HistoryEvent[] | ReactEvent[]): void {
    this.localLastSeq = 0;
    this.localLastSeqByRunId.clear();
    this.reducer.replayEvents(events as ReactEvent[]);
  }

  /** 清空会话内容与游标（新会话/清空对话）。 */
  clearConversationState(): void {
    this.localLastSeq = 0;
    this.localLastSeqByRunId.clear();
    this.pendingLiveRunPayload = null;
    this.sessionEventAssembler.reset();
    this.reducer.clearConversation();
  }

  /** run 前的重置（发送新 run 时游标清零等）。 */
  resetForNewRunView(): void {
    this.sessionViewVersion += 1;
    this.reducer.resetForNewRun();
    this.localLastSeq = 0;
    this.localLastSeqByRunId.clear();
    this.sessionEventAssembler.reset();
    this.pendingRunWaitingForConnection = false;
    this.pendingRunReconnectAttempts = 0;
    this.pendingRunNeedsResend = false;
  }

  // ─── 运行 ───────────────────────────────────────────────

  /**
   * 发起一次推理（原 AgentClient.run 的会话级逻辑）：
   * run 活跃时走 steer 准入（默认排队），否则准备新 run 并发送。
   */
  run(userPrompt: string, options?: RunOptions): void | Promise<void> {
    // 用户主动发送：重新给满自动续跑额度。放在这里而不是 sendRun，
    // 因为 sendRun 也是自动续跑自己走的路径，放那儿等于永远清不掉。
    this.consecutiveRecoveryRuns = 0;
    // Steering S1：run 活跃时新消息交给后端准入（guide/queue/明确拒绝），
    // 回执以 steer_guided/steer_queued/steer_rejected 事件下发。不走 sendRun：
    // 不能重置当前 run 的运行态（currentRunId/工具卡索引/seq 游标），也不插乐观
    // 用户气泡——排队项被消费时自动续跑的 run 事件会带原文渲染，提前插会重复。
    if (this.isRunInProgress()) {
      return this.sendSteerMessage(userPrompt, options);
    }
    this.reducer.markPendingPlanSubmitting();
    const sessionId = this.reducer.getState().sessionId;
    let hookLlmContext: LlmContext | undefined | Promise<LlmContext | undefined>;
    try {
      hookLlmContext = this.deps.config.hooks?.beforeRun?.({
        userPrompt,
        sessionId,
        inputOrigin: options?.inputOrigin,
      });
    } catch (error) {
      this.reducer.markSubmittingPlanSendFailed();
      throw error;
    }

    if (isPromiseLike(hookLlmContext)) {
      return Promise.resolve(hookLlmContext).then((resolvedLlmContext) => {
        this.sendRun(userPrompt, options, resolvedLlmContext);
      }).catch((error) => {
        this.reducer.markSubmittingPlanSendFailed();
        throw error;
      });
    }

    this.sendRun(userPrompt, options, hookLlmContext);
  }

  /** 确认计划：以一条新 run 指示模型开始执行刚提交的计划；新 run 启动后 reducer 会把计划置为 accepted。 */
  confirmPlan(planId: string): void | Promise<void> {
    if (!planId || this.isRunInProgress() || this.reducer.getState().status === 'recovering') {
      return;
    }
    return this.run('开始执行刚才的计划', {
      inputOrigin: { type: 'manual' },
    }) as void | Promise<void>;
  }

  /**
   * @param silent 静默发送：userPrompt 照常发给后端，但不在当前 UI 插入用户消息气泡。
   *   仅供掉线自动续跑使用；历史回放侧由 reducer 依据 controlContext 标记做同样的隐藏。
   */
  private sendRun(
    userPrompt: string,
    options: RunOptions | undefined,
    hookLlmContext: LlmContext | undefined,
    silent = false,
  ): void {
    const config = this.deps.config;
    const llmContext = hookLlmContext ?? options?.llmContext ?? config.llmContext;

    this.resetForNewRunView();
    console.log('[agent-ui][activity-debug] run:start', {
      statusAfterReset: this.reducer.getState().status,
      sessionId: this.reducer.getState().sessionId,
      hasDisplayParts: !!options?.displayParts?.length,
      promptLength: userPrompt.length,
    });

    // 先记录到当前 UI，等首个直播事件返回真实 ID 后再补写本地 events[]
    if (!silent) {
      this.reducer.addUserStep(userPrompt, undefined, options?.displayParts);
    }

    const payload: RunPayload = {
      callerKey: config.callerKey,
      routeValues: config.routeValues,
      type: 'chat',
      userPrompt,
      inputOrigin: options?.inputOrigin,
      attachments: options?.attachments,
      controlContext: options?.controlContext ?? config.controlContext,
      llmContext,
      modelKey: options?.modelKey ?? config.modelKey,
      modelVersion: options?.modelVersion ?? config.modelVersion,
      modelHash: options?.modelHash ?? config.modelHash,
      maxSteps: options?.maxSteps ?? config.maxSteps,
      executionMode: options?.executionMode ?? 'react',
      reasoning: options?.reasoning,
    };
    this.pendingLiveRunPayload = payload;

    const sentOnExistingConnection = this.wsClient.isConnected;
    if (!this.wsClient.isConnected) {
      this.pendingRunWaitingForConnection = true;
      if (!this.wsClient.isConnecting) {
        // 真断线：进入恢复态并触发重连（原有语义）。
        this.reducer.markRecovering();
        this.wsClient.connect();
      }
      // 建立中（会话切换/首次建连）：send 进缓冲，open 后自动发出，不打扰 UI 状态。
    }

    this.wsClient.send({
      type: 'run',
      sessionId: this.reducer.getState().sessionId ?? undefined,
      payload: payload as unknown as Record<string, unknown>,
    });
    this.pendingRunNeedsResend = sentOnExistingConnection;
  }

  /**
   * run 活跃期间的 steer 发送：组装 RunPayload 原样走 WS run 消息，
   * 后端 SteerSession 准入后把回执以 steer_* 事件推回当前连接。
   * 本地不重置任何运行态，也不等待新 runId。
   */
  private sendSteerMessage(userPrompt: string, options?: RunOptions): void | Promise<void> {
    const config = this.deps.config;
    const hookLlmContext = config.hooks?.beforeRun?.({
      userPrompt,
      sessionId: this.reducer.getState().sessionId,
      inputOrigin: options?.inputOrigin,
    });

    const send = (llmContext?: LlmContext): void => {
      const payload: RunPayload = {
        callerKey: config.callerKey,
        routeValues: config.routeValues,
        type: 'chat',
        userPrompt,
        inputOrigin: options?.inputOrigin,
        attachments: options?.attachments,
        controlContext: options?.controlContext ?? config.controlContext,
        llmContext: llmContext ?? options?.llmContext ?? config.llmContext,
        modelKey: options?.modelKey ?? config.modelKey,
        modelVersion: options?.modelVersion ?? config.modelVersion,
        modelHash: options?.modelHash ?? config.modelHash,
        maxSteps: options?.maxSteps ?? config.maxSteps,
        executionMode: options?.executionMode ?? 'react',
        reasoning: options?.reasoning,
        // 双模式之一（默认排队）：运行中回车显式排队，等 run 完全结束依次执行；
        // 想立即影响当前对话，从队列列表点"立即发送"注入。
        steerDelivery: 'queue',
      };
      this.wsClient.send({
        type: 'run',
        sessionId: this.reducer.getState().sessionId ?? undefined,
        payload: payload as unknown as Record<string, unknown>,
      });
    };

    if (isPromiseLike(hookLlmContext)) {
      return Promise.resolve(hookLlmContext).then((resolved) => send(resolved));
    }
    send(hookLlmContext);
  }

  /** 取消本会话正在进行的 run；恢复中则中止自动续跑（不发后端 cancel，旧 run 已终态）。 */
  cancel(): void {
    const state = this.reducer.getState();
    // 用户点停止也算主动接管，与 run() 同类：重新给满自动续跑额度。
    this.consecutiveRecoveryRuns = 0;
    if (state.status === 'recovering') {
      this.recoveryAborted = true;
      this.clearRecoveryState();
      this.pendingRunWaitingForConnection = false;
      this.pendingRunReconnectAttempts = 0;
      this.pendingLiveRunPayload = null;
      this.wsClient.clearPendingMessages();
      this.reducer.markSubmittingPlanSendFailed();
      this.reducer.abortRecovering();
      return;
    }

    if (!this.isRunInProgress()) return;
    const runId = state.currentRunId;
    if (!runId) return;

    this.wsClient.send({
      type: 'cancel',
      runId,
    });
  }

  /** 回填危险操作确认（P2-3）的允许/拒绝。 */
  sendToolConfirmAnswer(toolUseId: string, approved: boolean, reason?: string): void {
    if (!this.isRunInProgress()) return;
    const runId = this.reducer.getState().currentRunId;
    if (!runId) return;

    this.wsClient.send({
      type: 'tool_confirm_answer',
      runId,
      payload: { toolUseId, approved, reason } as unknown as Record<string, unknown>,
    });
  }

  /** 回填 ask_question 的用户答案。 */
  sendAskQuestionAnswer(toolUseId: string, content: AskQuestionAnswerContent): void {
    if (!this.isRunInProgress()) return;
    const runId = this.reducer.getState().currentRunId;
    if (!runId) return;

    this.wsClient.send({
      type: 'tool_use_answer',
      runId,
      payload: { toolUseId, content } as unknown as Record<string, unknown>,
    });
  }

  /**
   * 显式发送一条排队输入（S3 双模式）：run 模式空闲时 claim-once 晋升开新 run；
   * inject 模式把该排队项晋升为活跃 run 的 guide 在下一个安全边界注入当前对话。
   */
  sendQueuedMessage(pendingInputId: string, mode: 'run' | 'inject' = 'run'): void {
    if (!pendingInputId) return;
    const runInProgress = this.isRunInProgress();
    if (mode === 'run' && runInProgress) return;
    const sessionId = this.reducer.getState().sessionId;
    if (!sessionId) return;
    this.wsClient.send({
      type: 'queue_send',
      sessionId,
      payload: {
        sessionId,
        pendingInputId,
        mode: runInProgress ? 'inject' : 'run',
      } as unknown as Record<string, unknown>,
    });
  }

  /** Plan 命令发送（resume/retry/skip/cancel 公开方法的共用实现）。 */
  sendPlanCommand(type: WsMessageType, planExecutionId: string, payload: Record<string, unknown>): void {
    if (!planExecutionId || this.isRunInProgress() || this.reducer.getState().status === 'recovering') return;
    const state = this.reducer.getState();
    const plan = state.plans[planExecutionId];
    if (!plan) return;

    this.reducer.markPlanCommandRunning();
    this.sessionViewVersion += 1;
    this.localLastSeq = 0;
    this.localLastSeqByRunId.clear();
    this.sessionEventAssembler.reset();

    if (!this.wsClient.isConnected) {
      this.wsClient.connect();
    }
    this.wsClient.send({
      type,
      runId: plan.outerRunId ?? state.currentRunId ?? undefined,
      sessionId: state.sessionId ?? undefined,
      payload,
    });
  }

  isRunInProgress(): boolean {
    const status = this.reducer.getState().status;
    return status === 'running' || status === 'waiting_client_tool' || status === 'compacting';
  }

  // ─── 事件处理 ───────────────────────────────────────────

  /**
   * 统一处理从本会话 WS 连接收到的事件
   * （原 AgentClient.handleEvent 的会话级逻辑，seq 对齐/账本写入/终态同步照搬）。
   */
  handleEvent(event: ReactEvent): void {
    // 前端工具执行
    if (event.type === 'client_tool_use_start') {
      const payload = event.payload as unknown as ClientToolUseStartPayload;
      void this.executor.handleClientToolUseStart(payload);
    }

    // 一轮回答完整跑完，说明网络已撑住，之前的抖动翻篇：重新给满自动续跑额度。
    // 放在 seq 对齐之前，避免丢包走 handleSeqGap 提前 return 时漏掉归零。
    if (event.type === 'done') {
      this.consecutiveRecoveryRuns = 0;
    }

    // 乐观 seq 对齐：WS seq 是 run 内递增，只能在同一个 runId 下比较
    const lastSeqInRun = event.runId ? this.localLastSeqByRunId.get(event.runId) ?? 0 : 0;
    if (event.sessionId && event.runId && event.seq > 0 && lastSeqInRun > 0) {
      if (event.seq !== lastSeqInRun + 1 && event.seq !== 1) {
        this.handleSeqGap(event, lastSeqInRun);
        this.maybeResolveSession(event);
        return;
      }
    }

    // 正常处理
    this.persistPendingLiveRunEvent(event);
    this.reducer.applyEvent(event);
    this.localLastSeq = Math.max(this.localLastSeq, event.seq);
    if (event.runId && event.seq > 0) {
      this.localLastSeqByRunId.set(event.runId, event.seq);
    }

    this.persistCompletedLiveEvents(event);

    // 更新 session 元数据
    if (event.sessionId) {
      this.deps.ledger.upsertSession({
        sessionId: event.sessionId,
        callerKey: this.deps.config.callerKey,
        routeValues: this.deps.config.routeValues ?? [],
        type: 'chat',
      });

      if (this.isTerminalEvent(event)) {
        this.deps.onTerminalEvent(this);
        const viewVersion = this.sessionViewVersion;
        void this.deps.syncSessionEvents(event.sessionId).then(async () => {
          if (viewVersion !== this.sessionViewVersion || this.isRunInProgress()) {
            return;
          }
          const events = await this.deps.ledger.getEvents(event.sessionId!);
          this.replayEvents(events);
        }).catch((err) => {
          console.warn('[SessionRuntime] terminal session sync failed:', err);
        });
      }
    }

    this.maybeResolveSession(event);
  }

  /** 草稿转正检测：reducer 归属会话变化时通知 AgentClient rekey。 */
  private maybeResolveSession(event: ReactEvent): void {
    if (!event.sessionId || event.sessionId === this.lastResolvedSessionId) return;
    this.lastResolvedSessionId = event.sessionId;
    this.deps.onSessionResolved(this, event.sessionId);
  }

  /**
   * 直播流不会稳定返回 history 里的 run 事件，前端需要在拿到真实 sessionId/runId 后补写一条。
   * 这条事件只进入本地 events[]，不再喂给当前 reducer，避免当前 UI 重复出现用户消息。
   */
  private persistPendingLiveRunEvent(event: ReactEvent): void {
    if (!this.pendingLiveRunPayload || !event.sessionId || !event.runId) {
      return;
    }

    this.pendingRunWaitingForConnection = false;
    this.pendingRunReconnectAttempts = 0;
    this.pendingRunNeedsResend = false;
    if (event.type === 'run') {
      this.pendingLiveRunPayload = null;
      return;
    }

    this.deps.ledger.append({
      type: 'run',
      seq: 0,
      runId: event.runId,
      sessionId: event.sessionId,
      payload: this.pendingLiveRunPayload as unknown as Record<string, unknown>,
    });
    this.pendingLiveRunPayload = null;
  }

  /** 将当前 WS 事件中已经完整的部分追加到本地 /session/events 事实 */
  private persistCompletedLiveEvents(event: ReactEvent): void {
    const completedEvents = this.sessionEventAssembler.consume(event);
    if (completedEvents.length === 0) return;
    this.deps.ledger.appendEvents(completedEvents);
  }

  /**
   * 处理 seq 不连续（丢失事件）：从服务端全量同步当前 session 到本地缓存，再 replay。
   * （内部方法；公开供 AgentClient 兼容转发。）
   */
  handleSeqGap(currentEvent: ReactEvent, lastSeqInRun: number): void {
    const sessionId = currentEvent.sessionId!;
    const viewVersion = this.sessionViewVersion;
    console.warn(
      `[SessionRuntime] seq gap detected: runId=${currentEvent.runId}, local=${lastSeqInRun}, received=${currentEvent.seq}.`,
    );

    this.persistPendingLiveRunEvent(currentEvent);
    this.reducer.applyEvent(currentEvent);
    this.localLastSeq = Math.max(this.localLastSeq, currentEvent.seq);
    if (currentEvent.runId && currentEvent.seq > 0) {
      this.localLastSeqByRunId.set(currentEvent.runId, currentEvent.seq);
    }
    this.persistCompletedLiveEvents(currentEvent);

    if (!this.isTerminalEvent(currentEvent)) {
      return;
    }

    void this.deps.syncSessionEvents(sessionId).then(async () => {
      const events = await this.deps.ledger.getEvents(sessionId);
      if (viewVersion !== this.sessionViewVersion) {
        return;
      }
      this.replayEvents(events);
    }).catch((err) => {
      console.warn('[SessionRuntime] full refetch failed:', err);
    });
  }

  // ─── 连接回调 ───────────────────────────────────────────

  /**
   * WS 连接/重连建立后的处理：发送阶段待重发的 run 补发；恢复中则触发续跑轮询。
   */
  handleWsOpen(): void {
    if (this.pendingRunNeedsResend && this.pendingRunWaitingForConnection && this.pendingLiveRunPayload) {
      this.pendingRunNeedsResend = false;
      this.wsClient.send({
        type: 'run',
        sessionId: this.reducer.getState().sessionId ?? undefined,
        payload: this.pendingLiveRunPayload as unknown as Record<string, unknown>,
      });
    }
    this.maybeResumeRecovery();
  }

  /**
   * WS 连接状态变化处理（原 AgentClient.handleConnectionChange）：
   * 后端将 run 生命周期绑定在单条 WS 连接上，连接断开即 cancel 当前 run——
   * 异常断开时若有 run 进行中，进入「恢复中」态，重连后尝试自动续跑。
   */
  handleConnectionChange(connected: boolean): void {
    const state = this.reducer.getState();
    const willInterruptRun = !connected && this.isRunInProgress();
    console.log(`[agent-sdk][connection] state-change ${JSON.stringify({
      timestamp: new Date().toISOString(),
      connected,
      previousConnected: state.connected,
      status: state.status,
      sessionId: state.sessionId,
      runId: state.currentRunId,
      lastSeq: this.localLastSeq,
      willInterruptRun,
    })}`);
    if (willInterruptRun) {
      const runId = state.currentRunId;
      if (runId) {
        this.startRecovery(runId);
      } else if (this.pendingLiveRunPayload) {
        this.pendingRunWaitingForConnection = true;
        this.pendingRunNeedsResend = true;
        this.reducer.markRecovering();
      }
    }
    this.reducer.setConnected(connected);
  }

  /** 发送阶段的重连超过上限后，才把本轮标记为失败。 */
  handleReconnectAttempt(_attempt: number): void {
    if (!this.pendingRunWaitingForConnection || !this.pendingLiveRunPayload) {
      return;
    }
    this.pendingRunReconnectAttempts += 1;
    if (this.pendingRunReconnectAttempts > MAX_PENDING_RUN_RECONNECT_ATTEMPTS) {
      this.pendingRunWaitingForConnection = false;
      this.pendingLiveRunPayload = null;
      this.wsClient.clearPendingMessages();
      this.reducer.markSubmittingPlanSendFailed();
      this.reducer.markRunInterrupted('连接已断开，本次回答已中断');
    }
  }

  // ─── 掉线续跑 ───────────────────────────────────────────

  /** 进入「恢复中」：记录待恢复的旧 run，重置计数，等重连后轮询终态。 */
  private startRecovery(runId: string): void {
    this.recoveringRunId = runId;
    this.recoveryAttempts = 0;
    this.recoveryAborted = false;
    this.clearRecoveryTimer();
    this.reducer.markRecovering();
  }

  /** 重连成功后按需启动续跑轮询；已在轮询或已排程重试则跳过，避免并发。 */
  private maybeResumeRecovery(): void {
    if (this.recoveringRunId == null) return;
    if (this.reducer.getState().status !== 'recovering') return;
    if (this.recoveryPolling || this.recoveryTimer) return;
    void this.pollRecovery();
  }

  /**
   * 一次续跑轮询：sync 拉取旧 run 的最终状态。
   * - EventDone/Cancelled → 旧 run 已正常结束，重建展示完整内容，不续跑
   * - EventError → 旧 run 被中断，开一个普通新 run 续跑
   * - 无终态事件 → 旧 run 尚未收敛，退避后重试；超过上限降级为手动「继续」
   */
  private async pollRecovery(): Promise<void> {
    if (this.recoveryAborted || this.recoveringRunId == null || this.disposed) return;
    if (this.reducer.getState().status !== 'recovering') return;

    const runId = this.recoveringRunId;
    const sessionId = this.reducer.getState().sessionId;
    if (!sessionId) {
      this.failRecovery();
      return;
    }

    this.recoveryPolling = true;
    try {
      const events = await this.deps.syncSessionEvents(sessionId);
      if (this.recoveryAborted || this.reducer.getState().status !== 'recovering') {
        return;
      }
      const terminal = events.find(
        (e) => e.runId === runId
          && (e.type === 'done' || e.type === 'error' || e.type === 'cancelled'),
      );
      if (terminal && (terminal.type === 'done' || terminal.type === 'cancelled')) {
        // 旧 run 其实已正常结束/取消：重建以展示完整内容，不续跑。
        // 这条路径不会有 live done 事件经过 handleEvent，连续续跑计数要在这里补一次归零。
        this.replayEvents(events as unknown as ReactEvent[]);
        this.consecutiveRecoveryRuns = 0;
        this.clearRecoveryState();
        return;
      }
      if (terminal && terminal.type === 'error') {
        // 旧 run 被中断：清理恢复态并开新 run 续跑。
        // 带上 controlContext 标记并静默发送，使这条自动消息在实时和历史回放中都不展示。
        this.clearRecoveryState();
        if (this.consecutiveRecoveryRuns >= MAX_CONSECUTIVE_RECOVERY_RUNS) {
          this.reducer.markRunInterrupted('网络不稳定，本轮回答已中断，请重新发送');
          return;
        }
        this.consecutiveRecoveryRuns += 1;
        this.sendRun(
          RECOVERY_CONTINUE_PROMPT,
          {
            controlContext: {
              ...(this.deps.config.controlContext ?? {}),
              [RECOVERY_CONTINUE_FLAG]: true,
            },
          },
          undefined,
          true,
        );
        return;
      }
      // 旧 run 还没收敛终态 → 退避重试
      this.scheduleRecoveryRetry();
    } catch (err) {
      console.warn('[SessionRuntime] recovery poll failed:', err);
      this.scheduleRecoveryRetry();
    } finally {
      this.recoveryPolling = false;
    }
  }

  /** 排程下一次续跑轮询；轮询次数到顶则把本轮打成中断，交回用户手动重发。 */
  private scheduleRecoveryRetry(): void {
    if (this.recoveryAborted || this.reducer.getState().status !== 'recovering') return;
    this.recoveryAttempts += 1;
    if (this.recoveryAttempts >= MAX_RECOVERY_POLL_ATTEMPTS) {
      this.failRecovery();
      return;
    }
    const delay = RECOVERY_BACKOFF_MS[Math.min(this.recoveryAttempts, RECOVERY_BACKOFF_MS.length - 1)];
    this.recoveryTimer = setTimeout(() => {
      this.recoveryTimer = null;
      void this.pollRecovery();
    }, delay);
  }

  /** 续跑彻底失败：清理恢复态并把这一轮打成中断（用户可手动重发）。 */
  private failRecovery(): void {
    this.clearRecoveryState();
    this.reducer.markRunInterrupted('连接已断开，未能自动恢复');
  }

  /** 清理续跑相关的所有内部状态（不含 recoveryAborted，由 startRecovery/cancel 管理）。 */
  private clearRecoveryState(): void {
    this.recoveringRunId = null;
    this.recoveryAttempts = 0;
    this.clearRecoveryTimer();
  }

  private clearRecoveryTimer(): void {
    if (this.recoveryTimer) {
      clearTimeout(this.recoveryTimer);
      this.recoveryTimer = null;
    }
  }

  private isTerminalEvent(event: ReactEvent): boolean {
    return event.type === 'done' || event.type === 'error' || event.type === 'cancelled';
  }
}
