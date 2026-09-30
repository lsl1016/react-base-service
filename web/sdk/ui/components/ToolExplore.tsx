/**
 * ToolExplore - 探索类工具调用卡片
 *
 * 用于展示 get_skill、list_tools 等探索类工具的执行状态与详情。
 */

import { createSignal, Show } from "solid-js";
import IconMdiChevronDown from "~icons/mdi/chevron-down";
import IconMdiChevronRight from "~icons/mdi/chevron-right";
import type { ToolCallState } from "../../runtime/types";
import type { ClientToolExploreUIOptions } from "../../tools/types";

export interface ToolExploreProps {
  /** 工具调用状态 */
  toolCall: ToolCallState;
  /** 是否展示详细的输入/输出 */
  showDetails?: boolean;
  /** explore 卡片的额外展示配置 */
  options?: ClientToolExploreUIOptions;
}

export function ToolExplore(props: ToolExploreProps) {
  const [expanded, setExpanded] = createSignal(props.options?.defaultExpanded === true);

  const isCollapsible = () => props.options?.collapsible !== false;

  const explorePrefixText = () => props.options?.prefixText?.trim() || '';

  const showInput = () => props.options?.showInput !== false
    && Object.keys(props.toolCall.input || {}).length > 0;
  const showResult = () => props.options?.showResult !== false && !!props.toolCall.result;
  const showDetails = () => !isCollapsible() || expanded();
  const toggleExpanded = () => {
    if (!isCollapsible()) return;
    setExpanded(!expanded());
  };

  const toolDisplayName = () => props.toolCall.description || props.toolCall.toolName;

  return (
    <div class="agent-ui-tool-card agent-ui-tool-explore">
      <div
        class="agent-ui-tool-header"
        onClick={toggleExpanded}
      >
        <div class="agent-ui-tool-info">
          <Show when={explorePrefixText()}>
            <span class="agent-ui-tool-explore-prefix-text">{explorePrefixText()}</span>
          </Show>
          <span class="agent-ui-tool-name" title={toolDisplayName()}>{toolDisplayName()}</span>

          {/* <Show when={props.toolCall.durationMs}>
            <span class="agent-ui-tool-duration">
              {props.toolCall.durationMs}ms
            </span>
          </Show> */}
          <Show when={isCollapsible()}>
            <span class="agent-ui-tool-expand-icon">
              <Show when={expanded()} fallback={<IconMdiChevronRight width="16" height="16" />}>
                <IconMdiChevronDown width="16" height="16" />
              </Show>
            </span>
          </Show>
        </div>
      </div>

      <Show when={showDetails()}>
        <div class="agent-ui-tool-details">
          <Show when={showInput()}>
            <div class="agent-ui-tool-section">
              <pre class="agent-ui-tool-json">
                {JSON.stringify(props.toolCall.input, null, 2)}
              </pre>
            </div>
          </Show>

          <Show when={showResult()}>
            <div class="agent-ui-tool-section">
              <pre class="agent-ui-tool-json">
                {props.toolCall.result}
              </pre>
            </div>
          </Show>

          <Show when={props.toolCall.isError}>
            <div class="agent-ui-tool-section agent-ui-tool-error">
              <div class="agent-ui-tool-section-title">错误信息</div>
              <div class="agent-ui-tool-section-content">
                {props.toolCall.result || "未知错误"}
              </div>
            </div>
          </Show>
        </div>
      </Show>
    </div>
  );
}