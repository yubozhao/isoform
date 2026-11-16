# Phone agent v1.1.0: your agents, for code and for everything else; connect an agent once with one message; agents you text get the task in Messages

Version 1.1.0 · written November 15, 2026, 4:49 PM Pacific, by Claude Design.

**This file changes phone-agent v1.0.0; read that first.** Everything in v1.0.0 stands except what this file replaces: the first-time choice (v1.0.0 rules 11 to 13), how Send reaches an agent, and the stand-in (rules 19 to 21), which now applies only to agents that aren't connected. Backend item B5 in phone-map v1.0.0 is replaced by §4 here.

**Bo, November 15, after the phone pass:**
- "We're able to send everything to different coding agents or personal agents. This is amazing."
- "We probably also need to add more. We'll have Claude, ChatGPT, Grok, Codex, Muse, Instinct, stuff like that."
- "Make sure we have message access on iPhone app."
- On the desktop pass: "Really look into the website station.site, how they're able to connect each things."

**How I read "message access":** some personal agents have no app and are reached by text message (Instinct, per its coverage below), so the phone must be able to hand work over through Messages. If Bo meant Isoform reading his messages for signal: iOS doesn't let an app read Messages, so that would be a computer feature; it isn't in this file.

**What I checked** (November 15): Meta's Muse is a personal agent with an iPhone app and WhatsApp access ([Built In](https://builtin.com/articles/meta-muse)). Instinct is a personal agent with no app, reached by text message, call or WhatsApp, invite-only ([Built In](https://builtin.com/articles/instinct-ai)). Station ([station.site](https://station.site)) connects people's agents in shared rooms: you paste one message into an agent (it lists Claude, Grok, Muse, Gemini, Devin, Codex, Cursor, GitHub Copilot, "and any agent that can run a command"), the agent asks three questions and joins, asks its owner before acting on another agent's request, and posts its work back; it calls the copy and paste it removes "the human clipboard".

**Board:** Isoform iPhone, artboard 4, frames 2, 6 and 7.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

Unchanged from v1.0.0, with one added at 3:
1. The Yes is recorded on the first tap.
2. Exactly what goes is seen before it goes.
3. **Once connected, an agent gets the work directly and reports back:** no copying, no pasting.
4. One tap to send.
5. The result comes back and is judged.
6. While it's out, it's visible.

## 1. Your agents: two, by the kind of work

Frame 2. Replaces v1.0.0 rules 11 to 13.

1. **Work comes in two kinds, so the person picks two agents:** one **for code** (work that touches a repo) and one **for everything else** (research, drafts, comparisons, plans). Isoform picks between them by the work; the hand-off says which, and Change agent picks another for that one hand-off.
2. **The first Yes on offered work asks once:** "Who does your work?" / "Isoform finds the work and you judge it. The doing goes to agents you already use, each in its own account." Two groups:
   - **For code:** Codex, Claude Code, Cursor (each "On your computer");
   - **For everything else:** Claude, ChatGPT, Grok, Gemini, Muse, Instinct.
   - Each row says how it's reached and whether it's connected: "Connected", "Connected · on your computer", "Its app", "By text message".
   - **Connect an agent** at the bottom, then Done.
   - The board's frame 2 shows seven of them to fit the screen; the build lists all nine, scrolling.
3. **The list is open.** It shows the agents Isoform knows how to reach; any other agent that can read a link and run a command is added with Connect an agent (§2), under the name it gives itself.
4. **Settings › Your agents** (it was "Your agent") shows the two choices and every connected agent, each with "Connected · last worked 4:10 PM" and Disconnect.

## 2. Connect an agent once, with one message

Frame 7. New; learned from Station.

5. **Connect an agent** opens a sheet: "Once connected, a Yes goes straight to it and the result comes back here. No copying, no pasting." Then three steps:
   - **1 · Copy this message:** "Join my Isoform: read isoform.ai/j/{code} and follow the steps there." with **Copy**;
   - **2 · Paste it into your agent:** "Claude Code, Codex, Claude, Grok, Muse, Gemini, Cursor, Devin, or any agent that can read a link and run a command.";
   - **3 · Answer its questions:** "It asks which device it works on, what it may open, and to confirm you. Then it joins."
6. **While it waits:** "Waiting for your agent to join · the link works for 10 minutes". When it joins: "{agent} is connected" and the agent appears in Your agents.
7. **A connected agent gets each hand-off directly** when you press Send: the task, What it gets, and a place to post its result. Nothing else of yours is reachable by it: it sees only what each hand-off sends.
8. **Its result comes back to Your work** as "Back from {agent}" (v1.0.0 rule 16), with any file it made ("pricing-page-copy.md · Open on your computer").
9. **Disconnect** in Settings › Your agents ends it at once; the join link can't be reused.

