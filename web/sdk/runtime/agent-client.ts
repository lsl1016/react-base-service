/**
 * Agent 客户端
 *
 * SDK 的核心入口，组合以下模块对外提供统一 API：
 * - WsClient: WebSocket 连接管理
 * - SessionManager: HTTP 会话管理
 * - ClientToolRegistry + ClientToolExecutor: 客户端工具注册与执行
 * - EventReducer: 事件流聚合为 AgentState
 * - EventLedger: IndexedDB 事件账本（默认开启，对调用者透明）
 *
 * 使用方式：
 *
 *   const agent = createAgentClient({
 *     baseUrl: '/react-base-service/react',
 *     callerKey: 'report-editor',
 *     routeValues: ['report_abc123'],
 *   });
 *
 *   agent.registerTool(myTool);
 *   agent.subscribe((state) => { ... });
 *   agent.connect();
 *   agent.run('帮我创建一个柱状图');
 *
 * 数据流：
 *   宿主调用 run() → WsClient 发送 run 消息 → 后端推事件流
 *   → WsClient.onEvent → AgentClient.handleEvent
 *     → client_tool_use_start 事件交由 ToolExecutor 执行并回填
 *     → 所有事件交由 EventReducer 聚合
 *     → 完整语义事件异步写入 EventLedger（IndexedDB 账本）
 *   → EventReducer 通知 subscribers → 宿主更新 UI
 *
 * 持久化（默认开启，无需配置）：
 *   构造时自动从 IndexedDB 恢复最近会话 → replayEvents
 *   WS 细块只用于实时 UI，thought/content 完成后再写入本地 events[]
 *   WS 重连后检查 seq 连续性，不连续则在 run 终态后全量拉取
 */
import { WsClient } from '../client/ws-client';
import { RECOVERY_CONTINUE_FLAG } from '../protocol/types';
import type { ApiResponse, AskQuestionAnswerContent, ChatFileUpload, ClientToolUseStartPayload, ExecutionMode, LlmContext, ReactAttachmentRef, ReactEvent, ReactModelInfo, ReactModelsResp, RunPayload, UserInputOrigin, WsMessageType } from '../protocol/types';
import { SessionManager } from '../session/session-manager';
import type { AsyncTaskItem, HistoryEvent, SessionListParams, SessionListResp } from '../session/types';
import type { SessionMeta } from '../storage/event-ledger';
import { EventLedger } from '../storage/event-ledger';
import { ClientToolExecutor } from '../tools/executor';
import { ClientToolRegistry } from '../tools/registry';
import type { ClientTool } from '../tools/types';
import { AsyncTaskManager } from './async-task-manager';
import type { AsyncTaskListener } from './async-task-manager';
import { EventReducer } from './event-reducer';
import { SessionEventAssembler } from './session-event-assembler';
import type { AgentInputPart, AgentState, RunFeedbackPayload, RunFeedbackState } from './types';

function isPromiseLike<T>(value: T | Promise<T>): value is Promise<T> {
  return !!value && typeof (value as Promise<T>).then === 'function';
}

/**
 * AgentClient 配置
 *
 * @property baseUrl          - 后端服务地址，如 '/react-base-service/react'
 * @property callerKey        - 业务标识，用于后端区分不同调用方
 * @property routeValues      - 路由参数，后端根据 callerKey+routeValues 定位 Session
 * @property modelKey         - 默认模型标识
 * @property modelVersion     - 默认模型版本
 * @property modelHash        - 模型配置哈希（可替代 modelKey+modelVersion）
 * @property maxSteps         - ReAct 最大步骤数
 * @property reconnect        - 是否自动重连，默认 true
 * @property heartbeatTimeout - 心跳超时（ms），默认 30000
 */
export interface BeforeRunHookContext {
  /** 本轮用户输入 */
  userPrompt: string;
  /** 当前会话 ID；新会话首次发送时为 null */
  sessionId: string | null;
  /** 本轮用户输入来源 */
  inputOrigin?: UserInputOrigin;
}

export interface AgentClientHooks {
  /**
   * 每次 run 发送前解析最新模型上下文。
   * 返回字符串时服务端按纯文本消费，返回对象时按 JSON 消费；返回 undefined 时沿用静态 llmContext。
   */
  beforeRun?: (context: BeforeRunHookContext) => LlmContext | undefined | Promise<LlmContext | undefined>;
}

