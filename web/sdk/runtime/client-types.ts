/**
 * AgentClient 对外类型定义
 *
 * 独立成文件是为了让 SessionRuntime（每会话运行时）引用这些类型时
 * 不与 AgentClient 形成模块循环依赖；agent-client.ts 会 re-export 保持
 * 外部导入路径不变。
 */
import type { ExecutionMode, LlmContext, ReactAttachmentRef, ReactReasoningOptions, UserInputOrigin } from '../protocol/types';
import type { AgentInputPart } from './types';

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
  /** 本轮思考程度三态；默认 auto（与历史行为一致）。 */
  reasoning?: ReactReasoningOptions;
  /** 本轮随用户消息发送的已上传附件 */
  attachments?: ReactAttachmentRef[];
  /** 本轮用户输入来源 */
  inputOrigin?: UserInputOrigin;
  /** 仅用于前端展示的用户输入结构，不发送给后端 */
  displayParts?: AgentInputPart[];
}
