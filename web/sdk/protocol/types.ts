/**
 * WebSocket 消息协议类型
 *
 * 1:1 对齐 react-base-service/components/params/react.go
 * 定义了客户端与服务端之间的 WebSocket 通信协议。
 *
 * 通信方向：
 * - 客户端 → 服务端：run（发起推理）、cancel（取消）、client_tool_use_end（前端工具结果回填）
 * - 服务端 → 客户端：ReactEvent 流式事件（思考、回答、工具调用、完成、错误等）
 *
 * 一次完整的 run 生命周期：
 *   客户端发 run → 服务端返回 thought/content/tool_use 事件流 → 最终 done/error/cancelled
 *   中间如果模型决定调用 client 工具：
 *     服务端发 client_tool_use_start → 客户端执行 → 客户端发 client_tool_use_end → 服务端继续推理
 */

// ─── 客户端 → 服务端 ───────────────────────────────────────────────

/** 客户端可发送的消息类型 */
export type WsMessageType =
  | 'run'
  | 'cancel'
  | 'client_tool_use_end'
  | 'tool_use_answer'
  | 'tool_confirm_answer'
  | 'plan_resume'
  | 'plan_retry'
  | 'plan_skip'
  | 'plan_cancel';

/**
 * WebSocket 消息帧
 *
 * 客户端发给服务端的每一条消息都遵循此结构：
 * - type: 'run'               — 发起一次推理
 * - type: 'cancel'            — 取消当前 run（需要 runId）
 * - type: 'client_tool_use_end' — 前端工具执行完毕，回填结果（payload 为 ClientToolUseEndPayload）
 * - type: 'tool_use_answer'   — 向处于 waiting 状态的内置工具回填用户输入（payload 为 ToolUseAnswerPayload）
 */
export interface WsMessage {
  /** 消息类型：发起推理 / 取消 / 前端工具结果回填 */
  type: WsMessageType;
  /** 当前推理轮次 ID（cancel 和 client_tool_use_end 时必填） */
  runId?: string;
  /** 会话 ID（run 时可选，用于关联到已有会话） */
  sessionId?: string;
  /** 消息载荷，按 type 不同对应不同结构 */
  payload?: Record<string, unknown>;
}

/** 随本轮用户消息提供给模型的动态业务上下文。 */
export type LlmContext = string | Record<string, unknown>;

/**
 * run 消息的 payload
 *
 * 发起一次 ReAct 推理循环。服务端根据 callerKey + routeValues 定位/创建 Session，
 * 然后进入 ReAct 主循环（模型生成 → 工具调用 → 回填 → 继续），直到模型给出最终回答或超过 maxSteps。
 *
 * @example
 * {
 *   callerKey: 'report-editor',
 *   routeValues: ['report_abc123'],
 *   type: 'chat',
 *   userPrompt: '帮我创建一个柱状图',
 *   modelKey: 'claude-sonnet',
 *   maxSteps: 10,
 * }
 */
export type ExecutionMode = 'react' | 'plan';

export interface RunPayload {
  /** 业务标识，用于后端区分不同调用方（如 'report-editor'） */
  callerKey: string;
  /** 路由参数，后端根据 callerKey + routeValues 定位或创建 Session */
  routeValues?: string[];
  /** 会话类型，当前仅支持 'chat' */
  type: string;
  /** 用户输入的自然语言提示 */
  userPrompt: string;
  /** 用户输入来源；快捷下一步会携带其所属回答位置，普通输入可省略 */
  inputOrigin?: UserInputOrigin;
  /** 本轮随用户消息发送的文本附件 */
  attachments?: ReactAttachmentRef[];
  /** 运行控制参数（可选），给后端运行态和工具执行链路使用 */
  controlContext?: Record<string, unknown>;
  /** 模型业务上下文（可选），字符串按纯文本、对象按 JSON 随用户消息一起发给模型 */
  llmContext?: LlmContext;
  /** 模型标识（如 'claude-sonnet'），与 modelVersion 配合使用 */
  modelKey?: string;
  /** 模型版本（如 '20250514'） */
  modelVersion?: string;
  /** 模型配置哈希，可替代 modelKey + modelVersion */
  modelHash?: string;
  /** ReAct 最大推理步骤数，超过后强制结束 */
  maxSteps?: number;
  /** 执行范式；未传时服务端按 react 兼容旧客户端。 */
  executionMode?: ExecutionMode;
}

