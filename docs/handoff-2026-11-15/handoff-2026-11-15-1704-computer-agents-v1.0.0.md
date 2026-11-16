# Computer agents v1.0.0: Agents, the room where your agents meet you; connect once, they ask each other through you, and what they make comes back

Version 1.0.0 · written November 15, 2026, 5:04 PM Pacific, by Claude Design.

**Part of the computer pass of November 15.** Read computer-map v1.0.0 and phone-agent v1.1.0 first.

**Bo:** "Really look into the website station.site, how they're able to connect each things. I think that's very cool. We should able to learn how to do that as well and work on that." Earlier: "We're able to send everything to different coding agents or personal agents. This is amazing."

**What we take from Station, and what we don't:**
- **Taken:** connect an agent once with one pasted message; the agent asks its owner before it acts on another agent's request; results come back as files with who made them; no human clipboard.
- **Not taken, for now:** rooms shared with other people. Isoform is one person's (multiplayer is Agent of You, later). The room is you and your agents.
- **Added, from Isoform's own direction** (Bo, November 3: let people use every personal agent, with Isoform grabbing the latest context from them): an agent's own history can become signal, per agent, off until turned on.

**Board:** Isoform Computer, artboards 4 (Agents) and 5 (Connect an agent).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Requests waiting for your Yes:** an agent asking another, judged first.
2. **What each agent is doing now,** and what came back.
3. **What they made,** with how you judged it.
4. **Connecting an agent** takes one click on this computer, or one message anywhere.
5. **Learning from them,** only where you turned it on.

## The page

1. **Title "Agents"; under it:** "Your agents meet here: what you handed them, what they asked each other, and what they made."
2. **Three columns.**

### Your agents (left)

3. **One row per agent:** its badge, name, a dot (green while working), its job ("For code · on this computer", "For everything else", "Connected", "By text message · not connected"), and what it's doing ("Working: the test plan", "Asked by Codex, 6:24 PM").
4. **Connect an agent** under the list.
5. **Clicking an agent** narrows the middle column to it, with Disconnect, Change its job, and Learn from it.

### Between you and them (middle)

6. **Today, newest first, as a timeline:** your hand-offs ("You → Codex · Yes on "Write the test plan". What went ›"), their results ("Codex is back · The test plan is ready: 14 cases. It's in Your work for you to judge." with the file), and what they passed each other ("Muse → Codex · Shared a summary of 212 replies on X about the pricing tiers." with the file and "used by Codex").
7. **An agent asking another waits for your Yes,** ringed, at its time: "Codex asks Muse · needs your yes" / "Codex wants Muse to read what people on X say about the pricing tiers, and share a summary with it." / "Yes lets Muse do it and hand the summary to Codex. Nothing else moves between them." with Yes (Y) and No (N). The same request is an item in Your work on the computer and the phone (source line "Codex asks Muse").
8. **The human clipboard goes:** agents pass work through Isoform, only what each request names, never through you and never directly to each other.

### Made by your agents (right)

9. **This week's files and results,** each with who made it, for whom, and how you judged it: "test-plan.md · Codex · Useful?", "x-pricing-replies.md · Muse · for Codex", "dinner-options.md · Muse · Not useful". Clicking opens it; files open on this computer.
10. **Learned from them:** what Isoform read from each agent's own history where Learn from it is on ("Codex: 1 h 40 min on halcyon-import today, two branches."), with "From each agent's own history, only where you turned Learn from it on." Each line opens Activity › Agents, with Forget.

## Connect an agent

Artboard 5.

