/**
 * MessageList - 消息列表组件
 *
 * 渲染所有消息步骤，并在新消息到达时自动滚动到底部。
 * 使用 For 组件进行高效列表渲染。
 */

import { For, Index, Show, createEffect, createMemo, createSignal, onCleanup, type JSX } from "solid-js";
import IconGrommetIconsLinkDown from "~icons/grommet-icons/link-down";
import IconMdiRobot from "~icons/mdi/robot";
import type { AskQuestionAnswerContent } from "../../protocol/types";
import type { PlanRuntimeState, RunFeedbackPayload, RunFeedbackState, Step, ToolCallState } from "../../runtime/types";
import type { ClientTool } from "../../tools/types";
import type { NextButtonAction } from "../events";
import { InputPartsView } from "../editor/InputPartsView";
import type { AgentQuickInsertItem } from "../editor/types";
import { AssistantTurnBody } from "./AssistantTurnBody";
import { getPlanExecutionId, isPlanExecutionToolCall } from "./turn-segments";
import type { CodeBlockCopyData, CodeBlockSelectionCopyData } from "./CodeBlock";
import { PhaseGapIndicator } from "./PhaseGapIndicator";
import { PlanRuntimeCard } from "./PlanRuntimeCard";
import { SmartScroll, type SmartScrollHandle } from "./SmartScroll";
import { TurnFeedbackBar } from "./TurnFeedbackBar";
import { getActivityGapSignature, shouldShowDelayedActivityIndicator } from "./message-activity";

export interface MessageListProps {
  /** 消息步骤数组 */
  steps: Step[];
  /** 当前会话 ID，用于反馈区域的 Copy ID */
  sessionId?: string | null;
  /** 当前调用方标识，与 sessionId 一起用于反馈区域的 Copy ID */
  callerKey?: string;
  /** 当前会话路由参数，与 sessionId 一起用于反馈区域的 Copy ID */
  routeValues?: string[];
  /** 当前是否正在运行 */
  isRunning: boolean;
  /** 当前是否正在压缩上下文（status==='compacting'） */
  isCompacting?: boolean;
  /** 当前是否正在掉线恢复（status==='recovering'） */
  isRecovering?: boolean;
  /** 自定义开始对话空态 */
  renderStartBlock?: () => JSX.Element;
  /** 用于把历史文本反解析为快捷节点 */
  quickInsertItems?: AgentQuickInsertItem[];
  /** 点击快捷下一步 */
  onNextButtonClick?: (action: NextButtonAction) => void;
  /** 确认执行计划 */
  onPlanConfirm?: (planId: string) => void;
  /** 根据工具名查找已注册的客户端工具定义 */
  resolveTool?: (toolName: string, frontendHint?: string) => ClientTool | undefined;
  /** 提交 ask_question 的用户答案（tool_use_answer 回填） */
  onAskQuestionSubmit?: (toolUseId: string, content: AskQuestionAnswerContent) => void;
  onToolConfirmSubmit?: (toolUseId: string, approved: boolean) => void;

  /** 当前活跃的等待作答 ask_question，消息列表中不再重复渲染 */
  activeAskQuestion?: ToolCallState;
  /** 当前会话 Plan 的实时公开状态和步骤详情。 */
  plans?: Record<string, PlanRuntimeState>;
  onPlanResume?: (planExecutionId: string, waitRequestId: string, response: Record<string, unknown>) => void;
  onPlanRetry?: (planExecutionId: string, stepId: string) => void;
  onPlanSkip?: (planExecutionId: string, stepId: string) => void;
  onPlanCancel?: (planExecutionId: string) => void;
  onLoadPlan?: (planExecutionId: string) => void | Promise<void>;
  onLoadPlanAttempt?: (planExecutionId: string, stepAttemptId: string) => void | Promise<void>;
  /** 各轮次(runId)的反馈状态，用于回显点赞/点踩/问题反馈 */
  feedbackByRunId?: Record<string, RunFeedbackState>;
  /** 提交某一轮反馈；不传则不渲染反馈条 */
  onFeedback?: (runId: string, payload: RunFeedbackPayload) => void | Promise<void>;
  /** 复制助手代码块 */
  onCodeCopy?: (step: Step, data: CodeBlockCopyData) => void;
  /** 复制助手代码选区 */
  onCodeSelectionCopy?: (step: Step, data: CodeBlockSelectionCopyData) => void;
  /** 打开问题反馈表单 */
  onProblemFeedbackOpen?: (runId: string) => void;
}

