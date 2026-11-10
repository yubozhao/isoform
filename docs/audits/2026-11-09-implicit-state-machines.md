# Implicit state machines audit, 2026-11-09

| ID | Area | Severity | Problem | Smallest fix | Effort |
| --- | --- | --- | --- | --- | --- |
| SM-01 | Brief sealing | High | A built brief can remain pending after bounded retries, with no scheduled wake-up. | Persist seal outcome and next attempt; wake or show a recoverable failure. | M |
| SM-02 | iPhone pending writes | High | An ambiguous dispatched write can become permanently pending after its safe replay window. | Mark it outcome-unknown and offer safe reconciliation. | M |
| SM-03 | Website Stop | High | A successful Stop followed by an unavailable chat read leaves the card stopping indefinitely. | Give Stop an outcome-unknown state and bounded reconciliation. | S |
| SM-04 | Brief latest pointer | Medium | Concurrent seals can move the latest pointer backward. | Compare and update the pointer under one lock. | S |
| SM-05 | Today brief build | Medium | An unrelated stored event or an old brief can make a build appear successful. | Correlate build, completion, and stored result by run ID. | M |
| SM-06 | Brief audio | Medium | Persistent upload failures retry without a terminal or visible state. | Persist bounded upload attempts and a retryable failure. | M |
| SM-07 | Relay brief retention | Medium | Future-held versions can evict every currently readable brief. | Retain readable and future-held briefs separately. | S |
| SM-08 | Brief run history | Medium | Unreadable or unknown-version history is treated as empty and can be overwritten. | Fail closed on unreadable history and migrate explicitly. | S |
| SM-09 | Website chat reads | Medium | Older overlapping reads can overwrite newer settled chat state. | Fence responses by chat and request generation. | S |

Severity follows the requested rubric. These are source-derived failure paths, not claims that each was reproduced in production. The known symptoms are linked where the mechanism matches; that does not establish the historical root cause. Effort is an estimate for a focused fix and its tests.

## Findings

### SM-01: Sealing has a retry pause but no terminal recovery

**States and writers.** A source is stored, has no compatible sealed copy, is being sealed, is temporarily withheld, has a valid sealed copy, or is backed off. `seal` saves a versioned copy only when policy, source and privacy checks agree (`wails-gui/mobile_brief_finished_read.go:382`). `sealedReadOrPending` attempts a seal or schedules a retry and returns pending (`wails-gui/mobile_brief_finished_read.go:446`). `sealStored`, `sealAndAnnounce`, and the retry worker also attempt the transition (`wails-gui/mobile_brief_finished_read.go:688`, `wails-gui/mobile_brief_finished_read.go:742`, `wails-gui/mobile_brief_finished_read.go:799`). The worker makes four attempts, records an in-memory one-hour pause and exits; it emits `brief:stored` only upon success (`wails-gui/mobile_brief_finished_read.go:756`, `wails-gui/mobile_brief_finished_read.go:814`).

**Failure sequence.** A brief builds while a required source remains unavailable for all four attempts. The source becomes available during the one-hour pause. No timer is scheduled to try at the end of that pause; absent another qualifying read or source event, the placeholder can stay on screen indefinitely. A restart or changed digest can retry sooner, but neither is a durable recovery contract. This can match both “brief built but hidden” and “sealing retry that never ends.” This is a liveness failure, not a reason to bypass privacy gates.

**Fix.** Define pending, sealing, retry-wait, sealed, withheld and failed states, with a single transition function and a durable source digest, reason and next-attempt time. Schedule the wake-up, surface a recoverable failure after a bounded policy, and keep withholding until checks pass. Test restart, recovery during backoff, changed source, permanent failure and privacy changes. Use the explicit guarded transition style in `wails-gui/purpose_map.go:538` without treating inferred evidence as authority.

### SM-02: Expired ambiguous iPhone writes have no resolution

