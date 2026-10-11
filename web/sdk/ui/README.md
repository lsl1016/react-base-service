# Agent Web SDK UI Layer

SolidJS-based UI components for the Agent Web SDK. Provides a complete, out-of-the-box chat interface with streaming support, tool execution visualization, and session management.

## Features

- 🎨 **Modern Design**: Clean, accessible UI with customizable themes
- ⚡ **Reactive**: Built with SolidJS for optimal performance
- 📦 **Zero Config**: Works out of the box with sensible defaults
- 🔌 **Pluggable**: Easy to integrate into any web application
- 🎯 **Type Safe**: Full TypeScript support

## Quick Start

### Installation

The UI layer is included in the `react-agent-web-sdk` package.

```bash
yarn add solid-js vite-plugin-solid
```

### Basic Usage

```typescript
import { createAgentClient, mountAgentUI } from 'react-agent-web-sdk';

// Create agent client
const agent = createAgentClient({
  baseUrl: 'https://your-api.com',
  appKey: 'my-app',
});

// Mount UI to a DOM element
const container = document.getElementById('agent-container');
if (container) {
  const ui = mountAgentUI(container, agent, {
    title: 'AI Assistant',
    showSidebar: true,
    theme: 'dark',
  });

  ui.setTheme('light');
  
  // Later, to cleanup:
  // ui.unmount();
}
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
    }
  </style>
</head>
<body>
  <div id="agent-container"></div>
  <script type="module" src="./app.ts"></script>
</body>
</html>
```

## Configuration Options

### mountAgentUI Options

```typescript
interface MountAgentUIOptions {
  readOnly?: boolean;        // Hide all controls that can mutate a conversation (default: false)
  title?: string;           // Panel title (default: "AI 助手")
  showSidebar?: boolean;    // Show session sidebar (default: false)
  attachmentUpload?: boolean; // Show attachment upload UI (default: true)
}
```

### AgentClient Configuration

```typescript
interface AgentClientConfig {
  baseUrl: string;          // API base URL
  appKey: string;        // Application identifier
  routeValues?: string[];   // Route parameters for session routing
  modelKey?: string;        // Model identifier
  modelVersion?: string;    // Model version
  maxSteps?: number;        // Max ReAct steps (default: 25)
  heartbeatTimeout?: number; // Heartbeat timeout in ms (default: 30000)
  reconnect?: boolean;      // Enable auto-reconnect (default: true)
}
```

## Components

The UI layer consists of the following components:

### Core Components

- **AgentPanel**: Main container component with header, sidebar, and chat area
- **MessageList**: Displays conversation messages with auto-scroll
- **MessageItem**: Individual message with thinking, content, and tool calls
- **InputArea**: Text input with send/cancel functionality

### UI Components

- **ThoughtBlock**: Collapsible thinking process display
- **ContentBlock**: Markdown content renderer
- **ToolCallCard**: Tool execution status and details
- **UsageStats**: Token usage statistics
- **SessionList**: Session history sidebar

### Custom ClientTool UI

Client tools may use `ui: { type: 'custom', renderer }`. A renderer mounts into an SDK-provided `HTMLElement` and returns `update()` and `unmount()` lifecycle methods. This contract can host React roots, Vue apps, Web Components, or plain DOM without exposing framework-specific JSX through the SDK API. The renderer context includes SDK-owned read-only `ui.codeEditor` and `ui.diffEditor` primitives. They use `highlight.js` plus unified patches parsed by `diff`, without loading a full editor runtime. A tool may return persistent `meta`, which is not sent to the model and is restored as `context.toolCall.meta` for history replay. `uiPlacement` defaults to `inner-worker`; set it to `root` to render any tool UI as an independent turn-level block. See the package README for complete examples.

### Store

- **createAgentStore**: SolidJS store bridge for AgentClient

## Customization

### Styling

All styles are scoped under `.agent-ui` and use SCSS variables for theming. Override variables in your application:

```scss
// variables.scss
$agent-ui-primary: #1890ff;
$agent-ui-bg-primary: #ffffff;
$agent-ui-text-primary: rgba(0, 0, 0, 0.85);
// ... more variables
```

### Custom Components

Use individual components to build custom layouts:

```typescript
import { AgentPanel, MessageList, InputArea } from 'react-agent-web-sdk/ui';
import { createAgentStore } from 'react-agent-web-sdk/ui';

function CustomLayout() {
  const store = createAgentStore(agentClient);
  
  return (
    <div class="custom-layout">
      <MessageList steps={store.state.steps} />
      <InputArea
        onSend={(content) => agentClient.run(content)}
        onCancel={() => agentClient.cancel()}
        isRunning={store.state.status === 'running'}
      />
    </div>
  );
}
```

## API Reference

### mountAgentUI

```typescript
function mountAgentUI(
  container: HTMLElement,
  client: AgentClient,
  options?: MountAgentUIOptions
): AgentUIHandle
```

Mounts the complete agent UI to a DOM container.

**Parameters:**
- `container`: DOM element to mount the UI
- `client`: AgentClient instance
- `options`: UI configuration options

**Returns:**
- Handle with `unmount`, `setTheme`, and `getTheme` methods

### createAgentStore

```typescript
function createAgentStore(client: AgentClient): AgentStore
```

Creates a SolidJS reactive store from an AgentClient.

**Returns:**
- `{ state: AgentState, client: AgentClient }`

## Development

### Project Structure

```
packages/agent-web-sdk/
├── ui/
│   ├── components/          # SolidJS components
│   │   ├── AgentPanel.tsx
│   │   ├── MessageList.tsx
│   │   ├── MessageItem.tsx
│   │   ├── InputArea.tsx
│   │   ├── ThoughtBlock.tsx
│   │   ├── ContentBlock.tsx
│   │   ├── ToolCallCard.tsx
│   │   ├── UsageStats.tsx
│   │   └── SessionList.tsx
│   ├── styles/              # SCSS styles
│   │   ├── variables.scss
│   │   ├── mixins.scss
│   │   ├── global.scss
│   │   └── components/
│   ├── store.ts             # SolidJS store
│   ├── mount.tsx            # Mount function
│   └── index.ts             # UI exports
└── __tests__/               # Tests
```

### Building

The UI is built as part of the main agent-web-sdk package:

```bash
yarn build
```

### Testing

```bash
yarn test packages/agent-web-sdk
```

## Browser Support

- Chrome/Edge: 90+
- Firefox: 88+
- Safari: 14+

## License

MIT
