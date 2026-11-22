# Components

React UI components for the Wails GUI desktop application. This directory contains all feature components, reusable UI primitives, icon assets, and domain-specific sub-modules.

## Directory Overview

The root of this directory holds feature-level `.tsx` components that are consumed directly by `App.tsx` and the page-level views. Deeply cohesive surfaces (onboarding, automations, timeline, contacts, etc.) live in subdirectories with their own READMEs.

```
components/
├── *.tsx              # Feature components (71 files, including co-located tests)
├── ui/                # 18 styling and layout primitives
├── icons/             # App, executor, dream, and redaction icon components
├── onboarding/        # Multi-step first-run onboarding flow
├── automations/       # Cron job automation management
├── timeline/          # Calendar/timeline data hook and detail components
├── contacts/          # Principal/contact management dialogs and pickers
├── people/            # Who you know page parts: person detail, merge banner, folded sections, plain-words formatting
└── handoffs/          # Handoff proposal list and reader
```

## Key Components

### Session & Streaming

| Component | Description |
|-----------|-------------|
| `stream-body.tsx` | Main chat message area with scroll management, pagination, and Vim navigation |
| `stream-card.tsx` | Renders individual messages (user, assistant, tool results, errors, thinking); supports inline editing of historical user messages (rewind + resend) |
| `grouped-stream-list.tsx` | Groups stream items into turns, filtering system and compaction bookkeeping rows; in the main chat, one divider per local day with that day's routine updates folded into one row |
| `main-chat/main-chat-stream-body.tsx` | The main chat's RC 2 view around `StreamBody`: the filter row above the scroll area, the summary line, the waiting cards and a topic's rows; any other chat is the plain `StreamBody` |
| `main-chat/main-chat-filter-row.tsx` | Filter row (Everything, Waiting on you, work topics, Hide folded updates) and the "In your main chat today" line |
| `main-chat/main-chat-day.tsx` | Day divider and the per-day fold of routine updates |
| `main-chat/main-chat-topic-view.tsx` | The main chat filtered to one work topic at full width, and the box that sends with the topic attached (removable chip; Stop and the queue while a reply runs) |
| `composer-queue.tsx` | Messages queued behind a running reply, shown by the main box and the topic box |
| `main-chat/main-chat-topic-tags.tsx`, `main-chat-answer-chips.tsx`, `main-chat-view-context.tsx` | Topic tags on Isoform's cards and reports, prepared chips under the latest answer, and the context that carries them |
| `processing-block.tsx` | Collapsible AI processing timeline with reasoning, interim text, and ordered tool rows |
| `thinking-card.tsx` | Claude reasoning/thinking display with markdown and chronological tool summaries |
| `tool-group-card.tsx` | Thin wrapper that delegates a single tool-call row to `ToolGroupRow` |
| `tool-group-item.tsx` | Inline summary chip for a single tool call, with special icons for web search/fetch and a debug popover when debug mode is enabled |
| `tool-group-row.tsx` | Renders a tool-call row with icon, label phrase, and chip list; used inside `ProcessingBlock` (via `ToolGroupCard`) and `ThinkingCard` |
| `typing-indicator.tsx` | Animated three-dot indicator showing active executor type |
| `chat-error-card.tsx` | Renders classified upstream chat errors (quota, billing, auth, network, etc.) with recovery hints; quota and billing errors link to Settings > Assistant (See usage) |
| `chat-error-preview.tsx` | Dev-only gallery for visually iterating on `ChatErrorCard` variants (`#dev=chat-errors`) |
| `file-attachment-preview.tsx` | Chips and image thumbnails for files attached to the composer or a user message |
| `permission-question-card.tsx` | Agent permission question card with selectable action buttons, skip action, and free-text "Other" input |
| `animated-ellipsis.tsx` | Three-dot cycling ellipsis for inline "running" labels |
| `first-chat-welcome.tsx` | Builds the welcome greeting for first-time sessions, including connected provider status and suggestion chips |
| `new-chat-welcome.tsx` | Welcome content (logo, heading, subheading) shown for empty new chat sessions |
| `plan-review-card.tsx` | Plan approval/rejection interface with markdown display |
| `compaction-banner.tsx` | Auto-compaction lifecycle banner showing compacting spinner or error state between message list and composer; renders nothing when idle |

