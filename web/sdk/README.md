# Agent Web SDK

Complete Agent Web SDK with SolidJS-based UI components for building AI chat interfaces.

## Features

- 🚀 **Full-Featured Agent Client**: WebSocket-based communication with streaming support
- 🎨 **Beautiful UI**: SolidJS components with modern design
- 📦 **Zero Configuration**: Works out of the box
- 🔌 **Extensible**: Easy to customize and extend
- 💾 **Persistent Storage**: IndexedDB-based event ledger for session persistence
- ⚡ **High Performance**: Optimized with SolidJS reactivity
- 🎯 **Type Safe**: Full TypeScript support

## Installation

```bash
# Install the SDK
yarn add @react-agent-web-sdk

# Install peer dependencies
yarn add solid-js vite-plugin-solid
```

## Quick Start

### Basic Usage

```typescript
import { createAgentClient, mountAgentUI } from '@react-agent-web-sdk';

// Create agent client
const agent = createAgentClient({
  baseUrl: 'http://localhost:8080',
  appKey: 'my-app',
  routeValues: ['demo'],
});

// Mount UI to DOM
const container = document.getElementById('agent-container');
const ui = mountAgentUI(container, agent, {
  title: 'AI Assistant',
  showSidebar: true,
  theme: 'dark',
});

ui.setTheme('light');
ui.unmount();
```

### HTML Structure

```html
<!DOCTYPE html>
<html>
<head>
  <title>Agent Chat</title>
  <style>
    #agent-container {
      width: 100%;
      height: 100vh;
      margin:0 auto;
    }
  </style>
</head>
<body>
  <div id="agent-container"></div>
  <script type="module" src="./app.ts"></script>
</body>
</html>
```

## Development Workflow

### Local Development

Run the Go backend and the Vite dev server as two separate processes.

```bash
# Terminal 1: start the Go service from the repository root
# make sure /react-base-service/react/ws is reachable on http://127.0.0.1:8080

# Terminal 2: start the SDK dev server
npm run dev
```

The Vite dev server serves the `examples/` host and proxies `/react-base-service/**` to `http://127.0.0.1:8080`, so UI work keeps HMR while still talking to the local Go ReAct backend.

### Build Output

```bash
npm run build
```

Build output is versioned and emitted to:

- `dist/0.0.1/web-agent.js`
- `dist/0.0.1/web-agent.css`

The Go service embeds and serves these built assets. Build the frontend first, then run `go build` or `go test` if you need the latest SDK assets to be visible from the backend.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                   Agent Web SDK                      │
├─────────────────────────────────────────────────────┤
│                                                       │
│  ┌──────────────────────────────────────────────┐  │
│  │              UI Layer (SolidJS)                │  │
│  │  • AgentPanel                                 │  │
│  │  • MessageList & MessageItem                  │  │
│  │  • InputArea                                  │  │
│  │  • ThoughtBlock & ContentBlock                │  │
│  │  • ToolCallCard                               │  │
│  │  • SessionList & UsageStats                   │  │
│  └──────────────────────────────────────────────┘  │
│                                                       │
│  ┌──────────────────────────────────────────────┐  │
│  │           Core Layer (TypeScript)              │  │
│  │  • AgentClient (WebSocket manager)            │  │
│  │  • EventReducer (state aggregation)           │  │
│  │  • EventLedger (IndexedDB persistence)        │  │
│  │  • SessionManager (HTTP API)                  │  │
│  │  • ClientToolExecutor (tool execution)        │  │
│  └──────────────────────────────────────────────┘  │
│                                                       │
└─────────────────────────────────────────────────────┘
```

## Core Concepts

### Event Sourcing

The SDK uses Event Sourcing pattern:
- **Event Ledger**: Immutable log of all events stored in IndexedDB
- **Event Reducer**: Aggregates events into current state
- **Projection**: Current state is derived by replaying events

### Session Persistence

Sessions are automatically persisted to IndexedDB:
- Auto-restore last session on page load
- Support for multiple sessions
- Sequence number alignment for consistency

### Tool Execution

Two types of tools:
- **Server Tools**: Executed on the backend
- **Client Tools**: Executed in the browser via registered handlers

## API Reference

### createAgentClient

```typescript
function createAgentClient(config: AgentClientConfig): AgentClient

interface AgentClientConfig {
  baseUrl: string;
  appKey: string;
  routeValues?: string[];
  controlContext?: Record<string, unknown>;
  llmContext?: string | Record<string, unknown>;
  hooks?: {
    beforeRun?: (context: {
      userPrompt: string;
      sessionId: string | null;
      inputOrigin?: UserInputOrigin;
    }) => string | Record<string, unknown> | undefined | Promise<string | Record<string, unknown> | undefined>;
  };
  modelKey?: string;
  modelVersion?: string;
  maxSteps?: number;
  heartbeatTimeout?: number;
  reconnect?: boolean;
}
```

`hooks.beforeRun` runs before every user-message run and may return a value or a Promise. The run
waits for an asynchronous hook before sending. Its resolved value becomes the current run's
`llmContext`: strings are sent as text and objects are sent as JSON. Returning `undefined` falls
back to `run()` options and then the client-level `llmContext`.

```typescript
const agent = createAgentClient({
  baseUrl: '/react',
  appKey: 'demo-app',
  hooks: {
    beforeRun() {
      return {
        currentSql: sqlEditor.getValue(),
        currentTab: 'query-1',
      };
    },
  },
});

