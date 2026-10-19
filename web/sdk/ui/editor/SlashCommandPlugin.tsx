import { autoUpdate, computePosition, flip, hide, inline, offset, shift, type VirtualElement } from '@floating-ui/dom';
import {
  $createTextNode,
  $getNearestNodeFromDOMNode,
  $getNodeByKey,
  $getSelection,
  $isRangeSelection,
  $isTextNode,
  COMMAND_PRIORITY_HIGH,
  KEY_ARROW_DOWN_COMMAND,
  KEY_ARROW_UP_COMMAND,
  KEY_ENTER_COMMAND,
  KEY_ESCAPE_COMMAND,
  KEY_TAB_COMMAND,
  type LexicalEditor,
  type LexicalNode,
  type NodeKey,
} from 'lexical';
import { createEffect, createMemo, createSignal, For, onCleanup, onMount, Show } from 'solid-js';
import { Portal } from 'solid-js/web';
import IconMdiChevronRight from '~icons/mdi/chevron-right';
import { ScrollArea, type ScrollAreaHandle } from '../components/ScrollArea';
import { useAgentUITheme } from '../theme';
import { $createShortcutNode, $isShortcutNode } from './ShortcutNode';
import { TreeSelectPicker, type TreeSelectPickerHandle } from './TreeSelectPicker';
import type {
  AgentCommandQuickInsertItem,
  AgentQuickInsertItem,
  AgentQuickInsertShortcutItem,
  AgentQuickInsertTreeSelectItem,
  AgentQuickInsertTreeSelection,
  AgentQuickInsertTreeNode,
} from './types';

type SlashTrigger =
  | {
      type: 'text';
      nodeKey: NodeKey;
      startOffset: number;
      endOffset: number;
      query: string;
      rect: DOMRect;
    }
  | {
      type: 'shortcut';
      nodeKey: NodeKey;
      query: string;
      rect: DOMRect;
    };

export interface SlashCommandPluginProps {
  editor: LexicalEditor;
  items: AgentQuickInsertItem[];
  rootElement: HTMLElement;
  onInsert?: (items: AgentQuickInsertShortcutItem[]) => void;
}

function getCaretRect(rootElement: HTMLElement): DOMRect {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0) {
    return rootElement.getBoundingClientRect();
  }

  const range = selection.getRangeAt(0).cloneRange();
  range.collapse(false);
  const rect = range.getBoundingClientRect();
  if (rect.width || rect.height) {
    return rect;
  }

  const rects = range.getClientRects();
  return rects[0] ?? rootElement.getBoundingClientRect();
}

function readSlashTrigger(rootElement: HTMLElement): SlashTrigger | null {
  const selection = $getSelection();
  if (!$isRangeSelection(selection) || !selection.isCollapsed()) {
    return null;
  }

  const anchor = selection.anchor;
  const node = anchor.getNode();
  if (!$isTextNode(node)) {
    return null;
  }

  const textBeforeCursor = node.getTextContent().slice(0, anchor.offset);
  const match = /\/([^\s/]*)$/.exec(textBeforeCursor);
  if (!match) {
    return null;
  }

  const slashStart = textBeforeCursor.length - match[1].length - 1;
  return {
    type: 'text',
    nodeKey: node.getKey(),
    startOffset: slashStart,
    endOffset: anchor.offset,
    query: match[1],
    rect: getCaretRect(rootElement),
  };
}

function getSlashTriggerRect(
  trigger: SlashTrigger,
  editor: LexicalEditor,
  rootElement: HTMLElement
): DOMRect {
  if (trigger.type === 'shortcut') {
    return editor.getElementByKey(trigger.nodeKey)?.getBoundingClientRect() ?? trigger.rect;
  }

  const selection = window.getSelection();
  if (!selection?.focusNode || !rootElement.contains(selection.focusNode)) {
    return trigger.rect;
  }

  return getCaretRect(rootElement);
}

const COLLAPSED_GROUP_ITEM_LIMIT = 3;
const TREE_SUBMENU_OFFSET = -1;

function getElementRect(element: Element | undefined) {
  if (!element) return undefined;
  const rect = element.getBoundingClientRect();
  return {
    left: Math.round(rect.left),
    top: Math.round(rect.top),
    right: Math.round(rect.right),
    bottom: Math.round(rect.bottom),
    width: Math.round(rect.width),
    height: Math.round(rect.height),
  };
}

