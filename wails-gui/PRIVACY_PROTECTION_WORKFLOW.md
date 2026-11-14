# Wails GUI Privacy Protection Workflow

This document summarizes the current privacy protection workflow in `wails-gui/`, focusing on activity screenshots, AX text, sensitive application windows, and downstream activity context usage.

## Overview

The current protection chain is: block capture when the context should not be recorded, then ensure screenshots are protected before they hit disk.

1. On startup, the app restores the user-configured protected application list, then starts the activity monitor if permissions allow it.
2. Before each screenshot, the capture path skips the Pause observation privacy state, locked screens, private browser windows, and overloaded PII pipeline states.
3. After a screenshot is captured in memory, visible sensitive application windows are synchronously pixelated.
4. AX text is redacted with regex-based PII replacement, using tags such as `[REDACTED:<category>]`.
5. The screenshot is passed through async OCR + PII detection; it is written to disk only after it is confirmed clean or successfully masked. Failures drop the frame.
6. If GLiNER is still downloading or loading, screenshots stay in a bounded in-memory pending PII queue and are drained only after the detector is ready.
7. Background OCR, activity triage, and recent context injection read from the saved data.

## Entry Points And Configuration

During startup, `loadUserSensitiveApps()` restores user-added protected applications from `ui_state["sensitive_apps"]`. The app then pre-warms the system application list in the background and starts the activity monitor.

Protected applications come from two sources:

- Defaults: password managers such as 1Password, Bitwarden, LastPass, Dashlane, KeePass, and macOS Keychain.
- User configuration: the Settings page "Protected Applications" section calls the Wails bindings `AddSensitiveApp` and `RemoveSensitiveApp`, which persist changes back to `ui_state`.

Matching is prefix-based on bundle IDs. For example, `com.1password.desktop` matches the protected prefix `com.1password`.

## Startup Preconditions

On macOS, the activity monitor starts only when both Accessibility and Screen Recording system permissions are granted, and the corresponding user permission toggles are enabled. If not, the activity store is still initialized so historical sessions remain readable, but capture does not start.

On Windows, there is no macOS-style permission gate, but the monitor still respects the user's screen and input toggles.

## Single-Screenshot Workflow

A screenshot capture enters through `captureSnapshotForEvent`. The high-level flow is:

```text
event trigger
  -> Pause observation check; skip if active
  -> PII/capture backpressure check
  -> locked-screen check; skip if matched
  -> private browser window title check; skip if matched
  -> CaptureRaw() screenshot into memory
  -> visual dedup; skip if not worth keeping
  -> JPEG encode
  -> collect AX tree / AX text / browser URL
  -> appparser creates structured ParsedContent
  -> RedactText(axText)
  -> synchronously pixelate sensitive application windows
  -> Pause observation check before disk path setup
  -> if GLiNER is initializing, queue the frame in RAM within fixed caps
  -> runAsyncPIIMasking()
      -> OCR with boxes
      -> GLiNER PII detection
      -> regex fallback
      -> merge detections
      -> MaskRegions()
      -> write 0600 JPEG
```

The important behavior is that the original unprotected screenshot is not normally written first. `runAsyncPIIMasking` writes the file only when the image has no text, has no detected PII, or has been successfully masked. OCR failures, masking failures, and panics drop the frame instead.

## Pre-Capture Gates

Before capture, several hard gates apply:

- If observation is paused, capture returns immediately. Entering the pause also stops activity monitoring and native microphone/system-audio capture; resuming restores only sources that were previously running and still pass current gates.
- If `IsScreenLocked()` returns true, no screenshot is captured.
- If the browser window title contains `Private Browsing`, `Incognito`, or `InPrivate`, no screenshot is captured.
- If the PII semaphore is full, ordinary triggers are skipped to avoid capturing a frame that would later be dropped by the PII pipeline.
- `heartbeat`, `session_end`, and selected forced triggers bypass throttling so session keepalive and important workflow captures still happen.

Private browser detection is currently a case-sensitive substring match against the window title, so it depends on browser title formatting.

## Sensitive Application Window Protection

After JPEG encoding, the capture path applies synchronous window-level protection:

1. It calls `activitymon.FindVisibleSensitiveWindows(pii.AllBundlePrefixes())` to scan all visible windows, not just the frontmost app.
2. On macOS, the implementation uses the CGWindow list to obtain window bounds, then filters windows by running applications whose bundle IDs match sensitive prefixes.
3. Matching regions are passed to `pii.BlurRegionsBatch`, which decodes and re-encodes the JPEG once while pixelating all sensitive regions.
4. If the visible-window scan returns nothing but the frontmost app matches `IsSensitiveApp`, the frontmost window is blurred; if bounds are unavailable, the entire image is blurred.

Because this layer is synchronous, frames with a detected sensitive application window can still be written as "window already blurred" even if the later OCR PII pipeline is throttled.

## OCR And PII Detection Protection