// Plain text is also supported.
const textAgent = createAgentClient({
  baseUrl: '/react',
  appKey: 'demo-app',
  hooks: {
    beforeRun: () => '<context>User is editing SQL</context>',
  },
});
```

The hook is evaluated before local run state changes. If it throws, the run is not sent. Text in
`llmContext`, including markup such as `<system_prompt>`, remains part of the user message and does
not become a model system message.

### mountAgentUI

```typescript
function mountAgentUI(
  container: HTMLElement,
  client: AgentClient,
  options?: MountAgentUIOptions
): AgentUIHandle

interface MountAgentUIOptions {
  theme?: 'light' | 'dark';
  title?: string;
  showSidebar?: boolean;
  attachmentUpload?: boolean; // Enable built-in attachment upload (default: true)
  onUIEvent?: (event: AgentUIEvent) => void;
}

interface AgentUIHandle {
  fillInput(
    input: string | AgentInputPart[],
    options?: { submit?: boolean },
  ): Promise<
    | { status: 'filled' }
    | { status: 'submitted' }
    | {
        status: 'rejected';
        reason: 'empty' | 'disconnected' | 'unmounted';
      }
  >;
  unmount(): void;
  setTheme(theme: 'light' | 'dark'): void;
  getTheme(): 'light' | 'dark';
}
```

`fillInput()` replaces the visible input draft by default. Pass `{ submit: true }` to send the supplied content immediately through the active `AgentClient`. While a run is active, `submit` is ignored and the content is only filled into the draft, preserving the existing single-run behavior. Immediate submission is rejected when the content is empty, the WebSocket is disconnected, or the UI has been unmounted. Plain strings and existing `AgentInputPart[]` rich-input values are both supported.

```typescript
await ui.fillInput('Explain the current query');
await ui.fillInput('Run the current query', { submit: true });
```

`onUIEvent` exposes structured user actions without coupling the SDK to a specific analytics provider. Event types currently include input changes, new/history session actions, user-message and code copying, code selection copying, successful attachment uploads, opening the problem-feedback form, and `next_button_click`. The next-button event includes `sourceRunId`, `sourceStepIndex`, `buttonText`, and `buttonIndex`; the resulting run also carries the same data in `inputOrigin`. Every event carries the available session/run context; see the exported `AgentUIEvent` discriminated union for the exact payload.

For analytics, the SDK exports a single `track(event, params?)` helper. It is a no-op by default (no external analytics provider); hosts can swap in their own reporting by replacing the implementation in `analytics/index.ts`.

`onAfterSend` receives `(content, meta)`. `meta.inputOrigin.type` is `manual` for regular input and `next_button` for a shortcut button. Existing one-argument handlers remain compatible.

### AgentClient Methods

```typescript
class AgentClient {
  // Connection
  connect(): void;
  disconnect(): void;
  get isConnected(): boolean;

  // Execution
  run(userPrompt: string, options?: RunOptions): void;
  cancel(): void;

  // State
  getState(): AgentState;
  subscribe(listener: (state: AgentState) => void): Unsubscribe;

  // Sessions
  listSessions(filter?: SessionFilter): Promise<Session[]>;
  switchSession(sessionId: string): Promise<void>;
  createSession(): Promise<string>;

  // Tools
  registerTool(tool: ClientTool): this;
  registerTools(tools: ClientTool[]): this;
  getRegisteredTool(name: string, ...aliases: Array<string | undefined>): ClientTool | undefined;
}
```

### Custom ClientTool UI

`ClientTool.ui.type='custom'` supports framework-neutral custom tool rendering. The SDK provides an `HTMLElement` host and plain tool-call snapshots; the renderer owns its framework lifecycle.

`ClientTool.uiPlacement` controls where the tool UI appears. It defaults to `'inner-worker'` (inside `WorkBlock`); use `'root'` to render the tool as an independent root-level block in the turn. Placement works with default, explore, and custom tool UIs.

`ClientTool.interactionUI` adds a second, temporary custom renderer for tools that wait for user input. With `placement: 'feedback'`, the SDK mounts it in the fixed interaction area above the input box while the active tool call has `status: 'waiting'`, then unmounts it when the tool settles, the session changes, or the panel is destroyed. The normal `ui` renderer remains in the message flow and continues to provide the persistent/history card.

Use the optional `shouldRender(toolCall)` predicate when a waiting tool does not always need interaction UI, such as a call that fails input validation before creating its pending user action.

```typescript
agent.registerTool({
  name: 'confirm_write',
  uiPlacement: 'root',
  ui: { type: 'custom', renderer: resultCardRenderer },
  interactionUI: {
    placement: 'feedback',
    renderer: confirmationRenderer,
  },
  execute: waitForUserConfirmation,
});
```

The custom context also exposes SDK-owned lightweight code and diff viewers. They use `highlight.js` for syntax highlighting and `diff` for unified-patch parsing; no editor worker is loaded:

```typescript
context.ui.codeEditor.create(container, {
  value: 'select 1',
  fileName: 'query.sql',
  height: 240,
});