/**
 * 掉线自动续跑的标记键，由客户端塞进 controlContext 随 run 发出。
 *
 * 后端把 controlContext 当不透明 JSON 存取，回放时原样带回，因此实时与历史
 * 两条路径都能据此识别出「这条用户消息是自动续跑发的」并隐藏掉，让恢复过程
 * 对用户无感。改名会导致存量会话的历史回放漏网。
 */
export const RECOVERY_CONTINUE_FLAG = 'recoveryContinue';

export type UserInputOrigin =
  | { type: 'manual' }
  | {
      type: 'next_button';
      sourceRunId: string;
      sourceStepIndex: number;
      buttonText: string;
      buttonIndex: number;
    };

/** ReAct run 中引用的已上传附件 */
export interface ReactAttachmentRef {
  fileId: string;
  fileName?: string;
  description?: string;
}

/** /api/chat/files/upload 返回的附件元信息 */
export interface ChatFileUpload {
  fileId: string;
  fileName: string;
  ext: string;
  size: number;
  uploadedAt: string;
}

/** client_tool_use_end 消息的 payload，包含一个或多个前端工具的执行结果 */
export interface ClientToolUseEndPayload {
  /** 前端工具执行结果数组（支持一次回填多个工具结果） */
  toolOutputs: ClientToolOutput[];
  /** Plan 内客户端工具所属执行 ID；outer 工具为空。 */
  planExecutionId?: string;
  /** Plan 内客户端工具所属 StepAttempt；outer 工具为空。 */
  stepAttemptId?: string;
}

/** 单个前端工具的执行结果 */
export interface ClientToolOutput {
  /** 对应 client_tool_use_start 事件中的 toolUseId，用于服务端匹配 */
  toolUseId: string;
  /** 返回给模型的结果；可直接回填文本，也可回填 JSON 结构 */
  content: string | number | boolean | null | Record<string, unknown> | unknown[];
  /** 仅用于工具 UI 状态持久化和历史回放，不会传给模型 */
  meta?: Record<string, unknown>;
  /** 是否执行出错，出错时 content 为错误描述 */
  isError?: boolean;
  /** 服务端回放或补偿时下发的工具终态；旧客户端可省略。 */
  status?: string;
}

/**
 * tool_use_answer 消息的 payload（通用信封）
 *
 * 服务端在 tool_use_start（status='waiting'）后阻塞等待此消息，按 toolUseId 匹配；
 * content 的结构由该处于等待状态的工具自行解释（ask_question 对应 AskQuestionAnswerContent）。
 */
export interface ToolUseAnswerPayload {
  /** 对应 tool_use_start 事件中的 toolUseId，用于服务端匹配 */
  toolUseId: string;
  /** 用户输入内容，结构由等待中的工具定义 */
  content: Record<string, unknown>;
}

/** ask_question 中单个问题的回答；label 由服务端按原始 questions 反查，前端只传选项 id */
export interface AskQuestionAnswerItem {
  /** 对应 toolInput.questions[].id */
  questionId: string;
  /** 所选选项 id 数组；单选也是数组（长度 1），只填自由输入时传空数组 */
  selectedOptionIds: string[];
  /** 「其他」自由输入内容，未填写时传空字符串 */
  freeText: string;
}

/** tool_confirm_answer 消息的 payload（P2-3 危险操作确认作答） */
export interface ToolConfirmAnswerPayload {
  /** 对应 tool_confirm_request 事件中的 toolUseId */
  toolUseId: string;
  /** true=允许执行，false=拒绝 */
  approved: boolean;
  /** 拒绝/允许原因（可选，回填给模型与审计日志） */
  reason?: string;
}

/**
 * ask_question 工具对 tool_use_answer.content 的内容结构
 *
 * 每个 questionId 都必须出现在 answers 中；用户跳过整个提问时 skipped=true、answers 传空数组。
 */
export interface AskQuestionAnswerContent {
  /** 各问题的回答 */
  answers: AskQuestionAnswerItem[];
  /** 用户是否跳过了整个提问 */
  skipped?: boolean;
}

// ─── 服务端 → 客户端 ───────────────────────────────────────────────

export interface ReactModelInfo {
  modelKey: string;
  modelVersion: string;
  displayName: string;
}

export interface ReactModelsResp {
  models: ReactModelInfo[];
  defaultModel: ReactModelInfo;
}

