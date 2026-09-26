/**
 * AgentPanel - 完整的面板组件
 *
 * 组合以下子组件：
 * - SessionList: 会话历史浮层
 * - MessageList: 消息列表
 * - InputArea: 输入区域
 * - UsageStats: Token 统计
 */

import { createEffect, createMemo, createSignal, onCleanup, onMount, Show, type JSX } from "solid-js";
import IconAntDesignDisconnectOutlined from "~icons/ant-design/disconnect-outlined";
import IconMaterialSymbolsWifiSharp from "~icons/material-symbols/wifi-sharp";
import IconMdiClose from "~icons/mdi/close";
import IconMdiCog from "~icons/mdi/cog";
import IconMdiHistory from "~icons/mdi/history";
import IconMdiPlus from "~icons/mdi/plus";
import type { AgentClient } from "../../runtime/agent-client";
import type { AskQuestionAnswerContent, ExecutionMode, ReactAttachmentRef, ReactModelInfo, ReactReasoningOptions, UserInputOrigin, UserModelItem } from "../../protocol/types";
import type { RunFeedbackPayload, RunFeedbackState } from "../../runtime/types";
import type { AsyncTaskItem } from "../../session/types";
import type { SessionMeta } from "../../storage/event-ledger";
import type { AgentInputPart, AgentInputSerializer, AgentQuickInsertItem } from "../editor/types";
import { parseAgentInputText, serializeAgentInputParts } from "../editor/types";
import type { AgentInputCommand, AgentInputValue, FillInputOptions, FillInputResult } from "../input-api";
import type { AgentUIEvent, AgentUIEventHandler, NextButtonAction } from "../events";
import { createAgentStore } from "../store";
import type { QueueItem } from "../../session/types";
import { AgentLaneView } from "./AgentLaneView";
import { AsyncTaskResults, type AsyncTaskNotice } from "./AsyncTaskResults";
import { FeedbackArea } from "./FeedbackArea";
import { InputArea } from "./InputArea";
import { MessageList } from "./MessageList";
import { PanelMessage } from "./PanelMessage";
import { QueueStrip } from "./QueueStrip";
import { SessionList } from "./SessionList";
import { SettingsDrawer } from "./SettingsDrawer";
import { groupStepsByAgentLane } from "./lanes";

export type AgentStartBlockMessage =
  | string
  | AgentInputPart[]
  | {
      content: string;
      displayParts?: AgentInputPart[];
    };

export interface AgentStartBlockCommand {
  sendMessage: (message: AgentStartBlockMessage) => void;
  fillInput: (input: string | AgentInputPart[]) => void;
  newSession: () => void;
  close: () => void;
}

export interface AgentStartBlockState {
  isRunning: boolean;
  connected: boolean;
}

export interface AgentStartBlockContext {
  command: AgentStartBlockCommand;
  state: AgentStartBlockState;
}

export interface AgentPanelProps {
  /** AgentClient 实例 */
  client: AgentClient;
  /** 只读展示模式，隐藏所有会改变会话状态的入口 */
  readOnly?: boolean;
  /** 面板标题 */
  title?: string;
  /** 自定义标题 logo 区域，不传则使用默认 */
  renderTitleLogo?: () => JSX.Element;
  /** 自定义开始对话空态，不传则使用默认 */
  renderStartBlock?: (context: AgentStartBlockContext) => JSX.Element;
  /** 是否启用会话管理入口，默认 true；字段名保留兼容旧配置 */
  showSidebar?: boolean;
  /** 是否启用内置附件上传，默认 true */
  attachmentUpload?: boolean;
  /** 输入框占位文本 */
  placeholder?: string;
  /** / 快捷输入候选项 */
  quickInsertItems?: AgentQuickInsertItem[];
  /** 输入片段转最终发送文本 */
  serializeInput?: AgentInputSerializer;
  /** 点击关闭按钮的回调（不传则不显示关闭按钮） */
  onClose?: () => void;
  /** 点击清理按钮的回调（不传则不显示清理按钮） */
  onClean?: () => void;
  /** 发送消息后的回调 */
  onAfterSend?: (content: string, meta: AgentAfterSendMeta) => void;
  /** 将输入控制命令暴露给挂载层；组件卸载时回填 null。 */
  bindInputCommand?: (command: AgentInputCommand | null) => void;
  /** 各轮次(runId)的反馈状态，用于回显点赞/点踩/问题反馈 */
  feedbackByRunId?: Record<string, RunFeedbackState>;
  /** 提交某一轮反馈；不传则不渲染反馈条 */
  onFeedback?: (runId: string, payload: RunFeedbackPayload) => void | Promise<void>;
  /** SDK 内部用户交互事件 */
  onUIEvent?: AgentUIEventHandler;
}