const patch = context.ui.diffEditor.createPatch({
  original: 'select old_column',
  modified: 'select new_column',
  oldFileName: 'current.sql',
  newFileName: 'current.sql',
});

context.ui.diffEditor.create(container, {
  value: patch,
  fileName: 'current.sql',
  height: 320,
  onDiffChange: ({ addedLines, removedLines }) => {
    console.log(`+${addedLines} -${removedLines}`);
  },
});
```

Syntax highlighting is inferred from `fileName`; an explicit `language` overrides that inference. For unified diff input, `diffEditor.create()` can also infer the file name from the `+++` / `---` headers, so `fileName` may be omitted. Both factories are read-only and synchronously return a controller with `ready`, `getValue()`, `setValue()`, `focus()`, `layout()`, and `dispose()`. Custom renderers must call `dispose()` from their own `unmount()` lifecycle. `update()` is only used for state changes within the same `toolUseId`; when the call identity or renderer changes, the SDK unmounts the old view and mounts a new one.

Client tools may return an optional JSON-object `meta` for durable UI state. Unlike `content`, `meta` is never sent to the model. The server stores it alongside the tool result and restores it as `context.toolCall.meta` during history replay. `data` remains local and ephemeral. A single `meta` payload is limited to 1MB and must not contain credentials or other secrets.

```typescript
execute: async () => ({
  content: 'SQL updated',
  meta: {
    version: 1,
    original: 'select old_column',
    modified: 'select new_column',
  },
});
```

```typescript
import type { ClientToolUIRenderer } from '@react-agent-web-sdk';

const renderer: ClientToolUIRenderer = {
  mount(container, context) {
    const render = (nextContext: typeof context) => {
      container.textContent = `${nextContext.toolCall.toolName}: ${nextContext.toolCall.status}`;
    };
    render(context);
    return {
      update: render,
      unmount() {
        container.replaceChildren();
      },
    };
  },
};

agent.registerTool({
  name: 'custom_chart',
  uiPlacement: 'root',
  ui: { type: 'custom', renderer },
  execute: async () => ({ content: 'created' }),
});
```

React adapter:

```tsx
import { createRoot } from 'react-dom/client';
import type { ClientToolUIRenderer } from '@react-agent-web-sdk';

const renderer: ClientToolUIRenderer = {
  mount(container, context) {
    const root = createRoot(container);
    const update = (next) => root.render(<ToolCard toolCall={next.toolCall} />);
    update(context);
    return { update, unmount: () => root.unmount() };
  },
};
```

Vue adapter:

```typescript
import { createApp, h, shallowRef } from 'vue';
import type { ClientToolUIRenderer } from '@react-agent-web-sdk';

const renderer: ClientToolUIRenderer = {
  mount(container, context) {
    const current = shallowRef(context);
    const app = createApp({
      setup: () => () => h(ToolCard, { toolCall: current.value.toolCall }),
    });
    app.mount(container);
    return {
      update: (next) => { current.value = next; },
      unmount: () => app.unmount(),
    };
  },
};
```

React and Vue remain host dependencies; the SDK core does not bundle either framework.

## Customization

### Custom UI Components

Use individual components to build custom layouts:

```typescript
import { 
  createAgentClient,
  createAgentStore,
  MessageList,
  InputArea 
} from '@react-agent-web-sdk/ui';

const agent = createAgentClient({ /* config */ });
const store = createAgentStore(agent);

function CustomLayout() {
  return (
    <div class="custom-layout">
      <MessageList
        steps={store.state.steps}
        isRunning={store.state.status === 'running'}
      />
      <InputArea
        onSend={(text) => agent.run(text)}
        onCancel={() => agent.cancel()}
        isRunning={store.state.status === 'running'}
      />
    </div>
  );
}
```

### Styling

All styles are scoped under `.agent-ui` and use SCSS variables:

```scss
// Override variables before importing styles
$agent-ui-primary: #1890ff;
$agent-ui-bg-primary: #ffffff;
// ... more variables

@import '@react-agent-web-sdk/ui/styles/global.scss';
```

## Examples

See the `examples/` directory for complete examples:

- [Basic Usage](./examples/basic-usage.ts) - Simple integration
- [Custom UI](./examples/custom-ui.tsx) - Custom component composition
- [Demo HTML](./demo.html) - Standalone demo page

## Development

```bash
# Install dependencies
yarn install

# Run tests
yarn test

# Type check
yarn type-check

# Build
yarn build
```

## Browser Support

- Chrome/Edge: 90+
- Firefox: 88+
- Safari: 14+

## License

MIT

## Contributing

Contributions are welcome! Please read our contributing guidelines before submitting PRs.

## Support

For issues and questions:
- Open an issue on GitHub
- Check the documentation
- Review the examples
