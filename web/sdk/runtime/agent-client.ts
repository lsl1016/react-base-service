/**
 * Agent 客户端
 *
 * SDK 的核心入口，组合以下模块对外提供统一 API：
 * - SessionRuntime: 每会话独立运行时（WS 连接 + 事件状态机 + 游标 + 掉线续跑）
 * - SessionManager: HTTP 会话管理（共享）
 * - ClientToolRegistry + ClientToolExecutor: 客户端工具注册与执行（注册表共享，executor per-runtime）
 * - EventReducer: 事件流聚合为 AgentState（per-runtime）
 * - EventLedger: IndexedDB 事件账本（共享，按 session 存取）
 *
 * 多会话架构（docs/plan/20260930_每会话独立WS连接与会话状态分片方案.md）：
 * 一个会话一份 SessionRuntime（各自的 WS 连接与状态），AgentClient 只做
 * 管理与门面——对外 API 作用于"当前视图 runtime"；切换会话 = 换指针，
 * 不触碰任何连接，原会话 run 在后台继续执行、事件照常入账本。
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
 *   宿主调用 run() → 当前 runtime 的 WsClient 发送 run 消息 → 后端推事件流
 *   → SessionRuntime.handleEvent
 *     → client_tool_use_start 事件交由本 runtime 的 ToolExecutor 执行并回填
 *     → 所有事件交由本 runtime 的 EventReducer 聚合
 *     → 完整语义事件异步写入 EventLedger（IndexedDB 账本，按 session 分片）
 *   → reducer 通知 → AgentClient 过滤"当前视图"后分发外部 subscribers
 *
 * 持久化（默认开启，无需配置）：
 *   构造时自动从 IndexedDB 恢复最近会话 → replayEvents
 *   WS 细块只用于实时 UI，thought/content 完成后再写入本地 events[]
 *   WS 重连后检查 seq 连续性，不连续则在 run 终态后全量拉取
 */
import type { ApiResponse, CheckModelConnectivityReq, ChatFileUpload, ConfigSchemaResp, ConnectionItem, ConnectionModelsResp, ContextCompactSettingResp, CreateConnectionReq, CreateUserModelReq, ReactModelInfo, ReactModelsResp, ReactEvent, UpdateConnectionReq, UpdateContextCompactSettingReq, UpdateUserModelReq, UserModelItem, VendorModelsResp } from '../protocol/types';
import { SessionManager } from '../session/session-manager';
import type { AsyncTaskItem, HistoryEvent, QueueDeleteParams, QueueListParams, QueueListResp, QueueMutateResp, QueueReorderParams, QueueUpdateParams, SessionListParams, SessionListResp } from '../session/types';
import type { SessionMeta } from '../storage/event-ledger';
import { EventLedger } from '../storage/event-ledger';
import { ClientToolRegistry } from '../tools/registry';
import type { ClientTool } from '../tools/types';
import { AsyncTaskManager } from './async-task-manager';
import type { AsyncTaskListener } from './async-task-manager';
import { SessionRuntime } from './session-runtime';
import type { AgentState, RunFeedbackPayload, RunFeedbackState } from './types';
import type { WsClient } from '../client/ws-client';
import type { EventReducer } from './event-reducer';
import type { AgentClientConfig, AgentSessionScope, RunOptions } from './client-types';

export type { AgentClientConfig, AgentClientHooks, AgentSessionScope, BeforeRunHookContext, RunOptions } from './client-types';

/** 草稿 runtime（新会话未发送前尚无服务端 sessionId）在 Map 中的 key */
const DRAFT_KEY = '__draft__';
/** 后台会话空闲多久后断开其 WS 连接（无 run/等待/恢复中才会断，见 canDisconnectIdle） */
const IDLE_DISCONNECT_MS = 60_000;
/** 常驻 runtime 数量上限（LRU 淘汰非当前非运行中的；账本持久在磁盘，销毁可恢复） */
const MAX_RUNTIMES = 8;

