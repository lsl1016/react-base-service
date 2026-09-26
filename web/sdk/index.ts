// 协议类型
export * from './protocol/types';

// WebSocket 客户端
export * from './client/types';
export * from './client/ws-client';

// 会话管理
export * from './session/session-manager';
export * from './session/types';

// 存储
export * from './storage/event-ledger';

// 运行时
export * from './runtime/agent-client';
export * from './runtime/async-task-manager';
export * from './runtime/event-reducer';
export * from './runtime/types';

// UI 输入
export { parseAgentInputText, serializeAgentInputParts } from './ui/editor/types';
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
} from './ui/editor/types';
export type { AgentUICopyMethod, AgentUIEvent, AgentUIEventHandler, NextButtonAction } from './ui/events';

// 工具
export * from './tools/executor';
export * from './tools/registry';
export * from './tools/types';

// UI 层（SolidJS）
export { AsyncTaskResults } from './ui/components/AsyncTaskResults';
export type { AsyncTaskNotice, AsyncTaskResultsProps } from './ui/components/AsyncTaskResults';
export { ScrollArea } from './ui/components/ScrollArea';
export type { ScrollAreaHandle, ScrollAreaProps, ScrollAreaSize } from './ui/components/ScrollArea';
export { SmartScroll } from './ui/components/SmartScroll';
export type { SmartScrollHandle, SmartScrollProps, SmartScrollState } from './ui/components/SmartScroll';
export { mountAgentUI } from './ui/mount';
export { SettingsDrawer } from './ui/components/SettingsDrawer';
export type { SettingsDrawerProps } from './ui/components/SettingsDrawer';
export type {
    AgentAfterSendMeta,
    AgentInputValue,
    AgentStartBlockCommand,
    AgentStartBlockContext,
    AgentStartBlockMessage,
    AgentStartBlockState,
    AgentUIHandle,
    FillInputOptions,
    FillInputResult,
    MountAgentUIOptions
} from './ui/mount';
export type { AgentUITheme } from './ui/theme';
export { createAgentStore } from './ui/store';
export type { AgentStore } from './ui/store';
