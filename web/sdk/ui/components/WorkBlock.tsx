/**
 * WorkBlock - 连续的 thought / tool 工作区，与 Content 同级。
 */

import {
  For,
  Index,
  Show,
  createEffect,
  createMemo,
  createSignal,
  createUniqueId,
  on,
  untrack,
} from 'solid-js';
import type { AskQuestionAnswerContent } from '../../protocol/types';
import type { Step, ToolCallState } from '../../runtime/types';
import type { ClientTool } from '../../tools/types';
import { ThoughtBlock } from './ThoughtBlock';
import { CompactDivider } from './CompactDivider';
import { NoticeChip } from './NoticeChip';
import { AutoScrollPanel } from './AutoScrollPanel';
import { PhaseGapIndicator } from './PhaseGapIndicator';
import { ToolCallView } from './ToolCallView';
import type { WorkPart } from './turn-segments';
import {
  formatWorkSummary,
  getWorkPartToolCalls,
  summarizeWorkParts,
} from './turn-segments';

export interface WorkBlockProps {
  workId: string;
  parts: WorkPart[];
  collapsed: boolean;
  liveTimer?: boolean;
  resolveTool?: (toolName: string, frontendHint?: string) => ClientTool | undefined;
  onAskQuestionSubmit?: (toolUseId: string, content: AskQuestionAnswerContent) => void;
  onToolConfirmSubmit?: (toolUseId: string, approved: boolean) => void;
  activeAskQuestion?: ToolCallState;
}

function WorkThought(props: { step: Step }) {
  return (
    <ThoughtBlock
      content={props.step.thoughts}
      complete={props.step.thoughtComplete}
      defaultCollapsed={false}
    />
  );
}

function WorkTools(props: {
  step: Step;
  toolUseIds?: readonly string[];
  resolveTool?: (toolName: string, frontendHint?: string) => ClientTool | undefined;
  onAskQuestionSubmit?: (toolUseId: string, content: AskQuestionAnswerContent) => void;
  onToolConfirmSubmit?: (toolUseId: string, approved: boolean) => void;
  activeAskQuestion?: ToolCallState;
}) {
  const toolCalls = createMemo(() => getWorkPartToolCalls(props.step, props.toolUseIds));
  return (
    <Show when={toolCalls().length > 0}>
      <div class="agent-ui-message-tools">
        <For each={toolCalls()}>
          {(toolCall) => (
            <ToolCallView
              toolCall={toolCall}
              resolveTool={props.resolveTool}
              onAskQuestionSubmit={props.onAskQuestionSubmit}
              onToolConfirmSubmit={props.onToolConfirmSubmit}
              activeAskQuestion={props.activeAskQuestion}
            />
          )}
        </For>
      </div>
    </Show>
  );
}

export function WorkBlock(props: WorkBlockProps) {
  const [userExpanded, setUserExpanded] = createSignal(false);
  const [liveBodyCollapsed, setLiveBodyCollapsed] = createSignal(false);
  const bodyId = `agent-ui-work-block-body-${createUniqueId()}`;

  const getSummaryLabel = () => {
    const { thoughtCount, toolRunCount } = summarizeWorkParts(props.parts);
    return formatWorkSummary(thoughtCount, toolRunCount);
  };
  const [summaryLabel, setSummaryLabel] = createSignal(untrack(getSummaryLabel));

  /** 仅以 liveTimer 为准，避免 collapsed=false 且 live 已结束时出现「无 chevron、无 loading」的中间态 */
  const isLive = createMemo(() => props.liveTimer === true);
  const showBody = createMemo(() => (isLive() ? !liveBodyCollapsed() : userExpanded()));

  const scrollFollowKey = createMemo(() => props.parts.map((part) => {
    if (part.kind === 'thought') {
      return `t:${part.step.thoughts.length}:${part.step.thoughtComplete ? 1 : 0}`;
    }
    if (part.kind === 'tools') {
      const tools = getWorkPartToolCalls(part.step, part.toolUseIds);
      return `o:${tools.map((tool) => `${tool.toolUseId}:${tool.status}:${tool.result?.length ?? 0}`).join(',')}`;
    }
    return `c:${part.step.index}`;
  }).join('|'));

  createEffect(on(() => props.workId, () => {
    setUserExpanded(false);
    setLiveBodyCollapsed(false);
    setSummaryLabel(untrack(getSummaryLabel));
  }));

  createEffect(on(isLive, (live, wasLive) => {
    if (live && wasLive === false) {
      setLiveBodyCollapsed(false);
    }
    if (!live && wasLive) {
      setSummaryLabel(untrack(getSummaryLabel));
    }
  }));

  const toggleBody = () => {
    if (isLive()) {
      setLiveBodyCollapsed((value) => !value);
      return;
    }
    setUserExpanded((value) => !value);
  };

  return (
    <div
      class="agent-ui-work-block"
      classList={{
        'agent-ui-work-block-collapsed': !showBody(),
        'agent-ui-work-block-live': isLive(),
      }}
    >
      <button
        type="button"
        class="agent-ui-work-block-header"
        aria-expanded={showBody()}
        aria-controls={bodyId}
        aria-label={showBody() ? '收起工作过程' : '展开工作过程'}
        onClick={toggleBody}
      >
        <PhaseGapIndicator
          showIcon={isLive()}
          showChevron
          text={isLive() ? '工作中' : summaryLabel()}
          expanded={showBody()}
        />
      </button>

      <div id={bodyId} class="agent-ui-work-block-body">
        <Show when={showBody()}>
          <AutoScrollPanel
            class="agent-ui-work-block-inner"
            followKey={scrollFollowKey()}
          >
            <div class="agent-ui-auto-scroll-panel-stack">
              <Index each={props.parts}>
                {(partAccessor) => {
                  const compactPart = createMemo(() => {
                    const part = partAccessor();
                    return part.kind === 'compact' ? part : undefined;
                  });
                  const noticePart = createMemo(() => {
                    const part = partAccessor();
                    return part.kind === 'notice' ? part : undefined;
                  });
                  const thoughtPart = createMemo(() => {
                    const part = partAccessor();
                    return part.kind === 'thought' ? part : undefined;
                  });
                  const toolsPart = createMemo(() => {
                    const part = partAccessor();
                    return part.kind === 'tools' ? part : undefined;
                  });

                  return (
                    <>
                      <Show when={compactPart()}>
                        {(part) => <CompactDivider step={part().step} />}
                      </Show>
                      <Show when={noticePart()}>
                        {(part) => <NoticeChip step={part().step} />}
                      </Show>
                      <Show when={thoughtPart()}>
                        {(part) => <WorkThought step={part().step} />}
                      </Show>
                      <Show when={toolsPart()}>
                        {(part) => (
                          <WorkTools
                            step={part().step}
                            toolUseIds={part().toolUseIds}
                            resolveTool={props.resolveTool}
                            onAskQuestionSubmit={props.onAskQuestionSubmit}
                            onToolConfirmSubmit={props.onToolConfirmSubmit}
                            activeAskQuestion={props.activeAskQuestion}
                          />
                        )}
                      </Show>
                    </>
                  );
                }}
              </Index>
            </div>
          </AutoScrollPanel>
        </Show>
      </div>
    </div>
  );
}
