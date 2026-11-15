# Today page: data flow map

Read-only trace of the Today page on `claude/today-rework` (worktree `daily-real-app-test`). The app was not run and no counters were taken, so the "83 reads in one second" explanation is a hypothesis.

Confidence tags: **[high]** read directly in code. **[med]** follows from the code paths but not run. **[low]** a hypothesis to check in logs.

Frontend paths are under `wails-gui/frontend/src/`. Go paths are under `wails-gui/`.

---

## 1. Bindings Today calls

Source of truth for the cache: `lib/query-cache.ts` (a per-name cache with a TTL). `lib/page-queries.ts` wraps reads in `cached(...)` and maps events to re-reads. "Derived" means a Go snapshot cell (`derived_views.go:371 derivedRead`) that is rebuilt on schedule or on event, not on read.

| Binding | Caller (trigger) | Go file:line | Reads | Invalidated / refreshed by |
|---|---|---|---|---|
| `GetLatestBrief` | `useLatestBrief` (`TodayPage.tsx:122`): mount, `brief:stored`, `brief:building`, privacy sweep, show after 15 min, "Make my brief now" | `brief_today.go:176` -> `launch_brief.go:14` `finishedLatest` -> `mobile_brief_current.go:176` `latestShown` | Sealed copies in the brief store; `today.json` before the first brief; live signals only through `refreshTodaySoon` | Frontend: cache TTL 15 min (`page-queries.ts:263`), `clear` on every `brief:stored` (`page-queries.ts:166`). Go: `todayRefreshBudget` 15 min and `todayRefreshGap` 5 min (`mobile_brief_today_refresh.go:41-45`) |
| `GetTodayBriefByID` | `useLatestBrief` when a `briefRequest.briefId` is set and not `followLatest` (`useLatestBrief.ts:64-68`) | `brief_today.go:346`; falls back to `GetLatestBrief` at `:348, :353, :366-367, :381` | Same stored brief | Not cached; no event |
| `GenerateMobileBriefNow` / `GenerateTodayMissedBrief` | `makeNow` (`useLatestBrief.ts:144`) | `mobile_brief_seam.go:159, :169` | Starts a build | Emits `brief:building` (`mobile_brief_service.go:536, :736`) |
| `GetMobileBriefHealth` | `useTodayStatus` (`useTodayStatus.ts:45`) | `mobile_brief_seam.go:118` | Store runs and slot history | `mobile:updated`; resubscribes on show if it was held |
| `GetEngagementWeekSummary` | `useTodayStatus` (`:44`) | `engagement_api.go:180` (not traced further) | n/a | `mobile:updated`; prefetch |
| `GetTodayDayShape(date)` | `useTodayDayShape` (`TodayPage.tsx:140`) | `today_day_shape.go:69`, derived | Derived snapshot cell | `today-dayshape:updated`; hold settings (`useTodayDayShape.ts:64`); local midnight timer (`:69-86`); `brief:stored` touches it (`derived_views.go:663`) |
| `GetTodayBeforeYouTalk` | `useTodayBeforeYouTalk` (`TodayPage.tsx:167`) | `today_before_you_talk.go:34` (derived, keyed by date) | Calendar events in store. Returns nil (no card) when muted, Closed Eyes, or calendar permission off (`:65-70`) | `today-before-you-talk:updated`; `calendar:synced`, `activity:*`, `mail:synced`, `people:updated`; 30 s `activeInterval` invalidate (`useTodayBeforeYouTalk.ts:33`) |
| `GetTodayQuietLines` | `useTodayQuietLines` (`TodayPage.tsx:187`) | `engagement_quiet.go:35` (not traced further) | n/a | `brief:stored`, `engagement:settings-changed` (`page-queries.ts:48`); mount/show |
| `GetTodayTopics` | `useTodayTopics` (`TodayPage.tsx:202`) | `topics_registry.go:1804` (in-memory `snapshotRead`) | Topics service snapshot | `topics:updated`, `topics:withdrawn`; TTL 2 h, or 10 min while `refreshing` (`page-queries.ts:254-255`); budgeted resync (`useTodayTopics.ts:165-170`); window `SIDEBAR_TOPICS_CHANGED` |
| `GetTopicFinds` | `useTopicFinds` (`TodayPage.tsx:165`) | `topic_finds.go:1177` | Finds snapshot | `topics:finds`, `topics:updated`, `model-accounts:updated`, `ai:config-changed` |
| `GetGoalCheckIn` | `useGoalCheckIn` (`TodayPage.tsx:163`) | `goal_checkin.go:54` | n/a | `agency:updated`, `closed-eyes:changed` |
| `GetPortraitTodayCard` | `TodayPortraitCard` (`TodayPage.tsx:164`) | `about_you_portrait.go:2166` | Derived portrait card | `portrait:updated`; privacy sweep |
| `GetNeedsYou(0)` | `useTodayNeeds` (`TodayPage.tsx:236`; also `TasksPage`, `useTasksNeedsYou`) | `today_review_needs.go:77` | Handoffs and put-off store | `today:needs-changed` (`app.go:502`); timer at `nextReturnAt` (`useTodayNeeds.ts:25-35`); 30 s retry on failure (`:66`) |
| `GetDoneForYou(0)` | `useTodayDone` (`TodayPage.tsx:237`) | `today_review_done.go:292` | n/a | `today:done-changed` (`app.go:506`) only; no retry |
| `GetTodayPanelAnswer(ref)` x N | `useBriefSavedAnswers` (`TodayPage.tsx:123`): one per send-mode question or item card | `today_panel_answers.go:261` | Saved panel answers | Re-runs on every change of the `brief` object identity (`useBriefSavedAnswers.ts:24`); `brief:stored`, `today:answer-saved` |
| `GetWatchedToday` | `useWatchedToday` (`TodayFirstDay.tsx:42-52`), first days only | `watched_today.go:23` | n/a | Every 60 s (`activeInterval`) |
| `GetCalendarFirstDay`, `GetAssistantHistory`, `GetFirstWeekDay` | `useFirstDay` (`TodayPage.tsx:230`) | `calendar_first_day.go:65`, `assistant_history.go:139`, `tasks_page_api.go:40` | n/a | `assistant-history:updated`, `calendar:synced` |

