# Today and morning brief: failure history, 2026-10-16 to 2026-11-15

Read-only analysis of `claude/today-rework` (origin/feat/iphone-companion). No code changed. Scope is the last 30 days; commit counts use `git log --no-merges` path filters on `wails-gui/frontend/src/components/today`, `main-chat-brief-card.tsx`, `wails-gui/*brief*`, `wails-gui/*today*`.

## Method and caveats

- Each fix commit (subject starts with `fix` or `Revert`) was read by its subject, and the diffs of the commits named below were checked (for example `1702e7b84` adds `mobile_brief_provenance.go`, `8bfec6a4f` changes only `mobile_brief_finished_read.go`, `2b9147970` changes `TodayBriefSection.tsx`). Existing diagnoses in `docs/hub-reviews/` and `docs/backlog-history.md` were used where they name a cause.
- Each commit is assigned to one group. Commits that fit two groups go to the group that owns the state being changed. Every assignment is listed below, so any can be moved.
- Counts are commits, not diffs. A commit that touches two groups is counted once.

## 1. Totals

- Non-merge commits touching Today or brief paths in the 30 days: **862**.
- Fix commits among them: **523** (518 `fix`, 5 `Revert`), touching **2,349** file entries. Fixes landed on 29 distinct days of the 30.
- Top code files by fix commits (window, excluding locale JSON):

| Fix commits | File |
|---:|---|
| 85 | `(locale) wails-gui/frontend/src/i18n/locales/zh-CN/today.json` |
| 82 | `(locale) wails-gui/frontend/src/i18n/locales/en/today.json` |
| 74 | `wails-gui/mobile_brief_service.go` |
| 74 | `wails-gui/frontend/src/components/today/TodayBriefSection.tsx` |
| 72 | `wails-gui/frontend/src/components/today/TodayBriefSection.test.tsx` |
| 62 | `wails-gui/frontend/src/components/today/TodayPage.test.tsx` |
| 48 | `wails-gui/mobile_brief_read_privacy.go` |
| 42 | `wails-gui/frontend/src/components/today/TodayPage.tsx` |
| 32 | `wails-gui/mobile_brief_finished_read.go` |
| 30 | `wails-gui/mobile_brief_narrative.go` |

The 79 figure in the report is the all-time count with rename following: `TodayBriefSection.tsx` and `mobile_brief_service.go` each have 79 fix-subject commits in total (137 and 127 commits). In the 30-day window it is 74 each.

Root-cause groups (commit counts, from the per-commit assignment below):

| Group | Root cause | Commits | Days |
|---|---|---:|---:|
| A | Brief copy lifecycle: the seal, the made-ahead copy, amendments and the audio are separate stores | 40 | 15 |
| B | Read-time source and privacy gating: a brief is decided by re-checking its sources on every read | 95 | 22 |
| C | Failed, empty, retried and refreshing states: no shared state model for Today and the brief cards | 104 | 24 |
| D | Clock, day and slot boundaries: computed in several places with different rules | 44 | 16 |
| E | Generated-copy text guards: the writer's output is checked by text rules after it is written | 63 | 17 |
| F | Calendar hold offers and cards: offers recognised by their text | 38 | 5 |
| G | Delivery surfaces read the brief separately (desktop, phone, MCP, menu bar) | 24 | 9 |
| H | Day strip and card layout: labels, skeletons and chips sized after content loads | 38 | 11 |
| I | Review-round batches: walkthrough findings, mixed causes and test repairs | 21 | 8 |
| J | Retention deletes brief inputs | 8 | 4 |
| K | Other areas that touch the brief but are not brief-specific | 48 | 16 |

## 2. Groups by root cause

Each group states the broken rule, the evidence, how often it recurred, and the one structural fix. The commit list under each group is every fix commit assigned to it.

### A. Brief copy lifecycle: the seal, the made-ahead copy, amendments and the audio are separate stores

**Broken rule.** The written brief, its sealed copy, the made-ahead copy, the recording and the Today player are each written by their own rule, so after any edit or rebuild they can disagree about which words are current.

**Evidence and recurrence.** The mechanism is in place: `mobile_brief_finished_read.go` says a brief is sealed once, and `mobile_brief_today_refresh.go` and `mobile_brief_service.go` still write, amend and re-seal copies from different paths. The 2026-11-14 commits (`8bfec6a4f`, `0910843de`, `f144b2992`, `63e499b79`) each correct one path while the others keep their own rule. Recurrence: 40 commits on 15 days, the sealed-copy paths were changed on 15 separate days, and `f144b2992` is labelled v3 for the audio alone.

**Structural fix.** One owner, `brief_record`, that writes exactly one record per slot with a state machine (draft, sealed, amended(epoch)). Every surface (Today, Chat, the phone package, the audio file) reads that record by revision and never writes it. The audio is a derived file keyed by a hash of the sealed words. Delete the made-ahead path, the refresh-writes-copy path in `mobile_brief_today_refresh.go`, and the amend branches in `mobile_brief_finished_read.go`. Amendments become new revisions, not edits.

<details><summary>40 commits</summary>

- `f68ad96d3` 2026-10-19 fix(wails-gui): Today chat context keeps the item's own facts and survives brief changes
- `04fe453df` 2026-10-20 fix(wails-gui): the brief keeps its written text across reads and rejected sentences
- `7f8c6dd65` 2026-10-21 fix(wails-gui): the spoken brief is attached again and kept while its text stands
- `5a7a0516e` 2026-10-26 fix(today): preserve weekly audio and hold copy
- `da5b43286` 2026-10-27 fix(mobile-brief): keep audio and stored text when an answer paragraph drops
- `60ecae385` 2026-11-02 fix(today): day one keeps current work; remake the legacy brief once (v1.1.11)
- `3321dfc2b` 2026-11-04 fix(brief): deliver a ready brief on the computer, drop only what a withdrawn source touched
- `2a6a9c1f0` 2026-11-04 fix(brief): the Today player follows a goal reopened by Undo
- `891dcc9ba` 2026-11-04 fix(brief): keep the recording with the saved brief before Today's refresh stores it
- `0469da6c3` 2026-11-04 fix(brief): build from a pinned memory snapshot and open the saved finished brief
- `3edab7fe3` 2026-11-05 fix(brief): amend after the action, count Today drops, collect outside patchMu
- `48671d457` 2026-11-05 fix(brief): sealed reads amend on privacy change; test setup uses the real paths
- `7242538aa` 2026-11-05 fix(brief): privacy and handoff removals re-seal before the action returns
- `383d8f9e1` 2026-11-05 fix(brief): no seal before the observation policy loads; writes read the stored brief
- `86efafef2` 2026-11-06 fix(brief): reseal a stale copy from the newer version, not withhold it
- `880e611d9` 2026-11-06 fix(brief): back off a brief whose seal keeps failing
- `c6374feba` 2026-11-06 fix(brief): a copy is written only for the version it was made from
- `d770160ad` 2026-11-06 fix(brief): seals carry their epoch; amendment is tracked and coalesced
- `be657c0ef` 2026-11-07 fix(brief): made-ahead words per v1.0.2 (P157 part C)
- `31089a7a5` 2026-11-10 fix(brief): align audio with the displayed brief
- `44ca9d3c0` 2026-11-10 fix(brief): advance latest shown atomically
- `0fd78bf56` 2026-11-10 fix(brief): preserve unchanged summary sources
- `26e5a3df9` 2026-11-10 fix(brief): resume sealing after retry backoff
- `fe1f638f5` 2026-11-10 fix(brief): compare sealed source fingerprints
- `23b9e7b54` 2026-11-11 fix(brief): amend sealed copies on belief correction, stop test leak
- `7500c7223` 2026-11-11 fix(brief): a made-ahead copy no longer fills the morning slot
- `cd570466d` 2026-11-11 fix(brief): limit migration to recent days, withdraw deleted people, audit test changes
- `fe334f287` 2026-11-11 fix(brief): migrate copies the old rule hid, report v2 decision in brief-replay
- `ad1b9f083` 2026-11-11 fix(brief): restore Today refresh, gate mail syncs on sealed copies
- `55b3ca6b2` 2026-11-12 fix(brief): morning words survive calendar citations and made-ahead days
- `250fec6e6` 2026-11-12 fix(brief): a refresh stores the build's words; the gated copy goes to the seal
- `4998cf12b` 2026-11-12 fix(brief): keep the build's words; refreshes amend copies only
- `c2bb296fb` 2026-11-13 fix(brief): remake the recording only when the sealed words change
- `02c83e3a1` 2026-11-13 fix(brief): the spoken brief follows its words within a minute
- `41ad79030` 2026-11-13 fix(today): set Your week off the written brief, footnote the private-items line, fill the row for few topics
- `836f2099c` 2026-11-13 fix(brief): the real morning build replaces a made-ahead copy
- `8bfec6a4f` 2026-11-14 fix(brief): the slot's real build gets its own seal, not the made-ahead copy's
- `f144b2992` 2026-11-14 fix(brief): record and keep the brief's audio by its sealed words (v3)
- `0910843de` 2026-11-14 fix(brief): keep a recording that speaks the sealed copy, so Today plays it
- `63e499b79` 2026-11-14 fix(brief): a stated exclusion amends the sealed brief and remakes its audio