export interface ModelFallbackPayload {
  fromModelKey: string;
  fromModelVersion: string;
  toModelKey: string;
  toModelVersion: string;
  reason: string;
  resetCurrentOutput: boolean;
}

export interface PlanStepResultRef {
  plan_execution_id?: string;
  plan_version_id?: string;
  step_id?: string;
  step_attempt_id: string;
  step_result_id: string;
}

export interface PlanStepPublicView {
  step_id: string;
  step_order: number;
  step_name?: string;
  step_type?: 'AGENT' | 'USER_INPUT' | 'USER_ACTION' | string;
  required?: boolean;
  status: string;
  summary: string;
  public_fields?: Record<string, unknown>;
  step_result_ref?: PlanStepResultRef;
}

export interface PlanWaitRequest {
  request_id?: string;
  type: 'USER_INPUT' | 'USER_ACTION' | 'EXTERNAL_TASK' | string;
  question?: string;
  response_schema?: Record<string, unknown>;
  tool_use_id?: string;
  frontend_hint?: string;
  pending_tool_use_ids?: string[];
  data?: unknown;
}

export interface PlanPublicError {
  code: string;
  summary: string;
}

export interface PlanPublicView {
  plan_execution_id: string;
  status: string;
  summary: string;
  steps: PlanStepPublicView[];
  current_step?: PlanStepPublicView;
  result?: Record<string, unknown>;
  wait_request?: PlanWaitRequest;
  error?: PlanPublicError;
  can_resume: boolean;
  updated_at: string;
}

export interface PlanAttemptItem {
  planExecutionId: string;
  stepId: string;
  stepAttemptId: string;
  attemptNo: number;
  status: string;
  stepRunId?: string;
  errorCode?: string;
  errorSummary?: string;
  createdAt: string;
  updatedAt: string;
}

export interface PlanExecutionDetailResp {
  view: PlanPublicView;
  attempts: PlanAttemptItem[];
}

export interface PlanStepEventsResp {
  planExecutionId: string;
  stepAttemptId: string;
  stepId: string;
  attemptNo: number;
  events: ReactEvent[];
}

export interface PlanResumeRequest {
  planExecutionId: string;
  waitRequestId: string;
  response: Record<string, unknown>;
}

export interface PlanRetryRequest {
  planExecutionId: string;
  stepId: string;
}

export interface PlanSkipRequest {
  planExecutionId: string;
  stepId: string;
}

export interface PlanCancelRequest {
  planExecutionId: string;
}

export interface PlanViewUpdatePayload {
  planExecutionId: string;
  view: PlanPublicView;
}

export interface PlanStepEventPayload {
  planExecutionId: string;
  planVersionId: string;
  stepId: string;
  stepOrder: number;
  stepAttemptId: string;
  attemptNo: number;
  stepRunId: string;
  event: ReactEvent;
}

/**
 * 服务端事件类型
 *
 * 按生命周期阶段分类：
 * - 思考阶段：thought_start → thought_delta(流式) → thought_end
 * - 回答阶段：content_start → content_delta(流式) → content_end
 * - 工具调用：tool_use_start(服务端/内置工具) → tool_use_end
 *              client_tool_use_start(前端工具) → client_tool_use_end
 * - 上下文压缩：compact_start → compact_end
 * - 任务管理：todo_update
 * - Steering（运行中消息准入）：steer_guided | steer_queued | steer_rejected（准入回执）
 *              steer_drained（guide/队列项/显式发送项注入模型轮） | steer_delivery_changed（guide 降级排队）
 *              steer_discarded（未消费输入结算作废）
 * - 通知邮箱：notice_drained（后台任务完成通知被吸收进当前模型轮）
 * - 终态事件：done | error | cancelled（三选一，标记 run 结束）
 * - 心跳：heartbeat（服务端每 20s 发送，用于保活）
 */
export type EventType =
  | 'run'
  | 'thought_start'
  | 'thought_delta'
  | 'thought_end'
  | 'content_start'
  | 'content_delta'
  | 'content_end'
  | 'tool_use_start'
  | 'tool_use_end'
  | 'tool_confirm_request'
  | 'client_tool_use_start'
  | 'client_tool_use_end'
  | 'compact_start'
  | 'compact_end'
  | 'todo_update'
  | 'plan_view_update'
  | 'plan_step_event'
  | 'model_fallback'
  | 'steer_guided'
  | 'steer_queued'
  | 'steer_rejected'
  | 'steer_drained'
  | 'steer_delivery_changed'
  | 'steer_discarded'
  | 'notice_drained'
  | 'done'
  | 'error'
  | 'cancelled'
  | 'heartbeat';

