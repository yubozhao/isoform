# storage

SQLite-backed persistence layer for the Wails GUI desktop application. The package provides a single `Store` type that manages all local data — projects, workspaces, sessions, conversation stream items, file checkpoints, git snapshots, sidebar groups, cron jobs, composer suggestions, extractor records, and UI state.

## Overview

The database lives in a single SQLite file (path chosen by the caller). On first open, `New()` runs all migrations automatically and advances the schema to the latest version. WAL mode, foreign-key enforcement, and a 5-second busy timeout are enabled at startup.

```
Projects
  └── Workspaces  (remote_env for SSH/WSL)
        └── Sessions
              └── Stream Items
              └── Checkpoint Files
              └── Git Snapshots

Sidebar Groups
  └── Sidebar Group Items  (refs to chats or automations)

Cron Jobs
  └── Cron Runs

Extractor Records
Composer Suggestions
```

Deleting a project cascades through its workspaces, sessions, and all associated rows.

## Key Files

| File | Responsibility |
|---|---|
| `storage.go` | `Store` struct, `New()`, schema migrations (v1–v77), orphan cleanup, `Vacuum()` |
| `types.go` | Plain-Go row structs for all tables |
| `principals_import_migration.go` | Transactional v61 import provenance migration and legacy contact backfill |
| `projects.go` | Project CRUD, sort-order management, atomic project+sessions save |
| `sessions.go` | Session CRUD, single and bulk load helpers, `LoadMainSession` / `LoadSessionsByParent` for the chat model |
| `workspaces.go` | Workspace CRUD, atomic project+workspaces+sessions save, `EnsureProjectStructure` |
| `items.go` | Stream item insert/update/delete, cursor-based pagination, turn-based loading |
| `checkpoints.go` | Checkpoint file records and git snapshot records (rewind support) |
| `sidebar_groups.go` | Sidebar custom group and group item CRUD, bulk ref cleanup, cascade deletes |
| `cron.go` | Cron job and cron run CRUD, stale-run recovery, stream item log linkage |
| `extractor.go` | Extractor pipeline record CRUD; upsert-on-retry, date-range queries, bulk delete |
| `composer_suggestions.go` | LLM-generated prompt chip persistence; auto-expiry and pruning |
| `ui_state.go` | Generic key-value store for UI preferences |
| `storage_test.go` | Unit tests covering migrations, CRUD, cascades, pagination, and edge cases |

## Architecture

### Store initialisation

```go
store, err := storage.New("/path/to/app.db")
if err != nil {
    log.Fatal(err)
}
defer store.Close()
```

`New` checks existing database headers and then uses a read-only connection to include any newer version committed in the WAL. A stored schema newer than the audited compatibility ceiling (**73**) returns a typed `*SchemaVersionError` before opening for writes or changing the journal mode. The header check avoids creating WAL sidecars just to reject an already-newer database. The app can present a localized error using its `StoredVersion` and `SupportedVersion` fields.

The schema is currently **77**. The migration target and compatibility ceiling are both **77**. Earlier databases upgrade to 77 with the pre-migration snapshot policy below. Route history remains migration v60: it adds columns with defaults to `cron_runs` and the independent `agent_run_routes` table, and tolerates populated route objects from earlier assistant-first builds that used v58. Versions above 77 are refused before writes.

Migration v62 adds `imported_contact_cards`, `contact_handle_provenance` and `principal_contact_links` and `principal_contact_link_updates`. Link receipts and merges commit together. Unlink restores the card and its handles, aliases and the original kind of the seen person; pre-link activity returns to its original person, while later observations and merges are preserved. A schema-61 binary needs the pre-migration snapshot when reverting.

Migration v61 adds `principals.source` and `principals.external_ref` in one transaction, both defaulting to an empty string. Existing principals with `created_from` equal to `system_contacts` or `vcard` receive the same source; their earliest nonempty `contacts` or `vcard` app handle supplies the external reference when available. Other principals remain unmarked, including manually curated people that own imported handles. Existing nonempty provenance and `created_from` values are preserved on replay. New imports persist provenance with the principal insert, and merges retain the destination principal's provenance. A build whose compatibility ceiling is 60 refuses a migrated v61 database; use the snapshot recovery procedure below when rolling back to that build.

