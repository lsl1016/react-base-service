/**
 * 挂载 Agent UI
 *
 * 将 AgentPanel 组件挂载到指定的 DOM 容器。
 * 这是 SDK UI 层的主要入口。
 */

import { createSignal, type JSX } from "solid-js";
import { render } from "solid-js/web";
import type { AgentClient } from "../runtime/agent-client";
import type { RunFeedbackPayload, RunFeedbackState } from "../runtime/types";
import { AgentPanel, type AgentAfterSendMeta, type AgentStartBlockContext } from "./components/AgentPanel";
import type { AgentCommandQuickInsertItem, AgentInputSerializer, AgentQuickInsertItem } from "./editor/types";
import type { AgentInputCommand } from "./input-api";
import type { AgentUIEventHandler } from "./events";
import { AgentUIThemeProvider, type AgentUITheme } from "./theme";
import "./styles/global.scss";

export interface MountAgentUIOptions {
  /** 只读展示模式：隐藏会话操作、交互确认、反馈和输入区，不连接或执行任何动作 */
  readOnly?: boolean;
  /** 初始颜色主题，默认 light */
  theme?: AgentUITheme;
  /** 面板标题 */
  title?: string;
  /** 自定义标题 logo 区域，不传则使用默认 */
  renderTitleLogo?: () => JSX.Element;
  /** 自定义开始对话空态，不传则使用默认 */
  renderStartBlock?: (context: AgentStartBlockContext) => JSX.Element;
  /** 是否启用会话管理入口，默认 true；字段名保留兼容旧配置 */
  showSidebar?: boolean;
  /** 输入框占位文本 */
  placeholder?: string;
  /** / 快捷输入候选项 */
  quickInsertItems?: AgentQuickInsertItem[];
  /**
   * 斜杠命令目录自动拉取，默认 true：挂载后经 client.listCommands() 拉取后端命令目录
   * （内置 + 自定义命令），与 quickInsertItems 合并渲染为「命令」分组；
   * 失败静默降级为无命令分组。传 false 关闭（宿主完全自管候选项）。
   */
  commands?: boolean;
  /** 输入片段转最终发送文本 */
  serializeInput?: AgentInputSerializer;
  /** 点击关闭按钮的回调（不传则不显示关闭按钮） */
  onClose?: () => void;
  /** 点击清理按钮的回调（不传则不显示清理按钮） */
  onClean?: () => void;
  /** 发送消息后的回调 */
  onAfterSend?: (content: string, meta: AgentAfterSendMeta) => void;
  /** 反馈条开关，默认 true；false 时不渲染点赞/点踩/问题反馈 */
  feedback?: boolean;
  /** 附件上传开关，默认 true；false 时隐藏内置附件上传入口 */
  attachmentUpload?: boolean;
  /** 提交某一轮(run)反馈；传入则由宿主接管，不传则 SDK 内部走默认反馈接口 */
  onFeedback?: (runId: string, payload: RunFeedbackPayload) => void | Promise<void>;
  /** SDK 内部用户交互事件，由宿主用于埋点等横切能力 */
  onUIEvent?: AgentUIEventHandler;
}

export type {
    AgentAfterSendMeta,
    AgentStartBlockCommand,
    AgentStartBlockContext,
    AgentStartBlockMessage,
    AgentStartBlockState
} from "./components/AgentPanel";
export type {
  AgentInputValue,
  FillInputOptions,
  FillInputResult,
} from "./input-api";

export interface AgentUIHandle extends AgentInputCommand {
  /** 卸载 UI 并清理资源 */
  unmount: () => void;
  /** 运行时切换颜色主题 */
  setTheme: (theme: AgentUITheme) => void;
  /** 获取当前颜色主题 */
  getTheme: () => AgentUITheme;
  /** 整体替换各轮次反馈状态（用于切换/加载历史会话时回显） */
  setFeedback: (map: Record<string, RunFeedbackState>) => void;
  /** 局部更新某一轮反馈状态（用于乐观更新） */
  patchFeedback: (runId: string, state: RunFeedbackState) => void;
}

/**
 * 将 Agent UI 挂载到 DOM 容器
 *
 * 宿主只需提供一个 div 容器，SDK 负责渲染完整的面板 UI。
 *
 * @param container - 挂载目标 DOM 元素
 * @param client - AgentClient 实例
 * @param options - UI 配置项
 * @returns 包含 unmount 方法的句柄
 *
 * @example
 * ```typescript
 * const handle = mountAgentUI(
 *   document.getElementById('agent-container')!,
 *   agentClient,
 *   { title: '报表助手', onClose: () => closePanel() }
 * );
 *
 * // 清理时调用：
 * handle.unmount();
 * ```
 */
