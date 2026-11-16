# Computer map v1.0.0: the computer app simplified to the phone's ranking, with Agents, where your agents connect once and meet you

Version 1.0.0 · written November 15, 2026, 5:04 PM Pacific, by Claude Design.

**Bo, November 15, after the phone pass:** "We should also redesign the desktop too, given your redesign of the iPhone app. For the desktop app, I think we can simplify as well. Uses just be a little bit more powerful for iPhone app, really. Really look into the website station.site, how they're able to connect each things. I think that's very cool. We should able to learn how to do that as well and work on that."

**So:** the computer is the phone's app with more room and more power (the keyboard, every source, the whole day's activity, and your agents connected here), ranked the same way. Station's idea, agents joining once and passing work through a room instead of through you, becomes a page: Agents.

**Read with:** phone-map v1.0.0 (the shared item, answers, words, the hub's input on Tasks and Chat) and phone-agent v1.1.0 (your agents, connect once, agents you text). Those rules hold on the computer; this pass says what's different. It replaces the computer's page structure in redesign v1.0.0 and redesign-work v1.0.0 (Tasks), and phone-map v1.0.0 §6.

**What I checked:** Station ([station.site](https://station.site)) is "meeting rooms for your agents": you paste one message into an agent (it lists Claude, Grok, Muse, Gemini, Devin, Codex, Cursor, GitHub Copilot, and any agent that can run a command), it asks three questions and joins; an agent asks its owner before acting on another agent's request; work is posted back to the room as files; it calls the copy and paste it removes "the human clipboard".

**Board:** Isoform Computer, a Design canvas (https://claude.ai/artifact/4E18TrYUYKfQHqTQdtfoPn), eight artboards: 1 the map; 2 Today; 3 Hand it to your agent; 4 Agents; 5 Connect an agent; 6 Chat and Quick chat; 7 Activity and You; 8 Settings and the menu bar. The app's dark colours; invented content; English only.

A change goes in a new file with a new version; this one isn't edited after it's written.

## The files of this pass

| File | Page |
| --- | --- |
| computer-map v1.0.0 (this) | The ranking, the sidebar, what goes, setup, the backend items |
| computer-today v1.0.0 | Today: the brief, Your work, the keyboard, the hand-off |
| computer-agents v1.0.0 | Agents: your agents, between you and them, what they made, connect |
| computer-chat v1.0.0 | Chat and Quick chat |
| computer-activity v1.0.0 | Activity |
| computer-you v1.0.0 | You |
| computer-settings v1.0.0 | Settings and the menu bar |

## 1. The computer, ranked

Nothing lower may block a higher one: not its loading, not its errors, not its space on the screen.

1. **The latest brief shows up:** the top of Today, with Hear it.
2. **Judging work:** Your work on Today, with the keyboard (Y, N, M, E). A Yes on offered work goes to your agent.
3. **Asking and correcting:** Chat and Quick chat.
4. **Your agents:** connected once; what they have, what they asked each other (for your Yes), what they made.
5. **What it saw and what it thinks:** Activity, with Forget on every row; You, every line a claim to judge.
6. **Trust and sources:** Closed Eyes on every device; What Isoform sees, every source on one page.

## 2. The sidebar

Artboard 1.

1. **Five places:** Today, Chat, Agents, Activity, You. Counts only on Today (to answer) and Agents (waiting for your Yes).
2. **At its foot:** the phone and the watch in one line each ("Phone · connected", "Watch · listening", in the mark's pose), Closed Eyes as a button with ⌘⇧E, and Settings.
3. **⌥Space** opens Quick chat over any app; **⌘K** finds anything (a command, not a page).

## 3. What goes

Before: main's sidebar (fbc8050e0) is Search ⌘K, Today, Chat, Activity, Goals, Library (Upcoming, One-time work, Playbooks, Deliverables), Pinned, Custom Groups, the list of sessions, New chat, and You; redesign v1.0.0 added Tasks, Your topics and three You pages.

4. **The Tasks page.** Its work is Your work on Today; its agents' side (what's out, what came back, files) is Agents.
5. **Three You pages** (About you, How you do things, Who you know) become one You page with tabs.
6. **Search as a sidebar row.** ⌘K stays, and finds what redesign-work v1.0.0 rules 17 to 20 say, from any page.
7. **The list of sessions, Pinned, Custom Groups and New chat.** Chat is one thread (computer-chat v1.0.0). A session with a coding agent is a hand-off and lives in Agents; a meeting is a conversation in Activity.
8. **Library.** One-time work and Deliverables are Agents (what's out, Made by your agents); Upcoming, Playbooks and Goals stay off behind their switch (redesign v1.0.0 rule 33).
9. **Work on it as copy and paste.** Connected agents get the work directly; copy and paste stays only for agents that can't connect (phone-agent v1.1.0 rule 12).
10. **The Topics page.** A topic opens from its item (its page as on the phone, phone-today rule 17); its proposals are in Your work.
11. **Settings › Thinks with** becomes Settings › Your agents (computer-settings v1.0.0), and the agents themselves are on Agents.

## 4. Setup on the computer

12. **The same four screens as the phone's** (phone-setup v1.1.0) before any permission: the brief, saying yes and your agent does it, the watch (labelled With Apple Watch), yours and on your devices. Then permissions one at a time, each asked when its screen explains it (redesign-shell v1.0.0's setup stands for the permissions).
13. **Then Your agents:** "Found on this computer: Claude Code, Codex" with Connect, and Skip (computer-agents v1.0.0 rule 15). Then pair a phone.

## 5. What the backend lacks

The phone pass's B1 to B13 (phone-map v1.0.0) hold; B5 is phone-agent v1.1.0's. New for the computer:

| # | What | Needed by |
| --- | --- | --- |
| C1 | **Agents on this computer found:** which coding agents are installed (Claude Code, Codex, Cursor), and a way to connect each with the person's OK (register Isoform where the agent reads its tools, so it receives hand-offs and posts results). | Agents |
| C2 | **Agents asking agents:** a connected agent can ask another of the person's agents for something through Isoform; the request waits as an item for the person's Yes; only what the request names moves between them. | Agents, Today |
| C3 | **What agents made:** results and files from every agent, with who made them, for whom, and how the person judged them. | Agents |
| C4 | **Learn from them:** per agent, off by default, read its own history (sessions, files it changed) as a source, with Forget, like the screen. | Agents, Settings |
| C5 | **Messages as a source on the computer:** observation-sources-plan item 11 as written (chat.db, opt-in behind Full Disk Access, people you know only, its own consent), shown as one switch, off until turned on. Today the Messages parser reads only the window on screen. | Settings |
| C6 | **Your work on the computer from the same list as the phone** (B2): the same ids, states and answers, with keyboard focus. | Today |

## Proof

On Bo's computer, a named desktop build, against a screenshot of the sidebar and pages before (main's sidebar, §3, and walk-1113's Tasks and You pages):

1. **Five places:** the sidebar shows Today, Chat, Agents, Activity, You, and none of Search, Goals, Library, Pinned, groups, the session list or Tasks.
2. **No clipboard:** Bo connects Codex from Agents with one click, says Yes on a code proposal, and its result comes back to Your work without copying anything. A screen recording.
3. **Agents asking agents:** a request from one agent to another waits for Bo's Yes, on the computer and the phone.
4. **The brief first:** with the network off, Today opens on the latest brief within a second.

## On the map (shell)

- SH5 · The sidebar: Today, Chat, Agents, Activity, You; the phone and watch, Closed Eyes and Settings at its foot.
- SH6 · Gone from the sidebar: Search (⌘K stays), Goals, Library, Pinned, groups, the session list, New chat, Tasks.
- SH7 · Setup on the computer: the phone's four screens, permissions one at a time, then Your agents.

The other files list their rows: SH1 to SH4 (computer-today), AG1 to AG6 (computer-agents), CH1 to CH3 (computer-chat), AC1 to AC3 (computer-activity), YO1 and YO2 (computer-you), ST1 to ST4 (computer-settings).
