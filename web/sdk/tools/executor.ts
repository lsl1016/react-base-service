/**
 * 客户端工具执行器
 *
 * 桥接后端 client_tool_use_start 事件和客户端工具的实际执行。
 *
 * 工作流程：
 * 1. AgentClient 收到 client_tool_use_start 事件后调用 handleClientToolUseStart
 * 2. 在注册表中按 toolName、frontendHint 查找工具
 * 3. 调用 tool.execute(input, context) 执行工具逻辑
 * 4. 将结果组装为 client_tool_use_end 消息，通过 sendFn 回填给服务端
 * 5. 服务端收到回填后继续 ReAct 推理循环
 *
 * 异常处理：
 * - 工具未注册 → 回填 isError=true + "Tool not registered: xxx"
 * - execute 抛异常 → 回填 isError=true + 异常消息
 * - execute 返回 isError=true → 原样回填
 *
 * 并发：多个 client_tool_use_start 可以并行执行，
 * activeExecutions Map 跟踪所有进行中的执行。
 */
import type { WsMessage, ClientToolUseStartPayload, ClientToolOutput } from '../protocol/types';
import type { ClientToolRegistry } from './registry';

/** 发送 WsMessage 的函数签名，由 AgentClient 注入 */
type SendFn = (msg: WsMessage) => void;

export class ClientToolExecutor {
  private registry: ClientToolRegistry;
  private sendFn: SendFn;
  /** 正在执行中的工具调用，key=toolUseId */
  private activeExecutions = new Map<string, Promise<void>>();

  constructor(registry: ClientToolRegistry, sendFn: SendFn) {
    this.registry = registry;
    this.sendFn = sendFn;
  }

  /**
   * 处理 client_tool_use_start 事件
   *
   * 查找工具 → 执行 → 回填结果。
   * 返回的 Promise 在工具执行完毕后 resolve。
   */
  async handleClientToolUseStart(payload: ClientToolUseStartPayload): Promise<void> {
    const { toolUseId, toolName, toolInput, frontendHint, description, planExecutionId, stepAttemptId } = payload;

    const execution = this.executeTool(
      {
        toolUseId,
        toolName,
        frontendHint,
        description,
        planExecutionId,
        stepAttemptId,
      },
      toolInput ?? {},
    ).finally(() => {
      this.activeExecutions.delete(toolUseId);
    });

    this.activeExecutions.set(toolUseId, execution);
    await execution;
  }

  private async executeTool(
    context: {
      toolUseId: string;
      toolName: string;
      frontendHint?: string;
      description?: string;
      planExecutionId?: string;
      stepAttemptId?: string;
    },
    input: Record<string, unknown>,
  ): Promise<void> {
    const tool = this.registry.getFirst([
      context.toolName,
      context.frontendHint,
    ]);
    const toolUseId = context.toolUseId;

    if (!tool) {
      this.sendResult(context, {
        toolUseId,
        content: `Tool not registered: ${context.toolName}`,
        isError: true,
      });
      return;
    }

    try {
      const result = await tool.execute(input, context);
      this.sendResult(context, {
        toolUseId,
        content: result.content,
        meta: result.meta,
        isError: result.isError,
      });
    } catch (err) {
      this.sendResult(context, {
        toolUseId,
        content: err instanceof Error ? err.message : String(err),
        isError: true,
      });
    }
  }

  /** 组装 client_tool_use_end 消息并发送 */
  private sendResult(
    context: { toolUseId: string; planExecutionId?: string; stepAttemptId?: string },
    output: ClientToolOutput,
  ): void {
    this.sendFn({
      type: 'client_tool_use_end',
      runId: undefined,
      payload: {
        toolOutputs: [output],
        planExecutionId: context.planExecutionId,
        stepAttemptId: context.stepAttemptId,
      } as Record<string, unknown>,
    });
  }

  get isBusy(): boolean {
    return this.activeExecutions.size > 0;
  }

  get activeCount(): number {
    return this.activeExecutions.size;
  }
}
