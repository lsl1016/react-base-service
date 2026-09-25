import type { Step, ToolCallState } from '../../runtime/types';
import { readPlanToolInput, shouldRenderPlanTool } from './plan-tool';

export type SectionItem =
  | { kind: 'step'; step: Step }
  | { kind: 'compact'; step: Step }
  | { kind: 'notice'; step: Step };

export type WorkPart =
  | { kind: 'thought'; step: Step }
  | { kind: 'tools'; step: Step; toolUseIds?: readonly string[] }
  | { kind: 'compact'; step: Step }
  | { kind: 'notice'; step: Step };

export type TurnSegment =
  | { kind: 'work'; id: string; parts: WorkPart[]; collapsed: boolean }
  | { kind: 'content'; id: string; step: Step }
  | { kind: 'plan'; id: string; step: Step; toolUseId: string }
  | { kind: 'root_tool'; id: string; step: Step; toolUseId: string }
  | { kind: 'ask_question'; id: string; step: Step; toolUseId: string };

/** content / root tool / ask_question 等根级块之后的 Work 应视为已结束 */
function segmentClosesPrecedingWork(segment: TurnSegment): boolean {
  return segment.kind === 'content'
    || segment.kind === 'plan'
    || segment.kind === 'root_tool'
    || segment.kind === 'ask_question';
}

/** content 阶段已开始（content_start 或已有正文），Work 在此刻结束并折叠 */
export function stepHasContentPhase(step: Step): boolean {
  return step.contentStarted || step.content.length > 0;
}

export interface BuildTurnSegmentsOptions {
  isActiveTurn: boolean;
  isRunning: boolean;
  /** 返回 true 时将普通工具移出 WorkBlock，作为根级工具段展示。 */
  isRootTool?: (toolCall: ToolCallState) => boolean;
}

const CLIENT_TOOL_MISSING_RESULT_MESSAGES = new Set([
  '用户取消了本次运行，客户端工具未返回结果。',
  '连接断开，客户端工具未返回结果。',
]);

function hasRenderableToolResult(toolCall: ToolCallState): boolean {
  if (toolCall.result === undefined) return false;
  return !(
    toolCall.executedBy === 'client'
    && toolCall.isError
    && CLIENT_TOOL_MISSING_RESULT_MESSAGES.has(toolCall.result.trim())
  );
}

export function isPlanExecutionToolCall(toolCall: ToolCallState): boolean {
  return toolCall.toolName === 'start_template_plan' || toolCall.toolName === 'resume_template_plan';
}

export function isPlanToolCall(toolCall: ToolCallState): boolean {
  if (isPlanExecutionToolCall(toolCall)) return true;
  const name = toolCall.toolName.toLowerCase().replace(/[\s_-]/g, '');
  if (name === 'todowrite' || name === 'updatetodos' || name === 'createplan') return true;

  const input = toolCall.input;
  return ((typeof input.merge === 'boolean' || Array.isArray(input.items))
      && (Array.isArray(input.todos) || Array.isArray(input.items)))
    || (typeof input.todoState === 'object'
      && input.todoState !== null
      && Array.isArray((input.todoState as Record<string, unknown>).items));
}

/** 单个 WorkPart 内要展示的工具（按 afterContent 分段后的子集） */
export function getWorkPartToolCalls(step: Step, toolUseIds?: readonly string[]): ToolCallState[] {
  if (!toolUseIds || toolUseIds.length === 0) {
    return getWorkToolCalls(step);
  }
  const idSet = new Set(toolUseIds);
  return step.toolCalls.filter((toolCall) => idSet.has(toolCall.toolUseId));
}

export function getPlanExecutionId(toolCall: ToolCallState): string | undefined {
  if (toolCall.planExecutionId) return toolCall.planExecutionId;
  const inputPlanExecutionId = toolCall.input.plan_execution_id;
  return typeof inputPlanExecutionId === 'string' && inputPlanExecutionId.trim()
    ? inputPlanExecutionId.trim()
    : undefined;
}

function isAskQuestionToolCall(toolCall: ToolCallState): boolean {
  return toolCall.toolName === 'ask_question';
}

function isDisplayFilesToolCall(toolCall: ToolCallState): boolean {
	return toolCall.toolName === 'displayFiles'
		|| Array.isArray(toolCall.meta?.artifacts) && toolCall.meta.artifacts.length > 0;
}

