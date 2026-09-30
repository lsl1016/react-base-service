/**
 * SessionList - 会话列表组件
 *
 * 展示历史会话列表，支持在会话之间切换。
 * 多会话架构：statuses 提供各会话 runtime 的运行状态（后台运行中/等待输入徽标）。
 */

import { For, Show } from "solid-js";
import type { SessionStatusInfo } from "../../runtime/agent-client";
import type { SessionMeta } from "../../storage/event-ledger";
import { ScrollArea } from "./ScrollArea";

export interface SessionListProps {
  /** 会话列表 */
  sessions: SessionMeta[];
  /** 当前激活的会话 ID */
  activeSessionId: string | null;
  /** 是否禁用新会话入口 */
  newSessionDisabled?: boolean;
  /** 各会话运行时状态（后台运行徽标）；缺省时不渲染徽标 */
  statuses?: SessionStatusInfo[];
  /** 选中会话时的回调 */
  onSelectSession: (sessionId: string) => void;
  /** 创建新会话时的回调 */
  onNewSession: () => void;
}

/** 会话运行徽标文案：空串表示不显示。 */
function sessionBadgeText(status?: SessionStatusInfo["status"]): string {
  switch (status) {
    case "running":
    case "compacting":
      return "运行中";
    case "waiting_client_tool":
      return "等待输入";
    case "recovering":
      return "恢复中";
    default:
      return "";
  }
}

export function SessionList(props: SessionListProps) {
  const formatDate = (value: string) => {
    const normalized = value.includes("T") ? value : value.replace(" ", "T");
    const date = new Date(normalized);
    if (Number.isNaN(date.getTime())) return "";

    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffMins = Math.floor(diffMs / 60000);
    const diffHours = Math.floor(diffMs / 3600000);
    const diffDays = Math.floor(diffMs / 86400000);

    if (diffMins < 1) return "刚刚";
    if (diffMins < 60) return `${diffMins}分钟前`;
    if (diffHours < 24) return `${diffHours}小时前`;
    if (diffDays < 7) return `${diffDays}天前`;

    return date.toLocaleDateString("zh-CN", {
      month: "short",
      day: "numeric",
    });
  };

  const badgeFor = (sessionId: string): string => {
    const info = props.statuses?.find((item) => item.sessionId === sessionId);
    return info ? sessionBadgeText(info.status) : "";
  };

  return (
    <div class="agent-ui-session-list">
      <div class="agent-ui-session-header">
        <h3 class="agent-ui-session-title">会话历史</h3>
        <button
          class="agent-ui-session-new-button"
          disabled={props.newSessionDisabled}
          onClick={props.onNewSession}
        >
          + 新会话
        </button>
      </div>

      <ScrollArea class="agent-ui-session-items" contentClass="agent-ui-session-items-content" size="sm">
        <Show when={props.sessions.length === 0}>
          <div class="agent-ui-session-empty">
            暂无会话历史
          </div>
        </Show>

        <For each={props.sessions}>
          {(session) => (
            <button
              class="agent-ui-session-item"
              classList={{
                "agent-ui-session-active": session.sessionId === props.activeSessionId,
              }}
              onClick={() => props.onSelectSession(session.sessionId)}
            >
              <div class="agent-ui-session-item-title">
                {session.title || "未命名会话"}
                <Show when={badgeFor(session.sessionId)}>
                  <span class="agent-ui-session-item-badge">{badgeFor(session.sessionId)}</span>
                </Show>
              </div>
              <Show when={session.lastMessage}>
                <div class="agent-ui-session-item-preview">
                  {session.lastMessage}
                </div>
              </Show>
              <div class="agent-ui-session-item-meta">
                <span class="agent-ui-session-item-events">
                  {session.eventCount > 0 ? `${session.eventCount} 条事件` : session.state}
                </span>
                <span class="agent-ui-session-item-time">
                  {formatDate(session.updatedAt)}
                </span>
              </div>
            </button>
          )}
        </For>
      </ScrollArea>
    </div>
  );
}