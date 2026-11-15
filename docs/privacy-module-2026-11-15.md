# internal/privacy (2026-11-15)

Step 2 of `docs/rework-2026-11.md`, first module. Pure refactor: no behavior
change, every existing test passes before and after with no assertion change.

## Why first

Every other module (brief, today, chat, memory, mobile) checks the privacy
state before it shows or sends anything. Today that state is spread over the
root `wails-gui` package (withdrawal events in `privacy_withdrawal.go`, Closed
Eyes in `closed_eyes_mode.go`, saved-copy stamps in `saved_copy_stamp.go`, the
privacy epoch in `mobile_brief_finished_read.go`, excluded apps in
`internal/pii` plus `sensitive_apps_bindings.go`). A module that depends on
privacy cannot leave package main until privacy has.

## Shape

Follow `internal/brief`: a small exported interface, its own tests, no import
of package main, and the root package calling it. The root keeps thin aliases
(`type privacyWithdrawalScope = privacy.Scope`, and so on) so the hundreds of
call sites do not change in the same PR; later PRs can rename callers module by
module.

What moves in, one small PR each (under about 800 changed lines):

1. **Withdrawal events**: `Scope`, `ScopeKind` and its constants,
   `Withdrawal`, and `Publisher` (Subscribe and Publish). The root keeps
   `publishPrivacyWithdrawal`, because it also tells the brief (that call goes
   away when the brief listens as an ordinary subscriber).
2. **Closed Eyes pause state**: `PauseSnapshot` and `PauseState` (the atomic
   flag plus timed-pause deadline). Starting and stopping capture stays in the
   root; only the state every reader checks moves.
3. **Saved-copy stamps**: the read stamp format (epoch, Closed Eyes,
   excluded-app version) as `privacy.ReadStamp`, so every saved copy builds it
   the same way.
4. **Privacy epoch** (later, after the brief owner agrees, since it lives in
   `mobile_brief_finished_read.go`): the durable counter every withdrawal
   advances and its saved file.

Not moved: forget (memory tombstones, `memory_forget*.go`) reads and writes the
memory folder and is part of `internal/memory`'s move; it will call the module
for the epoch and the withdrawal event. Excluded apps already live in
`internal/pii`; the module reads its version and does not duplicate it.

## Rules the module keeps

- Data stays on the computer: the module has no network code.
- Closed Eyes always wins: a pending policy restore reads as paused.
- Withdrawn data never shows again: every withdrawal reaches every subscriber
  synchronously, before the action that withdrew it returns.

## Files avoided today

`stream_page_snapshot.go`, `derived_views*.go` and the Today and brief page
files belong to the Today owner this week; none of these PRs touch them.