/** 多会话状态徽标视图：会话列表渲染后台运行状态用 */
export interface SessionStatusInfo {
  sessionId: string;
  status: AgentState['status'];
  connected: boolean;
  isCurrent: boolean;
}

export class AgentClient {
  private sessionManager: SessionManager;
  private asyncTaskManager: AsyncTaskManager;
  private registry: ClientToolRegistry;
  private ledger: EventLedger;
  private config: AgentClientConfig;
  /** 会话 runtime 表：key 为 sessionId（草稿为 DRAFT_KEY） */
  private runtimes = new Map<string, SessionRuntime>();
  private currentKey: string = DRAFT_KEY;
  /** 外部 subscribers：只接收当前视图 runtime 的状态 */
  private subscribers = new Set<(state: AgentState) => void>();
  /** 初始化期服务端同步，避免构造和 connect 重复触发 */
  private initialServerSyncPromise: Promise<void> | null = null;
  /** 后台会话空闲断开计时 */
  private idleTimers = new Map<string, ReturnType<typeof setTimeout>>();
  /** 宿主是否已要求在线（connect 后为 true）：切到新 runtime 时立即建连，避免首条消息闪 recovering */
  private shouldConnect = false;

  constructor(config: AgentClientConfig) {
    this.config = config;

    const httpUrl = this.buildHttpUrl();
    this.sessionManager = new SessionManager(httpUrl);
    this.asyncTaskManager = new AsyncTaskManager(this.sessionManager);

    this.registry = new ClientToolRegistry();
    this.ledger = new EventLedger();

    // 初始草稿 runtime：restoreFromLedger/syncInitialServerState 会把最近会话恢复进去
    const draft = this.createRuntime(null, DRAFT_KEY);
    this.runtimes.set(DRAFT_KEY, draft);

    // 自动从本地账本恢复（异步，不阻塞构造）
    void this.restoreFromLedger();
    this.syncInitialServerState();
  }

  // ─── runtime 管理（多会话核心） ──────────────────────────

  private createRuntime(sessionId: string | null, key: string): SessionRuntime {
    return new SessionRuntime(
      {
        sessionId,
        wsUrl: this.buildWsUrl(),
        config: this.config,
        registry: this.registry,
        ledger: this.ledger,
        syncSessionEvents: (sid) => this.syncSessionEvents(sid),
        onStateChange: (runtime) => {
          if (runtime.key === this.currentKey) {
            this.notifySubscribers();
          }
        },
        onSessionResolved: (runtime, sessionId) => this.resolveRuntime(runtime, sessionId),
        onTerminalEvent: (runtime) => this.handleRuntimeTerminal(runtime),
      },
      key,
    );
  }

  private get currentRuntime(): SessionRuntime {
    const runtime = this.runtimes.get(this.currentKey);
    if (!runtime) {
      // 防御：当前 runtime 被淘汰/销毁时回落到草稿
      const draft = this.createRuntime(null, DRAFT_KEY);
      this.runtimes.set(DRAFT_KEY, draft);
      this.currentKey = DRAFT_KEY;
      return draft;
    }
    return runtime;
  }

  /** 切换当前视图 runtime：只换指针 + 通知 subscribers；已要求在线时立即建立目标连接。 */
  private setCurrentRuntime(runtime: SessionRuntime): void {
    const previousKey = this.currentKey;
    if (runtime.key !== previousKey) {
      const previous = this.runtimes.get(previousKey);
      this.currentKey = runtime.key;
      this.clearIdleTimer(runtime.key);
      // 原视图若无 run 则进入空闲倒计时；有 run 则连接保持（后台继续跑）
      if (previous && previous !== runtime) {
        this.scheduleIdleDisconnect(previous);
      }
      this.asyncTaskManager.setSession(runtime.sessionId ?? null);
    }
    if (this.shouldConnect && !runtime.isConnected) {
      runtime.wsClient.connect();
    }
    this.notifySubscribers();
  }