export interface AgentClientConfig {
  baseUrl: string;
  /** 调用方标识，用于后端区分不同调用方 */
  callerKey: string;
  /** 会话作用域参数，用于分隔不同的场景 */
  routeValues?: string[];
  /** 默认运行控制参数，会在每次 run 时带给后端 */
  controlContext?: Record<string, unknown>;
  /** 默认模型业务上下文，会在每次 run 时带给后端 */
  llmContext?: LlmContext;
  /** Agent 生命周期 hooks */
  hooks?: AgentClientHooks;
  modelKey?: string;
  modelVersion?: string;
  modelHash?: string;
  maxSteps?: number;
  /** 重连配置，默认 true */
  reconnect?: boolean;
  /** 心跳超时配置，默认 30000 */
  heartbeatTimeout?: number;
  /** 断网宽限（ms）：offline 后保留连接等待网络恢复，超时才断开重连；0 表示立刻断开，默认 5000 */
  offlineGraceMs?: number;
}

export interface AgentSessionScope {
  callerKey: string;
  routeValues: string[];
}

/**
 * run() 的可选参数，覆盖 AgentClientConfig 中的默认值
 */
export interface RunOptions {
  controlContext?: Record<string, unknown>;
  llmContext?: LlmContext;
  modelKey?: string;
  modelVersion?: string;
  modelHash?: string;
  maxSteps?: number;
  /** 本轮执行范式；默认 react。 */
  executionMode?: ExecutionMode;
  /** 本轮随用户消息发送的已上传附件 */
  attachments?: ReactAttachmentRef[];
  /** 本轮用户输入来源 */
  inputOrigin?: UserInputOrigin;
  /** 仅用于前端展示的用户输入结构，不发送给后端 */
  displayParts?: AgentInputPart[];
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

export class AgentClient {
  private wsClient: WsClient;
  private sessionManager: SessionManager;
  private asyncTaskManager: AsyncTaskManager;
  private registry: ClientToolRegistry;
  private executor: ClientToolExecutor;
  private reducer: EventReducer;
  private ledger: EventLedger;
  private config: AgentClientConfig;
  /** 外部 subscribers，由 reducer 的内部 listener 桥接分发 */
  private subscribers = new Set<(state: AgentState) => void>();
  /** 本地账本中当前 session 的最大 seq，用于重连后的 seq 连续性检查 */
  private localLastSeq: number = 0;
  /** WS 直播 seq 是 run 内递增，不能跨 run 比较 */
  private localLastSeqByRunId = new Map<string, number>();
  /** 初始化期服务端同步，避免构造和 connect 重复触发 */
  private initialServerSyncPromise: Promise<void> | null = null;
  /** 当前会话视图版本，用于让过期的异步恢复/切换失效 */
  private sessionViewVersion = 0;
  /** 等待真实 sessionId/runId 回填后写入本地 events[] 的用户输入事件 */
  private pendingLiveRunPayload: RunPayload | null = null;
  /** 将 WS 细块聚合成可持久化的 /session/events 风格事件 */
  private sessionEventAssembler = new SessionEventAssembler();
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

  constructor(config: AgentClientConfig) {
    this.config = config;
    this.reducer = new EventReducer();

    const wsUrl = this.buildWsUrl();
    this.wsClient = new WsClient(
      wsUrl,
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
        reconnect: config.reconnect,
        heartbeatTimeout: config.heartbeatTimeout,
        offlineGraceMs: config.offlineGraceMs,
      },
    );

    const httpUrl = this.buildHttpUrl();
    this.sessionManager = new SessionManager(httpUrl);
    this.asyncTaskManager = new AsyncTaskManager(this.sessionManager);

    this.registry = new ClientToolRegistry();
    this.executor = new ClientToolExecutor(this.registry, (msg) => this.wsClient.send(msg));
    this.ledger = new EventLedger();

    // 桥接 reducer 内部 listeners → 外部 subscribers
    this.reducer.subscribe((state) => {
      for (const fn of this.subscribers) {
        fn(state);
      }
    });

    // 自动从本地账本恢复（异步，不阻塞构造）
    this.restoreFromLedger();
    this.syncInitialServerState();
  }

  // ─── 连接 ───────────────────────────────────────────────

  connect(): void {
    this.syncInitialServerState();
    this.wsClient.connect();
  }

  disconnect(): void {
    this.wsClient.disconnect();
    this.asyncTaskManager.dispose();
    this.ledger.close();
  }