export function getWorkToolCalls(step: Step): ToolCallState[] {
  return step.toolCalls.filter((toolCall) => {
    if (isAskQuestionToolCall(toolCall)) return false;
    if (!isPlanToolCall(toolCall)) return true;
    const input = readPlanToolInput(toolCall);
    return input.kind === 'todo' && input.merge && shouldRenderPlanTool(toolCall);
  });
}

export function getAskQuestionToolCall(step: Step, toolUseId: string): ToolCallState | undefined {
  return step.toolCalls.find((toolCall) => toolCall.toolUseId === toolUseId);
}

export function getPlanToolCall(step: Step, toolUseId: string): ToolCallState | undefined {
  return step.toolCalls.find((toolCall) => toolCall.toolUseId === toolUseId && isPlanToolCall(toolCall));
}

function pushToolsWorkPart(workParts: WorkPart[], step: Step, toolCalls: ToolCallState[]) {
  if (toolCalls.length === 0) return;
  workParts.push({
    kind: 'tools',
    step,
    toolUseIds: toolCalls.map((toolCall) => toolCall.toolUseId),
  });
}

export function summarizeWorkParts(parts: WorkPart[]): { thoughtCount: number; toolRunCount: number } {
  let thoughtCount = 0;
  let toolRunCount = 0;
  for (const part of parts) {
    if (part.kind === 'thought') {
      thoughtCount += 1;
    }
    if (part.kind === 'tools') {
      toolRunCount += getWorkPartToolCalls(part.step, part.toolUseIds).length;
    }
  }
  return { thoughtCount, toolRunCount };
}

/** 折叠态 Work 标题：已思考 / 已运行 / 组合 */
export function formatWorkSummary(thoughtCount: number, toolRunCount: number): string {
  const hasThought = thoughtCount > 0;
  const hasTool = toolRunCount > 0;
  if (hasThought && hasTool) {
    return `已思考${thoughtCount}次，运行${toolRunCount}次`;
  }
  if (hasThought) {
    return `已思考${thoughtCount}次`;
  }
  if (hasTool) {
    return `已运行${toolRunCount}次`;
  }
  return '已工作';
}

/**
 * 按消息到达顺序将一轮 assistant 项切分为 Work / Content 同级块。
 * content 出现时其前的连续 thought/tool 归入已折叠的 Work；Content 不折叠。
 */
