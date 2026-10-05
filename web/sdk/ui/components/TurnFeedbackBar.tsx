/**
 * TurnFeedbackBar - 单轮(run)反馈条
 *
 * 渲染在每个已完成轮次末尾：点赞、点踩、问题反馈、复制回复。
 * 点赞/点踩互斥且可再次点击取消；问题反馈展开文本框（≤500 字）。
 */

import copy from "copy-to-clipboard";
import { Show, createEffect, createSignal, type JSX } from "solid-js";
import IconIcRoundContentCopy from "~icons/ic/round-content-copy";
import IconMdiBugOutline from "~icons/mdi/bug-outline";
import IconMdiCommentQuestionOutline from "~icons/mdi/comment-question-outline";
import IconMdiSourceBranch from "~icons/mdi/source-branch";
import IconMdiThumbDown from "~icons/mdi/thumb-down";
import IconMdiThumbDownOutline from "~icons/mdi/thumb-down-outline";
import IconMdiThumbUp from "~icons/mdi/thumb-up";
import IconMdiThumbUpOutline from "~icons/mdi/thumb-up-outline";
import type { RunFeedbackPayload, RunFeedbackState } from "../../runtime/types";

const PROBLEM_FEEDBACK_MAX = 500;

export interface TurnFeedbackBarProps {
  runId: string;
  sessionId?: string | null;
  callerKey?: string;
  routeValues?: string[];
  state?: RunFeedbackState;
  copyContent?: string;
  /** 提交轮次反馈；不传则隐藏点赞/点踩/问题反馈按钮（可只保留分叉入口） */
  onSubmit?: (runId: string, payload: RunFeedbackPayload) => void | Promise<void>;
  onProblemFeedbackOpen?: (runId: string) => void;
  /** 从该轮末尾分叉出新会话（带着该轮及之前的历史继续）；不传则不渲染分叉入口 */
  onFork?: (runId: string) => void | Promise<void>;
}

