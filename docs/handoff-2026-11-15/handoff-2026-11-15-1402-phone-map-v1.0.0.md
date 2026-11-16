# Phone map v1.0.0: the iPhone app redesigned from the thesis, ranked; the navigation map, the shared parts, and what the backend lacks

Version 1.0.0 · written November 15, 2026, 2:02 PM Pacific, by Claude Design.

**The hub's request, from Bo:** "ask design to redesign the pages on iphone app." Every page in one pass, from first principles, with:
- the thesis: gather signal, turn it into work people judge (Yes, No, Not mine), and treat their judgments as the most valuable data;
- **Bo's rule, stack ranking instead of pros and cons:** for the brief, 1. the latest brief shows up, 2. its quality; nothing lower may block those. The same ranking on every page, written at the top of its handoff;
- a first-class place for the Apple Watch (v1.1.0 is built: hour-long parts, tap or double tap to mark, hold to stop, pause from the watch, conversations named per hour, notes with follow-ups over 5 minutes); Bo couldn't find Settings › Watch › Recordings and sync on today's TestFlight;
- copy says computer and phone.

**The hub's second input, the same afternoon** (14 days of PostHog: 47 active users; the Tasks section seen 279 times by 31 users, task actions 134 by 28 users, only 14 of them "do"; chat 35 turns by 18 users):
1. Tasks goes away as its own place. Tasks, topics' proposals and Done for you merge into one list of work: Isoform proposes, you judge it, a Yes goes to your agent through Work on it, and the result comes back for another judgment.
2. Chat stays but narrows: ask about your own life, correct and steer Isoform, and reply attached to an item. No general-assistant features; anything task-like goes through Work on it.
3. Ranked goals for the phone: 1. the latest brief, 2. judging proposed work, 3. asking and correcting.

**Designed to that.** Where I see a better shape, it's said in §2.

A change goes in a new file with a new version; this one isn't edited after it's written.

## The files of this pass

Read this one first. One handoff per page:

| File | Page |
| --- | --- |
| phone-map v1.0.0 (this) | The ranking, the navigation map, the shared parts, notifications and the widget, words across devices, the backend items, what changes on the computer |
| phone-today v1.0.0 | Today: the brief, then Your work |
| phone-brief v1.0.0 | The brief in full, the player, Past briefs |
| phone-agent v1.0.0 | Yes goes to your agent: the hand-off, With your agent, the result back |
| phone-watch v1.0.0 | The Watch tab and a conversation |
| phone-chat v1.0.0 | Chat: ask and correct, replies attached |
| phone-settings v1.0.0 | Settings and Your data |
| phone-setup v1.0.0 | First run, pairing, pairing lost |