export interface AgentAfterSendMeta {
  inputOrigin: UserInputOrigin;
}

const ASYNC_TASK_NOTIFIED_KEY_PREFIX = "agent-ui:async-task-notified:";
const MAX_NOTIFIED_TASKS_PER_SESSION = 200;

function readNotifiedAsyncTasks(sessionId: string): Set<string> {
  if (typeof sessionStorage === "undefined") return new Set();
  try {
    const value = JSON.parse(sessionStorage.getItem(`${ASYNC_TASK_NOTIFIED_KEY_PREFIX}${sessionId}`) ?? "[]");
    return new Set(Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : []);
  } catch {
    return new Set();
  }
}

function writeNotifiedAsyncTasks(sessionId: string, ids: Set<string>): void {
  if (typeof sessionStorage === "undefined") return;
  try {
    const values = [...ids].slice(-MAX_NOTIFIED_TASKS_PER_SESSION);
    sessionStorage.setItem(`${ASYNC_TASK_NOTIFIED_KEY_PREFIX}${sessionId}`, JSON.stringify(values));
  } catch {
    // 浏览器禁用存储时仅退化为本次组件生命周期内去重。
  }
}

function sortTerminalAsyncTasks(tasks: readonly AsyncTaskItem[]): AsyncTaskItem[] {
  return tasks
    .filter((task) => task.executionStatus === "succeeded" || task.executionStatus === "failed")
    .sort((left, right) => (right.completedAt || right.updatedAt).localeCompare(left.completedAt || left.updatedAt));
}

function buildAsyncTaskNotice(tasks: readonly AsyncTaskItem[]): AsyncTaskNotice {
  const failures = tasks.filter((task) => task.executionStatus === "failed");
  if (tasks.length === 1) {
    const task = tasks[0];
    return {
      status: task.executionStatus === "failed" ? "failed" : "succeeded",
      title: `${task.toolName || "异步任务"}${task.executionStatus === "failed" ? "执行失败" : "已完成"}`,
      detail: task.executionStatus === "failed" ? task.errorMessage : undefined,
    };
  }
  return {
    status: failures.length > 0 ? "failed" : "succeeded",
    title: `${tasks.length} 个异步任务已有结果`,
    detail: failures.length > 0 ? `其中 ${failures.length} 个执行失败` : "全部执行成功",
  };
}

