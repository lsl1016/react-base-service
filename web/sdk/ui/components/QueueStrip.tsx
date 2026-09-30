import { createSignal, For, Show } from "solid-js";
import IconMdiArrowDown from "~icons/mdi/arrow-down";
import IconMdiArrowUp from "~icons/mdi/arrow-up";
import IconMdiCheck from "~icons/mdi/check";
import IconMdiChevronDown from "~icons/mdi/chevron-down";
import IconMdiChevronUp from "~icons/mdi/chevron-up";
import IconMdiClockOutline from "~icons/mdi/clock-outline";
import IconMdiClose from "~icons/mdi/close";
import IconMdiPencilOutline from "~icons/mdi/pencil-outline";
import IconMdiSendOutline from "~icons/mdi/send-outline";
import type { QueueItem } from "../../session/types";

/**
 * QueueStrip：会话排队输入的管理条（S3 队列管理）。
 *
 * 展示当前会话 run 运行中准入排队、等待消费的用户消息（FIFO），支持：
 * - 展开/收起队列列表（输入框上方常驻，run 结束队列消费完自动消失）
 * - 编辑/删除单条排队输入（后端 claim-once，已被晋升/作废时操作落空）
 * - 上下移动调整执行顺序（服务端同步改写账本 seq）
 * - 每条"立即发送"双模式：run 运行中 → 晋升为 guide 注入当前对话（下一个安全
 *   边界生效，带附件的项不支持注入）；空闲 → claim-once 晋升开新 run
 */
export interface QueueStripProps {
  items: QueueItem[];
  /** 当前是否有 run 在进行（决定"立即发送"走注入还是开新 run） */
  isRunning: boolean;
  connected: boolean;
  /** steering 队列配置回显（决定提示文案） */
  autoDrain: boolean;
  /** 编辑一条排队输入；返回 false 表示未生效（已被晋升/作废或请求失败） */
  onEdit: (id: number, content: string) => Promise<boolean>;
  /** 删除一条排队输入；返回 false 表示未生效 */
  onDelete: (id: number) => Promise<boolean>;
  /** 按新顺序提交全部排队输入 ID */
  onReorder: (idList: number[]) => void;
  /** 立即发送一条排队输入：mode=inject（运行中注入当前对话）或 run（空闲开新 run） */
  onSend: (pendingInputId: string, mode: 'run' | 'inject') => void;
}

const CLAIM_LOST_NOTICE = "该条已被消费或作废，操作未生效";