function DefaultStartBlock() {
  return (
    <div class="agent-ui-message-empty-default">
      <div class="agent-ui-message-empty-hero">
        <span class="agent-ui-message-empty-logo">
          <IconMdiRobot width="30" height="30" />
        </span>
        <div class="agent-ui-message-empty-title">
          你好，我是 <span>Agent Web SDK</span>
        </div>
        <div class="agent-ui-message-empty-subtitle">
          描述你的需求，我会尽力帮你完成分析、生成内容或整理思路。
        </div>
      </div>
      <div class="agent-ui-message-empty-tips">
        <div class="agent-ui-message-empty-tips-title">开始对话</div>
        <div>1、尽量说明背景、目标和期望结果。</div>
        <div>2、如果有上下文信息，可以直接贴在输入框里。</div>
        <div>3、需要继续追问时，可以基于上一轮结果补充要求。</div>
      </div>
    </div>
  );
}

type SectionItem =
  | { kind: 'step'; step: Step }
  | { kind: 'compact'; step: Step }
  | { kind: 'notice'; step: Step };

interface MessageSection {
  id: string;
  runId: string;
  userStep?: Step;
  /** 助手回复与压缩分隔标记，按到达顺序排列 */
  items: SectionItem[];
}

function getStepIdentity(step: Step, index: number): string {
  return `${step.runId || 'run'}:${step.index}:${step.role}:${index}`;
}

function sectionHasAssistant(section: MessageSection): boolean {
  return section.items.some((item) => item.kind === 'step');
}

function getSectionAssistantContent(section: MessageSection): string {
  return section.items
    .filter((item) => item.kind === 'step' && item.step.content?.trim())
    .map((item) => item.step.content)
    .join('\n\n');
}

function buildMessageSections(steps: Step[]): MessageSection[] {
  const sections: MessageSection[] = [];
  let currentSection: MessageSection | undefined;

  const ensureSection = (step: Step, index: number) => {
    if (!currentSection) {
      currentSection = {
        id: getStepIdentity(step, index),
        runId: step.runId || '',
        items: [],
      };
      sections.push(currentSection);
    }
    if (!currentSection.runId && step.runId) {
      currentSection.runId = step.runId;
    }
    return currentSection;
  };

  steps.forEach((step, index) => {
    if (step.role === 'user') {
      currentSection = {
        id: getStepIdentity(step, index),
        runId: step.runId || '',
        userStep: step,
        items: [],
      };
      sections.push(currentSection);
      return;
    }

    const section = ensureSection(step, index);
    section.items.push({ kind: step.role === 'compact' ? 'compact' : step.role === 'notice' ? 'notice' : 'step', step });
  });

  return sections;
}

function nativePlanToolCall(plan: PlanRuntimeState): ToolCallState {
  let status: ToolCallState['status'] = 'running';
  switch (plan.view.status) {
    case 'WAIT_USER_INPUT':
    case 'WAIT_USER_ACTION':
    case 'WAIT_EXTERNAL_TASK':
      status = 'waiting';
      break;
    case 'SUCCEEDED':
      status = 'done';
      break;
    case 'FAILED':
      status = 'error';
      break;
    case 'CANCELLED':
      status = 'cancelled';
      break;
    default:
      status = 'running';
  }
  return {
    toolUseId: `plan_runtime:${plan.planExecutionId}`,
    toolName: 'plan_runtime',
    description: plan.view.summary || 'Plan Runtime',
    input: {},
    status,
    executedBy: 'internal',
    planExecutionId: plan.planExecutionId,
  };
}

function UserMessage(props: {
  step: Step;
  quickInsertItems?: AgentQuickInsertItem[];
}) {
  return (
    <div class="agent-ui-message-user-card">
      <div class="agent-ui-message-user-content">
        <InputPartsView
          parts={props.step.displayParts}
          fallback={props.step.content}
          quickInsertItems={props.quickInsertItems}
        />
      </div>
    </div>
  );
}