**States and writers.** A pending write is reserved, marked dispatched before network I/O, answered and removed, or retained on an ambiguous error. `reserveTodayRefresh` deduplicates against the existing entry (`ios/Yansu/PendingWriteJournal.swift:306`); `markTodayRefreshDispatched` and `markTodayDispatched` persist the send marker (`ios/Yansu/PendingWriteJournal.swift:415`). `TodayStore.retryFresh` and `ActionStore.retryToday` send and remove on a confirmed answer (`ios/Yansu/TodayStore.swift:455`, `ios/Yansu/ActionStore.swift:153`). `undoToday` permits undo only before dispatch for these operations (`ios/Yansu/PendingWriteJournal.swift:445`).

**Failure sequence.** The phone records dispatch, the request or response is lost, and the phone remains unable to reconcile for 24 hours. Subsequent retries skip the entry because the server receipt lifetime has elapsed (`ios/Yansu/TodayStore.swift:458`, `ios/Yansu/ActionStore.swift:160`). These entry types are excluded from ordinary age pruning (`ios/Yansu/ActionStore.swift:364`), and an existing refresh is reused. The UI can continue to show a pending operation that cannot be retried or undone. A user can be stuck; replaying blindly would risk a duplicate action.

**Fix.** Add a versioned `outcomeUnknown` terminal state and an explicit reconciliation path that reads the authoritative outcome when possible, or explains the uncertainty and allows the user to dismiss it without claiming it did not execute. Do not automatically replay beyond the receipt window. Test lost request, lost response, relaunch, expiry, late answer and a fresh user request.

### SM-03: Stop can stay on screen forever when reconciliation cannot read

**States and writers.** The website combines chat `working`/`checking`/`stopping`, local `ui.stopping`, and the most recent progress phase (`website/js/aoy-core.js:534`). `stopNow` sets the local flag before posting; on 200/202 it reloads and starts `watchStopping` (`website/js/aoy-hire.js:358`). `tick` clears the flag only after a read says the chat has left those states (`website/js/aoy-hire.js:151`). The watcher retries reads on an interval and has no failed-read or elapsed-time transition (`website/js/aoy-hire.js:383`).

**Failure sequence.** Stop is accepted; the network or server then stays unavailable, or subsequent reads never return a settled state. The old chat and local stopping flag remain, and every failed poll leaves them untouched. The card stays “Stopping” with no distinct unknown/retry outcome. This directly matches the reported stuck Stop symptom, although the original incident may have had another cause.

**Fix.** Model sending, accepted/reconciling, settled, refused and outcome-unknown; bound waiting and expose a retry/read-again action without asserting that work stopped or retrying a money-changing request with a new key. Test accepted stop followed by outage, lost response, eventual settlement, late progress and navigation.

### SM-04: Latest-brief comparison and update are not atomic

**States and writers.** The versioned latest-shown file identifies a brief; a sealed brief with written text can advance it. `noteLatestShownIfNewer` reads and compares the existing pointer (`wails-gui/mobile_brief_current.go:124`); `noteLatestShown` reads again and calls `writeLatestShown`, which locks only the individual write (`wails-gui/mobile_brief_current.go:91`, `wails-gui/mobile_brief_current.go:108`). Seal completion, amendments and retries invoke the newer check from several goroutines (`wails-gui/mobile_brief_finished_read.go:469`, `wails-gui/mobile_brief_finished_read.go:606`, `wails-gui/mobile_brief_finished_read.go:706`, `wails-gui/mobile_brief_finished_read.go:750`, `wails-gui/mobile_brief_finished_read.go:826`, `wails-gui/mobile_brief_finished_read.go:883`). `latestShown` visits the pointer first and returns it when readable (`wails-gui/mobile_brief_current.go:142`, `wails-gui/mobile_brief_current.go:188`).

