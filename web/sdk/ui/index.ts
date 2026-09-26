/**
 * Agent Web SDK - UI 层
 *
 * 基于 SolidJS 的 UI 组件集合。
 * 提供开箱即用的对话界面。
 */

// 核心
export { mountAgentUI } from './mount';
export type {
    AgentInputValue,
    AgentUIHandle,
    FillInputOptions,
    FillInputResult,
    MountAgentUIOptions
} from './mount';
export type { AgentUITheme } from './theme';

// 状态管理
export { createAgentStore } from './store';
export type { AgentStore } from './store';
export { clientToolUI } from './client-tool-ui';

// 组件
export { AgentPanel } from './components/AgentPanel';
export { AsyncTaskResults } from './components/AsyncTaskResults';
export { CodeBlock } from './components/CodeBlock';
export { ContentBlock } from './components/ContentBlock';
export { CustomToolView } from './components/CustomToolView';
export { DisplayFiles } from './components/DisplayFiles';
export { InputArea } from './components/InputArea';
export { InputAttachments } from './components/InputAttachments';
export { MessageItem } from './components/MessageItem';
export { MessageList } from './components/MessageList';
export { PanelMessage } from './components/PanelMessage';
export { PlanRuntimeCard } from './components/PlanRuntimeCard';
export { ScrollArea } from './components/ScrollArea';
export { SessionList } from './components/SessionList';
export { SettingsDrawer } from './components/SettingsDrawer';
export type { SettingsDrawerProps } from './components/SettingsDrawer';
export { SmartScroll } from './components/SmartScroll';
export { ThoughtBlock } from './components/ThoughtBlock';
export { TodoCreateBlock } from './components/TodoCreateBlock';
export { TodoStartBlock } from './components/TodoStartBlock';
export { ToolCallCard } from './components/ToolCallCard';
export { ToolCallView } from './components/ToolCallView';
export { ToolExplore } from './components/ToolExplore';
export { UsageRing } from './components/UsageRing';
export { UsageStats } from './components/UsageStats';
export { InputPartsView } from './editor/InputPartsView';

// 类型
export type {
    AgentAfterSendMeta, AgentPanelProps,
    AgentStartBlockCommand,
    AgentStartBlockContext,
    AgentStartBlockMessage,
    AgentStartBlockState
} from './components/AgentPanel';
export type { AsyncTaskNotice, AsyncTaskResultsProps } from './components/AsyncTaskResults';
export type { ContentBlockProps } from './components/ContentBlock';
export type { CustomToolViewProps } from './components/CustomToolView';
export type { DisplayFileArtifact, DisplayFilesProps } from './components/DisplayFiles';
export type { CodeBlockData, CodeBlockProps } from './components/CodeBlock';
export type { InputAreaProps } from './components/InputArea';
export type { InputAttachment, InputAttachmentsProps } from './components/InputAttachments';
export type { MessageItemProps } from './components/MessageItem';
export type { MessageListProps } from './components/MessageList';
export type { PanelMessageProps } from './components/PanelMessage';
export type { ScrollAreaHandle, ScrollAreaProps, ScrollAreaSize } from './components/ScrollArea';
export type { SessionListProps } from './components/SessionList';
export type { SmartScrollHandle, SmartScrollProps, SmartScrollState } from './components/SmartScroll';
export type { ThoughtBlockProps } from './components/ThoughtBlock';
export type { TodoCreateBlockProps } from './components/TodoCreateBlock';
export type { TodoStartBlockProps } from './components/TodoStartBlock';
export type { ToolCallCardProps } from './components/ToolCallCard';
export type { ToolCallViewProps } from './components/ToolCallView';
export type { UsageRingProps } from './components/UsageRing';
export type { ToolExploreProps } from './components/ToolExplore';
export type { UsageStatsProps } from './components/UsageStats';
export type { InputPartsViewProps } from './editor/InputPartsView';
export { parseAgentInputText, serializeAgentInputParts } from './editor/types';
export type {
    AgentInputPart,
    AgentInputSerializer,
    AgentQuickInsertData,
    AgentQuickInsertItem,
    AgentQuickInsertProtoValue,
    AgentQuickInsertShortcutItem,
    AgentQuickInsertTreeNode,
    AgentQuickInsertTreePicker,
    AgentQuickInsertTreeSelectItem,
    AgentQuickInsertTreeSelection
} from './editor/types';

// 样式
import './styles/global.scss';
