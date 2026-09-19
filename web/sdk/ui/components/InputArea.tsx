import { createMemo, createSignal, Show } from "solid-js";
import IconIconoirAttachment from "~icons/iconoir/attachment";
import IconOuiSortUp from "~icons/oui/sort-up";
import IconOuiStopFilled from "~icons/oui/stop-filled";
import type { ChatFileUpload, ExecutionMode, ReactAttachmentRef, ReactModelInfo, RunStats } from "../../protocol/types";
import { RichInputEditor } from "../editor/RichInputEditor";
import type { AgentInputPart, AgentInputSerializer, AgentQuickInsertItem, AgentQuickInsertShortcutItem } from "../editor/types";
import { serializeAgentInputParts } from "../editor/types";
import { InputAttachments, type InputAttachment } from "./InputAttachments";
import { UsageRing } from "./UsageRing";

export const MAX_ATTACHMENT_COUNT = 5;
export const MAX_SINGLE_ATTACHMENT_SIZE = 50 * 1024 * 1024;
export const MAX_TOTAL_ATTACHMENT_SIZE = 50 * 1024 * 1024;
export const SUPPORTED_ATTACHMENT_EXTENSIONS = ["csv", "md", "txt"] as const;

function isSupportedAttachment(file: File): boolean {
  const extension = file.name.split(".").pop()?.toLowerCase();
  return SUPPORTED_ATTACHMENT_EXTENSIONS.some((supported) => supported === extension);
}

export interface InputAreaProps {
  /** 当前是否正在运行 */
  isRunning: boolean;
  /** 当前执行范式；默认 react。 */
  executionMode?: ExecutionMode;
  /** 切换 ReAct / Plan 执行范式。 */
  onExecutionModeChange?: (mode: ExecutionMode) => void;
  /** 用户发送消息时的回调 */
  onSend: (content: string, displayParts?: AgentInputPart[], attachments?: ReactAttachmentRef[], model?: ReactModelInfo) => void;
  /** 前端可选择的模型列表 */
  models?: ReactModelInfo[];
  /** 当前选中的模型 */
  selectedModel?: ReactModelInfo | null;
  /** 切换模型 */
  onModelChange?: (model: ReactModelInfo) => void;
  /** 用户取消时的回调 */
  onCancel: () => void;
  /** 上传附件并返回可用于 ReAct run 的文件元信息 */
  onUploadAttachment?: (file: File) => Promise<ChatFileUpload>;
  /** 附件上传成功 */
  onAttachmentUploadSuccess?: (file: File, uploaded: ChatFileUpload) => void;
  /** 附件选择校验或上传失败；无参数表示清除已有提示 */
  onAttachmentUploadError?: (message?: string) => void;
  /** 是否显示内置附件上传入口，默认 true */
  attachmentUpload?: boolean;
  /** 占位文本 */
  placeholder?: string;
  /** 是否禁用 */
  disabled?: boolean;
  /** 外部回填的输入草稿 */
  draft?: {
    key: number;
    parts: AgentInputPart[];
  };
  /** / 快捷输入候选项 */
  quickInsertItems?: AgentQuickInsertItem[];
  /** 当前会话历史输入补全候选 */
  autocompleteItems?: AgentInputPart[][];
  /** 输入片段转最终发送文本 */
  serializeInput?: AgentInputSerializer;
  /** 最近一轮的 token 统计，用于展示上下文窗口占用与缓存占比 */
  stats?: RunStats | null;
  /** 输入内容发生变化 */
  onInputChange?: (content: string, parts: AgentInputPart[], isComposing: boolean) => void;
  /** 用户选中并插入快捷指令 */
  onQuickInsertSelect?: (item: AgentQuickInsertShortcutItem) => void;
}