function logTreeSelectDebug(event: string, details: Record<string, unknown> = {}) {
  console.info(`[agent-ui][tree-select-debug] ${event} ${JSON.stringify(details)}`);
}

interface SlashGroupItem {
  item: AgentQuickInsertItem;
  index: number;
  itemIndexInGroup: number;
}

interface SlashGroup {
  group: string;
  items: AgentQuickInsertItem[];
}

interface VisibleSlashGroup {
  group: string;
  items: SlashGroupItem[];
  hiddenCount: number;
  expanded: boolean;
  toggleIndex: number | null;
}

type SlashSelectableEntry =
  | {
      type: 'item';
      index: number;
      item: AgentQuickInsertItem;
      group: string;
      itemIndexInGroup: number;
    }
  | {
      type: 'toggle';
      index: number;
      group: string;
      expanded: boolean;
    };

type PendingSlashSelection =
  | { type: 'firstExpandedItem'; group: string }
  | { type: 'previousAfterCollapse'; group: string };

function getSearchText(item: AgentQuickInsertItem): string {
  // 命令项没有 data.proto，搜索文本取 label/命令名/argumentHint/分组/描述。
  const values =
    item.kind === 'tree-select' || item.kind === 'command'
      ? item.kind === 'command'
        ? [`/${item.command.name}`, item.command.argumentHint ?? '']
        : []
      : Object.values(item.data.proto).map(String);
  return [item.label, item.group, item.description, ...values].join(' ').toLowerCase();
}

export function dedupeShortcutItems(items: AgentQuickInsertShortcutItem[]): AgentQuickInsertShortcutItem[] {
  const ids = new Set<string>();
  return items.filter((item) => {
    if (ids.has(item.id)) return false;
    ids.add(item.id);
    return true;
  });
}

function dedupeQuickInsertItems(items: AgentQuickInsertItem[]): AgentQuickInsertItem[] {
  const ids = new Set<string>();
  return items.filter((item) => {
    if (ids.has(item.id)) return false;
    ids.add(item.id);
    return true;
  });
}

function groupItems(items: AgentQuickInsertItem[]): SlashGroup[] {
  const groups: SlashGroup[] = [];
  items.forEach((item) => {
    const group = groups.find((candidate) => candidate.group === item.group);
    if (group) {
      group.items.push(item);
      return;
    }

    groups.push({
      group: item.group,
      items: [item],
    });
  });
  return groups;
}

