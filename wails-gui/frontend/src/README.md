# wails-gui/frontend/src

React 18 + TypeScript frontend source for the Yansu GUI desktop application, built with **Wails** (Go + native WebView). This is **not Electron** — the app uses the OS native WebView (WebKit on macOS/Linux, WebView2 on Windows), and all Go↔JS communication goes through Wails bindings and runtime events rather than Node.js IPC.

## Directory Overview

This directory contains the complete frontend application for the Yansu GUI — a desktop AI coding assistant with multi-workspace support, real-time streaming, integrated terminal, Git-aware project management, activity monitoring, knowledge base, memory management, people/identity tracking, handoffs, and cron job automations.

## Directory Structure

```
src/
├── main.tsx              # React 18 entry point
├── App.tsx               # Root component — composes all hooks and renders the full UI
├── types.ts              # Core TypeScript type definitions (app state, permissions, sidebar groups)
├── index.css             # Global TailwindCSS styles and theme variables
├── vite-env.d.ts         # Vite environment type declarations
├── test-setup.ts         # Vitest + @testing-library configuration
├── assets/               # SVG icon components (assets/icons/)
├── components/           # UI components
│   ├── ui/               # Radix UI wrapper primitives
│   ├── icons/            # App and executor icon components
│   ├── onboarding/       # Multi-step onboarding flow (steps/, components/)
│   ├── automations/      # Cron job automation UI (grid, detail pane, schedule utils)
│   ├── timeline/         # Timeline/calendar view components
│   ├── handoffs/         # Handoffs page — file-based work handoff management
│   ├── contacts/         # Contact/principal editor and picker components
│   └── [feature components]
├── hooks/                # 32 custom React hooks
├── lib/                  # Utility modules
│   ├── stream-helpers.ts     # Session/workspace factories and persistence converters
│   ├── tool-icons.ts         # Tool name → icon mappings
│   ├── utils.ts              # `cn()` class merging and general helpers
│   ├── notify.ts             # Notification helper wrappers
│   ├── random-name.ts        # Random project/session name generation
│   ├── summary-content.ts    # Content summarization helpers
│   ├── platform.ts           # Platform detection (isMac, isWindows)
│   ├── line-diff.ts          # LCS-based line-level diff (memory entry content comparison)
│   ├── chat-error.ts         # Chat error classification and resolution helpers
│   ├── browser-bridge.ts     # Security context shims for running the UI in a regular browser
│   ├── wails-shim-app.ts     # Shims for Wails App bindings (used in browser mode)
│   ├── wails-shim-models.ts  # Shims for Wails model types (used in browser mode)
│   └── wails-shim-runtime.ts # Shims for Wails runtime events (used in browser mode)
├── i18n/                 # Internationalization
│   ├── index.ts              # i18next initialization
│   ├── i18next.d.ts          # TypeScript augmentation for typed t() keys
│   └── locales/
│       ├── en/               # English (source of truth)
│       └── zh-CN/            # Chinese translations
└── themes/               # Theme system
    ├── registry.ts       # Theme registry and default export
    ├── paper.ts          # "Light" theme (warm parchment, default light)
    ├── onedark.ts        # "Dark" theme (Atom One Dark-inspired, default dark)
    └── types.ts          # Theme type definitions
```

## Architecture

### Single-Page, State-Driven Navigation

There is no React Router. Navigation is driven entirely by state:

- `activeProjectId` — which project is visible
- `activeWorkspaceId` — which workspace (main branch or git worktree) is active
- `activeSessionId` — which conversation tab is displayed
- `appMode` — top-level view: `chat` | `automations` | `handoffs`

Modals and dialogs (Activity, Knowledge, Memory, People, Timeline, Settings) are toggled via boolean flags and rendered as Radix Dialog overlays.

### Data Hierarchy

```
Project
└── Workspace[]               # Default + git worktree workspaces
    ├── Session[]              # Conversation tabs
    │   └── StreamItem[]       # Messages: user | assistant | thinking | tool | error
    └── Terminal tabs          # Per-workspace xterm.js state
```