/**
 * 服务端事件帧
 *
 * 服务端推送的每一条事件都遵循此结构。
 */
export interface ReactEvent {
  /** 事件类型（见 EventType） */
  type: EventType;
  /** 全局递增序号，客户端可用于检测消息丢失 */
  seq: number;
  /** 当前推理轮次 ID */
  runId?: string;
  /** 所属会话 ID */
  sessionId?: string;
  /** ReAct 步骤索引（从 0 开始），同一步骤内的 thought/content/tool 共享同一个 index */
  stepIndex?: number;
  /** 多 Agent 事件归属路径（如 main/ops-agent）；外层 run 省略该字段（按 main 渲染） */
  agentPath?: string;
  /** 事件载荷，按 type 不同对应不同的 Payload 类型 */
  payload?: Record<string, unknown>;
}

// ─── 各事件 Payload 类型 ────────────────────────────────────────────

/** 模型开始一次思考（无 payload 字段） */
export interface ThoughtStartPayload {}

/** 思考内容的流式增量 */
export interface ThoughtDeltaPayload {
  /** 增量文本片段，客户端应追加到当前 step 的 thoughts 字段 */
  contentDelta?: string;
}

/**
 * 思考结束
 *
 * 除完整思考文本外，还携带该步结束时的 token 统计与上下文窗口占用，
 * 供前端在思考阶段也实时刷新上下文/缓存指标。实时流与历史回放均会携带这些 token 字段。
 */
export interface ThoughtEndPayload {
  /** 完整的思考文本，客户端应用此值覆盖之前所有 delta 拼接的结果 */
  content?: string;
  /** 输入 token 数 */
  inputTokens?: number;
  /** 输出 token 数 */
  outputTokens?: number;
  /** 缓存命中 token 数（Prompt Caching） */
  cacheReadTokens?: number;
  /** 缓存创建 token 数（Prompt Caching） */
  cacheCreateTokens?: number;
  /** 当前会话已占用的上下文 token 数 */
  contextUsedTokens?: number;
  /** 模型上下文窗口总大小（token） */
  maxContextTokens?: number;
}

/** 模型开始生成最终回答（无 payload 字段） */
export interface ContentStartPayload {}

/** 回答内容的流式增量 */
export interface ContentDeltaPayload {
  /** 增量文本片段，客户端应追加到当前 step 的 content 字段 */
  contentDelta: string;
}

/**
 * 回答结束
 *
 * 除完整回答文本外，还携带该步结束时的 token 统计与上下文窗口占用，
 * 供前端在流式过程中实时刷新上下文/缓存指标。实时流与历史回放均会携带这些 token 字段。
 */
export interface ContentEndPayload {
  /** 完整的回答文本，客户端应用此值覆盖之前所有 delta 拼接的结果 */
  content?: string;
  /** 输入 token 数 */
  inputTokens?: number;
  /** 输出 token 数 */
  outputTokens?: number;
  /** 缓存命中 token 数（Prompt Caching） */
  cacheReadTokens?: number;
  /** 缓存创建 token 数（Prompt Caching） */
  cacheCreateTokens?: number;
  /** 当前会话已占用的上下文 token 数 */
  contextUsedTokens?: number;
  /** 模型上下文窗口总大小（token） */
  maxContextTokens?: number;
}

/**
 * 服务端/内置工具开始执行
 *
 * executedBy 区分执行方：
 * - 'server'   — 通过 HTTP 调外部 API 的 Business Tool
 * - 'internal' — 在服务端直接执行的 Meta Tool（如 list_tools、get_tool 等）
 */
export interface ToolUseStartPayload {
  /** 工具调用唯一标识 */
  toolUseId: string;
  /** 工具名称 */
  toolName: string;
  /** 工具展示描述，存在时优先用于 UI 展示 */
  description?: string;
  /** 模型传入的工具参数 */
  toolInput?: Record<string, unknown>;
  /** 执行方：server（HTTP 外部调用）或 internal（服务端内置 Meta Tool） */
  executedBy: 'server' | 'internal';
  /** 初始状态，通常为 'running'；ask_question 为 'waiting'（等待用户作答） */
  status: string;
}

/**
 * 前端工具调用通知
 *
 * 服务端通知客户端需要执行一个前端工具。
 * 客户端收到后应在本地执行工具逻辑，然后通过 client_tool_use_end 消息回填结果。
 */