export function AgentPanel(props: AgentPanelProps) {
  const store = createAgentStore(props.client);
  const sessionScope = props.client.getSessionScope?.() ?? { callerKey: "", routeValues: [] };
  const [sessions, setSessions] = createSignal<SessionMeta[]>([]);
  const [panelMessage, setPanelMessage] = createSignal<string>();
  const [showSessionHistory, setShowSessionHistory] = createSignal(false);
  // 多代理泳道视图（P3）：出现 ≥2 条泳道（并行委派）时可切换对话流 ↔ 泳道并排。
  const [laneView, setLaneView] = createSignal(false);
  const laneCount = createMemo(() => groupStepsByAgentLane(store.state.steps).length);
  const [asyncTasks, setAsyncTasks] = createSignal<AsyncTaskItem[]>([]);
  const [showAsyncTaskResults, setShowAsyncTaskResults] = createSignal(false);
  const [asyncTaskNotice, setAsyncTaskNotice] = createSignal<AsyncTaskNotice>();
  const [inputDraft, setInputDraft] = createSignal<{ key: number; parts: AgentInputPart[] }>();
  const [models, setModels] = createSignal<ReactModelInfo[]>([]);
  const configuredModel = () => props.client.getConfiguredModel?.() ?? null;
  const [selectedModel, setSelectedModel] = createSignal<ReactModelInfo | null>(configuredModel());
  // 思考程度三态（off/auto/custom）；默认 auto 与历史行为一致。
  const [reasoning, setReasoning] = createSignal<ReactReasoningOptions>({ mode: 'auto' });
  // 模型配置面板（SettingsDrawer）开关与用户模型刷新触发器。
  const [settingsOpen, setSettingsOpen] = createSignal(false);
  const [userModelsVersion, setUserModelsVersion] = createSignal(0);
  // 用户主动选择 Plan 才进入 Plan Runtime；默认继续保持现有 ReAct 行为。
  const [executionMode, setExecutionMode] = createSignal<ExecutionMode>('react');
  // S3 队列管理：当前会话的排队输入与 steering 自动续跑配置回显
  const [queueItems, setQueueItems] = createSignal<QueueItem[]>([]);
  const [queueAutoDrain, setQueueAutoDrain] = createSignal(true);
  let panelMessageTimer: ReturnType<typeof setTimeout> | undefined;
  let asyncTaskNoticeTimer: ReturnType<typeof setTimeout> | undefined;
  let toolHighlightTimer: ReturnType<typeof setTimeout> | undefined;
  let lastFallbackKey = '';
  const notifiedAsyncTasksInMemory = new Map<string, Set<string>>();

  const showPanelMessage = (message?: string) => {
    if (panelMessageTimer) {
      clearTimeout(panelMessageTimer);
      panelMessageTimer = undefined;
    }
    setPanelMessage(message);
    if (message) {
      panelMessageTimer = setTimeout(() => {
        setPanelMessage(undefined);
        panelMessageTimer = undefined;
      }, 3000);
    }
  };

  const showAsyncTaskNotice = (notice?: AsyncTaskNotice) => {
    if (asyncTaskNoticeTimer) {
      clearTimeout(asyncTaskNoticeTimer);
      asyncTaskNoticeTimer = undefined;
    }
    setAsyncTaskNotice(notice);
    if (notice) {
      asyncTaskNoticeTimer = setTimeout(() => {
        setAsyncTaskNotice(undefined);
        asyncTaskNoticeTimer = undefined;
      }, notice.status === "failed" ? 8000 : 5000);
    }
  };

  const handleAsyncTaskSnapshot = (tasks: readonly AsyncTaskItem[]) => {
    const terminalTasks = sortTerminalAsyncTasks(tasks);
    setAsyncTasks(terminalTasks);
    if (terminalTasks.length === 0) {
      setShowAsyncTaskResults(false);
      return;
    }

    const sessionId = store.state.sessionId;
    if (!sessionId) return;
    let notified = notifiedAsyncTasksInMemory.get(sessionId);
    if (!notified) {
      notified = readNotifiedAsyncTasks(sessionId);
      notifiedAsyncTasksInMemory.set(sessionId, notified);
    }
    const unseen = terminalTasks.filter((task) => !notified!.has(task.toolUseId));
    if (unseen.length === 0) return;
    for (const task of unseen) notified.add(task.toolUseId);
    writeNotifiedAsyncTasks(sessionId, notified);
    showAsyncTaskNotice(buildAsyncTaskNotice(unseen));
  };

  const locateAsyncTask = (toolUseId: string) => {
    const target = [...document.querySelectorAll<HTMLElement>("[data-tool-use-id]")]
      .find((element) => element.dataset.toolUseId === toolUseId);
    setShowAsyncTaskResults(false);
    if (!target) {
      showPanelMessage("当前对话中未找到对应的工具记录");
      return;
    }
    target.scrollIntoView?.({ behavior: "smooth", block: "center" });
    target.classList.add("agent-ui-tool-card-highlight");
    if (toolHighlightTimer) clearTimeout(toolHighlightTimer);
    toolHighlightTimer = setTimeout(() => {
      target.classList.remove("agent-ui-tool-card-highlight");
      toolHighlightTimer = undefined;
    }, 1600);
  };

  onMount(() => {
    if (props.readOnly || typeof props.client.subscribeAsyncTasks !== "function") return;
    const unsubscribe = props.client.subscribeAsyncTasks(handleAsyncTaskSnapshot);
    onCleanup(unsubscribe);
  });

  onCleanup(() => {
    if (panelMessageTimer) clearTimeout(panelMessageTimer);
    if (asyncTaskNoticeTimer) clearTimeout(asyncTaskNoticeTimer);
    if (toolHighlightTimer) clearTimeout(toolHighlightTimer);
  });

  const isRunning = () => store.state.status === "running" || store.state.status === "waiting_client_tool" || store.state.status === "compacting" || store.state.status === "recovering";
  const isRecovering = () => store.state.status === "recovering";

  // ─── S3 队列管理 ─────────────────────────────────────────
  const refreshQueue = async () => {
    if (props.readOnly || typeof props.client.listQueue !== "function") return;
    if (!store.state.sessionId) {
      setQueueItems([]);
      return;
    }
    try {
      const resp = await props.client.listQueue();
      setQueueItems(resp.items ?? []);
      setQueueAutoDrain(resp.autoDrain !== false);
    } catch (error) {
      console.warn("[AgentUI] queue list failed:", error);
    }
  };

  // 队列变化感知：steer 回执（排队/引导/作废）、run 状态翻转（自动续跑消费队首）、
  // 会话切换三类时机各触发一次刷新；刷新幂等且廉价，不做节流。
  createEffect(() => {
    void store.state.sessionId;
    void store.state.steerState;
    void isRunning();
    void refreshQueue();
  });

  const handleQueueEdit = async (id: number, content: string): Promise<boolean> => {
    try {
      const resp = await props.client.updateQueueItem(id, content);
      await refreshQueue();
      return resp.claimed;
    } catch (error) {
      console.warn("[AgentUI] queue update failed:", error);
      return false;
    }
  };

  const handleQueueDelete = async (id: number): Promise<boolean> => {
    try {
      const resp = await props.client.deleteQueueItem(id);
      await refreshQueue();
      return resp.claimed;
    } catch (error) {
      console.warn("[AgentUI] queue delete failed:", error);
      return false;
    }
  };

  const handleQueueReorder = (idList: number[]) => {
    props.client.reorderQueue(idList)
      .then(() => refreshQueue())
      .catch((error) => {
        console.warn("[AgentUI] queue reorder failed:", error);
        void refreshQueue();
      });
  };

  const handleQueueSend = (pendingInputId: string) => {
    props.client.sendQueuedMessage(pendingInputId);
  };

  // plans 空值保护（D7）：宿主手工构造的 AgentState 可能没有 plans 字段，不能因派生 memo 抛错。
  const hasBlockingPlanWait = createMemo(() => Object.values(store.state.plans ?? {}).some((plan) => (
    plan.view.status === 'WAIT_USER_INPUT'
    || plan.view.status === 'WAIT_USER_ACTION'
    || plan.view.status === 'WAIT_EXTERNAL_TASK'
  )));
  const showSessionControls = () => !props.readOnly && (props.showSidebar ?? true);
  const emitUIEvent = (event: AgentUIEvent) => {
    try {
      props.onUIEvent?.(event);
    } catch (error) {
      console.error("Agent UI event handler failed:", error);
    }
  };

  const activeInteraction = createMemo(() => {
    if (!isRunning() || isRecovering()) {
      return undefined;
    }
    const steps = store.state.steps;
    for (let i = steps.length - 1; i >= 0; i--) {
      const step = steps[i];
      for (let j = step.toolCalls.length - 1; j >= 0; j--) {
        const toolCall = step.toolCalls[j];
        if (toolCall.status !== 'waiting') continue;
        if (toolCall.toolName === 'ask_question') return { toolCall };
        const interactionUI = props.client.getRegisteredTool(
          toolCall.toolName,
          toolCall.frontendHint,
        )?.interactionUI;
        const shouldRender = (() => {
          if (!interactionUI?.shouldRender) return true;
          try {
            return interactionUI.shouldRender(toolCall);
          } catch {
            return false;
          }
        })();
        if (interactionUI?.placement === 'feedback' && shouldRender) {
          return { toolCall, renderer: interactionUI.renderer };
        }
      }
    }
    return undefined;
  });
  const activeAskQuestion = createMemo(() => {
    const interaction = activeInteraction();
    return interaction?.toolCall.toolName === 'ask_question'
      ? interaction.toolCall
      : undefined;
  });

  const handleAskQuestionSubmit = (toolUseId: string, content: AskQuestionAnswerContent) => {
    let runId = store.state.currentRunId ?? '';
    let questionCount = content.answers.length;

    for (let i = store.state.steps.length - 1; i >= 0; i--) {
      const step = store.state.steps[i];
      const toolCall = step.toolCalls.find((candidate) => candidate.toolUseId === toolUseId);
      if (!toolCall) continue;

      runId = step.runId || runId;
      questionCount = Array.isArray(toolCall.input.questions)
        ? toolCall.input.questions.length
        : questionCount;
      break;
    }

    emitUIEvent({
      type: 'ask_question_submit',
      sessionId: store.state.sessionId,
      runId,
      toolUseId,
      content,
      skipped: content.skipped === true,
      questionCount,
    });
    props.client.sendAskQuestionAnswer(toolUseId, content);
  };

  const handleToolConfirmSubmit = (toolUseId: string, approved: boolean) => {
    emitUIEvent({
      type: 'tool_confirm_submit',
      sessionId: store.state.sessionId,
      runId: store.state.currentRunId ?? '',
      toolUseId,
      approved,
    });
    props.client.sendToolConfirmAnswer(toolUseId, approved);
  };
  const autocompleteItems = createMemo<AgentInputPart[][]>(() => {
    const seen = new Set<string>();
    const items: AgentInputPart[][] = [];

    for (const step of [...store.state.steps].reverse()) {
      if (step.role !== 'user') continue;

      const parts = step.displayParts?.length
        ? step.displayParts
        : parseAgentInputText(step.content, props.quickInsertItems);
      const key = serializeAgentInputParts(parts);
      if (!key || seen.has(key)) continue;

      seen.add(key);
      items.push(parts);
      if (items.length >= 20) break;
    }

    return items;
  });

  const renderDefaultTitleLogo = () => (
    <div class="agent-ui-panel-title-logo">
      <span class="agent-ui-panel-title-text">{props.title ?? "Agent Web SDK"}</span>
    </div>
  );

  const loadSessions = async () => {
    try {
      const cachedSessions = await props.client.listCachedSessions();
      setSessions(cachedSessions);

      const syncedSessions = await props.client.syncSessions();
      setSessions(syncedSessions);
    } catch (error) {
      console.error("Failed to load sessions:", error);
    }
  };

  createEffect(() => {
    if (showSessionHistory()) {
      loadSessions();
    }
  });

  const userModelToModelInfo = (item: UserModelItem): ReactModelInfo => ({
    modelKey: item.modelKey,
    modelVersion: item.modelVersion,
    displayName: item.modelName,
    contextTokens: item.contextTokens || undefined,
    maxOutputTokens: item.maxOutputTokens || undefined,
    supportThinking: (item.supportThinking ?? 0) === 1,
    modelHash: item.modelHash,
    isUserModel: true,
  });

  // 合并用户/平台自建模型（携带 modelHash，run 走自带 Key/端点）；保留平台模型列表在前。
  const mergeUserModels = (platformModels: ReactModelInfo[], userModels: UserModelItem[]): ReactModelInfo[] => {
    const merged = [...platformModels];
    const seen = new Set(merged.map((item) => `${item.modelKey}\u0000${item.modelVersion}`));
    for (const item of userModels) {
      const key = `${item.modelKey}\u0000${item.modelVersion}`;
      if (seen.has(key)) continue;
      merged.push(userModelToModelInfo(item));
    }
    return merged;
  };

  const loadUserModels = async (platformModels: ReactModelInfo[]) => {
    if (typeof props.client.listUserModels !== 'function') {
      setModels(platformModels);
      return;
    }
    try {
      const userModels = await props.client.listUserModels();
      setModels(mergeUserModels(platformModels, userModels ?? []));
    } catch (error) {
      console.warn('[AgentPanel] 加载用户模型列表失败，仅使用平台模型:', error);
      setModels(platformModels);
    }
  };

  onMount(() => {
    if (typeof props.client.listModels !== 'function') {
      const configured = configuredModel();
      setModels(configured ? [configured] : []);
      setSelectedModel(configured);
      return;
    }
    void props.client.listModels().then(async (response) => {
      const platformModels = response.models ?? [];
      const configured = configuredModel();
      const initial = platformModels.find((item) => configured
        && item.modelKey === configured.modelKey
        && item.modelVersion === configured.modelVersion)
        ?? response.defaultModel
        ?? platformModels[0]
        ?? configured;
      setSelectedModel(initial ?? null);
      await loadUserModels(platformModels);
    }).catch((error) => {
      console.warn('[AgentPanel] 加载模型列表失败，使用宿主默认模型:', error);
      const configured = configuredModel();
      setModels(configured ? [configured] : []);
      setSelectedModel(configured);
    });
  });

  // 模型配置面板保存后刷新合并列表（userModelsVersion 变化触发）。
  createEffect(() => {
    if (userModelsVersion() === 0) return;
    if (typeof props.client.listUserModels !== 'function') return;
    void props.client.listUserModels().then((userModels) => {
      setModels((current) => {
        const platformModels = current.filter((item) => !item.isUserModel);
        return mergeUserModels(platformModels, userModels ?? []);
      });
    }).catch((error) => {
      console.warn('[AgentPanel] 刷新用户模型列表失败:', error);
    });
  });

  createEffect(() => {
    const fallback = store.state.lastModelFallback;
    if (!fallback) return;
    const key = `${store.state.currentRunId ?? ''}:${fallback.fromModelKey}:${fallback.fromModelVersion}:${fallback.toModelKey}:${fallback.toModelVersion}:${fallback.reason}`;
    if (key === lastFallbackKey) return;
    lastFallbackKey = key;
    showPanelMessage(`${fallback.fromModelVersion} 响应异常，本轮已切换至 ${fallback.toModelVersion}`);
  });

  const handleSend = (
    content: string,
    displayParts?: AgentInputPart[],
    attachments?: ReactAttachmentRef[],
    model?: ReactModelInfo,
    inputOrigin: UserInputOrigin = { type: 'manual' },
  ): void | Promise<void> => {
    if (hasBlockingPlanWait()) {
      showPanelMessage("请先处理当前 Plan 的等待项");
      return;
    }
    const effectiveModel = model ?? selectedModel() ?? undefined;
    const result = props.client.run(content, {
      displayParts,
      attachments,
      inputOrigin,
      modelKey: effectiveModel?.modelKey,
      modelVersion: effectiveModel?.modelVersion,
      modelHash: effectiveModel?.modelHash,
      executionMode: executionMode(),
      reasoning: reasoning(),
    });
    props.onAfterSend?.(content, { inputOrigin });
    return result;
  };

  const handleCancel = () => {
    props.client.cancel();
  };

  const handleClean = async () => {
    if (!props.onClean) return;
    if (confirm("清理后当前对话内容将被清空，确认继续吗？")) {
      await props.client.clearConversation();
      props.onClean();
      setShowSessionHistory(false);
      setSessions([]);
    }
  };

  const handleSelectSession = async (sessionId: string) => {
    const previousSessionId = store.state.sessionId;
    await props.client.switchSession(sessionId);
    emitUIEvent({ type: 'session_select', previousSessionId, sessionId });
    setShowSessionHistory(false);
    setShowAsyncTaskResults(false);
    showAsyncTaskNotice(undefined);
  };

  const handleNewSession = () => {
    const previousSessionId = store.state.sessionId;
    const created = props.client.newSession();
    emitUIEvent({ type: 'new_session', previousSessionId, accepted: created });
    if (!created) return;

    setShowSessionHistory(false);
    setShowAsyncTaskResults(false);
    showAsyncTaskNotice(undefined);
  };

  const handleToggleSessionHistory = () => {
    setShowAsyncTaskResults(false);
    setShowSessionHistory((visible) => !visible);
  };

  const normalizeInputParts = (input: string | AgentInputPart[]): AgentInputPart[] => {
    if (typeof input !== "string") {
      return input;
    }
    return [{ type: "text", text: input }];
  };

  const sendStartBlockMessage = (message: AgentStartBlockMessage) => {
    if (isRunning() || !store.state.connected) {
      return;
    }

    if (typeof message === "string") {
      handleSend(message);
      return;
    }

    if (Array.isArray(message)) {
      handleSend(serializeAgentInputParts(message), message);
      return;
    }

    handleSend(message.content, message.displayParts);
  };

  const fillStartBlockInput = (input: string | AgentInputPart[]) => {
    const parts = normalizeInputParts(input);
    if (!parts.length) {
      return;
    }
    setInputDraft((draft) => ({
      key: (draft?.key ?? 0) + 1,
      parts,
    }));
  };

  const fillInput = async (
    input: AgentInputValue,
    options: FillInputOptions = {},
  ): Promise<FillInputResult> => {
    const parts = normalizeInputParts(input);
    // 运行中 submit 不再降级填充：消息走 Steering 准入（排队/引导/拒绝）。
    if (!options.submit) {
      setInputDraft((draft) => ({
        key: (draft?.key ?? 0) + 1,
        parts,
      }));
      return { status: 'filled' };
    }

    const content = (props.serializeInput ?? serializeAgentInputParts)(parts).trim();
    if (!content) {
      return { status: 'rejected', reason: 'empty' };
    }
    if (!store.state.connected) {
      return { status: 'rejected', reason: 'disconnected' };
    }

    await Promise.resolve(handleSend(content, parts));
    setInputDraft((draft) => ({
      key: (draft?.key ?? 0) + 1,
      parts: [],
    }));
    return { status: 'submitted' };
  };

  onMount(() => {
    props.bindInputCommand?.({ fillInput });
  });

  onCleanup(() => {
    props.bindInputCommand?.(null);
  });

  const handleNextButtonClick = (action: NextButtonAction) => {
    if (isRunning() || !store.state.connected) {
      return;
    }
    emitUIEvent({
      type: 'next_button_click',
      sessionId: store.state.sessionId,
      ...action,
    });
    const parts = normalizeInputParts(action.buttonText);
    fillStartBlockInput(parts);
    handleSend(serializeAgentInputParts(parts), parts, undefined, selectedModel() ?? undefined, {
      type: 'next_button',
      ...action,
    });
    setInputDraft((draft) => ({
      key: (draft?.key ?? 0) + 1,
      parts: [],
    }));
  };

  const renderStartBlock = () => {
    return props.renderStartBlock?.({
      command: {
        sendMessage: sendStartBlockMessage,
        fillInput: fillStartBlockInput,
        newSession: handleNewSession,
        close: () => props.onClose?.(),
      },
      state: {
        isRunning: isRunning(),
        connected: store.state.connected,
      },
    });
  };

  return (
    <div class="agent-ui-panel">
      <PanelMessage message={panelMessage()} />
      {/* 模型配置面板（厂商与密钥/模型与参数/上下文与压缩/高级） */}
      <Show when={!props.readOnly}>
        <SettingsDrawer
          open={settingsOpen()}
          onClose={() => setSettingsOpen(false)}
          client={props.client}
          onModelsChanged={() => setUserModelsVersion((version) => version + 1)}
        />
      </Show>
      {/* 头部 */}
      <div class="agent-ui-panel-header">
        <div class="agent-ui-panel-title">
          {props.renderTitleLogo ? props.renderTitleLogo() : renderDefaultTitleLogo()}
        </div>

        <div class="agent-ui-panel-header-right">
          {/* 多代理泳道视图切换（P3）：出现并行委派（≥2 条泳道）时可见 */}
          <Show when={laneCount() > 1}>
            <button
              class="agent-ui-header-icon-btn"
              classList={{ "agent-ui-header-icon-btn-active": laneView() }}
              title={laneView() ? "切换回对话视图" : "切换到多代理泳道视图（按代理 tab 分页查看）"}
              onClick={() => setLaneView((visible) => !visible)}
            >
              <span class="agent-ui-lane-toggle-label">{laneView() ? "对话" : "泳道"}</span>
            </button>
          </Show>
          <Show when={showSessionControls()}>
            <button
              class="agent-ui-header-icon-btn"
              title="新会话"
              disabled={isRunning()}
              onClick={handleNewSession}
            >
              <IconMdiPlus width="18" height="18" />
            </button>
            <button
              class="agent-ui-header-icon-btn"
              classList={{ "agent-ui-header-icon-btn-active": showSessionHistory() }}
              title="历史记录"
              onClick={handleToggleSessionHistory}
            >
              <IconMdiHistory width="18" height="18" />
            </button>
            <button
              class="agent-ui-header-icon-btn"
              classList={{ "agent-ui-header-icon-btn-active": settingsOpen() }}
              title="模型配置"
              onClick={() => setSettingsOpen((visible) => !visible)}
            >
              <IconMdiCog width="18" height="18" />
            </button>
          </Show>
          <Show when={!props.readOnly}>
            <AsyncTaskResults
              tasks={asyncTasks()}
              open={showAsyncTaskResults()}
              notice={asyncTaskNotice()}
              onToggle={() => {
                setShowSessionHistory(false);
                setShowAsyncTaskResults((visible) => !visible);
              }}
              onClose={() => setShowAsyncTaskResults(false)}
              onDismissNotice={() => showAsyncTaskNotice(undefined)}
              onLocate={locateAsyncTask}
            />
          </Show>
          <Show
            when={!props.readOnly}
            fallback={
              <div class="agent-ui-panel-connection">
                <span class="agent-ui-status-text">只读</span>
              </div>
            }
          >
            <div class="agent-ui-panel-connection">
              <Show
                when={store.state.connected}
                fallback={
                  <IconAntDesignDisconnectOutlined
                    width="14"
                    height="14"
                    class="agent-ui-connection-indicator agent-ui-connection-indicator-disconnected"
                  />
                }
              >
                <IconMaterialSymbolsWifiSharp
                  width="14"
                  height="14"
                  class="agent-ui-connection-indicator agent-ui-connection-indicator-connected"
                />
              </Show>
              <span class="agent-ui-status-text">
                {store.state.connected ? "已连接" : "未连接"}
              </span>
            </div>
          </Show>
          {/* <UsageStats usage={store.state.usage} /> */}
          <Show when={props.onClose}>
            <button class="agent-ui-header-btn" onClick={() => props.onClose?.()}>
              <IconMdiClose width="18" height="18" />
            </button>
          </Show>
        </div>
      </div>
      {/* 主内容区域 */}
      <div class="agent-ui-panel-content">
        {/* 聊天区域：多代理泳道视图与对话视图按切换分流（同一份 steps，纯渲染差异） */}
        <div class="agent-ui-panel-chat">
          <Show
            when={!laneView()}
            fallback={
              <AgentLaneView
                steps={store.state.steps}
                isRunning={isRunning()}
                resolveTool={(toolName, frontendHint) => props.client.getRegisteredTool(toolName, frontendHint)}
                onAskQuestionSubmit={props.readOnly ? undefined : handleAskQuestionSubmit}
                onToolConfirmSubmit={props.readOnly ? undefined : handleToolConfirmSubmit}
                quickInsertItems={props.quickInsertItems}
              />
            }
          >
          <MessageList
            steps={store.state.steps}
            sessionId={store.state.sessionId}
            callerKey={sessionScope.callerKey}
            routeValues={sessionScope.routeValues}
            isRunning={isRunning()}
            isCompacting={store.state.status === "compacting"}
            isRecovering={isRecovering()}
            renderStartBlock={props.renderStartBlock ? renderStartBlock : undefined}
            quickInsertItems={props.quickInsertItems}
            onNextButtonClick={props.readOnly ? undefined : handleNextButtonClick}
            onPlanConfirm={props.readOnly ? undefined : (planId) => props.client.confirmPlan(planId)}
            resolveTool={(toolName, frontendHint) => props.client.getRegisteredTool(toolName, frontendHint)}
            onAskQuestionSubmit={props.readOnly ? undefined : handleAskQuestionSubmit}
            onToolConfirmSubmit={props.readOnly ? undefined : handleToolConfirmSubmit}
            activeAskQuestion={props.readOnly ? undefined : activeAskQuestion()}
            plans={store.state.plans}
            onPlanResume={props.readOnly ? undefined : (planExecutionId, waitRequestId, response) => {
              props.client.resumePlanWithResponse(planExecutionId, waitRequestId, response);
            }}
            onPlanRetry={props.readOnly ? undefined : (planExecutionId, stepId) => {
              props.client.retryPlanStep(planExecutionId, stepId);
            }}
            onPlanSkip={props.readOnly ? undefined : (planExecutionId, stepId) => {
              props.client.skipPlanStep(planExecutionId, stepId);
            }}
            onPlanCancel={props.readOnly ? undefined : (planExecutionId) => {
              props.client.cancelPlanExecution(planExecutionId);
            }}
            onLoadPlan={(planExecutionId) => props.client.loadPlanExecutionDetail(planExecutionId)}
            onLoadPlanAttempt={(planExecutionId, stepAttemptId) => props.client.loadPlanStepEvents(planExecutionId, stepAttemptId)}
            feedbackByRunId={props.feedbackByRunId}
            onFeedback={props.onFeedback}
            onCodeCopy={(step, code) => emitUIEvent({
              type: 'code_copy',
              sessionId: store.state.sessionId,
              runId: step.runId,
              stepIndex: step.index,
              language: code.language,
              content: code.content,
              source: 'content',
            })}
            onCodeSelectionCopy={(step, code) => emitUIEvent({
              type: 'code_selection_copy',
              sessionId: store.state.sessionId,
              runId: step.runId,
              stepIndex: step.index,
              language: code.language,
              content: code.content,
              copyMethod: code.copyMethod,
            })}
            onProblemFeedbackOpen={(runId) => emitUIEvent({
              type: 'feedback_open',
              sessionId: store.state.sessionId,
              runId,
              feedbackType: 'problem',
            })}
          />
          </Show>
        </div>

        {/* 历史会话浮层 */}
        <Show when={showSessionHistory()}>
          <div class="agent-ui-session-overlay" onClick={() => setShowSessionHistory(false)}>
            <div class="agent-ui-session-drawer" onClick={(event) => event.stopPropagation()}>
              <SessionList
                sessions={sessions()}
                activeSessionId={store.state.sessionId}
                newSessionDisabled={isRunning()}
                onSelectSession={handleSelectSession}
                onNewSession={handleNewSession}
              />
            </div>
          </div>
        </Show>
      </div>

      {/* 反馈区域 */}
      <Show when={!props.readOnly}>
        <FeedbackArea
          toolCall={activeInteraction()?.toolCall}
          renderer={activeInteraction()?.renderer}
          onAskQuestionSubmit={handleAskQuestionSubmit}
          onToolConfirmSubmit={handleToolConfirmSubmit}
        />
      </Show>

      {/* 输入区域 */}
      <Show when={!props.readOnly}>
        <div class="agent-ui-panel-footer">
          <QueueStrip
            items={queueItems()}
            isRunning={isRunning()}
            connected={store.state.connected}
            autoDrain={queueAutoDrain()}
            onEdit={handleQueueEdit}
            onDelete={handleQueueDelete}
            onReorder={handleQueueReorder}
            onSend={handleQueueSend}
          />
          <InputArea
          isRunning={isRunning()}
          executionMode={executionMode()}
          onExecutionModeChange={setExecutionMode}
          onSend={handleSend}
          models={models()}
          selectedModel={selectedModel()}
          onModelChange={setSelectedModel}
          reasoning={reasoning()}
          onReasoningChange={setReasoning}
          onCancel={handleCancel}
          attachmentUpload={props.attachmentUpload}
          onUploadAttachment={props.attachmentUpload === false ? undefined : (file) => props.client.uploadAttachment(file)}
          onAttachmentUploadSuccess={(file, uploaded) => emitUIEvent({
            type: 'attachment_upload_success',
            sessionId: store.state.sessionId,
            fileId: uploaded.fileId,
            fileName: uploaded.fileName,
            size: uploaded.size,
            mimeType: file.type || undefined,
          })}
          onAttachmentUploadError={showPanelMessage}
          onInputChange={(content, displayParts, isComposing) => emitUIEvent({
            type: 'input_change',
            sessionId: store.state.sessionId,
            content,
            displayParts,
            isComposing,
          })}
          onQuickInsertSelect={(item) => emitUIEvent({
            type: 'quick_insert_select',
            sessionId: store.state.sessionId,
            itemId: item.id,
            label: item.label,
            group: item.group,
            tag: item.data.tag,
          })}
          disabled={!store.state.connected || hasBlockingPlanWait()}
          draft={inputDraft()}
          placeholder={props.placeholder}
          quickInsertItems={props.quickInsertItems}
          autocompleteItems={autocompleteItems()}
          serializeInput={props.serializeInput}
          stats={store.state.lastRunStats}
          />
        </div>
      </Show>

    </div>
  );
}