### Hook-Based State Management

No external state library (no Redux, Jotai, Zustand). All state lives in custom hooks composed in `App.tsx`:

| Hook | Responsibility |
|---|---|
| `useProjects()` | Project/workspace/session CRUD with backend persistence |
| `useAgentStreaming()` | Real-time agent event handling with 150ms batch flush |
| `useComposerInput()` | Composer input, model selection, message queuing during streaming, send logic |
| `useComposerSuggestions()` | AI-powered input suggestion proposals for the composer |
| `useComposerDockSlide()` | FLIP animation that slides the composer from the centered welcome layout to the docked chat layout on first message |
| `useTerminal()` | Per-workspace terminal tab lifecycle |
| `useAuth()` | GitHub OAuth login state and env override detection |
| `useSettings()` | Settings dialog and vim mode toggle |
| `useKeybindings()` | Global keybinding registry |
| `useTheme()` | Theme preference (paper / onedark / system) |
| `useSidebarResize()` | Sidebar width persistence |
| `useResizable()` | Generic resizable panel with localStorage key |
| `usePRInfo()` | Fetch PR info and build PR creation URL for the active workspace (currently disabled) |
| `useVimScroll()` | Vim-style j/k scrolling in message area |
| `useUserProfile()` | Fetch and cache authenticated user profile (module-level cache, single API call) |
| `useProductTelemetry()` | Anonymous foreground activity through the backend telemetry gate |
| `useFirstBriefSeen()` | Counts the first brief entering the focused viewport without content |
| `useVoiceInput()` | Whisper voice input integration for composer |
| `usePermissions()` | Platform permission checks (microphone, notifications, etc.) |
| `useEnvironmentSwitch()` | Dev/prod environment switching |
| `useInsightNotification()` | Insight event notifications |
| `useCronJobs()` | Cron job automation state management |
| `useSidebarGroups()` | Sidebar group CRUD — create, rename, delete, reorder, and collapse groups |
| `useConnectedPlatforms()` | Connected platform status (Slack, Discord, Telegram, Feishu, WeChat) |
| `useShowToolSteps()` | Toggles tool input/result details in chat independently of debug mode |
| `useDevToolsUnlock(defaultEnabled)` | On/off switch for Settings > Developer tools (Cmd+Shift+Option+D); on by default in staging and dev builds |
| `useHandoffNotification()` | Handoff proposal notifications with AFK digest queue |

### Wails Bridge

The frontend communicates with the Go backend through generated bindings in `../wailsjs/go/main/`. A custom **Vite plugin** in `vite.config.ts` intercepts all `wailsjs/` imports at build time and redirects them to shim modules in `lib/` (`wails-shim-app.ts`, `wails-shim-runtime.ts`, `wails-shim-models.ts`). Each shim checks `window.__wails` at runtime — using native Wails bindings if available, or falling back to HTTP via `lib/browser-bridge.ts`. This single-artifact strategy lets the UI run in both the Wails WebView and a standard browser without separate builds.

Key binding categories:

- **Auth** — `CheckAuth`, `Login`, `CancelLogin`, `Logout`, `HasEnvOverride`, `HasSavedToken`
- **Streaming** — `StartAgent`, `RestartAgent`, `CancelStream`, `CancelByTag`, `RespondToPermission`, `CancelPermission`, `EnsureTranscriptChatMessage`
- **Project persistence** — `LoadAllProjects`, `LoadActiveProjectID`, `SaveActiveProjectID`, `SaveProjectState`, `DeleteProjectData`, `ReorderProjects`, `NextProjectSortOrder`
- **Workspace persistence** — `SaveWorkspaceState`, `DeleteWorkspaceData`, `CreateWorkspace`, `RemoveWorkspace`, `MergeWorkspace`, `ReorderWorkspaces`, `UpdateProjectActiveWorkspace`, `UpdateWorkspaceActiveSession`, `UpdateProjectActiveSession`
- **Session persistence** — `SaveSessionState`, `DeleteSessionData`, `PersistStreamItem`, `LoadStreamItemsPage`, `RewindSession`
- **UI state** — `LoadUIState`, `SaveUIState`, `LoadWindowState`, `SaveWindowState`
- **Project detection** — `DetectProject`, `FetchProjectContext`
- **File system** — `SelectWorkDir`, `OpenProjectFolder`, `OpenProjectURL`, `OpenInApp`, `DetectInstalledApps`
- **Git** — `GetGitInfo`, `CloneFromURL`, `GetPRInfo`, `BuildPRCreateURL`
- **Remote browse** — `BrowseRemote`
- **Terminal** — `CreateTerminal`, `WriteTerminal`, `CloseTerminal`, `ResizeTerminal`
- **Platform status** — `GetSlackStatus`, `GetFeishuStatus`
- **Activity** — `GetActivitySessions`, `GetActivitySessionDetail`, `GetActivityOCRMatches`, `GetOCRHighlightBoxes`, `GetHostBridgePort`, `GetKnowledgeForSession`, `GetKnowledgeEntries`, `DeleteKnowledgeEntry`, `GetSemanticEventsInRange`, `GetSemanticEventTimeline`, `DeleteSemanticEvent`
- **Updates** — `CheckForUpdates`, `DownloadUpdate`, `QuitAndInstall`, `GetVersion`, `GetUpdateStatus`
- **Models** — `ListAvailableModels`
- **Knowledge** — `PullKnowledge`, `PushKnowledge`
- **Memory** — `GetMemoryEntries`, `SaveMemoryEntry`, `DeleteMemoryEntry`, `SearchMemory`, `SyncMemoryIndex`, `GetMemoryStatus`, `GetMemoryConfig`, `SaveMemoryConfig`, `RejectMemoryAttribution`, `ReassignMemoryAttribution`
- **Automations** — `CreateCronJob`, `DeleteCronJob`, `UpdateCronJob`, `ListCronJobs`
- **Handoffs** — `ListHandoffs`, `GetHandoff`, `GetActiveHandoffChip`, `GetActiveHandoffChips`, `AcceptHandoff`, `AbortHandoff`, `DeleteHandoff`
- **People / Identity** — `ListPrincipals`, `ListPrincipalsByKind`, `EnsureSelfProfile`, `CreateContact`, `UpdatePrincipal`, `DeletePrincipal`, `BindAppHandle`, `UnbindAppHandle`, `ListAppHandlesForPrincipal`, `PromoteAnonymousToContact`, `MergePrincipals`, `CleanupAnonymousPrincipals`, `ListPrincipalActivitySessions`, `ListSessionPrincipals`, `GetPrincipalStats`, `ListPrincipalsWithStats`
- **Voice enrollment** — `GetVoiceEnrollmentStatus`, `ListVoiceSamples`, `EnrollVoiceSample`, `DeleteVoiceSample`, `TestVoiceMatch`, `ListSessionSpeakerClusters`, `GetVoiceInputApps`, `AddVoiceInputApp`, `RemoveVoiceInputApp`
- **MCP** — `SetMcpServerTrusted`
- **Telemetry** — `GetTelemetryTier`, `SetTelemetryTier`
- **Notifications** — `TestDesktopNotification`, `FireDelayedDesktopNotification`
- **AX Parser (dev tools)** — `ProbeAXParser`, `ListAXParserCatalog`, `ListAXParserUnsupportedApps`, `ListAXWindowsByBundle`
- **Identity debug** — `ClassifyActivityEvents`, `ResolveChatSender`
- **Quick chat** — `AttachQuickChatContext`, `SendPromptWithAttachments`