/** tool_confirm_request 事件 payload（P2-3）：服务端工具执行前等待人工确认 */
export interface ReactToolConfirmRequestPayload {
  toolUseId: string;
  toolName: string;
  toolInput?: Record<string, unknown>;
  /** 生效模式：confirm / confirm_risky */
  mode: string;
  /** 触发原因（confirm_risky 时含命中的风险正则） */
  reason?: string;
}

/** Steering 准入回执（steer_guided / steer_queued / steer_rejected 事件共用） */
export interface ReactSteerReceiptPayload {
  /** guided / queued / rejected */
  kind: string;
  pendingInputId?: string;
  /** queued 时为当前队列长度 */
  queueLength?: number;
  /** queue/rejected 时的原因：run_not_steerable / soft_landing / attachments_unsupported */
  reason?: string;
}

/** steer_drained 事件 payload：guide/队列项/显式发送项已注入模型轮 */
export interface ReactSteerDrainedPayload {
  pendingInputId: string;
  messageId?: string;
}

/** steer_discarded / steer_delivery_changed 事件共用 payload */
export interface ReactSteerDiscardedPayload {
  count: number;
  /** turn_cancelled / turn_failed / run_finished / session_resumed / turn_ended */
  reason: string;
}

/** notice_drained 事件 payload：后台任务完成通知被吸收进当前模型轮；
 *  content 仅历史回放（session/events）携带（react_notice 消息的信封正文） */
export interface ReactNoticeDrainedPayload {
  messageId: string;
  count: number;
  content?: string;
}

export interface ClientToolUseStartPayload {
  /** 工具调用唯一标识，回填时需原样带回 */
  toolUseId: string;
  /** 工具名称，通常是服务端下发的可执行工具 ID */
  toolName: string;
  /** 模型传入的工具参数 */
  toolInput?: Record<string, unknown>;
  /** 工具展示描述 */
  description?: string;
  /** 服务端给出的提示信息，可辅助客户端决策（可选） */
  frontendHint?: string;
  /** 初始状态，通常为 'waiting' */
  status: string;
  /** Plan 内工具所属执行 ID；outer 工具为空。 */
  planExecutionId?: string;
  /** Plan 内工具所属 StepAttempt；outer 工具为空。 */
  stepAttemptId?: string;
}

/**
 * 工具执行完成（服务端/内置工具）
 *
 * 大结果会通过 resultRef 引用，客户端可通过 read_tool_result Meta Tool 分页读取。
 */
export interface ToolUseEndPayload {
  /** 对应的工具调用 ID */
  toolUseId: string;
  /** 工具执行结果的文本内容（可能是截断后的预览） */
  content?: string;
  /** 大结果的引用 ID，可通过 read_tool_result 分页读取完整内容 */
  resultRef?: string;
  /** 结果是否被截断 */
  truncated: boolean;
  /** 被省略的字符数（truncated=true 时有值，按 Unicode 字符/rune 计，与 read_tool_result 的 offset/limit 同坐标系） */
  omittedChars?: number;
  /** 执行是否出错 */
  isError: boolean;
  /** 执行方标识 */
  executedBy: string;
  /** 执行状态（success、error 或 cancelled） */
  status: string;
  /** 执行耗时（毫秒） */
  durationMs: number;
}

/** 上下文压缩开始 */
export interface CompactStartPayload {
  /** 压缩前的消息数量 */
  messageCount: number;
  /** 压缩前的预估大小（token 或字节） */
  estimatedSize: number;
}

/** 上下文压缩完成 */
export interface CompactEndPayload {
  /** 压缩前的消息数量 */
  beforeMessageCount: number;
  /** 压缩后的消息数量 */
  afterMessageCount: number;
  /** 压缩后的摘要文本 */
  summary: string;
}

/**
 * Todo 状态变更
 *
 * todoState 结构与后端 todo.go 保持一致：
 * `{ version: 1, items: TodoItem[], updatedAt: number }`
 */
export interface TodoUpdatePayload {
  /** merge=false 表示新的 todo 列表；merge=true 表示按 id 更新当前 todo 列表 */
  merge?: boolean;
  /** 简化结构：直接携带 todo 项 */
  todos?: TodoItem[];
  /** 完整的 todo 状态对象 */
  todoState?: Record<string, unknown>;
}

