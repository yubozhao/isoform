# hooks

Custom React hooks for the Yansu Wails desktop application. Each hook encapsulates a distinct concern and communicates with the Go backend through the Wails-generated bindings in `wailsjs/go/main/App`.

## Directory Overview

| File | Purpose |
|---|---|
| `useAgentStreaming.ts` | Real-time AI stream event processing |
| `useComposerInput.ts` | Message composer state and submission |
| `useComposerSuggestions.ts` | Composer suggestion chips powered by the backend SuggestionGenerator |
| `useComposerDockSlide.ts` | FLIP animation: composer slides from centered welcome layout to docked chat layout |
| `useMainChatView.ts` | The main chat's RC 2 view: filter, today's numbers (`GetMainChatOverview`), the waiting cards (`ListMainChatWaitingItems`), "Hide folded updates" (`SetMainChatHideRoutine`), prepared chips (`ListMainChatAnswerChips`, `main-chat:chips`) and a work topic's rows and per-topic drafts (`useTopicChat`); it never sends or changes a chat row |
| `usePRInfo.ts` | Pull request info and create-PR URL |
| `usePermissions.ts` | macOS/platform permission checks and requests |
| `useProjects.ts` | Projects, workspaces, and sessions state |
| `useTerminal.ts` | Per-workspace terminal tab management |
| `useSettings.ts` | App settings dialog and vim mode toggle |
| `useUserProfile.ts` | Cached user profile fetch with module-level deduplication |
| `useVimScroll.ts` | Vim-style keyboard navigation for scroll areas |
| `useKeybindings.ts` | Customizable keyboard shortcut bindings |
| `useResizable.ts` | Generic drag-to-resize panel utility |
| `useSidebarResize.ts` | Sidebar width and collapse management |
| `useEnvironmentSwitch.ts` | Production/staging environment toggle via keyboard shortcut or logo rapid-click |
| `useProductTelemetry.ts` | Anonymous foreground activity through the backend telemetry gate |
| `useFirstBriefSeen.ts` | Counts the first brief entering the focused viewport without content |
| `useTheme.tsx` | Theme context provider and `useTheme` hook |
| `useShowToolSteps.ts` | Tool input/result visibility in chat: always off since its Settings switch went in settings batch D1 (answers v1.0.0 T2); a saved value is ignored |
| `useVoiceInput.ts` | Whisper-based voice recording and transcription |
| `useInsightNotification.ts` | Desktop notifications for new knowledge/memory insights from the activity/memory pipeline |
| `useSidebarGroups.ts` | CRUD and reorder operations for sidebar groups and their items |
| `useCronJobs.ts` | Cron job automation: create, update, delete, run, and stream run logs |
| `useConnectedPlatforms.ts` | Parallel status check for all supported messaging platforms (Slack, Discord, Telegram, Feishu, WeChat) |
| `useDevToolsUnlock.ts` | On/off switch for the Settings → Developer tools group, flipped with `Cmd+Shift+Option+D` |

---

## Hook Reference

### `useAgentStreaming`

Subscribes to Wails `agent:status` and `agent:event` events and funnels AI streaming output into React state.

**Key behaviors:**
- Batches state updates with a 150 ms flush interval to reduce renders during streaming.
- Terminal events (`done`, `error`, `close`, `plan_complete`) flush immediately.
- Accumulates assistant text chunks into a single string to avoid O(n²) concatenation.
- Caps in-memory items per session at 500 (`MAX_INMEMORY_ITEMS`); older items are trimmed and `hasMore` is set.
- Persists each stream item and debounces assistant content saves (every 3 s).

**Handled event types:** `run_created`, `assistant`, `tool_use`, `thinking`, `system`, `result`, `error`, `done`, `plan_complete`, `permission_request`, `available_commands`, `close`.

```ts
const { status, setStatus, initializeTracker, cleanupSession, flushAllPendingContent } = useAgentStreaming({ setProjects });
```

---

### `useComposerInput`

Manages the message composer: prompt text, image attachments, and submission logic.