### Input & Composition

| Component | Description |
|-----------|-------------|
| `composer.tsx` | Auto-resizing prompt input with image/file attachments, model/executor selection, thinking level, and plan mode toggle |
| `composer-suggestions.tsx` | Inline suggestions/completions displayed within the composer |
| `voice-recording-indicator.tsx` | Pill-shaped recording indicator with red pulsing dot, mm:ss timer, 7-bar waveform reacting to `audioLevel`, and tap-to-stop-and-transcribe; self-contained elapsed-seconds counter |

### Project & Session Management

| Component | Description |
|-----------|-------------|
| `sidebar.tsx` | Chat/Automations/Handoffs mode toggle, standalone chat section (pinned/unpinned sessions), project list with dnd-kit drag-and-drop reordering, and menu linking to Activity, Knowledge, Memory, People, and Settings |
| `sidebar-group-section.tsx` | Renders a collapsible sidebar group section containing grouped sessions and projects |
| `add-to-group-menu.tsx` | Context menu for assigning sessions, projects, or automations to sidebar groups |
| `project-node.tsx` | Project item with workspace management, Git operations, and open-in-editor/terminal actions |
| `workspace-item.tsx` | Workspace entry with branch display, streaming indicator, and delete option |
| `session-item.tsx` | Session list entry with pin, archive, and inline rename |

### Panels

| Component | Description |
|-----------|-------------|
| `terminal-panel.tsx` | Multi-tab terminal manager with rename, create, close, search, and clear |
| `terminal-instance.tsx` | xterm.js terminal with FitAddon, WebglAddon, Unicode11Addon, SearchAddon, AttachAddon (WebSocket backend). Exposes `TerminalInstanceHandle` with `clear()`, `toggleSearch()`, `focus()` |

### Full-Page Views

