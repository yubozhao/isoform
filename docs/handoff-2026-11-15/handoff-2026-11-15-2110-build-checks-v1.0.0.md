# Build checks v1.0.0: for each of the eight build sessions, what changed tonight, the frames I need to review, and what I'll check first

Version 1.0.0 · written November 15, 2026, 9:10 PM Pacific, by Claude Design.

**Why this file:** the hub started eight build sessions from the phone and computer passes at about 5:45 PM (PR #1105 mirrors them). Since then I've filed fifteen more handoffs, several of which change what those sessions are building. I can't open the queued PRs from here (#1106, #1108, #1109, #1110: no GitHub access, no fetch). So each session gets one section: the files to read now, in order; the frames to send with a PR; and the checks I'll make first, which they can make themselves.

A change goes in a new file with a new version; this one isn't edited after it's written.

## For every session

1. **Read tonight's files that touch you before the next commit** (each section names them). Where a later file changes an earlier one, the later one wins, and it says which rule.
2. **Send frames with each PR:** named build, light and dark, at the states listed, from the app (the harness only where the app can't reach the state). One still per state; a recording where the section asks.
3. **Words come from the files' Every word tables,** with agent-words v1.0.0 and v1.0.1 applied ("your agents" is work agents; Agent of You by name; a yes "goes to", never "straight to").
4. **Colours are tokens** (computer-theme v1.0.0, phone-theme v1.0.0); no hex outside index.css and Theme.swift.
5. **No left-edge bars, no white on #7a8aff, nothing under 44 points on the phone** (AGENTS.md rule 12; pass-access v1.0.0).

## 1. Phone backend

- **Read now:** agents-join v1.0.0 (the protocol, the six tools, status only from what Isoform knows), phone-agent v1.2.0 §3 (the hand-off by kind), brief-agents v1.0.0 (what the brief package carries about agents).
- **Frames:** none; logs instead: a hand-off's record with exactly what went; a status sequence for one agent of each kind.
- **I'll check first:** (a) "Working" is set only from a process or a cloud account, never for an agent in its own app; (b) Take it back revokes the agent's read at once; (c) the brief package (B1) carries results, requests and no-word hand-offs since the last brief; (d) the relay holds only sealed bytes for the report page.

## 2. Phone Today, brief and setup

- **Read now:** phone-agent v1.2.0 (requests and results as items; the file chip), brief-agents v1.0.0 (the "Your agents:" paragraph), pass-access v1.0.0 (stacked answers at large text; compact items 44 points, not 38), phone-theme v1.0.0 (dark).
- **Frames:** Today with a result as the open item; Today with an agents' request; the brief in full with the agents paragraph and its two compact items; the four setup screens; all at the largest accessibility size once, and in dark.
- **I'll check first:** (a) one answer counts in the brief and in Your work; (b) a request has Yes and No only; (c) compact items measure 44; (d) the brief never says an agent is "working on" something.

## 3. Watch tab

- **Read now:** phone-theme v1.0.0 rule 4 (the Closed Eyes gap is a striped dot, not a left-edge bar), pass-questions v1.0.0 §3 (Listen on your watch: off only, at once; on from the watch), pass-access v1.0.0 rules 11 to 13 (Reduce Motion, the mark's labels).
- **Frames:** the tab with a Closed Eyes hour; Listen on your watch just turned off; turning it on from the phone; dark.
- **I'll check first:** (a) no bar at a row's left edge; (b) "Off. Your watch stops when it next syncs; nothing heard from now is kept." and the computer drops audio after that moment (a log); (c) the mark is still with Reduce Motion on.

## 4. Agents (computer, and the phone's Your agents)

- **Read now, in this order:** agents-join v1.0.0, computer-agents v1.1.0 (every state; Claude in its app never "Working"), agent-words v1.0.1, computer-setup v1.0.0 step 4.
- **Frames:** the connect sheet; the code confirmation; Joined with the job choice; Agents with none connected (the full-size sheet); a quiet day; each trouble state of artboard 13c; one agent opened; Disconnect's confirmation. A recording of Codex joining from the pasted message.
- **I'll check first:** (a) the green dot only where Isoform can see work; (b) a second agent on the same link shows different digits; (c) the join page's agent text is word for word; (d) Isoform's tool list is exactly six tools.

## 5. Chat

- **Read now:** computer-chat v1.1.0 (Reading chips; couldn't answer after 30 seconds; Undo in the thread; Quick chat's ready work), pass-questions v1.0.0 §4 (the brief card leaves Chat on both devices).
- **Frames:** an answer while it's reading; "I don't know"; couldn't answer; a correction and its undo; Quick chat over Mail with the screen chip, and over Isoform without it; a task-like ask getting the hand-off card.
- **I'll check first:** (a) no brief card in Chat; (b) no dots or "Thinking…" while answering, only the sources; (c) no retry by itself after a failure; (d) the hand-off card opens the sheet, never runs work from Chat.

## 6. Sidebar, Settings and the menu bar (#1110 is the sidebar)

- **Read now:** pass-questions v1.0.0 §1 and §2 (⌘⌃⌥E everywhere; the watch line's still pose from mark-poses.json), computer-settings v1.0.1 (the popover keeps the next item with Yes and No) and v1.1.0 (Agent of You back in Settings, after Your agents, each with its line), computer-today v1.1.0 rule 14 (what the counts mean), computer-theme v1.0.0.
- **Frames:** the sidebar with counts (Today 4, Agents 1) and with none; the foot with the watch listening, not listening and in Closed Eyes; Settings › What Isoform sees with Messages off and on-without-access; Your agents and Agent of You side by side; the popover with an item and with an agent item (Yes opens Today).
- **I'll check first:** (a) Agents' count is only requests waiting for your yes; (b) ⌘⇧E appears nowhere; (c) Settings lists Agent of You; (d) the popover's first control is Closed Eyes.

## 7. Activity and You

- **Read now:** computer-activity v1.1.0 (Forget lists what goes and counts it; agent work stays with the agent; You with nothing new), computer-settings v1.1.0 rule 4 (You's Agent of You tab), agent-words v1.0.0 ("Agent of You can say this").
- **Frames:** a day with screen, watch and agent rows; Forget on a watch conversation; Forget on a call whose prep went to an agent; You with nothing new; You on a first day; the Agent of You tab with the flag off and on.
- **I'll check first:** (a) dots, not bars; (b) the Forget button's count includes the thing itself; (c) no sample claims on a first day.

## 8. Computer Today

- **Read now:** computer-today v1.1.0 (every state), agents-join v1.0.0 §6 (the sheet's button by kind) and §7 (status), computer-setup v1.0.0 rule 10 (the first brief the same day), pass-access v1.0.0 rules 1 to 6 (keys, focus order, VoiceOver, stacking under 1,100 points).
- **Frames:** each of artboard 12's six states; the hand-off sheet for an agent on this computer, a chat app with the connector, and one not connected; Today at 1,000 points wide (stacked).
- **I'll check first:** (a) Y, N, M, E never fire while a text field has focus; (b) "couldn't start" quotes the agent and keeps the item offered; (c) the brief band says when the first brief comes on day one; (d) focus returns to the item after a sheet closes.

## Proof of this file

When each session's first PR after this file arrives, its description lists which of these files it read, and its frames include its section's states.

## On the map

No new rows: each check is a row already filed in the files it names.
