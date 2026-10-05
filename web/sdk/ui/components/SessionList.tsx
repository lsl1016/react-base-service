/**
 * SessionList - 会话列表组件
 *
 * 展示历史会话列表，支持在会话之间切换、删除会话（硬删，行内二次确认）、
 * 重命名会话（行内编辑，仅标题元数据，运行中的会话也可改名）。
 * 多会话架构：statuses 提供各会话 runtime 的运行状态（后台运行中/等待输入徽标）；
 * 运行中的会话删除按钮禁用（服务端同样会拒绝）。
 */

import { createSignal, For, Show } from "solid-js";
import IconMdiCheck from "~icons/mdi/check";
import IconMdiClose from "~icons/mdi/close";
import IconMdiPencilOutline from "~icons/mdi/pencil-outline";
import IconMdiTrashCanOutline from "~icons/mdi/trash-can-outline";
import type { SessionStatusInfo } from "../../runtime/agent-client";
import type { SessionMeta } from "../../storage/event-ledger";
import { ScrollArea } from "./ScrollArea";

/** 与服务端 customSessionTitleMaxLength 对齐的标题上限（rune） */
const SESSION_TITLE_MAX = 64;

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
  /** 删除会话时的回调（硬删不可恢复；运行中的会话由调用方/服务端拒绝） */
  onDeleteSession?: (sessionId: string) => Promise<void> | void;
  /** 重命名会话时的回调（仅标题元数据）；成功后由调用方刷新列表 */
  onRenameSession?: (sessionId: string, title: string) => Promise<void> | void;
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

/** 运行中的会话不允许删除（与服务端约束一致）。 */
function isSessionBusy(status?: SessionStatusInfo["status"]): boolean {
  return status === "running" || status === "waiting_client_tool" || status === "compacting" || status === "recovering";
}

export function SessionList(props: SessionListProps) {
  /** 行内二次确认：正在确认删除的会话 ID */
  const [confirmingId, setConfirmingId] = createSignal<string | null>(null);
  const [deletingId, setDeletingId] = createSignal<string | null>(null);
  /** 行内重命名：正在编辑的会话 ID + 草稿 + 提交中标记 */
  const [renamingId, setRenamingId] = createSignal<string | null>(null);
  const [renameDraft, setRenameDraft] = createSignal("");
  const [renamingBusy, setRenamingBusy] = createSignal(false);

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

  const busyFor = (sessionId: string): boolean => {
    const info = props.statuses?.find((item) => item.sessionId === sessionId);
    return info ? isSessionBusy(info.status) : false;
  };

  const confirmDelete = async (sessionId: string) => {
    if (!props.onDeleteSession || deletingId()) return;
    setDeletingId(sessionId);
    try {
      await props.onDeleteSession(sessionId);
      setConfirmingId(null);
    } finally {
      setDeletingId(null);
    }
  };

  const startRename = (session: SessionMeta) => {
    setConfirmingId(null);
    setRenameDraft(session.title ?? "");
    setRenamingId(session.sessionId);
  };

  const cancelRename = () => {
    if (renamingBusy()) return;
    setRenamingId(null);
    setRenameDraft("");
  };

  const confirmRename = async (sessionId: string) => {
    const title = renameDraft().trim();
    if (!props.onRenameSession || renamingBusy() || !title) return;
    setRenamingBusy(true);
    try {
      await props.onRenameSession(sessionId, title);
      setRenamingId(null);
      setRenameDraft("");
    } finally {
      setRenamingBusy(false);
    }
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
            <div
              class="agent-ui-session-item-wrapper"
              classList={{ "agent-ui-session-active": session.sessionId === props.activeSessionId }}
            >
              <Show
                when={renamingId() === session.sessionId}
                fallback={
                  <button
                    class="agent-ui-session-item"
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
                }
              >
                {/* 行内重命名编辑态：输入框 + 确认/取消，替换整行主区域 */}
                <div class="agent-ui-session-rename-box">
                  <input
                    class="agent-ui-session-rename-input"
                    type="text"
                    value={renameDraft()}
                    maxlength={SESSION_TITLE_MAX}
                    disabled={renamingBusy()}
                    ref={(el) => queueMicrotask(() => el.focus())}
                    onInput={(event) => setRenameDraft(event.currentTarget.value)}
                    onKeyUp={(event) => {
                      if (event.key === "Enter") void confirmRename(session.sessionId);
                      if (event.key === "Escape") cancelRename();
                    }}
                  />
                  <button
                    type="button"
                    class="agent-ui-session-rename-confirm"
                    disabled={renamingBusy() || !renameDraft().trim()}
                    title="保存标题"
                    onClick={() => void confirmRename(session.sessionId)}
                  >
                    <IconMdiCheck width="14" height="14" />
                  </button>
                  <button
                    type="button"
                    class="agent-ui-session-rename-cancel"
                    disabled={renamingBusy()}
                    title="取消"
                    onClick={cancelRename}
                  >
                    <IconMdiClose width="14" height="14" />
                  </button>
                </div>
              </Show>
              <Show when={renamingId() !== session.sessionId}>
                <Show
                  when={confirmingId() === session.sessionId}
                  fallback={
                    <>
                      <Show when={props.onRenameSession}>
                        <button
                          type="button"
                          class="agent-ui-session-rename-button"
                          title="重命名会话"
                          aria-label="重命名会话"
                          onClick={() => startRename(session)}
                        >
                          <IconMdiPencilOutline width="14" height="14" />
                        </button>
                      </Show>
                      <Show when={props.onDeleteSession}>
                        <button
                          type="button"
                          class="agent-ui-session-delete-button"
                          title={busyFor(session.sessionId) ? "会话运行中，请先停止任务再删除" : "删除会话（不可恢复）"}
                          disabled={busyFor(session.sessionId) || deletingId() === session.sessionId}
                          onClick={() => setConfirmingId(session.sessionId)}
                        >
                          <IconMdiTrashCanOutline width="14" height="14" />
                        </button>
                      </Show>
                    </>
                  }
                >
                  <div class="agent-ui-session-delete-confirm">
                    <span class="agent-ui-session-delete-hint">不可恢复</span>
                    <button
                      type="button"
                      class="agent-ui-session-delete-confirm-yes"
                      disabled={deletingId() === session.sessionId}
                      onClick={() => void confirmDelete(session.sessionId)}
                    >
                      <IconMdiCheck width="14" height="14" />
                    </button>
                    <button
                      type="button"
                      class="agent-ui-session-delete-confirm-no"
                      disabled={deletingId() === session.sessionId}
                      onClick={() => setConfirmingId(null)}
                    >
                      <IconMdiClose width="14" height="14" />
                    </button>
                  </div>
                </Show>
              </Show>
            </div>
          )}
        </For>
      </ScrollArea>
    </div>
  );
}