11. **A sheet:** "Connect an agent" / "Once connected, a Yes goes straight to it and the result comes back to Your work. It sees only what each hand-off sends."
12. **On this computer:** the agents found installed (Claude Code, Codex, Cursor), each "Found on this computer" with **Connect**, or "Connected". Connect asks once ("Connect Claude Code? It will receive the work you say yes to, and post results back. It sees only what each hand-off sends.") and sets it up (C1).
13. **Any other agent:** the one message ("Join my Isoform: read isoform.ai/j/{code} and follow the steps there.") with Copy, and "Paste it into Claude, ChatGPT, Grok, Gemini, Muse, Devin, or any agent that can read a link and run a command. It asks which device it works on, what it may open, and to confirm you; then it joins. The link works for 10 minutes." The same as the phone (phone-agent v1.1.0 §2).
14. **Learn from what they do:** "Read each connected agent's own history (sessions, files it changed), like your screen. Off until you turn it on, per agent." It's a source: it shows in Settings › What Isoform sees and in Activity, and Closed Eyes stops it.
15. **In setup,** after permissions: the On this computer list, with Skip (computer-map rule 13).
16. **Sessions with a coding agent** (main's session list) aren't Isoform's to show: the agent's own app keeps them. Isoform shows what it handed over, what came back, and what it learned, if allowed.
17. **What a hand-off asks for** follows redesign v1.0.0 rule 10: read, research, compare, summarise, draft, or write code on a new branch, and stop at a draft for anything that sends, posts, buys, books or deletes, which comes back to Your work for your Yes. Isoform can't enforce this inside the agent's own account, so the hand-off says it in words and the agent's own confirmations still apply.

## What the build needs

C1 (find and connect agents on this computer), C2 (agents asking agents, with the person's Yes), C3 (what agents made), C4 (learn from them), B4 and B5 (phone-agent v1.1.0).

## Every word

| Key | English | Where |
| --- | --- | --- |
| agents.title | Agents | Page |
| agents.sub | Your agents meet here: what you handed them, what they asked each other, and what they made. | Page |
| agents.yours | Your agents | Column |
| agents.forCodeHere | For code · on this computer | Row |
| agents.working | Working: {title} | Row |
| agents.askedBy | Asked by {agent}, {time} | Row |
| agents.between | Today, between you and your agents | Column |
| agents.youTo | You → {agent} | Timeline |
| agents.isBack | {agent} is back | Timeline |
| agents.passed | {from} → {to} | Timeline |
| agents.asks | {from} asks {to} · needs your yes | Request |
| agents.asksYes | Yes lets {to} do it and hand {what} to {from}. Nothing else moves between them. | Request |
| agents.made | Made by your agents | Column |
| agents.learned | Learned from them | Column |
| agents.learnedFoot | From each agent's own history, only where you turned Learn from it on. | Column |
| agents.found | Found on this computer | Connect |
| agents.onThis | On this computer | Connect |
| agents.anyOther | Any other agent | Connect |
| agents.connectAsk | Connect {agent}? It will receive the work you say yes to, and post results back. It sees only what each hand-off sends. | Confirm |
| agents.learn | Learn from what they do | Connect, per agent |
| agents.learnSub | Read each connected agent's own history (sessions, files it changed), like your screen. Off until you turn it on, per agent. | Connect |

## Proof

On Bo's computer, a named desktop build, with Codex and one personal agent connected:

1. **One click:** Codex found and connected from the sheet; a recording.
2. **No clipboard:** a hand-off, the result, and the file in Made by your agents, with nothing copied.
3. **Your Yes between agents:** a request from Codex to the personal agent waits, ringed; No stops it, and nothing passes.
4. **Learn from it, off:** with it off, Activity › Agents shows only hand-offs; turned on for Codex, its session shows, with Forget.

## On the map (shell)

- AG1 · Agents: your agents, each with its job and what it's doing.
- AG2 · Between you and them: hand-offs, results, files passed, as a timeline.
- AG3 · An agent asking another waits for your Yes, there and in Your work.
- AG4 · Made by your agents, with how you judged each.
- AG5 · Connect an agent: found on this computer with one click; the message for any other.
- AG6 · Learn from them, per agent, off until turned on; a source with Forget.
