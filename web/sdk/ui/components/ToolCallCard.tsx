/**
 * ToolCallCard - 工具调用卡片
 *
 * 展示工具名称、执行状态，以及可选的输入/输出详情。
 * 同时支持服务端执行和客户端执行的工具。
 */

import { createSignal, Match, Show, Switch } from "solid-js";
import IconMdiCached from "~icons/mdi/cached";
import IconMdiCheckCircle from "~icons/tabler/ai-gateway";
import IconMdiAiGateway from "~icons/tabler/ai-gateway";
import IconMdiChevronDown from "~icons/mdi/chevron-down";
import IconMdiChevronRight from "~icons/mdi/chevron-right";
import IconMdiCloseCircle from "~icons/tabler/ai-gateway";
import IconMdiCog from "~icons/mdi/cog";
import IconMdiPause from "~icons/mdi/pause";
import type { ToolCallState } from "../../runtime/types";

export interface ToolCallCardProps {
  /** 工具调用状态 */
  toolCall: ToolCallState;
  /** 是否展示详细的输入/输出 */
  showDetails?: boolean;
  /** 危险操作确认作答（P2-3）：confirmReason 存在且 waiting 时卡片渲染允许/拒绝按钮 */
  onToolConfirmSubmit?: (toolUseId: string, approved: boolean) => void;
}

export function ToolCallCard(props: ToolCallCardProps) {
  const [expanded, setExpanded] = createSignal(false);

  const statusIcon = () => {
    return (
      <Switch fallback={<IconMdiCog width="16" height="16" />}>
        <Match when={props.toolCall.status === "running"}>
          <span class="agent-ui-tool-status-running">
            <IconMdiAiGateway width="16" height="16" class="agent-ui-spin agent-ui-tool-status-icon" />
          </span>
        </Match>
        <Match when={props.toolCall.status === "waiting"}>
          <span class="agent-ui-tool-status-waiting">
            <IconMdiAiGateway width="16" height="16" />
          </span>
        </Match>
        <Match when={props.toolCall.status === "done"}>
          <span class="agent-ui-tool-status-done">
            <IconMdiAiGateway width="16" height="16" />
          </span>
        </Match>
        <Match when={props.toolCall.status === "error"}>
          <span class="agent-ui-tool-status-error">
            <IconMdiAiGateway width="16" height="16" />
          </span>
        </Match>
        <Match when={props.toolCall.status === "cancelled"}>
          <span class="agent-ui-tool-status-waiting">
            <IconMdiAiGateway width="16" height="16" />
          </span>
        </Match>
      </Switch>
    );
  };

  const statusText = () => {
    switch (props.toolCall.status) {
      case "running":
        return "执行中...";
      case "waiting":
        return "等待执行";
      case "done":
        return "已完成";
      case "error":
        return "执行失败";
      case "cancelled":
        return "已取消";
      default:
        return props.toolCall.status;
    }
  };

  const executedByText = () => {
    switch (props.toolCall.executedBy) {
      case "server":
        return "服务端";
      case "internal":
        return "内置";
      case "client":
        return "客户端";
      default:
        return props.toolCall.executedBy;
    }
  };

  const toolDisplayName = () => {
    return props.toolCall.description || props.toolCall.toolName
  };

  // ToolMeta 声明的风险等级徽标（后端 tool_use 事件下发）；缺省不展示。
  const riskBadge = () => {
    const level = props.toolCall.riskLevel;
    if (!level) return null;
    const label = level === 'high' ? '高风险' : level === 'medium' ? '中风险' : '低风险';
    return (
      <span class={`agent-ui-tool-risk-badge agent-ui-tool-risk-${level}`} title={`风险等级：${label}`}>
        {label}
      </span>
    );
  };

  // ToolMeta 声明的只读徽标（只读执行域内非只读工具会被服务端硬拦截）。
  const readOnlyBadge = () => (
    <Show when={props.toolCall.readOnly}>
      <span class="agent-ui-tool-readonly-badge" title="只读工具：无外部副作用">
        只读
      </span>
    </Show>
  );

  return (
    <div class="agent-ui-tool-card" data-tool-use-id={props.toolCall.toolUseId}>
      <div
        class="agent-ui-tool-header"
        onClick={() => setExpanded(!expanded())}
      >
        <div class="agent-ui-tool-info">
          {statusIcon()}
          <span class="agent-ui-tool-name" title={toolDisplayName()}>{toolDisplayName()}</span>
          {riskBadge()}
          {readOnlyBadge()}
          <Show when={props.toolCall.agentPath}>
            <span class="agent-ui-agent-badge" title={props.toolCall.agentPath}>
              {props.toolCall.agentPath}
            </span>
          </Show>
          <span class="agent-ui-tool-status">{statusText()}</span>
        </div>
        <Show when={props.toolCall.durationMs}>
          <span class="agent-ui-tool-duration">
            {props.toolCall.durationMs}ms
          </span>
        </Show>
        <span class="agent-ui-tool-expand-icon">
          <Show when={expanded()} fallback={<IconMdiChevronRight width="16" height="16" />}>
            <IconMdiChevronDown width="16" height="16" />
          </Show>
        </span>
      </div>

      <Show when={props.toolCall.status === 'waiting' && props.toolCall.confirmReason}>
        <div class="agent-ui-tool-confirm">
          <div class="agent-ui-tool-confirm-reason">需要人工确认：{props.toolCall.confirmReason}</div>
          <div class="agent-ui-tool-confirm-actions">
            <button
              type="button"
              class="agent-ui-tool-confirm-approve"
              onClick={() => props.onToolConfirmSubmit?.(props.toolCall.toolUseId, true)}
            >
              允许执行
            </button>
            <button
              type="button"
              class="agent-ui-tool-confirm-reject"
              onClick={() => props.onToolConfirmSubmit?.(props.toolCall.toolUseId, false)}
            >
              拒绝
            </button>
          </div>
        </div>
      </Show>

      <Show when={expanded()}>
        <div class="agent-ui-tool-details">
          <Show when={Object.keys(props.toolCall.input || {}).length > 0}>
            <div class="agent-ui-tool-section">
              <pre class="agent-ui-tool-json">
                {JSON.stringify(props.toolCall.input, null, 2)}
              </pre>
            </div>
          </Show>

          <Show when={props.toolCall.result}>
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