export function InputArea(props: InputAreaProps) {
  const [parts, setParts] = createSignal<AgentInputPart[]>([]);
  const [isInputEmpty, setIsInputEmpty] = createSignal(true);
  const [resetKey, setResetKey] = createSignal(0);
  const [attachments, setAttachments] = createSignal<InputAttachment[]>([]);
  let fileInput: HTMLInputElement | undefined;
  let nextAttachmentId = 0;

  const placeholder = () => props.placeholder ?? "输入消息，按 Enter 发送...";
  const serialize = () => props.serializeInput ?? serializeAgentInputParts;
  const content = createMemo(() => serialize()(parts()));
  const hasPendingAttachments = createMemo(() => attachments().some((item) => item.status !== "uploaded"));

  const showUsageRing = createMemo(() => {
    const stats = props.stats;
    return !!stats && stats.maxContextTokens > 0;
  });

  const handleSend = () => {
    const text = content().trim();
    if (text && !props.isRunning && !props.disabled && !hasPendingAttachments()) {
      const attachmentRefs = attachments()
        .filter((item): item is InputAttachment & { uploaded: ChatFileUpload } => item.status === "uploaded" && !!item.uploaded)
        .map((item) => ({ fileId: item.uploaded.fileId, fileName: item.uploaded.fileName }));
      props.onSend(text, parts(), attachmentRefs.length > 0 ? attachmentRefs : undefined, props.selectedModel ?? undefined);
      setParts([]);
      setAttachments([]);
      setIsInputEmpty(true);
      setResetKey((key) => key + 1);
    }
  };

  const uploadFiles = (files: FileList | null) => {
    if (!files || !props.onUploadAttachment) return;
    const selectedFiles = Array.from(files);
    const currentAttachments = attachments();

    if (selectedFiles.some((file) => !isSupportedAttachment(file))) {
      props.onAttachmentUploadError?.("仅支持 CSV、MD、TXT 文件");
      if (fileInput) fileInput.value = "";
      return;
    }

    if (currentAttachments.length + selectedFiles.length > MAX_ATTACHMENT_COUNT) {
      props.onAttachmentUploadError?.(`最多上传 ${MAX_ATTACHMENT_COUNT} 个文件`);
      if (fileInput) fileInput.value = "";
      return;
    }

    if (selectedFiles.some((file) => file.size > MAX_SINGLE_ATTACHMENT_SIZE)) {
      props.onAttachmentUploadError?.("单个文件大小不能超过 50MB");
      if (fileInput) fileInput.value = "";
      return;
    }

    const currentSize = currentAttachments.reduce((total, item) => total + item.size, 0);
    const selectedSize = selectedFiles.reduce((total, file) => total + file.size, 0);
    if (currentSize + selectedSize > MAX_TOTAL_ATTACHMENT_SIZE) {
      props.onAttachmentUploadError?.("文件总大小不能超过 50MB");
      if (fileInput) fileInput.value = "";
      return;
    }

    props.onAttachmentUploadError?.();
    for (const file of selectedFiles) {
      const id = nextAttachmentId += 1;
      setAttachments((current) => [...current, {
        id,
        fileName: file.name,
        size: file.size,
        status: "uploading",
      }]);
      void props.onUploadAttachment(file).then((uploaded) => {
        props.onAttachmentUploadSuccess?.(file, uploaded);
        setAttachments((current) => current.map((item) => item.id === id
          ? { ...item, fileName: uploaded.fileName, size: uploaded.size, status: "uploaded", uploaded }
          : item));
      }).catch((error: unknown) => {
        const message = error instanceof Error ? error.message : "上传失败";
        props.onAttachmentUploadError?.(message);
        setAttachments((current) => current.map((item) => item.id === id
          ? { ...item, status: "error", error: message }
          : item));
      });
    }
    if (fileInput) fileInput.value = "";
  };

  const removeAttachment = (id: number) => {
    setAttachments((current) => current.filter((item) => item.id !== id));
  };

  const handleAction = () => {
    if (props.isRunning) {
      props.onCancel();
    } else {
      handleSend();
    }
  };

  return (
    <div class="agent-ui-input-area">
      <div class="agent-ui-execution-mode-switch" role="tablist" aria-label="执行模式">
        <button
          type="button"
          role="tab"
          aria-selected={(props.executionMode ?? 'react') === 'react'}
          classList={{ "agent-ui-execution-mode-active": (props.executionMode ?? 'react') === 'react' }}
          disabled={props.disabled || props.isRunning}
          onClick={() => props.onExecutionModeChange?.('react')}
          title="直接使用 ReAct 循环执行"
        >
          ReAct
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={props.executionMode === 'plan'}
          classList={{ "agent-ui-execution-mode-active": props.executionMode === 'plan' }}
          disabled={props.disabled || props.isRunning}
          onClick={() => props.onExecutionModeChange?.('plan')}
          title="先生成持久化计划，再按步骤执行"
        >
          Plan
        </button>
      </div>
      <Show when={props.attachmentUpload !== false}>
        <div class="agent-ui-input-attachments-container">
          <InputAttachments
            attachments={attachments()}
            disabled={props.isRunning}
            onRemove={removeAttachment}
          />
        </div>
      </Show>

      <div class="agent-ui-input-wrapper">
        <RichInputEditor
          placeholder={placeholder()}
          disabled={props.disabled}
          resetKey={resetKey()}
          draft={props.draft}
          quickInsertItems={props.quickInsertItems}
          autocompleteItems={props.autocompleteItems}
          onSubmit={handleSend}
          onChange={(nextParts, empty, isComposing) => {
            setParts(nextParts);
            setIsInputEmpty(empty);
            props.onInputChange?.(serialize()(nextParts), nextParts, isComposing);
          }}
          onQuickInsertSelect={props.onQuickInsertSelect}
        />
      </div>

      <div class="agent-ui-input-actions">
        <div class="agent-ui-input-actions-left">
          <Show when={props.attachmentUpload !== false}>
            <input
              ref={fileInput}
              class="agent-ui-input-file-control"
              type="file"
              accept=".csv,.md,.txt"
              multiple
              onChange={(event) => uploadFiles(event.currentTarget.files)}
            />
            <button
              type="button"
              class="agent-ui-input-attachment-button"
              onClick={() => fileInput?.click()}
              disabled={props.disabled || props.isRunning || !props.onUploadAttachment}
              title="上传附件"
              aria-label="上传附件"
            >
              <IconIconoirAttachment width="20" height="20" />
            </button>
          </Show>
        </div>
        <div class="agent-ui-input-actions-right">
          <Show when={(props.models?.length ?? 0) > 0 && props.selectedModel}>
            {(selected) => (
              <label class="agent-ui-model-selector" title="选择模型">
                <span class="agent-ui-model-selector-label">模型</span>
                <select
                  value={`${selected().modelKey}\u0000${selected().modelVersion}`}
                  disabled={props.disabled || props.isRunning}
                  aria-label="选择模型"
                  onChange={(event) => {
                    const next = props.models?.find((item) => `${item.modelKey}\u0000${item.modelVersion}` === event.currentTarget.value);
                    if (next) props.onModelChange?.(next);
                  }}
                >
                  {props.models?.map((item) => (
                    <option value={`${item.modelKey}\u0000${item.modelVersion}`}>{item.displayName}</option>
                  ))}
                </select>
              </label>
            )}
          </Show>
          <Show when={showUsageRing()}>
            <UsageRing
              contextUsedTokens={props.stats!.contextUsedTokens}
              maxContextTokens={props.stats!.maxContextTokens}
              cacheReadTokens={props.stats!.cacheReadTokens}
              inputTokens={props.stats!.inputTokens}
            />
          </Show>
          <button
            class="agent-ui-input-button"
            classList={{
              "agent-ui-button-send": !props.isRunning,
              "agent-ui-button-cancel": props.isRunning,
            }}
            onClick={handleAction}
            disabled={props.disabled || (!props.isRunning && (isInputEmpty() || !content().trim() || hasPendingAttachments()))}
            title={props.isRunning ? "停止" : hasPendingAttachments() ? "请等待附件上传完成" : "发送"}
            aria-label={props.isRunning ? "停止" : "发送"}
          >
            <Show when={props.isRunning} fallback={<IconOuiSortUp width="22" height="22" />}>
              <IconOuiStopFilled width="15" height="15" />
            </Show>
          </button>
        </div>
      </div>
    </div>
  );
}
