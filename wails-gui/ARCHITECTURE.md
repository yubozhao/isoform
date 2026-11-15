# Wails GUI Architecture

Yansu Desktop is a [Wails v2](https://wails.io/) application that wraps the `yansu-agent` as a native macOS desktop app. It provides a chat-style interface for interacting with AI coding agents (Claude, Codex) while managing projects, git worktrees, and knowledge synchronization.

## High-Level Overview

```
┌─────────────────────────────────────────────────────────┐
│                    Yansu.app (macOS)                     │
│                                                         │
│  ┌──────────────┐   Wails Bindings    ┌──────────────┐  │
│  │   Frontend    │ ◄════════════════► │  Go Backend   │  │
│  │  React + TS   │   EventsEmit /     │  (app.go)     │  │
│  │  (WebView)    │   Function Calls   │               │  │
│  └──────┬───────┘                     └──────┬───────┘  │
│         │                                     │          │
│         │ xterm.js / WebSocket                │ HTTP     │
│         ▼                                     ▼          │
│  ┌──────────────┐                     ┌──────────────┐  │
│  │  Terminal PTY │                     │  Anthropic    │  │
│  │  (per project)│                     │  Messages API │  │
│  └──────────────┘                     └──────────────┘  │
│                                                         │
│  ┌──────────────┐                                        │
│  │  SQLite DB    │                                        │
│  │  (persistence)│                                        │
│  └──────────────┘                                        │
└─────────────────────────────────────────────────────────┘
```

## Directory Structure

```
wails-gui/
├── main.go                  # Wails app entry point — creates window, binds App struct
├── app.go                   # Core App struct — lifecycle, state, method dispatch
├── streaming.go             # Prompt execution, event streaming, context injection
├── checkpoint.go            # File backup/restore for rewind support
├── models.go                # Model discovery (Claude, Codex CLI detection)
├── project.go               # Project detection, Yansu API integration, knowledge sync
├── worktree.go              # Git worktree CRUD, workspace settings
├── persistence.go           # Load/save projects, sessions, stream items (SQLite wrapper)
├── terminal.go              # PTY terminal creation, resize, lifecycle
├── auth.go                  # GitHub OAuth login/logout flow
├── wails.json               # Wails build configuration
│
├── storage/                 # SQLite persistence layer
│   └── storage.go           # DB connection, tables, queries (projects, sessions, items, checkpoints)
│
├── internal/terminal/       # Terminal session manager
│   └── manager.go           # PTY process management, WebSocket bridge for xterm.js
│
├── frontend/                # React frontend (Vite + TypeScript + TailwindCSS)
│   ├── src/
│   │   ├── App.tsx          # Root component — composes all hooks, renders layout
│   │   ├── main.tsx         # React DOM entry point
│   │   ├── types.ts         # All TypeScript interfaces (Project, Session, StreamItem, etc.)
│   │   ├── hooks/           # Custom hooks — all application state lives here
│   │   │   ├── useAgentStreaming.ts # Subscribes to agent events, updates session state
│   │   │   ├── useComposerInput.ts  # Prompt input, image attachments, send logic
│   │   │   ├── useProjects.ts       # Project/workspace/session CRUD, plan approval
│   │   │   ├── useAuth.ts           # Auth state machine
│   │   │   ├── useTerminal.ts       # Multi-tab terminal management
│   │   │   ├── useSettings.ts       # User preferences (vim mode, etc.)
│   │   │   └── ...
│   │   └── components/      # UI components
│   │       ├── stream-body.tsx              # Conversation display with infinite scroll
│   │       ├── composer.tsx                 # Prompt input + model selector + toggles
│   │       ├── stream-card.tsx              # Individual message rendering
│   │       ├── plan-review-card.tsx         # Plan approval/rejection UI
│   │       ├── permission-question-card.tsx # Interactive permission prompt
│   │       ├── session-tabs.tsx             # Tab bar for sessions
│   │       ├── sidebar.tsx                  # Project/workspace navigation
│   │       ├── terminal-panel.tsx           # xterm.js terminal tabs
│   │       ├── context-panel.tsx            # PRD/scenarios from Yansu API
│   │       ├── changes-panel.tsx            # Git diff viewer
│   │       └── ...
│   ├── wailsjs/             # Auto-generated Wails bindings
│   │   ├── go/main/App.js   # JS wrappers for all Go methods
│   │   ├── go/main/App.d.ts # TypeScript type declarations
│   │   ├── go/models.ts     # Go struct → TS interface mappings
│   │   └── runtime/         # Wails runtime API (EventsOn, EventsEmit, etc.)
│   ├── package.json
│   └── vite.config.ts
│
└── build/                   # Wails build output
    └── bin/Yansu.app        # Final macOS application bundle
```

## Go Backend

### App Struct (`app.go`)

The central object bound to the Wails frontend. All Go methods accessible from JS are methods on `App`.

```go
type App struct {
    ctx         context.Context               // Wails-provided context
    runner      agent.AgentRunner             // native in-process agent runner
    streams     map[string]context.CancelFunc // Active stream cancellation (keyed by run tag)
    activeTags  map[string]bool               // Tracks which sessions are streaming
    store       *storage.Store                // SQLite persistence
    termMgr     *terminalsvc.Manager          // Terminal PTY manager
    // ... auth state, mutex, etc.
}
```

**Lifecycle:**
- `startup(ctx)` — Called by Wails on launch. Initializes PATH, SQLite database, and the agent runner.
- `shutdown(ctx)` — Called on quit. Stops terminals, shuts the agent down, closes the database.

### Streaming (`streaming.go`)

Manages prompt execution through the in-process `NativeAgentRunner` (from the `yansu-agent/agent` package), which drives the Anthropic Messages API directly.

**Key methods:**
| Method | Description |
|--------|-------------|
| `StartAgent()` | Initialize the native agent runner |
| `StopAgent()` | Shutdown runner, cancel all active streams |
| `GetStatus()` | Returns `AgentStatus{running, ready, port, message}` |
| `SendPrompt(prompt, path, model, sessionID, tag, planMode, thinkingEnabled)` | Execute a prompt, stream events to frontend |
| `SendPromptWithImages(...)` | Same as above with image attachments |
| `CancelStream(runID)` / `CancelByTag(tag)` | Cancel an in-flight run |
| `RespondToPermission(sessionID, optionID)` | Unblock a pending permission request |

**Event flow:**
```
SendPrompt() → agent.ExecuteTask() → Anthropic Messages API
                                         │
                    StreamCallback(event) ◄┘
                         │
            Wails EventsEmit("agent:event", AgentEvent)
                         │
              Frontend EventsOn("agent:event") → useAgentStreaming
```

**AgentEvent types:** `run_created`, `assistant`, `tool_use`, `thinking`, `system`, `result`, `error`, `done`, `plan_complete`, `permission_request`, `close`

### Checkpoint System (`checkpoint.go`)

Enables "rewind" — reverting a session to a previous point by restoring file backups.

- Before every `Write`/`Edit` tool execution, the original file is backed up
- `RewindSession(localId, sessionId, projectPath, sortOrder, prompt)` restores files and truncates the session's history
- Backups stored as metadata in SQLite (`checkpoints` table)

### Other Backend Modules

| File | Purpose |
|------|---------|
| `models.go` | Detects installed CLI tools (claude, codex) and returns available models |
| `project.go` | Reads `.something/project.json`, fetches context from Yansu API, knowledge push/pull |
| `worktree.go` | Git worktree create/remove/merge, workspace settings |
| `persistence.go` | Wraps `storage.Store` — loads/saves projects, workspaces, sessions, stream items |
| `terminal.go` | Creates PTY shells, resize, close. Manager emits exit events to frontend |
| `auth.go` | GitHub OAuth flow with cancellation support |

### Storage Layer (`storage/storage.go`)

SQLite database with these tables:

| Table | Contents |
|-------|----------|
| `projects` | Project metadata (name, path, sort order, active workspace/session) |
| `workspaces` | Workspace per project (default + worktree-based) |
| `sessions` | Chat sessions with model, thinking, plan mode settings |
| `stream_items` | Individual messages/events with sort_order for ordering |
| `checkpoints` | File backup records (path, backup path, sort_order) |
| `ui_state` | Active project ID, sidebar state |

## Frontend

### State Architecture

All application state is managed through custom React hooks — no Redux or Zustand. The three core hooks are:

```
App.tsx
 ├── useAuth()              → auth state machine
 ├── useProjects()          → project/workspace/session CRUD
 ├── useAgentStreaming()    → event subscription, stream item updates
 ├── useComposerInput()     → prompt input, send logic
 ├── useTerminal()          → PTY terminal tabs
 └── useSettings()          → user preferences
```

### Data Flow

```
User types prompt
    │
    ▼
useComposerInput.sendPrompt()
    │
    ├─► PersistStreamItem(tag, userItem)     // Save user message to DB
    └─► SendPrompt(prompt, path, model, ...) // Call Go backend
                │
                ▼
        Go backend executes via the native agent runner
                │
        Wails EventsEmit("agent:event", event)
                │
                ▼
useAgentStreaming receives event
    │
    ├─► "run_created"         → set session.streaming = true
    ├─► "assistant"           → accumulate text, debounce flush (3s)
    ├─► "tool_use"            → add tool item to session
    ├─► "thinking"            → add thinking item
    ├─► "plan_complete"       → set awaitingPlanApproval = true
    ├─► "permission_request"  → set session.permissionOptions
    ├─► "result"              → add result item
    ├─► "error"               → add error item
    └─► "done"/"close"        → set session.streaming = false
                │
                ▼
        React re-renders UI
```

### Key Components

| Component | Role |
|-----------|------|
| `App.tsx` | Root layout — sidebar, main area, context/changes/terminal panels |
| `StreamBody` | Renders conversation items, infinite scroll for older messages, rewind buttons |
| `Composer` | Prompt textarea, model selector dropdown, image attachments, thinking/plan toggles |
| `StreamCard` | Single message — markdown rendering, syntax highlighting, tool call display |
| `PlanReviewCard` | Shows plan text with Approve/Reject buttons |
| `PermissionQuestionCard` | Renders interactive permission options as clickable buttons |
| `SessionTabs` | Tab bar for switching between sessions in a workspace |
| `Sidebar` | Project list, workspace tree, session list with drag-reorder |
| `TerminalPanel` | Multi-tab xterm.js terminals, one per project |
| `ContextPanel` | PRD and scenarios fetched from Yansu API |
| `ChangesPanel` | Git diff view for workspace changes |

### Wails Bindings (`frontend/wailsjs/`)

Auto-generated by `wails dev` / `wails build`. Maps every Go method on `App` to a callable JS function:

```typescript
// frontend/wailsjs/go/main/App.js
import { SendPrompt } from '../wailsjs/go/main/App';
await SendPrompt(prompt, workDir, model, sessionId, tag, planMode, thinkingEnabled);

// frontend/wailsjs/runtime/runtime.js
import { EventsOn } from '../wailsjs/runtime/runtime';
EventsOn('agent:event', (event: AgentEvent) => { ... });
```

## Build System

### Building the App

```bash
# One-command dev start (checks prerequisites + wails dev)
wails-gui/scripts/start-wails-gui.sh

# Full production build
wails-gui/scripts/build-wails.sh
```

### Build Steps

1. **Wails Build** (`wails-gui/scripts/build-wails.sh`)
   - Runs `wails build` which:
     - Installs frontend deps (`pnpm install`)
     - Builds frontend (`pnpm run build` → Vite)
     - Compiles Go backend
     - Packages as `Yansu.app`
   - Copies bundled runtimes (bun, ffmpeg, yansu CLI, claude CLI, git) into `Yansu.app/Contents/Resources/` and the sherpa-onnx native libs into `Yansu.app/Contents/Frameworks/` (or alongside `Yansu.exe` on Windows)
   - Clears macOS quarantine attributes

### Development

```bash
# Recommended: centralized launcher with prerequisite checks
wails-gui/scripts/start-wails-gui.sh
```

The dev server starts both the Vite dev server (with HMR) and the Go backend. Wails proxies the frontend and regenerates bindings on Go changes.

## Integration with yansu-agent

The `wails-gui` imports the `yansu-agent/agent` package for:

- **`agent.AgentRunner`** — Interface for executing tasks
- **`agent.NativeAgentRunner`** — Drives the Anthropic Messages API directly, with an in-process tool registry (Read, Write, Edit, Bash, Grep, Glob, WebFetch, WebSearch, NotebookEdit, TodoWrite, plan-mode, BashOutput/KillShell)
- **`agent.TaskInput`** — Prompt execution parameters (prompt, workDir, model, sessionID, etc.)
- **`agent.StreamEvent`** — Streaming event types emitted during execution

## Entity Hierarchy

```
Project (directory-based)
 └── Workspace (default + git worktrees)
      └── Session (chat conversation)
           └── StreamItem (message, tool call, thinking, etc.)
```

- **Project** — Mapped to a directory. Optionally linked to a Yansu cloud project via `.something/project.json`.
- **Workspace** — Each project has a default workspace. Additional workspaces create git worktrees with their own branches.
- **Session** — A conversation with the AI agent. Tracks model, thinking mode, plan mode, and the session ID for resumability.
- **StreamItem** — Individual events within a session (user messages, assistant responses, tool calls, errors). Ordered by `sort_order` from the database.
