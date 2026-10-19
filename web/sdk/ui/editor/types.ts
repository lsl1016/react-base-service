import type { AgentInputPart } from '../../runtime/types';

export type { AgentInputPart } from '../../runtime/types';

export type AgentQuickInsertProtoValue = string | number | boolean;

export interface AgentQuickInsertData {
  tag: string;
  proto: Record<string, AgentQuickInsertProtoValue>;
  [key: string]: unknown;
}

export interface AgentQuickInsertShortcutItem {
  id: string;
  label: string;
  group: string;
  description: string;
  data: AgentQuickInsertData;
  kind?: 'insert';
}

export interface AgentQuickInsertTreeNode<Payload = any> {
  id: string;
  label: string;
  disabled?: boolean;
  selectable?: boolean;
  children?: AgentQuickInsertTreeNode<Payload>[];
  payload?: Payload;
}

export interface AgentQuickInsertTreeSelection<Payload = any> {
  node: AgentQuickInsertTreeNode<Payload>;
  path: AgentQuickInsertTreeNode<Payload>[];
}

export interface AgentQuickInsertTreePicker<Payload = any> {
  multiple: boolean;
  leafOnly?: boolean;
  loadOptions: () => Promise<AgentQuickInsertTreeNode<Payload>[]>;
  toShortcut: (selected: AgentQuickInsertTreeSelection<Payload>[]) => AgentQuickInsertShortcutItem;
  onConfirm?: (selected: AgentQuickInsertTreeSelection<Payload>[]) => void;
  onCancel?: (selected: AgentQuickInsertTreeSelection<Payload>[]) => void;
}

export interface AgentQuickInsertTreeSelectItem<Payload = any> {
  id: string;
  label: string;
  group: string;
  description: string;
  kind: 'tree-select';
  picker: AgentQuickInsertTreePicker<Payload>;
}

// 斜杠命令项（后端命令目录下发，docs/plan/20261013_斜杠命令支持设计方案.md §4.7）：
// 选中后插入 "/name " 纯文本（不是 ShortcutNode 标签），回车原文上送、由后端命令分发层解析。
// 已知退化：纯文本不参与 parseAgentInputText 反解析，跨会话历史输入补全退化为纯 text part。
export interface AgentCommandQuickInsertItem {
  id: string;
  label: string;
  group: string;
  description: string;
  kind: 'command';
  command: {
    name: string;
    argumentHint?: string;
    source?: 'builtin' | 'custom';
  };
}

export type AgentQuickInsertItem =
  | AgentQuickInsertShortcutItem
  | AgentQuickInsertTreeSelectItem
  | AgentCommandQuickInsertItem;

export type AgentInputSerializer = (parts: AgentInputPart[]) => string;

function appendTextPart(parts: AgentInputPart[], text: string) {
  if (!text) return;
  const last = parts[parts.length - 1];
  if (last?.type === 'text') {
    last.text += text;
    return;
  }
  parts.push({ type: 'text', text });
}

function normalizeTagName(tag: string): string {
  return tag.replace(/^<\/?/, '').replace(/\/?>$/, '').trim();
}

function escapeAttribute(value: AgentQuickInsertProtoValue): string {
  return String(value)
    .replace(/&/g, '&amp;')
    .replace(/"/g, '&quot;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

function escapeText(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

function unescapeText(value: string): string {
  return value
    .replace(/&quot;/g, '"')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&amp;/g, '&');
}

export function serializeShortcutPart(part: Extract<AgentInputPart, { type: 'shortcut' }>): string {
  const tagName = normalizeTagName(part.data.tag);
  const attrs = Object.entries(part.data.proto)
    .sort(([keyA], [keyB]) => keyA.localeCompare(keyB))
    .map(([key, value]) => `${key}="${escapeAttribute(value)}"`)
    .join(' ');
  const openTag = attrs ? `<${tagName} ${attrs}>` : `<${tagName}>`;
  return `${openTag}${escapeText(part.label)}</${tagName}>`;
}

function createShortcutPart(item: AgentQuickInsertShortcutItem): Extract<AgentInputPart, { type: 'shortcut' }> {
  return {
    type: 'shortcut',
    id: item.id,
    label: item.label,
    group: item.group,
    description: item.description,
    data: item.data,
  };
}

function buildSerializedCandidates(items: AgentQuickInsertItem[]) {
  return items
    // 仅快捷节点项参与序列化反解析（tree-select 无序列化形态、command 是纯文本无 chip）。
    .filter((item): item is AgentQuickInsertShortcutItem => item.kind !== 'tree-select' && item.kind !== 'command')
    .map((item) => ({ item, token: serializeShortcutPart(createShortcutPart(item)) }))
    .sort((a, b) => b.token.length - a.token.length);
}

const SERIALIZED_SHORTCUT_PATTERN = /^<([A-Za-z][\w.-]*)(\s[^<>]*?)?>([^<>]*)<\/\1>/;
const SERIALIZED_ATTRIBUTE_PATTERN = /([A-Za-z_][\w.-]*)="([^"]*)"/g;

function parseDynamicShortcut(text: string): { part: Extract<AgentInputPart, { type: 'shortcut' }>; length: number } | null {
  const match = SERIALIZED_SHORTCUT_PATTERN.exec(text);
  if (!match) return null;

  const tag = match[1];
  const rawAttributes = match[2]?.trim() ?? '';
  const proto: Record<string, string> = {};
  let consumedAttributes = '';
  for (const attribute of rawAttributes.matchAll(SERIALIZED_ATTRIBUTE_PATTERN)) {
    proto[attribute[1]] = unescapeText(attribute[2]);
    consumedAttributes += consumedAttributes ? ` ${attribute[0]}` : attribute[0];
  }
  if (consumedAttributes !== rawAttributes) return null;

  const label = unescapeText(match[3]);
  const identity = Object.entries(proto)
    .sort(([keyA], [keyB]) => keyA.localeCompare(keyB))
    .map(([key, value]) => `${key}=${value}`)
    .join('&');
  return {
    part: {
      type: 'shortcut',
      id: `serialized:${tag}${identity ? `?${identity}` : ''}`,
      label,
      group: tag,
      description: label,
      data: { tag, proto },
    },
    length: match[0].length,
  };
}

export function parseAgentInputText(text: string, items: AgentQuickInsertItem[] = []): AgentInputPart[] {
  if (!text) return [];

  const candidates = buildSerializedCandidates(items);
  const parts: AgentInputPart[] = [];
  let index = 0;
  while (index < text.length) {
    const matched = candidates.find((candidate) => text.startsWith(candidate.token, index));
    if (matched) {
      parts.push(createShortcutPart(matched.item));
      index += matched.token.length;
      continue;
    }

    const dynamic = parseDynamicShortcut(text.slice(index));
    if (dynamic) {
      parts.push(dynamic.part);
      index += dynamic.length;
      continue;
    }

    appendTextPart(parts, unescapeText(text[index]));
    index += 1;
  }

  return parts;
}

export function serializeAgentInputParts(parts: AgentInputPart[]): string {
  return parts
    .map((part) => (part.type === 'text' ? part.text : serializeShortcutPart(part)))
    .join('')
    .trim();
}