Not traced: `MainChatPanel` reads, `TodayDayStrip` meeting target (`today_day_meeting.go:22`), action writes (`RecordTodayAction`, `GetTodayNeedCheck`).

---

## 2. Events Today listens to

Listener mechanics: `page-queries.ts:157-169` maps each event to reads, invalidates them, then dispatches to local handlers. `brief:stored` is **not** coalesced (only `noticed:updated` is, at `:118`).

| Event | Frontend effect | Go emitter (file:line) |
|---|---|---|
| `brief:stored` | Invalidate and re-read `GetLatestBrief` (`useLatestBrief.ts:98-103`); `GetTodayQuietLines`; `useBriefSavedAnswers` re-runs; `followLatest` set when id differs (`:99`) | `mobile_brief_service.go:513, :1356, :1379, :1534, :1653`; `mobile_brief_finished_read.go:992, :1254, :1503`; `mobile_brief_today_refresh.go:111`; `mobile_brief_week_intent.go:89, :300, :339`; `today_closing_answer.go:139`; `topic_finds.go:905`; `app.go:1308, :1353` |
| `brief:building` | Re-read `GetLatestBrief` (`useLatestBrief.ts:105-110`) | `mobile_brief_service.go:536, :736` |
| `today-dayshape:updated` | `GetTodayDayShape`, `GetTodayBeforeYouTalk` | `today_day_shape_updates.go:96` (timer), triggered by `app.go:460-461` (`calendar:synced`, `brief:stored`) |
| `today:done-changed` | `useTodayDone` refresh | `app.go:506` (also on `brief:stored`), `today_review_done.go:355` |
| `today:needs-changed` | `useTodayNeeds` refresh | `app.go:502`, `needs_put_off.go:225` |
| `mobile:updated` | `useTodayStatus` refresh; `GetMobileBriefSchedule` | `mobile_brief_seam.go:110`, `mobile_brief_service.go:735, :1357, :1380, :1654`, `mobile_bridge.go:746`, `mobile_keep_awake.go:325`, `topic_finds.go:906`, `mobile_brief_week_intent.go:88, :299, :338` |
| `topics:updated` | `GetTodayTopics`, `GetTopicFinds`, sidebar | `topics_registry.go:1305, :1903`, `topics_service.go:616, :629`, `topic_finds.go:1213`, `topics_today.go:267, :405`, `interests_service.go:174`, `main_chat_send.go:551`, `main_chat_followup.go:121` |
| `topics:finds` | `GetTopicFinds` | `topic_finds.go:600, :725, :809, :1211`; `topic_finds_snapshot.go:308`; `tasks_api.go:154`; `mobile_brief_feedback.go:327` |
| `calendar:synced` | `GetTodayBeforeYouTalk`, `GetCalendarFirstDay`, `GetCaptureCounts` | `calendar_bindings.go:120`, `calendar_peek.go:556`, `calendar_sync.go:588` |
| `noticed:updated` | `GetDayNoticed` (coalesced 250 ms) | `activity_noticed.go:660` |
| `agency:updated` | `GetGoalCheckIn`, `ListGoals` (and `useGoalCheckIn` directly) | `activity_row_actions.go:356, :417`, `goal_tools.go:118`, `purpose_user_actions.go:531, :559`, `self_directives_api.go:53`, others |
| `today:answer-saved` | `useBriefSavedAnswers` | `today_panel_answers.go:255` |
| Privacy sweep: `memory:updated`, `closed-eyes:changed`, `background-listening:changed`, `permissions:changed`, `sensitive-apps:updated` | Everything on the page is cleared and re-read (`page-queries.ts:84-92`); `useTodayTopics` clears locally (`useTodayTopics.ts:175-185`) | Various |
| `system:state`, `visibilitychange`, `focus`, `pointerdown`, `keydown` | Activity gate (`app-activity.ts:27-58`); cache stale-only re-read on show (`page-queries.ts:150`) | Runtime |