</details>

### B. Read-time source and privacy gating: a brief is decided by re-checking its sources on every read

**Broken rule.** Whether a brief is shown, held or withdrawn is re-derived from current source stores, privacy policy and provenance each time it is read, so the same unchanged brief can flip between shown and withheld.

**Evidence and recurrence.** `mobile_brief_read_privacy.go` (1,171 lines) and `mobile_brief_provenance.go` (329 new lines on 2026-11-10 in `1702e7b84`) run the withholding check per read. The 2026-10-18 series fixes privacy gaps 1, 4, 5, 8, 10 and 14, most by adding another read-time check. `c2243bbc7` (11-05) waits for a store that is still opening before withholding, which means a startup race is shown to the user as a withheld brief. Recurrence: 95 commits on 22 days; 14 commits mention withholding by name.

**Structural fix.** Gate at write time, not read time. A privacy or source change emits one event; the gate runs once per affected sealed record and writes either an amendment or a withdrawal before the triggering action returns (`7242538a` already does this for removals, so extend that to every source). Reads only load the stored record and never re-check sources. A store that is not ready means 'not sealed yet', never 'withheld'. Delete `mobileBriefForProjectionWithholding` and the per-read provenance checks. Privacy stays absolute: withdrawal is still enforced, just at the write.

<details><summary>95 commits</summary>

- `998bded94` 2026-10-16 fix(wails-gui): resolve automation continuation deliverables
- `dadaee0ce` 2026-10-16 fix(wails-gui): filter Purpose text before brief delivery
- `e6bddbd95` 2026-10-16 fix(wails-gui): recheck deliverable sources and retain meeting intervals
- `fc6dca1a4` 2026-10-16 fix(wails-gui): preserve Today privacy across companion refresh
- `86055fc93` 2026-10-16 fix(wails-gui): cancel Today source reads and keep drafts source-free
- `f86b68197` 2026-10-16 fix(wails-gui): recheck Today provenance and cancel context reads
- `8ec2e2426` 2026-10-16 fix(wails-gui): recheck MCP and phone delivery sources
- `b7415b841` 2026-10-16 fix(wails-gui): gate memory injection and brief recall
- `7a746da2e` 2026-10-16 fix(wails-gui): keep MCP privacy reads retryable and consistent
- `ff1ce2739` 2026-10-16 fix(brief): separate goal identity from displayed evidence
- `255296ea3` 2026-10-16 fix(wails-gui): close filtered read metadata and memo gaps
- `757175223` 2026-10-16 fix(wails-gui): bind activity models and caches to readable sources
- `937934630` 2026-10-16 fix(wails-gui): distinguish brief evidence from purpose identity
- `68f168c6a` 2026-10-17 fix(ideas): fence source publication and history reads
- `23271dbe6` 2026-10-17 fix(ideas): preserve source provenance through chat delivery
- `82ecba4ba` 2026-10-17 fix(wails-gui): revalidate Today sources through history admission
- `9a11caaa2` 2026-10-17 fix(wails-gui): preserve Today draft provenance through undo
- `dbc6c3ffb` 2026-10-17 fix(wails-gui): retain privacy coverage for brief timing
- `8f8466429` 2026-10-17 fix(wails-gui): close brief privacy and projection review findings
- `0e411ee43` 2026-10-17 fix(wails-gui): recheck brief and podcast delivery privacy
- `153a8f48f` 2026-10-17 fix(wails-gui): deliver the Purpose Map and filter Upcoming in the delivery projection
- `be88a44e3` 2026-10-17 fix(wails-gui): protect brief summaries and failed source reads
- `d6a1fb7f2` 2026-10-18 fix(brief): bind model context and reconcile recap actions
- `946a89c77` 2026-10-18 fix(brief): resolve older loop items through chat source gates
- `767486198` 2026-10-18 fix(brief): redact outcome codes and share reading budget
- `b85e5b380` 2026-10-18 fix(engagement): revalidate saved first-week cards and recaps
- `0802ff7be` 2026-10-18 fix(privacy): gap 5 withhold unresolvable deliverable aliases
- `c38ef16ae` 2026-10-18 fix(privacy): gap 10 resolve complete Purpose read dependencies
- `21880cfc6` 2026-10-18 fix(privacy): gaps 8 and 14 withdraw synthesis when dependencies shrink
- `98607c170` 2026-10-18 fix(privacy): gap 10 validate fresh interest evidence closure
- `779cb527a` 2026-10-18 fix(privacy): gap 5 gate private-folder deliverable metadata
- `d2b1350f9` 2026-10-18 fix(privacy): gap 4 redact codes at Today output boundaries
- `d9e9cbfcc` 2026-10-18 fix(privacy): gap 1 gate refreshed Today memory reads
- `2df3ba42b` 2026-10-19 fix(wails-gui): an unproven brief's offer never sends its stored words
- `73ba1a7b9` 2026-10-19 fix(wails-gui): Today reads no sources while observation is paused
- `7472727cb` 2026-10-19 fix(release): audit privacy gates, copy and feedback
- `08973aafd` 2026-10-19 fix(brief): fence source collection before binding
- `99057657d` 2026-10-20 fix(wails-gui): only the user's own words skip the mail hold (#97 self-review)
- `1f49c3475` 2026-10-20 fix(pii): a kept date no longer hides a short code's keyword
- `7d1793135` 2026-10-21 fix(topics): retire withdrawn identities and wire topic chat
- `3636cc5bd` 2026-10-21 fix(agents): narrow scheduled run context (item 60 audit fix 4)
- `c4e91187f` 2026-10-22 fix(cache): gate retained news and refresh protected sources
- `5578a1499` 2026-10-23 fix(brief): keep redaction tokens out of display and speech
- `735cc8d1c` 2026-10-23 fix(ui): use shared privacy masks for topic names
- `c3c2ddf32` 2026-10-23 fix(ui): guard brief and stream topic labels at render time
- `3227416e8` 2026-10-23 fix(topics): withhold masked names and main chat subjects
- `89e3fa42d` 2026-10-23 fix(ui): withhold redacted topics and the chat itself
- `3e682442d` 2026-10-24 fix(menubar): clear shared audio on account withdrawal
- `ed9a9d4da` 2026-10-26 fix: keep chat item panels private during refresh
- `64817d886` 2026-10-26 fix(today): revalidate frozen weekly privacy
- `6ec485884` 2026-10-26 fix(topics): preserve withdrawal rebuild deadlines
- `c0354b45d` 2026-10-26 fix(topics): hide unproven migrated snapshots
- `7fb2b23d6` 2026-10-27 fix(brief): fence collection on privacy changes, not on every write
- `3f31ad188` 2026-10-27 fix(mobile-brief): drop unreadable answer paragraphs
- `8ac63a635` 2026-10-27 fix(mobile-brief): preserve non-answer chat references
- `a46aabc4a` 2026-10-27 fix(open-answers): revalidate brief references at delivery
- `14026ae83` 2026-10-28 fix(first-day): withdraw traced history and clarify calendar status
- `69120c1ca` 2026-10-30 fix(brief): limit historical proofs to recent briefs
- `77855d1d5` 2026-10-30 fix(brief): require ranked input proof for ranked prose
- `74c1522ce` 2026-10-30 fix(brief): freeze ranked model inputs
- `d5c63e800` 2026-10-30 fix(brief): derive history from frozen ranked inputs
- `6e6328b3a` 2026-10-30 fix(brief): retain readable Purpose candidates
- `6fd5de2c7` 2026-10-30 fix(brief): protect stored ranked inputs on read
- `5eafb5241` 2026-10-30 fix(brief): gate tomorrow calendar reads
- `126d593bc` 2026-10-30 fix(brief): recheck activity inputs on delivery
- `c6caeea92` 2026-10-31 fix(brief): filter unrebindable rows at the ranked route, keep gather facts
- `93c0db86f` 2026-10-31 fix(brief): count only rebindable sources as ranked morning candidates
- `3297194e8` 2026-10-31 fix(today): withdraw corrected weekly belief lines
- `5d9498515` 2026-10-31 fix(today): explain attendee note source
- `733655a29` 2026-10-31 fix(today): isolate paused brief read proofs
- `faa164c3e` 2026-10-31 fix(today): bind asks to the selected paragraph
- `bc209cc32` 2026-10-31 fix(today): verify paused brief activity candidates
- `6bef77fa4` 2026-10-31 fix(today): recheck paused brief purpose sources
- `94ef86ea0` 2026-10-31 fix(today): prove paused brief calendar and history
- `2b9ce6a80` 2026-10-31 fix(today): recheck paused brief activity provenance
- `f6fbba36e` 2026-11-02 fix(today): recheck saved brief evidence
- `12102b5e5` 2026-11-05 fix(brief): a closed policy store leaves out web-source items
- `31037a13a` 2026-11-05 fix(brief): copies with a stale privacy print are withheld; goal moves re-seal
- `e6580fa0d` 2026-11-05 fix(brief): pending briefs show identity only; Closed Eyes strips all content
- `c2243bbc7` 2026-11-05 fix(brief): wait briefly for a store that is still opening before withholding on read
- `2571351c1` 2026-11-05 fix(today): name why the current brief is withheld; the panel says so in one line
- `ccda73dc2` 2026-11-05 fix(today): the last-brief panel shows the remaining items when the narrative is withheld
- `281e0572d` 2026-11-06 fix(privacy): saved first page never shows withdrawn cards; memory titles keep their words
- `471d18f62` 2026-11-09 fix(brief): reduce spurious withholding
- `fcbf25e03` 2026-11-10 fix(brief): persist edited-source withholding
- `1702e7b84` 2026-11-10 fix(brief): withhold text after cited source edits
- `1acdd3277` 2026-11-12 fix(brief): only explicit privacy actions withdraw; morning pre-flight
- `c193a65c3` 2026-11-12 fix(brief): a re-summarized source is an edit, not a withdrawal
- `3868b74d7` 2026-11-12 fix(brief): drop a paragraph whole when any cited source cannot be bound
- `cc35ca17f` 2026-11-12 fix(brief): drop unbindable refs instead of the new brief
- `87e85f6e6` 2026-11-12 fix(brief): source edits during a build no longer throw it away
- `fcfbfc0cd` 2026-11-13 fix(chat): a withdrawal removes saved first pages from disk, not only on the next read
- `0214c37e0` 2026-11-13 fix(brief): the agent check reads the apps a record was seen in
- `5cc415630` 2026-11-13 fix(brief): work built in an agent host is the agents', not the user's
- `1109a2536` 2026-11-14 fix(brief): exclusions cover the person, the screen summary and attended meetings