The migration ledger keeps v58 and v59 for proactive chat structures, v60 for assistant-first and v61 for contact import provenance; v62 for the local contact cache, per-handle provenance and reversible link receipts; v63 implements RC 1 main-chat topics (#140); v64 adds the C5 context-use log (#144); v65 adds reversible People merges (#143). Chat chips (C2) follow at v66, the durable outbound-send ledger at v67, open-answer storage at v68, answer preferences at v69, open-answer check state at v70, ask state at v71, quiet-hour answer holds at v72, and learned open-answer state at v73. Version 74 removes captured timestamp suffixes from saved principal names. These historical migrations remain implemented here. Audit sibling schema changes before raising `supportedSchemaVersion`.

The v58 indexes and v59 identity tables, indexes and triggers are created with `IF NOT EXISTS` regardless of the stored version. This repairs a database stamped by another build that did not include them. Missing tables or maintenance triggers trigger one transactional identity backfill; healthy reopens never rescan all historical chat payloads. The final stamp advances only when the stored version is below the implemented target and all migrations succeeded; it never decreases a newer stamp.

Before raising the version of an existing database, `New` creates a dated `VACUUM INTO` snapshot beside it (`<database>.snapshot-<UTC date>-v<version>-<unique suffix>.db`) and verifies it with `PRAGMA integrity_check`. This includes committed WAL data. A new, empty database needs no snapshot. Available disk space must cover both the main database file size and the logical database size (including WAL pages); when space is insufficient, startup logs why the snapshot was skipped and continues the migration. Creation and integrity failures stop migration. Cleanup of older verified snapshots is best effort: it keeps the newest two when possible and logs removal failures without blocking migration or repeating snapshots on every launch. Snapshots contain private app data and are created with owner-only permissions.

Snapshot creation and pending-file cleanup share a nonblocking OS lock in `<database>.snapshot.lock`; the file is retained to avoid lock-inode races, and the OS releases the lock if the process exits. A startup never removes pending files while another snapshot holds that lock. Unlocked `.pending` files older than 24 hours are cleaned before checking free space, and on later opens even without a migration. The age threshold also protects recent copies made by binaries that predate the lock protocol. Recent remnants are deliberately left for a later launch. Cleanup failures are logged and do not block startup.

After these safety checks, `New` enables WAL mode and foreign keys, runs migrations, and purges orphaned rows left by older builds that lacked foreign-key enforcement. To recover from a snapshot, quit Isoform, preserve the current database and its WAL/SHM sidecars elsewhere, then copy the chosen snapshot to the original database path with no old sidecars remaining. Open it with a build that supports its schema version.

These guards and snapshots cover the `sessions.db` managed by this package. `activity.db` and the memory database use separate migration mechanisms and are not protected by this schema-version guard or snapshot policy; extending equivalent safety to them is separate work.

Migration v64 adds the identifier-only `context_uses` table and its `(entry_kind, entry_id, used_at)` index. Each row records a memory or knowledge entry ID, a code-owned surface, a Unix millisecond time and an optional session/brief ID. No source text is stored. A bounded asynchronous writer deduplicates the same entry/surface/ref for ten minutes; failures or queue saturation drop use receipts rather than delaying a send. Startup prunes receipts older than 400 days. Forget, memory deletion and knowledge deletion discard queued receipts and delete persisted rows, with deletion epochs captured before source reads and advanced across unlink. New entries with reused deterministic IDs start with empty counts and can log new uses; late completions from the deleted lifetime cannot restore rows. This is a best-effort usage count, not an execution or permission ledger.

The target now includes #140 (v63), merged here before C5 (v64); #143 follows at v65. The actual v62 fixture is unchanged; a v63 fixture is emitted by applying #140's real `migrateStreamItemTopics` at `7ad3a51b` to it. Tests preserve both schemas' manual data and topic structures through upgrade/reopen and refuse a newer stamp. The combined build retains #140's v63 migration and its unconditional topic/proof trigger repair. Reverting to a binary with a lower schema ceiling requires the verified pre-migration snapshot; never lower `user_version` manually. Reverting also loses use receipts written after that snapshot.

### Schema migrations

Migrations are idempotent. Schema additions tolerate existing columns, and the v74 name repair leaves unrelated names alone. v75 adds `message_asks` (`message_asks.go`): asks and commitments read from Messages, in Isoform's words, never message text. v76 adds `assistant_calls` and `assistant_call_sources` (`assistant_calls.go`): what each assistant call was for and the sources it was written from, by kind and opaque id, never text. v77 adds `assistant_calls.made`: how many claims to check an entry's work made. The current schema version is stamped in `PRAGMA user_version` (currently **77**).

| Version | Changes |
|---|---|
| v1 | Initial schema: `projects`, `sessions`, `stream_items`, `ui_state` |
| v2 | Session fields: `thinking_level`, `plan_mode`, `thinking_enabled`, `pinned`, `pinned_at` |
| v3 | `checkpoint_files` table |
| v5 | Session workspace fields: `workspace_type`, `worktree_path`, `worktree_branch` |
| v6 | `workspaces` table; `workspace_id` on sessions; `active_workspace_id` on projects; default workspace migration |
| v7 | `projects.sort_order` (with legacy `sort_ord` backfill) |
| v8 | `projects.setup_command` |
| v9 | `git_snapshots` table |
| v10 | `workspaces.sort_order` for drag-and-drop reordering |
| v11 | Unique index on `git_snapshots(session_local_id, sort_order)` to prevent duplicate rows on rewind |
| v12 | `git_snapshots.head_sha` for HEAD-aware worktree restore |
| v13 | _removed_ — version reserved |
| v14 | _removed_ — version reserved |
| v15 | `sidebar_groups` and `sidebar_group_items` tables |
| v16 | `hidden` column on projects/workspaces/sessions; `cron_jobs` and `cron_runs` tables |
| v17 | `cron_jobs.source_session_id` |
| v18 | `cron_runs.session_tag` and `cron_runs.item_sort_start` for run log loading |
| v19 | `cron_jobs.source_platform` (slack, feishu, ringcentral, or empty for GUI) |
| v20 | `workspaces.remote_env` for SSH/WSL targeting; `ssh_configs` table |
| v21 | `ssh_configs.exec_mode` and `ssh_configs.cli_tool` columns |
| v22 | `ssh_configs.password` column |
| v23 | `ssh_configs.auto_connect` column |
| v24 | `cron_jobs.fingerprint` with unique partial index for automation dedup |
| v25 | `cron_runs.notify_result` for DM notification outcome tracking |
| v26 | `cron_jobs.notify_dm` flag (default true) to control integration DM on completion |
| v27 | `sessions.automation_result` flag to mark sessions that hold an automation run result |
| v28 | _removed_ — version reserved |
| v29 | `cron_jobs.sort_order` for drag-and-drop reordering of automation jobs |
| v30 | `cron_jobs.notify_platform` preferred DM platform override (empty = legacy auto-pick) |
| v31 | Index on `cron_runs(started_at DESC)` for date-range Timeline queries |
| v32 | `extractor_records` table with indexes on `processed_at` and `platform` |
| v33 | UNIQUE index on `extractor_records(pipeline_run_id)`; deduplicates retried pipeline runs |
| v34 | `cron_jobs.schedule_evidence`: verbatim quote from source text justifying LLM-inferred schedule; cleared on manual user edit |
| v35 | `composer_suggestions` table for cached LLM-generated prompt chips with 24-hour expiry |
| v36 | `stream_items.images` column (JSON-encoded user attachments that survive session reload) |
| v37 | Rename legacy `cron_jobs.target_kind` from `acp_agent` to `agent` for the native in-process runner |
| v38 | `stream_items.tool_use_id` column for pairing `tool_use`/`tool_result` blocks in Anthropic API history |
| v39 | `cron_jobs.surface_kind` column for sidebar routing (`""` = scheduled, `"handoff"` = one-shot from activity-monitor handoff proposals) |
| v40 | Rewrite orphaned `path=""` projects with random-UUID ids to the canonical `__standalone_chat__` sentinel, fixing FK violations on workspace/session insert for callers that pass the sentinel id |
| v41 | Identity tables (`self`, principals and their aliases) |
| v42 | Principal sightings tables fed by the activity render path |
| v43 | Per-session sighting index for the People page |
| v44 | Voice fingerprint embeddings per principal |
| v45 | `stream_items.automation_context` for automation "continue in chat" sessions |
| v46 | `sessions.meeting_id` so Recent can collapse every chat a meeting produced |
| v47 | `stream_items.files` for non-image attachment metadata |
| v48 | `chat_deliverables` table for files an interactive chat agent produced |
| v49 | `principal_research` dossiers |
| v50 | Engagement service tables (`engagement_moments`, `engagement_receipts`, `engagement_prefs`) |
| v51 | `model_calls` table recording which account served each model call |
| v52 | `engagement_moments.stakes` stored apart from the derived score |
| v53 | `mobile_receipts` for idempotent phone requests |
| v54 | `sessions.kind` (`main`, `side`, or `""`) and `sessions.parent_id` for the chat model: one main chat plus side chats split off from it; legacy kinds stay inferred from the label prefix, `meeting_id` and `automation_result` |
| v55 | The day ahead: `calendar_events` (every EventKit or web occurrence in the sync window, vanished rows kept with `deleted_at`), `calendar_event_attendees` (resolved to principals), `calendar_sources` (which calendars the user uses and how that was noticed), `meeting_followups` (decisions and follow-ups a model pass found in a transcript) |
| v56 | `reminders`: every reminder read through EventKit (title, due time or day, list, priority), with `completed_at` and `deleted_at` kept so a ticked-off or deleted reminder resolves the commitment made from it |
| v57 | `mail_messages`: the headers of the last week's inbox and sent messages read from Mail.app over Apple Events (sender, recipients, scrubbed subject, date, read, flagged and replied state, the person the sender is), keyed by a hash of the mailbox and the message-id; never a body |
| v58 | Index stream items by session, tool and order for bounded proactive reads; index root moment identities without rejecting malformed legacy payloads |
| v59 | `stream_item_proactive_children` and `stream_item_status_entries` identity tables, equality indexes, insert/update triggers and transactional backfill for digest children and folded status entries |
| v60 | Assistant route provenance on `cron_runs` and `agent_run_routes`: ordered candidates, chosen backend, fallback assistants and reason, reported model and durable actions started; interrupted actions are blocked before startup catch-up while recurring schedules retain later occurrences |
| v61 | `principals.source` and `principals.external_ref`, with transactional backfill from legacy `system_contacts`/`vcard` creation history and source app handles; curated principals remain unmarked |
| v62 | Local contact cache, per-handle provenance, reversible contact-link receipts and post-link observation accounting |
| v63 | `stream_item_topics`, topic/run indexes and provenance inheritance/repair triggers |
| v65 | `principals.merged_into`, `principal_merges` and metadata-only `principal_merge_updates`: reversible People merges, hidden sources and atomic Undo |
| v66 | `stream_item_chips`: up to two prepared requests per main-chat answer; cascading row/session deletes and row-move/content-identity triggers; empty attempts enforce single flight and a rolling hourly budget |
| v67 | `outbound_sends_v1`: exact stored-draft snapshots and durable sending/accepted/finalized/failed/unknown state, keyed by moment and revision, with sent and follow-up times |
| v68 | `open_answers` and `open_answer_posts`: promise state and durable message associations |
| v69 | `open_answer_muted_kinds`: explicit answer-kind preferences |
| v70 | Open-answer Mail anchor/correspondent binding and named-time check state |
| v71 | Allow ask state for non-counting open-answer prompts |
| v72 | Persist open-answer posts held during quiet hours |

| v62 | Local contact cache, handle provenance and reversible contact links |
| v63 | Main-chat topic tags and run-proof triggers (#140, integration retains its migration) |
| v64 | Identifier-only memory/knowledge `context_uses` log and entry/time index |

### Cursor-based pagination for stream items

Long conversations are loaded in pages to avoid reading the entire `stream_items` table at startup.

```go
// Load the most recent 50 items.
page, err := store.LoadStreamItemsLatest(ctx, sessionLocalID, 50)

// Scroll back: load 50 items before sort_order cursor.
page, err = store.LoadStreamItemsBefore(ctx, sessionLocalID, cursor, 50)
// page.HasMore indicates whether older items exist.
```

### Turn-based loading for session restore

On startup, `LoadRecentStreamItemsByTurns` loads the last N user-visible conversation turns per session, skipping intermediate tool calls. Sessions with fewer user messages than the turn limit fall back to loading the most recent 50 items, preserving pre-user content (e.g. transcript body from auto-created chats).

### Atomic bulk saves

Several helpers wrap multiple upserts in a single transaction:

```go
// Save project + sessions together.
err = store.SaveProjectWithSessions(ctx, project, sessions)

// Save project + workspaces + sessions together.
err = store.SaveProjectFull(ctx, project, workspaces, sessions)

// Insert project/workspace/sessions only if they don't exist (ON CONFLICT DO NOTHING).
err = store.EnsureProjectStructure(ctx, project, workspaces, sessions)
```

### Rewind / checkpoint support

Before each AI turn the app records which files were modified (`checkpoint_files`) and, for Codex worktree sessions, a full git snapshot (`git_snapshots`). Both tables are keyed by `(session_local_id, sort_order)` so the rewind feature can truncate history from any point.

```go
// Save a checkpoint before writing files.
err = store.SaveCheckpointFile(ctx, CheckpointFileRow{...})

// Load all checkpoints from a given turn onwards.
rows, err = store.LoadCheckpointFilesFrom(ctx, sessionLocalID, fromSortOrder)

// Save a git worktree snapshot.
err = store.SaveGitSnapshot(ctx, GitSnapshotRow{...})

// Find the most recent snapshot at or before the rewind target.
snap, err = store.LoadGitSnapshotAtOrBefore(ctx, sessionLocalID, sortOrder)
```

### Sidebar groups

Custom sidebar groups let users organise chat sessions and automations into labelled, collapsible groups.

```go
// Create a group.
group, err := store.CreateSidebarGroup(ctx, "My Group")

// Add a chat session to the group.
item, err := store.AddItemToSidebarGroup(ctx, group.ID, "chat", sessionLocalID)

// Load all groups with their items.
groups, itemsByGroup, err := store.LoadAllSidebarGroups(ctx)
```

Item types are `"chat"` or `"automation"`. Each item is unique within a group (`group_id, item_type, ref_id`). Orphaned refs are hard-deleted during orphan cleanup.

### Cron jobs

Cron jobs automate recurring AI agent tasks. Each job stores a schedule (`at`, `every`, or `cron` kind with JSON config), a target config, retry policy, and run history.

```go
// Save (upsert) a job.
err = store.SaveCronJob(ctx, CronJobRow{...})

// List all jobs.
jobs, err := store.ListCronJobs(ctx)

// Record a run start.
err = store.InsertCronRun(ctx, CronRunRow{ID: runID, JobID: jobID, Status: "running", ...})

// On app startup, mark any in-flight runs as errored.
err = store.MarkStaleRunsAsError(ctx)
```

### Extractor records

Extractor records store the structured output of AI pipeline runs (e.g. Slack/Discord message digests). Each record is keyed by `pipeline_run_id` so retries upsert in place instead of inflating the table.

```go
// Save (or update on retry) an extractor result.
err = store.SaveExtractorRecord(ctx, ExtractorRecordRow{
    ID:            "some-uuid",
    PipelineRunID: "pipeline-abc123",
    Platform:      "slack",
    ChannelName:   "#eng",
    ProcessedAt:   time.Now().UnixMilli(),
    Title:         "Weekly digest",
    FullOutput:    "...",
    Deliverables:  `["summary.md"]`,
    MessageCount:  42,
})

// Get a single record by ID (includes full_output).
record, err := store.GetExtractorRecordDetail(ctx, id)

// Query all records for a specific date (full_output excluded for performance).
records, err := store.GetExtractorRecordsByDate(ctx, "2026-04-13")

// Get per-day counts for a Calendar/heatmap view.
counts, err := store.GetExtractorRecordCountsByDateRange(ctx, startMs, endMs)

// Fetch full output by pipeline run ID (returns "" if not found).
output, err := store.GetExtractorFullOutputByPipelineRunID(ctx, "pipeline-abc123")

// Delete a single record by pipeline run ID (e.g. user removes a history entry).
err = store.DeleteExtractorRecordByPipelineRunID(ctx, "pipeline-abc123")
```

### Composer suggestions

LLM-generated prompt chips are cached with a 24-hour TTL and pruned on each write to keep the table small.

```go
// Save a fresh batch (expires 24 h from now).
err = store.SaveComposerSuggestions(ctx, ComposerSuggestionsRow{
    ID:          uuid.New().String(),
    Suggestions: `["Summarize this", "Write tests"]`,
    ExpiresAt:   time.Now().Add(24 * time.Hour).UnixMilli(),
})

// Load the most recent batch.
row, err := store.LatestComposerSuggestions(ctx)

// Prune expired rows, keep the N most recent.
err = store.CleanupComposerSuggestions(ctx, keepRecent)
```

## Dependencies

| Module | Purpose |
|---|---|
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGO) |
| `database/sql` | Standard Go database interface |
| `github.com/google/uuid` | UUID generation for sidebar group IDs |

## Running Tests

```bash
cd wails-gui && go test ./storage/...
```

Tests use an on-disk SQLite file in `t.TempDir()` (not in-memory) to exercise real WAL behaviour and directory creation. The suite covers migrations, CRUD operations, cascade deletes, pagination walk-backs, orphan cleanup, legacy schema repair, and edge cases like empty sessions and cursor exhaustion.

Migration v63 adds `stream_item_topics` with cascading row/session foreign keys and topic/run indexes. Run inheritance triggers attach tags in the same transaction as assistant and tool rows, and copy the recorded source proof into those rows’ `automation_context`. The migration keeps the existing chat table and rows; a trigger repair can backfill recorded proof metadata. A schema-62 binary refuses the new stamp; restore a verified pre-migration snapshot to downgrade, never lower the stamp.

Topic structures are also checked on stamped v63 opens. Startup transactionally repairs missing/outdated topic triggers, recovers only already-recorded run proofs, and marks tagged rows without an original proof unproven. Healthy trigger definitions avoid a history backfill.

### Reversible People merges (v65)

`principals.merged_into` hides merged profiles without removing them. The
`principal_merges` receipt records typed before/after rows, their IDs and old
owners, the original target baseline and alias provenance. Each version-2
receipt contains only its own source contribution and the target baseline;
it excludes sibling sources' aliases, handles, previews and aggregate counters.
Text cells use base64 bytes so JSON cannot alter invalid UTF-8 stored in SQLite.
Legacy or malformed receipts return typed `invalid_receipt` without mutation.

Successive merges into one target have independent receipts. Undo subtracts
that receipt's counters and recomputes first/last sightings from the original
baseline, remaining source contributions and a metadata-only observation
journal (`principal_merge_updates`). The journal records app, session, count
and time, never message text. It also covers messages with empty previews and
messages outside the rolling preview window. New observations stay with the
target; profile edits, including a rename, remain intact. A routine save that
resends the full alias list does not adopt copied aliases: Undo removes those
introduced by the merge. Only aliases newly added relative to the pre-save set
are attributed to the later edit. Handles retain their
stable IDs and contact provenance. Either order works for Undo of successive
merges into one target. When a target was itself merged into another person,
undo the containing merge first (`merge_dependency`). Contact-linked sources
remain refused.

Undo is atomic. Unknown and already undone receipts return `merge_not_found`
and `already_undone`; destructive ownership or counter rewrites can return
`merge_conflict`. Successful Undo erases snapshot payloads and observation
journal rows, retaining only the receipt identity and undone timestamp.
Deleting a visible person transactionally removes its entire hidden-source
family, cascading voice/research/handle data and deleting every referencing
merge receipt and journal. Independent sibling receipts do not contain that
source's data and remain unchanged and undoable. Stale writes to hidden profiles
are refused. Loop attribution reads each hidden source's original session
lineage and current origin protection before folding its person-ID moments or
exact name/alias commitments into the survivor.

The legacy anonymous cleanup remains destructive. Code rollback to schema 64
requires the verified pre-migration snapshot; never lower the schema stamp.
