# Brief seal v2

## Why the brief keeps disappearing

A sealed brief is a copy of what the brief says, made once when it is built.
Before this change, every read also asked whether the sources the brief cites
had changed since the copy was made. Any change counted as a withdrawal, and
the whole written brief was blanked: the narrative, audio, cards and weekly
recap. Cited sources change by design: activity summaries are rewritten all
day, calendar rows are updated by sync and attendance, files are edited, and
retention removes old rows. So a brief was hidden because it was changed, not
because anything was withdrawn. Every read also ran source lookups on the same
database as the chat stream.

## The rule

1. A sealed brief is a snapshot, like a sent letter. It is sealed once when it
   is built, after the privacy gates pass.
2. Only an explicit privacy action withdraws a source. Edits, regenerated
   summaries, calendar and mail sync, attendance updates, retention cleanup and
   Isoform's own writes do not withdraw anything, and they never blank a copy.
3. A read compares the privacy print the copy was sealed under with the
   current one. The print is in memory and reads no store. Equal: the copy is
   returned. Different: the copy is amended before it is shown.
4. An amend uses the sources the privacy changes named. It drops only the
   paragraphs, cards, items and evidence that cite a withdrawn source, keeps the
   rest, and saves the copy under the current print. It reads no live source
   and runs no gate. A written synthesis (narrative, weekly recap) is withheld as
   a whole when any of its paragraphs cites a withdrawn source, because its
   words can name an item without citing it.
5. When a change named no sources (an app, a pause, an account, a session, a
   tombstone written from brief feedback), or the log has no entries for the
   copy's epoch, the amend runs the gates again for that brief. The gates are the
   only way to know what such a change removes. This is the one place an amend
   reads live sources.
6. A Today refresh is a freshness change. It stores its result as it always did
   (`store.save`, which seals the brief again), so a handoff, a calendar change or
   a cron job reaches the sealed words. A mail sync is the exception: it runs a
   live pass (`refreshTodayLiveSoon`, `refreshToday(liveOnly)`) that stores and
   seals nothing for a brief that has a sealed copy, unless the privacy gates now
   refuse words in that copy (a mail subject echoed in the text, a held purpose,
   a withdrawn source). Then the copy is sealed again through the gates, which is
   the amend. A brief with no copy is sealed as before.

## The withdrawal log

Each privacy epoch change is written to `briefs/withdrawals.json` with the
sources it named, before the epoch file is written. The log keeps the last 500
changes. A copy can be amended from the log only when it was sealed at or after
the log's floor, and every change after its epoch named its sources. A log that
is missing or behind the epoch (an older build, or a save that did not finish)
sets the floor to the current epoch, so older copies take the gate path.

## Privacy actions and print or epoch callers

Named (the log records the source; the amend drops what cites it):

- Deleting a person (identity_bindings.go, `DeletePrincipal`) and a person removed
  from the system contacts (identity_contacts.go): keyed by the principal id, the
  `principal` source kind. An edit to a person is not a withdrawal.
- A handoff removal (mobile_brief_finished_read.go, handoff `RemovalHook`).
- Deleting or forgetting a source: `publishPrivacyWithdrawal` with a source scope
  (memory forget and delete in memory.go and memory_forget.go, topic withdrawal
  in topics_withdrawal.go, activity and knowledge deletion in activity_store*.go
  and knowledge.go, file removal in activity_store_files.go, purpose forget in
  purpose_user_actions.go).
- A memory file removed on disk (memory_store.go, `memorySourcesRemovedOnDisk`).
- A memory forgotten by tombstone (memory_tombstone.go, `recordMemoryTombstoneScoped`).

Unnamed (the gates decide on each affected brief):

- Privacy actions that do not name a brief source: account, session, app, window
  and time-range withdrawals through `publishPrivacyWithdrawal` (persistence.go,
  discord.go, telegram.go, feishu.go, wechat.go, slack.go, composio.go,
  sensitive_apps_bindings.go, voice_input_apps_bindings.go, activity_monitor.go,
  closed_eyes_mode.go, and the triage protection in activity_store.go). A session
  id is not always the id a brief cites (a triage session differs from its
  summary row), so these are not matched by id.
- Deleting meeting notes (meeting_notes.go, `DeleteMeetingNotes`).
- An observation pause (observation_pauses.go).
- A memory tombstone written from brief feedback (mobile_brief_feedback.go).
- A goal move (`MoveGoal`, purpose_user_actions.go). A closed goal withholds its
  content (closed purposes), so every move is a privacy action that names no
  source; the gates decide. The bump stays on every move.
- App events in `mobileBriefPrivacyInputEvents` (app.go, `emitBrowserEvent`):
  `sensitive-apps:updated`, Closed Eyes, `background-listening:changed` (listening
  on or off), and `topics:withdrawn`. Each is a user privacy setting or action.

Not privacy actions, so they advance nothing:

- `mail:synced`: a sync. It runs a live Today pass (rule 6) and seals a sealed
  copy only when the gates now refuse its words (for example a mail subject
  echoed in the text). It is not a privacy-epoch change.
- `calendar:synced` and `people:updated` (freshness only).
- Memory file edits and new files (memory_sync.go, memory_store.go).
- Memory tombstone preserved-record upkeep (memory.go).
- Handoff saves and edits (`ChangedHook` removed).
- Belief corrections (today_week_belief.go, removed).
- Activity summary rewrites, calendar sync, attendance, file edits, retention.
- Isoform's own writes that add to a brief (audio, week answers): these call
  `briefSourceChanged`, which re-seals, and do not advance the epoch.

## Migration of copies the old rule hid

Copies sealed under the old edited-source rule have no `rule` field (seal v2
writes `rule: 2`). Those whose `withheld_reason` is `source_withdrawn` were hidden
as a whole, and seal v2 reads only the copies it wrote, so they would stay hidden.
`migrateOldSealedCopies` (mobile_brief_migrate.go) runs once per start, off the
read path:

- It selects copies with schema 2, no rule, and `source_withdrawn`, whose brief is
  from today or the two local days before. The withdrawal log keeps only its last
  500 changes, and the gates cannot tell whether a source cited by an older brief
  was deleted long ago, so an older old-rule copy is left as it is.
- It seals the stored brief (the `.json`) again through the current gates, under
  the current privacy print. The result is kept whatever it is.
- It then drops what cites a recorded keyed withdrawal from the saved copy, as the
  keyed amend does. A transient withholding leaves the old copy for the next start.
- A migrated copy has `rule: 2`, so no later start or read selects it again.

`brief-replay` reports the prediction as `migrate=shown|withheld|none`, from the
built brief and the keyed withdrawal log. It does not run the gates.

## Still open

- Unnamed changes re-run the gates, which may drop items that retention already
  removed. Naming sessions and accounts needs a map from their ids to the ids
  the briefs cite.
- A mail account removal has no keyed form yet: composio's account withdrawal
  is unnamed. No mailbox or sender exclusion exists in the code.
- A mail sync does not change the sealed copy of an existing brief unless the
  gates refuse its words. New mail therefore shows in the sealed words of a
  scheduled brief only in the next brief built. This is the trade-off of the
  snapshot rule and needs a product decision.
- A copy whose day has changed is re-sealed off the read path. This is the one
  retry a read can schedule, and the job state bounds it.
- A brief with no copy is sealed on first read (legacy and pending briefs).
- Launch re-seals every stored brief once, which repairs copies that were
  hidden by the old edited-source rule.