</details>

### C. Failed, empty, retried and refreshing states: no shared state model for Today and the brief cards

**Broken rule.** Today and the Chat cards decide loading, failed, held, empty and no-topic states as separate branches in each component, and each card fetches and retries on its own, so a new edge case adds a branch that can show blank, spin, or loop.

**Evidence and recurrence.** The 'never blank / never empty' rule has been fixed several times on different days: `7165e801f` (10-23), `dbac97699` (11-03), `4a0a2c481` (11-06), `365db7552` (11-02). Bootstrap and cold-start retry loops were stopped three separate times (`b1ac8a18` and `1dbc322f` on 10-24, `a603c526` on 10-25), and `af23e790` reverted a fix that had stopped topic verification altogether. The hub review `claude-dsh-day-shape-2-self.md` shows the cause: the day strip sits inside the brief card, and two independent reads paint at different times, so the placeholder shows first. Recurrence: 104 commits on 24 days, the largest group.

**Structural fix.** One Today view model. A single backend `TodayState` snapshot (discriminated union: loading, ready, held, failed, empty, each with its reserved geometry) is read once and updated by one change event. Components render from that snapshot only; they do not fetch, poll or retry. The retry policy lives in one backend place with one backoff. Delete the per-card reads and bootstrap loops in `TodayBriefSection.tsx` and `TodayPage.tsx`, and the local 'Try again' branches, which become the snapshot's `failed` state.

<details><summary>104 commits</summary>

- `1c907c388` 2026-10-16 fix(wails-gui): retain failed Today drafts and localize context errors
- `ada244aa6` 2026-10-16 fix(wails-gui): release the feed listener before shutdown
- `6c22c03af` 2026-10-16 fix(wails-gui): refresh stored Today signals between briefs
- `21a7d6a01` 2026-10-17 fix(wails-gui): include retry cooldown in brief predictions
- `8f938f18b` 2026-10-19 fix(brief): recompute the shape of today on a Today refresh
- `eda9fc597` 2026-10-21 fix(today): show the topics the registry sends, as #148 shapes them
- `2b9147970` 2026-10-21 fix(wails-gui): Talk about this brief opens the conversation again, as a button
- `78a92f33d` 2026-10-22 fix(today): serve topics while the cold registry builds
- `c8ea4a2fa` 2026-10-22 fix(frontend): preserve refresh behavior in retained pages
- `d18242320` 2026-10-22 fix(brief): keep Talk keys stable and reuse source reads
- `1b4a247a2` 2026-10-23 fix(today): coalesce refreshes and preserve topic cards
- `a02997dd8` 2026-10-23 fix(topics): close publication, chat retry, and backfill gaps
- `e8094c00d` 2026-10-23 fix(today): publish full views and coalesce background reads
- `d3ce71aa1` 2026-10-23 fix(today): preserve saved identities in cold topics
- `58cc41091` 2026-10-23 fix(today): verify historical captures and preserve topic state
- `7165e801f` 2026-10-23 fix(today): never show the empty state while topics exist
- `b1ac8a182` 2026-10-24 fix(today): stop bootstrap refresh loop
- `1dbc322ff` 2026-10-24 fix(today): bound cold proof retries
- `b319ed413` 2026-10-24 fix(today): preserve topics through cold proof retries
- `fbe902e01` 2026-10-25 fix(today): render the day strip at once
- `0e96a06b3` 2026-10-25 fix(desktop): keep topic chat available across refreshes
- `4be076c4f` 2026-10-25 fix(today): keep day shape preloaded and stable
- `bdc521d59` 2026-10-25 fix(today): retain cold topics and index activity reads
- `a603c5262` 2026-10-25 fix(today): stop exhausted bootstrap retry loop
- `3534239b6` 2026-10-26 fix(today): open source Your Week brief
- `1a6fb3b7e` 2026-10-26 fix(today): release superseded brief openings
- `ae716aa7f` 2026-10-26 fix(today): guard brief chat selection and sends
- `9f121fc1e` 2026-10-26 fix(today): open brief conversation without blocking navigation
- `1054d06b9` 2026-10-26 fix(today): harden brief paragraph actions
- `b6dd898b8` 2026-10-27 fix(brief): make scheduled briefs reliable and Today honest about them
- `61e762ab1` 2026-10-27 fix(chat): Read your week opens the week's brief
- `23800a68e` 2026-10-28 fix(mobile): Today loops need a lifecycle context; pause test waits for its event
- `6f1b33b8c` 2026-10-30 fix: recover Spotlight submissions and streams
- `698937fca` 2026-10-30 fix: retain failed chat sends for retry
- `7e6c43ab1` 2026-10-30 fix: retain chat decisions and queued messages
- `e2571e97b` 2026-10-31 fix(chat): Read it opens the week's brief on Today
- `f6979ca23` 2026-10-31 fix(chat): name the recorded missed-brief reason (asleep, closed)
- `6bf4f6b46` 2026-10-31 fix(chat): a refused or unanswered draft keeps only Send and Edit
- `32d400019` 2026-10-31 fix(today): retain multiple resolved receipts
- `5339e710c` 2026-10-31 fix(today): cancel stale work on topic navigation
- `018274118` 2026-10-31 fix(today): cancel stale waiting-item checks
- `c98c53b38` 2026-10-31 fix(today): keep the chosen task panel current
- `abae9cf30` 2026-10-31 fix(today): bound reply receipt checks
- `57a968520` 2026-10-31 fix(today): retry unavailable panel items in place
- `3f8ce39e4` 2026-10-31 fix(today): keep report failures in the panel
- `ddad0964e` 2026-10-31 fix(holds): replace repeated failure messages in place
- `17512f917` 2026-10-31 fix(today): preserve stable refresh rendering
- `692d9295b` 2026-10-31 fix(today): show chat opening failures in place
- `494c6a840` 2026-10-31 fix(today): keep goal action receipts in their rows
- `4f61ba81a` 2026-10-31 fix(today): surface failed topic refreshes
- `d62350333` 2026-10-31 fix(today): show calendar refresh failures in place
- `a9e55b264` 2026-10-31 fix(holds): defer failure reports until shared flow
- `8223535ee` 2026-10-31 fix(today): keep weekly controls and failures in place
- `00f5ccb01` 2026-10-31 fix(today): persist weekly recommendation dismissal
- `ebff5d279` 2026-10-31 fix(today): serialize scheduled action undo
- `0b5cbd8b7` 2026-10-31 fix(today): show portrait dismissal failures in place
- `48bd5cf02` 2026-10-31 fix(today): retry a brief read quietly before showing failure
- `471e1480a` 2026-10-31 fix(today): track missed brief completion by request
- `974089afb` 2026-10-31 fix(today): wait for pending answer saves
- `2c62c4aef` 2026-10-31 fix(today): honor the recorded completion author
- `ed4b96528` 2026-10-31 fix(today): keep deferred tasks and navigation in sync
- `40d2c21df` 2026-11-01 fix(brief): the morning brief survives an unreadable score reply
- `62efe67f6` 2026-11-02 fix(tasks): restore retry guard and retain finished history
- `7a73a1e58` 2026-11-02 fix(today): restore scheduled brief pending state
- `e9e3ca666` 2026-11-02 fix(today): retain quiet state without a published brief
- `365db7552` 2026-11-02 fix(chat): keep brief cards visible while loading
- `cf8b3712b` 2026-11-02 fix(tasks): allow late brief retries after failures
- `a3a2d0e39` 2026-11-02 fix(today): recover brief and save week intent
- `dbac97699` 2026-11-03 fix(today): never blank Today, selected row follows the page (D8, D9)
- `418825b63` 2026-11-03 fix(today): the tries line with its cause on Today, the Scheduled row and the panel; Read it opens at once ...
- `a55b866a4` 2026-11-03 fix(today): a brief that can't open says so in copper with Try again (today-review v1.1.13 D4)
- `7a54ebdf6` 2026-11-03 fix(tasks): keep retrying a failed brief slot; the loop guard is for task runs
- `af58899f1` 2026-11-04 fix(brief): keep the last written brief on Today
- `687d98004` 2026-11-04 fix(brief): wait for assistant sign-in before retrying
- `e58956211` 2026-11-04 fix(brief): retain the known assistant on failed runs
- `4084df0a0` 2026-11-04 fix(brief): wait for work during shutdown
- `b0393f569` 2026-11-04 fix(today): align retry copy and screenshot fixture
- `4cce2b974` 2026-11-04 fix(brief): deliver scheduled briefs when judging fails
- `ceac0bb26` 2026-11-04 fix(today): align brief retry and waiting states
- `df85bc658` 2026-11-04 fix(today): saved brief opens at once in Chat, started-event question and stale didn't-run line go, one cap...
- `10a06febc` 2026-11-05 fix(today): a cancelled Stop returns while the database is busy
- `f2e3527c4` 2026-11-05 fix(chat): open on the newest item only when its page is shown
- `7b1dcdd46` 2026-11-05 fix(chat): open on the newest item and count only the reader's scroll
- `60c2ba3f4` 2026-11-05 fix(activity): a cancelled task-onset lookup returns while the read pool is busy
- `ba26dbdfc` 2026-11-05 fix(brief): fire handoff and brief-store hooks after their locks are released
- `fb8f34694` 2026-11-05 fix(brief): announce a brief stored before the Engagement service started
- `c0561a339` 2026-11-05 fix: speed waiting resync and explain brief judge failures
- `96f2fe96f` 2026-11-06 fix(brief): Read it only with more than the card shows; one summary log line per brief (R74 v1.0.0)
- `1cf0f739d` 2026-11-06 fix(chat): a loaded brief with no words and no reason shows no failure line (R74)
- `a7b337942` 2026-11-06 fix(chat): Read it on a recap counts the rows the Your week view would draw
- `62af10e7b` 2026-11-06 fix(chat): a failed read says so with Try again; Read it shows for a recap with rows
- `80b42eecb` 2026-11-06 fix(chat): a failed brief read recovers on the store's event; Read it follows the words
- `e555396f0` 2026-11-06 fix(brief): a held card waits for the store; it says it can't be read only after
- `4a0a2c481` 2026-11-06 fix(brief): the card never empty; the repair keeps the card with text (R40)
- `a5c4c64f7` 2026-11-06 fix(brief): one Chat card per brief, repaired once (R40)
- `70a101384` 2026-11-06 fix(brief): give every held-back brief its Chat card, once (R36)
- `c9ac2c631` 2026-11-06 fix(brief): read Today's brief when it changes, not on every source event (R24)
- `df1c6cba7` 2026-11-07 fix(chat): Send again only on the latest signed-out row; wrapped line has no dangling separator; Sign in us...
- `42afbbb7a` 2026-11-10 fix(brief): cache unchanged read decisions
- `7260b09e5` 2026-11-10 fix(brief): reuse latest candidate read
- `b5893d3af` 2026-11-11 fix(brief): cap no-words builds, keep words on removed events, window skips failed builds
- `e72a450a0` 2026-11-13 fix(brief): a mail sync's Today refresh is tracked before the event returns
- `2af78d8bf` 2026-11-14 fix(chat): a withheld brief's card lead settles instead of looping
- `5b5d1973a` 2026-11-14 fix(chat): a desktop message never sits unanswered without a word