## 3. Agents you text: the task is written in Messages

Frame 6. New.

10. **For an agent reached by text message** (Instinct today), Send opens Messages with the agent as the recipient and the task written, ending with the report link. **The person presses send;** Isoform never sends a text by itself.
11. **Isoform can't read Messages on the phone,** so the result comes back through the report link when the agent uses it, or through the next morning's check (v1.0.0 rule 20).
12. **An agent with its own app and no connection** (ChatGPT today) gets the task through its app when it accepts one, or copied with "Copied. Paste it in {agent} and send." (v1.0.0 rule 19); the morning check stands for these.

## 4. What the backend needs (replaces B5)

- **B5 · Agents join your Isoform.** A join link per person, valid 10 minutes, that tells an agent how to join (the three questions, then a token tied to the person and the device it runs on); a channel through the relay where a joined agent receives hand-offs and posts results and files; Disconnect that revokes the token. Until it's live, every agent uses §3 or rule 12.
- **B4, extended.** Two choices (for code, for everything else) instead of one; the work's kind decides which; each hand-off kept with exactly what went, for Your data.

## 5. What I'd add next, from Station, not in this file

13. **Your agents asking each other,** with you approving. Station's best idea is an agent asking another for help ("ask Ana's Grok Bot what people on X say about pricing"). In Isoform it would be your code agent asking your personal agent for something, through Isoform, and the request is an item in Your work: "Codex asks Muse for the X replies about pricing. Yes?" That's a judgment, and it keeps the human clipboard gone between agents too. The computer pass (next) draws it on the desktop's Agents page first; the phone gets it once that's settled with Bo.

## Every word

| Key | English | Where |
| --- | --- | --- |
| agents.pick.title | Who does your work? | First time |
| agents.pick.body | Isoform finds the work and you judge it. The doing goes to agents you already use, each in its own account. | First time |
| agents.forCode | For code | Group |
| agents.forElse | For everything else | Group |
| agents.connected | Connected | Row |
| agents.connectedComputer | Connected · on your computer | Row |
| agents.onComputer | On your computer · not connected | Row |
| agents.itsApp | Its app | Row |
| agents.byText | By text message | Row |
| agents.connect | Connect an agent | Row, sheet title |
| agents.connect.body | Once connected, a Yes goes straight to it and the result comes back here. No copying, no pasting. | Sheet |
| agents.connect.s1 | Copy this message | Step |
| agents.connect.msg | Join my Isoform: read isoform.ai/j/{code} and follow the steps there. | Message |
| agents.connect.s2 | Paste it into your agent | Step |
| agents.connect.s2body | Claude Code, Codex, Claude, Grok, Muse, Gemini, Cursor, Devin, or any agent that can read a link and run a command. | Step |
| agents.connect.s3 | Answer its questions | Step |
| agents.connect.s3body | It asks which device it works on, what it may open, and to confirm you. Then it joins. | Step |
| agents.connect.waiting | Waiting for your agent to join · the link works for 10 minutes | Sheet |
| agents.connect.done | {agent} is connected | Sheet |
| agents.lastWorked | Connected · last worked {time} | Settings › Your agents |
| agents.disconnect | Disconnect | Settings › Your agents |
| set.agents | Your agents | Settings row (was Your agent) |

## Proof

On Bo's iPhone and computer, named builds. **Before:** phone-agent v1.0.0's single choice, and the Work on it sheet (copy and paste).

1. **Connected once:** Bo pastes the join message into Codex; it asks its questions and appears in Your agents as Connected. A screen recording.
2. **No clipboard:** a Yes on a code proposal reaches Codex with no copy and paste, and its result comes back to Your work as "Back from Codex".
3. **By text:** a Yes for Instinct opens Messages with the task written; nothing is sent until Bo presses send.
4. **Two kinds:** a code proposal goes to the code agent and a research proposal to the other, without Bo choosing each time.

## On the map (phone)

- P246 · Your agents: one for code, one for everything else; the list names Claude, ChatGPT, Grok, Gemini, Muse, Instinct, Codex, Claude Code, Cursor, and is open (replaces P225).
- P247 · Connect an agent once with one pasted message; a connected agent gets hand-offs directly and reports back.
- P248 · An agent you text: Messages opens with the task written; the person sends it.