**Failure sequence.** An older seal and a newer seal both compare against the old pointer. The newer seal writes first, then the older seal writes last. A subsequent read visits the now-older pointer first and can return it despite a newer stored brief. This could produce the reported yesterday-on-Today symptom; no claim is made that it caused that incident. The atomic file replacement prevents torn JSON, not this ordering race.

**Fix.** Perform comparison and update in one store operation under one lock, including the fallback update from `latestShown`; use an explicit tie-break for equal times. Test deterministic interleaving, restart and withheld/quiet/early briefs. Preserve the existing versioned record and privacy behavior.

### SM-05: Today infers build success from unrelated events and old data

**States and writers.** `useLatestBrief` has `idle`, `making`, `failed` and `cooldown`, plus `makingSince`, `buildEnded`, `lastBuildEnd` and the displayed brief (`wails-gui/frontend/src/hooks/useLatestBrief.ts:44`). `makeNow` sets making after the request returns (`wails-gui/frontend/src/hooks/useLatestBrief.ts:144`); every `brief:stored` event clears making and marks idle (`wails-gui/frontend/src/hooks/useLatestBrief.ts:98`); a building-end event triggers a read, and any `next.brief` is treated as success (`wails-gui/frontend/src/hooks/useLatestBrief.ts:78`). A four-minute timer marks failure if no end is observed (`wails-gui/frontend/src/hooks/useLatestBrief.ts:133`).

**Failure sequence.** Start a new build while an older brief is displayed. If an unrelated amendment emits `brief:stored`, it resets the build state; alternatively the new build ends without storing a result and the read returns the older brief, which is sufficient for `idle`. The user sees a completed build while the displayed brief is old. This resembles both reported brief symptoms; the code does not prove either incident's cause.

**Fix.** Give each build an ID and make its stored result, failure and end events carry that ID. Reduce them through one typed state transition and verify the resulting brief belongs to that build, rather than inferring success from any stored event or any existing brief. Test unrelated amendments, end before request response, failure with an old brief present, duplicate and missing events.

### SM-06: Audio upload retries have no exhausted state

**States and writers.** Local audio is absent, rendering/downloading, ready without an attachment, or ready with an attachment. `briefAudioToResend` derives retry eligibility from `Status == ok`, bytes and a nil attachment (`wails-gui/mobile_brief_audio_route.go:44`); `scheduleBriefAudioRetry` starts the worker (`wails-gui/mobile_brief_audio_route.go:23`). `retryBriefAudio` stops on shutdown, a newer brief, success, no audio or offline/no phone, but all other errors continue forever with capped delay (`wails-gui/mobile_brief_audio_route.go:67`, `wails-gui/mobile_brief_runs.go:309`). Relaunch restarts eligible work without a persisted attempt count.

**Failure sequence.** A current brief's local recording exists but the relay persistently rejects its upload for a reason other than the two special errors. The worker repeats for as long as that brief remains latest and the app runs; after relaunch the process starts over. The phone can lack audio indefinitely with no durable exhausted state. This is a separate retry from SM-01, not proof of the reported sealing incident.

**Fix.** Represent queued, uploading, retry-wait, uploaded and failed, keyed by brief generation; persist attempt count and next time, bound retries and expose an actionable retry. Test persistent and transient errors, restart, missing audio, new brief and duplicate worker admission.

### SM-07: Future-held relay versions compete with readable history

**States and writers.** A relay brief is either held until `deliver_at` or due; sequence number supplies version. `PutBriefDueAt` inserts both kinds and enqueues a due push in the transaction (`relay/internal/store/briefs.go:27`), then retains the seven highest versions without distinguishing held from due (`relay/internal/store/briefs.go:59`, `relay/internal/store/store.go:40`). The janitor uses the same version-only retention (`relay/internal/store/janitor.go:35`). `ListBriefs` filters out held rows at read time (`relay/internal/store/briefs.go:77`).