</details>

### D. Clock, day and slot boundaries: computed in several places with different rules

**Broken rule.** The brief slot, the 'evening' or 'tomorrow' word, the day a meeting belongs to, the 24-hour or 12-hour clock and the spoken form are each computed by a separate function in the writer, the card, the chips, the strip and the speech path.

**Evidence and recurrence.** `briefSlotKey` names a slotless brief by hour, and 'hour < 7' means evening, so before the first brief the heading read 'Evening brief' (hub review `brief-reliability-self.md`). `db60ce57` and `981503101` (10-23) add a shared clock for generated prose and chips after the fact, and `cd05b0a9` and `565922553` repair chip labels it broke. `9e94f637` and `0bc7d0b0` (11-04) repair zh-CN clocks again. Recurrence: 44 commits on 16 days.

**Structural fix.** One `briefclock` module (Go, with a TypeScript mirror generated from it) that owns local day, slot key, time formatting per locale and the day word. The writer receives pre-formatted tokens; cards and speech render those strings and never parse a time. Delete `briefSlotKey`'s hour heuristic, the separate chip and strip date formatters, and any `hour < N` rule outside that module.

<details><summary>44 commits</summary>

- `db0d230f5` 2026-10-16 fix(wails-gui): keep Today timely and handoff actions visible
- `8db258c42` 2026-10-17 fix(wails-gui): preserve evening calendar context in Today drafts
- `444731059` 2026-10-17 fix(wails-gui): project meetings across brief days
- `0c73757f4` 2026-10-18 fix(brief): follow the latest cross-day thread state
- `54881729b` 2026-10-18 fix(brief): normalize weekday dates with commas
- `d1d0501c1` 2026-10-19 fix(wails-gui): the shape of today keeps only time still ahead
- `88ac45f12` 2026-10-23 fix(activity): allocate overnight time from source intervals
- `4614ff1a4` 2026-10-23 fix(today): bound strip dates and validate hold clock pairs
- `469e03ced` 2026-10-23 fix(today): honor derived strip updates and rejected dates
- `fc72e0a01` 2026-10-23 fix(today): preview tomorrow for evening briefs
- `db60ce572` 2026-10-23 fix(brief): normalize clocks across generated display fields
- `565922553` 2026-10-23 fix(brief): shorten retained chips after clock normalization
- `cd05b0a9a` 2026-10-23 fix(brief): preserve valid clock tokens in chip labels
- `981503101` 2026-10-23 fix(brief): use the shared clock in generated prose and chips
- `fcc17f856` 2026-10-23 fix(brief): refresh schedule readers after a quiet slot
- `c580ae12e` 2026-10-26 fix(mobile-brief): enforce configured slot timing
- `b37e5e0e9` 2026-10-30 fix(brief): bind topic hours to their local day
- `29a55e578` 2026-10-31 fix(today): preserve each result completion time
- `660301cf7` 2026-11-01 fix(today): keep today's current work read after the slot's time
- `04b7b6ea8` 2026-11-02 fix(chat): date old pauses, keep chat-question replies off the week, shorten more dictated commitments
- `e4dbbd2e3` 2026-11-02 fix(tasks): correct legacy evening detail projection
- `a1e6c3c40` 2026-11-03 fix(tasks,today): The last three with its rows, a time on every brief row, one next time, day words on Last...
- `0cca27f7a` 2026-11-03 fix(chat): past brief lines name their own day, time and cause (H9); capture sleep uses the system sleep si...
- `84dfe2960` 2026-11-03 fix(chat): Thursday polish: short commitment names, past day words, one fold level
- `d67cba6a5` 2026-11-04 fix(today): keep the next brief in the subline
- `b5fdba29a` 2026-11-04 fix(today): a brief in full only on its own day, no now line outside the strip (today-review v1.1.23 R52, R53)
- `7cbe7e34a` 2026-11-04 fix(today): a missed slot says what comes next and gives the title to the next slot (today-review v1.1.22 R51)
- `e3c13626d` 2026-11-04 fix(today): the title and strip follow the slot once a brief gives way (today-review v1.1.22 R51, R21)
- `f8ebc1cce` 2026-11-04 fix(today): retries give way to the next slot (today-review v1.1.21 R49)
- `0bc7d0b06` 2026-11-04 fix(brief): Chinese speech keeps the spoken period while written clocks are 24-hour (today-review v1.1.20 R47)
- `9e94f6379` 2026-11-04 fix(today): zh-CN 24-hour times, the strip shows the brief's own day (today-review v1.1.20 R44 R47)
- `3df1c8696` 2026-11-04 fix(today): a strip for another day says which day (today-review v1.1.17 R21)
- `ded038a33` 2026-11-04 fix(today): brief slot follows the time, sentence around a private item, brief card title, skip after a rec...
- `46e57c5b6` 2026-11-05 fix(brief): card facts digest leaves out the read-time 'since' time
- `1bf5e206e` 2026-11-05 fix: correct people count and chat brief slot
- `76e92b3ae` 2026-11-06 fix(brief): a brief made after its slot covers that slot (R55 regression)
- `2b0c70f9b` 2026-11-06 fix(brief): an early brief is not missed; Chat shows the made-at time (R55, R64)
- `133e9775d` 2026-11-06 fix(brief): one time for the next brief and the shown brief, its ready time (R64)
- `00854a0b4` 2026-11-06 fix(brief): the screen keeps clock digits, the voice says them as words (R68)
- `2ce71bf5f` 2026-11-06 fix(today): an older moment keeps its card, with its date and no day count
- `2e4c35978` 2026-11-13 fix(today): the written brief shows on week days, above Your week
- `705fd9d3e` 2026-11-14 fix(brief): a day word follows the cited item's date only when it is that day
- `25a5fbfad` 2026-11-14 fix(brief): replay rules for the writer; next year's dates and agent credit
- `aabd87028` 2026-11-14 fix(brief): the writer names the reader's days, and no next step carries a past date