### Loops and amplifiers

- **No true cycle found.** Success notifies subscribers (`query-cache.ts:226`), and the subscriber re-reads from cache, which is fresh, so nothing re-fetches. Go-side, `GetLatestBrief` triggers `refreshTodaySoon` only past the 15 min budget, and a refresh that stores a change emits `brief:stored` (`mobile_brief_today_refresh.go:56-112`). That is bounded by the 5 min gap. Checked, not a loop.
- **Fan-out per `brief:stored` [med].** One event causes: `GetLatestBrief` (1 backend call; the cache is cleared at `query-cache.ts:56`); `GetTodayQuietLines` (1); `GetDoneForYou` (1, via `app.go:506`); `GetTodayDayShape` and `GetTodayBeforeYouTalk` (2, via `app.go:460-461`); and `GetTodayPanelAnswer` x N (`useBriefSavedAnswers.ts:9-24`).
- **Superseding burst [med].** A new `brief:stored` clears the in-flight `GetLatestBrief` and marks it superseded (`query-cache.ts:48-64`, `:210-213`, `:229-232`). Each event therefore reissues the backend call. `sealSources` and `amendSources` emit once per stored brief in a loop (`mobile_brief_finished_read.go:1238-1254`, `:992`).
- **Likely source of 83 reads/s [low].** About 8 stored briefs sealed in one pass times ~10 calls per event would give about 80. This fits a privacy-sweep or launch reseal. Check the log for `sealSources` or `amendSources` runs at that time.

---

## 3. Card states

| Card | States | Exact condition |
|---|---|---|
| Brief (`TodayBriefSection.tsx`) | Loading skeleton | `loading && !view` (`:706`) |
| | Load failed (no view) | `!view && loadFailed`, with Retry (`:720`) |
| | Written brief | `(hasWrittenBrief(brief) \|\| ...) && (hasContent \|\| quiet) && brief` (`:732`); `hasWrittenBrief` = `narrativeStatus==="ok" && briefHasWords` (`:283-285`); `hasContent = !view.empty` (`:695`) |
| | Quiet | `view.quiet` (`:778`) |
| | Pending ("on its way") | `waiting` = `pending` (server `pendingSlot`) or `fromEarlierDay` (`:693`); `pending` needs `view.enabled` (`:684`) |
| | Empty / nothing | `view.empty` with no written words, and not pending (`:695`, `:826`) |
| | Nothing new | `view.nothingNew` (`:856`) |
| Your week | Inside the brief card | `brief.yourWeek`; dropped when its lead fails `safeCopy` (`brief-hygiene.ts:28-30`) |
| Your topics | loading, ready, failed, unavailable, first-run, hidden | `useTodayTopics.ts` status and `hasTopics`/`firstRun`. `unavailable` is set when the error text contains "is not available" (`:87-89`) |
| Before you talk | Card, or nothing | `GetTodayBeforeYouTalk` returns nil in several cases (`today_before_you_talk.go:65-70`) |
| Chat (`MainChatPanel`) | Not traced | |
| Needs you / Done for you | Items, empty, failed | `useTodayNeeds` `failed` with a 30 s retry (`useTodayNeeds.ts:66`); `useTodayDone` `failed` with no retry (`useTodayDone.ts:19`) |
| Status line (`TodayStatus`) | Brief health or null | `status.brief` from `GetMobileBriefHealth` (`TodayPage.tsx:761`); null on any failed read (`useTodayStatus.ts:44-49`) |
| Goal check-in, portrait card | Card or hidden | Set to null on a failed read (`useGoalCheckIn.ts:18-19`; `TodayPortraitCard.tsx:24-25`) |

---

## 4. Paths where stored data exists but Today shows nothing or old data