export function TurnFeedbackBar(props: TurnFeedbackBarProps): JSX.Element {
  const [showForm, setShowForm] = createSignal(false);
  const [draft, setDraft] = createSignal("");
  const [submitting, setSubmitting] = createSignal(false);
  const [contentCopied, setContentCopied] = createSignal(false);
  const [sessionIdCopied, setSessionIdCopied] = createSignal(false);
  const [forking, setForking] = createSignal(false);

  createEffect(() => {
    void props.runId;
    void props.sessionId;
    void props.callerKey;
    void props.routeValues;
    setContentCopied(false);
    setSessionIdCopied(false);
  });

  const feedback = () => props.state?.feedback ?? 0;
  const isLiked = () => feedback() === 1;
  const isDisliked = () => feedback() === -1;
  const hasProblem = () => !!props.state?.problemFeedback;

  const submit = async (payload: RunFeedbackPayload) => {
    if (!props.onSubmit || submitting()) return;
    setSubmitting(true);
    try {
      await props.onSubmit(props.runId, payload);
    } finally {
      setSubmitting(false);
    }
  };

  const handleLike = () => submit({ feedback: isLiked() ? 0 : 1 });
  const handleDislike = () => submit({ feedback: isDisliked() ? 0 : -1 });

  const openForm = () => {
    setDraft(props.state?.problemFeedback ?? "");
    setShowForm(true);
    props.onProblemFeedbackOpen?.(props.runId);
  };

  const submitProblem = async () => {
    await submit({ problemFeedback: draft().trim() });
    setShowForm(false);
  };

  const handleCopy = () => {
    if (!props.copyContent || !copy(props.copyContent)) return;
    setContentCopied(true);
  };

  const handleCopySessionId = () => {
    if (!props.sessionId) return;
    const content = JSON.stringify({
      sessionId: props.sessionId,
      callerKey: props.callerKey ?? "",
      routeValues: props.routeValues ?? [],
    }, null, 2);
    if (!copy(content)) return;
    setSessionIdCopied(true);
  };

  const handleFork = async () => {
    if (!props.onFork || forking()) return;
    setForking(true);
    try {
      await props.onFork(props.runId);
    } finally {
      setForking(false);
    }
  };

  return (
    <div class="agent-ui-feedback-bar">
      <div class="agent-ui-feedback-actions">
        <Show when={!!props.onSubmit}>
          <button
            type="button"
            class="agent-ui-feedback-btn"
            classList={{ "is-active": isLiked() }}
            disabled={submitting()}
            title="点赞"
            aria-label="点赞"
            onClick={handleLike}
          >
            <Show when={isLiked()} fallback={<IconMdiThumbUpOutline width="16" height="16" />}>
              <IconMdiThumbUp width="16" height="16" />
            </Show>
          </button>
          <button
            type="button"
            class="agent-ui-feedback-btn"
            classList={{ "is-active": isDisliked() }}
            disabled={submitting()}
            title="点踩"
            aria-label="点踩"
            onClick={handleDislike}
          >
            <Show when={isDisliked()} fallback={<IconMdiThumbDownOutline width="16" height="16" />}>
              <IconMdiThumbDown width="16" height="16" />
            </Show>
          </button>
          <button
            type="button"
            class="agent-ui-feedback-btn agent-ui-feedback-problem-btn"
            classList={{ "is-active": showForm() || hasProblem() }}
            disabled={submitting()}
            title="问题反馈"
            aria-label="问题反馈"
            onClick={() => (showForm() ? setShowForm(false) : openForm())}
          >
            <IconMdiCommentQuestionOutline width="16" height="16" />
            <span class="agent-ui-feedback-btn-text">问题反馈</span>
          </button>
        </Show>
        <Show when={!!props.copyContent?.trim()}>
          <button
            type="button"
            class="agent-ui-feedback-btn agent-ui-feedback-copy-btn"
            title={contentCopied() ? "已复制" : "复制"}
            aria-label={contentCopied() ? "已复制" : "复制"}
            onClick={handleCopy}
          >
            <IconIcRoundContentCopy width="16" height="16" />
            <span class="agent-ui-feedback-btn-text">{contentCopied() ? "已复制" : "复制"}</span>
          </button>
        </Show>
        <Show when={!!props.sessionId}>
          <button
            type="button"
            class="agent-ui-feedback-btn agent-ui-feedback-copy-id-btn"
            title={sessionIdCopied() ? "已复制" : "Copy ID"}
            aria-label={sessionIdCopied() ? "已复制 ID" : "Copy ID"}
            onClick={handleCopySessionId}
          >
            <IconMdiBugOutline width="16" height="16" />
            <span class="agent-ui-feedback-btn-text">{sessionIdCopied() ? "已复制" : "Copy ID"}</span>
          </button>
        </Show>
        <Show when={!!props.onFork}>
          <button
            type="button"
            class="agent-ui-feedback-btn agent-ui-feedback-fork-btn"
            disabled={forking()}
            title={forking() ? "分叉中..." : "从这里分叉出新会话"}
            aria-label="从这里分叉出新会话"
            onClick={() => void handleFork()}
          >
            <IconMdiSourceBranch width="16" height="16" />
            <span class="agent-ui-feedback-btn-text">分叉</span>
          </button>
        </Show>
      </div>

      <Show when={showForm()}>
        <div class="agent-ui-feedback-form">
          <textarea
            class="agent-ui-feedback-textarea"
            placeholder="描述你遇到的问题（可选，最多 500 字）"
            maxlength={PROBLEM_FEEDBACK_MAX}
            value={draft()}
            disabled={submitting()}
            onInput={(event) => setDraft(event.currentTarget.value)}
          />
          <div class="agent-ui-feedback-form-footer">
            <span class="agent-ui-feedback-count">{draft().length}/{PROBLEM_FEEDBACK_MAX}</span>
            <div class="agent-ui-feedback-form-actions">
              <button
                type="button"
                class="agent-ui-feedback-cancel"
                disabled={submitting()}
                onClick={() => setShowForm(false)}
              >
                取消
              </button>
              <button
                type="button"
                class="agent-ui-feedback-submit"
                disabled={submitting()}
                onClick={submitProblem}
              >
                提交
              </button>
            </div>
          </div>
        </div>
      </Show>
    </div>
  );
}