</details>

### E. Generated-copy text guards: the writer's output is checked by text rules after it is written

**Broken rule.** The model writes the brief first, and a growing stack of regex and text checks (template text, unfinished copy, over-long text, names, speech, language) decide afterwards what is shown, dropped or spoken, so each fix adds one more rule and one more exception.

**Evidence and recurrence.** `cef393591`, `c9831b7c`, `c698af73`, `8e92462c`, `84d5e03d`, `41f9f03d` and `0b40c776` each add or tune a check on model text. `8e31b61b` (10-23) has to keep privacy masks alive inside the copy-hygiene guard, and `3c694bda` has to keep work names alive inside the voice check. Several fixes change the guard instead of the writer. Recurrence: 63 commits on 17 days.

**Structural fix.** Constrain the writer with a typed output schema (lead, paragraphs with cited refs, pre-rendered tokens, a fixed set of allowed phrases for the template case) so unsafe text cannot be expressed, then run one validator with a golden-file test set. Delete per-copy regexes in the brief writer and the voice path, and drop the post-hoc 'template text' detector once the schema forbids free text where a template is required.

<details><summary>63 commits</summary>

- `114be46d7` 2026-10-17 fix(wails-gui): localize Today schedules and unblock brief reads
- `e71fbbe8d` 2026-10-18 fix(brief): preserve exact speech and source numbers
- `3b1e03dee` 2026-10-18 fix(today): clarify brief sections and preserve file names
- `b99272395` 2026-10-19 fix(brief): Today refresh reads in the UI language; narrower zh typing check
- `218634983` 2026-10-19 fix(brief): round 1 nits on typing narration, prompt words and docs
- `1a4f0b04f` 2026-10-19 fix(brief): noun titles stay nouns in spoken text
- `475a2723e` 2026-10-19 fix(brief): say titles plainly, offer concrete steps, review fixes
- `ec8b0b92b` 2026-10-20 fix(topics): every topic says why again, duplicates merge, short to read (item 61)
- `34d372744` 2026-10-20 fix(wails-gui): one pick is said, the opener follows the picks, About-you notes bind
- `fa3f2789f` 2026-10-21 fix(brief): Closed Eyes never withdraws a template brief as model text
- `7cf9d57ce` 2026-10-21 fix(wails-gui): Today so far renders the day summary's Markdown
- `3a2465746` 2026-10-22 fix(brief): restore question labels with stored model requests
- `a6f64d3f8` 2026-10-22 fix(today): shorten brief chips and label missing recordings
- `c13393838` 2026-10-22 fix(brief): bind closing answers and prepare paragraph chips
- `e75d5e1f7` 2026-10-22 fix(today): join quiet-line stretches with the language's separator
- `1c67c3675` 2026-10-23 fix(brief): preserve shape prose and withhold masked questions
- `bc9595efb` 2026-10-23 fix(brief): preserve short legacy chip labels on reads
- `10ebc112d` 2026-10-23 fix(brief): preserve requested readings and real bracketed copy
- `295f08a4c` 2026-10-23 fix(today): preserve quiet-slot source rows and filter cached copy
- `8e31b61b4` 2026-10-23 fix(brief): preserve privacy masks in copy hygiene guards
- `4c0e12a93` 2026-10-23 fix(today): render localized quiet slots and guard cached brief copy
- `378823ab4` 2026-10-23 fix(brief): keep short readings quiet and reject unfinished copy
- `11f711d51` 2026-10-23 fix(ui): retain specific sentences on proactive cards
- `ae59a2ee3` 2026-10-26 fix(i18n): unify task actions and add bilingual Tasks copy
- `5672f70c8` 2026-10-26 fix(today): preserve linked item cards through hygiene
- `e5ac8f9eb` 2026-10-26 fix(today): prefer typed chip subjects
- `1bed2a52b` 2026-10-27 fix(today): normalize one-to-one title email tokens
- `b3c3fbeac` 2026-10-30 fix(brief): verify closing answers and confidence
- `8e92462c4` 2026-10-30 fix(brief): defer and constrain fallback grounding
- `41f9f03d9` 2026-10-30 fix(brief): judge the prepared draft chips
- `84d5e03d4` 2026-10-30 fix(brief): reject overlong unverified drafts
- `c4d54541c` 2026-10-30 fix(brief): distinguish unknown ranking attributes
- `cdc1c72d1` 2026-10-30 fix(brief): distinguish unseen prior items
- `0b40c776d` 2026-10-30 fix(brief): validate judge inputs and verdicts
- `0f60cf2f6` 2026-10-30 fix(brief): bound lead-only fallback text
- `0f7f85d40` 2026-10-30 fix(brief): preserve unknown candidate attributes
- `e42f16735` 2026-10-30 fix(brief): enforce lead precedence on scored picks
- `0979948cf` 2026-10-30 fix(brief): retry unverified drafts once
- `08eaeacb1` 2026-10-31 fix(today): scope topic panel link copy
- `95e426c71` 2026-10-31 fix(today): align topic and brief copy with the review
- `18c1b1539` 2026-10-31 fix(today): use the dismissal failure copy
- `2d31bde96` 2026-10-31 fix(today): preserve translated review sentences
- `82ad88642` 2026-10-31 fix(today): match weekly answer copy
- `378850982` 2026-11-02 fix(brief): preserve commitment names in sentences
- `25c52a6f1` 2026-11-04 fix(today): the title names the brief on screen (today-review v1.1.24 R54)
- `76fc9f0ee` 2026-11-04 fix(today): tries line once in the card, private rewrite in the brief's language (today-review v1.1.20 R45 ...
- `969c2b355` 2026-11-04 fix(today): one question about one thing; the Chat chip says what it counts (today-review v1.1.17 D18, D19)
- `b136d32a7` 2026-11-04 fix(brief): write the brief in plain order when the scorer picks no lead
- `c698af737` 2026-11-06 fix(brief): picks reach the writer; quiet opening sentence and trimmed model text (R74)
- `96d41f10a` 2026-11-06 fix(brief): preserve actions in own-voice rewrites
- `b56915c71` 2026-11-06 fix(brief): preserve legacy card digests
- `c9831b7cf` 2026-11-06 fix(brief): a template is never spoken; over-long model text is silent (R74)
- `32dee6b4d` 2026-11-06 fix(brief): every brief opens with a sentence from its own counts; picks reach the writer (R74)
- `3c694bda7` 2026-11-06 fix(brief): preserve work names in voice checks
- `8c194287f` 2026-11-06 fix(brief): log the shape of a rejected morning scores reply
- `1507e757b` 2026-11-07 fix(today): unnamed proven reply reads "you replied" per design
- `776ebd5cc` 2026-11-07 fix(today): a proven reply with no name still resolves the need
- `89667fba1` 2026-11-07 fix(r75): match all signed-out lines to the chosen assistant's name (Today, menu bar, Tasks); tests follow
- `53011a543` 2026-11-07 fix(r75): review word changes: Settings "Signed out", phone zh-CN line, chosen assistant's name on the sign...
- `e5a06971c` 2026-11-07 fix(brief): the writer's log names the model call's error class, never its text (R74)
- `cef393591` 2026-11-09 fix(brief): report visible template text in read logs
- `47ff3803e` 2026-11-12 fix(brief): log which check refused a draft
- `f12db7cc5` 2026-11-12 fix(brief): part balance is a goal, not a gate

</details>

### F. Calendar hold offers and cards: offers recognised by their text

**Broken rule.** A hold offer is recognised by matching its visible text (including localized text) and the hold card decides its own visibility, so the brief, the strip and the panel each apply a different rule to the same offer.

