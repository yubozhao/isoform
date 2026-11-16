# Brief writer tokens (Today rework, group E part 2)

## Problem

The writer (`internal/brief`, `mobile_brief_new_writer.go`) returns JSON paragraphs, but each paragraph's text is free prose. The model writes its own day words, weekdays, dates and clock times, and about 50 regex passes then fix or hide what it got wrong:
- `internal/brief/dates.go` repairs weekdays and past dates;
- `brief_text_time.go` normalizes dates and clocks;
- `mobile_brief_relative_display.go` rewrites today, yesterday and tomorrow for the reader's day;
- the voice, hygiene and walkthrough guards.

Each slip adds a pattern (63 fix commits in 30 days). On 2026-11-15 a rewrite with the wrong anchor shifted every day word in the morning brief.

## Design

1. **The model writes tokens, never day or time words.** For an item it cites, it writes `{when:eN}` where the day and time go ("The interview is {when:e3}."). The prompt lists each item's ref, and our code knows each item's time. Free day words ("today", "yesterday", "tomorrow", weekdays, month dates, clock times) are not allowed in the text.
2. **One validator at build time** (`internal/brief`):
   - every token names a cited ref that has a time;
   - no free day, date or clock words remain;
   - the existing length and part rules still apply.

   A failing paragraph gets one rewrite call. If it fails again it is dropped at build, and never at read.
3. **Rendering at read.** `briefRenderTokens(brief, now, lang)` replaces each token with the item's day words relative to the reader's now, through `briefclock`:
   - the day: today, yesterday, tomorrow, a weekday this week, otherwise a date;
   - then the clock in the person's format.

   Today, the phone package and the menu bar all render at read. The stored text keeps its tokens, so it never needs rewriting.
4. **Voice.** The recording is made from the text rendered at build time, for the brief's own day. When a read on another day renders different words, the recording stays (Bo's ranking) and its binding is computed from the tokenized text, so a day change does not drop it.
5. **Old briefs.** A brief without tokens keeps today's path. It ages out with retention, and no migration is needed.
6. **Deletions, after the tokens ship:**
   - the date repair in `dates.go`;
   - the today, yesterday and tomorrow rewrite in `mobile_brief_relative_display.go`, which is kept only for tokenless briefs until they age out;
   - the clock and date normalization of model text in `brief_text_time.go`;
   - the display-side copy guards that only exist for model day and time slips.
7. **Golden files:**
   - `internal/brief/testdata/golden/*.json` holds synthetic material and expected token output, plus validator cases (pass, rewrite, drop).
   - Briefs made from real data are redacted and kept out of the repository (`~/isoform-trials/golden/`, on the test machine only), so they never reach the public export.

## Plan

- **PR A:** token syntax, validator, renderer and `briefclock` rendering, with golden tests. The writer prompt is not changed yet.
- **PR B:** the writer prompt asks for tokens, the parser keeps them, and Today, the phone and the voice render them. A real-data replay on the Mac mini's copy compares day words before and after.
- **PR C:** delete the regex passes listed in step 6.