Runtime utilities from `@wailsapp/runtime`: `EventsOn`, `EventsEmit`, `ClipboardSetText`, `WindowGetPosition`, `WindowGetSize`, `WindowSetPosition`, `WindowSetSize`, `WindowIsMaximised`, `BrowserOpenURL`, `LogInfo`, `LogWarning`.

Real-time events from Go are received via `EventsOn`:

| Event | Payload |
|---|---|
| `agent:event` | `AgentEvent` — streaming output (text, tool calls, thinking, plan, permission requests, done/error) |
| `agent:status` | `AgentStatus` — backend connectivity state (running, ready, port, message) |
| `clone:progress` | Git clone progress updates |
| `cron:state` | `CronJobState` — cron job insert/update/delete notifications |
| `terminal:exit` | Terminal session closed notification |
| `voice:status` | Voice recording/transcription state changes |
| `voice:download-progress` | Whisper model download progress |
| `subscription:changed` | Subscription status update |
| `insight:saved` | New knowledge/memory insight created |
| `app:ready` | App startup complete — emitted once after Wails WebView fully initializes |
| `spotlight:session-changed` | Spotlight conversation persisted — refresh project tree |
| `spotlight:expand-to-chat` | Spotlight maximize — switch to standalone chat |
| `auth:greeting` | Credential probe greeting toast |
| `auth:recovering` / `auth:fallback` / `auth:error` | Auth recovery state changes |
| `activity:summary-ready` | Activity summary generation complete |
| `transcript:ready` | Meeting transcript complete |
| `proactive:notification` | Automation proposal or handoff request |
| `notification:clicked` | OS notification click routing |
| `update:status` | Auto-updater status change |
| `settings:open` | Open settings dialog with optional topic |
| `parser:dispatch` | AX parser dispatch trace (dev mode) |
| `nav:open-automation` | Navigate to automation from chat link |
| `nav:open-activity-session` | Open activity session from chat UUID link |

### Streaming Event Pipeline

```
Go backend
    │ EventsOn('agent:event', handler)
    ▼
useAgentStreaming
    │ Converts AgentEvent → StreamItem
    │ Buffers in pendingEventsRef
    │ Flushes every 150ms to avoid React thrashing
    ▼
useProjects.setProjects()   (immutable session.stream update)
    ▼
App.tsx re-render
    ▼
StreamBody → GroupedStreamList → StreamCard
```

Stream items are grouped before rendering (`lib/stream-helpers.ts`):
- `tool` items → single-row `tool-group` wrappers that preserve stream order
- `thinking` blocks remain in their original stream position
- Mid-turn assistant text stays inside the collapsible processing block
- User messages create conversation **turn** boundaries

## Key Components