export function SlashCommandPlugin(props: SlashCommandPluginProps) {
  const theme = useAgentUITheme();
  const [trigger, setTrigger] = createSignal<SlashTrigger | null>(null);
  const [activeIndex, setActiveIndex] = createSignal(0);
  const [expandedGroups, setExpandedGroups] = createSignal<Set<string>>(new Set<string>());
  const [pendingSelection, setPendingSelection] = createSignal<PendingSlashSelection | null>(null);
  const [treeSelectItem, setTreeSelectItem] = createSignal<AgentQuickInsertTreeSelectItem>();
  const [treeAnchor, setTreeAnchor] = createSignal<HTMLElement>();
  const [floatingStyle, setFloatingStyle] = createSignal<Record<string, string>>({});
  const [treeFloatingStyle, setTreeFloatingStyle] = createSignal<Record<string, string>>({});
  const selectableElementRefs = new Map<number, HTMLElement>();
  const treeOptionsCache = new WeakMap<AgentQuickInsertTreeSelectItem, AgentQuickInsertTreeNode[]>();
  let floatingRef: HTMLDivElement | undefined;
  let treeFloatingRef: HTMLDivElement | undefined;
  let scrollAreaRef: ScrollAreaHandle | undefined;
  let treePickerRef: TreeSelectPickerHandle | undefined;

  const filteredItems = createMemo(() => {
    const query = trigger()?.query.trim().toLowerCase() ?? '';
    if (!query) return props.items;
    return props.items.filter((item) => getSearchText(item).includes(query));
  });

  const groupedItems = createMemo(() => groupItems(filteredItems()));

  const visibleGroups = createMemo<VisibleSlashGroup[]>(() => {
    const expanded = expandedGroups();
    let visibleIndex = 0;
    return groupedItems().map((group) => {
      const isExpanded = expanded.has(group.group);
      const visibleItems = isExpanded ? group.items : group.items.slice(0, COLLAPSED_GROUP_ITEM_LIMIT);
      const items = visibleItems.map((item, itemIndexInGroup) => ({
        item,
        index: visibleIndex++,
        itemIndexInGroup,
      }));
      const hiddenCount = Math.max(group.items.length - COLLAPSED_GROUP_ITEM_LIMIT, 0);
      return {
        group: group.group,
        items,
        hiddenCount,
        expanded: isExpanded,
        toggleIndex: hiddenCount > 0 ? visibleIndex++ : null,
      };
    });
  });

  const selectableEntries = createMemo<SlashSelectableEntry[]>(() =>
    visibleGroups().flatMap((group) => [
      ...group.items.map((groupItem) => ({
        type: 'item' as const,
        index: groupItem.index,
        item: groupItem.item,
        group: group.group,
        itemIndexInGroup: groupItem.itemIndexInGroup,
      })),
      ...(group.toggleIndex === null ? [] : [{
        type: 'toggle' as const,
        index: group.toggleIndex,
        group: group.group,
        expanded: group.expanded,
      }]),
    ])
  );

  const isOpen = () => Boolean(trigger() && (treeSelectItem() || selectableEntries().length));

  const close = () => {
    setTrigger(null);
    setActiveIndex(0);
    setPendingSelection(null);
    setTreeSelectItem(undefined);
    setTreeAnchor(undefined);
    setExpandedGroups(new Set<string>());
    selectableElementRefs.clear();
  };

  const setSelectableElement = (index: number, element: HTMLElement) => {
    selectableElementRefs.set(index, element);
    const selectedItem = treeSelectItem();
    const entry = selectableEntries().find((candidate) => candidate.index === index);
    if (
      selectedItem
      && entry?.type === 'item'
      && entry.item.id === selectedItem.id
      && treeAnchor() !== element
    ) {
      logTreeSelectDebug('anchor-replaced', {
        itemId: selectedItem.id,
        index,
        anchorConnected: element.isConnected,
        anchorRect: getElementRect(element),
      });
      setTreeAnchor(element);
    }
  };

  const resolveTreeAnchor = (item: AgentQuickInsertTreeSelectItem) => {
    const entry = selectableEntries().find((candidate) =>
      candidate.type === 'item' && candidate.item.id === item.id
    );
    return entry ? selectableElementRefs.get(entry.index) : undefined;
  };

  const restoreEditorFocus = () => {
    props.rootElement.focus();
    props.editor.focus();
  };

  const toggleGroup = (group: string) => {
    const shouldCollapse = expandedGroups().has(group);
    const pendingSelection: PendingSlashSelection = {
      type: shouldCollapse ? 'previousAfterCollapse' : 'firstExpandedItem',
      group,
    };
    setExpandedGroups((current) => {
      const next = new Set<string>(current);
      if (shouldCollapse) {
        next.delete(group);
      } else {
        next.add(group);
      }
      return next;
    });
    setPendingSelection(pendingSelection);
  };

  const selectEntryByIndex = (index: number) => {
    const length = selectableEntries().length;
    if (!length) return;
    setActiveIndex(Math.max(0, Math.min(index, length - 1)));
  };

  const openShortcutTrigger = (element: HTMLElement) => {
    const rect = element.getBoundingClientRect();
    props.editor.read(() => {
      const node = $getNearestNodeFromDOMNode(element);
      if (!$isShortcutNode(node)) return;

      setTrigger({
        type: 'shortcut',
        nodeKey: node.getKey(),
        query: '',
        rect,
      });
      setActiveIndex(0);
      setPendingSelection(null);
      setExpandedGroups(new Set<string>());
      setTreeSelectItem(undefined);
    });
  };

  const moveActive = (delta: number) => {
    const length = selectableEntries().length;
    if (!length) return;
    setActiveIndex((index) => (index + delta + length) % length);
  };

  const insertItems = (items: AgentQuickInsertItem[]) => {
    const current = trigger();
    const uniqueItems = dedupeQuickInsertItems(items);
    if (!current || !uniqueItems.length) return;
    let didInsert = false;

    // 命令项：插入 "/name " 纯文本（不是 ShortcutNode 标签）——回车后原文上送，
    // 由后端命令分发层按首 token 解析执行。shortcut 替换与 text 触发两条路径都要覆盖。
    if (uniqueItems[0].kind === 'command') {
      const commandItems = uniqueItems.filter(
        (item): item is AgentCommandQuickInsertItem => item.kind === 'command',
      );
      const commandText = `${commandItems.map((item) => `/${item.command.name}`).join(' ')} `;
      props.editor.update(() => {
        const node = $getNodeByKey(current.nodeKey);
        if (current.type === 'shortcut') {
          if (!$isShortcutNode(node)) return;
          const textNode = $createTextNode(commandText);
          node.replace(textNode);
          textNode.selectEnd();
          didInsert = true;
          return;
        }
        if (!$isTextNode(node)) return;
        node.spliceText(current.startOffset, current.endOffset - current.startOffset, commandText, true);
        didInsert = true;
      });
      if (didInsert) {
        close();
        restoreEditorFocus();
      }
      return;
    }

    const shortcutItems = uniqueItems as AgentQuickInsertShortcutItem[];
    props.editor.update(() => {
      const node = $getNodeByKey(current.nodeKey);

      if (current.type === 'shortcut') {
        if (!$isShortcutNode(node)) return;
        let cursor: LexicalNode = $createShortcutNode(shortcutItems[0]);
        node.replace(cursor);
        shortcutItems.slice(1).forEach((item) => {
          const separator = $createTextNode(' ');
          cursor.insertAfter(separator);
          const shortcut = $createShortcutNode(item);
          separator.insertAfter(shortcut);
          cursor = shortcut;
        });
        const trailingSpace = $createTextNode(' ');
        cursor.insertAfter(trailingSpace);
        trailingSpace.selectEnd();
        didInsert = true;
        return;
      }

      if (!$isTextNode(node)) return;

      node.spliceText(current.startOffset, current.endOffset - current.startOffset, '', true);
      const selection = $getSelection();
      if (!$isRangeSelection(selection)) return;
      const nodes = shortcutItems.flatMap((item) => [$createShortcutNode(item), $createTextNode(' ')]);
      selection.insertNodes(nodes);
      didInsert = true;
    });

    if (didInsert) props.onInsert?.(shortcutItems);
    close();
    restoreEditorFocus();
  };

  const selectItem = (item: AgentQuickInsertItem, anchor?: HTMLElement) => {
    if (item.kind === 'tree-select') {
      logTreeSelectDebug('open-request', {
        itemId: item.id,
        query: trigger()?.query,
        anchorConnected: anchor?.isConnected,
        anchorInPrimaryMenu: Boolean(anchor && floatingRef?.contains(anchor)),
        anchorRect: getElementRect(anchor),
      });
      setTreeAnchor(anchor);
      setTreeSelectItem(item);
      return;
    }
    insertItems([item]);
  };

  const cancelTreeSelect = (selected: AgentQuickInsertTreeSelection[]) => {
    const item = treeSelectItem();
    item?.picker.onCancel?.(selected);
    setTreeSelectItem(undefined);
    setTreeAnchor(undefined);
    restoreEditorFocus();
  };

  const dismissTreeSelect = (reason: string) => {
    if (!treeSelectItem()) return;
    logTreeSelectDebug('dismiss', {
      reason,
      itemId: treeSelectItem()?.id,
      anchorConnected: treeAnchor()?.isConnected,
      anchorInPrimaryMenu: Boolean(treeAnchor() && floatingRef?.contains(treeAnchor()!)),
    });
    setTreeSelectItem(undefined);
    setTreeAnchor(undefined);
  };

  const confirmTreeSelect = (selected: AgentQuickInsertTreeSelection[]) => {
    const item = treeSelectItem();
    if (!item || !selected.length) return;
    const shortcut = item.picker.toShortcut(selected);
    item.picker.onConfirm?.(selected);
    insertItems([shortcut]);
  };

  const selectActiveEntry = () => {
    const entry = selectableEntries()[activeIndex()];
    if (!entry) return;

    if (entry.type === 'item') {
      selectItem(entry.item, selectableElementRefs.get(entry.index));
      return;
    }

    toggleGroup(entry.group);
  };

  onMount(() => {
    const unregisterUpdate = props.editor.registerUpdateListener(({ editorState }) => {
      editorState.read(() => {
        const currentTrigger = trigger();
        if (currentTrigger?.type === 'shortcut') {
          const node = $getNodeByKey(currentTrigger.nodeKey);
          if ($isShortcutNode(node)) return;
        }

        const nextTrigger = readSlashTrigger(props.rootElement);
        setTrigger(nextTrigger);
        setActiveIndex(0);
      });
    });

    const unregisterDown = props.editor.registerCommand(
      KEY_ARROW_DOWN_COMMAND,
      (event) => {
        if (!isOpen()) return false;
        event?.preventDefault();
        if (treeSelectItem()) return true;
        moveActive(1);
        return true;
      },
      COMMAND_PRIORITY_HIGH
    );

    const unregisterUp = props.editor.registerCommand(
      KEY_ARROW_UP_COMMAND,
      (event) => {
        if (!isOpen()) return false;
        event?.preventDefault();
        if (treeSelectItem()) return true;
        moveActive(-1);
        return true;
      },
      COMMAND_PRIORITY_HIGH
    );

    const unregisterEnter = props.editor.registerCommand(
      KEY_ENTER_COMMAND,
      (event) => {
        if (!isOpen()) return false;
        event?.preventDefault();
        if (treeSelectItem()) return true;
        selectActiveEntry();
        return true;
      },
      COMMAND_PRIORITY_HIGH
    );

    const unregisterTab = props.editor.registerCommand(
      KEY_TAB_COMMAND,
      (event) => {
        if (!isOpen()) return false;
        event?.preventDefault();
        if (treeSelectItem()) return true;
        moveActive(event?.shiftKey ? -1 : 1);
        return true;
      },
      COMMAND_PRIORITY_HIGH
    );

    const unregisterEscape = props.editor.registerCommand(
      KEY_ESCAPE_COMMAND,
      (event) => {
        if (!isOpen()) return false;
        event?.preventDefault();
        if (treeSelectItem()) treePickerRef?.cancel();
        else close();
        return true;
      },
      COMMAND_PRIORITY_HIGH
    );

    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      const path = event.composedPath();
      if (
        (floatingRef && (path.includes(floatingRef) || floatingRef.contains(target)))
        || (treeFloatingRef && (path.includes(treeFloatingRef) || treeFloatingRef.contains(target)))
      ) return;

      const shortcutElement = target instanceof Element
        ? target.closest<HTMLElement>('.agent-ui-shortcut-node')
        : null;
      if (shortcutElement && props.rootElement.contains(shortcutElement) && props.editor.isEditable()) {
        event.preventDefault();
        event.stopPropagation();
        openShortcutTrigger(shortcutElement);
        return;
      }

      if (!props.rootElement.contains(target)) {
        close();
        return;
      }

      if (trigger()?.type === 'shortcut') {
        close();
      }
    };
    document.addEventListener('pointerdown', handlePointerDown, { capture: true });

    onCleanup(() => {
      unregisterUpdate();
      unregisterDown();
      unregisterUp();
      unregisterEnter();
      unregisterTab();
      unregisterEscape();
      document.removeEventListener('pointerdown', handlePointerDown, { capture: true });
    });
  });

  createEffect(() => {
    const pending = pendingSelection();
    const entries = selectableEntries();
    if (!pending || !entries.length) return;

    const target = pending.type === 'firstExpandedItem'
      ? entries.find((entry) =>
        entry.type === 'item' &&
        entry.group === pending.group &&
        entry.itemIndexInGroup >= COLLAPSED_GROUP_ITEM_LIMIT
      )
      : [...entries].reverse().find((entry) =>
        entry.type === 'item' && entry.group === pending.group
      );

    if (target) {
      selectEntryByIndex(target.index);
    }
    setPendingSelection(null);
  });

  createEffect(() => {
    const length = selectableEntries().length;
    const index = activeIndex();
    if (!length) {
      if (index !== 0) setActiveIndex(0);
      return;
    }
    if (index > length - 1) {
      setActiveIndex(length - 1);
    }
  });

  createEffect(() => {
    if (!isOpen()) return;
    if (treeSelectItem()) return;
    const index = activeIndex();

    const scrollFrame = requestAnimationFrame(() => {
      if (treeSelectItem()) return;
      const element = selectableElementRefs.get(index);
      const viewport = scrollAreaRef?.getViewportElement();
      if (!element || !viewport) return;

      const elementRect = element.getBoundingClientRect();
      const viewportRect = viewport.getBoundingClientRect();
      if (elementRect.top < viewportRect.top) {
        viewport.scrollTop -= viewportRect.top - elementRect.top;
      } else if (elementRect.bottom > viewportRect.bottom) {
        viewport.scrollTop += elementRect.bottom - viewportRect.bottom;
      }
    });

    onCleanup(() => {
      cancelAnimationFrame(scrollFrame);
    });
  });

  createEffect(() => {
    const current = trigger();
    if (!current || !floatingRef || !isOpen()) return;

    const reference: VirtualElement = {
      getBoundingClientRect: () => getSlashTriggerRect(current, props.editor, props.rootElement),
      getClientRects: () => [getSlashTriggerRect(current, props.editor, props.rootElement)],
      contextElement: props.rootElement,
    };

    const floatingElement = floatingRef;
    const dispose = autoUpdate(reference, floatingElement, () => {
      computePosition(reference, floatingElement, {
        placement: 'top-start',
        middleware: [inline(), offset(8), shift(), flip()],
      }).then(({ x, y }) => {
        if (!floatingElement.isConnected) return;
        const width = Math.min(320, props.rootElement.getBoundingClientRect().width);
        setFloatingStyle({ left: `${x}px`, top: `${y}px`, width: `${width}px` });
      });
    });

    onCleanup(() => dispose());
  });

  createEffect(() => {
    const item = treeSelectItem();
    const anchor = treeAnchor();
    if (!item) return;
    if (!anchor || !treeFloatingRef) {
      logTreeSelectDebug('position-effect-missing-element', {
        itemId: item.id,
        hasAnchor: Boolean(anchor),
        hasFloatingElement: Boolean(treeFloatingRef),
      });
      return;
    }

    const floatingElement = treeFloatingRef;
    let lastPositionSignature = '';
    const recoverAnchor = (reason: string) => {
      const replacement = resolveTreeAnchor(item);
      if (replacement?.isConnected && replacement !== anchor) {
        logTreeSelectDebug('anchor-recovered', {
          itemId: item.id,
          reason,
          anchorRect: getElementRect(replacement),
        });
        setTreeAnchor(replacement);
        return true;
      }
      dismissTreeSelect(reason);
      return false;
    };
    logTreeSelectDebug('position-effect-start', {
      itemId: item.id,
      anchorConnected: anchor.isConnected,
      floatingConnected: floatingElement.isConnected,
      anchorInPrimaryMenu: Boolean(floatingRef?.contains(anchor)),
      anchorRect: getElementRect(anchor),
    });
    const dispose = autoUpdate(anchor, floatingElement, () => {
      if (!anchor.isConnected) {
        recoverAnchor('anchor-disconnected-before-compute');
        return;
      }
      if (!floatingElement.isConnected) return;
      computePosition(anchor, floatingElement, {
        placement: 'right-start',
        middleware: [
          offset(TREE_SUBMENU_OFFSET),
          flip({ fallbackPlacements: ['left-start'] }),
          shift({ padding: 8 }),
          hide(),
        ],
      }).then(({ x, y, placement, middlewareData }) => {
        if (!anchor.isConnected) {
          recoverAnchor('anchor-disconnected-after-compute');
          return;
        }
        if (!floatingElement.isConnected) return;
        const width = Math.min(320, props.rootElement.getBoundingClientRect().width);
        const referenceHidden = Boolean(middlewareData.hide?.referenceHidden);
        const signature = [Math.round(x), Math.round(y), placement, referenceHidden].join(':');
        if (signature !== lastPositionSignature) {
          lastPositionSignature = signature;
          logTreeSelectDebug('position-result', {
            itemId: item.id,
            x: Math.round(x),
            y: Math.round(y),
            placement,
            referenceHidden,
            anchorConnected: anchor.isConnected,
            anchorInPrimaryMenu: Boolean(floatingRef?.contains(anchor)),
            anchorRect: getElementRect(anchor),
            floatingRect: getElementRect(floatingElement),
          });
        }
        setTreeFloatingStyle({
          left: `${x}px`,
          top: `${y}px`,
          width: `${width}px`,
          visibility: middlewareData.hide?.referenceHidden ? 'hidden' : 'visible',
        });
      }).catch((error: unknown) => {
        console.error(`[agent-ui][tree-select-debug] position-error ${JSON.stringify({
          itemId: item.id,
          message: error instanceof Error ? error.message : String(error),
          anchorConnected: anchor.isConnected,
          floatingConnected: floatingElement.isConnected,
        })}`);
      });
    }, { animationFrame: true });

    onCleanup(() => {
      dispose();
    });
  });

  return (
    <Show when={isOpen()}>
      <>
        <Portal>
          <div
            ref={floatingRef}
            class="agent-ui-slash-menu"
            data-agent-ui-theme={theme()}
            style={floatingStyle()}
            onKeyDown={(event) => {
              if (event.key !== 'Escape') return;
              event.preventDefault();
              event.stopPropagation();
              if (treeSelectItem()) treePickerRef?.cancel();
              else close();
            }}
          >
            <ScrollArea
              ref={(handle) => {
                scrollAreaRef = handle;
              }}
              class="agent-ui-slash-menu-scroll"
              contentClass="agent-ui-slash-menu-content"
              size="sm"
            >
              <For each={visibleGroups()}>
              {(group) => (
                <div class="agent-ui-slash-group">
                  <div class="agent-ui-slash-group-title">{group.group}</div>
                  <For each={group.items}>
                    {(groupItem) => (
                      <div
                        ref={(element) => setSelectableElement(groupItem.index, element)}
                        class="agent-ui-slash-item"
                        classList={{
                          'agent-ui-slash-item-active': groupItem.index === activeIndex(),
                          'agent-ui-slash-item-has-submenu': groupItem.item.kind === 'tree-select',
                        }}
                        role="menuitem"
                        aria-haspopup={groupItem.item.kind === 'tree-select' ? 'menu' : undefined}
                        aria-expanded={groupItem.item.kind === 'tree-select'
                          ? treeSelectItem() === groupItem.item
                          : undefined}
                        onMouseDown={(event) => {
                          event.preventDefault();
                          selectEntryByIndex(groupItem.index);
                          selectItem(groupItem.item, event.currentTarget);
                        }}
                      >
                        <span class="agent-ui-slash-item-label">{groupItem.item.label}</span>
                        <span class="agent-ui-slash-item-desc">{groupItem.item.description}</span>
                        <Show when={groupItem.item.kind === 'tree-select'}>
                          <span class="agent-ui-slash-item-submenu-icon" aria-hidden="true">
                            <IconMdiChevronRight width="16" height="16" />
                          </span>
                        </Show>
                      </div>
                    )}
                  </For>
                  <Show when={group.hiddenCount > 0}>
                    <div
                      ref={(element) => {
                        if (group.toggleIndex !== null) {
                          setSelectableElement(group.toggleIndex, element);
                        }
                      }}
                      class="agent-ui-slash-group-toggle"
                      classList={{ 'agent-ui-slash-item-active': group.toggleIndex === activeIndex() }}
                      onMouseDown={(event) => {
                        event.preventDefault();
                        toggleGroup(group.group);
                      }}
                    >
                      {group.expanded ? '收起' : `显示 ${group.hiddenCount} 个更多`}
                    </div>
                  </Show>
                </div>
              )}
              </For>
            </ScrollArea>
          </div>
        </Portal>
        <Show when={treeSelectItem()} keyed>
            {(item) => (
              <Portal>
                <div
                  ref={(element) => {
                    treeFloatingRef = element;
                    logTreeSelectDebug('floating-mounted', {
                      itemId: item.id,
                      floatingConnected: element.isConnected,
                    });
                  }}
                  class="agent-ui-tree-picker-menu"
                  data-agent-ui-theme={theme()}
                  style={treeFloatingStyle()}
                  onKeyDown={(event) => {
                    if (event.key !== 'Escape') return;
                    event.preventDefault();
                    event.stopPropagation();
                    treePickerRef?.cancel();
                  }}
                >
                  <TreeSelectPicker
                    ref={(handle) => {
                      treePickerRef = handle;
                    }}
                    item={item}
                    initialOptions={treeOptionsCache.get(item)}
                    onOptionsLoaded={(options) => treeOptionsCache.set(item, options)}
                    onCancel={cancelTreeSelect}
                    onConfirm={confirmTreeSelect}
                  />
                </div>
              </Portal>
            )}
        </Show>
      </>
    </Show>
  );
}