**The board:** Isoform iPhone, a Design canvas (https://claude.ai/artifact/WKCdoNzqXTrsGG4aerRFrs), ten artboards:
1. The map: the phone, ranked;
2. Today: the brief, then your work;
3. The brief;
4. Yes goes to your agent;
5. Watch;
6. A conversation;
7. Chat: ask and correct;
8. Settings;
9. First run and pairing;
10. Lock screen, notifications, widget.

Light, the app's own colours (accent `#4f5ed8`, the floating tab bar). Names and content are invented. English only; zh-CN waits.

**What I read** (read-only): the iPhone app on `feat/iphone-companion` at c1f0e8bcf (tabs Today, Chat, Tasks; Settings as a sheet from the person button; Settings › Recordings for the watch), the watch app on `claude/watch-pause-from-watch` at 5d2dda083 ("Recording on your wrist", "Hold to stop", "Pause for an hour", "Marked {time}"), the watch notes commit 26a08f65d (notes and commitments for conversations over five minutes, behind `watch-notes`), the walk-1114-ios captures, and docs/rework-2026-11.md. Nothing here is proven; the befores for each page's proof are in its handoff.

## 1. The phone, ranked

Each goal outranks everything below it. **Nothing lower may block a higher one: not its loading, not its errors, not its space on the screen.**

1. **The latest brief shows up.** The newest brief made, morning or evening, with its time, from what this phone has saved, before anything is fetched. Then it plays.
2. **Judging work.** One list, Your work, on Today under the brief: Isoform proposes, you say Yes, No or Not mine (Edit for the richest answer); a Yes on offered work goes to your agent; the result comes back to be judged.
3. **Asking and correcting.** Chat about your own day, corrections, and replies attached to a paragraph, an item, a topic or a conversation.
4. **The watch gathers.** It listens; what it heard is findable as conversations; its follow-ups go to Your work.
5. **Trust and connection.** Paired and reachable; Closed Eyes on every device; what's kept and what left, visible.

What "never blocks" means for the build, on every page:
- Each page draws its rank-1 content from what the phone has saved, at once, then refreshes it in place.
- Each section below loads on its own. A section that fails shows one line in its own place; it never blanks, delays or moves the sections above it.
- A newer version of rank-1 content replaces the old in place, never as a banner asking to be tapped ("A newer brief is ready ›" goes).
- Lower sections never push rank 1 off the first screen: the brief is always on Today's first screen, folded to two lines once it has been read.

## 2. Where I'd shape it differently from the hub's suggestion

The hub's three points stand. Four refinements, each built into the pages:

1. **Your work lives on Today, under the brief, not in a tab of its own.** The brief's items are the first proposals of the day; a second place to judge splits judging again, which is what the Tasks numbers show (279 views, 14 "do"). Today becomes: the brief, then Your work.
2. **Only offered work goes to your agent.** Work Isoform already did (prep, a drafted reply) is used where it lands with one tap: Mail drafts, the meeting. Sending finished work to an agent would add a step and a company for nothing.
3. **The hand-off shows exactly what goes before it goes.** Your Yes is recorded on the first tap; Send is the second. It leaves your devices for another company's agent, so, like Agent of You (redesign v1.0.0, rule 11), what goes is shown in full, every item removable. This is the one Yes with a second step.
4. **The watch gets a tab, ranked 4th.** Ranking decides order and what may block what, not visibility. Bo couldn't find the watch inside Settings; the watch is where the phone gathers signal, and its follow-ups feed rank 2. The tab exists only when this phone has an Apple Watch paired.

## 3. The navigation map

Artboard 1.

**Three tabs:** Today · Watch · Chat, on the floating bar. Watch shows only when this phone has an Apple Watch paired (the system says so; Isoform installed or not). Without one, the bar is Today · Chat.

**Today** (ranks 1 and 2):
- The brief, folded to two lines once read → **The brief in full** (player, read-along, its items inline) → **Past briefs** (30 days).
- **Your work:** the open item; Up next; With your agent; Done today.
  - An item's **Edit** → full screen, What it gets as chips.
  - An item's **•••** → Why, What it gets, Reply (Chat, attached), and Hand it to your agent on work it did when you'd rather an agent finished it.
  - **Yes on offered work** → the hand-off sheet; the first time, Who does your work?
  - **A result back** → Useful or Not useful.
- Your topics' proposals sit in Your work, while Proposals from your topics is on.
- **The mark** at the top opens the Watch tab; the watch line (only when hours wait or fail) opens Where today's talk is.

**Watch** (rank 4): Now → Where today's talk is (a sheet, only when it matters); today's hours; Conversations → **A conversation**; Earlier days; Your watch (its settings).

**Chat** (rank 3): ask about your day; correct and steer; replies attached; anything task-like becomes the hand-off sheet.

**Settings** (rank 5), a sheet from the person button on every tab: Your computer; Closed Eyes; Your data; Your agent; Your voice; Apple Watch (opens the tab); Notifications; Proposals from your topics; Agent of You; This phone.

**Ways in:** the brief notification → Today; prep → the item, or Not needed from the notification; finished work → the result; the widget and lock screen → Today.

**Gone:**
- the Tasks tab (its groups are Your work);
- Done for you (now Your work);
- the topic chips and Your day block on Today (redesign-phone v1.0.0 had already moved them);
- "A newer brief is ready ›" (the newer brief replaces the old in place);
- Settings › Recordings and Settings › Watch › Recordings and sync (now the Watch tab);
- the Work on it switch (Work on it is how Yes works on offered work);
- Chat's proactive cards (held since redesign-phone v1.0.0) and the "Give Isoform a task" field.

## 4. The shared parts

### The item

Used on Today, in the brief, in a conversation, and in Chat's hand-off card. Redesign v1.0.0 §1 to §6 stand; this is its phone form.

1. **The first line says where it came from and why now:** "For your 10:00 with Lena · tomorrow · from the brief", "Asked of you · Sam · 2:05 PM, from your watch", "Halcyon import · a project · from your topics", "Back from Claude · you said yes at 6:20 PM".
2. **One sentence of what Isoform noticed,** in bold.
3. **The work:** a grey box with its title and the text (work it did), or the title and what it would do with "About 10 min" (work it offers).
4. **The line saying what Yes does:**
   - "Yes keeps it with the meeting. It opens at 9:55 AM."
   - "Yes puts it in your Mail drafts. You send it."
   - "Yes hands it to Claude, your agent. You see exactly what goes first."
5. **Yes, No, Not mine,** big. **Edit** and **•••** at the top right.
6. **"1 of 4"** and dots under it. Swipe moves between items without answering; no swipe answers.
7. **No can carry a reason; none is needed:**
   - on work it offers: Already done, Not now, Not useful, I'll do it myself;
   - on work it did: Not needed, Wrong facts, Not how I'd say it, I'll do it myself;
   - on a claim: Was true once, Only sometimes, Not at all.
   - **"Not now" is a reason, never a button.** The brief's "Plan next steps" and "Not now" become Yes and No like everything else.
8. **After an answer, one line** with a check, what happened, and Undo until tomorrow's build: "Prep for Lena: kept for 10:00 tomorrow".
9. **A result** has "Was it useful?" with Useful and Not useful, once, and Reply.
10. **The same item everywhere:** answered on Today, it shows answered in the brief, in its conversation and on the computer.

### Answers when the computer is away

11. Every answer, edit, reply and question is kept on the phone and sent when the computer can be reached, in order. Its line says "waits for your computer". Nothing is lost, nothing is sent twice.

### The mark

12. Today's title carries the mark in the watch's pose (watch v1.2.0 §2 and §6): the lifted brow while the watch listens, the cap in Closed Eyes, the shut eye when it can't listen. At rest it keeps watch (v1.2.0 rules 3 to 8). Tapping it opens the Watch tab.

### Words across devices

13. **Copy says computer and phone.**
    - "This iPhone" becomes "This phone" everywhere: Settings, Your watch and Your data.
    - The computer's "on your iPhone" becomes "on your phone" (From your watch, the Phone page).
    - "Pair an iPhone" becomes "Pair a phone".
    - "Apple Watch" stays as the product's name where the phone names the device ("Apple Watch" in Settings, "Your Apple Watch is paired with this phone").
14. **The pause is Closed Eyes on every device** (watch v1.2.0 §1). The watch build still says "Recording on your wrist", "Paused" and "Pause for an hour"; the phone uses Listening and Closed Eyes from this pass, so watch v1.2.0's words ship with it or before it.
15. **"Off for today"** names the watch's stop, which the build already has (79676acff, "a stop on the watch is the off for that day").

## 5. Notifications, the lock screen, the widget

Artboard 10.

16. **Three kinds, and nothing else** (redesign v1.0.0, rule 22):
    - **the brief** at its time: "Your evening brief", its first two sentences, and "{n} to answer";
    - **meeting prep,** 15 minutes before, only if it hasn't been opened: "Your 2:00 with Lena" / "Prep is ready: her two questions, where the fix stands, and the test plan." with **Read it** and **Not needed** (a No, recorded without opening the app);
    - **finished work,** when it came back while you were away: "Claude finished the test plan" with **Read it** and **Useful**.
17. **No notification** for a proposal, a claim, a watch hour, a follow-up or anything to check. Those wait in Your work.
18. **The widget,** ranked like the app: the latest brief and its time, its first line, "{n} to answer", then the watch in words ("Your watch is listening"). With the computer asleep it keeps the last brief, with its own time. Tapping opens Today.
19. **The lock-screen widget:** the mark's pose and "{n} to answer · evening brief".

## 6. What changes on the computer

The hub's input applies there too. Decided here; the computer's pages follow in their own handoffs (redesign-today and redesign-work, next versions), drawn from this board's parts:

20. **Tasks goes as a page.** Its groups become Your work on the computer's Today, under the brief: the same list, the same ids, the same answers as the phone. The sidebar loses Tasks.
21. **Work on it is how Yes works on offered work,** on the computer too, with the same sheet (exactly what goes, Your agent, Send). On the computer the agent can also be the one installed there (Claude Code or Codex); the person's choice is one setting for both devices.
22. **Chat narrows the same way,** on the computer and in Quick chat: your own life, corrections, replies attached; task-like asks become the hand-off.
23. **Settings › Your data gains "Sent to your agent":** each hand-off with exactly what went.

## 7. What the backend lacks

Each page's handoff names the items it needs. The list:

| # | What | Needed by |
| --- | --- | --- |
| B1 | **The brief package reaches the phone whole, first.** When a brief is made, the computer sends its text, audio, items and made-at time; the phone keeps the last whole package and never replaces it with a partial one (e9ff9af27 is a start). A status the phone can read: made at, being made and ready around, made but not sent because the computer is asleep. | Today, the brief, the widget |
| B2 | **One list of work for the phone.** A single feed: the brief's items, topics' proposals (when on), follow-ups from watch notes, prep, Agent of You's questions, results back from agents, and "did it finish?" checks. Each with an id shared with the computer, its kind (work done, work offered, claim, result, check), the first line's parts, the Yes line, What it gets, Why, and its state (open, answered with what, with your agent since, back). Today's separate feeds (brief cards, topics, tasks, Done for you) end. | Today, the brief, a conversation |
| B3 | **Answers from the phone:** Yes, No with a reason, Not mine, Edit (the text before and after), Useful, Not useful, with the surface "phone"; queued when the computer is away, sent once, in order; Undo until the next build. The brief's "Plan next steps / Not now" map to Yes / No (Not now). | Today, the brief, notifications |
| B4 | **Your agent:** the person's chosen agent, one setting for phone and computer; each hand-off kept with exactly what went, when and to whom, for Your data. | Yes goes to your agent, Settings |
| B5 | **Agents report back.** The one-time task links (agents.isoform.ai) aren't live. Each hand-off needs a link the agent reports its result to; the result lands in Your work. Until then, the next morning's "Did Claude finish…?" check stands in. | Yes goes to your agent |
| B6 | **Conversations for the phone.** Per day: each conversation's name, start and end, length, speakers, marks, place and heart-rate words (when on), its note (over 5 minutes), its follow-ups as item ids, and where it is. Today's hours with talk minutes and states, for the strip. Delete a conversation everywhere. Today the phone reads only names per hour (ec3941f5b). | Watch, a conversation |
| B7 | **The watch's state on the phone:** Listening, Closed Eyes until, Off for today, Not listening with its reason, and the last time talk was heard; the computer's state for Where today's talk is (awake, asleep since, can't reach, needs an update, Closed Eyes on). | Watch, Today's mark |
| B8 | **Notes for long talks as a person's setting.** Today it's `YANSU_FEATURES=watch-notes`; it needs a per-person switch the phone can read and set. | Watch |
| B9 | **Push through the relay** for the three kinds, with prep's Read it and Not needed and finished work's Useful recorded from the notification (P183). | Notifications |
| B10 | **Chat's narrowing:** replies carry what they're attached to (a paragraph, item, topic or conversation id); a correction returns what changed, with Undo; a task-like ask returns a hand-off card; the claims an answer leans on come marked for underlining with their Why. | Chat |
| B11 | **The widget's snapshot:** the latest brief's title, time and first line, the count to answer, the watch in words. | Widget |
| B12 | **Pairing sends the latest brief package with it,** so the first thing after pairing is the brief. | Setup |
| B13 | **A speaker guess answerable from the phone** (Yes, No, Edit), as watch-voice v1.0.0 has it on the computer. | A conversation |

