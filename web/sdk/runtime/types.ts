/**
 * Runtime 状态类型
 *
 * 定义了 SDK 对外暴露的核心状态结构 AgentState，
 * 由 EventReducer 从后端事件流中聚合产生。
 *
 * 宿主应用通过 agent.subscribe((state) => {...}) 订阅此状态的实时变化。
 */
import type { CompactState, ModelFallbackPayload, PlanPublicView, RunStats, TodoItem, UsageStats } from '../protocol/types';

export type AgentInputProtoValue = string | number | boolean;

export interface AgentInputShortcutData {
  tag: string;
  proto: Record<string, AgentInputProtoValue>;
  [key: string]: unknown;
}

export type AgentInputPart =
  | { type: 'text'; text: string }
  | {
      type: 'shortcut';
      id: string;
      label: string;
      group: string;
      description: string;
      data: AgentInputShortcutData;
    };

/**
 * Agent 整体状态
 *
 * - idle                — 空闲，等待用户输入
 * - running             — 正在执行 ReAct 推理循环
 * - waiting_client_tool — 等待客户端工具执行并回填
 * - compacting          — 正在进行上下文压缩
 * - done                — 当前 run 正常结束
 * - error               — 当前 run 出错
 * - cancelled           — 当前 run 被用户取消
 */
export type AgentStatus =
  | 'idle'
  | 'running'
  | 'waiting_client_tool'
  | 'compacting'
  | 'recovering'
  | 'done'
  | 'error'
  | 'cancelled';

export type PlanConfirmationStatus = 'pending' | 'submitting' | 'accepted';

/**
 * 单个工具调用的运行时状态
 *
 * status 流转：
 * - server/internal 工具：running → done | error
 * - client 工具：waiting → done | error（客户端执行完毕后由 ToolExecutor 回填）
 * - ask_question：waiting → done（用户作答/跳过）
 * - run 被取消时，仍在 running/waiting 的工具统一收敛为 cancelled
 */
export interface ToolCallState {
  /** 工具调用唯一标识（对应后端 toolUseId） */
  toolUseId: string;
  /** 工具名称 */
  toolName: string;
  /** 工具展示描述，存在时优先用于 UI 展示 */
  description?: string;
  /** 客户端工具的额外匹配标识，可用于按注册别名解析工具定义和 UI */
  frontendHint?: string;
  /** 模型传入的工具输入参数 */
  input: Record<string, unknown>;
  /** 当前执行状态：running=服务端执行中, waiting=等待客户端执行/用户作答, done=完成, error=出错, cancelled=run 取消时未完成 */
  status: 'running' | 'waiting' | 'done' | 'error' | 'cancelled';
  /** 工具执行结果的文本内容 */
  result?: string;
  /** 客户端工具持久化的自定义 UI 状态；不进入模型上下文 */
  meta?: Record<string, unknown>;
  /** 执行是否出错 */
  isError?: boolean;
  /** 执行方：server=HTTP 外部调用, internal=服务端内置, client=前端执行 */
  executedBy: 'server' | 'internal' | 'client';
  /** 执行耗时（毫秒），仅在 status=done 或 error 时有值 */
  durationMs?: number;
  /** 是否在 content_start 之后到达（与事件序一致，用于 Work/Content 分段） */
  afterContent?: boolean;
  /** create_plan 卡片的确认发送状态；其他工具不设置 */
  planConfirmationStatus?: PlanConfirmationStatus;
  /** 最近一次完整 todo_write 卡片对应的最新 Todo 状态；仅用于 UI 展示，不修改工具历史入参 */
  todoItems?: TodoItem[];
  /** Plan 内工具所属执行 ID。 */
  planExecutionId?: string;
  /** Plan 内工具所属 StepAttempt。 */
  stepAttemptId?: string;
  /** 多 Agent 归属路径（子 Agent 产出该工具调用时形如 main/ops-agent）；外层 run 为空 */
  agentPath?: string;
  /** 危险操作确认待答说明（P2-3）：tool_confirm_request 到达时写入，卡片渲染允许/拒绝按钮 */
  confirmReason?: string;
}

/**
 * ReAct 步骤
 *
 * 一次 ReAct 循环对应一个 Step，包含：
 * - thoughts: 模型的思考过程（由 thought_delta 流式拼接）
 * - content: 模型的最终回答（由 content_delta 流式拼接）
 * - toolCalls: 该步骤中触发的工具调用列表
 *
 * 一个 run 可能包含多个 step（模型调工具 → 回填 → 继续推理 → 再次调工具...）
 */
