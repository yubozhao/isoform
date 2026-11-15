# Test audit, November 2026

Bo asked on Nov 14: "we have a lot of tests, is this too much? we are just testing for the sake of testing". This is the plan to keep the tests that protect what matters, prune the rest, and get a merge-queue run under 10 minutes. Nothing is deleted until Bo approves this plan.

## What we have

The desktop Go timings were measured at tip `33bfcdc2e` on the Mac mini with `go test -json` (GOMAXPROCS=4, nice 10, with the merge queue running beside it), the frontend timings with `vitest --reporter=json` (3 workers).

| Suite | Tests | Time per merge | Notes |
|---|---|---|---|
| Desktop Go (`wails-gui` root package) | 6,797 | 21 to 28 min | One package, every test runs one after another (no `t.Parallel` anywhere) |
| yansu-agent | 808 | 3 min (in parallel with the rest) | 172 s of it is the sandbox (seatbelt) tests, which are security tests |
| Frontend (vitest) | 4,342 in 351 files | about 105 s | 97 s of summed time is starting jsdom for each file |

Where the desktop time goes:

| Test length | Tests | Total time |
|---|---|---|
| under 0.1 s | 3,958 | 52 s |
| 0.1 to 1 s | 2,483 | 792 s |
| 1 to 5 s | 330 | 635 s |
| 5 s or more | 26 | 192 s |

**The main cause is setup, not test logic.** A test that only reads from a few rows still takes 0.24 s, because it builds a fresh activity database and runs about 80 migrations (290 ms), plus a fresh sessions database (55 ms). Copying an already-migrated database file and opening it takes 8 ms. About 2,500 tests pay this cost.

**Pruning saves little time.** Tests for the deleted and turned-off features take about 130 s of the 1,650 s, and the review-round files 148 s. Pruning is worth doing because those tests block merges and cost upkeep, but the speed comes from setup and from running in parallel.

**Why today's regressions got through.** The missing play button, the stale phone brief and the reload loop each sat between layers (Go store, binding, React page, phone). Every layer's unit tests passed. More unit tests would not have caught them; the daily real-app check in `docs/rework-2026-11.md` step 3 is where these belong.

## Kept untouched

Tests that guard privacy (withdrawal, forget, Closed Eyes, gates, redaction), data loss, money, the brief writer's rules, the phone protocol and crypto, and migrations. By name, that is about 1,640 desktop tests taking 624 s. They are not deleted or tagged off. They do get faster through the shared setup change below, which changes no assertion.

