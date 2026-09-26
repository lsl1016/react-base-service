/**
 * Session 相关类型
 *
 * 对齐 react-base-service 的 Session 数据模型。
 * Session 是一组对话的容器，每个 Session 包含多个 Run（推理轮次），
 * 每个 Run 包含多个 Step（ReAct 步骤）。
 */
import type { EventType } from '../protocol/types';

/** 会话列表中的单个会话项 */
export interface SessionItem {
  /** 会话唯一标识（格式：session_ + uuid） */
  sessionId: string;
  /** 业务标识（如 'report-editor'） */
  callerKey: string;
  /** 路由参数（如 ['report_abc123']） */
  routeValues: string[];
  /** 会话类型，当前仅支持 'chat' */
  type: string;
  /** 会话标题（通常取第一条用户消息的前 N 个字） */
  title: string;
  /** 最后一次推理轮次 ID */
  lastRunId: string;
  /** 最后一条消息的文本摘要 */
  lastMessage: string;
  /** 会话状态：active=活跃 / archived=已归档 / deleted=已删除 */
  state: 'active' | 'archived' | 'deleted' | string;
  /** 创建时间（服务端格式：2006-01-02 15:04:05） */
  createdAt: string;
  /** 最后更新时间（服务端格式：2006-01-02 15:04:05） */
  updatedAt: string;
}

/** session/list 接口请求参数 */
export interface SessionListParams {
  /** 业务标识（必填） */
  callerKey: string;
  /** 路由参数 */
  routeValues?: string[];
  /** 会话类型过滤，当前仅 'chat' */
  type?: string;
  /** 关键词搜索（匹配 title） */
  keyword?: string;
  /** 页码（从 1 开始） */
  page?: number;
  /** 每页条数 */
  pageSize?: number;
}

/** session/list 接口响应 */
export interface SessionListResp {
  /** 会话列表 */
  sessions: SessionItem[];
  /** 符合条件的总记录数 */
  total: number;
  /** 当前页码 */
  page: number;
  /** 每页条数 */
  pageSize: number;
}

/**
 * 历史事件
 *
 * 与实时 WS 的 ReactEvent 结构一致，额外携带 createdAt 字段。
 * 用于切换会话时通过 EventReducer.replayEvents 还原历史对话状态。
 */
export interface HistoryEvent {
  /** 事件类型（同 EventType） */
  type: EventType;
  /** 全局递增序号 */
  seq: number;
  /** 所属推理轮次 ID */
  runId?: string;
  /** 所属会话 ID */
  sessionId?: string;
  /** ReAct 步骤索引 */
  stepIndex?: number;
  /** 多 Agent 事件归属路径（如 main/geo-agent；外层事件缺省，回放/泳道视图依赖） */
  agentPath?: string;
  /** 事件载荷 */
  payload?: Record<string, unknown>;
  /** 事件创建时间（服务端格式：2006-01-02 15:04:05） */
  createdAt?: string;
}

/** session/events 接口响应 */
export interface SessionEventsResp {
  /** 会话 ID */
  sessionId: string;
  /** 会话标题 */
  title: string;
  /** 该会话的全部历史事件（按 seq 排序） */
  events: HistoryEvent[];
}

/** run/feedback 接口请求参数 */
export interface RunFeedbackParams {
  /** 推理轮次 ID */
  runId: string;
  /** 1=点赞 / -1=点踩 / 0=未评价 */
  feedback?: number;
  /** 问题反馈文本 */
  problemFeedback?: string;
}

/** 单个推理轮次的反馈状态 */
export interface RunFeedbackResp {
  runId: string;
  sessionId: string;
  feedback: number;
  problemFeedback: string;
}

/** session/feedback 接口响应 */
export interface SessionFeedbackResp {
  items: RunFeedbackResp[];
}

/** 当前会话中的异步任务；state 表示本地处理状态，executionStatus 表示第三方执行状态。 */
export interface AsyncTaskItem {
  toolUseId: string;
  toolName: string;
  scope: 'outer' | 'plan_step' | string;
  planExecutionId?: string;
  stepAttemptId?: string;
  state: 'pending' | 'resolved' | 'expired' | string;
  executionStatus: 'processing' | 'succeeded' | 'failed' | string;
  providerStatus?: string;
  progress?: number;
  errorMessage?: string;
  createdAt: string;
  updatedAt: string;
  lastObservedAt?: string;
  completedAt?: string;
}

/** async_task/list 游标分页请求。 */
export interface AsyncTaskListParams {
  sessionId: string;
  cursor?: string;
  pageSize?: number;
}

/** async_task/list 游标分页响应。 */
export interface AsyncTaskListResp {
  tasks: AsyncTaskItem[];
  nextCursor?: string;
  hasMore: boolean;
  /** 当前会话是否仍有 Provider 正在跟踪的任务，用于决定前端是否继续轮询。 */
  hasProcessingTasks: boolean;
}

// ─── S3 队列管理（/queue/*，对齐 params.ReactQueueItem 等结构）──────────

/** 队列管理视图里的一条排队输入（run 运行中准入排队的用户消息）。 */
export interface QueueItem {
  /** 账本行 ID（update/reorder/delete 用） */
  id: number;
  /** 账本行 ID 的字符串形态（WS queue_send 用） */
  pendingInputId: string;
  sessionId: string;
  /** 排队消息内容 */
  content: string;
  /** 执行顺序号（FIFO，重排由服务端同步改写） */
  seq: number;
  status: string;
  createdAt: string;
}

/** queue/list 请求参数 */
export interface QueueListParams {
  sessionId: string;
  callerKey: string;
  routeValues?: string[];
}

/** queue/list 响应：items 为 FIFO 排序的排队输入，autoDrain/queueEnabled 回显 steering 配置 */
export interface QueueListResp {
  items: QueueItem[];
  queueEnabled: boolean;
  autoDrain: boolean;
}

/** queue/update 请求参数 */
export interface QueueUpdateParams extends QueueListParams {
  id: number;
  content: string;
}

/** queue/reorder 请求参数：idList 按期望的新顺序给出全部排队输入 ID */
export interface QueueReorderParams extends QueueListParams {
  idList: number[];
}

/** queue/delete 请求参数 */
export interface QueueDeleteParams extends QueueListParams {
  id: number;
}

/** queue/update|reorder|delete 统一响应：claimed=false 表示该输入已被晋升或作废（claim-once 落空） */
export interface QueueMutateResp {
  claimed: boolean;
  queueLength: number;
}