| Component | Description |
|-----------|-------------|
| `activity-page.tsx` | Full-screen activity view: the day page (daily digest, merged "what you worked on" list, learned entries), the week digest, and the screen timeline (virtualized session list, screenshots, OCR search, video playback controls, per-frame OCR highlight overlay) |
| `activity-summary.tsx` | `copyTextToClipboard`, the clipboard helper the old day digest card shared (About you's portrait uses it) |
| `knowledge-page.tsx` | How you do things (RC 2 D5 + RC 3 confirm moment): cards by kind from `ListKnowHow` with scope chips, the `GetKnowHowCandidate` proposal banner with its draft review (Confirm / Edit steps / Not a how-to, Undo on the result), the `GetKnowHowMatch` banner, a detail whose buttons prepare a main-chat request, and Technical details with the raw entry and its Activity sessions |
| `memory-page.tsx` | Memory management UI backed by Wails calls; supports keyword search, entry editing, and memory configuration |
| `memory-dream-dialog.tsx` | Nightly memory consolidation dialog: overview, settings, run history, and archive management |
| `people-page.tsx` | People (RC 2): headline of what you owe, "Open between you" cards and "Everyone else" from `ListPeopleOverview`, duplicate merge banners with Undo, and the person detail (`people/`); search, groups, edit, merge, contacts sync and cleanup stay in the page and person More menus; `initialSelection="self"` opens on your own card |

### Settings

| Component | Description |
|-----------|-------------|
| `settings-page.tsx` | Settings dialog shell: sidebar (`settings/settings-nav.tsx`), page header with the page subtitle, deep-link resolution, and the Developer tools pages |
| `settings/settings-registry.ts` | The Settings pages and sidebar sections (labels, one-line subtitles, icons, search keywords, legacy id aliases) and `resolveSettingsTopic()` |
| `settings/pages/*.tsx` | One file per Settings page (What Isoform sees, Closed Eyes, Your data, Your agents, Agent of You, Your phone, Briefs and notifications, Learning and memory, Privacy, Storage, Plugins, Tool servers, Chat apps, Other AI apps, About), plus the parts they share: `about-settings.tsx` (About's name, Open at login, language, appearance and models rows) and `read-aloud-settings.tsx` (the Hear it voice on Briefs and notifications) |
| `settings/settings-advanced.tsx` | Shared "Advanced" disclosure for Settings pages; remembers its open state per page, and opens again on every navigation that asks for it (sidebar or Search and commands keyword search, `openSettings(topic, { advanced: true })`, `settings:open` with `{ advanced }`) through the `SettingsAdvancedRevealContext` counter; also the anchor scrolling helpers |
| `settings/pages/chat-apps/*` | Chat apps drill-ins: `bot-setups.tsx` (Slack, Discord, Telegram), `wechat-setup.tsx`, `feishu-setup.tsx`, `disconnect-button.tsx`, and `chat-app-status.ts` (the shared `{ configured, online }` status each setup reports) |
| `keybindings-settings.tsx` | Shared shortcut editor (live key recording, clash check, reset, the Quick chat OS hotkey round trip), the sheet's `ShortcutRow`, and the Quick Chat card on Settings › General |
| `keys-sheet.tsx` | Keyboard shortcuts sheet (Cmd+/): every key grouped by where it works, Change on the rows that can change, the Vim switch and the focus note |
| `mcp-hub-settings.tsx` | MCP (Model Context Protocol) server hub: discover local servers, import/install from registry, enable/disable, restart, and manage per-server configuration |
| `use-yansu-anywhere-settings.tsx` | The Other AI apps card (copy the install prompt for other assistants), plus `UseYansuAnywhereDetails` for that page's Advanced section (what other assistants can recall, examples, the full install prompt) |
| `ai-memory-import-dialog.tsx` | Dialog for configuring and importing AI memory from external sources (used during onboarding and from settings) |

### Auth & Onboarding

| Component | Description |
|-----------|-------------|
| `onboarding-screen.tsx` | Full-window first-run flow orchestrating `WelcomeStep` → `DependenciesStep` → `AgentStep` → `PermissionsStep` → `MemoryImportStep` → `FirstTaskStep` |
| `user-menu.tsx` | User avatar dropdown with profile info and sign-out |

### Project Add Flows

| Component | Description |
|-----------|-------------|
| `add-project-dialog.tsx` | Tabbed dialog delegating to the three add-project variants |
| `add-project-yansu.tsx` | Yansu organization/product picker with search and clone |
| `add-project-url.tsx` | GitHub URL clone with auto-detected repo name and target path |
| `add-project-local.tsx` | Native file-dialog local directory selector |

### Global Utilities & Navigation

| Component | Description |
|-----------|-------------|
| `search-dialog.tsx` | Find (`Cmd+K`) on the `ui/command` primitive, in computer-find v1.0.0 order: Ready (Your work), Conversations, People, Claims and Your answers (`FindAnything`), Go to (the five places with their keys, then Settings), then topics and chats (`SearchEverything` and titles) and every Settings page; know-how lines join Claims; matched words in bold; no commands from the settings registry; a chat message opens scrolled to it (`hooks/useChatMessageJump.ts`) |
| `markdown-content.tsx` | Streaming markdown renderer with `streamdown`, `remark-gfm`, and Prism syntax highlighting; includes a streaming pacer, custom action-protocol URL handling, and lazy activity-session UUID linking |
| `entry-content.tsx` | Markdown renderer for memory/knowledge entries with keyword highlighting, frontmatter parsing, and clickable activity session links |
| `patch-view.tsx` | Unified diff renderer that parses custom patch format (`*** Update/Add/Delete File:` headers) and displays add/remove/context lines with color highlighting |
| `edit-diff-view.tsx` | Line-level LCS-based diff renderer comparing old and new strings with add/remove/context highlighting |
| `pr-button.tsx` | PR status button showing state/number, with browser open and create options |
| `copy-button.tsx` | Reusable copy-to-clipboard button with copied-state feedback |
| `confirm-dialog.tsx` | Generic confirmation modal dialog for destructive or irreversible actions |
| `tool-debug-popover.tsx` | Bug-icon trigger that pops out raw tool input, paired tool_result, tool_use_id, and run_id; only shown when debug mode is enabled |
| `automation-indicator.tsx` | Faceted-clock SVG indicator for automation items; shows a rotating second hand and purple glow when running |

## Subdirectories

Each subdirectory has its own `README.md` with detailed component-level documentation, API references, and usage examples. See:

- [`ui/README.md`](ui/README.md) — UI primitive component library
- [`icons/README.md`](icons/README.md) — Icon components
- [`onboarding/README.md`](onboarding/README.md) — Onboarding flow
- [`automations/README.md`](automations/README.md) — Automation management
- [`timeline/README.md`](timeline/README.md) — Timeline data and components
- [`contacts/README.md`](contacts/README.md) — Principal/contact management

### `ui/`

18 UI primitives — a mix of Radix UI-based, third-party library, and custom components styled with TailwindCSS:

`avatar`, `button`, `calendar`, `card`, `checkbox`, `collapse-panel`, `collapsible`, `combobox`, `command`, `context-menu`, `dialog`, `dropdown-menu`, `popover`, `scroll-area`, `select`, `sheet`, `switch`, `textarea`

### `icons/`

- `app-icons.tsx` — Editor icons (VS Code, Cursor, Windsurf, Zed, Sublime), terminal icons (Ghostty, Warp, iTerm2, Alacritty), and app icons (Slack, Teams, Feishu, Telegram, Discord, WhatsApp, WeChat), plus `AppIcon` factory and `hasAppIcon` helper
- `executor-icons.tsx` — `ClaudeIcon` and `CodexIcon` SVG components using official brand colors
- `sidebar-3d-icons.tsx` — 3D-style faceted sidebar icons: `ChatIcon3D`, `GroupIcon3D` / `GroupParentIcon3D`, `AutomationsIcon3D`
- `dream-icons.tsx` — Dimensional SVG icons used in the Memory Dream dialog (`DreamMoonIcon`, `DreamShieldIcon`, `DreamCheckIcon`)
- `redacted-keyword.tsx` / `redaction-shield-icon.tsx` — Privacy/redaction visuals used in the activity monitor

### `onboarding/`

Step components and sub-components for the multi-step first-run onboarding flow. See [`onboarding/README.md`](onboarding/README.md).

### `automations/`

Cron job automation management panel. See [`automations/README.md`](automations/README.md).

### `timeline/`

Support components and data hook for the timeline/calendar page. See [`timeline/README.md`](timeline/README.md).

### `contacts/`

Contact/principal management components:

- `contact-editor.tsx` — `ContactEditorDialog`, `HandleListEditor`, `ChipInput`, and `syncHandles` for create/edit/promote flows
- `principal-picker.tsx` — Searchable principal list with inline creation affordance

### `handoffs/`


## Component Hierarchy

```
Sidebar
└── ProjectNode → WorkspaceItem

Main Content
├── SessionTabs → SessionItem
├── StreamBody
│   ├── GroupedStreamList
│   │   ├── StreamCard
│   │   ├── ProcessingBlock → ThinkingCard, ToolGroupCard → ToolGroupItem
│   │   └── PlanReviewCard
│   └── TypingIndicator
├── TerminalPanel → TerminalInstance
├── Composer
├── ContextPanel
├── ChangesPanel
├── PRButton
├── PermissionQuestionCard
└── AutomationDetailPane → CronJobDetail
```

## Architecture & Patterns

**State management**: React hooks (`useState`, `useEffect`, `useCallback`, `useMemo`) composed in `App.tsx` and custom hooks. Cross-component state is managed via props and hooks; there is no global external state library inside this directory.

**Internationalization (i18n)**: All user-facing strings (labels, headings, tooltips, descriptions, placeholders, dialog text, toast messages, error text) go through `react-i18next`. Keys are defined in `src/i18n/locales/en/<namespace>.json` (English is the source of truth) and mirrored in `src/i18n/locales/zh-CN/<namespace>.json`. Components use the `useTranslation('<namespace>')` hook. Hardcoded English strings and inline fallback defaults are intentionally avoided so missing keys surface as bugs. See `src/i18n/README.md` for the full convention.

**Styling**: TailwindCSS utility classes with CSS variable tokens (`--accent`, `--ink`, `--muted`, `--error`, shadow variables) for light/dark theming.

**Accessibility**: Radix UI primitives provide ARIA roles, keyboard navigation, and focus management. Vim-style scroll keys are layered on top via `useVimScroll`.

**Terminal**: xterm.js with `FitAddon`, `WebglAddon`, `Unicode11Addon`, `SearchAddon`, and `AttachAddon` for WebSocket-backed GPU-accelerated terminals.

**Drag & drop**: `dnd-kit` with `SortableContext` for reordering. The sidebar uses `verticalListSortingStrategy` with a custom `groupAwareCollision` detector that prevents groups and their child items from interfering during drag. Reordering is disabled when search is active to avoid confusing behavior with filtered lists.

**Markdown**: The chat/stream renderer uses `streamdown` with `remark-gfm` and `react-syntax-highlighter` (Prism). It supports a custom URL transform for internal action protocols (`handoff-accept://`, `handoff-dismiss://`, `automation://`) and lazy activity-session UUID resolution. A streaming pacer (`useSmoothedText`) feeds paced text into `streamdown`, and `startTransition` keeps rendering from blocking the browser during fast token arrival. Some legacy surfaces (timeline summary, settings release notes) still use `react-markdown`.

**Executor branching**: The UI adapts to the active executor type. Claude surfaces Think mode, Plan mode, and extended thinking toggles; Codex surfaces a thinking-level selector. Components use `ClaudeIcon` / `CodexIcon` and different color schemes per executor.

## Design System

Components use CSS custom properties (design tokens) rather than hard-coded colors, enabling theme switching without component changes.

```css
/* Semantic color tokens */
--accent          /* Primary accent */
--accent-2        /* Secondary accent (Claude brand) */
--ink             /* Primary text */
--muted           /* Secondary text */
--error           /* Error state text */
--border          /* Border color */
--background      /* Main background */

/* Green accent palette for redaction/privacy animations */
--color-seaweed
--color-seaweed-deep
--color-seaweed-light
--color-seaweed-foam
--color-seaweed-olive

/* Shadow tokens */
--shadow-button, --shadow-button-hover
--shadow-card, --shadow-soft

/* Code tokens */
--color-code-bg   /* Code block background */
--font-mono       /* Monospace font family */

/* Semantic TailwindCSS classes built on tokens */
bg-panel, bg-panel-warm, bg-panel-code, bg-panel-result
text-accent, text-muted, text-ink, text-error
border-border, border-border-strong
```

Themes are toggled by swapping CSS variable values via the `useTheme` hook. All component styling should use tokens rather than raw Tailwind color utilities to stay theme-safe.

## Performance Patterns

- **`React.memo()`** on `StreamCard` and `MarkdownContent` — prevents re-renders during active token streaming and state updates
- **`startTransition`** in `MarkdownContent` — marks per-frame streaming updates as transitions so React can interrupt the heavy `streamdown` render to let the browser paint
- **`useCallback` / `useMemo`** — stabilize function and object references in `useEffect` dependency arrays to avoid infinite effect loops
- **Terminal tab visibility toggle** — `TerminalPanel` hides inactive tabs with CSS rather than unmounting them, preserving xterm.js scroll history
- **Scroll intent detection** — `StreamBody` tracks whether the user has scrolled up and suppresses auto-scroll until they return to the bottom (80px threshold)
- **Prepend scroll preservation** — `useLayoutEffect` adjusts the scroll position when older messages are prepended (load more), preventing the viewport from jumping

## Dependencies

| Library | Usage |
|---------|-------|
| `@radix-ui/*` | Accessible UI primitives (dialog, dropdown, select, scroll-area, sheet, etc.) |
| `@xterm/xterm` + addons | Terminal emulation (FitAddon, WebglAddon, Unicode11Addon, SearchAddon, AttachAddon) |
| `@dnd-kit/core` + sortable | Drag-and-drop project reordering in sidebar |
| `streamdown` + `remark-gfm` | Streaming markdown renderer used in chat |
| `react-markdown` + `remark-gfm` | Legacy markdown renderer used in a few non-streaming surfaces |
| `react-syntax-highlighter` | Code block syntax highlighting (Prism.js) |
| `tailwindcss` | Utility-class styling |
| `lucide-react` | Icon set used throughout feature components |
| `class-variance-authority` | Typed variant generation for Button component |
| `cmdk` | Command palette primitive used in Combobox and Command |
| `react-virtuoso` | Virtual list for Combobox items (activates beyond 50 items) and activity session list |
| `@pierre/diffs/react` | Word-level Git diff rendering in ChangesPanel |
| `sonner` | Toast notifications |
| `react-day-picker` | Month-view calendar/date picker used in the `ui/calendar` primitive |

## Wails Backend Integration

Feature components call Go backend functions through the Wails FFI layer. Most imports come from the generated bindings or from the manually-maintained shim:

```tsx
import * as App from "../../wailsjs/go/main/App";
import { ListHandoffs } from "@/lib/wails-shim-app";
import { WindowGetSize } from "../../wailsjs/runtime/runtime";

// Examples used across components:
App.OpenFileDialog();                       // add-project-local
App.CloneRepository(url, path);             // add-project-url
App.GetSettings();                          // settings-page
```

Go-generated TypeScript types are imported from `../../wailsjs/go/models` and kept in sync with the backend schema. When a new Go method is added and imported by name, an explicit re-export must be added to `src/lib/wails-shim-app.ts` so Vite's alias resolves correctly.

## Usage

Components are imported directly by pages and other components:

```tsx
import { Sidebar } from "@/components/sidebar";
import { Composer } from "@/components/composer";
import { TerminalPanel } from "@/components/terminal-panel";
import { Button } from "@/components/ui/button";
import { ClaudeIcon } from "@/components/icons/executor-icons";
```

Feature components consume backend data through custom hooks (e.g., `useAgentStreaming`, `useProjects`) defined in `src/hooks/` and Wails Go bindings.

## Configuration

No component-level configuration files. Runtime behavior is controlled by:

- **Theme**: CSS variable tokens (`--accent`, `--ink`, `--muted`, `--error`, `--shadow-soft`, etc.) defined in the global stylesheet and toggled via the `useTheme` hook
- **Keybindings**: Stored in `~/.claude/keybindings.json` on the host and surfaced through the `useKeybindings` hook; editable in the Keyboard shortcuts sheet (`KeysSheet`, Cmd+/)
- **Settings**: Persisted by the Go backend (`App.GetSettings` / `App.SaveSettings`) and accessed through `settings-page.tsx`
- **Locale**: `i18n` initializes with the host locale and falls back to English; new strings must be added to both `en/` and `zh-CN/` locale files

## Testing

Unit tests live alongside their component files:

- `settings-page.test.tsx`, `settings/*.test.ts(x)` — Settings shell, sidebar, registry (every legacy id resolves; every page has en and zh-CN copy), Advanced disclosure and Chat apps page (Vitest + React Testing Library)
- `onboarding-screen.test.tsx` — Onboarding screen smoke test
- `onboarding/steps/WelcomeStep.test.tsx` — Welcome step behavior
- `automations/schedule-utils.test.ts` — Cron preset parsing and human-readable conversion
- `timeline/filters.test.ts` — Calendar/timeline data filtering logic

Run the test suite from `wails-gui/frontend`:

```bash
cd wails-gui/frontend && pnpm test
```

End-to-end tests for the broader application live in the separate `frontend/e2e/` Playwright suite. To run them:

```bash
cd frontend && pnpm run test:e2e          # headless
cd frontend && pnpm run test:e2e:debug    # with browser inspector
```