The proactive voice card is turned off, but its privacy gates (the saved first page following deleted and protected sources, #945) stay in the default run.

## Speed (no assertion changes)

| # | Change | Saves (serial) | Risk |
|---|---|---|---|
| S1 | **Migrated database templates.** `TestMain` migrates one activity database and one sessions database, and a fresh store in a test starts from a copy of that file. Migration tests that need an empty or old database already create their own file first, so they keep running every migration; any test that asserts a fresh migration opts out with one call. | about 600 s | Low: same schema, same code; the full suite proves it |
| S2 | **Run the desktop package as 3 processes in the queue.** Build the test binary once, list the tests, and run three binaries side by side, each with its own HOME. That also ends cross-test leaks through the shared HOME (the cause of the search folder flake fixed in #1020). | divides the rest by about 3 | Medium: tests that share a fixed port or file must be found; trial on the mini first |
| S3 | **An opt-in `slow` build tag** (like the existing `rc` and `updateqa` tags) for tests that measure speed on a large fixture, for example `TestTodayColdReadLargeFixtureAndCaptureChurn` (35 s) and the `*_perf_test.go` and `*_timing_test.go` files (88 tests). They run in the daily real-app check instead of on every merge. Privacy tests never get this tag. | about 60 s | Low |
| S4 | **Frontend:** use fake timers in the two tests that wait 5 s of real time, check the four slowest files (memory 20 s, Tasks 19 s, people 10 s, knowledge 9.5 s) for real-timer waits, and run the frontend suite beside the Go shards in the queue. | about 15 s, and the suite no longer adds to the queue time | Low |
| S5 | **yansu-agent:** `TestBashLauncherThrowawayLaunchdJob` creates a real launchd job and fails on the mini. Gate it behind an opt-in variable. | 6 s, one false failure | Low |

Expected queue run after S1 to S3: about 2 min to build and vet, then about 5 to 6 min for the three desktop shards, with yansu-agent and the frontend running beside them. That is about 8 to 9 minutes, under the 10 minute target.

## Pruning

| # | Area | Tests | Plan |
|---|---|---|---|
| P1 | **Deleted features** (shadow agent, Agency, our own computer-use driver, opencli) | 252 Go, 34 frontend | Deleted together with their code (rework step 1). No separate PR. |
| P2 | **Turned-off features** (automations and cron prompts, playbooks and SOPs and deliverables and goals, calendar holds, proactive voice cards) | 335 Go, 199 frontend | Move behind an `offfeatures` build tag, not delete: the code stays and can come back one feature at a time, and its tests come back with it. Privacy gates in these areas stay in the default run. |
| P3 | **Wall-clock timing tests** | 38 files assert on elapsed time; 37 sleeps of 100 ms or more (up to 61 s) | Rewrite each to wait on the event it means (a channel, a fake clock, a done signal), or move it to `slow` when it measures speed by design. These are the flaky ones that cost time today. |
| P4 | **Duplicates** in review-round files (`*_round1_test.go`, `*_r41_r42_test.go` and similar) | 59 files, 342 tests, 148 s | Read each one against its main file. Fold repeats into the main file's table test; keep the ones that guard a distinct case. Estimate about a third removed; the exact count comes per PR. |
| P5 | **Frontend tests that only pin markup** (CSS class names, texture classes, theme files read as text) and debug-panel tests for deleted features | about 100 tests (for example `sidebar.test.tsx` 26 class checks, `button.texture`, `settings-ax-texture`, `TopicComposer.polish`, 11 debug-panel files with 42 tests) | Delete the class-only checks; keep behavior checks in the same files. The frontend has no snapshot tests. |

## Order of PRs, each small and through the merge queue

1. S1 templates (biggest saving, no assertion changes)
2. S2 three shards in `mq-runner.sh` (queue tooling, trial on the mini first)
3. S3 `slow` tag, then P3 wall-clock rewrites, a few files per PR
4. S5 and S4
5. P2 `offfeatures` tag, one area per PR
6. P4 duplicates, a few files per PR
7. P5 frontend markup-only tests

P1 goes with the rework's deletion PRs.

## Measured afterwards

After each speed PR, the queue's own log gives the desktop suite time. The goal is a queue run under 10 minutes at the same pass rate. Any test moved behind `slow` or `offfeatures` is listed in its PR so it can be found again.

## Outcome (Nov 14)

| Step | PR | Result |
|---|---|---|
| S1 migrated database copies | #1025 | Desktop root package 1,650 s to 527 s on the mini, no assertion changed |
| S2 three shards in the queue | queue tooling | Desktop step 281 s; a whole queue run 6.5 min (was 22 to 25 min) |
| S3 `slow` tag | skipped | After S1 the biggest slow test (47 s) also checks a withdrawal; the rest add up to about 6 s |
| P1 deleted features | #1032 | 12 Go files, 54 tests behind `offfeatures` until the code is deleted |
| P2 turned-off features | #1031 | 22 Go files (50 tests) behind `offfeatures`; 11 frontend files (70 tests) behind `VITEST_OFFFEATURES=1` |
| P3 wall-clock tests | #1033 | 6 fixed budgets replaced by behavior checks (held dependency or a bound relative to the slow path) |
| P4 duplicates | none | No two test bodies are identical; the near-identical pairs check different inputs |
| P5 CSS-class tests | #1030 | 5 tests removed; the rest of the class checks guard the active-row rule or the design system |

Queue flakes found and fixed along the way: #1020, #1024, #1029, and two in #1025. The open one is a goroutine leaked by an earlier test that calls `GetMemoryEntries` and `GetKnowledgeEntries` under whatever HOME is current (seen on #1021, `TestRemoveExternalMcpConfig`).
