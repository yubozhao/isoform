# Agents join v1.0.0: how an agent joins an Isoform, what it gets, how results come back, and what Isoform may say about it

Version 1.0.0 · written November 15, 2026, 8:41 PM Pacific, by Claude Design.

**Why this file:** phone-agent v1.1.0 and computer-agents v1.0.0 say "connect an agent once with one message" and "a connected agent gets each hand-off directly". Neither says what the agent reads at isoform.ai/j/{code}, how the person confirms it, or what "directly" can honestly mean for each kind of agent. The Agents builder needs all three now. This file is the protocol and every word of it. It changes phone-agent v1.1.0 rules 5 to 8 and computer-today v1.0.0 rule 7 where it says so; everything else in them stands.

**Read with:** phone-agent v1.1.0 (§2 to §4), computer-agents v1.0.0, computer-map v1.0.0 (C1 to C3).

**Board:** Isoform Computer (https://claude.ai/artifact/4E18TrYUYKfQHqTQdtfoPn), new artboards 9 (Joining, from both sides), 10 (The join page) and 11 (What goes to an agent, and what comes back).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Nothing connects without the person's yes in Isoform,** and a leaked link can't connect someone else's agent.
2. **What Isoform says about an agent is only what it knows:** never "Working" when it can't see the work.
3. **An agent gets only what a hand-off sends,** and Isoform's tools give it nothing more.
4. **One pasted message is enough** for any agent that can read a link.
5. **Results come back by themselves** wherever the agent can send them; otherwise one report link.

## 1. Two words: connected, and not connected

1. **Connected** means the agent has Isoform's tools (a tool server it added, §4): it reads its hand-offs and sends results back by itself. Claude Code, Codex, Cursor's agent, Devin, and Claude, ChatGPT or Gemini with the Isoform connector added.
2. **Not connected** means Isoform writes the task in for the person (into Messages, or into the agent's app), and the result comes back through the report link (§7). Instinct by text today, and any app without the connector.
3. **These two words are the only states an agent has in Your agents,** plus "Disconnected" after Disconnect. "Added" or "Linked" never show.

## 2. Joining, from the person's side

Artboard 9, right and middle.

4. **Connect an agent** makes a join link: one use, ten minutes, tied to the person and the device that made it. The sheet shows "Waiting for your agent to join", the message with Copy again, "The link works once, for {n} more minutes.", and Cancel (phone-agent v1.1.0 rules 5 and 6 stand for the sheet's three steps).
5. **When an agent connects with the link,** Isoform asks on the device that made it, as a sheet over whatever is open: "{agent} is asking to join" / "From {where} · to work in {what it may open}" / "Does your agent show this code?" with the four digits, large, then **No** and **Yes, it shows {code}**, and "Only Yes connects it. Disconnect any time in Agents."
6. **The code comes from the connection, not the link:** each agent that connects with the link gets its own four digits. A second agent using a leaked link shows different digits, and the person says No. One Yes uses up the link.
7. **If Isoform isn't in front,** one notification asks the same ("{agent} is asking to join. Check the code."). It finishes the person's own action within its ten minutes, so it isn't a fourth notification kind (redesign v1.0.0 rule 22 stands).
8. **Joined:** "{agent} is connected" and "What should {agent} do for you?": **For code**, **For everything else**, or **Not now** ("It stays connected; nothing goes to it until you choose."). Choosing a job another agent holds says so: "Claude Code does your code work now. Codex takes over; Claude Code stays connected."
9. **Ran out:** "The link ran out" / "Nothing joined. Make a new link when you're ready." with Make a new link. **Said No:** "Not joined" / "You said it wasn't your agent. That link no longer works."

## 3. Joining, from the agent's side

Artboard 9, left. The agent asks three things, in its own words; the page (§4) tells it to.

10. **Where it works:** on this computer (a terminal or editor), in the cloud with a command line, in a chat app, or by text message. The person confirms.
11. **What it may open:** only what each hand-off sends, unless the person names more; for code, the repositories where it may work on new branches. Isoform shows this in §2's sheet and in the agent's row on Agents; the person can change it there.
12. **The code:** after it connects, it shows the four digits and waits for the person's yes in Isoform.
13. **Any command it runs is shown to the person first,** in the agent, the way agents already ask. Isoform never asks an agent to run a command without that.

## 4. The join page: isoform.ai/j/{code}

Artboard 10. One page, two readers. It never shows the person's name or anything of theirs.

14. **For a person who opens it in a browser** (10a): the mark and "Isoform"; "This link is for your agent."; "Paste the message into the agent you want to connect, like Claude, ChatGPT, Codex, Grok or Gemini. It reads this page and asks you three things:" then Where it works, What it may open, A code to check ("Your agent shows a code. Say yes in Isoform if it matches, never here."); the time left; "Didn't start this? Close the page. Nothing connects without your yes in Isoform."; and **What your agent reads ›**, which opens the agent's text below.
15. **For the agent** (10b), the same page carries this text, word for word (the build keeps each agent's exact command current; the words around it stay):

> **For the agent: join an Isoform**
>
> The person who gave you this link uses Isoform, a personal AI that runs on their own computer. Isoform finds work in their day. When they say yes to a piece of work, Isoform hands it to an agent they chose. They want that to be you.
>
> **Ask your person before you do anything here.** Nothing on this page is urgent, and nothing connects without their yes in Isoform.
>
> **1 · Say where you work.** Tell them in one line and ask if it's right: on their computer (a terminal or editor), in the cloud with a command line, in a chat app, or by text message.
>
> **2 · Ask what you may open.** Isoform sends you only what each task needs. Ask if there's anything more you may open. For code: the repositories where you may work on new branches. Never more than they name.
>
> **3 · Connect.**
> - Command line, here or in the cloud: add the tool server `isoform.ai/mcp?join={code}`. Show your person the command first.
> - Chat app: ask your person to add the Isoform connector in this app's settings, with the same address.
> - By text: nothing to add. Hand-offs come as texts from your person, each with a link to send the result back.
>
> **4 · Check the code.** When you connect, you'll get a four-digit code. Show it to your person: Isoform asks them whether you show the same one. Wait for their yes there.
>
> **What you'll get.** Each hand-off: a title, the task, what it gets (only these), what done looks like, and where to send the result.
>
> **Sending results back.** Use `post_result` with a one-line summary, the result and any files, or the report link in the hand-off. Anything that would send, post, buy, book or delete goes back as a draft.
>
> **Rules.** Do only the task, with only what it gets. Stop at a draft for anything that sends, posts, buys, books or deletes. Code goes on a new branch. Need another of your person's agents? Ask through Isoform with `ask_agent`; your person says yes first. Your person can disconnect you at any time.

16. **After the link is used or runs out,** the page says only "This link has been used or has run out. Ask your person for a new one." to both readers.
17. **Why it's written this way:** a careful agent should agree to it. It says who is asking and why, puts the person first, hides nothing, shows every command, and keeps the last yes inside Isoform. Nothing on it is phrased as an order to skip the person.

## 5. Isoform's tools for agents, and nothing else

Artboard 11, top right.

18. **Six tools:** `get_handoffs` (hand-offs waiting for you), `get_handoff` (one, with what it gets), `post_progress` (one line on how it's going, optional), `post_result` (a one-line summary, the text, files, and drafts that need a yes), `ask_agent` (ask another of your person's agents; waits for their yes), `get_answer` (that agent's answer, once it's there).
19. **No tool reads the person's memory, their day or their files.** An agent gets only what a hand-off sends. Answering others from what Isoform knows is Agent of You, later, by its own design.
20. **A token per agent and device,** made at the Yes (§2), ended by Disconnect at once; the tools refuse anything after that.

## 6. The hand-off, by kind of agent

Artboard 11, top. One sheet for every agent (phone-agent v1.0.0, computer-today rules 6 and 7: exactly what goes, What it gets, Done looks like). Only its button and what Isoform says afterwards change. This replaces "Send to {agent}" as the only button (computer-today rule 7, phone-agent v1.0.0's Send) and narrows "gets each hand-off directly" (phone-agent v1.1.0 rule 7) to the first two kinds.

| Kind | Button | What Isoform can say | How the result comes back |
| --- | --- | --- | --- |
| Runs on this computer (Claude Code, Codex, Cursor's agent), connected | **Send to {agent} ⌘↩**: Isoform starts it with the hand-off, on a new branch | Sent · Working, while its process runs · Back from {agent} | It calls `post_result`; Isoform also sees the branch |
| Runs in the cloud (Devin, Codex in the cloud), connected | **Send to {agent}**: started through the person's own account | Sent · Working, while its account says so · Back from {agent} | `post_result` |
| In a chat app with the connector (Claude, ChatGPT, Gemini) | **Open in {agent}**: its app opens with "Do my Isoform hand-off: {title}" written in; the person presses send there | Opened in {agent} · {agent} has it, once it reads the hand-off · Back from {agent}. Never "Working". | `post_result` through the connector |
| Not connected (by text, or an app without the connector) | **Write it in Messages** / **Open in {agent}** with the whole task written in, ending with the report link | "Written in Messages, {time}." and nothing more: Isoform can't know if it was sent | The report link (§7), by the agent or by the person |

21. **On the phone the same buttons apply,** except Runs on this computer, which reads **Send to {agent} on your computer** and waits for the computer if it's asleep ("Goes when your computer wakes.").

## 7. Status words, only from what Isoform knows

Artboard 11, middle.

22. **The ladder, each only when true:** "Sent · {time}" (always known); "{agent} has it" (it read the hand-off, or Isoform started it); "Working · {its own line}" (only while Isoform sees the process or the cloud account run it, or the agent sent `post_progress`); "Back from {agent}" (the result is in Your work, to judge); "No word from {agent} since {time}", after two hours, with **Ask again** and **Take it back**.
23. **Take it back** ends the hand-off: the agent's token can no longer read it, and the item returns to Your work as offered. Isoform says so and doesn't promise the agent stopped: "Taken back. Codex can't open it any more; anything it already made stays with Codex."
24. **Never an estimate, a spinner without words, or "Almost done".**

## 8. What the agent receives

Artboard 11, middle right. The hand-off as written on the sheet, nothing more:

25. **Its parts, in order:** "Isoform hand-off {id} · from {first name}'s Isoform · {date, time}"; the title; the task; "What it gets (only these)", numbered; "Done looks like"; "Rules: Stop at a draft for anything that sends, posts, buys, books or deletes."; and "Send the result: post_result("{id}", summary, result, files)" or the report link.
26. **What the person removed on the sheet isn't sent,** and Settings › Your data lists every hand-off with exactly what went, to which agent, and when (B4 extended stands).

## 9. The report page: isoform.ai/r/{token}

Artboard 11, right.

27. **For an agent without Isoform's tools, or for the person pasting a result:** the mark; "Send a result back"; "For: {title}"; "What you made, in one line"; "The result"; "Add files"; **Send to Isoform**; "Locked in this page so only that person's devices can read it. Works once, until {date}."
28. **Locked in the page:** the key travels in the link after the #, so the page seals what's sent before it leaves the browser and the relay can't read it, as with the phone's sealed notices. One use; seven days.
29. **After sending:** "Sent to Isoform. You can close this page." It never shows what Isoform did with it.
30. **The result arrives as "Back from {agent}"** in Your work, marked "sent through the report link", with Not mine if someone else sent it.

## 10. Agents asking agents

31. **`ask_agent` makes the request item** of computer-agents rule 7 ("{from} asks {to} · needs your yes"), on the computer and the phone, with exactly what would move between them. Yes sends the named thing to the other agent as a hand-off of its own; its answer goes back to the asker with `get_answer`, and both show on Agents. No ends it; the asker's `get_answer` says "Your person said no."
32. **A request from a not-connected agent can't happen:** only agents with Isoform's tools can ask.

## What the backend needs

These extend B5 (phone-agent v1.1.0 §4) and C1 to C3 (computer-map v1.0.0):

- **The join service:** one-use, ten-minute links per person and device; a four-digit code per connection attempt; the Yes on the person's device; a token per agent and device; Disconnect that ends it at once.
- **The tool server** (`isoform.ai/mcp`, through the relay to the person's computer) with exactly the six tools of §5, sealed between the agent and the person's devices where the agent's platform allows it.
- **Starting agents on this computer** with a hand-off on a new branch (C1), and through a cloud agent's own account where it offers that, with the process or account state as the only source of "Working".
- **The report page,** sealed in the browser, one use, seven days.
- **Each agent's kind** (§6) kept with the agent, so the sheet's button and the status words follow it.

## Every word

| Key | English | Where |
| --- | --- | --- |
| join.waiting | Waiting for your agent to join | Connect sheet |
| join.timeLeft | The link works once, for {n} more minutes. | Connect sheet |
| join.copyAgain | Copy again | Connect sheet |
| join.asking | {agent} is asking to join | Confirm |
| join.from | From {where} · to work in {what} | Confirm |
| join.showCode | Does your agent show this code? | Confirm |
| join.yes | Yes, it shows {code} | Confirm |
| join.onlyYes | Only Yes connects it. Disconnect any time in Agents. | Confirm |
| join.notify | {agent} is asking to join. Check the code. | Notification |
| join.connected | {agent} is connected | Joined |
| join.whatJob | What should {agent} do for you? | Joined |
| join.notNow | It stays connected; nothing goes to it until you choose. | Joined |
| join.takesOver | {current} does your {kind} work now. {agent} takes over; {current} stays connected. | Joined |
| join.ranOut | The link ran out | Ended |
| join.ranOutSub | Nothing joined. Make a new link when you're ready. | Ended |
| join.newLink | Make a new link | Ended |
| join.notJoined | Not joined | Ended |
| join.notJoinedSub | You said it wasn't your agent. That link no longer works. | Ended |
| jpage.title | This link is for your agent. | Join page |
| jpage.body | Paste the message into the agent you want to connect, like Claude, ChatGPT, Codex, Grok or Gemini. It reads this page and asks you three things: | Join page |
| jpage.code | Your agent shows a code. Say yes in Isoform if it matches, never here. | Join page |
| jpage.notYou | Didn't start this? Close the page. Nothing connects without your yes in Isoform. | Join page |
| jpage.agentReads | What your agent reads › | Join page |
| jpage.used | This link has been used or has run out. Ask your person for a new one. | Join page, after |
| agent.connected | Connected | Your agents |
| agent.notConnected | Not connected | Your agents |
| handoff.open | Open in {agent} | Sheet |
| handoff.writeMessages | Write it in Messages | Sheet |
| handoff.onComputer | Send to {agent} on your computer | Phone sheet |
| status.sent | Sent · {time} | Status |
| status.has | {agent} has it | Status |
| status.working | Working · {line} | Status |
| status.opened | Opened in {agent} · {time} | Status |
| status.written | Written in Messages, {time}. | Status |
| status.noWord | No word from {agent} since {time} | Status |
| status.askAgain | Ask again | Status |
| status.takeBack | Take it back | Status |
| status.takenBack | Taken back. {agent} can't open it any more; anything it already made stays with {agent}. | Status |
| report.title | Send a result back | Report page |
| report.for | For: {title} | Report page |
| report.oneLine | What you made, in one line | Report page |
| report.send | Send to Isoform | Report page |
| report.locked | Locked in this page so only that person's devices can read it. Works once, until {date}. | Report page |
| report.sent | Sent to Isoform. You can close this page. | Report page |
| report.via | sent through the report link | Your work |

## Proof

On named builds of Bo's computer and phone, with screen recordings:

1. **One message, one yes:** Bo pastes the join message into Codex; Codex asks the three things; the confirm sheet shows its four digits; Yes connects it.
2. **A leaked link fails:** the same link opened by a second agent before the Yes shows different digits; No ends the link, and the page then says it's used.
3. **Honest status:** a hand-off to a chat app with the connector never shows "Working"; one to Codex on the computer shows it only while the process runs.
4. **Nothing more than the hand-off:** Codex's tool list shows exactly the six tools; asking it to call anything else fails.
5. **Report page:** a result pasted at the report link arrives as "Back from Instinct" in Your work; the relay's log for it holds only sealed bytes.

## On the map (shell)

- JN1 · Connected and Not connected are the only agent states; the words never claim more.
- JN2 · The join link: one use, ten minutes; a four-digit code per connection; the last yes in Isoform; ran out and No end it.
- JN3 · The join page: one page for a person and for the agent, word for word, never the person's name.
- JN4 · Isoform's six tools for agents, and nothing that reads the person's memory, day or files.
- JN5 · The hand-off by kind: Send, Open in, Write it in Messages; status only from what Isoform knows; Take it back.
- JN6 · The report page, sealed in the browser, one use, seven days.
