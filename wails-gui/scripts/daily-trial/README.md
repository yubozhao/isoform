# Daily real-app test

Two runs every morning, before the day starts (docs/rework-2026-11.md, step 4).

## 1. Real app on test data (Mac mini, 05:00)

`daily-trial.sh` checks out the feat tip, builds it with `build-wails.sh`, seeds a fresh data folder with a scripted day (`TestTrialSeed` in `wails-gui/trial_seed_test.go`: invented people, a main-chat exchange, a watch recording, and the morning brief due three minutes after launch; never anyone's data), launches the built app on that folder with the browser bridge on, and drives it with Playwright (`frontend/e2e/daily-real-app.spec.ts`, `playwright.daily.config.ts`):

| Check | What passes |
|---|---|
| today | Today shows within its paint budget |
| today-920, today-1440 | Today in a 920 and a 1440 px window: no sideways scroll, no section over another, nothing past the column (`e2e/today-layout-checks.ts`) |
| today-layout-920, today-layout-1440 | the same rules on fixture data with a full day strip, plus the strip's own: hour labels never touch, no cut names, free and under-strip labels fit (`e2e/today-layout.spec.ts`, its own Vite server, WebKit) |
| activity-920, activity-1440 | Activity's timeline in a 920 and a 1440 px window: no sideways scroll, no section or row over another, nothing past the timeline (`e2e/page-layout-checks.ts`) |
| activity-layout-*, activity-agents-layout-* | the same rules on fixture data, Your day and Agents (`e2e/page-layout.spec.ts`, run with today-layout) |
| stripped-brief | a seeded brief that a later pass saved without its words and recording (the 2026-11-15 failure) still shows its built words on Today and plays |
| brief-audio | the scheduled brief arrives on Today, replacing the seeded one, with its recording, and the play button switches to playing |
| chat | a message is sent and an answer arrives |
| tasks, activity, about-you, how-you-do-things, who-you-know, settings | the page shows within its budget |
| watch-recordings | Activity > From your watch lists the seeded recording |
| quick-chat | one prompt through the quick chat helper's own route to the launched app gets an answer (`quick-chat.mjs`; the panel window itself is not drawn) |
| phone-relay | `TestMobileEndToEndThroughRelayBinary`: the relay binary, the hub code and a scripted phone pair, and a brief reaches the phone (not the launched app) |

Every page must also raise no page errors and match the baseline screenshot (`~/isoform-trials/baseline`; a missing baseline is written by that run). A call the browser bridge refuses fails the run, except the ones kept native-only on purpose (listed in the spec). The app's own log is read for `first_paint` times, error lines (`known-noise.txt` lists the ones a test machine always has) and how each brief read went (shown, or withheld and why).

Results: `~/isoform-trials/daily/<date>.md`, artifacts in `~/isoform-trials/daily/<date>/`, and one line in `~/isoform-trials/daily/latest.txt` for the morning report.

Run it by hand on the mini: `ISOFORM_TRIAL_BRANCH=<branch> bash ~/isoform-trials/bin/daily-trial.sh`. `ISOFORM_TRIAL_REUSE_BUILD=1` keeps the last build when only the spec or the seed changed.

## 2. Replay of the previous day (the person's computer, 04:30)

`replay-day.sh [YYYY-MM-DD]` reads, never writes: the stored briefs through `cmd/brief-replay` (counts only), and the app's log for first paints, binding timings, error counts by log tag and `[chat-turn]` outcomes. No message, title or log text is kept. It saves the day's numbers, compares them with the previous saved day, and writes `~/isoform-trials/replay/<date>.md` and `latest.txt`.

## Today's read budgets

`today-budgets.json` sets, for each binding Today reads, a budget for window reads (`origin=window` in `[perf] binding=` lines): a steady p95 (100 ms, since Today reads saved snapshots) and a maximum for the first 90 s after launch (1500 ms). `today-budgets.mjs` checks a log against it. The replay marks the day `OVER BUDGET` in `latest.txt` and its report, and the real-app test fails, when any budget is exceeded. A binding that has to be slower gets its own budget in the JSON, on purpose. Tests: `node --test today-budgets.test.mjs`.

## Install

On each machine, once: `./install.sh trial` (Mac mini) or `./install.sh replay` (the person's computer). Each adds one launchd job. `ISOFORM_TRIAL_BRANCH=<branch> ./install.sh trial` pins the branch the trial builds, for one not merged yet.
