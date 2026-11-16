# Phone agent v1.2.0: your agents on the phone after agents-join; requests, results with files, the hand-off by kind, the code, the lock screen

Version 1.2.0 · written November 15, 2026, 8:47 PM Pacific, by Claude Design.

**This file changes phone-agent v1.1.0 where it says so; read v1.0.0, v1.1.0 and agents-join v1.0.0 first.** agents-join v1.0.0 set what an agent reads, how it joins, and what Isoform may say about it. This file draws what that means on the phone.

**Board:** Isoform iPhone (https://claude.ai/artifact/WKCdoNzqXTrsGG4aerRFrs), new artboard 11 (Your agents on the phone), frames 1 to 7.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Judging stays one tap** for results and for agents' requests.
2. **The hand-off's button says what will really happen,** and the phone never claims what it can't see.
3. **The lock screen gets only what's worth it:** finished work, and a join you just started.

## 1. Your agents asking each other (frame 1)

1. **A request is an item in Your work,** the same as on the computer (computer-today v1.1.0 rules 7 and 8): "{from} asks {to} · {time}", the sentence, "What would move:" in the grey box, "Yes lets {to} do it. It waits until {time} tomorrow, then ends.", **Yes** and **No**. No Not mine, no Edit.
2. **It counts in "{n} to answer"** and in the brief's count. **It never sends a notification.** After a day it ends by itself, with one Done today line: "Codex's request to Muse ran out · nothing moved".

## 2. A result, with its file (frame 2)

3. **The result item stands as phone-agent v1.0.0 rule 16,** with the file as a chip under the summary: its name, where it lives ("branch halcyon/test-plan, on your computer"), and **Read it** for text (Markdown, plain text, a draft), which opens it read-only here; anything else says **Open on your computer**, which opens it there (B3).
4. **Where a file lives is always said.** A file is never copied to the phone unless it's text and you open it.

## 3. The hand-off by kind (frames 3 and 4)

This replaces "Send to {agent}" as the phone's only button (phone-agent v1.0.0 rule 13, v1.1.0 rule 7) with agents-join §6:

5. **An agent that runs on the computer:** **Send to {agent} on your computer**, with "Codex works on a new branch. The result comes back here." With the computer asleep, one amber line above the button: "Your computer is asleep. This goes when it wakes; until then it waits under With your agents." The row then reads "Waiting for your computer".
6. **An agent in its own app, connected:** **Open in {agent}**, with "{agent} opens with it written in. Press send there; it reads the rest from Isoform." Afterwards the row reads "Opened in {agent} · {time}", then "{agent} has it" once it reads the hand-off. Never "Working".
7. **A cloud agent:** **Send to {agent}**, started through its own account (agents-join §6).
8. **Not connected:** **Write it in Messages** (phone-agent v1.1.0 rule 10 stands) or **Open in {agent}** with the whole task written in and the report link at the end.

## 4. The last yes, on the phone (frame 5)

9. **When the join link was made on the phone,** the code is checked here as a sheet: "{agent} is asking to join" / "From {where} · to open {what}" / "Does your agent show this code?" / the four digits, large / **Yes, it shows {code}** / **No** / "Only Yes connects it. Disconnect any time in Settings › Your agents." (agents-join §2). Then "What should {agent} do for you?" as on the computer.

## 5. The lock screen (frame 6)

10. **Finished work, when you're away from the computer** (redesign v1.0.0 rule 22's third kind): "Back from {agent}" / its one sentence, with **Read it** and **Useful**. Useful from the notification counts as the judgment.
11. **A join asking for its code,** only within the ten minutes after you made the link on this phone: "{agent} is asking to join" / "Check the code in Isoform." It finishes your own action; it isn't a fourth kind (agents-join rule 7).
12. **Nothing else about agents notifies:** not a request between agents, not "has it", not "no word since".

## 6. Settings › Your agents (frame 7)

13. **The two jobs on top,** For code and For everything else, each with its agent; then Other agents; then Connect an agent; then Sent to your agents.
14. **Each row says Connected or Not connected,** where it runs ("on your computer", "in its app", "by text message"), and when it last worked. Disconnected agents fade, with **Connect again**, for 30 days. The footer: "Connected agents send results back by themselves. For the others, Isoform writes the task in and the result comes back through a report link."

## Every word

phone-agent v1.0.0 and v1.1.0 keys stand except agent.send where §3 replaces it.

| Key | English | Where |
| --- | --- | --- |
| agent.sendOnComputer | Send to {agent} on your computer | Sheet |
| agent.asleepLine | Your computer is asleep. This goes when it wakes; until then it waits under With your agents. | Sheet |
| agent.waitingComputer | Waiting for your computer | With your agents |
| agent.newBranch | {agent} works on a new branch. The result comes back here. | Sheet |
| agent.openIn | Open in {agent} | Sheet |
| agent.openInFoot | {agent} opens with it written in. Press send there; it reads the rest from Isoform. | Sheet |
| agent.openedIn | Opened in {agent} · {time} | With your agents |
| agent.hasIt | {agent} has it | With your agents |
| agent.readIt | Read it | File chip |
| agent.openOnComputer | Open on your computer | File chip |
| agent.fileWhere | {where}, on your computer | File chip |
| agent.requestRanOut | {from}'s request to {to} ran out · nothing moved | Done today |
| notif.back | Back from {agent} | Lock screen |
| notif.joinAsk | {agent} is asking to join | Lock screen |
| notif.joinAskBody | Check the code in Isoform. | Lock screen |
| settings.agentsFoot | Connected agents send results back by themselves. For the others, Isoform writes the task in and the result comes back through a report link. | Settings › Your agents |

## Proof

On Bo's iPhone, a named TestFlight build, with his computer:

1. **Asleep, honestly:** with the computer asleep, Send to Codex on your computer shows the amber line; the row says Waiting for your computer; when the computer wakes, it becomes Codex has it. A recording across the wake.
2. **Never Working for an app:** a hand-off to an agent in its own app goes Opened in, then has it, then Back from; no frame shows Working.
3. **Lock screen:** over one day, the only agent notifications are Back from and a join Bo started; a request between agents arrives silently in Your work.
4. **Read it:** a Markdown result opens read-only on the phone; a .zip says Open on your computer.

## On the map (phone)

- P251 · Agents' requests on the phone: an item with Yes and No, waits a day, counted in "to answer", never a notification.
- P252 · A result's file: Read it for text, Open on your computer for anything else; where it lives is always said.
- P253 · The phone's hand-off by kind: Send to {agent} on your computer (asleep: one amber line), Open in {agent}, Write it in Messages; never Working for an app.
- P254 · The join code checked on the phone, when the link was made there.
- P255 · Lock screen for agents: Back from (Read it, Useful) and a join you started; nothing else.
- P256 · Settings › Your agents: the jobs on top; Connected or Not connected, where it runs, last worked; Connect again for 30 days.