**Failure sequence.** Seven higher-sequence future-held envelopes are inserted for one device before their due time, for example through repeated publication/amendment of held content. They evict the last due brief, and reads return no brief until one becomes due. Whether ordinary production paths can generate that many held versions within one slot needs an end-to-end reproduction. The relay operation itself permits the sequence.

**Fix.** Keep the newest currently due brief independently of the bounded held set, with a separate limit and consistent janitor rule. Test multiple future amendments, a due brief, release by time, and eventual retention cleanup.

### SM-08: Run-history decode failure is indistinguishable from first use

**States and writers.** Scheduled run state is `ok`, `failed` or `missed`, with attempts, failure cause, notification and early-replacement fields (`wails-gui/mobile_brief_runs.go:19`, `wails-gui/mobile_brief_runs.go:36`). `record` writes outcomes, `markEarlyReplaced` and `markNotified` modify flags (`wails-gui/mobile_brief_runs.go:183`, `wails-gui/mobile_brief_runs.go:196`, `wails-gui/mobile_brief_runs.go:246`); `recordFailure`, missed-slot handling and early/timing service are producers (`wails-gui/mobile_brief_service.go:661`, `wails-gui/mobile_brief_service.go:791`, `wails-gui/mobile_brief_early.go:330`, `wails-gui/mobile_brief_timing_service.go:162`). The file has version 1, but `loadLocked` returns a new empty file for any read error, malformed JSON or different version (`wails-gui/mobile_brief_runs.go:135`).

**Failure sequence.** A run-history file exists but is temporarily unreadable, damaged, or from a newer version. A subsequent mutation loads an empty history and, if writing succeeds, replaces the original with a new version-1 history. Attempts, sign-in blockers and notification markers can disappear, causing inaccurate health or duplicate/missing missed-slot reporting. An absent file on first run is a valid separate case.

**Fix.** Return missing, valid, unsupported-version and read/parse-error separately; reject writes on unknown or unreadable data and preserve it for recovery. Add explicit migrations when the schema changes. Test malformed content, read failure, newer version and ordinary first use.

### SM-09: Website applies overlapping chat reads in arrival order

**States and writers.** `S.chat` is replaced on each successful `loadChat` (`website/js/aoy-hire.js:129`). Event callbacks, Stop completion, the stopping watcher and the stop-question refresh can start reads independently (`website/js/aoy-hire.js:136`, `website/js/aoy-hire.js:350`, `website/js/aoy-hire.js:365`, `website/js/aoy-hire.js:386`). There is no request generation or chat identity check before applying a response. `tick` and `isStopping` derive the display from the last response plus local flags (`website/js/aoy-hire.js:151`, `website/js/aoy-core.js:534`).

**Failure sequence.** An earlier read captures `working` but is delayed; a later read captures the settled `waiting_client` state and arrives first; the earlier response arrives afterward and overwrites the settled view. A stale working or stopping card can reappear until another successful refresh. If navigation changes the active chat while a read is outstanding, an old response can similarly replace the current chat. This is a possible contributor to the reported stuck/wrong Stop screen, not an established incident cause.

**Fix.** Associate each read with the requested chat and a monotonic request generation, and discard superseded responses; where the server supplies a revision, compare it too. Test reversed response order, navigation, late progress and failed refresh after a settled response.

## Existing machines and coverage limits

