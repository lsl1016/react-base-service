/**
 * Session 管理器
 *
 * 封装 react-base-service 的 HTTP 接口，提供会话管理能力：
 * - listSessions: 查询会话列表（支持分页、关键词搜索）
 * - getEvents: 获取指定会话的全部历史事件（用于会话切换时还原状态）
 *
 * 统一处理后端响应格式 { errNo, errMsg, data }：
 * - errNo=0 → 返回 data
 * - errNo!=0 → 抛出 Error
 * - HTTP 非 2xx → 抛出 Error
 *
 * 构造函数接受可选的 fetch 函数注入，方便测试时 mock。
 */
import type { ApiResponse, CheckModelConnectivityReq, ConfigSchemaResp, ContextCompactSettingResp, CreateUserModelReq, PlanExecutionDetailResp, PlanStepEventsResp, UpdateContextCompactSettingReq, UpdateUserModelReq, UserModelItem, VendorModelsResp } from '../protocol/types';
import type {
  AsyncTaskListParams,
  AsyncTaskListResp,
  QueueDeleteParams,
  QueueListParams,
  QueueListResp,
  QueueMutateResp,
  QueueReorderParams,
  QueueUpdateParams,
  RunFeedbackParams,
  RunFeedbackResp,
  SessionEventsResp,
  SessionFeedbackResp,
  SessionListParams,
  SessionListResp,
} from './types';

export class SessionManager {
  private baseUrl: string;
  /** 服务根路径（baseUrl 去掉尾部 /react），承载 /model/*、/setting/* 等非 react 前缀端点 */
  private serviceBase: string;
  private fetchFn: typeof fetch;

  constructor(baseUrl: string, fetchFn?: typeof fetch) {
    const resolvedFetch = fetchFn ?? globalThis.fetch;

    this.baseUrl = baseUrl.replace(/\/$/, '');
    this.serviceBase = this.baseUrl.replace(/\/react$/, '');
    this.fetchFn = resolvedFetch === globalThis.fetch ? resolvedFetch.bind(globalThis) : resolvedFetch;
  }

  /** 查询会话列表 → POST /session/list */
  async listSessions(params: SessionListParams): Promise<SessionListResp> {
    return this.post<SessionListResp>('/session/list', params);
  }

  /** 获取指定会话的全部历史事件 → POST /session/events */
  async getEvents(sessionId: string): Promise<SessionEventsResp> {
    return this.post<SessionEventsResp>('/session/events', { sessionId });
  }

  /** 分页获取指定会话第三方已终态但本地尚未处理的异步任务。 */
  async listAsyncTasks(params: AsyncTaskListParams): Promise<AsyncTaskListResp> {
    return this.post<AsyncTaskListResp>('/async_task/list', params);
  }

  /** 提交单轮反馈 → POST /run/feedback */
  async submitRunFeedback(params: RunFeedbackParams): Promise<RunFeedbackResp> {
    return this.post<RunFeedbackResp>('/run/feedback', params);
  }

  /** 获取指定会话的全部反馈 → POST /session/feedback */
  async getSessionFeedback(sessionId: string): Promise<SessionFeedbackResp> {
    return this.post<SessionFeedbackResp>('/session/feedback', { sessionId });
  }

  /** 查询 Plan 最新公开视图和 Attempt 索引。 */
  async getPlanExecutionDetail(planExecutionId: string, sessionId: string, callerKey: string): Promise<PlanExecutionDetailResp> {
    return this.post<PlanExecutionDetailResp>('/plan_execution/detail', { planExecutionId, sessionId, callerKey });
  }

  /** 查询一个 Plan StepAttempt 的持久化详细事件。 */
  async getPlanStepEvents(planExecutionId: string, stepAttemptId: string, sessionId: string, callerKey: string): Promise<PlanStepEventsResp> {
    return this.post<PlanStepEventsResp>('/plan_execution/events', { planExecutionId, stepAttemptId, sessionId, callerKey });
  }

  /** 查询会话队列（S3）→ POST /queue/list */
  async listQueue(params: QueueListParams): Promise<QueueListResp> {
    return this.post<QueueListResp>('/queue/list', params);
  }

  /** 编辑一条排队输入的内容 → POST /queue/update；claimed=false 表示已被晋升/作废 */
  async updateQueueItem(params: QueueUpdateParams): Promise<QueueMutateResp> {
    return this.post<QueueMutateResp>('/queue/update', params);
  }

  /** 重排会话队列（同步改写账本 seq）→ POST /queue/reorder */
  async reorderQueue(params: QueueReorderParams): Promise<QueueMutateResp> {
    return this.post<QueueMutateResp>('/queue/reorder', params);
  }