/** 心跳，服务端每 20s 发送一次，用于保活 */
export interface HeartbeatPayload {
  /** 服务端时间戳（毫秒） */
  ts: number;
}

/**
 * Run 正常结束
 *
 * 携带本次 run 的 token 消耗统计。
 */
export interface DonePayload {
  /** 输入 token 数 */
  inputTokens: number;
  /** 输出 token 数 */
  outputTokens: number;
  /** 缓存命中 token 数（Prompt Caching） */
  cacheReadTokens: number;
  /** 缓存创建 token 数（Prompt Caching） */
  cacheCreateTokens: number;
  /** 当前会话已占用的上下文 token 数 */
  contextUsedTokens: number;
  /** 模型上下文窗口总大小（token） */
  maxContextTokens: number;
  /** 结束原因（如 'end_turn'、'max_steps'），可选 */
  terminationReason?: string;
}

/**
 * 错误
 *
 * 出错时拿不到本轮的 input/cache token，但仍可携带上下文窗口的占用情况。
 */
export interface ErrorPayload {
  /** 错误码 */
  errNo: number;
  /** 错误描述 */
  errMsg: string;
  /** 当前会话已占用的上下文 token 数（可选） */
  contextUsedTokens?: number;
  /** 模型上下文窗口总大小（token，可选） */
  maxContextTokens?: number;
}

/**
 * 已取消
 *
 * 取消时拿不到本轮的 input/cache token，但仍可携带上下文窗口的占用情况。
 */
export interface CancelledPayload {
  /** 是否取消成功 */
  ok: boolean;
  /** 取消原因（如 'cancelled_by_user'） */
  reason?: string;
  /** 当前会话已占用的上下文 token 数（可选） */
  contextUsedTokens?: number;
  /** 模型上下文窗口总大小（token，可选） */
  maxContextTokens?: number;
}

// ─── SDK 内部状态类型 ──────────────────────────────────────────────

/** 单条 Todo 项 */
export interface TodoItem {
  /** 唯一标识 */
  id: string;
  /** 任务描述 */
  content: string;
  /** 任务状态 */
  status: 'pending' | 'in_progress' | 'completed' | 'cancelled';
  /** 创建时间戳（毫秒） */
  createdAt: number;
  /** 最后更新时间戳（毫秒） */
  updatedAt: number;
}

/** 累计 Token 消耗统计（跨多次 run 累加） */
export interface UsageStats {
  /** 累计输入 token 数 */
  totalInputTokens: number;
  /** 累计输出 token 数 */
  totalOutputTokens: number;
  /** 累计缓存命中 token 数 */
  totalCacheReadTokens: number;
  /** 累计缓存创建 token 数 */
  totalCacheCreateTokens: number;
  /** 累计推理轮次数 */
  runCount: number;
}

/**
 * 最近一轮(run)的 token 统计快照
 *
 * 与 UsageStats（跨多轮累加）不同，这里只保存最近一轮的瞬时值，
 * 用于在输入框展示上下文窗口占用和缓存命中占比。
 * error/cancelled 终态拿不到 input/cache token，此时仅更新上下文窗口字段，
 * 其余字段沿用上一轮成功 run 的值。
 */
export interface RunStats {
  /** 本轮输入 token 数 */
  inputTokens: number;
  /** 本轮输出 token 数 */
  outputTokens: number;
  /** 本轮缓存命中 token 数 */
  cacheReadTokens: number;
  /** 本轮缓存创建 token 数 */
  cacheCreateTokens: number;
  /** 当前会话已占用的上下文 token 数 */
  contextUsedTokens: number;
  /** 模型上下文窗口总大小（token） */
  maxContextTokens: number;
}

/** 上下文压缩结果 */
export interface CompactState {
  /** 压缩前的消息数量 */
  beforeMessageCount: number;
  /** 压缩后的消息数量 */
  afterMessageCount: number;
  /** 压缩后的摘要文本 */
  summary: string;
}

// ─── HTTP 通用响应 ─────────────────────────────────────────────────

/**
 * 后端统一 HTTP 响应格式
 *
 * errNo=0 表示成功，data 为业务数据。
 * errNo!=0 表示失败，errMsg 为错误描述。
 */
export interface ApiResponse<T = unknown> {
  /** 错误码，0 表示成功 */
  errNo: number;
  /** 错误描述 */
  errMsg: string;
  /** 链路追踪 ID（可选） */
  traceId?: string;
  /** 业务数据 */
  data: T;
}