  get isConnected(): boolean {
    return this.wsClient.isConnected;
  }

  /** 获取宿主配置的初始模型；模型接口不可用时 UI 以此兜底。 */
  getConfiguredModel(): ReactModelInfo | null {
    if (!this.config.modelKey || !this.config.modelVersion) return null;
    return {
      modelKey: this.config.modelKey,
      modelVersion: this.config.modelVersion,
      displayName: this.config.modelVersion,
    };
  }

  /** 从 ReAct 后端获取输入框可选择的模型列表。 */
  async listModels(): Promise<ReactModelsResp> {
    const response = await fetch(this.buildModelsUrl(), { credentials: 'include' });
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`);
    }
    const json = (await response.json()) as ApiResponse<ReactModelsResp>;
    if (json.errNo !== 0) {
      throw new Error(`API Error [${json.errNo}]: ${json.errMsg}`);
    }
    return json.data;
  }

  // ─── 运行 ───────────────────────────────────────────────

  /** 进入新对话草稿态：只清空当前面板，不创建服务端 session，不清本地历史 */
  newSession(): boolean {
    if (this.isRunInProgress() || this.reducer.getState().status === 'recovering') {
      return false;
    }

    this.sessionViewVersion += 1;
    this.localLastSeq = 0;
    this.localLastSeqByRunId.clear();
    this.pendingLiveRunPayload = null;
    this.sessionEventAssembler.reset();
    this.asyncTaskManager.setSession(null);
    // 新对话不继承上一个会话的连续失败史
    this.consecutiveRecoveryRuns = 0;
    this.reducer.clearConversation();
    return true;
  }

  /**
   * 发起一次推理
   *
   * 1. 准备新 run 的运行态，保留当前会话消息
   * 2. 将用户消息先放入当前 UI 投影，等服务端返回 sessionId/runId 后补写本地 events[]
   * 3. 组装 RunPayload 并通过 WS 发送 run 消息
   * 4. 后端开始 ReAct 循环，通过 WS 推送事件流
   */
  run(userPrompt: string, options?: RunOptions): void | Promise<void> {
    // 用户主动发送：重新给满自动续跑额度。放在这里而不是 sendRun，
    // 因为 sendRun 也是自动续跑自己走的路径，放那儿等于永远清不掉。
    this.consecutiveRecoveryRuns = 0;
    this.reducer.markPendingPlanSubmitting();
    const sessionId = this.reducer.getState().sessionId;
    let hookLlmContext: LlmContext | undefined | Promise<LlmContext | undefined>;
    try {
      hookLlmContext = this.config.hooks?.beforeRun?.({
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
    const llmContext = hookLlmContext ?? options?.llmContext ?? this.config.llmContext;

    this.sessionViewVersion += 1;
    this.reducer.resetForNewRun();
    console.log('[agent-ui][activity-debug] run:start', {
      statusAfterReset: this.reducer.getState().status,
      sessionId: this.reducer.getState().sessionId,
      hasDisplayParts: !!options?.displayParts?.length,
      promptLength: userPrompt.length,
    });
    this.localLastSeq = 0;
    this.localLastSeqByRunId.clear();
    this.sessionEventAssembler.reset();
    this.pendingRunWaitingForConnection = false;
    this.pendingRunReconnectAttempts = 0;
    this.pendingRunNeedsResend = false;

    // 先记录到当前 UI，等首个直播事件返回真实 ID 后再补写本地 events[]
    if (!silent) {
      this.reducer.addUserStep(userPrompt, undefined, options?.displayParts);
    }

    const payload: RunPayload = {
      callerKey: this.config.callerKey,
      routeValues: this.config.routeValues,
      type: 'chat',
      userPrompt,
      inputOrigin: options?.inputOrigin,
      attachments: options?.attachments,
      controlContext: options?.controlContext ?? this.config.controlContext,
      llmContext,
      modelKey: options?.modelKey ?? this.config.modelKey,
      modelVersion: options?.modelVersion ?? this.config.modelVersion,
      modelHash: options?.modelHash ?? this.config.modelHash,
      maxSteps: options?.maxSteps ?? this.config.maxSteps,
      executionMode: options?.executionMode ?? 'react',
    };
    this.pendingLiveRunPayload = payload;

    const sentOnExistingConnection = this.wsClient.isConnected;
    if (!this.wsClient.isConnected) {
      this.pendingRunWaitingForConnection = true;
      this.reducer.markRecovering();
      this.wsClient.connect();
    }

    this.wsClient.send({
      type: 'run',
      sessionId: this.reducer.getState().sessionId ?? undefined,
      payload: payload as unknown as Record<string, unknown>,
    });
    this.pendingRunNeedsResend = sentOnExistingConnection;
  }

  /** 上传一个供 ReAct run 引用的 csv/md/txt 附件。 */
  async uploadAttachment(file: File): Promise<ChatFileUpload> {
    const formData = new FormData();
    formData.append('file', file);
    const response = await fetch(this.buildUploadUrl(), {
      method: 'POST',
      body: formData,
      credentials: 'include',
    });
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`);
    }
    const json = (await response.json()) as ApiResponse<ChatFileUpload>;
    if (json.errNo !== 0) {
      throw new Error(`API Error [${json.errNo}]: ${json.errMsg}`);
    }
    return json.data;
  }

  /** 取消当前正在进行的 run；恢复中则中止自动续跑（不发后端 cancel，旧 run 已终态）。 */
  cancel(): void {
    const state = this.reducer.getState();
    // 用户点停止也算主动接管，与 run() 同类：重新给满自动续跑额度。
    // 当前即使不清也够不到判断（取消后必须经 run() 才能再进恢复态），
    // 但让这个不变式在本地成立，不依赖「run() 一定是下一步」这个外部前提。
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

  /**
   * 回填危险操作确认（P2-3）的允许/拒绝。
   *
   * 服务端在 tool_confirm_request 后阻塞等待；宿主 UI 用户点击允许/拒绝后调用本方法继续。
   */
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

  /**
   * 回填 ask_question 的用户答案。
   *
   * 服务端在 tool_use_start（toolName='ask_question'，status='waiting'）后阻塞等待，
   * 宿主 UI 收集用户选择后调用本方法继续推理；用户跳过时传 skipped=true、answers 空数组。
   * 底层发送通用的 tool_use_answer 消息（{ toolUseId, content }），content 由等待中的工具解释。
   */
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
   * 清空当前对话。
   *
   * 同时清空运行态和当前作用域下的本地账本，避免重新挂载后又从 IndexedDB 恢复旧对话。
   */
  async clearConversation(): Promise<void> {
    this.localLastSeq = 0;
    this.localLastSeqByRunId.clear();
    this.pendingLiveRunPayload = null;
    this.sessionEventAssembler.reset();
    this.reducer.clearConversation();

    try {
      await this.ledger.clearSessions(this.config.callerKey, this.config.routeValues);
    } catch (error) {
      console.warn('[AgentClient] clear conversation ledger failed:', error);
    }
  }

  // ─── 状态 ───────────────────────────────────────────────

  /** 获取当前状态快照 */
  getState(): AgentState {
    return this.reducer.getState();
  }

  /** 获取当前会话的业务路由作用域，返回副本避免外部修改运行配置。 */
  getSessionScope(): AgentSessionScope {
    return {
      callerKey: this.config.callerKey,
      routeValues: [...(this.config.routeValues ?? [])],
    };
  }

  /**
   * 订阅状态变化
   *
   * 注册时立即回调一次当前状态（方便初始化）。
   * 返回 unsubscribe 函数。
   */
  subscribe(fn: (state: AgentState) => void): () => void {
    this.subscribers.add(fn);
    fn(this.reducer.getState());
    return () => {
      this.subscribers.delete(fn);
    };
  }

  // ─── 反馈 ───────────────────────────────────────────────

  /** 提交单轮反馈 */
  async submitFeedback(runId: string, payload: RunFeedbackPayload): Promise<RunFeedbackState> {
    const resp = await this.sessionManager.submitRunFeedback({
      runId,
      feedback: payload.feedback,
      problemFeedback: payload.problemFeedback,
    });
    return {
      feedback: resp.feedback,
      problemFeedback: resp.problemFeedback ?? '',
    };
  }

  /** 加载指定会话的全部反馈，并转换为 runId 索引 */
  async loadSessionFeedback(sessionId: string | null): Promise<Record<string, RunFeedbackState>> {
    if (!sessionId) {
      return {};
    }

    const resp = await this.sessionManager.getSessionFeedback(sessionId);
    const map: Record<string, RunFeedbackState> = {};
    for (const item of resp.items ?? []) {
      map[item.runId] = {
        feedback: item.feedback,
        problemFeedback: item.problemFeedback ?? '',
      };
    }
    return map;
  }

  // ─── 会话 ───────────────────────────────────────────────

  /** 只读取本地缓存中的会话列表 */
  async listCachedSessions(params?: Partial<SessionListParams>): Promise<SessionMeta[]> {
    const callerKey = params?.callerKey ?? this.config.callerKey;
    const routeValues = params?.routeValues ?? this.config.routeValues;
    return this.ledger.listSessions(callerKey, routeValues, {
      type: params?.type,
      keyword: params?.keyword,
    });
  }

  /** 从服务端同步会话列表到本地缓存，然后返回本地缓存快照 */
  async syncSessions(params?: Partial<SessionListParams>): Promise<SessionMeta[]> {
    const request: SessionListParams = {
      callerKey: this.config.callerKey,
      routeValues: this.config.routeValues,
      ...params,
    };
    const resp = await this.sessionManager.listSessions(request);
    await this.ledger.upsertServerSessions(resp.sessions);
    return this.listCachedSessions(request);
  }

  /** 查询会话列表：先同步服务端，再返回本地缓存快照 */
  async listSessions(params?: Partial<SessionListParams>): Promise<SessionListResp> {
    const request: SessionListParams = {
      callerKey: this.config.callerKey,
      routeValues: this.config.routeValues,
      ...params,
    };
    const resp = await this.sessionManager.listSessions(request);
    await this.ledger.upsertServerSessions(resp.sessions);
    const sessions = await this.listCachedSessions(request);
    return {
      ...resp,
      sessions,
    };
  }

  /** 同步指定会话的历史事件到本地缓存：服务端结果直接覆盖本地 events[] */
  async syncSessionEvents(sessionId: string): Promise<HistoryEvent[]> {
    const state = this.reducer.getState();
    if (state.sessionId === sessionId && this.isRunInProgress()) {
      return this.ledger.getEvents(sessionId);
    }

    const resp = await this.sessionManager.getEvents(sessionId);
    const latestState = this.reducer.getState();
    if (latestState.sessionId === sessionId && this.isRunInProgress()) {
      return this.ledger.getEvents(sessionId);
    }

    return this.ledger.replaceSessionEvents(sessionId, resp.events, {
      sessionId,
      callerKey: this.config.callerKey,
      routeValues: this.config.routeValues ?? [],
      type: 'chat',
      title: resp.title,
    });
  }

  /** 初始化期同步：会话列表 + 当前或最新会话事件 */
  private syncInitialServerState(): void {
    if (this.initialServerSyncPromise) return;

    this.initialServerSyncPromise = this.syncSessions({ page: 1, pageSize: 20 })
      .then(async (sessions) => {
        const currentSessionId = this.reducer.getState().sessionId;
        const targetSession = sessions.find((session) => session.sessionId === currentSessionId) ?? sessions[0];
        if (!targetSession) return;

        try {
          await this.syncSessionEvents(targetSession.sessionId);
          this.asyncTaskManager.setSession(targetSession.sessionId);
          // 初始化回放用的是本地账本快照，服务端同步可能带来更新（含 agentPath 等
          // 账本缓存形态曾缺失的字段）：空闲时用同步后的账本重放一次，保证首屏即最新。
          if (!this.isRunInProgress() && this.reducer.getState().sessionId === targetSession.sessionId) {
            const syncedEvents = await this.ledger.getEvents(targetSession.sessionId);
            if (syncedEvents.length > 0) {
              this.localLastSeq = 0;
              this.localLastSeqByRunId.clear();
              this.reducer.replayEvents(syncedEvents);
            }
          }
        } catch (err) {
          console.warn('[AgentClient] initial session events sync failed:', err);
        }
      })
      .catch((err) => {
        console.warn('[AgentClient] initial session sync failed:', err);
        this.initialServerSyncPromise = null;
      });
  }

  /** 加载指定会话的历史事件：先同步服务端，再返回本地缓存事件 */
  async loadEvents(sessionId: string): Promise<HistoryEvent[]> {
    return this.syncSessionEvents(sessionId);
  }

  /**
   * 切换到指定会话
   *
   * 确保本地缓存存在后，从本地账本读取事件并重建状态。
   */
  async switchSession(sessionId: string): Promise<void> {
    if (this.isRunInProgress() || this.reducer.getState().status === 'recovering') {
      return;
    }

    // 换了个对话，上一个会话的连续失败史不带过来
    this.consecutiveRecoveryRuns = 0;
    const viewVersion = this.sessionViewVersion + 1;
    this.sessionViewVersion = viewVersion;

    let localEvents = await this.ledger.getEvents(sessionId);
    if (localEvents.length === 0) {
      await this.syncSessionEvents(sessionId);
      localEvents = await this.ledger.getEvents(sessionId);
    }

    if (viewVersion !== this.sessionViewVersion) {
      return;
    }

    this.localLastSeq = 0;
    this.localLastSeqByRunId.clear();
    this.reducer.replayEvents(localEvents);
    this.asyncTaskManager.setSession(sessionId);
  }

  // ─── 异步任务 ───────────────────────────────────────────

  /** 主动取完当前会话全部待处理终态异步任务分页。 */
  async listAsyncTasks(): Promise<readonly AsyncTaskItem[]> {
    return this.asyncTaskManager.refresh();
  }

  /** 获取 SDK 当前保存的异步任务快照。 */
  getAsyncTasks(): readonly AsyncTaskItem[] {
    return this.asyncTaskManager.getTasks();
  }

  /** 当前会话是否仍有 Provider 正在跟踪的异步任务。 */
  hasProcessingAsyncTasks(): boolean {
    return this.asyncTaskManager.hasProcessing();
  }

  /** 订阅当前会话异步任务状态；注册后立即回调当前快照。 */
  subscribeAsyncTasks(listener: AsyncTaskListener): () => void {
    return this.asyncTaskManager.subscribe(listener);
  }

  // ─── 工具 ───────────────────────────────────────────────

  /** 注册单个客户端工具 */
  registerTool(tool: ClientTool): this {
    this.registry.register(tool);
    return this;
  }

  /** 批量注册客户端工具 */
  registerTools(tools: ClientTool[]): this {
    this.registry.registerMany(tools);
    return this;
  }

  /** 获取已注册客户端工具定义，供宿主 UI 决定展示形态 */
  getRegisteredTool(name: string, ...aliases: Array<string | undefined>): ClientTool | undefined {
    return this.registry.getFirst([name, ...aliases]);
  }

  /** 从 Plan 等待卡片恢复持久化 Plan；不会再伪装成一条新的用户消息。 */
  resumePlanWithResponse(planExecutionId: string, waitRequestId: string, response: Record<string, unknown> = {}): void {
    this.sendPlanCommand('plan_resume', planExecutionId, {
      planExecutionId,
      waitRequestId,
      response,
    });
  }

  /** 为 FAILED Step 创建一个新的不可变 Attempt 并继续执行。 */
  retryPlanStep(planExecutionId: string, stepId: string): void {
    this.sendPlanCommand('plan_retry', planExecutionId, { planExecutionId, stepId });
  }

  /** 跳过非 required 的 PENDING/FAILED/WAITING Step 并继续。 */
  skipPlanStep(planExecutionId: string, stepId: string): void {
    this.sendPlanCommand('plan_skip', planExecutionId, { planExecutionId, stepId });
  }

  /** 取消当前持久化 Plan；主要用于 WAIT/FAILED 等没有活跃 goroutine 的状态。 */
  cancelPlanExecution(planExecutionId: string): void {
    this.sendPlanCommand('plan_cancel', planExecutionId, { planExecutionId });
  }

  private sendPlanCommand(type: WsMessageType, planExecutionId: string, payload: Record<string, unknown>): void {
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

  /** 拉取 Plan 最新视图和 Attempt 索引并合并到 SDK 状态。 */
  async loadPlanExecutionDetail(planExecutionId: string): Promise<void> {
    const sessionId = this.reducer.getState().sessionId;
    if (!sessionId) return;
    const detail = await this.sessionManager.getPlanExecutionDetail(planExecutionId, sessionId, this.config.callerKey);
    this.reducer.applyPlanDetail(detail.view, detail.attempts ?? []);
  }

  /** 懒加载一个 StepAttempt 的持久化执行详情。 */
  async loadPlanStepEvents(planExecutionId: string, stepAttemptId: string): Promise<void> {
    const sessionId = this.reducer.getState().sessionId;
    if (!sessionId) return;
    const detail = await this.sessionManager.getPlanStepEvents(planExecutionId, stepAttemptId, sessionId, this.config.callerKey);
    this.reducer.applyPlanStepEvents(detail.planExecutionId, detail.stepId, detail.stepAttemptId, detail.attemptNo, detail.events ?? []);
  }

  // ─── 内部：持久化恢复 ────────────────────────────────────

  /**
   * 从本地账本恢复最近会话
   *
   * 构造时自动调用。从 IndexedDB 读取 callerKey + routeValues 匹配的最近 session，
   * replay 其事件流重建 AgentState。如果本地无缓存则跳过。
   */
  private async restoreFromLedger(): Promise<void> {
    const viewVersion = this.sessionViewVersion;

    try {
      const latest = await this.ledger.getLatestSession(
        this.config.callerKey,
        this.config.routeValues,
      );

      if (!latest || viewVersion !== this.sessionViewVersion) {
        return;
      }

      const events = await this.ledger.getEvents(latest.sessionId);
      if (events.length > 0 && viewVersion === this.sessionViewVersion) {
        this.localLastSeq = 0;
        this.localLastSeqByRunId.clear();
        this.reducer.replayEvents(events);
      }

    } catch (err) {
      console.warn('[AgentClient] restore from ledger failed:', err);
    }
  }

  // ─── 内部：事件处理 ─────────────────────────────────────

  /**
   * 统一处理从 WS 收到的事件
   *
   * 处理流程：
   * 1. client_tool_use_start → ToolExecutor 执行并回填
   * 2. 乐观 seq 对齐检查：
   *    - 如果新事件 seq 不连续（跳号），说明有丢失
   *    - 清掉本地账本，从服务端全量拉取后 replay
   * 3. 所有事件交给 EventReducer 聚合状态
   * 4. 异步写入 EventLedger（IndexedDB 账本）
   * 5. 更新 session 元数据到账本
   */
  private handleEvent(event: ReactEvent): void {
    // 前端工具执行
    if (event.type === 'client_tool_use_start') {
      const payload = event.payload as unknown as ClientToolUseStartPayload;
      this.executor.handleClientToolUseStart(payload);
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
        return;
      }
    }

    // 正常处理
    this.persistPendingLiveRunEvent(event);
    this.reducer.applyEvent(event);
    if (event.sessionId && this.reducer.getState().sessionId === event.sessionId) {
      this.asyncTaskManager.setSession(event.sessionId);
      if (this.isTerminalEvent(event)) {
        void this.asyncTaskManager.refresh().catch((error) => {
          console.warn('[AgentClient] async task refresh after run failed:', error);
        });
      }
    }
    this.localLastSeq = Math.max(this.localLastSeq, event.seq);
    if (event.runId && event.seq > 0) {
      this.localLastSeqByRunId.set(event.runId, event.seq);
    }

    this.persistCompletedLiveEvents(event);

    // 更新 session 元数据
    if (event.sessionId) {
      this.ledger.upsertSession({
        sessionId: event.sessionId,
        callerKey: this.config.callerKey,
        routeValues: this.config.routeValues ?? [],
        type: 'chat',
      });

      if (this.isTerminalEvent(event)) {
        const viewVersion = this.sessionViewVersion;
        this.syncSessionEvents(event.sessionId).then(async () => {
          if (viewVersion !== this.sessionViewVersion || this.isRunInProgress()) {
            return;
          }
          const events = await this.ledger.getEvents(event.sessionId!);
          this.reducer.replayEvents(events);
        }).catch((err) => {
          console.warn('[AgentClient] terminal session sync failed:', err);
        });
      }
    }
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

    this.ledger.append({
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
    this.ledger.appendEvents(completedEvents);
  }

  /**
   * 处理 seq 不连续（丢失事件）
   *
   * 从服务端全量同步当前 session 到本地缓存，再从本地缓存 replay。
   */
  private handleSeqGap(currentEvent: ReactEvent, lastSeqInRun: number): void {
    const sessionId = currentEvent.sessionId!;
    const viewVersion = this.sessionViewVersion;
    console.warn(
      `[AgentClient] seq gap detected: runId=${currentEvent.runId}, local=${lastSeqInRun}, received=${currentEvent.seq}.`,
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

    this.syncSessionEvents(sessionId).then(async () => {
      const events = await this.ledger.getEvents(sessionId);
      if (viewVersion !== this.sessionViewVersion) {
        return;
      }
      this.reducer.replayEvents(events);
      this.localLastSeq = 0;
      this.localLastSeqByRunId.clear();
    }).catch((err) => {
      console.warn('[AgentClient] full refetch failed:', err);
    });
  }

  /**
   * WS 连接/重连建立后的处理
   *
   * 若处于「恢复中」，重连成功即触发一次续跑轮询尝试；否则重连后 seq 对齐
   * 由 handleEvent 自动处理。
   */
  private handleWsOpen(): void {
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
   * WS 连接状态变化处理
   *
   * 后端将 run 生命周期绑定在单条 WS 连接上：连接断开即 cancel 当前 run。
   * 因此异常断开时若有 run 进行中，进入「恢复中」态（保留已生成内容），
   * WsClient 自动重连后由 maybeResumeRecovery 尝试自动续跑（开新 run）。
   * 发送阶段尚未拿到 runId 时只保留待发送状态，不能直接中断。
   */
  private handleConnectionChange(connected: boolean): void {
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
  private handleReconnectAttempt(_attempt: number): void {
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

  // ─── 内部：掉线续跑 ─────────────────────────────────────

  /** 进入「恢复中」：记录待恢复的旧 run，重置计数，等重连后轮询终态。 */
  private startRecovery(runId: string): void {
    this.recoveringRunId = runId;
    this.recoveryAttempts = 0;
    this.recoveryAborted = false;
    if (this.recoveryTimer) {
      clearTimeout(this.recoveryTimer);
      this.recoveryTimer = null;
    }
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
    if (this.recoveryAborted || this.recoveringRunId == null) return;
    if (this.reducer.getState().status !== 'recovering') return;

    const runId = this.recoveringRunId;
    const sessionId = this.reducer.getState().sessionId;
    if (!sessionId) {
      this.failRecovery();
      return;
    }

    this.recoveryPolling = true;
    try {
      const events = await this.syncSessionEvents(sessionId);
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
        this.reducer.replayEvents(events as unknown as ReactEvent[]);
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
              ...(this.config.controlContext ?? {}),
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
      console.warn('[AgentClient] recovery poll failed:', err);
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
    if (this.recoveryTimer) {
      clearTimeout(this.recoveryTimer);
      this.recoveryTimer = null;
    }
  }

  private isRunInProgress(): boolean {
    const status = this.reducer.getState().status;
    return status === 'running' || status === 'waiting_client_tool' || status === 'compacting';
  }

  private isTerminalEvent(event: ReactEvent): boolean {
    return event.type === 'done' || event.type === 'error' || event.type === 'cancelled';
  }

  // ─── 内部：URL 构建 ─────────────────────────────────────

  /**
   * 构建 WebSocket URL
   *
   * 支持三种 baseUrl 格式：
   * - 绝对 WS URL: 'ws://host/react' → 直接追加 /ws
   * - 绝对 HTTP URL: 'https://host/react' → http→ws 替换后追加 /ws
   * - 相对路径: '/react' → 根据当前页面 protocol 推导 ws/wss
   */
  private buildWsUrl(): string {
    const base = this.config.baseUrl.replace(/\/$/, '');
    const protocol = typeof location !== 'undefined' && location.protocol === 'https:' ? 'wss:' : 'ws:';
    const host = typeof location !== 'undefined' ? location.host : 'localhost';
    if (base.startsWith('ws://') || base.startsWith('wss://')) {
      return base + '/ws';
    }
    if (base.startsWith('http://') || base.startsWith('https://')) {
      return base.replace(/^http/, 'ws') + '/ws';
    }
    return `${protocol}//${host}${base}/ws`;
  }

  /**
   * 构建 HTTP URL
   *
   * 如果 baseUrl 已是绝对 URL 则直接使用，否则作为相对路径使用。
   */
  private buildHttpUrl(): string {
    const base = this.config.baseUrl.replace(/\/$/, '');
    if (base.startsWith('http://') || base.startsWith('https://')) {
      return base;
    }
    return base;
  }

  private buildModelsUrl(): string {
    return `${this.buildHttpUrl()}/models`;
  }

  private buildUploadUrl(): string {
    return `${this.buildHttpUrl().replace(/\/react$/, '')}/api/chat/files/upload`;
  }
}

/** 工厂函数，创建 AgentClient 实例 */
export function createAgentClient(config: AgentClientConfig): AgentClient {
  return new AgentClient(config);
}
