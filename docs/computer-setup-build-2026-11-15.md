# Building computer setup v1.0.0

Build note for `docs/handoff-2026-11-15/handoff-2026-11-15-2100-computer-setup-v1.0.0.md` (Claude Design, November 15). It says what exists, what each PR does and what waits on another PR or on design.

## What exists today

Setup v2 (`wails-gui/frontend/src/components/onboarding/`) is six screens in a fixed 600 x 560 window: Notices, Your AI, Sees, Runs on this computer, Send, Reaches you, then the finish into the menu bar.

| Handoff part | Today's code | Gap |
| --- | --- | --- |
| Four screens (ob.*) | Notices and Send are the nearest | New screens, built from the app's own parts with "Sample" |
| Rail of five steps | Six progress dashes | New rail |
| 1 · What Isoform sees, one Allow per row | `RequestAllPermissions` asks every prompt in turn after one Turn on | A one-key ask, `RequestSetupPermission(key)` |
| 2 · Is this right? | Nothing builds setup claims. #1118 has the Y, N, E claim rows on You | Claims from calendar and mail with a Why (backend) |
| 3 · What it can do today | `GetTodayTopics` has `FirstRun` | Day-one work items and an Add for "What are you working on, or following?" |
| 4 · Your agents | `GetAgentProbe` and `GetYourAgents` | Connect with the confirmation is #1153; the join message is #1122 |
| 5 · Your phone and watch | ReachesScene: the code, pending pairing and approve | New words; the same bindings |
| Finish lands on Today | Finish opens Chat | Land on Today |
| Rule 10, first brief time | `requestFirstBrief` makes one 2 minutes after setup | The next brief time if an hour away, otherwise an hour after setup, with the time shown on Today |

## Rules this build keeps

- One macOS prompt at a time, each only after its row's Allow…; nothing is asked on the four screens.
- No prompt when the app boots straight into the main screen (the sequencer never asks at launch).
- Every privacy choice setup offers now stays unless the handoff removes it: the On this computer lines, share usage counts, each permission's own answer, the local models with Later, notifications, and approve or decline for a pairing.
- Every step can be skipped and done later in Settings.

## PRs

1. **Backend:** `RequestSetupPermission(key)`, which asks one permission (Screen Recording keeps its reopen), and the first brief time from rule 10, with `GetFirstBriefTime()` for Today's band.
2. **Shell, the four screens and steps 1 and 5:** the larger window, the rail, Skip this step, Finish setup to Today.
3. **Step 3:** the topics first run with Add.
4. **Step 4:** after #1153 and #1122 merge.
5. **Step 2:** setup claims, backend and rows.

## Asked of design

Questions that change what the screens show go to Isoform design through the hub; see the hub log for the answers.

## Where a skipped step is done later

From the Settings owner (#1125, #1139). Open with `settings:open` and `(page, { anchor })`; none is linked from setup today.

| Step | Page | Anchor |
| --- | --- | --- |
| 1 · Screen, Calls (microphone), Mail, Calendar | `activity` | `sees-screen`, `sees-calls`, `sees-mail`, `sees-calendar` |
| 1 · Notifications | `notifications` | |
| 1 · Local models, share usage | `ai-provider` | `{ advanced: true }` |
| 4 · Your agents | `ai-provider` | |
| 5 · Your phone and watch | `iphone` | |

Steps 2 and 3 are done later in You and on Today.