  /**
   * 草稿转正：首个 run 事件带回真实 sessionId 后 rekey。
   * 目标 key 已有 runtime（极罕见的竞态：先 switch 过同 id 又在草稿里跑出同一会话）时，
   * 保留现有 runtime，销毁转正者并切回。
   */
  private resolveRuntime(runtime: SessionRuntime, sessionId: string): void {
    const oldKey = runtime.key;
    if (oldKey === sessionId) return;
    const existing = this.runtimes.get(sessionId);
    if (existing && existing !== runtime) {
      const wasCurrent = runtime.key === this.currentKey;
      this.runtimes.delete(oldKey);
      runtime.dispose();
      if (wasCurrent) {
        this.currentKey = existing.key;
        this.asyncTaskManager.setSession(existing.sessionId ?? null);
        this.notifySubscribers();
        void this.hydrateRuntime(existing);
      }
      return;
    }
    const wasCurrent = oldKey === this.currentKey;
    this.runtimes.delete(oldKey);
    this.clearIdleTimer(oldKey);
    runtime.key = sessionId;
    this.runtimes.set(sessionId, runtime);
    if (wasCurrent) {
      this.currentKey = sessionId;
      this.asyncTaskManager.setSession(sessionId);
    }
    this.evictLRU();
  }

  /** run 终态：当前会话刷新异步任务面板；后台会话进入空闲断开倒计时。 */
  private handleRuntimeTerminal(runtime: SessionRuntime): void {
    if (runtime.key === this.currentKey && runtime.sessionId) {
      this.asyncTaskManager.setSession(runtime.sessionId);
      void this.asyncTaskManager.refresh().catch((error) => {
        console.warn('[AgentClient] async task refresh after run failed:', error);
      });
      return;
    }
    this.scheduleIdleDisconnect(runtime);
  }

  /**
   * 空闲后台会话延迟断开：canDisconnectIdle 双重兜底（调度时 + 触发时），
   * 绝不断开有 run/等待中/恢复中的会话。断开只释放连接，状态与账本保留。
   */
  private scheduleIdleDisconnect(runtime: SessionRuntime): void {
    if (!runtime.canDisconnectIdle()) return;
    const key = runtime.key;
    this.clearIdleTimer(key);
    this.idleTimers.set(key, setTimeout(() => {
      this.idleTimers.delete(key);
      const target = this.runtimes.get(key);
      if (!target || target.key === this.currentKey || !target.canDisconnectIdle()) return;
      target.disconnectWs();
    }, IDLE_DISCONNECT_MS));
  }

  private clearIdleTimer(key: string): void {
    const timer = this.idleTimers.get(key);
    if (timer) {
      clearTimeout(timer);
      this.idleTimers.delete(key);
    }
  }

  /** LRU 淘汰：超出上限时销毁最旧的非当前、非运行中 runtime（磁盘账本可恢复）。 */
  private evictLRU(): void {
    for (const [key, runtime] of this.runtimes) {
      if (this.runtimes.size <= MAX_RUNTIMES) return;
      if (key === this.currentKey || !runtime.canDisconnectIdle()) continue;
      this.clearIdleTimer(key);
      this.runtimes.delete(key);
      runtime.dispose();
    }
  }

  /** 首次进入某会话时从本地账本/服务端恢复事件并 replay 到该 runtime。 */
  private async hydrateRuntime(runtime: SessionRuntime): Promise<void> {
    if (runtime.hydrated || !runtime.sessionId) return;
    const sessionId = runtime.sessionId;
    let events: HistoryEvent[] = [];
    try {
      events = await this.ledger.getEvents(sessionId);
    } catch (error) {
      console.warn('[AgentClient] hydrate read ledger failed:', error);
    }
    if (events.length === 0) {
      try {
        events = await this.syncSessionEvents(sessionId);
      } catch (error) {
        console.warn('[AgentClient] hydrate sync events failed:', error);
      }
    }
    if (this.runtimes.get(runtime.key) !== runtime) return; // 期间被销毁/替换
    runtime.replayEvents(events);
    runtime.hydrated = true;
    if (runtime.key === this.currentKey) {
      this.asyncTaskManager.setSession(runtime.sessionId ?? null);
      this.notifySubscribers();
    }
  }

