# Computer activity v1.0.0: one timeline of the day across your screen, your watch and your agents, with Forget on every row

Version 1.0.0 · written November 15, 2026, 5:04 PM Pacific, by Claude Design.

**Part of the computer pass of November 15.** Read computer-map v1.0.0 first. redesign-activity v1.0.0 holds except where this file says so: Forget and what goes with it, Closed Eyes as a gap, Your searches, Your answers and the four numbers.

**Board:** Isoform Computer, artboard 7, left (Activity).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Everything Isoform saw or did is here,** by time and source; nothing it uses is missing.
2. **Forget works on any row** and says what goes with it.
3. **Your watch and your agents** are sources like the screen, in the same timeline.

## The page

1. **Title "Activity"; under it:** "What Isoform saw today, by time and source. Forget anything; what was made from it goes too."
2. **Tabs:** Your day, Screen, Watch, Agents, Your answers. These replace redesign-activity rule 2's tabs: What you worked on and the Screen timeline are Screen; What it learned moves to You, where each claim has its Why.
3. **One row per thing,** newest first (changed from redesign-activity rule 3: the day you're in starts with what just happened; scroll down for the morning): the time, a small dot in the source's colour, the source ("Your watch", "Screen · Mail", "Your agents · Codex"), what it was in a few words, a line under it, and Forget.
4. **Your watch's rows** are its conversations, by name and length, with who spoke and whether there's a mark or a note ("Pricing with Sam · 36 min · You, Sam · 1 mark · note"); opening one shows the same conversation page as the phone (phone-watch v1.0.0).
5. **Your agents' rows** are hand-offs and results ("Write the test plan · Handed over by your Yes · back at 6:40 PM"), requests between agents with your answer, and, only where Learn from it is on, what Isoform read from the agent's own history.
6. **Messages' rows,** once Messages is on (computer-settings rule 4): one row per conversation with someone you know, by the hour, never the message text in the row.
7. **Closed Eyes is a gap:** "11:04–12:04 · nothing was kept", striped, and nothing else about that time anywhere (redesign-activity rule 5).
8. **No coloured bar at a row's left edge** (AGENTS.md rule 12): the source is the dot and its name.

## What the build needs

C3 (what agents made), C4 (learn from them), C5 (Messages), B6 (conversations) and B7 (watch state); the rest is redesign-activity's.

## Every word

redesign-activity's keys stand (act.*). New here:

| Key | English | Where |
| --- | --- | --- |
| act.sub | What Isoform saw today, by time and source. Forget anything; what was made from it goes too. | Page |
| act.tabWatch | Watch | Tab |
| act.tabAgents | Agents | Tab |
| act.tabScreen | Screen | Tab |
| act.handedOver | Handed over by your Yes · back at {time} | Agent row |
| act.agentAsked | {from} asked {to} · you said {answer} | Agent row |
| act.closedGap | {start}–{end} · nothing was kept | Closed Eyes row |

## Proof

On Bo's computer, a named desktop build. **Before:** walk-1113's Activity and What it learned.

1. **One timeline:** a real day shows screen, watch and agent rows together, newest first.
2. **Forget a watch conversation:** it names what goes with it (its note and the reply drafted from it); after confirming, they're gone from Today and the phone.
3. **Agents' history, off:** with Learn from it off, the Agents tab shows only hand-offs, results and requests.

## On the map (shell)

- AC1 · Activity: one timeline, newest first, across screen, watch and agents; tabs Your day, Screen, Watch, Agents, Your answers.
- AC2 · Watch and agents are sources with Forget, like the screen.
- AC3 · What it learned moves to You; each claim keeps its Why.
