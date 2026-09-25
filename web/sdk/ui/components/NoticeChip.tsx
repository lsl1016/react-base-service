import { Show } from 'solid-js';
import type { Step } from '../../runtime/types';

/**
 * NoticeChip：Steering 引导/队列/后台任务通知的系统标记条（role='notice' 的 Step）。
 * 与 CompactDivider 同风格；携带 detail（如 <task-notification> 信封正文）时可展开查看。
 */
export function NoticeChip(props: { step: Step }) {
  const text = () => props.step.notice?.text?.trim() ?? '';
  const detail = () => props.step.notice?.detail?.trim() ?? '';
  const detailLines = () =>
    detail()
      ? detail()
          .split('\n')
          .filter((line) => line.trim().length > 0)
      : [];
  return (
    <div class="agent-ui-notice-chip">
      <div class="agent-ui-compact-divider agent-ui-notice-chip-divider" title={text() || undefined}>
        <span class="agent-ui-compact-divider-line" />
        <span class="agent-ui-compact-divider-label">{text()}</span>
        <span class="agent-ui-compact-divider-line" />
      </div>
      <Show when={detailLines().length > 0}>
        <details class="agent-ui-notice-chip-detail">
          <summary>通知内容</summary>
          <pre>{detail()}</pre>
        </details>
      </Show>
    </div>
  );
}