1. **Stored brief with no written words and no sections is hidden [med].** `view.empty` is true when `mobileBriefIsEmpty` ignores audio (`brief_today.go` ~ `:97-103`). With the brief made today, `hasContent` is false (`TodayBriefSection.tsx:695`), `fromEarlierDay` is false, and `waiting` is null (`:693`). Result: no card, no pending card, though the store has a brief with audio.
2. **Client hygiene drops the words and the audio [med].** `briefForDisplay` removes narrative paragraphs that fail `briefCopySafe` (`brief-hygiene.ts:47-52`) and sets `audio` to undefined whenever anything changed (`:66`). The card test (`:732`) uses the raw brief, so a card renders with an empty narrative and no audio player, instead of the brief.
3. **Server returns a different brief than the one stored [med].** `latestShown` passes over a brief whose words are withheld now (`mobile_brief_current.go`, `visit` closure). `GetLatestBrief` never fills `WordsWithheld` (only `GetTodayBriefByID` does, `brief_today.go:394`), so the UI shows an older brief or a pending card with no reason.
4. **Brief held while policy loads [med].** `sealResult` returns `NotReady` while `briefPolicyPending` is set (`mobile_brief_finished_read.go:1160-1169`, `storage_status.go:56`). The brief stays unsealed until `briefObservationPolicyLoaded`. If that never runs, the brief is hidden for the session.
5. **Launch flag never clears [low].** `storedReads` is cleared only inside the deferred `brief_refresh` task and only when `mobileBriefSource() != nil` (`app.go:1346-1353`, `launch_startup.go:28`). Otherwise `GetLatestBrief` never schedules `refreshTodaySoon` (`brief_today.go:200`), and live signals stay old until an event.
6. **Frontend cache is not keyed by date [med].** `GetLatestBrief` is cached for 15 min with no date key (`page-queries.ts:263`). Nothing re-reads at local midnight. `TodayBriefSection` re-renders each minute (`:588-595`), so `fromEarlierDay` flips to the pending card (`:688-693`) while the Go side already uses the new `readDay()` (`launch_brief.go:19-21`). The old brief stays on screen until an event or a stale show.
7. **Single failed brief read hides nothing, but also never retries [high].** `useLatestBrief.ts:86-90` sets `loadFailed` and keeps the old `view`. Only one quiet retry (`quiet-retry.ts:3-10`), then nothing until an event or show. The error card appears only when there is no view.
8. **Goal check-in and portrait card vanish on one failed read [high].** `useGoalCheckIn.ts:18-19` and `TodayPortraitCard.tsx:24-25` set the card to null. No retry until the next event.
9. **Done for you empties and does not retry [high].** `useTodayDone.ts:19` publishes `items: []`, `failed: true`. Unlike `useTodayNeeds` (`:66`), there is no timer. It recovers only on `today:done-changed`.
10. **Status line clears on any failed read [high].** `useTodayStatus.ts:44-45` maps failures to null, then `setWeek(null)` and `setBrief(null)` (`:48-49`).
11. **Before-you-talk blinks and can vanish [high for blink, med for hidden].** `useTodayBeforeYouTalk.ts:16` sets the card to null before every read, and the 30 s invalidate (`:33`) triggers one every 30 s. A failure sets null (`:21`). Go returns nil silently when muted, Closed Eyes, or calendar permission is off (`today_before_you_talk.go:65-70`).
12. **Topics hidden by a text match [med].** `useTodayTopics.ts:87-89` treats any error containing "is not available" as unavailable and hides all topics. Go clears the list when `!firstRun && len(views)==0` (`topics_registry.go:1815`), which can happen during a rebuild.
13. **Superseded and burst reads [med].** See section 2. Each `brief:stored` clears the value (`page-queries.ts:166`, `query-cache.ts:56`), so a read that is still pending is superseded and reissued. Under a burst the cache never settles, and the page can keep `loading` in the cache without showing a new brief.
14. **Locked or hidden window delays the brief [med].** `GetLatestBrief` waits for `waitForAppActive` through the cache gate (`page-queries.ts:15`, `query-cache.ts:212-213`). While locked, `view` stays null and the skeleton shows (`TodayBriefSection.tsx:706-718`). This is latency, not loss.
15. **`brief:stored` with an empty id drops the open brief [low].** `useLatestBrief.ts:98-99` sets `followLatest` for `""`, so a brief opened from Your week is replaced by the latest on the next refresh.

Checked and not a path: hidden-window skips in `useTodayStatus` (`held` resubscribes on show), `useTodayDayShape` (version guards and midnight timer), `useTodayTopics` (request ids and budgeted resync), and `useTodayNeeds` (request ids).

---

## Next step to confirm

Add a counter in `GetLatestBrief` and `pageQueryCache.read` that logs the binding, the event that caused it, and the caller. Then run the morning replay: the 06:51 brief, with the Today window visible. Compare the reads per second with the number of `brief:stored` events. Check whether `GetLatestBrief` returned a brief with `Brief != nil`, `Empty == true`, or `WordsWithheld != ""` for that brief, and whether `briefForDisplay` removed `audio` on the client.