export function buildTurnSegments(
  items: SectionItem[],
  options: BuildTurnSegmentsOptions,
): TurnSegment[] {
  const segments: TurnSegment[] = [];
  let workParts: WorkPart[] = [];
  let workIndex = 0;
  const showPendingToolCalls = options.isActiveTurn && options.isRunning;
  const latestPlanToolUseIdByExecution = new Map<string, string>();

  for (const item of items) {
    if (item.kind !== 'step') continue;
    for (const toolCall of item.step.toolCalls) {
      if (!isPlanExecutionToolCall(toolCall)) continue;
      const planExecutionId = getPlanExecutionId(toolCall);
      if (planExecutionId) {
        latestPlanToolUseIdByExecution.set(planExecutionId, toolCall.toolUseId);
      }
    }
  }

  const pushWork = (collapsed: boolean) => {
    if (workParts.length === 0) return;
    segments.push({
      kind: 'work',
      id: `work-${workIndex += 1}`,
      parts: workParts,
      collapsed,
    });
    workParts = [];
  };

  const pushRootTool = (step: Step, toolCall: ToolCallState) => {
    pushWork(true);
    if (isPlanToolCall(toolCall)) {
      segments.push({
        kind: 'plan',
        id: `plan-${step.runId}-${step.index}-${toolCall.toolUseId}`,
        step,
        toolUseId: toolCall.toolUseId,
      });
      return;
    }
    if (isAskQuestionToolCall(toolCall)) {
      segments.push({
        kind: 'ask_question',
        id: `ask-${step.runId}-${step.index}-${toolCall.toolUseId}`,
        step,
        toolUseId: toolCall.toolUseId,
      });
      return;
    }
    segments.push({
      kind: 'root_tool',
      id: `root-tool-${step.runId}-${step.index}-${toolCall.toolUseId}`,
      step,
      toolUseId: toolCall.toolUseId,
    });
  };

  const pushOrderedTools = (step: Step, toolCalls: ToolCallState[]) => {
    let workTools: ToolCallState[] = [];
    const flushWorkTools = () => {
      pushToolsWorkPart(workParts, step, workTools);
      workTools = [];
    };

    for (const toolCall of toolCalls) {
      if (isPlanToolCall(toolCall)) {
        if (isPlanExecutionToolCall(toolCall)) {
          const planExecutionId = getPlanExecutionId(toolCall);
          const latestToolUseId = planExecutionId
            ? latestPlanToolUseIdByExecution.get(planExecutionId)
            : undefined;
          if (latestToolUseId && latestToolUseId !== toolCall.toolUseId) {
            workTools.push(toolCall);
            continue;
          }
          flushWorkTools();
          pushRootTool(step, toolCall);
          continue;
        }
        if (!shouldRenderPlanTool(toolCall)) continue;
        const planInput = readPlanToolInput(toolCall);
        if (planInput.kind === 'todo' && planInput.merge) {
          workTools.push(toolCall);
          continue;
        }
        flushWorkTools();
        pushRootTool(step, toolCall);
      } else if (isAskQuestionToolCall(toolCall)) {
        flushWorkTools();
        pushRootTool(step, toolCall);
      } else if (isDisplayFilesToolCall(toolCall)) {
        flushWorkTools();
        pushRootTool(step, toolCall);
      } else if (options.isRootTool?.(toolCall)) {
        flushWorkTools();
        pushRootTool(step, toolCall);
      } else {
        workTools.push(toolCall);
      }
    }
    flushWorkTools();
  };

  for (const item of items) {
    if (item.kind === 'compact') {
      workParts.push({ kind: 'compact', step: item.step });
      continue;
    }
    if (item.kind === 'notice') {
      workParts.push({ kind: 'notice', step: item.step });
      continue;
    }

    const step = item.step;
    if (step.role !== 'assistant') continue;

    if (step.thoughts.trim()) {
      workParts.push({ kind: 'thought', step });
    }

    // 回放时隐藏只有 start 或由断线/取消补写的无真实结果工具，
    // 但直播中的 running / waiting 工具仍需正常展示。
    const visibleToolCalls = showPendingToolCalls
      ? step.toolCalls
      : step.toolCalls.filter(hasRenderableToolResult);
    const beforeContentTools = visibleToolCalls.filter((toolCall) => !toolCall.afterContent);
    const afterContentTools = visibleToolCalls.filter((toolCall) => toolCall.afterContent);
    pushOrderedTools(step, beforeContentTools);

    const hasContent = stepHasContentPhase(step);

    if (hasContent) {
      pushWork(true);
      segments.push({
        kind: 'content',
        id: `content-${step.runId}-${step.index}`,
        step,
      });
      pushOrderedTools(step, afterContentTools);
      continue;
    }

    pushOrderedTools(step, afterContentTools);
  }

  if (workParts.length > 0) {
    const liveExpanded = options.isActiveTurn && options.isRunning;
    pushWork(!liveExpanded);
  }

  // 仅「后面还有 Content / ask_question」的 Work 视为已结束并折叠；末尾 Work 在下一阶段前仍可直播展开
  for (let index = 0; index < segments.length; index += 1) {
    const segment = segments[index];
    if (segment.kind !== 'work') continue;
    const hasRootPaneAfter = segments.slice(index + 1).some(segmentClosesPrecedingWork);
    if (hasRootPaneAfter) {
      segment.collapsed = true;
    }
  }

  return segments;
}

/** 仅末尾且无后续 Content/Work 时，Work 处于直播中展开态 */
export function isLiveWorkSegment(
  segments: TurnSegment[],
  segmentIndex: number,
  options: BuildTurnSegmentsOptions,
): boolean {
  if (!options.isActiveTurn || !options.isRunning) {
    return false;
  }
  const segment = segments[segmentIndex];
  if (!segment || segment.kind !== 'work') {
    return false;
  }
  if (segment.collapsed) {
    return false;
  }
  if (segmentIndex !== segments.length - 1) {
    return false;
  }
  // 后面若还有 Content / ask_question 根级块，说明尚未进入该 Work 的直播窗口
  const hasRootPaneAfter = segments.slice(segmentIndex + 1).some(segmentClosesPrecedingWork);
  return !hasRootPaneAfter;
}

export function segmentListHasVisibleWork(segments: TurnSegment[]): boolean {
  return segments.some((segment) => segment.kind === 'work' && segment.parts.length > 0);
}