Screenshot-level PII protection lives in `internal/pii`:

- OCR: `RecognizeJPEGWithBoxes` recognizes `en-US` and `zh-Hans`, returning text plus normalized bounding boxes.
- GLiNER: the ONNX model `knowledgator/gliner-pii-base-v1.0` detects the default entity types `credit_card`, `cvv`, `password`, `one_time_password`, `street_address`, and `api_key`.
- Regex fallback: covers credit cards, CVV, OTP/2FA codes, `password` / `passwd` / `密码` / `口令`, US-style street addresses, and common API key / token / private key formats. It also handles OCR line continuations and adjacent observation merging.
- Merging: GLiNER detections take priority, and non-overlapping regex detections are added.
- Masking: `MaskRegions` pixelates each detection bounding box with small padding.

For performance, PII OCR is serialized with a semaphore. Ordinary frames have a minimum OCR interval of 3 seconds. Frames that already contain a sensitive window use a 30-second OCR interval because synchronous window blurring has already run. Identical OCR fingerprints reuse the previous detection result to avoid repeated ONNX inference.

On first run or after a model upgrade, GLiNER may still be downloading or loading. During that window, screenshots are not written directly to disk: they are kept in a bounded pending queue, capped at 24 frames and 64 MB. When GLiNER becomes ready, the queued frames are drained through the same masking path. If initialization fails, pending screenshots are dropped and the system falls back to regex-only screenshot masking until the next retry window.

## Text Path Protection

Before `activity_snapshots.ax_text` is saved, AX text is passed through `pii.RedactText`. It uses the same regex pattern set and replaces matched sensitive values with `[REDACTED:<category>]`.

