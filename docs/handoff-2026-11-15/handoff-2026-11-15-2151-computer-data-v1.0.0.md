# Computer data v1.0.0: Settings › Your data, in the computer pass

Version 1.0.0 · written November 15, 2026, 9:51 PM Pacific, by Claude Design.

**This file brings redesign-shell v1.0.0 rules 7 to 12 (Your data) into the new Settings (computer-settings v1.0.0), and changes them where it says so; the rest stands.** Since November 14 the sources changed (Messages and Your agents' history joined What Isoform sees), work goes to many agents rather than one, and Agent of You is named by name (agent-words v1.0.0). Your data has to say the same.

**Board:** Isoform Computer, new artboard 27 (Settings › Your data).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Everything kept, and everything that leaves, on one page,** with real numbers.
2. **The same names as What Isoform sees,** so a source is one thing everywhere.

## The page

1. **Title and line stand:** "Your data" / "Everything Isoform keeps is on this computer." Third in Settings, after Closed Eyes (computer-settings v1.0.0).
2. **What it keeps:** the six sources of What Isoform sees, in its order and with its names: Screen; Calls and meetings; Mail and calendar; Messages; Your watch; Your agents' history. Each row: what it is and how long it's kept, then its size, then **Change** (the keep time) or **Turn on** for one that's off. Mail and calendar shows a dash: they're read where they are, not copied.
3. **Under them, the whole:** "{used} of {cap}. When it's full, the oldest goes first." with Change. The build's own numbers: kept 30 days and up to 10 GB unless changed (`defaultActivityRetentionDays`, `defaultActivityMaxStorageGB`); pictures of the screen go with their day unless a shorter time is chosen.
4. **Yours:** "{n} answers since {date}" / "Kept until you delete them" with **Export** and **Delete…**; Your voice with **Retrain** and **Delete…** (redesign-shell v1.0.0 rules 9 and 10 stand).
5. **What leaves this computer, this week,** by where it goes (changes rule 11):
   - **{assistant}, which writes your briefs and answers:** "What each brief or answer is written from, each time · this week {n} briefs, {m} answers", with **See them ›**: this week's briefs and answers, each with the sources it was written from, by name and time;
   - **Your agents:** "Only what each hand-off's What went lists · this week {n}: {agent} {k}, …", with **Activity › Agents ›**;
   - **Your phone and watch:** "Today's items, briefs and results, sealed through the relay, which can't read them";
   - **Notifications:** "Sealed too. Apple's push service sees only a fixed line, such as "A meeting is about to start."" (PROTOCOL.md §14.4's fallback lines);
   - **Agent of You,** only while its beta is on: "The {n} claims Agent of You can say" (replaces "your agent", agent-words v1.0.0).
6. **Forget everything…** last, in red, with "Asks first, with what it removes" (rule 12 stands).

## What the build needs

- **Sizes per source,** for rule 2's sizes and rule 3's total.
- **A record of what each assistant call was written from** (the sources by name and time, never the text), kept as long as the sources are, for See them.
- PROTOCOL.md §14.4 still names the push title "Yansu"; the relay's code already sends "Isoform". The doc changes to match.

## Every word

| Key | English | Where |
| --- | --- | --- |
| data.keeps | What it keeps | Section (was data.gathers, "What it gathers") |
| data.notCopied | Read where they are; not copied | Mail and calendar |
| data.total | {used} of {cap}. When it's full, the oldest goes first. | Under the sources |
| data.turnOn | Turn on | A source that's off |
| data.yours | Yours | Section |
| data.assistant | {assistant}, which writes your briefs and answers | What leaves |
| data.assistantSub | What each brief or answer is written from, each time · this week {n} briefs, {m} answers | What leaves |
| data.seeThem | See them › | What leaves |
| data.agents | Only what each hand-off's What went lists · this week {n}: {counts} | What leaves |
| data.devices | Today's items, briefs and results, sealed through the relay, which can't read them | What leaves (changes redesign-shell's line) |
| data.notifs | Sealed too. Apple's push service sees only a fixed line, such as "{fallback}" | What leaves |
| data.aoy | The {n} claims Agent of You can say | What leaves, beta on (was data.agent) |
| data.forgetNote | Asks first, with what it removes | Beside Forget everything |

## Proof

On Bo's computer, a named build: Your data against artboard 27, with sizes that add up to the total, this week's counts matching Activity › Agents and Past briefs, and See them listing each brief with its sources.

## On the map (settings)

- S73 · Your data in the computer pass: the six sources of What Isoform sees with keep times and sizes, the total against the cap, Yours, what leaves by destination with this week's counts (assistant, agents, phone and watch, notifications; Agent of You when on), Forget everything last.