  private notifySubscribers(): void {
    const state = this.currentRuntime.reducer.getState();
    for (const fn of this.subscribers) {
      fn(state);
    }
  }

  // ─── 连接 ───────────────────────────────────────────────

  connect(): void {
    this.shouldConnect = true;
    this.syncInitialServerState();
    this.currentRuntime.wsClient.connect();
  }

  disconnect(): void {
    for (const runtime of this.runtimes.values()) {
      runtime.dispose();
    }
    this.runtimes.clear();
    for (const key of [...this.idleTimers.keys()]) {
      this.clearIdleTimer(key);
    }
    this.asyncTaskManager.dispose();
    this.ledger.close();
  }

  get isConnected(): boolean {
    return this.currentRuntime.isConnected;
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

  // ─── 模型配置面板（厂商与密钥 / 模型与参数 / 上下文与压缩） ───────────────
  // 薄封装：全部经 SessionManager 落到 /model/*、/setting/context/*、/react/config/schema。

  /** 厂商模型目录（厂商枚举 key + 可选版本列表）。 */
  async listVendorModels(): Promise<VendorModelsResp> {
    return this.sessionManager.listVendorModels();
  }

  /** 模型配置面板参数 schema（声明式 min/max/step/default）。 */
  async getConfigSchema(): Promise<ConfigSchemaResp> {
    return this.sessionManager.getConfigSchema();
  }

  /** 模型列表（平台默认 + 自建；API Key 脱敏）。 */
  async listUserModels(): Promise<UserModelItem[]> {
    return this.sessionManager.listUserModels();
  }

  /** 模型详情（API Key 不脱敏，编辑回显用）。 */
  async getUserModelDetail(id: number): Promise<UserModelItem> {
    return this.sessionManager.getUserModelDetail(id);
  }

  /** 创建用户/平台模型（服务端会先做连通性检测）。 */
  async createUserModel(params: CreateUserModelReq): Promise<UserModelItem> {
    return this.sessionManager.createUserModel(params);
  }

  /** 编辑用户/平台模型。 */
  async updateUserModel(params: UpdateUserModelReq): Promise<void> {
    return this.sessionManager.updateUserModel(params);
  }

  /** 删除用户/平台模型。 */
  async deleteUserModel(id: number): Promise<void> {
    return this.sessionManager.deleteUserModel(id);
  }

  /** 模型连通性检测（支持自定义接入面）。 */
  async checkModelConnectivity(params: CheckModelConnectivityReq): Promise<void> {
    return this.sessionManager.checkModelConnectivity(params);
  }

  // ─── LLM 连接管理（协议+接入地址+Key，模型配置的主体形态） ───────────────

  /** 连接列表（key 脱敏 + 引用模型数）。 */
  async listConnections(): Promise<ConnectionItem[]> {
    return this.sessionManager.listConnections();
  }

  /** 创建连接。 */
  async createConnection(params: CreateConnectionReq): Promise<void> {
    return this.sessionManager.createConnection(params);
  }

  /** 编辑连接（apiKey 留空=保持原值）。 */
  async updateConnection(params: UpdateConnectionReq): Promise<void> {
    return this.sessionManager.updateConnection(params);
  }

  /** 删除连接（仍被模型引用时后端拒绝）。 */
  async deleteConnection(id: number): Promise<void> {
    return this.sessionManager.deleteConnection(id);
  }

  /** 连接连通性检测（/models + 最小对话，错误透传上游真因）。 */
  async checkConnection(id: number): Promise<void> {
    return this.sessionManager.checkConnection(id);
  }

  /** 拉取连接可用模型列表（服务端代理 GET {base}/models）。 */
  async fetchConnectionModels(id: number): Promise<ConnectionModelsResp> {
    return this.sessionManager.fetchConnectionModels(id);
  }

  /** 查询上下文压缩策略生效视图。 */
  async getContextSetting(): Promise<ContextCompactSettingResp> {
    return this.sessionManager.getContextSetting();
  }

  /** 更新上下文压缩策略（字段级覆盖，写后本进程立即生效）。 */
  async updateContextSetting(params: UpdateContextCompactSettingReq): Promise<ContextCompactSettingResp> {
    return this.sessionManager.updateContextSetting(params);
  }

  // ─── 运行（作用于当前会话 runtime） ───────────────────────

  /**
   * 进入新对话草稿态：创建独立草稿 runtime 并切换视图。
   * 运行中允许新建——原会话的后台运行不受影响（这是多会话并行的入口）。
   */
  newSession(): boolean {
    const oldDraft = this.runtimes.get(DRAFT_KEY);
    const draft = this.createRuntime(null, DRAFT_KEY);
    this.runtimes.set(DRAFT_KEY, draft);
    if (oldDraft && oldDraft !== draft) {
      oldDraft.dispose();
    }
    this.clearIdleTimer(DRAFT_KEY);
    this.currentKey = DRAFT_KEY;
    if (this.shouldConnect && !draft.isConnected) {
      draft.wsClient.connect();
    }
    this.asyncTaskManager.setSession(null);
    this.notifySubscribers();
    return true;
  }

  /**
   * 发起一次推理（作用于当前会话）：空闲走新 run；运行中走 steer 准入（默认排队）。
   */
  run(userPrompt: string, options?: RunOptions): void | Promise<void> {
    return this.currentRuntime.run(userPrompt, options);
  }

  /** 确认计划：以一条新 run 指示模型开始执行刚提交的计划。 */
  confirmPlan(planId: string): void | Promise<void> {
    return this.currentRuntime.confirmPlan(planId);
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

  /** 取消当前会话正在进行的 run；恢复中则中止自动续跑。 */
  cancel(): void {
    this.currentRuntime.cancel();
  }

  /** 回填危险操作确认（P2-3）的允许/拒绝。 */
  sendToolConfirmAnswer(toolUseId: string, approved: boolean, reason?: string): void {
    this.currentRuntime.sendToolConfirmAnswer(toolUseId, approved, reason);
  }

  /** 回填 ask_question 的用户答案。 */
  sendAskQuestionAnswer(toolUseId: string, content: import('../protocol/types').AskQuestionAnswerContent): void {
    this.currentRuntime.sendAskQuestionAnswer(toolUseId, content);
  }

  /**
   * 清空当前对话。
   *
   * 同时清空运行态和当前作用域下的本地账本，避免重新挂载后又从 IndexedDB 恢复旧对话。
   */
  async clearConversation(): Promise<void> {
    this.currentRuntime.clearConversationState();
    try {
      await this.ledger.clearSessions(this.config.callerKey, this.config.routeValues);
    } catch (error) {
      console.warn('[AgentClient] clear conversation ledger failed:', error);
    }
  }

  // ─── 状态 ───────────────────────────────────────────────

  /** 获取当前视图会话的状态快照 */
  getState(): AgentState {
    return this.currentRuntime.reducer.getState();
  }

  /** 获取当前会话的业务路由作用域，返回副本避免外部修改运行配置。 */
  getSessionScope(): AgentSessionScope {
    return {
      callerKey: this.config.callerKey,
      routeValues: [...(this.config.routeValues ?? [])],
    };
  }

  /**
   * 订阅当前视图会话的状态变化（切换会话时会以新会话状态立即回调一次）。
   *
   * 注册时立即回调一次当前状态（方便初始化）。
   * 返回 unsubscribe 函数。
   */
  subscribe(fn: (state: AgentState) => void): () => void {
    this.subscribers.add(fn);
    fn(this.getState());
    return () => {
      this.subscribers.delete(fn);
    };
  }

  /** 多会话状态徽标：各 runtime 的运行/连接状态（供会话列表渲染后台运行中标记）。 */
  getSessionStatuses(): SessionStatusInfo[] {
    const statuses: SessionStatusInfo[] = [];
    for (const runtime of this.runtimes.values()) {
      if (runtime.key === DRAFT_KEY || !runtime.sessionId) continue;
      const state = runtime.reducer.getState();
      statuses.push({
        sessionId: runtime.sessionId,
        status: state.status,
        connected: runtime.isConnected,
        isCurrent: runtime.key === this.currentKey,
      });
    }
    return statuses;
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

  /**
   * 切换到指定会话：运行中允许切换（只换视图指针，原会话 run 后台继续）。
   * 目标会话 runtime 不存在时创建并从账本/服务端懒恢复。
   */
  async switchSession(sessionId: string): Promise<void> {
    let runtime = this.runtimes.get(sessionId);
    if (!runtime) {
      runtime = this.createRuntime(sessionId, sessionId);
      this.runtimes.set(sessionId, runtime);
      this.evictLRU();
    }
    this.setCurrentRuntime(runtime);
    if (!runtime.hydrated) {
      await this.hydrateRuntime(runtime);
    }
  }

  /**
   * 删除一个会话（硬删级联，不可恢复；运行中的会话服务端拒绝）。
   * 删除成功后本地同步清理：dispose 该会话 runtime（断连）、清 IndexedDB 账本；
   * 若删的是当前视图会话，自动切到新会话草稿。
   */
  async deleteSession(sessionId: string): Promise<void> {
    await this.sessionManager.deleteSession({
      sessionId,
      callerKey: this.config.callerKey,
      routeValues: this.config.routeValues,
    });

    // 本地 runtime 摘除（多会话架构下可能是挂着空闲连接的后台会话）
    const runtime = this.runtimes.get(sessionId);
    if (runtime) {
      this.runtimes.delete(sessionId);
      this.clearIdleTimer(sessionId);
      runtime.dispose();
    }
    // 本地账本清理
    try {
      await this.ledger.clearSession(sessionId);
    } catch (error) {
      console.warn('[AgentClient] clear deleted session ledger failed:', error);
    }
    // 删的是当前视图会话：切到新会话草稿，避免 UI 悬空
    if (this.currentKey === sessionId) {
      this.newSession();
    }
  }

  /**
   * 基于历史会话分叉出一个新会话，并把视图切换过去。
   * run 级入口（前端消息天然携带 runId 而无 messageId）：
   * inclusive=true（缺省）带着 throughRunId 这轮继续，false 回到这轮提问之前。
   * 新会话由服务端分配 sessionId，直接走 switchSession 懒恢复（无需草稿转正）。
   */
  async forkSession(sessionId: string, options: {
    throughRunId?: string;
    throughMessageId?: string;
    inclusive?: boolean;
    title?: string;
  } = {}): Promise<string> {
    const resp = await this.sessionManager.forkSession({
      sessionId,
      throughRunId: options.throughRunId,
      throughMessageId: options.throughMessageId,
      inclusive: options.inclusive,
      title: options.title,
      callerKey: this.config.callerKey,
      routeValues: this.config.routeValues,
    });
    // 同步服务端列表让新会话进入本地缓存，再切换视图并懒恢复历史事件
    await this.syncSessions();
    await this.switchSession(resp.sessionId);
    return resp.sessionId;
  }

  /**
   * 重命名会话（仅标题元数据，运行中的会话也可改名）。
   * 成功后更新本地会话缓存的标题；返回服务端归一化后的标题。
   */
  async renameSession(sessionId: string, title: string): Promise<string> {
    const resp = await this.sessionManager.renameSession({
      sessionId,
      title,
      callerKey: this.config.callerKey,
      routeValues: this.config.routeValues,
    });
    try {
      // 带调用方归属写入本地缓存：会话已在列表缓存时仅合并标题，不在时也能出现在
      // 调用方作用域的列表里（AgentPanel 侧随后还会整体 syncSessions 校正）
      await this.ledger.upsertSession({
        sessionId,
        title: resp.title,
        callerKey: this.config.callerKey,
        routeValues: this.config.routeValues ?? [],
      });
    } catch (error) {
      console.warn('[AgentClient] rename update ledger failed:', error);
    }
    return resp.title;
  }

  /** 同步指定会话的历史事件到本地缓存：服务端结果直接覆盖本地 events[] */
  async syncSessionEvents(sessionId: string): Promise<HistoryEvent[]> {
    const runtime = this.runtimes.get(sessionId);
    if (runtime && runtime.isRunInProgress()) {
      return this.ledger.getEvents(sessionId);
    }

    const resp = await this.sessionManager.getEvents(sessionId);
    const latestRuntime = this.runtimes.get(sessionId);
    if (latestRuntime && latestRuntime.isRunInProgress()) {
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
        const runtime = this.currentRuntime;
        const currentSessionId = runtime.reducer.getState().sessionId;
        const targetSession = sessions.find((session) => session.sessionId === currentSessionId) ?? sessions[0];
        if (!targetSession) return;

        try {
          await this.syncSessionEvents(targetSession.sessionId);
          this.asyncTaskManager.setSession(targetSession.sessionId);
          // 初始化回放用的是本地账本快照，服务端同步可能带来更新（含 agentPath 等
          // 账本缓存形态曾缺失的字段）：空闲时用同步后的账本重放一次，保证首屏即最新。
          const latest = this.runtimes.get(runtime.key) === runtime ? runtime : this.currentRuntime;
          if (latest === this.currentRuntime && !latest.isRunInProgress()
            && latest.reducer.getState().sessionId === targetSession.sessionId) {
            const syncedEvents = await this.ledger.getEvents(targetSession.sessionId);
            if (syncedEvents.length > 0) {
              latest.hydrated = true;
              latest.replayEvents(syncedEvents);
              this.notifySubscribers();
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

  // ─── 队列（S3，作用于当前会话） ───────────────────────────

  /** 队列管理请求的公共字段；无会话时返回 null（调用方据此跳过请求）。 */
  private queueBaseParams(): QueueListParams | null {
    const sessionId = this.getState().sessionId;
    if (!sessionId) return null;
    return {
      sessionId,
      callerKey: this.config.callerKey,
      routeValues: this.config.routeValues,
    };
  }

  /** 查询当前会话的排队输入（FIFO）；无会话时返回空队列。 */
  async listQueue(): Promise<QueueListResp> {
    const base = this.queueBaseParams();
    if (!base) {
      return { items: [], queueEnabled: false, autoDrain: false };
    }
    return this.sessionManager.listQueue(base);
  }

  /** 编辑一条排队输入的内容；claimed=false 表示该输入已被晋升或作废，编辑未生效。 */
  async updateQueueItem(id: number, content: string): Promise<QueueMutateResp> {
    const base = this.queueBaseParams();
    if (!base) throw new Error('当前无会话，无法编辑排队输入');
    const params: QueueUpdateParams = { ...base, id, content };
    return this.sessionManager.updateQueueItem(params);
  }

  /** 重排会话队列（服务端同步改写账本 seq）；请求非法/并发变化时抛错。 */
  async reorderQueue(idList: number[]): Promise<QueueMutateResp> {
    const base = this.queueBaseParams();
    if (!base) throw new Error('当前无会话，无法重排队列');
    const params: QueueReorderParams = { ...base, idList };
    return this.sessionManager.reorderQueue(params);
  }

  /** 删除（取消）一条排队输入；claimed=false 表示该输入已被晋升或作废，删除未生效。 */
  async deleteQueueItem(id: number): Promise<QueueMutateResp> {
    const base = this.queueBaseParams();
    if (!base) throw new Error('当前无会话，无法删除排队输入');
    const params: QueueDeleteParams = { ...base, id };
    return this.sessionManager.deleteQueueItem(params);
  }

  /**
   * 显式发送一条排队输入（S3 双模式）：run 模式空闲时开新 run；
   * inject 模式把该排队项晋升为活跃 run 的 guide 注入当前对话。
   */
  sendQueuedMessage(pendingInputId: string, mode: 'run' | 'inject' = 'run'): void {
    this.currentRuntime.sendQueuedMessage(pendingInputId, mode);
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
    this.currentRuntime.sendPlanCommand('plan_resume', planExecutionId, {
      planExecutionId,
      waitRequestId,
      response,
    });
  }

  /** 为 FAILED Step 创建一个新的不可变 Attempt 并继续执行。 */
  retryPlanStep(planExecutionId: string, stepId: string): void {
    this.currentRuntime.sendPlanCommand('plan_retry', planExecutionId, { planExecutionId, stepId });
  }

  /** 跳过非 required 的 PENDING/FAILED/WAITING Step 并继续执行。 */
  skipPlanStep(planExecutionId: string, stepId: string): void {
    this.currentRuntime.sendPlanCommand('plan_skip', planExecutionId, { planExecutionId, stepId });
  }

  /** 取消当前持久化 Plan；主要用于 WAIT/FAILED 等没有活跃 goroutine 的状态。 */
  cancelPlanExecution(planExecutionId: string): void {
    this.currentRuntime.sendPlanCommand('plan_cancel', planExecutionId, { planExecutionId });
  }

  /** 拉取 Plan 最新视图和 Attempt 索引并合并到 SDK 状态。 */
  async loadPlanExecutionDetail(planExecutionId: string): Promise<void> {
    const sessionId = this.getState().sessionId;
    if (!sessionId) return;
    const detail = await this.sessionManager.getPlanExecutionDetail(planExecutionId, sessionId, this.config.callerKey);
    this.currentRuntime.reducer.applyPlanDetail(detail.view, detail.attempts ?? []);
  }

  /** 懒加载一个 StepAttempt 的持久化执行详情。 */
  async loadPlanStepEvents(planExecutionId: string, stepAttemptId: string): Promise<void> {
    const sessionId = this.getState().sessionId;
    if (!sessionId) return;
    const detail = await this.sessionManager.getPlanStepEvents(planExecutionId, stepAttemptId, sessionId, this.config.callerKey);
    this.currentRuntime.reducer.applyPlanStepEvents(detail.planExecutionId, detail.stepId, detail.stepAttemptId, detail.attemptNo, detail.events ?? []);
  }

  // ─── 内部：持久化恢复 ────────────────────────────────────

  /**
   * 从本地账本恢复最近会话（作用于初始草稿 runtime）
   *
   * 构造时自动调用。从 IndexedDB 读取 callerKey + routeValues 匹配的最近 session，
   * replay 其事件流重建 AgentState。如果本地无缓存则跳过。
   */
  private async restoreFromLedger(): Promise<void> {
    const runtime = this.currentRuntime;
    const viewVersion = runtime.sessionViewVersion;

    try {
      const latest = await this.ledger.getLatestSession(
        this.config.callerKey,
        this.config.routeValues,
      );

      if (!latest || viewVersion !== runtime.sessionViewVersion || this.currentRuntime !== runtime) {
        return;
      }

      const events = await this.ledger.getEvents(latest.sessionId);
      if (events.length > 0 && viewVersion === runtime.sessionViewVersion && this.currentRuntime === runtime) {
        runtime.hydrated = true;
        runtime.replayEvents(events);
        this.notifySubscribers();
      }

    } catch (err) {
      console.warn('[AgentClient] restore from ledger failed:', err);
    }
  }

  // ─── 内部兼容访问器（测试与旧集成直接触达当前 runtime 的会话级成员） ─────

  /** 当前视图 runtime 的 WS 连接（测试兼容访问）。 */
  private get wsClient(): WsClient {
    return this.currentRuntime.wsClient;
  }

  /** 当前视图 runtime 的状态机（测试兼容访问）。 */
  private get reducer(): EventReducer {
    return this.currentRuntime.reducer;
  }

  /** 事件入口：转发到当前视图 runtime（测试兼容访问）。 */
  private handleEvent(event: ReactEvent): void {
    this.currentRuntime.handleEvent(event);
  }

  private handleWsOpen(): void {
    this.currentRuntime.handleWsOpen();
  }

  private handleConnectionChange(connected: boolean): void {
    this.currentRuntime.handleConnectionChange(connected);
  }

  private handleReconnectAttempt(attempt: number): void {
    this.currentRuntime.handleReconnectAttempt(attempt);
  }

  private handleSeqGap(event: ReactEvent, lastSeqInRun: number): void {
    this.currentRuntime.handleSeqGap(event, lastSeqInRun);
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