## Proof

The pass is proven on Bo's iPhone (and watch) on a named TestFlight build, against the befores in each page's handoff. Three checks span the pages:

1. **Nothing lower blocks the brief.** With the computer asleep and no network, Today opens on the last brief, under its own time, within a second, and it plays. A screen recording from launch.
2. **One list.** Over a day, every proposal Bo sees on the phone is in Your work; there is no Tasks tab; each answer shows on the computer's Your answers with "phone".
3. **The watch is found.** Bo opens the Watch tab without being told where it is, and finds the conversation from his lunch by its name.

## On the map

**phone**, new rows, all gaps:
- P216 to P220 · Today (phone-today v1.0.0).
- P221 to P223 · The brief (phone-brief v1.0.0).
- P224 to P227 · Yes goes to your agent (phone-agent v1.0.0).
- P228 to P234 · Watch and a conversation (phone-watch v1.0.0).
- P235 to P237 · Chat (phone-chat v1.0.0).
- P238 to P240 · Settings (phone-settings v1.0.0).
- P241 to P243 · Setup (phone-setup v1.0.0).
- P244 · The phone, ranked: each page draws rank 1 from what it saved; lower sections load and fail on their own; a newer rank-1 replaces in place (this file, §1).
- P245 · Notifications: three kinds, prep's Not needed and finished work's Useful answered from the notification; the widget ranked like the app (this file, §5).

Replaced: P199 (Tasks on the phone) by P219; P211 by P228 and P232; P212 by P231; P213 by P234. P214 (Today's mark and watch line) stands, as phone-today rule 18 and this file's rule 12. P194 to P198 and P200 stand as notes to phone-today, phone-settings and §5; P201 (Agent of You's question) stands, as an item in Your work.