export function QueueStrip(props: QueueStripProps) {
  const [expanded, setExpanded] = createSignal(false);
  const [editingId, setEditingId] = createSignal<number | null>(null);
  const [editText, setEditText] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [notice, setNotice] = createSignal("");

  const items = () => props.items;
  const disabled = () => busy() || !props.connected;

  const showNotice = (message: string) => {
    setNotice(message);
    setTimeout(() => setNotice((current) => (current === message ? "" : current)), 3000);
  };

  const startEdit = (item: QueueItem) => {
    setEditingId(item.id);
    setEditText(item.content);
  };

  const cancelEdit = () => {
    setEditingId(null);
    setEditText("");
  };

  const saveEdit = async () => {
    const id = editingId();
    const content = editText().trim();
    if (id == null || !content || busy()) return;
    setBusy(true);
    try {
      const claimed = await props.onEdit(id, content);
      if (claimed) {
        cancelEdit();
      } else {
        showNotice(CLAIM_LOST_NOTICE);
      }
    } finally {
      setBusy(false);
    }
  };

  const removeItem = async (id: number) => {
    if (busy()) return;
    setBusy(true);
    try {
      const claimed = await props.onDelete(id);
      if (!claimed) showNotice(CLAIM_LOST_NOTICE);
    } finally {
      setBusy(false);
    }
  };

  const move = (index: number, offset: -1 | 1) => {
    const target = index + offset;
    if (target < 0 || target >= items().length || disabled()) return;
    const next = [...items()];
    [next[index], next[target]] = [next[target], next[index]];
    props.onReorder(next.map((item) => item.id));
  };

  /** 每条"立即发送"：运行中注入当前对话（带附件除外），空闲开新 run。 */
  const sendItem = (item: QueueItem) => {
    if (disabled()) return;
    if (props.isRunning && item.hasAttachments) return;
    props.onSend(item.pendingInputId, props.isRunning ? 'inject' : 'run');
  };

  /** 单条"立即发送"按钮的禁用条件与提示文案。 */
  const sendItemDisabled = (item: QueueItem): boolean => disabled() || (props.isRunning && !!item.hasAttachments);
  const sendItemTitle = (item: QueueItem): string => {
    if (props.isRunning && item.hasAttachments) return "带附件的排队消息不支持注入，将在 run 结束后自动执行";
    return props.isRunning ? "立即发送：注入当前对话（下一个安全边界生效）" : "立即发送（开新 run）";
  };

  return (
    <Show when={items().length > 0}>
      <div class="agent-ui-queue-strip">
        <button
          type="button"
          class="agent-ui-queue-strip-header"
          onClick={() => setExpanded((visible) => !visible)}
          title={expanded() ? "收起队列" : "展开队列"}
        >
          <IconMdiClockOutline width="14" height="14" class="agent-ui-queue-strip-icon" />
          <span class="agent-ui-queue-strip-title">
            已排队 {items().length} 条
            <span class="agent-ui-queue-strip-hint">
              {props.autoDrain ? "，run 结束后依次执行" : "，自动续跑未开启"}
            </span>
          </span>
          <Show when={notice()} fallback={<Show when={expanded()}><IconMdiChevronUp width="14" height="14" /></Show>}>
            <span class="agent-ui-queue-strip-notice">{notice()}</span>
          </Show>
          <Show when={!notice()}>
            <Show when={!expanded()}><IconMdiChevronDown width="14" height="14" /></Show>
          </Show>
        </button>
        <Show when={expanded()}>
          <div class="agent-ui-queue-strip-list">
            <For each={items()}>
              {(item, index) => (
                <Show
                  when={editingId() === item.id}
                  fallback={
                    <div class="agent-ui-queue-strip-item">
                      <span class="agent-ui-queue-strip-seq">{index() + 1}</span>
                      <span class="agent-ui-queue-strip-content" title={item.content}>{item.content}</span>
                      <span class="agent-ui-queue-strip-actions">
                        <button
                          type="button"
                          class="agent-ui-queue-strip-send-item"
                          title={sendItemTitle(item)}
                          disabled={sendItemDisabled(item)}
                          onClick={() => sendItem(item)}
                        >
                          <IconMdiSendOutline width="13" height="13" />
                        </button>
                        <button type="button" title="上移" disabled={index() === 0 || disabled()} onClick={() => move(index(), -1)}>
                          <IconMdiArrowUp width="13" height="13" />
                        </button>
                        <button type="button" title="下移" disabled={index() === items().length - 1 || disabled()} onClick={() => move(index(), 1)}>
                          <IconMdiArrowDown width="13" height="13" />
                        </button>
                        <button type="button" title="编辑" disabled={disabled()} onClick={() => startEdit(item)}>
                          <IconMdiPencilOutline width="13" height="13" />
                        </button>
                        <button type="button" title="删除" disabled={disabled()} onClick={() => void removeItem(item.id)}>
                          <IconMdiClose width="13" height="13" />
                        </button>
                      </span>
                    </div>
                  }
                >
                  <div class="agent-ui-queue-strip-editor">
                    <textarea
                      rows={2}
                      value={editText()}
                      disabled={busy()}
                      onInput={(event) => setEditText(event.currentTarget.value)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" && !event.shiftKey) {
                          event.preventDefault();
                          void saveEdit();
                        }
                        if (event.key === "Escape") cancelEdit();
                      }}
                    />
                    <span class="agent-ui-queue-strip-actions">
                      <button type="button" title="保存" disabled={busy() || !editText().trim()} onClick={() => void saveEdit()}>
                        <IconMdiCheck width="13" height="13" />
                      </button>
                      <button type="button" title="取消" disabled={busy()} onClick={cancelEdit}>
                        <IconMdiClose width="13" height="13" />
                      </button>
                    </span>
                  </div>
                </Show>
              )}
            </For>
          </div>
          <div class="agent-ui-queue-strip-footer">
            <span class="agent-ui-queue-strip-footer-hint">
              {props.isRunning
                ? "当前 run 结束后将按顺序自动执行；点条目右侧发送图标可立即注入当前对话"
                : props.autoDrain
                  ? "队列空闲，可立即发送或等待下一次 run 结束"
                  : "自动续跑未开启，点条目右侧发送图标可立即执行"}
            </span>
          </div>
        </Show>
      </div>
    </Show>
  );
}
