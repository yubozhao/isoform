# Today rework, 2026-11-15

## Ranked goals for today's brief (Bo, 2026-11-15)

> "the most important thing is show the latest brief, we can tradeoff other stuff ... 1. show up latest 2. quality. we can give up privacy and citation and others to make sure we reach those 2 goals."

A lower goal never blocks a higher one.

1. **The latest brief shows.** Once built, the newest brief shows on the computer and the phone, with its words and its recording.
   - Nothing after the build withholds it: no refresh or projection, no citation or evidence check, no live privacy re-check, no seal or store failure, and not Closed Eyes. Closed Eyes stops new capture only.
2. **Quality.** What shows is the words and recording the build made.
   - Cards may refresh around them.
   - A citation that no longer resolves loses its link, nothing more.
3. **Everything else gives way.** That covers privacy re-checks, citations and the rest.
   - Only the person's own explicit actions (forget, withdraw, a stated exclusion) change the words after the build.
   - Those actions leave out the paragraphs that cite what was withdrawn, and never blank the brief.
   - A recording that speaks a withdrawn paragraph is not played.

Where it is decided: `mobileBriefShowBuilt` (`wails-gui/mobile_brief_show_built.go`), at the read that Today, the menu bar and the phone package share.

Bo: "systematically work on the today page. if we can't get this page right. we failed."
Owner: the daily real-app test session (branch `claude/today-rework`). Root causes only, no symptom patches.

## Trigger

This morning Bo saw no morning brief. Yet the 2026-11-15-morning brief was built at 06:51 with audio, `GetLatestBrief` returned rows=1, and the mobile-brief log said written=shown. There were also 83 brief reads within one second at 11:26:57.

## Steps (each is reported to the hub when done)

1. **Ground truth on Bo's app (build 361d5ae0e).** Read only log lines, metadata and timings, never brief or memory text. For each card (brief, Your week, topics, before you talk, chat), record what Today should show now and what each binding returned, with times. Take the brief-audio session's (local_5861963b) findings on the brief.
2. **Data flow map** (`docs/today-data-flow.md`, one page). Cover:
   - every binding Today calls on load and on each event;
   - the saved copy or snapshot each one reads;
   - what makes that copy stale;
   - the states each card can be in (loading, empty, withheld, stale, error).

   Mark every path where stored data exists but the page shows nothing or old data.
3. **Failure history.** Group the last 30 days of Today and brief fixes by root cause, and name one structural fix for each group.
4. **Make the daily check catch each failure mode.** Add seeded scenarios in which the brief exists and Today must paint it within its budget:
   - on a cold start;
   - after a restart;
   - after a re-seal;
   - after a withdrawal (the old brief or the right withheld state);
   - around midnight;
   - during a storm of repeated reads.

   Each scenario fails on today's build before its fix lands.
5. **Fixes.** One PR per root cause, against `feat/iphone-companion`, with the step 4 check failing before and passing after. Queue each with `mq add` and `mq front`.

## Rules

- No regressions.
- Privacy first: counts and timings only from Bo's data.
- Follow the design system.
- Route all copy through react-i18next.
- Never launch Bo's app with the bridge; the browser bridge runs only on the mini's test app.

## Findings so far

**Step 1, ground truth (Bo's app, 2026-11-15).** Sources: log lines, file timestamps and status fields only.
- The morning brief was written at 06:51:21 with 4 paragraphs and a recording.
- 26 s later the Today refresh withdrew both (`dependency_dropped`).
- The working copy, the sealed copy and the phone package were all made from the stripped brief, so the computer and the phone showed the same missing brief.
- The read log said `written=shown` because two fallback lines remained.
- The Mac was locked from before 06:13 until 11:25. Today read nothing until the unlock, which is the activity gate working as designed.
- About 1,200 brief reads an hour come from internal timers, not from Today.

**Root cause 1: anything after the build could blank the brief.**
- The trigger on 2026-11-15: `projectMobileBriefToday` withdraws the written brief and its recording when a source the words depend on drops out of the refreshed evidence. That evidence is capped at two calendar events, and this morning's brief cited five.
- More than ten other paths could also blank the brief or drop its recording after the build: read-time gates, closed purposes, a failed seal, Closed Eyes, a store error and audio binding checks.
- Fix: one rule at the shared read, `mobileBriefShowBuilt`. Whatever those paths leave, the build's words and recording show. Only the person's explicit withdrawal leaves out the paragraphs that cite what was withdrawn.
- The read log says shown only when the model's words are shown.
- Gates:
  - a randomized property test over everything that can happen after a build;
  - a read-only real-data replay of Bo's two stripped briefs (11-15 morning, 11-14 evening), both back to 4 paragraphs with audio and no dangling citations;
  - the daily check's `stripped-brief` scenario.

**Step 2:** the data-flow map is in `docs/today-data-flow.md`, with 13 paths where stored data exists but Today shows nothing or old data.

**Step 3:** the failure history is in `docs/today-failure-history.md`. 523 fix commits in 30 days fall into these root-cause groups:
- C: per-card fetch state;
- B: read-time gating;
- A: the brief copy lifecycle;
- D: clock and day boundaries;
- E: text guards.
