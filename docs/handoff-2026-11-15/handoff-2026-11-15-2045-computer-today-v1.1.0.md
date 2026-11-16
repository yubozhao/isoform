# Computer Today v1.1.0: every state of Your work on the computer

Version 1.1.0 · written November 15, 2026, 8:45 PM Pacific, by Claude Design.

**This file adds to computer-today v1.0.0; read that first.** Everything in v1.0.0 stands. v1.0.0 drew one full day; this file draws the states a builder meets on day one and on bad days: the first day, a result to judge, an agent asking another, all answered, a hand-off that couldn't start, no word from an agent, and Undo. Words follow agents-join v1.0.0 (what Isoform may say about an agent).

**Board:** Isoform Computer, artboard 12 (Today, every state), six frames 12a to 12f.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **The brief band never goes empty,** even before the first brief.
2. **Every state says what's true and what to do next,** in Isoform's words or the agent's own; never "Something went wrong".
3. **Answering stays one key,** whatever the item is: work, a result, or an agent's request.

## 12a · First day

1. **Before the first brief,** the band shows a clock in place of play, "Your first brief" / "at 6:00 PM", and "Your first brief comes at 6:00 PM, made from today on this computer. Until then, here's what Isoform can already do." It becomes the brief in place when it's made (B1).
2. **Your work holds setup's day-one work** (redesign-shell v1.0.0 step 3), with "{n} to answer · day one" and the source line "Day one · from your calendar" (or mail, or files).
3. **With no agent connected,** the right column has one card under Up next: "Your agents" / "None connected yet. A yes on offered work goes straight to the agent you use." / Connect an agent ›. It goes once one agent is connected; it never comes back as a nag.

## 12b · A result to judge

4. **An agent's result is an item,** first among what's left (phone-agent v1.0.0 rule 16): "Back from {agent} · you said yes at {time}", Reply at the top right, one sentence of what it made, the summary in the grey box with Read it all ›, the file as a chip with where it is ("test-plan.md · branch halcyon/test-plan · Open"), then "Was it useful?" with **Useful (Y)** and **Not useful (N)**.
5. **Not useful asks for a reason,** optionally, as chips (phone-agent v1.0.0 rule 17). Neither answer sends anything to the agent.
6. **A result that came through the report link** says so in its source line: "Back from Instinct · sent through the report link" (agents-join rule 30).

## 12c · An agent asking another

7. **The request is an item:** "{from} asks {to} · {time}", the sentence, then a grey box "What would move:" naming both directions ("Codex's question to Muse, and Muse's summary back to Codex. Nothing else of yours."), then "Yes lets {to} do it. It waits until {time} tomorrow, then ends." with **Yes (Y)** and **No (N)**. No Not mine: it's your agents asking.
8. **It waits a day.** Then it ends by itself: nothing moves, the asker hears "No answer from your person.", and Agents shows "ran out" (computer-agents v1.1.0 rule 6). The sidebar's Agents count includes it while it waits.

## 12d · All answered

9. **The open item's column** says "That's everything for now." and "Prep comes an hour before a meeting; follow-ups, ten minutes after the call or email." (phone-today rule 16). With your agents and Done today stay. The Today count leaves the sidebar. No streak, no celebration.

## 12e · A hand-off that couldn't start

10. **When an agent Isoform starts fails before it begins** (agents-join §6, runs on this computer or in the cloud), the sheet turns into: "{agent} couldn't start" / "Nothing was sent; the work is still in Your work." / "{agent} said:" with its own words quoted in mono / one line of what to do, from what it said when Isoform recognises it ("Sign in to Codex on this computer, then send again. Or hand it to another agent.") and otherwise none / **Change agent** and **Send again ⌘↩**.
11. **The item stays offered,** in place. Agents shows the same event (computer-agents v1.1.0 rule 5).

## 12f · No word, and Undo

12. **No word:** after two hours with nothing from the agent, the With your agents row reads "No word from {agent} since {time}" in amber, with **Ask again** and **Take it back** (agents-join rules 22 and 23). Ask again sends the same hand-off once more and resets the two hours.
13. **Undo:** every answer shows one line at the bottom for five seconds, "{answer} · {what happened}" with Undo and ⌘Z ("Yes · Prep for Lena kept for 10:00"). Done today keeps Undo until tomorrow's build. Undo on a hand-off that already went says what it can't undo: "Taken back. Codex can't open it any more; anything it already made stays with Codex."

## Counts

14. **The sidebar's Today count** is what's left to answer; it disappears at zero. The **Agents count** is requests waiting for your yes, only. Neither counts results; results are in Your work's own count ("{n} to answer · {m} back from your agents").

## Every word

| Key | English | Where |
| --- | --- | --- |
| ctoday.firstTitle | Your first brief | Band, day one |
| ctoday.firstAt | at {time} | Band, day one |
| ctoday.firstBody | Your first brief comes at {time}, made from today on this computer. Until then, here's what Isoform can already do. | Band, day one |
| ctoday.dayOne | {n} to answer · day one | Your work, day one |
| ctoday.dayOneSource | Day one · from your {source} | Item source |
| ctoday.noAgents | None connected yet. A yes on offered work goes straight to the agent you use. | Right column, day one |
| ctoday.backFrom | Back from {agent} · you said yes at {time} | Result item |
| ctoday.backViaReport | Back from {agent} · sent through the report link | Result item |
| ctoday.useful | Was it useful? | Result item |
| ctoday.asks | {from} asks {to} · {time} | Request item |
| ctoday.wouldMove | What would move: | Request item |
| ctoday.waitsUntil | Yes lets {to} do it. It waits until {time} tomorrow, then ends. | Request item |
| ctoday.allDone | That's everything for now. | All answered |
| ctoday.allDoneSub | Prep comes an hour before a meeting; follow-ups, ten minutes after the call or email. | All answered |
| ctoday.couldntStart | {agent} couldn't start | Sheet |
| ctoday.nothingSent | Nothing was sent; the work is still in Your work. | Sheet |
| ctoday.agentSaid | {agent} said: | Sheet |
| ctoday.sendAgain | Send again | Sheet |
| ctoday.changeAgent | Change agent | Sheet |
| ctoday.undoLine | {answer} · {what happened} | Bottom line |

## Proof

On Bo's computer, a named desktop build:

1. **Day one:** a fresh setup at 2 PM shows the clock band with "at 6:00 PM" and setup's day-one items; at 6:00 PM the band becomes the brief without a tap. A screen recording across 6:00.
2. **Couldn't start:** with Codex signed out, Send shows "Codex couldn't start" with Codex's own words, and the item is still offered.
3. **Counts:** with two items, a result and one request, the sidebar shows Today 2 and Agents 1; answering both items removes the Today count.

## On the map (shell)

- SH8 · Today's first day: the band says when the first brief comes; Your work holds setup's day-one work.
- SH9 · Results and agents' requests are items, answered with Y and N; a request waits a day, then ends.
- SH10 · A hand-off that couldn't start shows the agent's own words and keeps the item offered; no word after two hours offers Ask again and Take it back.
- SH11 · Counts: Today is what's left to answer; Agents is requests waiting for your yes.