| Component | Description |
|---|---|
| `App.tsx` | Root — hooks composition, layout, top-level state |
| `sidebar.tsx` | Project/workspace navigation with drag-and-drop reordering |
| `stream-body.tsx` | Conversation display with pagination and virtualization |
| `composer.tsx` | Message input with model selection, thinking level, plan mode |
| `composer-suggestions.tsx` | Inline AI-powered suggestion chips displayed within the composer |
| `terminal-panel.tsx` | xterm.js terminal tabs per workspace |
| `terminal-instance.tsx` | Single xterm.js terminal with WebGL, search, WebSocket attach, and imperative API (`TerminalInstanceHandle`) |
| `context-panel.tsx` | Project PRD, scenarios, and git diff viewer |
| `stream-card.tsx` | Individual message card (user, assistant, tool, thinking) |
| `markdown-content.tsx` | Markdown rendering with syntax highlighting via **streamdown** + `react-syntax-highlighter`, including a 400ms streaming text pacer and activity session UUID linking |
| `add-project-dialog.tsx` | Entry point for local, URL, and Yansu project types |
| `settings-page.tsx` | Settings dialog shell (sidebar, page header, deep links) plus the Developer tools pages; user-facing pages live in `components/settings/` (see `settings/settings-registry.ts`) |
| `keybindings-settings.tsx` | Keybinding recorder with conflict detection |
| `processing-block.tsx` | In-progress tool execution display |
| `thinking-card.tsx` | Extended thinking (Claude / Codex) display |
| `plan-review-card.tsx` | Plan mode approval interface |
| `pr-button.tsx` | PR status button — open browser or create new PR |
| `changes-panel.tsx` | Git diff panel using `@pierre/diffs` |
| `grouped-stream-list.tsx` | Groups stream items into turns with one chronological processing block and a visible final summary |
| `typing-indicator.tsx` | Animated executor-type indicator while streaming |
| `voice-recording-indicator.tsx` | Pill-shaped recording indicator with pulsing red dot, elapsed timer, and waveform bars |
| `tool-group-card.tsx` / `tool-group-item.tsx` / `tool-group-row.tsx` | Single tool-call row with icon, label, summary chip, and optional debug popover |
| `tool-debug-popover.tsx` | Bug-icon trigger that pops out raw tool input/output when debug mode is enabled |
| `permission-question-card.tsx` | Agent permission approval/denial UI |
| `login-screen.tsx` | OAuth login screen shown when unauthenticated |
| `onboarding-screen.tsx` | Full-window first-run flow: Welcome, Tools, Assistant, Permissions, AI memory, First task |
| `activity-page.tsx` | Full-page activity view — the day page (digest, what you worked on, what Isoform learned), the week digest, and the screen timeline with OCR screenshot search, knowledge integration, semantic events |
| `knowledge-page.tsx` | How you do things: "You've hit this before", cards by kind with reuse counts, a detail that prepares a main-chat request, and the entry tools (edit, history, translate, delete) |
| `memory-page.tsx` | Memory manager — view, search, edit, delete memory entries; sync memory index across data sources |
| `people-page.tsx` | Who you know, ordered by what is open between you: the headline of what you owe, merge banners with Undo, and a person detail with Brief me before we talk and Draft a reply (both fill the main chat box); messages, sessions, the dossier, the voice fingerprint and handles are folded in the detail; `initialSelection="self"` opens on your own card (Settings > Learning and memory > Your profile) |
| `tasks/TasksPage.tsx` | Tasks board, including scheduled work and handoff readers |
| `AutomationDetailPane.tsx` | Inline automation detail with run logs, edit, and continue-in-chat |
| `mcp-hub-settings.tsx` | MCP server hub — install, discover, enable/disable, restart, view logs, and uninstall MCP servers |
| `skill-hub-settings.tsx` | Skill hub — install skills from ClawHub, GitHub, or local directories |
| `confirm-dialog.tsx` | Generic reusable confirmation modal |
| `automation-indicator.tsx` | Cron job automation status badge shown in the sidebar |
| `entry-content.tsx` | Markdown renderer for memory/knowledge entries with keyword highlighting |
| `sidebar-group-section.tsx` | Sidebar group section — collapsible group container with add/rename/delete actions |
| `add-to-group-menu.tsx` | Context menu for assigning a project to a sidebar group |
| `first-chat-welcome.tsx` | Welcome callout shown on the first message in a new session |
| `new-chat-welcome.tsx` | Welcome message shown at the start of each new chat session |
| `observing-status.tsx` | Live indicator for workspace observing status with pulse animation |
| `search-dialog.tsx` | Search and commands (Cmd+K and the sidebar's Search): finds the words across topics, chats, Activity, people, goals, know-how and About you (`SearchEverything`), plus chats, Upcoming, one-time work, projects, Library pages, Settings pages and actions |
| `activity-summary.tsx` | The digest card (Chat about this day/week, Copy, Regenerate) shared by the Activity page's Day and Week views |
| `ai-memory-import-dialog.tsx` | Dialog for importing AI memory from external sources |
| `compaction-banner.tsx` | Banner displayed between message list and composer surfacing auto-compaction state |
| `contacts/contact-editor.tsx` / `contacts/principal-picker.tsx` | People/contact management UI |

UI primitives in `components/ui/` wrap Radix UI components (`button`, `dialog`, `dropdown-menu`, `select`, `combobox`, `scroll-area`, `context-menu`, `popover`, `switch`, `textarea`, `calendar`, `checkbox`, `collapsible`, `sheet`, `card`, `avatar`, `command`) with consistent project styling.

## Theming

Two built-in themes, switchable at runtime via the `yansu.theme` localStorage key:

- **Paper** (light, default) — neutral light palette
- **One Dark** (dark) — near-black dark palette

Theme files in `themes/` define terminal color palettes (16-color xterm set + background/foreground/cursor/selection) and metadata (id, label, isDark). The full UI theming (colors, typography, shadows, border radii) is handled by CSS custom properties defined in `index.css`, toggled via a `data-theme` attribute on `<html>` set by `useTheme`. The `@custom-variant dark` Tailwind directive maps `[data-theme='onedark']` to the dark variant, so Tailwind utility classes like `dark:bg-*` respond to the onedark theme.

## Internationalization (i18n)

All user-facing strings go through `react-i18next`. English is the source of truth for every key. Missing keys in `zh-CN` automatically fall back to English (`fallbackLng: 'en'`).

```
i18n/
├── index.ts              # i18next initialization
├── i18next.d.ts          # TypeScript augmentation for typed t() keys
└── locales/
    ├── en/               # English (source of truth)
    │   ├── common.json   # Shared strings (buttons, labels, actions)
    │   ├── app.json      # App-related strings
    │   ├── chat.json     # Chat and composer strings
    │   ├── settings.json # Settings pages
    │   ├── onboarding.json
    │   ├── calendar.json
    │   ├── activity.json
    │   ├── knowledge.json
    │   ├── memory.json
    │   └── people.json   # People/identity page strings
    └── zh-CN/            # Chinese translations (mirrors en/ structure)
```

Usage: `const { t } = useTranslation('chat')` then `t('key.path')`. Never use inline defaults (`t('key', 'fallback')`) — missing keys must surface as bugs.

## Dependencies

Key production dependencies (see `../package.json` for full list):

| Package | Purpose |
|---|---|
| `react` / `react-dom` 18.3 | UI framework |
| `@wailsapp/runtime` | Wails Go↔JS bridge |
| `@radix-ui/*` | Headless accessible UI primitives |
| `tailwindcss` 4.x | Utility-first CSS |
| `lucide-react` | Icon set |
| `@xterm/xterm` + addons | Terminal emulator |
| `@dnd-kit/*` | Drag-and-drop project reordering |
| **streamdown** | Markdown rendering with streaming support (used in `markdown-content.tsx`) |
| `react-syntax-highlighter` | Code block syntax highlighting |
| `react-markdown` + `remark-gfm` | Markdown rendering for update changelog only |
| `react-virtuoso` | Virtual list for long message histories |
| `cmdk` | Command palette / combobox |
| `date-fns` | Date formatting and manipulation |
| `react-day-picker` | Calendar date picker component |
| `i18next` + `react-i18next` + `i18next-browser-languagedetector` | Internationalization framework |
| `sonner` | Toast notifications |
| `@pierre/diffs` | Git diff visualization |
| `class-variance-authority` | Component variant helpers |
| `clsx` + `tailwind-merge` | Safe className composition |
| `posthog-js` | Legacy telemetry regression tests; the app uses the Go telemetry sink |
| `@fontsource-variable/inter` | Inter variable font for UI typography |

## Usage

This directory is built by the Wails build system. To run locally:

```bash
# From repo root — starts Go backend + Vite dev server together
./start-demo.sh

# Frontend only (defaults to port 5173; auto-picks a free port if busy)
cd wails-gui/frontend && pnpm run dev
```

> **Do not run `npm run build` manually** while `start-demo.sh` is active.

## Configuration

- **TypeScript**: `tsconfig.json` — strict mode, ES2022 target, `@/*` path alias → `./src/*`
- **Vite**: `vite.config.ts` — React plugin, TailwindCSS plugin, default port 5173 with auto-port selection, `@` alias, Wails shim aliases
- **Tailwind**: Configured via `index.css` using v4 CSS-native approach (no `tailwind.config.js`)

## localStorage Keys

| Key | Purpose |
|---|---|
| `yansu.theme` | Theme preference (`paper` / `onedark` / `system`) |
| `yansu.sidebar-width` | Sidebar panel pixel width |
| `yansu.context-panel-width` | Right context panel pixel width |
| `yansu.keybindings` | Custom keybinding overrides |
| `yansu.terminal-height` | Terminal panel height |
| `yansu.vim-mode` | Vim scroll mode enabled |
| `yansu.context-panel` | Context panel open/closed state |
| `yansu.changes-panel` | Changes panel open/closed state |
| `yansu.onboarding.state` | Onboarding progress (JSON) |
| `yansu.hasChattedBefore` | Whether the user has sent a message before |
| `yansu.appMode` | The page open last (`chat` / `today` / `activity` / ...); written on every switch, not read at launch, which always opens Today |
| `yansu.group-display-mode` | Sidebar group display mode (`flat` vs `nested`) |
| `yansu.automation-display-mode` | Automations display mode (`sidebar` vs `menu`) |
| `yansu.devtools-unlocked` | Dev tools unlocked in release build |
| `yansu.debug-mode-tools` | Debug mode toggle for raw tool call rendering |
| `yansu.timeline.calendarCollapsed` | Calendar collapse state on the timeline page |
| `yansu.handoff-decisions` | Handoff decision state |
| `yansu.chat-wide-mode` | Chat wide/fixed layout mode |
| `yansu.connect-banner-dismissed` | Provider connect banner dismissed |
| `sidebar-chat-expanded` | Sidebar chat section expanded state |
| `sidebar-transcripts-expanded` | Sidebar transcripts section expanded state |
| `i18nextLng` | User language preference (`en` / `zh-CN`) |
| `yansu.debug.parsers` | Developer flag for AX parser dispatch tracing |

## Performance Patterns

- `React.memo` on `StreamCard` and `MarkdownContent` to avoid re-renders during streaming
- 400ms streaming text pacer in `MarkdownContent` — decouples token arrival from display using requestAnimationFrame interpolation
- 150ms batch flush in `useAgentStreaming` coalesces rapid agent events
- Terminal tabs stay mounted to preserve PTY state across tab switches
- `react-virtuoso` for virtual lists when message history is long
- Scroll intent detection — auto-scroll is suppressed if the user has scrolled up
- `startTransition` wrapping in the streaming pacer to let React's concurrent scheduler interrupt heavy renders

## Testing

Component tests use Vitest + `@testing-library/react` (configured in `test-setup.ts`):

```bash
cd wails-gui/frontend
pnpm run test        # Run component/unit tests
pnpm run test:watch  # Run in watch mode
```

Existing test files:
- `components/onboarding-screen.test.tsx` — onboarding screen component tests
- `components/onboarding/steps/WelcomeStep.test.tsx` — welcome step tests
- `components/automations/schedule-utils.test.ts` — cron preset parsing and human-readable conversion
- `components/timeline/filters.test.ts` — calendar/timeline data filtering logic
- `hooks/useClientPresenceTelemetry.test.tsx` — client presence telemetry hook tests
- `lib/chat-error.test.ts` — chat error classification tests
- `lib/stream-helpers.test.ts` — stream helper utility tests

End-to-end tests use Playwright (Chromium) and target `http://localhost:34115`:

```bash
cd wails-gui/frontend
pnpm exec playwright test        # Run E2E tests
pnpm exec playwright test --debug  # Run E2E tests in debug mode
```

## Linting & Formatting

Linting and formatting are run via pre-commit hooks at the repository root. ESLint, Prettier, and TypeScript checks execute automatically before each commit.
