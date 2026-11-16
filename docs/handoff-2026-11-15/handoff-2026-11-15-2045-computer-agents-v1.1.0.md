# Computer agents v1.1.0: every state of Agents, one agent opened, and no "Working" where Isoform can't see it

Version 1.1.0 · written November 15, 2026, 8:45 PM Pacific, by Claude Design.

**This file adds to computer-agents v1.0.0; read that first.** Everything in v1.0.0 stands except where this file says so. It draws Agents with no agents yet, on a quiet day, in trouble, and with one agent opened, and it fixes one word on v1.0.0's board: Claude, an agent in its own app, showed "Working". Isoform can't see a chat app work, so it never says so (agents-join v1.0.0 rule 22).

**Board:** Isoform Computer, artboard 13 (Agents, every state), frames 13a to 13d; artboard 4 corrected.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Trouble is visible where it happens,** in the agent's own words, with a way forward.
2. **Nothing is said that Isoform doesn't know.**
3. **An empty page is the way to fill it:** the connect sheet, not three empty columns.

## Correction to v1.0.0

1. **Artboard 4 now reads** "Claude · For everything else · in its app · Has it: Q4 plan outline" (no green dot) and, in the timeline, "Yes on "Q4 plan outline". Opened in Claude; Claude has it." The green dot means only "Isoform can see it working": an agent on this computer while its process runs, or a cloud agent while its account says so.

## 13a · No agents yet

2. **With no agent connected, the page is the connect sheet, full size:** "Connect the agents you already use" / "A yes on offered work goes straight to them, and what they make comes back to Your work for you to judge. They see only what each hand-off sends." / On this computer (each found agent with Connect, the first one filled) / Any other agent (the message, Copy, "Paste it into Claude, ChatGPT, Grok, Gemini or any agent that can read a link.") / "Until you connect one, a yes on offered work copies it for you to paste."
3. **Once one agent is connected,** the page becomes v1.0.0's three columns.

## 13b · A quiet day

4. **Each column says once that it's empty and what fills it:** "Nothing between you and your agents today." / "When you say yes to offered work, it shows here."; "Nothing yet this week."; "Learn from it is off for every agent." with Choose ›. Agent rows show "Last worked {day}".

## 13c · Trouble, said honestly

5. **Couldn't start:** the agent's row says "Couldn't start · {time}" in amber; the timeline row "{agent} couldn't start" quotes it ("Codex said: not logged in.") and "Nothing was sent.", with **Open {agent}** and **Send again** (computer-today v1.1.0 rule 10).
6. **A request that ran out:** "{from} asked {to} · ran out" / "You didn't answer within a day, so nothing moved. {from} was told "No answer from your person.""
7. **No word since:** the row says "No word since {time}" in amber; the timeline row has **Ask again** and **Take it back**.
8. **A result with a draft:** "Back from {agent} · 1 draft needs your yes", the sentence, and "{draft} · draft · in Your work ›". The draft is its own item in Your work; nothing was sent (computer-agents v1.0.0 rule 17 stands).
9. **Disconnected:** the row fades with "Disconnected · {date}" and **Connect again**, which makes a new join link (agents-join §2). It stays in the list for 30 days, then leaves; its past hand-offs stay in Activity and Your data.
10. **Amber is for something to act on,** never for an agent that is merely idle.

## 13d · One agent, opened

11. **Clicking an agent** opens its card beside the list (the timeline narrows to it under the card): badge, name, "Connected {date} · {where} · {how it runs}" ("runs when Isoform starts it" for agents on this computer; "in its app; you open it" for chat apps).
12. **Its job** (For code: work that touches a repository / For everything else) with Change its job ›; **What it may open** ("Only what each hand-off sends, and new branches in {repos}.") with Change ›; **Learn from it**, the switch, off, with "Read {agent}'s own history on this computer (its sessions, the files it changed) as a source, with Forget. Off until you turn it on." (only for agents on this computer; others say "Not available for agents in their own app."); **This week** ("{n} hand-offs · {m} back · {u} useful", from your own judgments); and **Disconnect**.
13. **Disconnect asks once:** "Disconnect {agent}? It stops getting hand-offs at once, and can't use Isoform's tools any more. What it already made stays with it." with Cancel and Disconnect.

## Every word

| Key | English | Where |
| --- | --- | --- |
| agents.emptyTitle | Connect the agents you already use | No agents |
| agents.emptyBody | A yes on offered work goes straight to them, and what they make comes back to Your work for you to judge. They see only what each hand-off sends. | No agents |
| agents.untilThen | Until you connect one, a yes on offered work copies it for you to paste. | No agents |
| agents.quietToday | Nothing between you and your agents today. | Quiet |
| agents.quietTodaySub | When you say yes to offered work, it shows here. | Quiet |
| agents.quietMade | Nothing yet this week. | Quiet |
| agents.learnOffAll | Learn from it is off for every agent. | Quiet |
| agents.lastWorked | Last worked {day} | Row |
| agents.couldntStart | Couldn't start · {time} | Row |
| agents.noWordSince | No word since {time} | Row |
| agents.ranOut | {from} asked {to} · ran out | Timeline |
| agents.ranOutBody | You didn't answer within a day, so nothing moved. {from} was told "No answer from your person." | Timeline |
| agents.draftNeedsYes | Back from {agent} · {n} draft needs your yes | Timeline |
| agents.disconnectedOn | Disconnected · {date} | Row |
| agents.connectAgain | Connect again | Row |
| agents.hasIt | Has it: {title} | Row |
| agents.runsWhenStarted | runs when Isoform starts it | Agent card |
| agents.inItsApp | in its app; you open it | Agent card |
| agents.mayOpen | Only what each hand-off sends, and new branches in {repos}. | Agent card |
| agents.learnNotHere | Not available for agents in their own app. | Agent card |
| agents.thisWeek | {n} hand-offs · {m} back · {u} useful | Agent card |
| agents.disconnectAsk | Disconnect {agent}? It stops getting hand-offs at once, and can't use Isoform's tools any more. What it already made stays with it. | Confirm |

## Proof

On Bo's computer, a named desktop build:

1. **Honest dot:** with Claude (in its app) holding a hand-off and Codex running one on this computer, only Codex has the green dot; a screenshot.
2. **Empty page:** a fresh setup with Skip at Your agents shows 13a, not empty columns.
3. **Disconnect:** after Disconnect, Codex's tools refuse at once; its row shows Disconnected with Connect again.

## On the map (shell)

- AG7 · Agents with none connected is the connect sheet, full size; a quiet day says what fills each column.
- AG8 · Trouble on Agents: couldn't start, no word since, ran out, a draft that needs a yes, disconnected; amber only when there's something to do.
- AG9 · One agent opened: where and how it runs, its job, what it may open, Learn from it, this week, Disconnect.
- AG10 · The green dot means only that Isoform can see the agent working.
