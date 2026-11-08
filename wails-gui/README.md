# Yansu Desktop (Wails GUI)

A cross-platform desktop application for AI-assisted coding, activity monitoring, knowledge management, and multi-channel integrations — built with [Wails v2](https://wails.io/) (Go backend + native WebView + React frontend).

> **Note:** This is a Wails app, **not** Electron. It uses the OS native WebView (WKWebView on macOS, WebView2 on Windows), not a bundled Chromium. The Go backend communicates with the frontend via Wails IPC, not Node.js IPC.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                 Frontend (React + TypeScript)         │
│   Components · Hooks · State · xterm.js terminals   │
└──────────────────┬──────────────────────────────────┘
                   │  Wails IPC (EventsEmit / function calls)
┌──────────────────▼──────────────────────────────────┐
│              App Struct (Go methods)                  │
│  auth · streaming · project · worktree · terminal   │
│  memory · activity · cron · mcp-hub                 │
│  permissions · voice · integrations · browser-bridge │
│  auto-update · skill-hub · spotlight · cua-driver    │
│  quick-chat · toast · i18n · subscription            │
│  install-discovery · opencli · prediction            │
└──────────┬──────────────────────────────┬───────────┘
           │                              │
  ┌────────▼────────┐           ┌─────────▼──────────┐
  │  storage/        │           │  internal/          │
  │  (SQLite)        │           │  terminal/          │
  │                  │           │  persistence/       │
  │  sessions.db     │           │  platform/          │
  └─────────────────┘           │  activitybuf/       │
                                │  worktree/          │
  ┌─────────────────┐           │  pii/ · sshconn/   │
  │  activity_store  │           │  meetingdetect/     │
  │  (activity.db)   │           │  mcp/ · asr/        │
  └─────────────────┘           │  singleinstance/    │
                                │  onnxdl/ · wsl/     │
  ┌─────────────────┐           └────────────────────┘
  │  memory_store    │
  │  (memory.db)     │
  └─────────────────┘
```

The Go backend exposes methods on the `App` struct via Wails IPC. The React frontend calls these as async functions through auto-generated bindings in `frontend/wailsjs/`. Bidirectional event streaming uses `wails.EventsEmit` (Go → JS) and `wails.EventsOn` (JS → Go). Exported `App` methods are spread across ~300 Go source files in the root package and internal packages, grouped by subsystem.

## Core Subsystems

### AI Agent Chat & Coding (`streaming.go`, `app.go`, `models.go`)

Manages AI coding sessions using an in-process `agent.AgentRunner` (from the `yansu-agent` monorepo sibling) that drives the Anthropic Messages API directly. Supports:
- Prompt execution with event streaming (tool calls, thinking blocks, results, errors, plan approval requests, permission prompts)
- Image attachments
- Session management (create, switch, rewind via file checkpoints)
- Model discovery (Claude Code CLI, Codex CLI)
- Session prewarming for faster first-prompt response
- Auto-compaction at 75% context window: `history_loader.go` detects `compact_boundary` rows and replaces pre-boundary history with an AI-generated summary, keeping context within limits without losing the conversation thread

### Project & Worktree Management (`project.go`, `worktree.go`, `clone.go`)

Each project maps to a local directory. Workspaces within a project are backed by git worktrees for isolated branch-based development:

- `worktree.go` — Create/remove/merge git worktrees with branch management
- `worktree_setup.go` — Post-clone setup command execution (e.g., `pnpm install`)
- `clone.go` — Repository cloning via Yansu git proxy
- `checkpoint.go` — Per-file backup snapshots for atomic session rewind (enables "rewind" button in chat UI)
- `git.go` — Git info queries (branch, status, URL parsing)

### CUA Driver System (`cua_driver_*.go`, `bundled_cua_*.go`)

Manages a bundled Computer Use Agent (CUA) driver daemon for desktop automation:

- `cua_driver_daemon.go` — Long-lived CUA driver process in `serve` mode over a Unix domain socket. Keeps the element cache hot across calls so `element_index` workflows work end-to-end. Uses `--no-relaunch` to ensure the bundled binary stays in the Yansu process tree for correct TCC attribution.
- `cua_driver_proxy.go` — JSON-RPC proxy that the frontend and host bridge call into; forwards requests to the daemon over the Unix socket with per-request timeouts.
- `cua_driver_permissions.go` — macOS accessibility permission checking and guided setup for the CUA driver's rebranded bundle (`com.yansu.cuadriver`).
- `cua_policy.go` — Policy engine that governs what CUA actions are allowed (rate limits, scope restrictions, sensitive-app blocking).
- `bundled_cua_driver.go` — Manages the bundled CUA driver binary (auto-downloaded on first launch via `scripts/download-cua-driver.sh`).
- `bundled_cua_skill.go` — Bundled CUA skill package for AI-driven computer use.

The CUA driver source (Swift) lives in `cua-driver-src/` along with its entitlements, Info.plist, and bundled Skills manifests. It is built via `cua-driver-src/build-cua-driver.sh` and bundled into the app by the build scripts.

### CUA Screenshot & Translator (`cua_translator.go`, `cua_screenshot_read.go`)

- `cua_translator.go` — Protocol bridge between the legacy trycua/cua JSON-RPC wire format and the native alma-derived CUA driver NDJSON protocol. Caches per-PID accessibility tree snapshots for element-index translation.
- `cua_screenshot_read.go` — Screenshot capture with optional OCR text extraction for CUA workflows. Reads accessibility trees and translates element references between wire formats.

### Activity Monitoring & Memory (`activity*.go`, `memory*.go`)

Continuous user activity tracking with AI-powered summarization and knowledge extraction:
- `activity_monitor.go` — Screen capture, app focus tracking, meeting detection via `internal/meetingdetect/`
- `activity_summary.go` — Daily/weekly AI-generated activity summaries (extracted into knowledge)
- `activity_summary_context.go` — Activity summary context injection for chat prompts
- `activity_transcript.go` — Meeting transcript assembly from speech events
- `activity_ocr.go` — OCR-based screenshot text extraction (macOS Vision, Windows WinRT)
- `activity_store.go` — SQLite-backed activity event store (`activity.db`)
- `activity_triage.go` — AI pipeline that converts raw activity into structured summaries/knowledge
- `activity_dedup.go` — Event deduplication
- `activity_rotator.go` — Data retention/rotation with periodic cleanup
- `activity_pipeline_runs.go` — Pipeline run tracking for activity/memory processing
- `activity_pipeline_metrics.go` — Pipeline performance metrics
- `activity_triage_cooldown.go` — Cooldown period between triage runs to prevent duplicate processing
- `activity_context_inject.go` — Recent-activity context injected into chat prompts
- `activity_migrations.go` — Activity DB schema migrations for the activity event store
- `activity_video.go` — Activity video recording with host-bridge HTTP serving for playback
- `activity_embedding.go` — Semantic search over activity OCR frames using ONNX embeddings
- `memory_generator.go` — Generative memory pipeline (triage → generate → save)
- `memory_search.go` — Hybrid search (vector embeddings via ONNX + FTS5 full-text)
- `memory_store.go` — FTS5-indexed memory entries (`memory.db`)
- `memory_embedding.go` — ONNX-based text embedding generation
- `memory_sync.go` — Memory sync with Yansu cloud
- `memory_knowledge_generator.go` — Knowledge → structured memory pipeline
- `memory_inject.go` — Memory context injection into prompts
- `memory_onnx.go` — ONNX runtime wrapper for memory (with stub for unsupported platforms)
- `memory_chunking.go` — Text chunking for embedding
- `memory_tokenizer.go` — Token counting for memory entries
- `memory_model_download.go` — ONNX model download for embeddings
- `pii/` — GLiNER ONNX-based PII detection for screenshot masking (passwords, API keys, credit cards)
- `internal/activitybuf/` — Thread-safe ring buffer for activity events with keyboard event merging

### Activity Event Pipeline (`activity_event_*.go`)

Raw activity observations are processed through a multi-stage pipeline before they reach the triage and memory systems:

- `activity_event_preprocess.go` — Normalizes raw screenshots, OCR text, and app-focus events into structured observations with deduplication and temporal grouping
- `activity_event_extract.go` — AI-powered semantic event extraction from observation sequences (titles, descriptions, categories, entity mentions)
- `activity_event_stitch.go` — Stitches related events into coherent narratives using temporal proximity and semantic similarity
- `activity_event_vision.go` — Vision-language model integration for screenshot understanding and visual context extraction
- `activity_event_entities.go` — Entity extraction and linking from activity events
- `activity_event_gating.go` — Quality gating — filters low-signal events before downstream processing
- `activity_event_repolink.go` — Detects and links repository references in activity events
- `activity_dedup.go` — Event-level deduplication to prevent duplicate processing

### Memory Dream & Corrections (`memory_dream*.go`, `memory_corrections.go`)

Periodic memory consolidation system that runs once per calendar day to clean and merge the memory store:

- `memory_dream.go` — Three-layer consolidation: (1) rule-based exact-duplicate removal and expiration, (2) vector self-similarity clustering with cosine threshold merging, (3) LLM-powered merge for borderline clusters with fact-coverage verification
- `memory_dream_archive.go` — Archive management for merged/deprecated memories
- `memory_dream_helpers.go` — Shared dream logic (similarity computation, survivor selection, synthesis validation)
- `memory_dream_bindings.go` — Wails bindings for dream status and manual trigger
- `memory_corrections.go` — User-initiated memory corrections (edit, delete, reassign) with confidence tracking

### Identity System (`identity_*.go`)

Cross-app identity resolution that links senders and contacts across different integrations (Slack, Discord, WeChat, etc.) into unified principals:

- `identity_bindings.go` — Principal and app-handle CRUD with qualified display names (e.g., "WeChat · Mom")
- `identity_debug_bindings.go` — Debug utilities for identity resolution diagnostics
- `internal/identity/` — Identity resolution engine with alias matching and confidence scoring

### Subject Attribution (`subject_attribution.go`)

Attribute messages and activity to specific people/subjects using the identity system, enabling personalized context and per-person activity summaries.

### Sender Labeler (`sender_labeler.go`)

Resolves chat senders to user-facing labels and accumulates per-session chat statistics. Caches labels per (app, sender) tuple to avoid repeated resolution.

### File Processor (`file_processor.go`)

Document ingestion pipeline supporting images, PDFs (text extraction + scanned page OCR), Word documents, Excel spreadsheets, and PowerPoint presentations. Resizes images to model-friendly dimensions and extracts text with token limits.

### Multi-Channel Integrations (`slack*.go`, `discord*.go`, `telegram*.go`, `feishu*.go`, etc.)

WebSocket-based daemon routing and message scanning for:
- **Slack** — Workspace scanner, user-defined message pipeline rules, AI-powered message segmentation for batch processing
- **Discord** — Guild message scanning, incremental scanner, webhook-based pipeline engine
- **Telegram** — Message polling, incremental scanner, pipeline engine with user-defined rules
- **Feishu/Lark** — Message scanning, incremental scanner, pipeline with health watchdog
- **Microsoft Teams** — Daemon-based message relay
- **RingCentral** — Daemon-based message relay
- **WeChat** — Long-polling monitor with message sending, file download, and auto-reply pipeline

Each integration has:
- A **daemon** (`daemon.go`) — WebSocket client connecting to the cloud relay service for real-time message routing
- An **incremental scanner** — Polling-based history scanner for AI-powered summarization
- A **message pipeline** — User-configurable rules that trigger automated actions on incoming messages
- A **segmenter** — AI batch processing for scanning large volumes of messages
- **Scan progress** reporting to the frontend

### Automation Extractor (`automation_extractor*.go`, `extractor_*.go`, `deliverables.go`)

Pipeline for automated knowledge extraction and delivery:

- `automation_extractor.go` — Core extractor that runs automated knowledge extraction tasks
- `automation_extractor_handoff.go` — Handoff protocol for chaining multiple extraction steps
- `automation_deliverable.go` — Formats extraction results as deliverables
- `deliverables.go` — Delivery system for sending results to Slack, Discord, etc.
- `extractor_record.go` — Pipeline run record management
- `extractor_run_logs.go` — Live log buffer for extractor runs
- `extractor_path_validator.go` — Path safety validation to prevent directory traversal

### Handoff System (`handoff_*.go`)

AI handoff protocol for chaining extraction steps and transferring work between sessions:

- `handoff_api.go` — Handoff lifecycle management (create, accept, reject, GC), with event streaming and stale-TTL aging
- `handoff_author.go` — Handoff author that generates context-rich handoff documents from session history
- `handoff_author_prompt.go` — Prompt engineering for handoff document generation
- `handoff_detect.go` — Detects when a session should trigger a handoff (task completion, context switch)
- `internal/handoff/` — Core handoff types and protocol definitions

### Browser Mode & Computer Use (`browser_server.go`, `host_bridge*.go`, `browser_events.go`)

Isoform runs two servers on 127.0.0.1. Any web page the user visits can send requests to 127.0.0.1, so both check every request: it comes from a loopback address, its Host is `127.0.0.1:<port>` or `localhost:<port>` (no DNS rebinding), its Origin, when sent, is one the route allows (every other Origin is refused, and CORS preflights are answered only for those), and a POST is `application/json`, which a foreign page can only send after a preflight. Each also needs a per-launch token on every route but its health check.

**Browser bridge** (`browser_server.go`, port 34116 by default) lets the frontend run in a regular browser during development: allowlisted App methods through `POST /api/call` and app events over `/ws`. Allowed Origins are the bridge itself and the Vite dev (5173 or `VITE_PORT`) and preview (4173) servers. `/api/call` and `/ws` need the per-launch token, written to `~/.yansu-agent/browser-bridge-token` (0600; `~/.isoform-agent` for the Isoform flavor) and handed to the browser by `GET /api/bridge-token`. That route asks only for the `X-Yansu-Bridge-Token-Request` header, which keeps web pages out but gives the token to any local process that asks, and with it the chats, notes and activity the allowlist reads, and the chat itself. This is a deliberate trade-off for development: the bridge is off in release builds unless turned on (`YANSU_BROWSER_BRIDGE`, or the switch under Settings > Developer tools > Browser preview, which says so), and on under `wails dev`.

**Host bridge** (`host_bridge.go`, a random port) starts on every launch, release builds included, and acts on the Mac with Isoform's own grants. Every route is declared in `routeTable()` and goes through `guard` (`host_bridge_guard.go`). Each caller has its own token, so each holds only what it needs:

| Routes | Token | Held by |
|---|---|---|
| `/api/health` | none (answers `{"ok":true}` and nothing else) | the Spotlight helper's check that Isoform is still running |
| `/api/computer-use/*`, `/api/cua/*`, `/api/open-url`, `/api/open-path`, `/api/clipboard/write`, `/api/notify` | `X-Yansu-Host-Bridge-Token`: random per launch, kept in memory, never written to disk or to child environments | nothing in the shipped app; local tools when Isoform was started with `YANSU_HOST_BRIDGE_TOKEN` (the e2e tests in `tools/test-computer-use` send it as `HOST_BRIDGE_TOKEN`) |
| `/api/spotlight/*` | `X-Yansu-Spotlight-Token`: the IPC token the supervisor generates for each helper (`--ipc-token`); refused while no helper runs | the Spotlight helper. Its webview is the only browser Origin allowed: `http://127.0.0.1:<asset port>` on macOS (the helper reports the port on stdout), `http://wails.localhost` on Windows, `wails://wails` on Linux |
| `/api/activity/*` | the media token in the `token` query parameter (`<img>` and `<video>` cannot send headers), from `GetHostBridgeMediaToken` | the activity page's screenshots and recordings; the browser preview reads it through the browser bridge |

- `browser_server.go` — HTTP/WS server, generic RPC handler, asset serving from embedded `frontend/dist`
- `browser_bridge_guard.go` — Host, Origin, Content-Type and token checks, CORS preflights, the token file
- `browser_rpc_allowlist.go` — Security allowlist for which RPC methods are exposed (reads, the chat and its sidebar; every state changer has its reason in `browser_rpc_allowlist_test.go`)
- `browser_bridge_config.go` — Whether the bridge starts (env, config, `wails dev` default)
- `host_bridge.go` — localhost-only HTTP bridge for host OS operations (screenshot, clipboard, app focus, keystrokes, mouse clicks) and its route table
- `host_bridge_guard.go` — the host bridge's Host, Origin, Content-Type and token checks, and the Spotlight webview's CORS preflights
- `host_bridge_computer_use.go` — Computer-use API endpoints (mouse, keyboard, screenshot, drag-and-drop)
- `host_bridge_cua.go` — CUA-specific bridge endpoints for the CUA driver proxy
- `host_bridge_spotlight.go` — Spotlight HTTP bridge for SSE-based chat streaming
- `browser_events.go` — WebSocket event channel definitions for browser clients
- `eventBroadcaster` — Forwards Wails EventsEmit to browser WebSocket clients for dual-mode delivery

### Spotlight (`spotlight_app.go`, `spotlight_supervisor.go`)

A Spotlight-like search overlay with integrated AI chat:

- `spotlight_app.go` — SSE-based streaming API for spotlight chat sessions. Accepts `/api/spotlight/submit` to start a chat and `/api/spotlight/stream/:id` for SSE event streaming, with a backlog buffer (cap 256 events) to bridge the gap between submission and subscriber connection.
- `spotlight_supervisor.go` — Process supervision for the spotlight helper app, managing launch, health checks, and graceful shutdown.
- `spotlight_jobobject_windows.go` — Windows job object integration for process lifecycle management.
- `host_bridge_spotlight.go` — HTTP bridge endpoints that the spotlight helper uses to communicate with the Yansu backend.

### Quick Chat (`quick_chat_*.go`)

A hotkey-activated floating chat panel that overlays on top of other apps:

- `quick_chat_window.go` — Shared HTML document and app state for the floating panel. Stores the frontmost app's bundle ID so suggestions are context-aware.
- `quick_chat_window_darwin.go` — macOS implementation using a borderless WKWebView panel with rounded corners and shadow.
- `quick_chat_window_windows.go` — Windows implementation using WebView2 in a borderless window.
- `quick_chat_hotkey.go` — Cross-platform global hotkey registration (macOS: Carbon event tap, Windows: RegisterHotKey).
- `quick_chat_context.go` — Context-aware suggestion generation based on the frontmost application. Generates relevant starter prompts from the surfaced bundle ID.
- `quick-chat-spotlight/` — Spotlight-integrated quick chat assets.
- `quick-chat-window-assets/` — HTML/CSS assets for the floating panel UI.

### Toast Windows (`toast_window*.go`)

Custom toast notification windows for macOS:

- `toast_window_darwin.go` — Native Cocoa borderless, non-key WKWebView window for rich toast notifications. Uses `YansuToastMessageHandler` for JS-to-Go message passing via `postMessage`.
- `toast_window_other.go` — Stub for non-macOS platforms.
- `toast_window_assets/` — HTML/CSS assets rendered inside the toast WebView.

### Pause Observation (`closed_eyes_mode.go`, `closed_eyes_overlay_*.go`)

Privacy control that blanks the screen with an overlay and stops activity,
microphone, and system-audio capture at the source. It remembers which primary
sources were running and restores only those that still pass current settings
and privacy gates. The macOS implementation uses a full-screen borderless
window; Windows uses a similar overlay approach.

- `closed_eyes_mode.go` — Core state management (enable/disable/toggle) with resume flags for activity and listening
- `closed_eyes_overlay_darwin.go` — macOS full-screen privacy overlay window
- `closed_eyes_overlay_windows.go` — Windows privacy overlay implementation

### Permissions System (`permissions*.go`, `permission_overlay*.go`)

Cross-platform permission management for OS-level capabilities:

- **macOS**: Screen recording (TCC), accessibility (AX API), microphone, notifications — with status checking, request dialogs, and guided overlay window
- **Windows**: Permission stubs with system tray integration
- **Linux**: Permission stubs
- `permission_overlay_darwin.go` — Full-screen permission guide overlay with animated diagrams and live status polling
- `permissions.go` — Common `Permissions` type with status checking + request initiation
- `sensitive_apps_bindings.go` — Registry of sensitive apps (password managers, keychains) that trigger full-screen blur (macOS implementation in `sensitive_apps_bindings_darwin.go`)

### Voice & Audio (`whisper.go`, `asr_sherpa*.go`, `background_audio*.go`)

- On-device voice transcription via sherpa-onnx (SenseVoice int8 + Silero VAD), loaded in-process via cgo
- Models live at `~/.yansu-agent/sherpa/` (model.int8.onnx + tokens.txt + silero_vad.onnx) — auto-downloaded on first init
- Continuous background listening (microphone + system audio) with speech-to-text for meeting transcription
- `internal/asr/asr.go` — engine-agnostic `Transcriber` interface
- `asr_sherpa.go` — sherpa-onnx implementation (darwin amd64/arm64 + windows amd64/386)
- `bundled_sherpa.go` — model auto-download with `YANSU_SHERPA_*_URL` env overrides
- `whisper.go` — voice-input lifecycle + Wails bindings (file name retained from the whisper.cpp era)
- `background_audio*.go` — System audio + microphone capture via Core Audio (macOS), with separate streams mixed into WAV
- `background_listening_bindings.go` — Go-to-JS bindings for background listening state
- `background_transcription_scheduler.go` — Scheduler for periodic background transcription runs
- `meeting_audio_writer.go` — Writes meeting audio (dual-stream mic + system) to timestamped WAV files
- `audio_capture_events.go` — Audio capture lifecycle events pushed to frontend
- `webview_media_darwin.go` — WebView media playback controls (macOS) for inline audio playback
- `BACKGROUND_AUDIO_DIAGNOSTICS.md` — Release crash notes and capture/transcription log checklist

### Voice Enrollment & Speaker ID (`voice_enrollment.go`, `voice_input_apps*.go`)

Speaker identification and enrollment system built on sherpa-onnx speaker embeddings:

- `voice_enrollment.go` — Lazy-loaded speaker embedding extractor with Self-embedding cache for real-time voice verification on every microphone chunk. Supports multiple enrollment clips per speaker for robustness across environments.
- `voice_input_apps.go` / `voice_input_apps_bindings.go` — App-specific voice input handling and Wails bindings
- `internal/speakerid/` — Speaker identification engine with similarity scoring

### Auto-Update (`version.go`)

Checks for updates every 30 minutes against a Cloudflare R2-hosted version manifest (or GitHub releases as fallback). Full `.app` bundle replacement on macOS, multi-file archive extraction on Windows/Linux (replacing the binary and any bundled DLLs/helpers atomically). Current clients read `updater_assets` from the manifest, while `assets` remains reserved for older single-file `.gz` updaters. Download progress reported to frontend via `update:status` events. The release distribution API is served by a Cloudflare Worker in `release-worker/`.

### Telemetry (`telemetry.go`, `telemetry_ce_collector.go`, `telemetry_tier_pref.go`)

PostHog-based telemetry that forwards agent lifecycle events to the same analytics project as the frontend:

- `telemetry.go` — `PostHogTelemetry` implementing `native.Telemetry` interface, injected into the yansu-agent runner. Gracefully degrades to no-op when offline or misconfigured.
- `telemetry_ce_collector.go` — Community edition telemetry collection for open-source builds
- `telemetry_tier_pref.go` — Tier preference telemetry (free/pro/enterprise plan tracking)

### Subscription & Plans (`subscription.go`)

Subscription status management with Stripe integration:

- There is no Yansu account (`noYansuAccount()` in `account_mode.go`): `GetSubscriptionStatus` answers a synthetic active, non-billable status locally and never calls the backend
- The checkout, portal and plan-change bindings remain but have no account to act on; the subscription topic is hidden in Settings

### Watchdog (`watchdog.go`)

Periodic heartbeat goroutine (30s interval) that logs process health metrics (uptime, goroutine count, CGO calls, heap usage, GC count). Helps disambiginate subsystem deadlocks from full process hangs in crash diagnostics.

### Cron Jobs & Automations (`cron.go`, `cron_api.go`, `cron_exec.go`)

Built-in job scheduler for AI-powered automated tasks:
- Schedule recurring prompts (every N minutes, hourly, daily, cron expressions)
- REST API server for CLI-based external cron management (`cron_api.go`)
- Job execution with streaming logs, run history, and result persistence
- Automation duplicate detection and reordering
- Automation delivery system via the extractor pipeline
- Missed runs: a scheduled time that passed while the Mac was asleep or Isoform was closed, and that no catch-up run covered, is stored as a run with status `missed`; resuming a paused schedule waits for its next time instead of running at once
- Upcoming (`upcoming.go`, `GetUpcoming`): one list, by next run, of the user's schedules, the morning and evening brief and the Daily News run, with last run, result in plain words and missed runs in the last seven days
- Changing Upcoming from the main chat (`upcoming_tools.go`, `upcoming_confirm.go`): the main chat's turn alone gets six tools (`UpcomingList`, `UpcomingChange`, `UpcomingPause`, `UpcomingResume`, `UpcomingRunNow`, `UpcomingRemove`) through `agent.TaskInput.ExtraTools`. Listing reads; changing, pausing and asking to remove are local writes; running now and resuming start unattended work, so they are external writes the user approves each time (scoped to the one item). No unattended run can use any of them. Proposals waiting for approval cannot be changed, turned on, run or removed from chat, work that runs only when asked is not turned into a schedule, and a schedule kept in another time zone is rewritten into local time when its time or days change. Running now waits five minutes between runs of one item from the chat. Every change posts a record line into the main chat. `UpcomingRemove` only posts a confirm card; the schedule goes when the user presses Remove on it (`ResolveUpcomingConfirm`: a card this process posted, in the main chat, within an hour; the schedule it removes comes from memory, never from the card's row)

### Skill Hub (`skill_hub.go`, `skill_hub_github.go`, `skill_hub_local.go`)

Manages discovery, installation, and lifecycle of Claude Code skills:
- **ClawHub** — Fetches and installs skills from the Yansu ClawHub registry (curated skill packages)
- **GitHub** — Installs skills directly from any GitHub repository (`owner/repo` or full URL) by downloading and extracting zip archives
- **Local folders** — Installs skills from local paths on disk (dev/offline workflow)
- `skill_hub_agent_dirs.go` — Manages per-skill agent directories
- Atomic state management with a mutex-protected read-modify-write cycle (`withSkillHubState`) to prevent concurrent install/toggle races
- 64 MiB download limit for both GitHub and ClawHub sources

### MCP Hub (`mcp_hub*.go`, `internal/mcp/`)

Manages MCP (Model Context Protocol) servers that extend the AI agent with external tools:

- `mcp_hub.go` — State management for the MCP server registry, client spawn/stop lifecycle, JSON-RPC communication
- `mcp_hub_rpc.go` — Wails RPC bindings (CRUD, start/stop, tools discovery)
- `mcp_hub_registry.go` — Server catalog with GitHub-based installation, local npm/pip spawn
- `mcp_hub_runtime.go` — Runtime discovery (node, npm, bun, uvx, python) for MCP server launch
- `mcp_hub_local.go` — Installs MCP servers from local disk paths
- `mcp_hub_prompt.go` — Generates MCP tool prompts for injection into the AI agent context
- `internal/mcp/` — Core MCP types, JSON-RPC bridge, ring buffer for server I/O, spec-based tool adapter

### MCP OAuth (`mcp_oauth.go`)

OAuth 2.0 flow for MCP servers that require third-party authentication. Manages authorization code exchange, token storage, and refresh for MCP server integrations.

### Composio MCP Gateway (`composio.go`)

Integrates with the Composio platform to provide MCP-based access to third-party app tools (e.g. Google apps) through the desktop:

- **Status & discovery** — `GetComposioStatus` fetches configured/connected apps from the Yansu backend, with fallback defaults for local dev
- **Connection flow** — `ConnectComposioApp` initiates OAuth through the backend and spawns a local MCP client; `DisconnectComposioApp` tears down the connection and removes the MCP server
- **MCP activation** — `ActivateComposioApp` creates an MCP client connected to the backend's SSE proxy at `/api/composio/mcp/{app}/sse`, making the app's tools available to the AI agent
- **Tool discovery & invocation** — `ListComposioTools` enumerates available tools; `CallComposioTool` invokes a specific tool through the MCP client
- **Auto-connect** — On startup and after OAuth login, `ensureConnectedComposioMCPActiveInBackground` automatically activates MCP for any previously connected apps
- The Composio API key never leaves the Yansu backend; the desktop receives per-user proxy URLs and attaches the standard Bearer token at runtime

### OpenCLI Integration (`opencli_integration.go`, `bundled_opencli.go`, `bundled_opencli_skill.go`)

Integration with Open CLI for external tool access:
- `opencli_integration.go` — Manages Open CLI installation, version detection, and command execution
- `bundled_opencli.go` — Bundles the Open CLI binary (auto-downloaded on first launch)
- `bundled_opencli_skill.go` / `bundled_opencli_skill_test.go` — Bundled OpenCLI skill execution
- `bundled-opencli-skills/` — Pre-packaged OpenCLI skill bundles (author, autofix, browser, usage, smart-search)

### Prediction (`prediction.go`)

Behavior prediction engine that analyzes user activity patterns to proactively suggest actions, prompt completions, and automation triggers based on historical behavior.

### Performance Analysis (`perf_analysis.go`)

Runtime performance profiling and analysis utilities. See [`PERF_ANALYSIS.md`](PERF_ANALYSIS.md) for the operator guide on using the DevTools performance panel.

### Install Discovery (`install_discovery.go`)

On every launch, writes an `install.json` marker file to the OS user config directory (`~/Library/Application Support/Yansu/` on macOS, `%APPDATA%/Yansu/` on Windows, `~/.config/Yansu/` on Linux). This serves as a discovery hook for external tools (skills, MCPs, coding agents) to locate the running desktop app without hard-coding install paths. The payload includes the executable path, app directory, bundled CLI path, version, platform, and a timestamp. Safe to call on every launch — the write is one small file via an atomic rename and any failure is logged but never blocks startup.

### Remote API (`remote_api.go`)

HTTP client for communicating with the Yansu cloud platform API. Shared across all backend-cloud interactions with consistent retry, timeout, and authentication handling.

### Theme & UI Language Preferences (`theme_pref.go`, `ui_language.go`)

- `theme_pref.go` — Detects and persists light/dark/system theme preference
- `ui_language.go` — UI language detection and switching (English / Chinese)

### i18n / Internationalization (`frontend/src/i18n/`)

The frontend uses `react-i18next` throughout, with namespace-based lazy loading:
- **Namespaces**: `common`, `app`, `chat`, `settings`, `onboarding`, `calendar`, `activity`, `knowledge`, `memory`
- **English** (`en/`) is the source of truth; **Chinese** (`zh-CN/`) is a maintained translation
- Every user-facing string in JSX goes through `t('key.path')` — hardcoded strings are a compile error via TypeScript augmentation
- Go-side translation support in `global_translation.go` for backend-originated strings
- See `frontend/src/i18n/README.md` for the full convention

### Composer Suggestions (`suggestion_generator.go`, `composer_suggestions*.go`)

Empty-state composer chips powered by AI:
- Analyzes recent activity and integrations to suggest relevant starter prompts
- Caches suggestions with TTL-based refresh
- `recommended_questions.go` — Personalized question suggestions from the backend
- `composer_suggestions_api.go` — API endpoints for suggestion management
- `composer_suggestions_defaults.go` — Default suggestion configurations

### Auth (`auth.go`, `auth_manager.go`)

Authentication with the Yansu cloud platform:
- No sign-in: the Yansu account left the product on 2026-10-12 (`noYansuAccount()` in `account_mode.go`). The app opens straight into the product and `GetUserProfile` returns the local People "self" identity
- Every backend helper (`apiGet`, `apiPost`, `apiDelete`, `apiPostMultipart`) fails closed with `errNoYansuAccount`; the daemon hub is never dialed; Slack, Discord and Telegram use their local bot setups
- A leftover `~/.yansu-agent/token` from an earlier install is ignored; the environment switch (production/staging URLs) only clears it

## Wails IPC / API Reference

All Go methods on the `App` struct are callable from the TypeScript frontend through auto-generated Wails bindings in `frontend/wailsjs/go/main/`. The bindings are regenerated automatically during `wails dev` or `wails build`.

### Calling Go from TypeScript

```typescript
// Import auto-generated bindings
import { SendPrompt, ListProjects, CreateSession } from '../../wailsjs/go/main/App';

// Call a Go method (returns a Promise)
const projects = await ListProjects();
await SendPrompt(prompt, workDir, model, sessionId, tag, planMode, thinkingEnabled);
```

### Event Streaming (Go → TypeScript)

Go emits events via `runtime.EventsEmit(ctx, eventName, payload)`. The frontend subscribes with `EventsOn`:

```typescript
import { EventsOn } from '../../wailsjs/runtime/runtime';

// Subscribe to agent streaming events
EventsOn('agent:event', (event: AgentEvent) => {
  // event.type: 'assistant' | 'tool_use' | 'thinking' | 'result' | 'error' | 'done' | 'plan_complete' | 'permission_request'
});

// Subscribe to update status
EventsOn('update:status', (status: UpdateStatus) => { ... });
```

### TypeScript Calling Go (EventsOn)

The frontend can emit events to Go using `EventsEmit`:

```typescript
import { EventsEmit } from '../../wailsjs/runtime/runtime';

// Notify Go of UI state changes
EventsEmit('frontend:window-state', { width, height, maximised });
```

### Key API Categories

| Category | Files | Description |
|----------|-------|-------------|
| **Agent / Chat** | `streaming.go`, `models.go` | Prompt execution, event streaming, model discovery, session management |
| **Projects & Worktrees** | `project.go`, `worktree.go`, `clone.go` | Project CRUD, git worktree management, repository cloning |
| **Checkpoints** | `checkpoint.go` | File backup/restore for session rewind |
| **Terminal** | `terminal.go` | PTY shell creation, resize, lifecycle |
| **Identity** | `auth.go`, `auth_manager.go` | Local self profile, base URLs, environment switch (no sign-in) |
| **CUA Driver** | `cua_driver_*.go` | Computer Use Agent daemon lifecycle and proxy |
| **Browser Bridge** | `browser_server.go`, `host_bridge*.go` | HTTP/WS server for browser-based clients |
| **Spotlight** | `spotlight_app.go`, `spotlight_supervisor.go` | Spotlight-like search overlay with AI chat |
| **Quick Chat** | `quick_chat_*.go` | Hotkey-activated floating chat panel |
| **Activity** | `activity_*.go` | Screen monitoring, OCR, summarization |
| **Memory** | `memory_*.go` | Knowledge base, hybrid search, embeddings |
| **Integrations** | `slack*.go`, `discord*.go`, `telegram*.go`, etc. | Multi-channel message pipelines |
| **Install Discovery** | `install_discovery.go` | install.json marker file for external tool discovery |
| **Cron** | `cron.go`, `cron_api.go`, `cron_exec.go` | Scheduled job execution |
| **MCP Hub** | `mcp_hub*.go` | MCP server registry and lifecycle |
| **Composio** | `composio.go` | Composio MCP gateway for Google apps and third-party integrations |
| **Skill Hub** | `skill_hub*.go` | Claude Code skill discovery and installation |
| **Voice** | `whisper.go`, `asr_sherpa.go`, `background_audio*.go` | Speech-to-text, background listening |
| **Permissions** | `permissions*.go` | OS-level permission management |
| **Auto-Update** | `version.go` | Version checking and self-update |
| **Identity** | `identity_bindings.go`, `identity_debug_bindings.go` | Principal/contact management with cross-app resolution |
| **Memory Dream** | `memory_dream*.go`, `memory_corrections.go` | Periodic memory consolidation and user corrections |
| **Handoff** | `handoff_*.go` | AI handoff protocol for cross-session work transfer |
| **Subject Attribution** | `subject_attribution.go` | Message/activity attribution to people |
| **File Processor** | `file_processor.go` | Document ingestion (PDF, images, Office docs) |
| **Pause Observation** | `closed_eyes_mode.go` | Privacy overlay that stops screen and audio capture |
| **Voice Enrollment** | `voice_enrollment.go`, `voice_input_apps*.go` | Speaker ID enrollment and verification |
| **Telemetry** | `telemetry.go` | PostHog analytics integration |
| **Subscription** | `subscription.go` | Plan/status management with Stripe |
| **MCP OAuth** | `mcp_oauth.go` | OAuth flow for MCP server authentication |
| **Performance** | `perf_analysis.go` | Runtime profiling and analysis |
| **Remote API** | `remote_api.go` | Yansu cloud platform HTTP client |
| **Theme / Language** | `theme_pref.go`, `ui_language.go` | Light/dark theme and UI language |
| **Watchdog** | `watchdog.go` | Periodic process health heartbeat |
| **i18n** | `global_translation.go` | Go-side translation support |

## Storage

Three SQLite databases under `~/.yansu-agent/`:

| Database | Path | Managed By | Contents |
|----------|------|------------|----------|
| `sessions.db` | `~/.yansu-agent/sessions.db` | `storage/` package | Projects, workspaces, sessions, stream items, checkpoints, cron jobs, SSH configs, sidebar groups, composer suggestions, extractor records, UI state |
| `activity.db` | `~/.yansu-agent/activity.db` | `activity_store.go` | Activity snapshots, AI-generated summaries, event pipeline runs |
| `memory.db` | `~/.yansu-agent/memory.db` | `memory_store.go` | FTS5-indexed memory entries with vector embeddings (ONNX) |

Schema is auto-migrated on startup. See [`storage/README.md`](storage/README.md) for the sessions.db migration history.

## Getting Started

### Prerequisites

- Go 1.25+
- Node.js 18+ with pnpm
- [Wails CLI](https://wails.io/docs/gettingstarted/installation):
  ```bash
  go install github.com/wailsapp/wails/v2/cmd/wails@latest
  ```

### Development

```bash
# One-command dev start (checks prerequisites + wails dev)
cd wails-gui
./scripts/start-wails-gui.sh

# Or skip specific bundled asset checks
./scripts/start-wails-gui.sh --skip-cli --skip-bun
```

The dev server starts the Vite dev server (with HMR) alongside the Go backend. Wails proxies the frontend and regenerates Go↔JS bindings on Go changes.

### Production Build

```bash
# Basic production build
./scripts/build-wails.sh

# With explicit version
./scripts/build-wails.sh --version 0.2.0

# macOS DMG installer
./scripts/build-wails.sh --dmg

# Cross-compilation
./scripts/build-wails.sh --platform darwin/arm64
```

The build script:
1. Installs frontend dependencies and runs Vite build
2. Compiles the Go backend with Wails
3. Bundles runtimes (bun, ffmpeg, git, yansu CLI, open CLI, cua-driver) and sherpa-onnx native libs (`Contents/Frameworks/libsherpa-onnx-*.dylib` + `libonnxruntime.dylib` on macOS, the equivalent DLLs next to `Yansu.exe` on Windows) into the app bundle
4. On macOS: creates `.app` bundle with optional code signing and DMG

### Isoform Dev Builds and macOS Privacy Grants

```bash
# One-time: the "Isoform Dev" signing identity (no password, no dialogs)
./scripts/dev-signing-identity.sh create

# Development build: Isoform.app, bundle id com.wails.Isoform, data in ~/.isoform-agent
./scripts/build-wails.sh --flavor isoform

# Check the signature the privacy grants are keyed to
codesign -d -r- build/bin/Isoform.app
#   designated => identifier "com.wails.Isoform" and certificate leaf = H"<sha1>"
```

macOS keys an app's privacy grants (Screen Recording, Accessibility, Microphone, Contacts, Automation) to its code-signing designated requirement, and it checks that requirement every time the app asks. An ad-hoc signature (`codesign --sign -`) has the binary's own cdhash as its requirement, so every rebuild invalidates the previous build's grants: System Settings keeps showing the switches on, but the new binary is denied and the app prompts again. That is why `build-wails.sh --flavor isoform` and `start-wails-gui.sh` clear the Isoform grants after an ad-hoc build (`scripts/reset-dev-permissions.sh`): the app then asks once instead of being denied with the switches on.

`scripts/dev-signing-identity.sh create` ends the cycle. It makes a self-signed "Isoform Dev" certificate and keeps it in a keychain of its own, `~/.isoform-agent/dev-signing/identity.keychain-db`, locked with a random password stored beside it (mode 600). The keychain is not in the search list and the certificate is not trusted: `codesign` needs neither to sign, TCC only compares the leaf hash, and Gatekeeper never assesses a locally built app. Keeping the key out of the login keychain is what makes signing unattended; the login keychain asks for the account password before a tool may use a key, which is also why `security find-identity -v` does not list this identity (`security find-identity -p codesigning ~/.isoform-agent/dev-signing/identity.keychain-db` does).

With the identity in place:

- `build-wails.sh --flavor isoform` signs the nested helpers (CuaDriver.app, quick-chat-spotlight.app, the OCR and PII helpers, the bundled dylibs) and then Isoform.app with it, with the same entitlements as a release build and without hardened runtime, timestamp or notarization. The designated requirement is `identifier "com.wails.Isoform" and certificate leaf = H"<sha1>"` for every build, so the grants stay valid across rebuilds.
- An identity-signed build clears the grants only when they were made for a differently signed build: `reset-dev-permissions.sh --if-signature-changed` compares the build's requirement with the record of the identity-signed build whose grants were last cleared (`~/.isoform-agent/dev-signing/privacy-grants-requirement`; per user, like the grants, so builds from several worktrees share it), clears when they differ or there is no record, and updates the record. An ad-hoc build is cleared every time, as before, and drops the record; a path that leaves an ad-hoc bundle behind without clearing (an ad-hoc build or dev run with `--keep-permissions`, a hook that could not sign) drops it too (`reset-dev-permissions.sh --forget`), so the next identity-signed build clears once again rather than trusting a stale record. `--keep-permissions` skips every clear.
- `start-wails-gui.sh` exports `YANSU_DEV_SIGNING=1`, and the `postBuildHooks` entry in `wails.json` runs `scripts/sign-dev-build.sh` after every Go rebuild in `wails dev`, after Wails's own ad-hoc signing, so the dev bundle keeps the same requirement across Go changes. The hook signs only a bundle whose `CFBundleIdentifier` is `com.wails.Isoform` and only when the variable is set; `build-wails.sh` unsets it before its own `wails build`, so a value leaked from the shell or `.env.local` cannot re-sign a Yansu build. Wails hides hook output unless it fails, so the hook also writes to `~/.isoform-agent/dev-signing/sign-dev-build.log` and to the terminal.
- Without the identity both scripts fall back to ad-hoc signing, say so in the log, clear the grants, and point at `dev-signing-identity.sh create`.
- `dev-signing-identity.sh remove` deletes the keychain and the record and clears the Isoform grants (deleting the key revokes nothing by itself; the grants stay keyed to the certificate until they are cleared). The next build is ad-hoc and asks for its permissions. Release builds with `APPLE_SIGNING_IDENTITY` are unaffected; a `--flavor isoform` Developer ID build drops the record, since its grants are keyed to the Developer ID requirement. A dev run is `com.wails.Isoform` whatever `YANSU_APP_FLAVOR` says (`build/darwin/Info.dev.plist`), so it is signed and cleared the same way in both flavors; the flavor only picks the data folder.
- The record only tracks what these scripts did. After running Isoform from an older checkout (scripts without this signing step), a raw `wails dev` outside `start-wails-gui.sh`, or an older Isoform.app you reopened, run `./scripts/reset-dev-permissions.sh --forget` so the next identity-signed build clears the grants that were made for that other signature; otherwise it reports "unchanged" while Isoform is denied with the switches on. The hook does this on its own for a raw `wails dev` in this checkout.

Security note: the private key signs without a prompt by design, so any process running as your user can sign code as `com.wails.Isoform` with it and inherit whatever privacy grants Isoform holds. That is the same exposure as a login-keychain key marked "Always Allow" for codesign, and it is bounded the same way: the key is imported non-extractable, only `codesign` is on its access list, the keychain is not in the search list, and `remove` clears the grants so that a copy of the keychain and its password file taken earlier no longer buys anything. Run `./scripts/dev-signing-identity.sh remove` when you are not working on Isoform, and treat `~/.isoform-agent/dev-signing/` like any other secret.

### Download Bundled Assets (Manual)

```bash
./scripts/download-bun.sh           # Bun runtime (bundled JS execution)
./scripts/download-ffmpeg.sh        # FFmpeg (activity video transcoding)
./scripts/download-git.sh           # Portable Git for Windows
./scripts/download-cua-driver.sh    # CUA driver (computer-use automation)

# Speech-to-text models (SenseVoice + Silero VAD) are downloaded lazily on
# first voice-input attempt by bundled_sherpa.go; override the URLs with
# YANSU_SHERPA_SENSE_VOICE_MODEL_URL / YANSU_SHERPA_SENSE_VOICE_TOKENS_URL /
# YANSU_SHERPA_SILERO_VAD_URL for air-gapped deploys.
```

## Configuration

### wails.json

```json
{
  "name": "Yansu",
  "outputfilename": "Yansu",
  "frontend:dir": "frontend",
  "info": {
    "productVersion": "0.1.301",
    "protocols": [{ "scheme": "yansu", "description": "Yansu deep link" }]
  }
}
```

### Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `ISOFORM_DESKTOP_REMOTE_ACCESS` | on | Build-time default for iroh remote access; `0` opts out, `1` stamps enabled |
| `ISOFORM_MOBILE_REMOTE_ACCESS` | on unless set to `0` | Runtime override: `1` enables remote access; `0` disables it |
| `YANSU_URL` | — | Override the Yansu platform base URL (e.g. `http://localhost:3000`) |
| `ANTHROPIC_MODEL` | — | Override default Claude model (sets all tiers) |
| `ANTHROPIC_DEFAULT_SONNET_MODEL` | — | Override Sonnet-tier model |
| `ANTHROPIC_DEFAULT_OPUS_MODEL` | — | Override Opus-tier model |
| `ANTHROPIC_DEFAULT_HAIKU_MODEL` | — | Override Haiku-tier model |
| `YANSU_ANTHROPIC_URL` | — | Override Anthropic API base URL |
| `YANSU_ANTHROPIC_TOKEN` | — | Override Anthropic API token |
| `BROWSER_PORT` | `34116` | Port for browser-mode HTTP/WebSocket bridge |
| `YANSU_GIT_TIMEOUT_MS` | `90000` | Timeout for git operations (ms) |
| `YANSU_GIT_CLONE_TIMEOUT_MS` | — | Timeout for git clone operations (ms) |
| `YANSU_PPROF` | — | Enable pprof profiling (`1` for port 6060, or custom port) |
| `YANSU_BROWSER_BRIDGE` | — | Enable browser bridge server (`1`/`true`/`on` to enable) |
| `YANSU_BROWSER_BRIDGE_ORIGINS` | — | Extra comma-separated origins the browser bridge answers (e.g. `http://localhost:3000`) |
| `YANSU_HOST_BRIDGE_TOKEN` | random | Host-bridge token for this launch (32 or more characters; shorter values are ignored), for the computer-use e2e tests |
| `YANSU_CONTEXT_WINDOW_OVERRIDE` | — | Override AI context window size |
| `YANSU_GITHUB_API_URL` | — | Override GitHub API base URL |
| `YANSU_GITHUB_ARCHIVE_URL` | — | Override GitHub archive download URL |
| `ISOFORM_DESKTOP_POSTHOG_KEY` | — | PostHog project ingest key, build time only (`build-wails.sh`; deprecated `YANSU_DESKTOP_POSTHOG_KEY`) |
| `ISOFORM_DESKTOP_POSTHOG_HOST` | — | PostHog ingest origin, build time only (`build-wails.sh`; deprecated `YANSU_DESKTOP_POSTHOG_HOST`) |
| `YANSU_SHERPA_ALLOWED_LANGS` | — | Comma-separated allowed languages for speech recognition |
| `YANSU_SHERPA_LANGUAGE` | — | Language hint for speech recognition |
| `ANTHROPIC_API_KEY` | — | Anthropic API key (alternative to token) |
| `ANTHROPIC_AUTH_TOKEN` | — | Anthropic auth token (alternative to API key) |
| `OPENAI_API_KEY` | — | OpenAI API key for fallback model routing |
| `OPENAI_BASE_URL` | — | OpenAI API base URL override |
| `GITHUB_TOKEN` | — | GitHub token for Skill Hub (raises rate limits) |
| `CLAWHUB_REGISTRY` | — | Override ClawHub skill registry URL |

Remote access over iroh is on by default. Set
`ISOFORM_DESKTOP_REMOTE_ACCESS=0` at build time to opt out, or
`ISOFORM_MOBILE_REMOTE_ACCESS=0` at runtime to disable it, even when the build
stamp enables it.

Model env vars are read from the user's login shell so they apply even when launching from the macOS Dock or Spotlight.

### Deep Link Protocol

The app registers the `yansu://` URI scheme for deep linking on macOS and Windows. On macOS, `main.go` handles `OnUrlOpen` to route incoming links.

## Directory Layout

```
wails-gui/
├── main.go                     # Wails app entry point, window config, logging, hidden console
├── app.go                      # App struct — core lifecycle, state fields, startup/shutdown orchestration
├── streaming.go                # Prompt execution, event streaming, context injection (activity, memory, soul)
├── models.go                   # AI model discovery (Claude CLI, Codex CLI, OpenAI-compatible)
├── checkpoint.go               # File backup/restore for session rewind
├── terminal.go                 # PTY shell creation, resize, lifecycle
├── permissions.go              # Cross-platform permission checks (screen recording, accessibility, mic)
├── permissions_darwin.go       # macOS-specific TCC permission handling
├── permissions_linux.go        # Linux permission stubs
├── permissions_windows.go      # Windows permission stubs
├── permission_overlay_darwin.go # macOS full-screen permission guide overlay
├── sensitive_apps_bindings.go  # Sensitive app detection (password managers, keychains)
├── closed_eyes_mode.go         # Privacy overlay state management
├── cron.go                     # CRON job scheduling infrastructure
├── cron_api.go                 # REST API server for external cron management
├── cron_exec.go                # Cron job execution engine
├── daemon.go                   # WebSocket-based platform relay daemon (Slack, Discord, Teams, etc.)
├── auth.go                     # Local self profile, base URLs, environment switch (no sign-in)
├── auth_manager.go             # Centralized auth fallback chain manager
├── cua_driver_daemon.go        # CUA driver daemon lifecycle (Unix socket, JSON-line protocol)
├── cua_driver_proxy.go         # JSON-RPC proxy forwarding requests to the CUA daemon
├── cua_driver_permissions.go   # CUA driver macOS accessibility permission setup
├── cua_policy.go               # CUA action policy engine (rate limits, scope restrictions)
├── cua_translator.go           # Legacy/native CUA protocol bridge with per-PID snap cache
├── cua_screenshot_read.go      # Screenshot capture + OCR for CUA workflows
├── host_bridge.go              # Localhost HTTP bridge for host OS operations
├── host_bridge_guard.go        # Host bridge request checks and tokens
├── host_bridge_computer_use.go # Computer-use API (mouse, keyboard, screenshot)
├── host_bridge_cua.go          # CUA bridge endpoints
├── host_bridge_spotlight.go    # Spotlight HTTP bridge for SSE chat streaming
├── browser_server.go           # Browser-mode HTTP/WS server (port 34116)
├── browser_events.go           # Browser event channel definitions
├── browser_bridge_config.go    # Bridge configuration (port, CORS, interface)
├── browser_rpc_allowlist.go    # Security allowlist for exposed RPC methods
├── spotlight_app.go            # Spotlight SSE streaming API + backlog buffer
├── spotlight_supervisor.go     # Spotlight helper app process supervision
├── quick_chat_window.go        # Quick Chat shared state + HTML document
├── quick_chat_window_darwin.go # Quick Chat macOS floating panel (WKWebView)
├── quick_chat_window_windows.go # Quick Chat Windows floating panel (WebView2)
├── quick_chat_hotkey.go        # Quick Chat cross-platform global hotkey registration
├── quick_chat_context.go       # Context-aware suggestion generation for Quick Chat
├── toast_window_darwin.go      # macOS custom toast notification windows (WKWebView)
├── whisper.go                  # Voice-input lifecycle + Wails bindings (delegates to asr.Transcriber)
├── asr_sherpa.go               # sherpa-onnx (SenseVoice int8 + Silero VAD) implementation
├── asr_sherpa_other.go         # Stub for unsupported platforms
├── bundled_sherpa.go           # SenseVoice / Silero VAD model auto-download
├── background_audio.go         # Background speech capture infrastructure
├── background_audio_darwin.go  # macOS Core Audio system audio + microphone capture
├── background_listening_bindings.go # Background listening Go ↔ JS bindings
├── background_transcription_scheduler.go # Periodic background transcription scheduler
├── audio_capture_events.go     # Audio capture lifecycle events
├── webview_media_darwin.go     # WebView media playback controls (macOS)
├── meeting_audio_writer.go     # Dual-stream meeting audio recording (WAV)
├── menubar_darwin.go           # macOS menu bar / system tray
├── menubar_windows.go          # Windows system tray
├── activity_monitor.go         # Continuous user activity tracking
├── activity_event_preprocess.go # Raw activity observation normalization
├── activity_event_extract.go   # AI-powered semantic event extraction
├── activity_event_stitch.go    # Event stitching into coherent narratives
├── activity_event_vision.go    # Vision-language screenshot understanding
├── activity_event_entities.go  # Entity extraction from activity events
├── activity_event_gating.go    # Quality gating for low-signal events
├── activity_event_repolink.go  # Repository reference detection
├── activity_summary.go         # AI-powered daily/weekly activity summaries
├── activity_summary_context.go # Activity summary context injection for prompts
├── activity_transcript.go      # Meeting transcript assembly
├── activity_triage.go          # Raw activity → structured knowledge pipeline
├── activity_store.go           # SQLite-backed activity event store
├── activity_ocr.go             # OCR-based screenshot text extraction
├── activity_dedup.go           # Activity event deduplication
├── activity_rotator.go         # Activity data retention/rotation
├── activity_pipeline_runs.go   # Pipeline run tracking for activity/memory processing
├── activity_pipeline_metrics.go # Pipeline run performance metrics
├── activity_triage_cooldown.go  # Cooldown between triage runs
├── activity_context_inject.go  # Recent-activity context injected into chat prompts
├── activity_migrations.go      # Activity DB schema migrations
├── activity_video.go            # Activity video recording and host-bridge serving
├── activity_embedding.go        # Semantic search over activity OCR frames
├── memory.go                   # Core memory system
├── memory_generator.go         # Generative memory pipeline (triage → generate → save)
├── memory_search.go            # Hybrid search (vector + FTS5)
├── memory_store.go             # FTS5-indexed memory database
├── memory_embedding.go         # ONNX-based text embeddings
├── memory_sync.go              # Memory sync with Yansu cloud
├── memory_knowledge_generator.go # Knowledge → structured memory pipeline
├── memory_inject.go            # Memory context injection into prompts
├── memory_onnx.go              # ONNX runtime wrapper for memory
├── memory_chunking.go          # Text chunking for embedding
├── memory_tokenizer.go         # Token counting for memory entries
├── memory_model_download.go    # ONNX model download for embeddings
├── memory_dream.go             # Periodic memory consolidation (dedup, cluster, LLM merge)
├── memory_dream_archive.go     # Archive management for merged memories
├── memory_dream_helpers.go     # Shared dream logic
├── memory_corrections.go       # User-initiated memory corrections
├── identity_bindings.go        # Principal/contact CRUD with app handles
├── identity_debug_bindings.go  # Identity resolution diagnostics
├── subject_attribution.go      # Message/activity attribution to people
├── sender_labeler.go           # Chat sender label resolution + stats
├── file_processor.go           # Document ingestion (PDF, images, Office)
├── slack.go                    # Slack integration
├── slack_pipeline.go           # Slack message pipeline engine
├── slack_message_pipeline.go   # Slack message rule processing
├── slack_incremental_scanner.go # Slack history scanner
├── slack_segmenter.go          # Slack AI batch segmenter
├── discord.go                  # Discord integration
├── discord_scan_filter.go      # Discord per-guild channel filter
├── discord_pipeline.go         # Discord message pipeline
├── discord_message_pipeline.go # Discord message rule processing
├── discord_incremental_scanner.go # Discord history scanner
├── discord_segmenter.go        # Discord AI batch segmenter
├── telegram.go                 # Telegram integration
├── telegram_message_pipeline.go # Telegram message pipeline
├── telegram_incremental_scanner.go # Telegram history scanner
├── telegram_segmenter.go       # Telegram AI batch segmenter
├── feishu.go                   # Feishu/Lark integration
├── feishu_incremental_scanner.go # Feishu history scanner
├── feishu_pipeline.go          # Feishu message pipeline
├── feishu_segmenter.go         # Feishu AI batch segmenter
├── teams.go                    # Microsoft Teams daemon
├── ringcentral.go              # RingCentral daemon
├── wechat.go                   # WeChat integration
├── wechat_api.go               # WeChat API client
├── wechat_download.go          # WeChat file download utilities
├── wechat_monitor.go           # WeChat long-polling message monitor
├── wechat_send.go              # WeChat message sending
├── wechat_types.go             # WeChat types and constants
├── version.go                  # Version info, auto-update (check + download + install)
├── prediction.go               # Behavior prediction for proactive suggestions
├── subscription.go             # Subscription status and plan queries
├── mcp_hub.go                  # MCP server state management + install/spawn lifecycle
├── mcp_hub_rpc.go              # MCP server Wails RPC bindings (CRUD, start/stop, tools)
├── mcp_hub_registry.go         # MCP server catalog with GitHub-based installation
├── mcp_hub_runtime.go          # Runtime discovery for MCP server launch (node, npm, bun, uvx)
├── mcp_hub_local.go            # MCP server installation from local disk paths
├── mcp_hub_prompt.go           # MCP tool prompt injection into AI agent context
├── composio.go                 # Composio MCP gateway (Google apps + third-party integrations)
├── skill_hub.go                # Skill Hub state management + ClawHub registry client
├── skill_hub_github.go         # GitHub-based skill installation (owner/repo zip download)
├── skill_hub_local.go          # Local folder skill installation
├── skill_hub_agent_dirs.go     # Per-skill agent directory management
├── opencli_integration.go      # Open CLI installation, version detection, execution
├── suggestion_generator.go     # Composer suggestion generation
├── recommended_questions.go    # Personalized question suggestions
├── composer_suggestions_api.go # Composer suggestions API endpoints
├── composer_suggestions_defaults.go # Default suggestion configurations
├── knowledge.go                # Knowledge base CRUD
├── project.go                  # Project detection, Yansu API sync, knowledge push/pull
├── worktree.go                 # Git worktree CRUD, workspace settings
├── worktree_setup.go           # Post-clone setup command execution
├── clone.go                    # Repository cloning via Yansu git proxy
├── persistence.go              # Load/save projects, sessions, stream items (SQLite wrapper)
├── history_loader.go           # Conversation history loading with compaction support
├── conversation_history.go     # Conversation history formatting
├── triage_log.go               # Triage log for crash diagnostics
├── subject_attribution.go      # Message/activity attribution to people
├── sender_labeler.go           # Chat sender label resolution + stats
├── file_processor.go           # Document ingestion (PDF, images, Office)
├── ai_memory_scanner.go        # AI memory import scanner
├── scan_progress.go            # Scan progress reporting
├── claude_model_config.go      # Claude model configuration
├── deliverables.go             # Automation delivery system
├── automation_deliverable.go   # Deliverable result formatting
├── automation_extractor.go     # Extractor record automation
├── automation_extractor_handoff.go # Extractor handoff protocol
├── extractor_record.go         # Extractor pipeline run record management
├── extractor_run_logs.go       # Extractor live log buffer
├── extractor_path_validator.go # Extractor path safety validation
├── handoff_api.go              # Handoff lifecycle + event streaming
├── handoff_author.go           # Handoff document generation from session history
├── handoff_author_prompt.go    # Handoff prompt engineering
├── handoff_detect.go           # Handoff trigger detection
├── json_helpers.go             # JSON serialization utilities
├── remote_api.go               # Remote API client for Yansu cloud platform
├── http_client.go              # Shared HTTP client
├── telemetry.go                # PostHog telemetry sink
├── telemetry_ce_collector.go   # Community edition telemetry collection
├── telemetry_tier_pref.go      # Tier preference telemetry
├── watchdog.go                 # Periodic process health heartbeat
├── voice_enrollment.go         # Speaker ID enrollment and verification
├── voice_input_apps.go         # App-specific voice input handling
├── mcp_oauth.go                # OAuth flow for MCP servers
├── mcp_hub_trusted.go          # Trusted MCP server management
├── download_helpers.go         # File download utilities
├── perf_analysis.go            # Performance analysis profiling
├── claude_cleanup_unix.go      # Claude process cleanup (Unix)
├── claude_cleanup_windows.go   # Claude process cleanup (Windows)
├── bun_cleanup_unix.go         # Bun process cleanup (Unix)
├── bun_cleanup_windows.go      # Bun process cleanup (Windows)
├── cli_status.go               # CLI status reporting
├── global_translation.go       # Go-side translation support
├── install_discovery.go        # Writes install.json marker for external tool discovery
├── theme_pref.go               # Theme preference detection (light/dark)
├── ui_language.go              # UI language detection
├── open_external.go            # Open URLs in system browser
├── open_folder.go              # Reveal folder in system file manager
├── fonts.go                    # Font asset embedding
├── bundled_assets.go           # Embedded asset management
├── bundled_bun.go              # Bundled Bun runtime management
├── bundled_ffmpeg.go           # Bundled FFmpeg management
├── bundled_git.go              # Bundled Git management
├── bundled_opencli.go          # Bundled Open CLI management
├── bundled_*.go                # Bundled runtime management (sherpa, cua-driver, cua-skill, opencli-skill)
├── wails.json                  # Wails build configuration
├── ARCHITECTURE.md             # Deep architecture documentation
├── HOT_RELOAD_TROUBLESHOOTING.md # Dev server hot reload guide
├── PERF_ANALYSIS.md            # Performance analysis operator guide
├── instructions.md             # AI-assisted development instructions
│
├── storage/                    # SQLite persistence layer
│   ├── storage.go              # Store initialization, migrations (v1–v40)
│   ├── projects.go             # Project CRUD
│   ├── sessions.go             # Session CRUD
│   ├── workspaces.go           # Workspace CRUD
│   ├── items.go                # Stream item persistence (cursor pagination)
│   ├── checkpoints.go          # File backup/restore records
│   ├── sidebar_groups.go       # Custom sidebar group CRUD
│   ├── cron.go                 # Cron job storage
│   ├── composer_suggestions.go # Composer suggestion cache
│   ├── extractor.go            # Extractor pipeline run records
│   ├── principals.go           # Identity principal CRUD and merge
│   ├── voice_embeddings.go     # Speaker embedding storage
│   ├── ui_state.go             # UI state persistence
│   ├── types.go                # Shared data types
│   └── types_identity.go       # Identity-related row types
│
├── internal/                   # Core internal packages
│   ├── terminal/               # PTY manager + WebSocket bridge for xterm.js
│   ├── persistence/            # Batch-optimized frontend state persistence
│   ├── worktree/               # Git worktree lifecycle management
│   ├── pii/                    # GLiNER ONNX PII detection + sensitive app masking
│   ├── sshconn/                # SSH connection management + remote deployment
│   ├── meetingdetect/          # Video call app detection
│   ├── activitybuf/            # Thread-safe activity event ring buffer
│   ├── mcp/                    # Model Context Protocol bridge + tool adapter + spec types
│   ├── asr/                    # Engine-agnostic ASR transcriber interface
│   ├── singleinstance/         # Cross-platform single-instance lock + IPC messaging
│   ├── handoff/                # Handoff protocol types and definitions
│   ├── identity/               # Principal resolution and alias matching
│   ├── speakerid/              # Speaker identification / enrollment engine
│   ├── wsl/                    # WSL integration (Windows only)
│   ├── onnxdl/                 # ONNX Runtime download and extraction
│   ├── ocr/                    # OCR evaluation tools
│   │   └── eval/               # OCR benchmark suite
│   └── platform/               # Cross-platform OS utilities
│       ├── activitymon/        # User activity monitoring (screen capture, clicks)
│       │   └── appparser/      # App-specific UI element parsers
│       ├── appdetect/          # Installed app detection
│       ├── browser/            # Browser detection
│       ├── clipboard/          # System clipboard access
│       ├── cmdutil/            # Command execution (Windows console-hiding)
│       ├── fonts/              # System font enumeration
│       ├── notify/             # Desktop notifications
│       ├── ocr/                # OCR (macOS Vision, Windows WinRT)
│       └── shellenv/           # Shell PATH resolution
│
├── frontend/                   # React + TypeScript + TailwindCSS
│   ├── src/
│   │   ├── main.tsx            # Entry point
│   │   ├── App.tsx             # Root component
│   │   ├── hooks/              # All state (useAgentStreaming, useProjects, useAuth, etc.)
│   │   ├── components/         # UI components (sidebar, composer, stream, terminal, etc.)
│   │   ├── lib/                # Utilities (browser-bridge, wails-shims, stream-helpers)
│   │   ├── themes/             # Theme definitions
│   │   ├── i18n/               # Internationalization (en + zh-CN namespaces)
│   │   └── types.ts            # TypeScript interfaces
│   └── wailsjs/                # Auto-generated Wails Go↔JS bindings
│
├── scripts/                    # Build and download scripts
│   ├── start-wails-gui.sh      # Dev launcher with prerequisite checks
│   ├── build-wails.sh          # Production build with bundle embedding
│   ├── build-cua-driver-bundle.sh # Build and bundle the CUA driver
│   ├── build-yansu-cli.sh      # Build the yansu CLI binary
│   ├── download-bun.sh         # Download Bun runtime
│   ├── download-ffmpeg.sh      # Download FFmpeg binary
│   ├── download-git.sh         # Download portable Git
│   ├── download-cua-driver.sh  # Download CUA driver binary
│   ├── download-paddleocr.sh   # Download PaddleOCR models
│   ├── profile-on-burst.sh     # CPU profiling helper during activity bursts
│   ├── pii-helper.entitlements.plist # PII helper entitlements
│   ├── spotlight.entitlements.plist  # Spotlight helper entitlements
│   └── patches/                # Patches for bundled tools
│
├── tools/                      # Developer tools (test-computer-use)
│   └── test-computer-use/      # E2E test for computer-use API
├── cmd/                        # CLI tools
│   ├── pii-test/               # PII detection CLI test tool
│   ├── pii-helper/             # PII detection helper utilities
│   ├── pii-eval/               # PII model evaluation
│   ├── pii-gen-dataset/        # PII training dataset generator
│   ├── sherpa-bench/           # sherpa-onnx benchmarking tool
│   ├── speakerid-poc/          # Speaker identification proof-of-concept
│   ├── ax-tree-dump/           # macOS accessibility tree dumper
│   ├── ocr-eval/               # OCR evaluation suite
│   └── ocr-helper/             # OCR helper utilities
├── bundled-opencli-skills/     # Bundled OpenCLI skill packages
│   ├── opencli-adapter-author/ # Author adapter skill
│   ├── opencli-autofix/        # Auto-fix skill
│   ├── opencli-browser/        # Browser automation skill
│   ├── opencli-usage/          # Usage analytics skill
│   └── smart-search/           # Smart search skill
├── cua-driver-src/             # CUA driver source, entitlements, bundled Skills
│   ├── src/                    # Swift source for the CUA driver helper
│   ├── Skills/                 # Bundled CUA skills (JSON manifests)
│   └── build-cua-driver.sh     # CUA driver build script
├── release-worker/             # Cloudflare Worker for release distribution (auto-update)
├── quick-chat-spotlight/       # Spotlight-integrated quick chat assets
├── quick-chat-window-assets/   # Quick Chat floating panel HTML/CSS
├── toast_window_assets/        # Toast notification HTML/CSS
├── assets/                     # Icons and app assets (appicon, menubar-icon)
├── build/                      # Build output and macOS bundling artifacts
```

## Testing

```bash
# Go tests (all packages)
cd wails-gui && go test ./...

# Go vet
cd wails-gui && go vet ./...

# Storage tests
cd wails-gui && go test ./storage/... -v

# Internal package tests
cd wails-gui && go test ./internal/...

# Frontend tests
cd wails-gui/frontend && pnpm test

# PII detection tool
cd wails-gui && go run ./cmd/pii-test

# Computer-use E2E test
cd wails-gui && go run ./tools/test-computer-use
```

## Key Dependencies

### Go

| Dependency | Purpose |
|------------|---------|
| `github.com/wailsapp/wails/v2` v2.12.0 | Desktop app framework |
| `modernc.org/sqlite` v1.44.3 | Pure Go SQLite driver (no CGo) |
| `github.com/gorilla/websocket` v1.5.3 | WebSocket for terminal I/O & daemon connections |
| `github.com/yalue/onnxruntime_go` v1.26.0 | ONNX inference (PII detection, memory embeddings) |
| `github.com/robfig/cron/v3` v3.0.1 | Job scheduling |
| `github.com/energye/systray` v1.0.3 | System tray / menu bar |
| `github.com/aymanbagabas/go-pty` v0.2.2 | PTY creation and resizing |
| `github.com/k2-fsa/sherpa-onnx-go` v1.13.0 | On-device speech-to-text |
| `github.com/mark3labs/mcp-go` v0.48.0 | Model Context Protocol server/client |
| `golang.org/x/crypto` v0.50.0 | SSH client, crypto utilities |
| `github.com/ledongthuc/pdf` | PDF text extraction |
| `github.com/pdfcpu/pdfcpu` v0.12.1 | PDF processing |
| `github.com/xuri/excelize/v2` v2.10.1 | Excel spreadsheet processing |
| `yansu-agent` (local) | AI agent runner (monorepo sibling at `../yansu-agent`) |

### Frontend

| Dependency | Purpose |
|------------|---------|
| React 18, TypeScript 5.6, Vite 5.4 | Core stack |
| TailwindCSS 4.1 | Utility CSS |
| `@xterm/xterm` + addons | Terminal emulation (WebGL renderer, fit, unicode) |
| `@radix-ui/*` | Headless accessible UI primitives |
| `@dnd-kit/*` | Drag-and-drop sidebar reordering |
| `cmdk` | Command palette and slash-command autocomplete |
| `lucide-react` | Icons |
| `sonner` | Toast notifications |
| `react-virtuoso` | Virtual scrolling for large lists |
| `react-markdown` + `remark-gfm` | Markdown rendering with GFM support |
| `react-syntax-highlighter` | Syntax highlighting for code blocks |
| `react-day-picker` | Calendar / date-picker UI |
| `i18next` + `react-i18next` | Internationalization (English + Chinese) |
| `date-fns` | Date utilities |
| `posthog-js` | Product analytics |

## Detailed Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — Deep dive into system architecture and data flow
- [internal/README.md](internal/README.md) — Internal package documentation (terminal, PII, worktree, SSH, MCP, etc.)
- [storage/README.md](storage/README.md) — SQLite schema and migration history
- [HOT_RELOAD_TROUBLESHOOTING.md](HOT_RELOAD_TROUBLESHOOTING.md) — Dev server hot reload guide
- [PERF_ANALYSIS.md](PERF_ANALYSIS.md) — Performance analysis guide for the DevTools panel
- [frontend/README.md](frontend/README.md) — Frontend architecture overview (hooks, components, theming, events)
- [frontend/src/i18n/README.md](frontend/src/i18n/README.md) — Internationalization conventions and namespace guide
- [tools/README.md](tools/README.md) — Developer tooling documentation

## How It Fits Into the Larger Project

The `wails-gui/` directory is a self-contained Wails v2 desktop application. It imports the `yansu-agent/agent` package from the monorepo sibling at `../yansu-agent` for:

- **`agent.AgentRunner`** — Interface for executing AI tasks
- **`agent.NativeAgentRunner`** — Drives the Anthropic Messages API directly with in-process tool registry
- **`agent.TaskInput`** — Prompt execution parameters
- **`agent.StreamEvent`** — Streaming event types emitted during execution

The frontend is compiled into the Go binary via Go's `embed` package (see `main.go` `//go:embed all:frontend/dist`). All Go methods declared on the `App` struct are callable from TypeScript via auto-generated `wailsjs/` bindings.
