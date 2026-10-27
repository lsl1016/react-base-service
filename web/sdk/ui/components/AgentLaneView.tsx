/**
 * AgentLaneView - 多代理泳道视图（P3）
 *
 * 并行委派时把会话按 agentPath 拆成泳道：主 Agent 一条，每个委派子代理一条。
 * 顶部 tab 栏选择当前查看的代理，一次只展示一条泳道（思考流 / 工具卡 / 任务与结论）；
 * 未手动选择时自动跟随运行中的泳道，点击 tab 后固定。刷新恢复由 store.steps 还原。
 * 与 MessageList 共用同一份 store.state.steps（纯渲染分流，无协议改动）。
 */
import { For, Show, createMemo, createSignal } from 'solid-js';
import type { AskQuestionAnswerContent } from '../../protocol/types';
import type { ToolCallState, Step } from '../../runtime/types';
import type { ClientTool } from '../../tools/types';
import type { AgentQuickInsertItem } from '../editor/types';
import { InputPartsView } from '../editor/InputPartsView';
import { ContentBlock } from './ContentBlock';
import { ThoughtBlock } from './ThoughtBlock';
import { ToolCallView } from './ToolCallView';
import { groupStepsByAgentLane, laneIsActive, resolveLaneTabPath } from './lanes';

export interface AgentLaneViewProps {
  steps: Step[];
  isRunning?: boolean;
  resolveTool?: (toolName: string, frontendHint?: string) => ClientTool | undefined;
  onAskQuestionSubmit?: (toolUseId: string, content: AskQuestionAnswerContent) => void;
  onToolConfirmSubmit?: (toolUseId: string, approved: boolean) => void;
  quickInsertItems?: AgentQuickInsertItem[];
}

function LaneStep(props: { step: Step } & Omit<AgentLaneViewProps, 'steps' | 'isRunning'>) {
  const step = () => props.step;
  return (
    <Show when={step().role !== 'compact'}>
      <Show when={step().role === 'user'}>
        <div class="agent-ui-lane-task" title="该泳道收到的任务">
          {step().displayParts?.length
            ? <InputPartsView parts={step().displayParts} fallback={step().content} quickInsertItems={props.quickInsertItems} />
            : <span class="agent-ui-lane-task-text">{step().content}</span>}
        </div>
      </Show>
      <Show when={step().role === 'assistant'}>
        <div class="agent-ui-lane-step">
          <Show when={step().thoughts}>
            <ThoughtBlock content={step().thoughts} complete={step().thoughtComplete} defaultCollapsed={false} />
          </Show>
          <For each={step().toolCalls}>
            {(toolCall: ToolCallState) => (
              <div class="agent-ui-lane-tool">
                <ToolCallView
                  toolCall={toolCall}
                  resolveTool={props.resolveTool}
                  onAskQuestionSubmit={props.onAskQuestionSubmit}
                  onToolConfirmSubmit={props.onToolConfirmSubmit}
                />
              </div>
            )}
          </For>
          <Show when={step().content}>
            <ContentBlock content={step().content} complete={step().contentComplete} />
          </Show>
          <Show when={step().terminalStatus}>
            <div class="agent-ui-run-terminal" role="status">
              <span class="agent-ui-run-terminal-icon" aria-hidden="true">⚠</span>
              <span>本轮运行异常终止{step().terminalMessage ? `：${step().terminalMessage}` : ''}</span>
            </div>
          </Show>
        </div>
      </Show>
    </Show>
  );
}

export function AgentLaneView(props: AgentLaneViewProps) {
  const lanes = createMemo(() => groupStepsByAgentLane(props.steps));
  // pinnedPath 为 null 表示未手动选择，由 resolveLaneTabPath 自动跟随活动泳道。
  const [pinnedPath, setPinnedPath] = createSignal<string | null>(null);
  const activePath = createMemo(() => resolveLaneTabPath(lanes(), pinnedPath(), !!props.isRunning));
  const activeLane = createMemo(() => lanes().find((lane) => lane.path === activePath()));
  const isTabBarVisible = () => lanes().length > 1;

  return (
    <div class="agent-ui-lanes">
      <Show when={isTabBarVisible()}>
        <div class="agent-ui-lane-tabs" role="tablist" aria-label="代理泳道">
          <For each={lanes()}>
            {(lane) => (
              <button
                type="button"
                role="tab"
                class="agent-ui-lane-tab"
                classList={{ 'agent-ui-lane-tab-selected': lane.path === activePath() }}
                aria-selected={lane.path === activePath() ? 'true' : 'false'}
                title={lane.path}
                onClick={() => setPinnedPath(lane.path)}
              >
                <span class="agent-ui-lane-tab-label">{lane.label}</span>
                <Show when={props.isRunning && laneIsActive(lane)}>
                  <span class="agent-ui-lane-live" title="该代理正在工作">●</span>
                </Show>
                <span class="agent-ui-lane-count">{lane.steps.length} 步</span>
              </button>
            )}
          </For>
        </div>
      </Show>
      <Show when={activeLane()} keyed>
        {(lane) => (
          <section
            class="agent-ui-lane"
            classList={{ 'agent-ui-lane-active': props.isRunning && laneIsActive(lane) }}
          >
            {/* tab 栏承担泳道标题后，仅单泳道（无 tab）时保留原头部信息 */}
            <Show when={!isTabBarVisible()}>
              <header class="agent-ui-lane-head">
                <span class="agent-ui-agent-badge agent-ui-lane-badge" title={lane.path}>{lane.label}</span>
                <Show when={props.isRunning && laneIsActive(lane)}>
                  <span class="agent-ui-lane-live" title="该代理正在工作">● 运行中</span>
                </Show>
                <span class="agent-ui-lane-count">{lane.steps.length} 步</span>
              </header>
            </Show>
            <div class="agent-ui-lane-body">
              <For each={lane.steps}>
                {(step) => (
                  <LaneStep
                    step={step}
                    resolveTool={props.resolveTool}
                    onAskQuestionSubmit={props.onAskQuestionSubmit}
                    onToolConfirmSubmit={props.onToolConfirmSubmit}
                    quickInsertItems={props.quickInsertItems}
                  />
                )}
              </For>
            </div>
          </section>
        )}
      </Show>
    </div>
  );
}
