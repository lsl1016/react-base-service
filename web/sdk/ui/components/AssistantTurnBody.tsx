/**
 * 一轮助手回复：Work / Content / root tool / ask_question 同级分段渲染
 */

import { Index, Show, createMemo } from 'solid-js';
import type { AskQuestionAnswerContent } from '../../protocol/types';
import type { PlanRuntimeState, Step, ToolCallState } from '../../runtime/types';
import type { ClientTool } from '../../tools/types';
import type { NextButtonAction } from '../events';
import { AskQuestionCard } from './AskQuestionCard';
import { ContentBlock } from './ContentBlock';
import type { CodeBlockCopyData, CodeBlockSelectionCopyData } from './CodeBlock';
import { PlanBlock } from './PlanBlock';
import { PlanRuntimeCard } from './PlanRuntimeCard';
import { ToolCallView } from './ToolCallView';
import { WorkBlock } from './WorkBlock';
import {
  buildTurnSegments,
  getAskQuestionToolCall,
  getPlanExecutionId,
  getPlanToolCall,
  getWorkPartToolCalls,
  isLiveWorkSegment,
  isPlanExecutionToolCall,
  type SectionItem,
  type TurnSegment,
} from './turn-segments';

export interface AssistantTurnBodyProps {
  items: SectionItem[];
  isActiveTurn: boolean;
  isRunning: boolean;
  onNextButtonClick?: (action: NextButtonAction) => void;
  onPlanConfirm?: (planId: string) => void;
  resolveTool?: (toolName: string, frontendHint?: string) => ClientTool | undefined;
  onAskQuestionSubmit?: (toolUseId: string, content: AskQuestionAnswerContent) => void;
  onToolConfirmSubmit?: (toolUseId: string, approved: boolean) => void;

  activeAskQuestion?: ToolCallState;
  plans?: Record<string, PlanRuntimeState>;
  /** 每个 Plan 当前最新的启动/恢复工具调用；更早的卡片只展示各自的历史结果快照。 */
  latestPlanToolUseIdByExecution?: ReadonlyMap<string, string>;
  onPlanResume?: (planExecutionId: string, waitRequestId: string, response: Record<string, unknown>) => void;
  onPlanRetry?: (planExecutionId: string, stepId: string) => void;
  onPlanSkip?: (planExecutionId: string, stepId: string) => void;
  onPlanCancel?: (planExecutionId: string) => void;
  onLoadPlan?: (planExecutionId: string) => void | Promise<void>;
  onLoadPlanAttempt?: (planExecutionId: string, stepAttemptId: string) => void | Promise<void>;
  onCodeCopy?: (step: Step, data: CodeBlockCopyData) => void;
  onCodeSelectionCopy?: (step: Step, data: CodeBlockSelectionCopyData) => void;
}

interface TurnSegmentViewProps extends Omit<AssistantTurnBodyProps, 'items'> {
  segment: TurnSegment;
  segmentIndex: number;
  segments: TurnSegment[];
}

function getRelatedToolUseIds(segments: TurnSegment[], contentIndex: number): string[] {
  let startIndex = contentIndex - 1;
  while (startIndex >= 0 && segments[startIndex].kind !== 'content') startIndex -= 1;

  const toolUseIds: string[] = [];
  for (let index = startIndex + 1; index < contentIndex; index += 1) {
    const segment = segments[index];
    if (segment.kind === 'work') {
      for (const part of segment.parts) {
        if (part.kind !== 'tools') continue;
        toolUseIds.push(...getWorkPartToolCalls(part.step, part.toolUseIds).map((tool) => tool.toolUseId));
      }
      continue;
    }
    if (segment.kind === 'plan' || segment.kind === 'root_tool' || segment.kind === 'ask_question') {
      toolUseIds.push(segment.toolUseId);
    }
  }
  return [...new Set(toolUseIds)];
}

function isLivePlanToolCall(
  toolCall: ToolCallState,
  latestPlanToolUseIdByExecution?: ReadonlyMap<string, string>,
): boolean {
  const planExecutionId = getPlanExecutionId(toolCall);
  return !!planExecutionId
    && (toolCall.status !== 'error' || !!toolCall.planExecutionId)
    && (!latestPlanToolUseIdByExecution
      || latestPlanToolUseIdByExecution.get(planExecutionId) === toolCall.toolUseId);
}

