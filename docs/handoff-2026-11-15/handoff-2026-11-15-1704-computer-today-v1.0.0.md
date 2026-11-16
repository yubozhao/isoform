# Computer Today v1.0.0: the brief across the top, Your work underneath with the keyboard, and the hand-off to your agent

Version 1.0.0 · written November 15, 2026, 5:04 PM Pacific, by Claude Design.

**Part of the computer pass of November 15.** Read computer-map v1.0.0 first; phone-today v1.0.0 and phone-agent v1.0.0 and v1.1.0 hold here unless this file says otherwise. This file replaces redesign-today v1.0.0's Today layout and redesign-work v1.0.0's Tasks.

**Board:** Isoform Computer, artboards 2 (Today) and 3 (Hand it to your agent).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **The latest brief shows up,** across the top, with Hear it.
2. **Judging work:** the whole day's Your work on one screen, answered from the keyboard.
3. **What happened is visible:** With your agents and Done today beside the open item.

## The page

1. **Title "Today", the date, and on the right "Briefs at 7:00 AM and 6:00 PM".**
2. **The brief band:** play (Space plays and pauses anywhere on Today), "Evening brief · 6:00 PM", "Hear it · 2 min", its first three sentences, Read all ›; on the right, "In this brief": meetings tomorrow, things to answer, what came from your watch, Past briefs ›. Every state of phone-today rule 7 applies, with "computer" words where the phone says "phone" (the brief is made here, so "comes when your computer wakes" never shows; "Couldn't make the evening brief: {reason}. Try again" can).
3. **Your work, two columns:**
   - **left, the open item** (the shared item, with the keys beside its buttons: Y, N, M; Edit E; "1 of 4 · ↑↓ to move"), focused with a ring;
   - **right, Up next** (each row with its kind or its agent: Codex, Muse, Draft), **With your agents** (one row per hand-off), and **Done today** (with Undo).
   - Clicking a row in Up next makes it the open item.
4. **The keyboard, at the foot:** ↑ ↓ move, Y N M answer, E edit, ⌘Z undo, and ⌥Space Quick chat at the right. After an answer the focus moves to the next item.
5. **Edit opens in place,** the work editable in the open item's box, with What it gets as chips under it, and Use it (⌘↩) or Hand it over (⌘↩ on offered work).

## The hand-off

Artboard 3. The same as phone-agent v1.0.0 and v1.1.0, as a sheet over Today:

6. **"Hand it to Codex"** with the agent's badge and "Your agent for code · connected on this computer", and Change agent.
7. **"This is exactly what goes:"**, What it gets (removable rows), "Done looks like:", then Cancel and **Send to Codex ⌘↩**, with "Codex works in its own account. The result comes back to Your work."
8. **The agent is picked by the work:** a proposal that touches a repo goes to the code agent; everything else to the other.

## What goes

9. The Tasks page, its "Give Isoform a task" field (ask in Chat or Quick chat), and Scheduled.
10. Brief items as chips under the brief; "Plan next steps" and "Not now" as buttons.

## What the build needs

B1, B2, B3 and B4 (phone-map), C6 (computer-map): one list with the phone, the keyboard focus, and the hand-off.

## Every word

The phone's keys stand (today.*, agent.*). New here:

| Key | English | Where |
| --- | --- | --- |
| ctoday.times | Briefs at {morning} and {evening} | Header |
| ctoday.inBrief | In this brief | Brief band |
| ctoday.meetings | {n} meetings tomorrow | Brief band |
| ctoday.toAnswer | {n} things to answer | Brief band |
| ctoday.fromWatch | From your watch: {n} conversation | Brief band |
| ctoday.workSub | {n} to answer · {m} back from your agents | Your work |
| ctoday.withAgents | With your agents | Group |
| ctoday.move | {i} of {n} · ↑↓ to move | Under the open item |
| ctoday.keys | move · answer · edit · undo | Keyboard row |
| ctoday.couldnt | Couldn't make the {kind} brief: {reason}. Try again | Brief band |
| agent.subtitle | Your agent for {kind} · connected on this computer | Hand-off |
| agent.foot | {agent} works in its own account. The result comes back to Your work. | Hand-off |

## Proof

On Bo's computer, a named desktop build. **Before:** walk-1113's Today and Tasks.

1. **The brief on top:** with the network off, Today opens on the latest brief; Space plays it.
2. **Keyboard judging:** five items answered with Y, N and M in under twenty seconds; the phone shows them answered.
3. **Hand-off:** a Yes on a code proposal opens the sheet with Codex; ⌘↩ sends it; it appears under With your agents.

## On the map (shell)

- SH1 · Today: the brief band on top (Space plays), from what this computer made.
- SH2 · Your work in two columns: the open item, and Up next, With your agents, Done today; the keyboard.
- SH3 · The Tasks page goes; its work is on Today and Agents.
- SH4 · The hand-off on the computer: the agent picked by the work; ⌘↩ sends.