**Evidence and recurrence.** `5a9d20e4` recognises localized hold offers, `d4c364d6` narrows detection to calendar time, `e8b9cbc1` hides untagged cached offers, `de67e743` withholds offers in the brief, and `3a6de01b` hides legacy previews. Most of this happened in one sprint: 38 commits on 5 days (10-23 to 11-02).

**Structural fix.** A typed hold-offer record (kind, state, calendar id, event id, source revision) created at offer time by one `HoldService`. The strip, the panel and the brief render from that record. Delete text-pattern detection, the legacy-preview path and the cached untagged offers; a missing record is a bug, not a hidden card.

<details><summary>38 commits</summary>

- `d69acba4c` 2026-10-23 fix(brief): validate hold range joiners case-insensitively
- `f8c1da7b1` 2026-10-23 fix(today): clear private calendar previews and preserve brief actions
- `90f09d20f` 2026-10-23 fix(today): revalidate holds and coalesce day strip updates
- `13d6c771a` 2026-10-23 fix(brief): preserve prompt guards with hold metadata
- `95a72618b` 2026-10-26 fix(tasks): preserve offer privacy through desktop drafts
- `8846339b4` 2026-10-30 fix(today): hide holds and retire composer guidance
- `d4867950b` 2026-10-30 fix(holds): retire strip controls when a hold starts
- `20e6f3f59` 2026-10-30 fix(holds): pass available work estimates to proposals
- `c94fc3015` 2026-10-30 fix(holds): withdraw weekly offers with their source
- `bca8a18bd` 2026-10-30 fix(holds): refresh offers when settings change
- `eee1e16a9` 2026-10-30 fix(holds): preserve other commitment actions
- `3f23ad03b` 2026-10-30 fix(holds): show the strip for later alternatives
- `418a1242c` 2026-10-30 fix(holds): add removal recovery controls
- `63f41c913` 2026-10-30 fix(holds): retry only the confirmed time
- `96189d05a` 2026-10-30 fix(holds): route current commitment keys to find time
- `0451a221f` 2026-10-30 fix(holds): narrow panel state for type checking
- `4d999e5bf` 2026-10-30 fix(holds): show moved event on its day
- `00f2aaf24` 2026-10-30 fix(holds): restore active holds when reopening
- `f91a3dac7` 2026-10-30 fix(holds): explain revoked calendar access
- `fba883318` 2026-10-30 fix(holds): keep alternatives and retry available
- `bc232bf96` 2026-10-30 fix(holds): hide hold card without calendar access
- `e8b9cbc15` 2026-10-30 fix(holds): hide untagged cached brief offers
- `2255503c3` 2026-10-30 fix(today): answer brief find-time chips in place
- `5a9d20e41` 2026-10-30 fix(holds): recognize localized hold offers
- `3a6de01b4` 2026-10-30 fix(today): hide legacy hold previews
- `d4c364d66` 2026-10-30 fix(holds): narrow offer detection to calendar time
- `581c835f4` 2026-10-30 fix(holds): hide up next hold offers
- `de67e743e` 2026-10-30 fix(holds): withhold brief hold offers
- `c65ce775b` 2026-10-30 fix(holds): suppress weekly hold previews
- `41d0170dd` 2026-10-31 fix(today): preserve a closing answer during hold checks
- `8fd4a3bde` 2026-10-31 fix(holds): offer another calendar after write refusal
- `349475415` 2026-10-31 fix(holds): answer find time when web coverage is incomplete
- `97fb7125e` 2026-10-31 fix(holds): leave edited calendar events untouched
- `0fc88427d` 2026-10-31 fix(holds): keep calendar selection retryable
- `a062b2b92` 2026-10-31 fix(holds): choose an available calendar without a default
- `c740a4619` 2026-10-31 fix(holds): protect calendar edits during removal
- `59381efc0` 2026-10-31 fix(today): guard stale asks and hold audio
- `31407abc3` 2026-11-02 fix(today): recognize account holiday calendars

</details>

### G. Delivery surfaces read the brief separately (desktop, phone, MCP, menu bar)

**Broken rule.** The phone package, the MCP read, the menu bar and the desktop each read and assemble the brief through their own path, so one sealed brief can show a different state, time or audio on each surface.

**Evidence and recurrence.** `d60529bec` (11-13) makes the phone read the sealed brief 'like the computer', which implies the phone had its own reader before. `02b03602` and `77b99bf6` key the phone package to the copy's privacy state, `14d0c70c` moves the MCP read onto the brief reader, and `dc44d560`, `bf696b03` and `4259222b` fix phone labels and defaults. Recurrence: 24 commits on 9 days.

**Structural fix.** One `BriefReader` API that returns the sealed record and its revision. Every surface subscribes to revision changes and renders from that record; the phone package becomes a pure function of the revision. Delete the phone's own build path in `mobile_brief_service.go` and `mobile_brief_read_privacy.go`, and the MCP side path.

<details><summary>24 commits</summary>

- `645222004` 2026-10-16 fix(wails-gui): reconnect before delivering wake briefs
- `c332aeb7e` 2026-10-16 fix(wails-gui): preserve relay readiness and recover dropped wakes
- `664089a81` 2026-10-16 fix(wails-gui): keep phones that hold newer scopes, and answer removed memories
- `73f72dbbe` 2026-10-16 fix(wails-gui): recover phone briefs and relay connections
- `dacae07a4` 2026-10-17 fix(wails-gui): retain phone corrections and bound memory facts
- `ccf935bbb` 2026-10-26 fix(today): align phone weekly delivery
- `09cdf0b89` 2026-10-26 fix(mobile): keep the default relay out of relay.json
- `dc44d5608` 2026-10-27 fix(mobile): label missed brief slots honestly
- `185646663` 2026-10-27 fix(mobile): wake phone queues after reconnect
- `b8d5a1ea8` 2026-10-27 fix(mobile): track brief revisions and refresh receipts
- `92c47b028` 2026-10-27 fix(menubar): Make my brief now always answers in the app
- `bf696b030` 2026-10-28 fix(desktop): the Late line says computer, and the week's Chat card plays only with words
- `4259222b0` 2026-10-28 fix(phone): brief schedule without a config store returns defaults
- `d656f5bd2` 2026-10-28 fix(phone): never say the Mac is asleep when it is only slow; brief body retries
- `b86a41d41` 2026-10-29 fix(ios): a refused chip question is Not sent, and Today plays audio still on the computer
- `7d2536aa2` 2026-10-31 fix(today): share week declines across phone and desktop
- `56b9b8552` 2026-11-06 fix(phone): Talk about this brief says why the conversation will not open
- `a8024e2e7` 2026-11-06 fix(phone): Talk about this brief asks the computer for the brief's conversation
- `0fdf195b7` 2026-11-06 fix(brief): push a scheduled brief to the phone in the first week (ST86)
- `02b036026` 2026-11-13 fix(brief): the phone package follows a copy sealed again with less in it
- `55e80ccb1` 2026-11-13 fix(mobile): attachments reach the direct path too
- `77b99bf65` 2026-11-13 fix(brief): phone package keys to the copy's privacy state; Closed Eyes keeps the live path
- `14d0c70cc` 2026-11-13 fix(brief): the MCP brief read goes through the brief reader
- `d60529bec` 2026-11-13 fix(brief): the phone reads the sealed brief and week, like the computer

</details>

### H. Day strip and card layout: labels, skeletons and chips sized after content loads

**Broken rule.** The day strip and brief cards size and place labels, skeletons and chips after their data arrives, so placeholders shift, labels overlap or clip, and each held, failed or loaded state reserves a different height.

**Evidence and recurrence.** Overlap and clip fixes on 10-23, 10-26 and 11-06 (`8772ae96`, `4241534c`, `1a39308f`, `5a44cc48`, `5ec9b2e9`, `56db1a13`, `af8c0082`, `0f7861f7`, `a10c7e23`, `c4fbcc25`, `f1ef34fe`). `claude-dsh-day-shape-2-self.md` documents the 82 px strip placeholder that the brief card paints over. `30667bd1` (11-06) reserves space for held and failed cards and makes Read it and Hear it wait for the words.

**Structural fix.** A fixed-geometry strip and card frame: one layout function owns label fitting (measure once, one fit rule, one truncation rule), and every card state (loading, held, failed, ready) reserves the same height from the same token. Delete the per-label clip and outline CSS in `TodayDayStrip.tsx`, `HoldCard.tsx` and `main-chat-brief-card.tsx`.

<details><summary>38 commits</summary>