export function mountAgentUI(
  container: HTMLElement,
  client: AgentClient,
  options: MountAgentUIOptions = {}
): AgentUIHandle {
  const [feedbackByRunId, setFeedbackByRunId] = createSignal<Record<string, RunFeedbackState>>({});
  const [theme, setTheme] = createSignal<AgentUITheme>(options.theme ?? 'light');
  const [commandItems, setCommandItems] = createSignal<AgentCommandQuickInsertItem[]>([]);
  const feedbackEnabled = !options.readOnly && (options.feedback ?? true);
  let disposed = false;
  let activeSessionId: string | null = null;
  let inputCommand: AgentInputCommand | null = null;
  let feedbackLoadVersion = 0;

  // 斜杠命令目录：挂载后拉取一次（失败静默降级为空），与宿主 quickInsertItems 合并渲染。
  if (options.commands !== false && !options.readOnly) {
    void (async () => {
      try {
        const resp = await client.listCommands();
        if (disposed) return;
        setCommandItems(
          (resp?.commands ?? []).map((entry) => ({
            id: `command:${entry.source}:${entry.name}`,
            label: `/${entry.name}`,
            group: entry.group || (entry.source === 'custom' ? '自定义命令' : '命令'),
            description: entry.argumentHint ? `${entry.description}（${entry.argumentHint}）` : entry.description,
            kind: 'command' as const,
            command: {
              name: entry.name,
              argumentHint: entry.argumentHint,
              source: entry.source === 'custom' ? ('custom' as const) : ('builtin' as const),
            },
          })),
        );
      } catch {
        // 目录拉取失败不阻塞 UI：无命令分组即可（插件渲染条件按合计非空判定）。
      }
    })();
  }

  const mergedQuickInsertItems = (): AgentQuickInsertItem[] => [
    ...(options.quickInsertItems ?? []),
    ...commandItems(),
  ];

  const setFeedbackMap = (map: Record<string, RunFeedbackState>) => {
    setFeedbackByRunId(map ?? {});
  };

  const patchFeedback = (runId: string, state: RunFeedbackState) => {
    setFeedbackByRunId((prev) => ({ ...prev, [runId]: state }));
  };

  const loadFeedbackForSession = async (sessionId: string | null) => {
    const version = ++feedbackLoadVersion;
    if (!feedbackEnabled || options.onFeedback) {
      return;
    }

    if (!sessionId) {
      setFeedbackMap({});
      return;
    }

    try {
      const map = await client.loadSessionFeedback(sessionId);
      if (!disposed && version === feedbackLoadVersion && activeSessionId === sessionId) {
        setFeedbackMap(map);
      }
    } catch {
      if (!disposed && version === feedbackLoadVersion && activeSessionId === sessionId) {
        setFeedbackMap({});
      }
    }
  };

  const submitFeedback = async (runId: string, payload: RunFeedbackPayload) => {
    if (!runId) return;

    if (options.onFeedback) {
      await options.onFeedback(runId, payload);
      return;
    }

    const prev = feedbackByRunId()[runId] ?? { feedback: 0, problemFeedback: '' };
    const optimistic: RunFeedbackState = {
      feedback: payload.feedback ?? prev.feedback,
      problemFeedback: payload.problemFeedback ?? prev.problemFeedback,
    };
    patchFeedback(runId, optimistic);

    try {
      const next = await client.submitFeedback(runId, payload);
      if (!disposed) {
        patchFeedback(runId, next);
      }
    } catch {
      if (!disposed) {
        patchFeedback(runId, prev);
      }
    }
  };

  const unsubscribe = client.subscribe((state) => {
    if (activeSessionId === state.sessionId) {
      return;
    }
    activeSessionId = state.sessionId;
    void loadFeedbackForSession(state.sessionId);
  });

  const dispose = render(
    () => (
      <AgentUIThemeProvider theme={theme}>
        <div class="agent-ui" data-agent-ui-theme={theme()}>
          <AgentPanel
            client={client}
            readOnly={options.readOnly}
            title={options.title}
            renderTitleLogo={options.renderTitleLogo}
            renderStartBlock={options.renderStartBlock}
            showSidebar={options.showSidebar}
            attachmentUpload={options.attachmentUpload}
            placeholder={options.placeholder}
            quickInsertItems={mergedQuickInsertItems()}
            serializeInput={options.serializeInput}
            onClose={options.onClose}
            onClean={options.onClean}
            onAfterSend={options.onAfterSend}
            bindInputCommand={(command) => {
              inputCommand = command;
            }}
            feedbackByRunId={feedbackByRunId()}
            onFeedback={feedbackEnabled ? submitFeedback : undefined}
            onUIEvent={options.onUIEvent}
          />
        </div>
      </AgentUIThemeProvider>
    ),
    container
  );

  return {
    fillInput: async (input, fillOptions) => {
      if (disposed || !inputCommand) {
        return { status: 'rejected', reason: 'unmounted' };
      }
      return inputCommand.fillInput(input, fillOptions);
    },
    unmount: () => {
      disposed = true;
      inputCommand = null;
      unsubscribe();
      dispose();
    },
    setTheme,
    getTheme: theme,
    setFeedback: setFeedbackMap,
    patchFeedback,
  };
}