- Purpose's `purposeEventState` names events, checks prior state and evidence, and rejects invalid moves (`wails-gui/purpose_map.go:538`). It is a good local pattern for explicit, provenance-aware transitions. It does not confer permission to act.
- Relay pairing names its states and checks claim, approval, expiry and ownership within transactions (`relay/internal/store/pairings.go:13`, `relay/internal/store/pairings.go:87`, `relay/internal/store/pairings.go:159`). iPhone pairing also names phases and bounds transient polling (`ios/Yansu/PairingStore.swift:4`, `ios/Yansu/PairingStore.swift:330`). Preserve those protections; expiry and one-time delivery still span the two stores.
- Server owner routes use guarded from/to transitions (`server/internal/api/api.go:105`, `server/internal/api/api.go:381`). Server pass and chat transitions are distributed across guarded SQL in API, runner, checker and settlement; state constraints and transactions are useful existing safeguards (`server/internal/store/migrations/0002_runner.sql:13`, `server/internal/runner/runner.go:497`, `server/internal/checker/checker.go:285`, `server/internal/settlement/settlement.go:343`). A stopped delivered pass is intentionally projected as `waiting_client` (`server/internal/settlement/settlement.go:354`), and a migration repairs older stopped chats (`server/internal/store/migrations/1038_stopped_files.sql:1`). The reported delivered-after-stop symptom should not be assumed still present. Holds use guarded authorized/captured/released transitions (`server/internal/payments/payments.go:48`, `server/internal/payments/payments.go:71`, `server/internal/payments/payments.go:97`). Before consolidating these money paths, map every writer and transaction, including Stripe reconciliation and migrations; preserve idempotency and do not infer payment state from UI.
- Desktop handoffs have typed statuses and attempt-conditional mutation (`wails-gui/handoff_run.go:127`); main-chat reads use a request generation (`wails-gui/frontend/src/hooks/useMainChatPanel.ts:100`). Relay push has leased claims and bounded delivery outcomes (`relay/internal/apnspush/worker.go:63`). These are useful patterns, not a guarantee that every surrounding lifecycle is complete.

This is a prioritized source audit, not an exhaustive inventory of every implicit machine. It covers the brief paths, representative desktop UI and chat paths, server stop/pass/hold, iPhone pairing and pending writes, relay pairing/brief/push, and website chat. A full writer-by-writer review remains for desktop handoffs/tasks and removed-Agency compatibility, chat streaming, memory and Dream, work profile, owner client, remote access, permissions/onboarding, monitor/audio, model runner/sidecar, updater, the remaining desktop and iPhone screens, notifications, server profile/receipts/payouts, and other relay and website flows. No conclusion of safety is implied for unreviewed paths. The separate hub tools directory was out of scope. No test suites were run for this read-only audit.

## Proposed parallel PR batches

Each row is a separately owned PR. Within each batch the listed production files do not overlap; tests should be scoped to the owning component. Land and rebase between batches where later work consumes an earlier contract. Preserve privacy gates, idempotency and existing data; add table-driven allowed-transition and recovery tests for each change.

| Batch | PR | Main files | Dependency |
| --- | --- | --- | --- |
| 1 | Seal recovery (SM-01) | `wails-gui/mobile_brief_finished_read.go` | None |
| 1 | iPhone ambiguous writes (SM-02) | `ios/Yansu/PendingWriteJournal.swift`, `ios/Yansu/ActionStore.swift`, `ios/Yansu/TodayStore.swift` | None |
| 1 | Website Stop and request fencing (SM-03, SM-09) | `website/js/aoy-hire.js`, `website/js/aoy-core.js` | None; combine because files overlap |
| 1 | Relay held retention (SM-07) | `relay/internal/store/briefs.go`, `relay/internal/store/janitor.go` | None |
| 1 | Run-history recovery (SM-08) | `wails-gui/mobile_brief_runs.go` and focused store callers | None; coordinate shared tests with audio work |
| 2 | Latest pointer atomicity (SM-04) | `wails-gui/mobile_brief_current.go` | Rebase after seal recovery; agree on latest-pointer contract |
| 2 | Audio retry outcome (SM-06) | `wails-gui/mobile_brief_audio_route.go` and its own state storage | Rebase after run-history work if shared helpers change |
| 3 | Correlated build lifecycle (SM-05) | `wails-gui/frontend/src/hooks/useLatestBrief.ts` and backend build event producers | Agree on event contract after seal and latest fixes |
| 3 | Complete remaining inventory | Report and affected-area follow-up PRs | Map remaining writers before proposing transition consolidation |