function TurnSegmentView(props: TurnSegmentViewProps) {
  const workSegment = createMemo(() => props.segment.kind === 'work' ? props.segment : undefined);
  const askSegment = createMemo(() => props.segment.kind === 'ask_question' ? props.segment : undefined);
  const planSegment = createMemo(() => props.segment.kind === 'plan' ? props.segment : undefined);
  const rootToolSegment = createMemo(() => props.segment.kind === 'root_tool' ? props.segment : undefined);
  const contentSegment = createMemo(() => props.segment.kind === 'content' ? props.segment : undefined);
  const codeCopyMemoryKey = createMemo(() => {
    const segment = contentSegment();
    if (!segment) return undefined;
    const toolUseIds = getRelatedToolUseIds(props.segments, props.segmentIndex);
    return `tools:${toolUseIds.join(',') || 'none'}:${segment.id}`;
  });
  const segmentOptions = createMemo(() => ({
    isActiveTurn: props.isActiveTurn,
    isRunning: props.isRunning,
  }));

  return (
    <>
      <Show when={workSegment()}>
        {(segment) => (
          <WorkBlock
            workId={segment().id}
            parts={segment().parts}
            collapsed={segment().collapsed}
            liveTimer={isLiveWorkSegment(props.segments, props.segmentIndex, segmentOptions())}
            resolveTool={props.resolveTool}
            onAskQuestionSubmit={props.onAskQuestionSubmit}
          onToolConfirmSubmit={props.onToolConfirmSubmit}
            activeAskQuestion={props.activeAskQuestion}
          />
        )}
      </Show>

      <Show when={askSegment()}>
        {(segment) => {
          const toolCall = () => getAskQuestionToolCall(segment().step, segment().toolUseId);
          return (
            <Show when={toolCall()}>
              {(tc) => {
                return (
                  <Show when={!(
                    tc().status === 'waiting'
                    && tc().toolUseId === props.activeAskQuestion?.toolUseId
                  )}>
                    <div class="agent-ui-message-assistant">
                      <div class="agent-ui-message-body">
                        <AskQuestionCard
                          toolCall={tc()}
                          onSubmit={props.onAskQuestionSubmit}
                          minimal={tc().status !== 'waiting'}
                        />
                      </div>
                    </div>
                  </Show>
                );
              }}
            </Show>
          );
        }}
      </Show>

      <Show when={planSegment()}>
        {(segment) => {
          const toolCall = () => getPlanToolCall(segment().step, segment().toolUseId);
          return (
            <Show when={toolCall()}>
              {(call) => (
                <div class="agent-ui-message-assistant agent-ui-message-plan">
                  <div class="agent-ui-message-body">
                    <Show
                      when={isPlanExecutionToolCall(call())}
                      fallback={(
                        <PlanBlock
                          toolCall={call()}
                          onConfirm={props.isRunning ? undefined : props.onPlanConfirm}
                        />
                      )}
                    >
                      <PlanRuntimeCard
                        toolCall={call()}
                        plan={getPlanExecutionId(call()) ? props.plans?.[getPlanExecutionId(call())!] : undefined}
                        useLiveView={isLivePlanToolCall(call(), props.latestPlanToolUseIdByExecution)}
                        disabled={props.isRunning}
                        resolveTool={props.resolveTool}
                        onResume={props.onPlanResume}
                        onRetry={props.onPlanRetry}
                        onSkip={props.onPlanSkip}
                        onCancel={props.onPlanCancel}
                        onLoadPlan={props.onLoadPlan}
                        onLoadAttempt={props.onLoadPlanAttempt}
                      />
                    </Show>
                  </div>
                </div>
              )}
            </Show>
          );
        }}
      </Show>

      <Show when={rootToolSegment()}>
        {(segment) => {
          const toolCall = () => segment().step.toolCalls.find((call) => call.toolUseId === segment().toolUseId);
          return (
            <Show when={toolCall()}>
              {(call) => (
                <div class="agent-ui-message-assistant agent-ui-message-root-tool">
                  <div class="agent-ui-message-body">
                    <ToolCallView
                      toolCall={call()}
                      resolveTool={props.resolveTool}
                      onAskQuestionSubmit={props.onAskQuestionSubmit}
                      onToolConfirmSubmit={props.onToolConfirmSubmit}
                      activeAskQuestion={props.activeAskQuestion}
                    />
                  </div>
                </div>
              )}
            </Show>
          );
        }}
      </Show>

      <Show when={contentSegment()}>
        {(segment) => (
          <div
            class="agent-ui-message-assistant"
            classList={{ 'agent-ui-message-error': segment().step.isError }}
          >
            <div class="agent-ui-message-body">
              <ContentBlock
                content={segment().step.content}
                complete={segment().step.contentComplete || !props.isActiveTurn || !props.isRunning}
                copyMemoryKey={codeCopyMemoryKey()}
                onNextButtonClick={props.onNextButtonClick
                  ? (buttonText, buttonIndex) => props.onNextButtonClick?.({
                      sourceRunId: segment().step.runId,
                      sourceStepIndex: segment().step.index,
                      buttonText,
                      buttonIndex,
                    })
                  : undefined}
                onCodeCopy={(data) => props.onCodeCopy?.(segment().step, data)}
                onCodeSelectionCopy={(data) => props.onCodeSelectionCopy?.(segment().step, data)}
              />
            </div>
          </div>
        )}
      </Show>
    </>
  );
}

export function AssistantTurnBody(props: AssistantTurnBodyProps) {

  const segmentOptions = createMemo(() => ({
    isActiveTurn: props.isActiveTurn,
    isRunning: props.isRunning,
    isRootTool: (toolCall: ToolCallState) => (
      props.resolveTool?.(toolCall.toolName, toolCall.frontendHint)?.uiPlacement === 'root'
    ),
  }));

  const segments = createMemo(() => buildTurnSegments(props.items, segmentOptions()));

  return (
    <Index each={segments()}>
      {(segment, index) => (
        <TurnSegmentView
          segment={segment()}
          segmentIndex={index}
          segments={segments()}
          isActiveTurn={props.isActiveTurn}
          isRunning={props.isRunning}
          onNextButtonClick={props.onNextButtonClick}
          onPlanConfirm={props.onPlanConfirm}
          resolveTool={props.resolveTool}
          onToolConfirmSubmit={props.onToolConfirmSubmit}
          onAskQuestionSubmit={props.onAskQuestionSubmit}
          activeAskQuestion={props.activeAskQuestion}
          plans={props.plans}
          latestPlanToolUseIdByExecution={props.latestPlanToolUseIdByExecution}
          onPlanResume={props.onPlanResume}
          onPlanRetry={props.onPlanRetry}
          onPlanSkip={props.onPlanSkip}
          onPlanCancel={props.onPlanCancel}
          onLoadPlan={props.onLoadPlan}
          onLoadPlanAttempt={props.onLoadPlanAttempt}
          onCodeCopy={props.onCodeCopy}
          onCodeSelectionCopy={props.onCodeSelectionCopy}
        />
      )}
    </Index>
  );
}
