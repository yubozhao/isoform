# Phone agent v1.0.0: a Yes on offered work goes to your agent, and what it makes comes back to be judged

Version 1.0.0 · written November 15, 2026, 2:02 PM Pacific, by Claude Design.

**Part of the phone pass of November 15.** Read phone-map v1.0.0 first. This file is the path a Yes takes on work Isoform offers: the hand-off (what was "Work on it"), the first-time choice of agent, With your agent, the result back, and the stand-in while agents can't report back. It replaces the Work on it sheet behind its Settings switch (6ab902bad).

**The hub, from Bo's question about Tasks:** "Isoform proposes, you judge it (yes / no / not mine), a yes goes to your agent through Work on it, and the result comes back for another judgment."

**Board:** Isoform iPhone, artboard 4 (Yes goes to your agent).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **The Yes is recorded on the first tap,** whatever happens after.
2. **Exactly what goes is seen before it goes,** every part removable.
3. **One tap to send,** to the agent the person chose once.
4. **The result comes back and is judged:** Useful or Not useful.
5. **While it's out, it's visible:** With your agent, with who has it and since when.

## Why only offered work, and why one more step

1. **Work Isoform already did** (prep, a drafted reply) is used where it lands with one tap; an agent adds a company and a step for nothing. Its ••• menu offers "Hand it to your agent" for the rare time the person wants an agent to finish it.
2. **Work Isoform offers** ("Write the test plan") is done by an agent the person already uses, in its own account. Isoform keeps the decision and the data and passes down only the task (rework plan, computer use).
3. **This is the one Yes with a second step,** because it leaves the person's devices for another company. The first tap is the judgment and is recorded; Send is the action. Closing the sheet without sending keeps the Yes and shows the item as "Yes · not sent", with Send in its row.

## The hand-off

Artboard 4, frame 1.