export interface Step {
  /** 步骤索引（从 0 开始，对应后端的 stepIndex） */
  index: number;
  /** 所属推理轮次 ID */
  runId: string;
  /** 多 Agent 归属：子 Agent 产出的步骤形如 main/ops-agent（按 (runId,index) 独立归组） */
  agentPath?: string;
  /** 角色：user=用户输入，assistant=模型回复，compact=上下文压缩分隔标记 */
  role: 'user' | 'assistant' | 'compact';
  /** 模型思考过程的文本（由 thought_delta 流式拼接，thought_end 后为最终值） */
  thoughts: string;
  /** 模型最终回答的文本（由 content_delta 流式拼接，content_end 后为最终值） */
  content: string;
  /** 用户消息的结构化展示片段；content 仍是发给后端的纯文本 */
  displayParts?: AgentInputPart[];
  /** 该步骤中触发的所有工具调用（含服务端工具和客户端工具） */
  toolCalls: ToolCallState[];
  /** 思考内容是否已完整接收（收到 thought_end 后为 true） */
  thoughtComplete: boolean;
  /** 正文阶段已开始（收到 content_start 后为 true，早于首条 content_delta） */
  contentStarted: boolean;
  /** 回答内容是否已完整接收（收到 content_end 后为 true） */
  contentComplete: boolean;
  /** 该步骤是否代表错误（error 或 cancelled 事件） */
  isError?: boolean;
  /** 仅 role='compact' 时有值：上下文压缩的展示信息 */
  compact?: {
    /** 压缩前消息数（实时事件有值，历史回放可能为 0） */
    beforeMessageCount: number;
    /** 压缩后消息数（实时事件有值，历史回放可能为 0） */
    afterMessageCount: number;
    /** 压缩摘要文本，hover 展示 */
    summary: string;
  };
}

export interface PlanAttemptState {
  planExecutionId: string;
  stepId: string;
  stepOrder: number;
  stepAttemptId: string;
  attemptNo: number;
  stepRunId: string;
  status?: string;
  /** 与 outer ReAct 相同形状的执行步骤，但只存在于 Plan 卡片详情中。 */
  steps: Step[];
}

export interface PlanRuntimeState {
  planExecutionId: string;
  /** 归属的外层 Run，用于把原生 Plan 卡片挂到对应用户轮次。 */
  outerRunId?: string;
  view: PlanPublicView;
  attempts: Record<string, PlanAttemptState>;
  attemptOrder: string[];
}

/**
 * Agent 完整状态
 *
 * 由 EventReducer 维护，每次收到后端事件后更新并通过 subscribe 分发给宿主。
 */
export interface AgentState {
  /** 当前 Agent 状态（见 AgentStatus 各状态说明） */
  status: AgentStatus;
  /** WebSocket 连接状态（true=已连接，false=断开） */
  connected: boolean;
  /** 当前会话 ID（首次 run 后由后端分配） */
  sessionId: string | null;
  /** 当前推理轮次 ID（终态快照中保留，下一轮开始或会话重置时清空） */
  currentRunId: string | null;
  /** 当前会话的消息步骤，按追加顺序展示 */
  steps: Step[];
  /** 当前 todo 列表（由 todo_update 事件同步） */
  todos: TodoItem[];
  /** 累计 token 消耗（跨多次 run 累加，resetForNewRun 不重置） */
  usage: UsageStats;
  /** 最近一轮(run)的 token 快照，用于展示上下文窗口占用与缓存占比，未结束过任何轮次时为 null */
  lastRunStats: RunStats | null;
  /** 最近一次上下文压缩的结果，未压缩过时为 null */
  compactState: CompactState | null;
  /** 当前 Run 最近一次后端模型互备切换，用于 UI 提示。 */
  lastModelFallback: ModelFallbackPayload | null;
  /** 当前会话内 Plan 的最新公开视图和折叠执行详情。 */
  plans: Record<string, PlanRuntimeState>;
}
/**
 * 单轮(run)反馈状态
 * feedback: 1=点赞 / -1=点踩 / 0=未评价
 */
export interface RunFeedbackState {
  feedback: number;
  problemFeedback: string;
}

/** 提交反馈时的载荷，feedback 与 problemFeedback 至少传一个 */
export interface RunFeedbackPayload {
  feedback?: number;
  problemFeedback?: string;
}