**Key behaviors:**
- Auto-renames the session on the first message (first 40 chars of the prompt).
- Auto-renames non-default workspaces (git branch + label) on first message; awaits the rename before calling `SendPrompt` to avoid working-directory errors.
- Persists the user message and obtains its `sort_order` before sending, so the checkpoint system works correctly.
- Supports plain-text and multimodal (image) submissions.
- `Cmd+Enter` triggers submission when the session is not already streaming.
- `sendImmediate(text)` sends a message bypassing the prompt state (used by suggestion chips).
- `onTyping()` is invoked on keystrokes and forwards to the optional `onTyping` dependency callback.
- Onboarding: when `isFirstLogin` is true, the very first message (usually the request typed in onboarding's First task step) carries context saying the user just finished setting up Isoform and chose this assistant, so the reply answers that request directly without an introduction or a push toward building an app.
- Transcript sessions (label starts with `Transcript:`): inject the transcript body as grounding context on the first user turn.

```ts
const { prompt, setPrompt, images, setImages, sendPrompt, sendImmediate,
        handleCancel, handleKeyDown, onTyping,
        queuedMessages, removeQueuedMessage, editQueuedMessage } =
  useComposerInput({ activeProject, activeWorkspace, activeSession, updateProject,
                     status, model, setError, sendPromptCombo,
                     connectedProviders, userName, isFirstLogin });
```

---

### `useComposerSuggestions`

Subscribes to the backend's cached suggestion batch and re-fetches whenever the `composer-suggestions:updated` Wails event fires. The batch is generated hourly by the Go-side `SuggestionGenerator`; the hook never triggers generation itself.

**Key behaviors:**
- Fetches the current batch via `GetComposerSuggestions` on mount.
- Re-fetches (with `isRefreshing: true`) whenever the backend emits `composer-suggestions:updated`.
- Exposes a `refresh()` method that calls `RegenerateComposerSuggestions` for debug/manual use; the backend event drives the subsequent re-fetch.

```ts
const { suggestions, batchId, isLoading, isRefreshing, refresh } = useComposerSuggestions();
```

---

### `useComposerDockSlide`

FLIP animation that slides the composer element from its centered welcome-screen position to its docked bottom position when the first message is sent.

**Usage:**
```ts
const { welcomeRef, chatRef } = useComposerDockSlide(isEmptySession);
// Attach welcomeRef to the composer wrapper in welcome layout
// Attach chatRef to the composer wrapper in chat layout
```

**Mechanism:** Records the composer's `DOMRect` on every commit while in welcome mode. On the `empty → chat` transition, computes `deltaY = welcomeRect.top - chatRect.top`, applies `translateY(deltaY)` synchronously (no transition), then removes it in the next `requestAnimationFrame` so the CSS spring (`520 ms cubic-bezier(0.16, 1, 0.3, 1)`) engages. Inline styles are cleaned up via `transitionend` with a 900 ms safety timer for WebView environments that may drop the event.

---

### `useShowToolSteps`

Whether chat shows raw tool calls and their results. Its Settings switch (Show tool steps in chat) went in settings batch D1 (computer-conformance-answers v1.0.0 T2), so the hook always returns the default, off, and ignores any value saved under `yansu.show-tool-steps`.

```ts
const [enabled] = useShowToolSteps(); // always false
```

---

### `usePRInfo`

Fetches existing PR information and builds a PR creation URL for the current workspace branch.

```ts
const { prInfo, loading, createPRUrl, refresh } = usePRInfo({
  projectPath, remoteUrl, branch, baseBranch, enabled,
});
```

Automatically refreshes when any dependency changes. Both `GetPRInfo` and `BuildPRCreateURL` are called in parallel.

---

### `useProjects`

The central state management hook for projects, workspaces, and sessions. It is the single source of truth for the project hierarchy.

**Responsibilities:**
- Project lifecycle: open (file picker + auto-detect), close, switch, reorder.
- Session lifecycle: create, switch, close, pin, rename, rewind.
- Workspace lifecycle: create (git worktree), switch, close, merge.
- Session settings: model selection, plan mode, thinking mode, thinking level.
- Knowledge sync: pull/push to remote.
- Self-healing: ensures every project always has at least one valid workspace and session.

```ts
const {
  projects, setProjects, activeProjectId, activeProject,
  activeWorkspace, activeSession, updateProject,
  handleOpenProject, handleCloseProject, handleSwitchProject,
  handleNewSession, handleSwitchSession, handleCloseSession,
  handleNewWorkspace, handleSwitchWorkspace, handleCloseWorkspace, handleMergeWorkspace,
  handleModelChange, handleTogglePlanMode, handleToggleThinking, handleApprovePlan,
  handleRejectPlan, handleRewind, handlePullKnowledge, handlePushKnowledge,
  handleTogglePin, handleRenameSession, handleReorderProjects, ...
} = useProjects({ model, setModel, setError });
```

---

### `useTerminal`

Manages terminal tabs with full per-workspace isolation. Each `projectId:workspaceId` pair maintains its own set of tabs.

**Key behaviors:**
- All terminal instances stay mounted across workspace switches to preserve PTY state.
- Handles `terminal:exit` events from the backend to clean up closed tabs.
- Auto-collapses the panel when no tabs remain.
- Uses `useResizable` for the terminal panel height (120–600 px, default 280 px).
- Provides `Cmd+W` (close active) and `Cmd+1–9` (switch by index) keyboard helpers.

```ts
const {
  terminalTabs, allTerminalTabs, activeTerminalId, setActiveTerminalId,
  terminalCollapsed, setTerminalCollapsed,
  terminalHeight, isResizingTerminal, startTerminalResize,
  handleCreateTerminal, handleCloseTerminal, handleToggleTerminal,
  handleRenameTerminal, handleCloseActiveTerminal, handleSwitchTerminalByIndex,
  handleCloseProjectTerminals, handleCloseWorkspaceTerminals,
  registerTerminalApi,
} = useTerminal({ activeProjectId, activeWorkspaceId, activeWorkDir, layoutRef, terminalFg, terminalBg, remoteEnv });
```

---

### `useSettings`

Settings hook: dialog open/close state and UI preferences, all persisted to `localStorage`.

| Setting | Storage key | Type | Default |
|---|---|---|---|
| `vimMode` | `yansu.vim-mode` | `boolean` | `false` |
| `groupDisplayMode` | `yansu.group-display-mode` | `'flat' \| 'nested'` | `'nested'` |
| `settingsOpen` | — (in-memory) | `boolean` | `false` |

```ts
const {
  settingsOpen, setSettingsOpen,
  vimMode, setVimMode,
  groupDisplayMode, setGroupDisplayMode,
} = useSettings();
```

---

### `useVimScroll`

Adds Vim-style keyboard navigation to a Radix `ScrollArea` component. All keys are no-ops when an editable element (`<input>`, `<textarea>`, or `contentEditable`) is focused.

| Key | Action |
|---|---|
| `j` / `k` | Scroll down / up 60 px |
| `Ctrl+D` / `Ctrl+U` | Scroll down / up half page |
| `Ctrl+F` / `Ctrl+B` | Scroll down / up full page |
| `G` (Shift+G) | Scroll to bottom |
| `gg` | Scroll to top (two `g` presses within 500 ms) |
| `i` | Call `onInsert` callback (focus composer) |
| `Escape` | Blur active element |

```ts
useVimScroll(vimMode, scrollAreaRef, () => composerRef.current?.focus());
```

---

### `useKeybindings`

Manages the full set of customizable keyboard shortcuts, persisted to `localStorage` under `yansu.keybindings`.

**Action groups:** `global`, `composer`, `vim`.

`readonly` actions are listed in Settings but never matched in the renderer; the system-wide Pause observing shortcut (`⌃⌥⌘E` on macOS, `Ctrl+Alt+Shift+E` on Windows, handled by the Go hook) is one, and `findConflict` still reports it so nothing else takes its keys. `devOnly` actions (Replay onboarding) are hidden from Settings and bound by App.tsx only while Developer tools are on.

Exported utilities:
- `comboToDisplayString(combo)` — returns a human-readable label like `⌘B`.
- `eventMatchesCombo(event, combo)` — checks a `KeyboardEvent` against a `KeyCombo`.
- `eventToCombo(event)` — converts a `KeyboardEvent` to a `KeyCombo`.
- `comboEquals(a, b)` — structural equality for `KeyCombo` values.
- `PAUSE_OBSERVING_COMBO` — the read-only Pause observing combo for this platform (`null` on Linux); Settings > General > Advanced and Observation both show it.

```ts
const { actions, getCombo, updateBinding, resetBinding, resetAll, findMatchingAction, findConflict, isCustomized } =
  useKeybindings();
```

---

### `useResizable`

Generic drag-to-resize utility. Supports anchoring to any edge (`left`, `right`, `top`, `bottom`) and persists the current size to `localStorage`.

```ts
const { size, setSize, isResizing, startResize } = useResizable({
  anchor: 'bottom',
  defaultSize: 280,
  minSize: 120,
  maxSize: 600,
  storageKey: 'yansu.terminal-height',
  layoutRef,
});
```

Attach `onMouseDown={startResize}` to the drag handle element.

---

### `useSidebarResize`

Fixed sidebar frame: 240 px open, 56 px collapsed. Automatically collapses below a 1160 px window width and opens again on wider windows unless hand-collapsed.

- Hand-collapse is window-scoped UI state in `sessionStorage` under `isoform.ui.sidebar.handCollapsed`; automatic resizing never overwrites it.
- The collapsed mark can open the sidebar on a narrow window; the next resize applies the breakpoint again.

```ts
const { sidebarCollapsed, setSidebarCollapsed, sidebarWidth } =
    useSidebarResize({ layoutRef });
```

---

### `useEnvironmentSwitch`

Manages the production/staging environment toggle. Reads the current environment from the backend on mount and exposes two trigger mechanisms plus a confirmation dialog.

**Trigger mechanisms:**
- **Keyboard shortcut**: `Cmd+Shift+Option+E` (macOS) / `Ctrl+Shift+Alt+E` (Windows/Linux)
- **Logo rapid-click**: 5 clicks on the app logo within a 2-second window

**States:** `production` | `staging`

```ts
const {
  environment, isStaging, isDev, loading, showConfirmDialog, error,
  handleLogoClick, confirmSwitch, cancelSwitch,
} = useEnvironmentSwitch(onEnvironmentChanged);
```

`confirmSwitch` calls `SwitchEnvironment` on the backend and invokes the optional `onEnvironmentChanged` callback on success.

---

### `useUserProfile`

Fetches the local self profile (People "self") exactly once per session. Results are cached at module level so multiple consumers share a single `GetUserProfile` call with no duplicate requests.

**Key behaviors:**
- Module-level `cachedProfile` and `fetchPromise` prevent redundant backend calls across consumers.
- `clearUserProfileCache()` is exported for callers that change the self profile, so stale data is not shown afterwards.

```ts
const { profile, loading } = useUserProfile();
```

---

### `useProductTelemetry` / `useFirstBriefSeen`

Product telemetry uses the Go backend's build configuration, stable anonymous install identity, batching, and telemetry tier. The renderer does not initialize a PostHog SDK or inspect a profile.

**Key behaviors:**
- Waits for the saved sharing preference and follows changes immediately.
- Reports foreground activity every 30 seconds, ending sessions after five minutes without input. Input event handlers never inspect their contents.
- Excludes background time, caps sleep gaps, and flushes partial intervals on blur, hide, or unmount.
- Reports only that a nonempty brief entered the focused viewport; the backend stores the once-per-install milestone locally even when sharing is off, without sending it later when sharing is enabled.
- Passes only elapsed seconds and a random activity session ID across the activity binding; no screen or observed content is included.
- Uses cryptographic random bytes when a WebView lacks `crypto.randomUUID`; if Web Crypto is unavailable, optional activity telemetry stays off and the app still mounts.

```ts
useProductTelemetry();
```

---

### `useTheme` / `ThemeProvider`

Theme context provider and consumer hook. Supports three preferences: `'paper'` (Light, default), `'onedark'` (Dark) and `'system'` (Match system: follows `prefers-color-scheme` live). Persisted under `yansu.theme`. The Go side (spotlight helper, toast windows) only ever receives the resolved `paper` / `onedark`.

The resolved `themeId` is applied as a `data-theme` attribute on `<html>` and the full `ThemeDefinition` (including `theme.terminal` colors) is available to consumers.

```tsx
// Wrap the app root
<ThemeProvider>
  <App />
</ThemeProvider>

// Consume anywhere inside the tree
const { preference, themeId, theme, terminalTheme, setPreference } = useTheme();
```

### `usePermissions`

Fetches and manages macOS (and other platform) permission states via `GetPlatformPermissions`. Re-fetches automatically on window focus and tab visibility change so the UI reflects out-of-band grants the user may have made in System Settings.

**Key behaviors:**
- Calls `RequestPermission(key)` and re-reads permissions afterwards to confirm the grant.
- Falls back to `OpenPermissionSettings` when a permission is still denied after the request.
- Exposes `requiredItems` (items where `required === true`) and `allGranted` for gating UI flows.

```ts
const { permissions, loading, refresh, requestPermission, hasItems, requiredItems, allGranted } =
  usePermissions();
```

---

### `useVoiceInput`

Manages Whisper-based voice recording and transcription. Communicates with the Go backend via `InitVoiceInput`, `TranscribeAudio`, and `GetVoiceInputStatus`, and listens for `voice:download-progress` and `voice:status` Wails events.

**States:** `idle` → `initializing` → `recording` → `transcribing` → `idle`

**Key behaviors:**
- Initializes the Whisper model on first use; tracks download progress with `fileName`, `percent`, and `speed` fields.
- Captures audio via the Web Audio API (`ScriptProcessorNode`, chosen over `AudioWorklet` for WKWebView compatibility) and encodes it as a 16-bit mono WAV before sending to the backend.
- Streams real-time audio level (`audioLevel`, 0–1) at ~60 fps during recording for VU-meter UI.
- `stopAndTranscribe()` is async and returns `Promise<string>` — the transcribed text for direct insertion into the composer.

```ts
const {
  state, error, statusMessage, liveTranscript,
  startRecording, stopAndTranscribe, cancelRecording, retryInitialization,
  openMicrophoneSettings, needsMicrophonePermission,
  downloadProgress, audioLevel,
} = useVoiceInput({
  onRecordingStart?: () => void,
  onTranscriptUpdate?: (text: string) => void,
  onRecordingCancel?: () => void,
});
```

---

### `useInsightNotification`

Listens for `insight:saved` Wails events emitted by the activity/memory pipeline when new knowledge or memory entries are created. Delivers a desktop notification via `notifyImportant` for each event.

**Event payload:**

| Field | Type | Description |
|---|---|---|
| `kind` | `'knowledge' \| 'memory'` | Whether the insight is a knowledge or memory entry |
| `title` | `string` | Title of the saved insight |
| `source` | `string` | Source identifier for the insight |

```ts
useInsightNotification(); // no arguments, no return value
```

---

### `useSidebarGroups`

Manages sidebar groups and their items — full CRUD, collapse state, reordering, and cross-group moves. All mutations apply optimistic updates immediately and reload from the backend on failure.

**Operations:**

| Method | Description |
|---|---|
| `createGroup(name)` | Create a new sidebar group |
| `renameGroup(id, name)` | Rename a group (optimistic) |
| `deleteGroup(id)` | Delete a group (optimistic) |
| `reorderGroups(ids)` | Persist a new group sort order (optimistic) |
| `setGroupCollapsed(id, collapsed)` | Persist collapse state (optimistic) |
| `addItemToGroup(groupId, itemType, refId)` | Add an item to a group; no-ops on duplicate |
| `removeItemFromGroup(groupId, itemId)` | Remove a specific item from a group (optimistic) |
| `removeItemsByRef(itemType, refId)` | Remove all group entries referencing a given entity (optimistic) |
| `moveItemBetweenGroups(srcId, tgtId, itemId, targetIndex?)` | Move an item across groups with fallback restore on failure |
| `reorderGroupItems(groupId, itemIds)` | Persist item sort order within a group (optimistic) |

```ts
const {
  groups, loading,
  createGroup, renameGroup, deleteGroup, reorderGroups,
  setGroupCollapsed, addItemToGroup, removeItemFromGroup,
  removeItemsByRef, moveItemBetweenGroups, reorderGroupItems,
} = useSidebarGroups();
```

---

### `useCronJobs`

Manages cron job automations: list, create, update, delete, trigger, and retrieve run history and logs. Subscribes to `cron:state` Wails events so the job list stays live without polling.

**Key behaviors:**
- Loads all jobs on mount via `ListCronJobs`.
- Subscribes to `cron:state` events; handles inserts, updates, and deletions in a single handler (deleted jobs carry a `deleted: true` flag).
- `createJob` optimistically prepends the new job before the `cron:state` event arrives.
- `loadRunLogs` maps raw backend items into typed `StreamItem[]` (same format as `useAgentStreaming`) for use with shared stream-rendering components.
- `reorderJobs(ids)` persists a new drag-and-drop sort order for the jobs list.

```ts
const {
  jobs, loading,
  createJob, toggleEnabled, updateJob, deleteJob,
  runNow, reorderJobs, loadRuns, loadRunLogs,
} = useCronJobs();
```

**`CreateAutomationInput`:**

| Field | Type | Description |
|---|---|---|
| `name` | `string` | Display name for the job |
| `prompt` | `string` | Agent prompt to run on schedule |
| `cronExpr` | `string` | Standard cron expression (e.g. `"0 9 * * 1-5"`) |
| `workDir` | `string` | Optional working directory for the agent |
| `notifyDm` | `boolean?` | Send a DM notification when the job completes (default `true`) |
| `notifyPlatform` | `string?` | Platform to send the DM notification on (e.g. `"slack"`) |

---

### `useConnectedPlatforms`

Checks the live connection status of all supported messaging platforms in parallel and returns the set of currently active platform IDs.

**Checked platforms:** `slack`, `discord`, `telegram`, `feishu`, `wechat`

A platform is included in the `connected` set only when its connection and daemon are both online. Uses `Promise.allSettled` so a single platform failure never blocks the others.

**Platform-specific checks:**
- **Slack, Telegram, Feishu**: `connected && daemon_online`
- **Discord**: `bound && daemon_online`
- **WeChat**: `connected && online`

```ts
const { connected, refresh } = useConnectedPlatforms();
// connected: Set<string> — e.g. new Set(['slack', 'discord'])
// refresh(): re-fetch all statuses on demand (call when entering edit mode)
```

---

### `useDevToolsUnlock`

The on/off switch for the Settings → Developer tools group. There is no visible switch for release users; the shortcut flips it, and pressing it again turns the tools off. UI-only, with no backend side effects.

**Trigger:** `Cmd+Shift+Option+D` (macOS) / `Ctrl+Shift+Alt+D` (Windows/Linux)

**Key behaviors:**
- `defaultEnabled` is the state before the shortcut is ever used: App.tsx passes `isStaging || isDev`, so staging and dev builds start with the tools on.
- An explicit choice is persisted to `localStorage` under `yansu.devtools-unlocked` (`'1'` on, `'0'` off) and wins over the default.
- Shows a translated Sonner toast (`settings:devTools.toggle.*`) on every flip; key auto-repeat is ignored.
- App.tsx binds the developer-only Replay onboarding shortcut (`Cmd+Shift+O`) only while this returns `true`.

```ts
const devToolsEnabled = useDevToolsUnlock(envSwitch.isStaging || envSwitch.isDev); // boolean
```

---

## Architecture

All hooks follow a consistent pattern:

1. **Dependency injection via a `deps` argument** — hooks receive only the state they need (no direct context reads), keeping them independently testable.
2. **Wails bindings for backend calls** — backend operations are imported from `../../wailsjs/go/main/App` and `../../wailsjs/runtime/runtime`.
3. **Optimistic updates** — UI state is updated immediately; persistence to the backend is fire-and-forget with `logPersistError` for non-critical failures.
4. **`localStorage` for UI preferences** — resize sizes, vim mode, theme, and keybinding overrides are all stored locally, not on the backend.

## Dependencies

- **React** — `useState`, `useEffect`, `useCallback`, `useMemo`, `useRef`
- **Wails runtime** — `EventsOn` for real-time events from the Go backend
- **Wails Go bindings** — auto-generated from `wailsjs/go/main/App`
- **Sonner** — toast notifications
- **`@/lib/stream-helpers`** — shared utilities (`buildId`, `toStreamItemData`, `logPersistError`, etc.)
- **`@/themes/registry`** — theme definitions consumed by `useTheme`