When the frontmost app is protected (`IsSensitiveApp`), the capture path clears `ax_text`, `parsed_json`, and `parsed_hash` entirely and skips browser URL collection, so no text or structured content from a protected app reaches the snapshot row. For non-protected apps, `ParsedContent` is marshaled into `parsed_json` before `RedactText(axText)` runs, so structured parser output does not pass through the regex redaction layer (PII in non-protected apps' parsed payloads relies on `redactParsedContentPII`).

Speech transcripts are cleared at capture when the real frontmost app is protected (checked against the pre-meeting-rewrite bundle ID, so dictating into a protected app during a Zoom/Teams call is still gated). Typed-text segments are dropped for protected apps in `activity_typed_text.go`.

At session persistence, every event whose bundle ID matches a protected app has its content-bearing fields scrubbed in place (`WindowTitle`, `ClickLabel`, `FocusLabel`, `KeyChars`, `KeyChar`, `TypedText`, `SpeechText`) before the row is written.

## Protected-App Architecture: Three Layers, One Invariant Each

The protected-apps system is deliberately layered so that each layer holds a
single, complete invariant. A new feature only needs to know Layer 3's rule.

### Layer 1 — Capture: protected-app content never reaches disk

Applied at capture time, keyed on the frontmost/event app:
screenshot blur, AX-text + parsedJSON + parsedHash cleared, typed text
dropped, speech transcript cleared (checked against the real frontmost
bundle, not the meeting-app rewrite), browser URL skipped, and per-event
scrub of WindowTitle/ClickLabel/FocusLabel/KeyChars/TypedText/SpeechText at
persistence. Quick Chat refuses live capture from a protected frontmost app.
The WeChat vision pass is skipped entirely when WeChat is protected (a VLM
reading a blurred screenshot hallucinates fake contacts).

### Layer 2 — Session state: any protected app marks the whole session

If ANY app in a session matches the protected list (bundle ID or display
name), the session is persisted with `triage_status = "protected"` — a
terminal state distinct from `"dismissed"` (which the triage pipeline writes
for every normally completed group). Side effects at session save: no
meeting transcript, OCR marked `"skipped"`.

### Layer 3 — Store reads: default APIs never return protected sessions

This is the layer every consumer interacts with, and the one that makes the
system maintainable. The rule (documented at `sqlExcludeProtected` in
activity_store.go):

> **Default ActivityStore reads never return protected sessions' data.**
> A getter with the `IncludingProtected` suffix returns them and is
> reserved for user-facing UI (the user viewing their own timeline) and
> retention/cleanup. Suffixed results must never feed an LLM prompt, an
> embedding, a search index, or an external service.

Default-filtered: `GetSessions`, `GetFilteredSessions`,
`GetSessionsByIDs`, `GetSessionEvents`, `GetSessionSegments`,
`GetSessionSnapshots`, `GetRecentSnapshotsForApp`,
`GetSemanticPendingSessions`, `GetPendingSessions` (implicit — protected is
never `"pending"`), embedding/backfill/semantic-search queries, OCR keyword
extraction.

`IncludingProtected` call sites: Activity page list/detail bindings,
snapshot binding, retention rotator (must see protected sessions to delete
their files), voice enrollment.

Consumers must NOT add their own `triage_status` checks — the store is the
single source of truth. Name-level helpers (`pii.FilterSensitiveAppNames`)
remain only where display names from pre-protection historical rows can
still surface (context injection, suggestion inputs, summary breakdowns).

### Adding a new consumer

Use the default store API and you are safe by construction. Reach for an
`IncludingProtected` variant only if you are building user-facing UI over
the user's own timeline or a maintenance task — and never pass its results
to an LLM, embedding, index, or network call.

## Persistence And Downstream Use

Local activity data is stored under `~/.yansu-agent/activity/` and `activity.db`:

- Activity directories are created with `0700`.
- Snapshot JPEGs are written with `0600`.
- `activity_snapshots` stores `ax_text`, `browser_url`, `content_type`, `app`, `parsed_json`, and related metadata.
- The background OCR worker reads saved snapshot JPEGs and performs low-priority OCR, so the new snapshot path reads images that have already gone through sensitive-window blurring and/or PII masking.
- The activity triager summarizes events, structured parsed content, and OCR samples for downstream AI pipelines. Keyboard events can be rendered into "Typed in ..." timeline text, and speech transcripts can also enter the summary.
- Page snapshots (the projections behind About you, How you do things, Who you know and the other derived views) are saved under `derived-snapshots/` (`0700` folder, `0600` files). A file is served only while its durable privacy stamp (saved privacy epoch, forget markers file, UI language) matches; a mismatched file is deleted unread. Withdrawals, forgets, Closed Eyes, permission and sensitive-app changes, and a language change delete the whole folder in the same step, and nothing is saved or served while Closed Eyes is on (`derived_views_disk.go`). `YANSU_DERIVED_SNAPSHOTS=memory` turns this off.

## Platform Differences

- macOS: sensitive application window protection is the most complete. It relies on bundle IDs, CGWindow bounds, Accessibility, and Screen Recording permissions.
- Windows: `FrontmostAppBundleID()` returns an empty string and `FindVisibleSensitiveWindows()` returns nil, so whole-window sensitive application blurring is effectively unavailable. Protection mainly depends on screenshot OCR + PII region masking.
- Other platforms: the activity monitor is a no-op stub, and sensitive window scanning is not implemented.

## Current Coverage

Covered today:

- Pause observation stops activity, microphone, and system-audio capture at the source while enabled.
- Screenshots are skipped while the screen is locked.
- Screenshots are skipped for common private browser window titles.
- Default and user-configured sensitive application windows are pixelated.
- Common screenshot PII is detected before disk write through OCR + GLiNER + regex, then pixelated.
- Regex PII in AX flat text is replaced before being saved to `ax_text`.
- Ordinary frames are dropped when the PII pipeline is busy or throttled, rather than being saved unchecked.

Known boundaries:

- For non-protected apps, `parsed_json` structured content has no unified regex PII redaction (protected apps' `parsed_json` is cleared entirely).
- For non-protected apps, keyboard typed text, speech transcripts, and browser URLs do not pass through `RedactText` (protected apps' values are cleared/skipped at capture).
- Private-window detection depends on English title substrings and is currently case-sensitive.
- If OCR is unsupported or unavailable, only sensitive-application window blurring remains; non-sensitive-window screenshot PII does not receive region-level detection.
- If GLiNER initialization fails, regex fallback still runs, but model-based coverage is reduced.
- Windows lacks whole-window sensitive application protection because bundle ID and sensitive window bounds support are not implemented there.

## Code References

- Pause observation privacy state: `wails-gui/closed_eyes_mode.go`, `wails-gui/activity_monitor.go`
- Restore protected applications on startup: `wails-gui/app.go:405`, `wails-gui/sensitive_apps_bindings.go:55`
- Protected application registry: `wails-gui/internal/pii/sensitive_apps.go:12`
- macOS permission gate: `wails-gui/activity_monitor_darwin.go:14`
- Skip capture on locked screen / private browser window: `wails-gui/activity_monitor.go:1484`, `wails-gui/activity_monitor.go:1493`
- AX text redaction: `wails-gui/activity_monitor.go:1745`, `wails-gui/internal/pii/text_redact.go:20`
- Synchronous sensitive-window blurring: `wails-gui/activity_monitor.go:1754`, `wails-gui/internal/pii/mask.go:72`
- Pending GLiNER queue: `wails-gui/activity_monitor.go:532`, `wails-gui/activity_monitor.go:1840`
- Async OCR + PII + mask + disk write: `wails-gui/activity_monitor.go:1854`, `wails-gui/activity_monitor.go:2006`
- Regex PII patterns: `wails-gui/internal/pii/regex.go:24`
- Snapshot store: `wails-gui/activity_store.go:932`
- Background OCR reads saved snapshots: `wails-gui/activity_ocr.go:152`
- `ParsedContent` is generated before text redaction: `wails-gui/activity_monitor.go:1660`
- Typed events enter triager summaries: `wails-gui/activity_triage.go:2584`