4. **"Hand it to Claude"** (the person's agent) as the sheet's title, Cancel at the right.
5. **"This is exactly what goes:"** and the task as the agent will read it, in plain words, ending with what it must not do ("Don't change any code.", "Leave it as a draft; don't send.").
6. **What it gets:** each attachment as a row, with where it came from and a size ("Sample rows, names removed · 40 rows"), and × to remove it. Removing one rewrites the task without it.
7. **"Done looks like:"** one line.
8. **Send to Claude,** full width. Under it, "Your agent works in its own account." and "Change agent".
9. **Edit first, if you like:** Edit on offered work opens the task full screen with What it gets; it ends in **Hand it over**, and the agent gets your version. The edit is kept word for word, like every edit.
10. **What it never carries:** anything under Closed Eyes or from an excluded app, passwords or keys, and nothing beyond What it gets (the rework plan's filter at build time applies here again at send).

## The first time: who does your work?

Frame 2.

11. **Asked once, on the first Yes on offered work:** "Who does your work?", "Isoform finds the work and you judge it. The doing goes to an agent you already use, in its own account.", then Claude, ChatGPT, Grok, Codex, one picked, and "Use {agent}". "Change it any time in Settings › Your agent."
12. **It never blocks a Yes on work already done.** The person who never picks an agent can still use every draft and prep.
13. **On the computer** the same setting also offers the agents installed there (phone-map rule 21).

## With your agent

Frame 3.

14. **After Send:** "Sent to {agent}" for two seconds, and the item moves to With your agent as one row: its title, "{agent} · since {time}".
15. **Opened, the row says** who has it, when it went, "you said yes", and "{agent} reports back here when it's done. You'll judge what it made." with three actions:
    - **Show what went** (exactly what was sent, read-only);
    - **It's done** (finished without a report: it becomes a result to judge, with what the person pastes or says);
    - **Took it back** (the agent won't finish it; recorded, no judgment asked).

## The result back

Frame 4.

16. **It joins Your work at the top of what's left:** "Back from Claude · you said yes at 6:20 PM", one bold sentence of what it made ("The test plan is ready: 14 cases."), a summary in the grey box with "Read it all ›", then **Was it useful?** with **Useful** and **Not useful**, once. **Reply** at the top right opens Chat with it attached; a file result says "Open on your computer".
17. **Not useful asks for a reason, optionally:** Wrong facts, Missed the point, I'll do it myself.
18. **Both judgments count** in the daily measures: the Yes as accepted, the result as useful or not (redesign v1.0.0's measures; rework plan, "Measured daily").

## Until agents can report back

Frame 5. The one-time task links (agents.isoform.ai) aren't live (B5).

19. **Send copies the task and opens the agent's app:** "Copied. Paste it in Claude and send." and **Open Claude**; "Isoform asks you tomorrow morning whether it's done, unless you say so first."
20. **The next morning one check comes to Your work:** "Did Claude finish the test plan?" with **Done, useful**, **Not useful** and **Not done**. Not done keeps it With your agent for one more day, then asks once more and lets it go.
21. **When the link server is live,** the report (rule 16) replaces the check, and Send opens the agent with the task already in it.

## Settings › Your data

22. **"Sent to your agent"** lists each hand-off with exactly what went, when, and to which agent (phone-settings v1.0.0).

## What goes

23. The Work on it switch in Settings, and "Work on it" as a separate button: handing work over is how Yes works on offered work.
24. Copy that reads "tell me what you did": the check (rule 20) asks instead.

## What the build needs

B4 (Your agent, and each hand-off kept), B5 (agents report back; until then the morning check), B2 (the result and the check as items in the one list), B3 (Useful, Not useful, and the Yes recorded before Send).

## Every word

| Key | English | Where |
| --- | --- | --- |
| agent.sheet.title | Hand it to {agent} | Sheet |
| agent.exact | This is exactly what goes: | Sheet |
| agent.gets | What it gets | Sheet |
| agent.done | Done looks like: {text} | Sheet |
| agent.send | Send to {agent} | Button |
| agent.ownAccount | Your agent works in its own account. | Sheet |
| agent.change | Change agent | Sheet |
| agent.yesLine | Yes hands it to {agent}, your agent. You see exactly what goes first. | The item's Yes line |
| agent.notSent | Yes · not sent | Item row |
| agent.pick.title | Who does your work? | First time |
| agent.pick.body | Isoform finds the work and you judge it. The doing goes to an agent you already use, in its own account. | First time |
| agent.pick.use | Use {agent} | First time |
| agent.pick.foot | Change it any time in Settings › Your agent. | First time |
| agent.sent | Sent to {agent} | Toast |
| agent.row | {agent} · since {time} | With your agent |
| agent.reports | {agent} reports back here when it's done. You'll judge what it made. | Opened row |
| agent.showWent | Show what went | Opened row |
| agent.itsDone | It's done | Opened row |
| agent.tookBack | Took it back | Opened row |
| agent.back | Back from {agent} · you said yes at {time} | Result |
| agent.useful | Was it useful? | Result |
| agent.usefulYes | Useful | Result |
| agent.usefulNo | Not useful | Result |
| agent.openComputer | Open on your computer | File result |
| agent.copied | Copied. Paste it in {agent} and send. | Stand-in |
| agent.open | Open {agent} | Stand-in |
| agent.askTomorrow | Isoform asks you tomorrow morning whether it's done, unless you say so first. | Stand-in |
| agent.check | Did {agent} finish {title}? | Morning check |
| agent.check.done | Done, useful | Morning check |
| agent.check.not | Not done | Morning check |

## Proof

On Bo's iPhone, a named TestFlight build. **Before:** the Work on it sheet behind its switch on `feat/iphone-companion` (e313d8bb7), and the Tasks tab's Do it.

1. **The Yes counts before Send:** Bo says Yes and closes the sheet; the computer's Your answers shows the Yes, and the item reads "Yes · not sent".
2. **Exactly what goes:** removing one attachment rewrites the task without it; Your data › Sent to your agent shows what went, word for word.
3. **The loop closes:** with the stand-in, the next morning's check appears, and Bo's "Done, useful" shows in the daily measures as accepted and useful.
4. **Done work stays one tap:** a drafted reply's Yes puts it in Mail drafts with no sheet.

## On the map (phone)

- P224 · Yes on offered work opens the hand-off: exactly what goes, What it gets removable, Send; the Yes is recorded on the first tap.
- P225 · The first Yes asks once who does your work; Settings › Your agent.
- P226 · With your agent: one row; Show what went, It's done, Took it back.
- P227 · The result comes back to Your work: Was it useful? Useful or Not useful; until agents report back, the morning check.