- `96c424d12` 2026-10-21 fix(today): quiet actions are the design-system Button (#142 round 1)
- `d23c181db` 2026-10-22 fix(ui): preserve full titles and shared chip button rules
- `b747f908b` 2026-10-22 fix(ui): preserve chip styling and localize chat summary
- `2aee7bf8f` 2026-10-22 fix(chat): polish main chat and Today details
- `70fdc8789` 2026-10-22 fix(today): the compact row runs only an allowlist of actions
- `79bd512a2` 2026-10-23 fix(today): wrap topic text and quiet detail refreshes
- `1a39308fc` 2026-10-23 fix(today): composite ended event labels with their backgrounds
- `4241534c1` 2026-10-23 fix(today): keep one label across partial overlaps
- `8772ae964` 2026-10-23 fix(today): day strip overlaps and evening rules per the handoff
- `5da2eb0da` 2026-10-23 fix(ui): restore streaming notices and anchor jumps to panel composers
- `7a1598616` 2026-10-23 fix(ui): contain narrow composer controls and align topic breakpoint
- `45dd7ba29` 2026-10-23 fix(ui): test production layout and complete Patina roles
- `89c766771` 2026-10-23 fix(ui): correct Patina work roles and topic grid
- `43f518c32` 2026-10-23 fix(ui): center Chat and move scope controls below the text
- `5a44cc485` 2026-10-26 fix(today): clip day-strip chip text on the right only
- `5ec9b2e91` 2026-10-26 fix(today): name and fit day-strip chips
- `3337f70b8` 2026-10-31 fix(today): correct overflow and review copy
- `462344af7` 2026-10-31 fix(today): keep draft stop in card header
- `8fee29691` 2026-10-31 fix(today): answer your week chips in the panel
- `ed7ad9e9b` 2026-10-31 fix(today): keep panel questions answer only
- `18a9878bb` 2026-11-01 fix(today): keep the Read it link on accent
- `22fb43d42` 2026-11-02 fix(chat): refine brief cards and stream labels
- `5484bd628` 2026-11-02 fix(chat): restore answer chips for questions
- `083224ee2` 2026-11-03 fix(chat): keep panel fold-line Show and one line per ended pause
- `a7f2a6e85` 2026-11-04 fix(today): a strip block shows the first name that fits; a private item says only Private (today-review v1...
- `4e5ff814a` 2026-11-05 fix(today): the finds skeleton is the saved row's height
- `202d997fb` 2026-11-05 fix(chat): a brief card draws whole with its first words
- `ed54b7f81` 2026-11-05 fix(chat): a brief card draws at its final size
- `c2eb11b90` 2026-11-06 fix(today): held holds are a violet outline; hour labels sit on the step (R70, R69)
- `56db1a13d` 2026-11-06 fix(today): strip labels follow the review; hour labels never touch (R30, R69)
- `30667bd1a` 2026-11-06 fix(brief): held and failed cards reserve no space; Read it and Hear it wait for the words
- `af8c00826` 2026-11-06 fix(today): day strip blocks are outlined, named only in whole words (R30)
- `f1ef34fe3` 2026-11-06 fix(today): an untitled block is outlined, never solid black
- `0f7861f72` 2026-11-06 fix(today): the strip shows Busy and Private inside a block, and never a cut name
- `1b0acac0e` 2026-11-06 fix(today): one card-line form for the week's days and the last move
- `c4fbcc258` 2026-11-06 fix(today): the strip names what the brief names and draws private events as outlines
- `a10c7e236` 2026-11-06 fix(today): a strip block with no title reads Private on hover
- `962b75466` 2026-11-06 fix(today): Your topics leads with the week's work and says when it moved

</details>

### I. Review-round batches: walkthrough findings, mixed causes and test repairs

**Broken rule.** Fixes arrive in batches from walkthrough or review documents (today-review v1.1.x, chat-review, R/D/H numbers). Each batch patches many files for many causes, and the test suite is then repaired to match, so no single rule is changed.

**Evidence and recurrence.** Batches on 10-31, 11-01 and 11-02 (`b126ce08`, `f0538ddc`, `5d42785a`, `a60eab5a`, `ba0e5327`, `eb8a0e26`, `ef076a8a`) and test repairs such as `363112bf`, `3f09efbf`, `f710cd35`, `1925e8ee` and `caafb5e1`. Recurrence: 21 commits on 8 days, plus the test-only fixes that wait for background writers.

**Structural fix.** Replace review rounds with a per-screen acceptance check run against the real app on a fixed day of data (the daily real-app test in this worktree is the start). Each finding must name the rule it breaks, and each fix must come with one guard at the rule's owner module, not at the screen.

<details><summary>21 commits</summary>

- `cdfeb2c76` 2026-10-26 fix: address shared chat card review findings
- `b126ce084` 2026-10-31 fix(chat): live walk-through fixes (chat-review-v1.0.2.md)
- `f0538ddcd` 2026-10-31 fix(today): live walk-through fixes (today-review-v1.1.4.md)
- `5d42785ab` 2026-10-31 fix(tasks): live walk-through fixes (tasks-review v1.0.3)
- `f710cd351` 2026-10-31 fix: repair remaining root package Go tests
- `1925e8ee3` 2026-10-31 fix: restore root package Go tests
- `ffa74a813` 2026-10-31 fix(today): measure review actions and preserve receipt position
- `0cc44340a` 2026-10-31 fix(today): apply B4 review decisions
- `ba0e53272` 2026-11-01 fix(chat): second walk-through fixes (chat-review v1.0.3)
- `a60eab5a7` 2026-11-01 fix(today, tasks): second walk-through fixes (today-review v1.1.5, tasks-review v1.0.8)
- `d02dbaf9c` 2026-11-02 fix(today): finish v1.1.10 A-G and the regression gate
- `3f09efbf0` 2026-11-02 fix: address full suite regressions
- `7cb8dc92b` 2026-11-02 fix(today): improve brief review projections
- `eb8a0e26a` 2026-11-02 fix(today): address third walkthrough brief issues
- `9a6df41cf` 2026-11-02 fix(today): guard brief recommendations and strip labels
- `ef076a8a6` 2026-11-02 fix(tasks): address third walkthrough findings
- `bfbbc055c` 2026-11-03 fix(chat): chat-review v1.0.10 H1-H8, H10 (one pause line per event, unchosen stops only, one background st...
- `9c3008232` 2026-11-03 fix: tests broken by the privacy fixture replacements
- `caafb5e1c` 2026-11-04 fix: align brief and closing regression tests with design
- `a7be4f638` 2026-11-05 fix: address people and chat review feedback
- `363112bf2` 2026-11-10 fix(brief): stabilize full suite regressions

</details>

### J. Retention deletes brief inputs

**Broken rule.** Retention jobs delete activity, run and memory rows that a sealed or re-sealed brief still reads, so a brief can lose evidence it already showed.

**Evidence and recurrence.** `4e152ab8` (10-16) preserves history through run retention; `d4734815`, `f222c013`, `f2c9d37b` and `51a9b5dd` follow the same pattern, and `7171529c` (11-14) keeps attendance when retention removes a session during a re-seal. `08889fba` and `6a32f415` tolerate missing run stores. Recurrence: 8 commits on 4 days.

**Structural fix.** The sealed record copies the evidence it cites (refs plus the facts it used), so the brief no longer depends on rows that retention may delete. Retention then needs no brief-specific exception. One retention owner; delete the 'keep this because a brief reads it' exceptions.

<details><summary>8 commits</summary>

- `4e152ab80` 2026-10-16 fix(wails-gui): preserve missing history through run retention
- `d47348159` 2026-10-16 fix(wails-gui): preserve goals across bounded activity retention
- `f222c013f` 2026-10-16 fix(wails-gui): minimize retention receipts and preserve brief privacy
- `f2c9d37b4` 2026-10-16 fix(wails-gui): reserve brief evidence for upcoming work
- `51a9b5dd4` 2026-10-16 fix(wails-gui): preserve safe purpose briefs through retention
- `6a32f4153` 2026-11-06 fix(brief): tolerate a missing run store; classify the invoice repair
- `08889fbab` 2026-11-10 fix(brief): preserve unreadable run history
- `7171529ce` 2026-11-14 fix(brief): a re-seal keeps attendance the first seal kept when retention removes its session

</details>

### K. Other areas that touch the brief but are not brief-specific

**Broken rule.** Reverts, Purpose, memory, topic, notification and chat fixes that changed Today or brief files as a side effect.

**Evidence and recurrence.** Five reverts are in this group: `745090cb6`, `ecace3ca9` and `5ff815a07` (11-04, merges of the today-owner and today-audio branches), `af23e790` (10-25, Revert #231 stopped topic verification in the running app) and `327bfc8a4` (11-06, the saved topic finds view crashed the app). The reverts suggest that merges were landing before they were checked against the real app.

**Structural fix.** Keep the Today data owner separate from Purpose and topics. Gate merges to Today on the real-app check (see group I) so the revert cycle stops. No new module needed for this group; the work is in the review gate.

<details><summary>48 commits</summary>

- `a20b35e44` 2026-10-16 fix(wails-gui): keep the Purpose Map through the Today refresh and link delivered reminders
- `46b7ea269` 2026-10-16 fix(wails-gui): retain purposes backed by confirmed Self entries
- `dc7ca3615` 2026-10-16 fix(wails-gui): preserve desktop summary and Spotlight merge behavior
- `4f8e866d5` 2026-10-16 fix(wails-gui): refresh voice status and share download progress
- `74326056f` 2026-10-16 fix(wails-gui): suppress automatic voice download toasts
- `2de18404a` 2026-10-16 fix(wails-gui): harden product telemetry lifecycle
- `094db67eb` 2026-10-16 fix(wails-gui): preserve successful news and unavailable retries
- `fc7e32805` 2026-10-17 fix(wails-gui): make the chief of staff brief and the Purpose and Today work merge
- `6a55e2e13` 2026-10-17 fix(wails-gui): keep memory forgetting offline and upgrade-safe
- `e8b2b0808` 2026-10-19 fix(recording): silently preserve complete calls
- `51b63f5ea` 2026-10-21 fix(memory): preserve confirmation and uses across entry lifecycles
- `8ec399e08` 2026-10-21 fix(activity): count reading gaps across Week and daily averages
- `ed8f2b86f` 2026-10-22 fix(chat): preserve needs-you cards and direct reminders
- `d90ac8c5f` 2026-10-22 fix(today): the panel's box prepares a Today item as the main chat box does
- `a8e3a37f5` 2026-10-22 fix(memory): gate evidence links and normalize redaction titles
- `8a7319799` 2026-10-23 fix(setup): honor saved voice and after-call choices
- `b8cb903fb` 2026-10-23 fix(setup): keep briefing voices behind download confirmation
- `528d00c10` 2026-10-23 fix(topics): repaint pins from every matched follow alias
- `b00a25cfe` 2026-10-23 fix(ui): preserve topic types through display filtering
- `fec57bdd5` 2026-10-23 fix(ui): retire placeholder settings and simplify About details
- `f8b86a11a` 2026-10-25 fix(chat): open thread-outcome cards and unify the waiting count
- `af23e7900` 2026-10-25 Revert #231: Today stopped verifying any topics in the running app
- `efd4b900a` 2026-10-26 fix(today): disambiguate meeting attendee segments
- `2de6e3ca9` 2026-10-27 fix(settings): answer_ready notification sample copy
- `4d6c5fe76` 2026-10-30 fix(today): update retired news coverage after feature merge
- `a311ce2ea` 2026-10-30 fix: settings and setup show the real state
- `8bd9b80d3` 2026-10-31 fix(logs): log the wrapped cause of resync and brief failures; prep by person id
- `e5271fada` 2026-10-31 fix(today): share the sidebar pin in Your topics
- `0cf2c2950` 2026-10-31 fix(today): draft replies on existing notifications
- `75db40c3a` 2026-10-31 fix(today): link only to visible first-day sections
- `8cd73296a` 2026-10-31 fix(activity): answer from the selected period
- `2afcb31af` 2026-11-02 fix(privacy): clear remaining public fixtures
- `b8e1a5eac` 2026-11-02 fix(privacy): sanitize public source fixtures
- `0ade27dbc` 2026-11-02 fix(chat): preserve history across shared views
- `97a8a594a` 2026-11-02 fix(chat): share history and show brief title metadata
- `c80eea277` 2026-11-02 fix(chat): share the stream with Today
- `085087938` 2026-11-02 fix(today): project readable calendar titles
- `881fc86ca` 2026-11-03 fix(people): Thursday polish (design review v1.0.0)
- `745090cb6` 2026-11-04 Revert "Merge branch 'origin/claude/today-owner' into feat/iphone-companion"
- `ecace3ca9` 2026-11-04 Revert "Merge branch 'origin/claude/today-owner-2' into feat/iphone-companion"
- `5ff815a07` 2026-11-04 Revert "Merge branch 'origin/claude/today-audio-fix' into feat/iphone-companion"
- `87b105b4e` 2026-11-04 fix: wait for background writers before close
- `67b58cc1e` 2026-11-04 fix(people,chat): hide sessions and align needs count
- `8dec9aae2` 2026-11-04 fix(activity): path to 80, round 2 (v1.0.6 AR40-AR48, v1.0.7 AR14, AR16, AR50-AR54)
- `8ae0190ef` 2026-11-04 fix(menu-bar): Waiting on you is what Tasks Needs you lists, with no count (B7)
- `de9cf09a2` 2026-11-04 fix(shell): menu bar popover and What Isoform sees (shell review v1.0.0 B1-B6)
- `f2a178046` 2026-11-05 fix(notifications): count automatic deliveries against the cap
- `327bfc8a4` 2026-11-06 Revert the saved topic finds view (#601): the app crashes since this build

</details>

## 3. Symptoms that recurred after being fixed

Same visible bug, several fix commits. Each line names the group that owns the cause.

- **Talk about this brief button.** Recurred 5 times. `2b9147970` (10-21) made it a button again; `9f121fc1e` (10-26) opens the conversation without blocking navigation and `ae716aa7f` guards the chat selection and sends; `56b9b8552` and `a8024e2e7` (11-06) repaired the phone path. The hub review `claude-codex-talk-about-brief-self.md` found the cause: the desktop button awaited a backend call with no deadline. Commits: `2b9147970`, `9f121fc1e`, `ae716aa7f`, `56b9b8552`, `a8024e2e7`.
- **Read it / Hear it links.** Recurred about 10 times, 10-26 to 11-06. Each fix changed the link's gate (week recap rows, failed read, held card, words not yet present), not the state the link reads. Commits: `3534239b6`, `61e762ab1`, `e2571e97b`, `418825b63`, `18a9878bb`, `96f2fe96f`, `a7b337942`, `62af10e7b`, `80b42eecb`, `30667bd1a`.
- **Brief withheld when it should show.** Recurred about 8 times, 11-05 to 11-14. Each fix changed one withholding rule (`471d18f6` 'reduce spurious withholding', `c2243bbc` store not yet open) and a new one appeared the next day. Group B. Commits: `31037a13a`, `c2243bbc7`, `2571351c1`, `86efafef2`, `471d18f62`, `1702e7b84`, `fcbf25e03`, `2af78d8bf`.
- **Today blank or empty while data exists.** Recurred 6 times, 10-23 to 11-03, plus the topic-verification revert. Each fix changed one empty or loading branch. Group C. Commits: `7165e801f`, `dbac97699`, `4a0a2c481`, `365db7552`, `a603c5262`, `b1ac8a182`, `af23e7900`.
- **Spoken or recorded brief differs from shown text.** Recurred about 12 times, 10-21 to 11-14, one of the most-hit symptoms. `f144b299` is 'v3' for the audio. Group A. Commits: `7f8c6dd65`, `5a7a0516e`, `b86a41d41`, `891dcc9ba`, `2a6a9c1f0`, `5ff815a07`, `31089a7a5`, `c2bb296fb`, `02c83e3a1`, `0910843de`, `f144b2992`, `63e499b79`.
- **Made-ahead copy or wrong slot shown.** Recurred about 9 times, 11-02 to 11-14, 5 of them in the last week. Commits `7500c722` (11-11) and `8bfec6a4` (11-14) each stop a made-ahead copy from filling the slot. Groups A and D. Commits: `7500c7223`, `836f2099c`, `8bfec6a4f`, `880e611d9`, `26e5a3df9`, `60ecae385`, `7a54ebdf6`, `cf8b3712b`, `4cce2b974`.
- **Day strip overlap, clip or label fit.** Recurred about 13 times, 10-23 to 11-06, in three waves. Group H. Commits: `8772ae964`, `4241534c1`, `1a39308fc`, `5a44cc485`, `5ec9b2e91`, `56db1a13d`, `af8c00826`, `0f7861f72`, `a10c7e236`, `c4fbcc258`, `f1ef34fe3`, `3df1c8696`, `a7f2a6e85`.
- **Clock or 24-hour display wrong.** Recurred about 9 times, 10-18 to 11-06. Each fix repaired one surface (chip, zh-CN prose, strip) while the others kept their own clock. Group D. Commits: `db60ce572`, `981503101`, `cd05b0a9a`, `565922553`, `9e94f6379`, `0bc7d0b06`, `00854a0b4`, `54881729b`, `46e57c5b6`.

## 4. Pattern across the groups

- Groups A, B and C account for about 240 of the 523 fixes. All three come from the same design choice: state is re-derived at read time in each surface, not stored once and updated by an event.
- The hub reviews already name the same causes (`brief-reliability-self.md`: a write fence that rejects builds on unrelated writes; `claude-dsh-day-shape-2-self.md`: two reads painting at different times; `claude-codex-talk-about-brief-self.md`: an await with no deadline). The fixes went into each symptom rather than into the shared rule.
- Test-only churn is visible in the fix list: about 8 commits repair tests or wait for background writers (for example `363112bf`, `9c300823`, `f710cd35`, `1925e8ee`, `23800a68`). Those hide timing races rather than fixing them, and they sit in groups C and I.