const ACTIVITY_GAP_DELAY_MS = 300;

export function MessageList(props: MessageListProps) {
  let scrollHandle: SmartScrollHandle | undefined;
  let activityGapTimer: ReturnType<typeof setTimeout> | undefined;
  const [isAtBottom, setIsAtBottom] = createSignal(true);
  const [showActivityGapIndicator, setShowActivityGapIndicator] = createSignal(false);

  const sections = createMemo(() => buildMessageSections(props.steps));
  const latestPlanToolUseIdByExecution = createMemo<ReadonlyMap<string, string>>(() => {
    const latest = new Map<string, string>();
    for (const step of props.steps) {
      for (const toolCall of step.toolCalls) {
        if (!isPlanExecutionToolCall(toolCall)) continue;
        const planExecutionId = getPlanExecutionId(toolCall);
        if (planExecutionId) latest.set(planExecutionId, toolCall.toolUseId);
      }
    }
    return latest;
  });

  const nativePlansForRun = (runId: string): PlanRuntimeState[] => (
    Object.values(props.plans ?? {})
      .filter((plan) => (
        !!runId
        && plan.outerRunId === runId
        && !latestPlanToolUseIdByExecution().has(plan.planExecutionId)
      ))
      .sort((left, right) => left.planExecutionId.localeCompare(right.planExecutionId))
  );

  const userMessageKey = createMemo(() => props.steps
    .filter((step) => step.role === 'user')
    .map((step) => [
      step.runId,
      step.index,
      step.content?.length ?? 0,
    ].join(':'))
    .join('|'));

  const followKey = createMemo(() => props.steps.map((step) => [
    step.runId,
    step.index,
    step.role,
    step.content?.length ?? 0,
    step.thoughts?.length ?? 0,
    step.toolCalls.length,
  ].join(':')).join('|'));

  const activityGapSignature = createMemo(() => getActivityGapSignature(
    props.steps,
    props.isRunning,
    props.isCompacting ?? false,
  ));

  const handleScrollToBottom = () => {
    scrollHandle?.resumeFollow({ scroll: true });
    setIsAtBottom(true);
  };

  let lastUserMessageKey = userMessageKey();
  createEffect(() => {
    const nextUserMessageKey = userMessageKey();
    if (nextUserMessageKey && nextUserMessageKey !== lastUserMessageKey) {
      scrollHandle?.resumeFollow({ scroll: true });
      setIsAtBottom(true);
    }
    lastUserMessageKey = nextUserMessageKey;
  });

  createEffect(() => {
    const shouldShow = shouldShowDelayedActivityIndicator(
      props.steps,
      props.isRunning,
      props.isCompacting ?? false,
    );
    const signature = activityGapSignature();

    if (activityGapTimer) {
      clearTimeout(activityGapTimer);
      activityGapTimer = undefined;
    }

    if (!shouldShow) {
      setShowActivityGapIndicator(false);
      return;
    }

    setShowActivityGapIndicator(false);
    activityGapTimer = setTimeout(() => {
      const stillShouldShow = shouldShowDelayedActivityIndicator(
        props.steps,
        props.isRunning,
        props.isCompacting ?? false,
      );
      const stillSameGap = signature === activityGapSignature();
      setShowActivityGapIndicator(stillShouldShow && stillSameGap);
      activityGapTimer = undefined;
    }, ACTIVITY_GAP_DELAY_MS);
  });

  onCleanup(() => {
    if (activityGapTimer) {
      clearTimeout(activityGapTimer);
    }
  });

  return (
    <div class="agent-ui-message-list-shell">
      <SmartScroll
        ref={(handle) => {
          scrollHandle = handle;
        }}
        class="agent-ui-message-list"
        follow="bottom"
        followKey={followKey()}
        active={props.isRunning}
        bottomThreshold={16}
        onStickChange={setIsAtBottom}
      >
        <Index each={sections()}>
          {(section, index) => {
            const isCompletedTurn = () =>
              sectionHasAssistant(section())
              && (index < sections().length - 1 || !props.isRunning);
            return (
              <section class="agent-ui-message-section">
                <Show when={section().userStep}>
                  {(userStep) => (
                    <UserMessage
                      step={userStep()}
                      quickInsertItems={props.quickInsertItems}
                    />
                  )}
                </Show>

                <AssistantTurnBody
                  items={section().items}
                  isActiveTurn={index === sections().length - 1}
                  isRunning={props.isRunning}
                  onNextButtonClick={props.onNextButtonClick}
                  onPlanConfirm={props.onPlanConfirm}
                  resolveTool={props.resolveTool}
                  onAskQuestionSubmit={props.onAskQuestionSubmit}
                  onToolConfirmSubmit={props.onToolConfirmSubmit}
                  activeAskQuestion={props.activeAskQuestion}
                  plans={props.plans}
                  latestPlanToolUseIdByExecution={latestPlanToolUseIdByExecution()}
                  onPlanResume={props.onPlanResume}
                  onPlanRetry={props.onPlanRetry}
                  onPlanSkip={props.onPlanSkip}
                  onPlanCancel={props.onPlanCancel}
                  onLoadPlan={props.onLoadPlan}
                  onLoadPlanAttempt={props.onLoadPlanAttempt}
                  onCodeCopy={props.onCodeCopy}
                  onCodeSelectionCopy={props.onCodeSelectionCopy}
                />

                <For each={nativePlansForRun(section().runId)}>
                  {(plan) => (
                    <div class="agent-ui-message-assistant agent-ui-message-plan">
                      <div class="agent-ui-message-body">
                        <PlanRuntimeCard
                          toolCall={nativePlanToolCall(plan)}
                          plan={plan}
                          useLiveView
                          disabled={props.isRunning}
                          resolveTool={props.resolveTool}
                          onResume={props.onPlanResume}
                          onRetry={props.onPlanRetry}
                          onSkip={props.onPlanSkip}
                          onCancel={props.onPlanCancel}
                          onLoadPlan={props.onLoadPlan}
                          onLoadAttempt={props.onLoadPlanAttempt}
                        />
                      </div>
                    </div>
                  )}
                </For>

                <Show when={props.onFeedback && section().runId && isCompletedTurn()}>
                  <TurnFeedbackBar
                    runId={section().runId}
                    sessionId={props.sessionId}
                    callerKey={props.callerKey}
                    routeValues={props.routeValues}
                    state={props.feedbackByRunId?.[section().runId]}
                    copyContent={getSectionAssistantContent(section())}
                    onSubmit={props.onFeedback!}
                    onProblemFeedbackOpen={props.onProblemFeedbackOpen}
                  />
                </Show>
              </section>
            );
          }}
        </Index>

        {/* 空状态 */}
        <Show when={props.steps.length === 0 && !props.isRunning}>
          <div class="agent-ui-message-empty">
            {props.renderStartBlock ? props.renderStartBlock() : <DefaultStartBlock />}
          </div>
        </Show>

        {/* 加载指示器 */}
        <Show when={props.isRunning && !props.isCompacting && !props.isRecovering && props.steps.length === 0}>
          <div class="agent-ui-message-loading">
            <div class="agent-ui-loading-spinner"></div>
            <div class="agent-ui-loading-text">正在思考...</div>
          </div>
        </Show>

        {/* 上下文压缩进行中指示器：压缩完成后由 compact 分隔条取代 */}
        <Show when={props.isCompacting}>
          <PhaseGapIndicator text="正在压缩上下文..." />
        </Show>

        {/* 掉线恢复指示器：断线重连并尝试自动续跑期间显示，成功续跑或恢复失败后消失 */}
        <Show when={props.isRecovering}>
          <PhaseGapIndicator text="正在恢复连接..." />
        </Show>

        {/* 阶段空档提示：首包未到或前一阶段结束后，300ms 内下一阶段未到时显示 */}
        <Show when={!props.isCompacting && !props.isRecovering && showActivityGapIndicator()}>
          <PhaseGapIndicator text="准备下一步..." />
        </Show>
      </SmartScroll>

      <Show when={props.steps.length > 0 && !isAtBottom()}>
        <button type="button" class="agent-ui-scroll-to-bottom" onClick={handleScrollToBottom} aria-label="回到底部">
          <IconGrommetIconsLinkDown width="18" height="18" />
        </button>
      </Show>
    </div>
  );
}