  /** 删除（取消）一条排队输入 → POST /queue/delete */
  async deleteQueueItem(params: QueueDeleteParams): Promise<QueueMutateResp> {
    return this.post<QueueMutateResp>('/queue/delete', params);
  }

  // ─── 模型配置面板（/model/*、/setting/context/*、/react/config/schema） ─────
  // 这些端点挂在服务根路径（不含 /react 前缀），统一经 serviceBase 访问。

  /** 厂商模型目录 → GET /models（厂商枚举 key + 可选版本列表） */
  async listVendorModels(): Promise<VendorModelsResp> {
    return this.get<VendorModelsResp>(`${this.serviceBase}/models`);
  }

  /** 模型配置参数 schema（声明式 min/max/step/default）→ GET /react/config/schema */
  async getConfigSchema(): Promise<ConfigSchemaResp> {
    return this.get<ConfigSchemaResp>(`${this.serviceBase}/react/config/schema`);
  }

  /** 模型列表（普通用户=平台默认+自建；白名单用户=全量；API Key 脱敏）→ POST /model/list */
  async listUserModels(): Promise<UserModelItem[]> {
    return this.postAt<UserModelItem[]>('/model/list', {});
  }

  /** 模型详情（API Key 不脱敏，编辑回显用）→ POST /model/detail */
  async getUserModelDetail(id: number): Promise<UserModelItem> {
    return this.postAt<UserModelItem>('/model/detail', { id });
  }

  /** 创建用户/平台模型 → POST /model/create */
  async createUserModel(params: CreateUserModelReq): Promise<UserModelItem> {
    return this.postAt<UserModelItem>('/model/create', params as unknown as Record<string, unknown>);
  }

  /** 编辑用户/平台模型 → POST /model/update */
  async updateUserModel(params: UpdateUserModelReq): Promise<void> {
    await this.postAt<Record<string, unknown>>('/model/update', params as unknown as Record<string, unknown>);
  }

  /** 删除用户/平台模型 → POST /model/delete */
  async deleteUserModel(id: number): Promise<void> {
    await this.postAt<Record<string, unknown>>('/model/delete', { id });
  }

  /** 模型连通性检测 → POST /model/check-connectivity */
  async checkModelConnectivity(params: CheckModelConnectivityReq): Promise<void> {
    await this.postAt<Record<string, unknown>>('/model/check-connectivity', params as unknown as Record<string, unknown>);
  }

  /** 查询上下文压缩策略 → POST /setting/context/get */
  async getContextSetting(): Promise<ContextCompactSettingResp> {
    return this.postAt<ContextCompactSettingResp>('/setting/context/get', {});
  }

  /** 更新上下文压缩策略（字段级覆盖，写后本进程立即生效）→ POST /setting/context/update */
  async updateContextSetting(params: UpdateContextCompactSettingReq): Promise<ContextCompactSettingResp> {
    return this.postAt<ContextCompactSettingResp>('/setting/context/update', params as unknown as Record<string, unknown>);
  }

  private async get<T>(url: string): Promise<T> {
    const res = await this.fetchFn(url, { method: 'GET' });
    if (!res.ok) {
      throw new Error(`HTTP ${res.status}: ${res.statusText}`);
    }
    const json = (await res.json()) as ApiResponse<T>;
    if (json.errNo !== 0) {
      throw new Error(`API Error [${json.errNo}]: ${json.errMsg}`);
    }
    return json.data;
  }

  /** 发往服务根路径（/model/*、/setting/*）的 POST；与 post 的区别仅在前缀拼接。 */
  private async postAt<T>(path: string, body: Record<string, unknown> | object): Promise<T> {
    const res = await this.fetchFn(`${this.serviceBase}${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      throw new Error(`HTTP ${res.status}: ${res.statusText}`);
    }
    const json = (await res.json()) as ApiResponse<T>;
    if (json.errNo !== 0) {
      throw new Error(`API Error [${json.errNo}]: ${json.errMsg}`);
    }
    return json.data;
  }

  private async post<T>(path: string, body: Record<string, unknown> | object): Promise<T> {
    const res = await this.fetchFn(`${this.baseUrl}${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });

    if (!res.ok) {
      throw new Error(`HTTP ${res.status}: ${res.statusText}`);
    }

    const json = (await res.json()) as ApiResponse<T>;
    if (json.errNo !== 0) {
      throw new Error(`API Error [${json.errNo}]: ${json.errMsg}`);
    }

    return json.data;
  }
}