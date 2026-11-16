# Phone Today v1.0.0: the latest brief first, then Your work, the one list where everything is judged

Version 1.0.0 · written November 15, 2026, 2:02 PM Pacific, by Claude Design.

**Part of the phone pass of November 15.** Read phone-map v1.0.0 first: the ranking, the shared item, the backend items (B1 to B13). This file replaces redesign-phone v1.0.0's Today (§ Today, rules 1 to 9) and its Tasks (rules 10 to 12), and watch v1.2.0's Today rules 36 to 38 stand as written here.

**Board:** Isoform iPhone, artboard 2 (Today) and artboard 4 for what a Yes on offered work opens.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **The latest brief shows up:** the newest brief made, with its time, on the first screen, from what this phone has saved.
2. **Its quality:** it reads in a minute and plays.
3. **Judging work:** Your work, the one list; the open item answered in one tap.
4. **What happened is visible:** one line per answer, with Undo.
5. **The watch:** its state in the mark; its trouble in one line, only when it matters.

Nothing lower blocks anything higher: Your work, the watch line and the topics load on their own; a failure shows one line in its own place, never above the brief.

**Before:** walk-1114-ios 01 to 03 and 15 to 16 (Today: the brief card with chips, Your day, Your topics, the offline states), on `feat/iphone-companion` at c1f0e8bcf; and the Tasks tab (14), with its groups Needs you, Up next, Done this week.

## The page

1. **Header:** the mark in the watch's pose (phone-map rule 12) and "Today", the date under it, the person button at the right (Settings).

### 1. The brief

2. **The brief card is always on the first screen.** Its title is the kind and the time it was made: "Evening brief · 6:00 PM". The ••• menu holds Past briefs and Make a new brief now.
3. **Hear it, with its length** ("Hear it · 2 min"). It plays here; leaving the page keeps it playing in the mini player above the tabs.
4. **Its first three sentences, then "Read all ›",** which opens the brief in full (phone-brief v1.0.0).
5. **Folded once you've moved on.** After the first answer in Your work, or once it has been heard or read in full, the card folds to its title and two lines. Tapping unfolds it. It never leaves the first screen.
6. **Drawn from what this phone has saved, at once,** then replaced in place when a newer one arrives (B1). Never a banner asking to be tapped: "A newer brief is ready ›" goes.
7. **Every state keeps a brief on screen** (brief rules v1.0.0 stand):
   - **A newer brief is made but not here, the computer asleep:** the last brief stays, under its own time, with one amber line above its title: "Your 6:00 PM brief comes when your computer wakes. It's asleep."
   - **A newer brief is being made:** the last brief stays, with "Your evening brief is being made · ready around 6:03 PM", only when the computer has said so.
   - **None made yet (the first day):** the card reads "Your first brief comes at 7:00 AM." and nothing else.
   - **A package that can't be opened** is never shown; the last whole one stays (B1).
   - **The phone needs pairing again:** phone-setup v1.0.0.
   - Never "missed", never "late", never a blank card.

### 2. Your work

8. **One list, under the brief:** "Your work" and "{n} to answer". It holds everything Isoform puts to you on the phone:
   - the brief's own items;
   - follow-ups from your watch's conversations;
   - meeting prep;
   - Agent of You's questions (redesign-you v1.0.0);
   - topics' proposals, while Proposals from your topics is on;
   - results back from your agent, and "did it finish?" checks (phone-agent v1.0.0).
9. **The order:**
   - the brief's items first, in the brief's order;
   - then what came since, newest first, with prep due soonest at the top;
   - a result back goes to the top of what's left;
   - at most ten items a day (redesign v1.0.0, rule 15).
10. **The first unanswered item waits open:** the shared item (phone-map rules 1 to 10): where it came from, what Isoform noticed, the work, what Yes does, Yes, No, Not mine, Edit and •••.
11. **What Yes does depends on the work:**
    - work Isoform already did is used where it lands, in one tap ("Yes puts it in your Mail drafts. You send it.", "Yes keeps it with the meeting. It opens at 9:55 AM.");
    - work it offers goes to your agent: "Yes hands it to Claude, your agent. You see exactly what goes first." (phone-agent v1.0.0);
    - a claim: "Right?", and Yes means it's right.
12. **No, with an optional reason** (artboard 2, frame 4). Tapping No shows the reasons as chips under the item, with Undo and Skip; picking one or Skip brings the next item up. "Not now" is a reason here, not a button anywhere.
13. **Up next:** the next two or three as one-line rows, each with what it is, where it came from and a word for its kind (Draft, Prep, Check, Offered). Tapping one makes it the open item.
14. **With your agent:** one row per hand-off, "Q4 plan outline · Claude · since 4:10 PM" (phone-agent v1.0.0).
15. **Done today:** one line per answer, newest first, with what happened and Undo until tomorrow's build:
    - "Prep for Lena: kept for 10:00 tomorrow"
    - "Reply to Priya: in your Mail drafts"
    - "Test plan: not now"
    - "Reply to Priya: in your Mail drafts · waits for your computer"
16. **When everything is answered:** "That's everything for now." and "Prep comes an hour before a meeting; follow-ups, ten minutes after the call or email."
17. **A topic's first line opens the topic:** its name and kind, the days it was seen this week, its proposals (open and answered), and Reply about this topic (Chat, attached). The topic chips row on Today goes.

### 3. The watch line

18. **One line, only when it matters,** between the header and the brief, amber, opening Where today's talk is (phone-watch v1.0.0):
    - hours have waited for the computer 30 minutes or more: "2 hours from your watch are waiting for your computer.";
    - an hour couldn't be sent: "1 hour from your watch couldn't be sent."
    - Nothing else. It sits above the brief only because it's one line and asks for nothing; it never pushes the brief off the first screen.

### What goes

19. The Tasks tab, with Needs you, Up next, Find more and Done this week. Its work is Your work; its "Give Isoform a task" field goes (ask in Chat; a task-like ask becomes a hand-off).
20. Done for you, Your day and the topic chips on Today.
21. "Plan next steps" and "Not now" as buttons on brief items.
22. "A newer brief is ready ›".

## What the build needs

B1 (the brief package, whole and first), B2 (one list of work), B3 (answers from the phone, with reasons and Undo), B7 (the watch's state for the mark and the line).

## Every word

| Key | English | Where |
| --- | --- | --- |
| today.title | Today | Header |
| today.brief.evening | Evening brief · {time} | Brief card |
| today.brief.morning | Morning brief · {time} | Brief card |
| today.brief.hear | Hear it | Brief card |
| today.brief.length | {mins} min | Brief card |
| today.brief.readAll | Read all › | Brief card |
| today.brief.menuPast | Past briefs | ••• menu |
| today.brief.menuMake | Make a new brief now | ••• menu |
| today.brief.asleep | Your {time} brief comes when your computer wakes. It's asleep. | Brief card, amber line |
| today.brief.making | Your {kind} brief is being made · ready around {time} | Brief card |
| today.brief.first | Your first brief comes at {time}. | Brief card, first day |
| today.work | Your work | Section |
| today.work.count | {n} to answer | Section |
| today.work.allDone | all answered | Section |
| today.work.upNext | Up next | Group |
| today.work.withAgent | With your agent | Group |
| today.work.doneToday | Done today | Group |
| today.kind.draft | Draft | Up next row |
| today.kind.prep | Prep | Up next row |
| today.kind.check | Check | Up next row |
| today.kind.offered | Offered | Up next row |
| today.reasons.ask | No. Any reason? | After No |
| today.reasons.optional | (optional) | After No |
| today.reasons.skip | Skip | After No |
| today.empty | That's everything for now. | All answered |
| today.emptyNext | Prep comes an hour before a meeting; follow-ups, ten minutes after the call or email. | All answered |
| today.waits | waits for your computer | Done line |
| today.watch.waiting | {n} hours from your watch are waiting for your computer. | Watch line |
| today.watch.failed | {n} hour from your watch couldn't be sent. | Watch line |

The item's words (source lines, the Yes lines, reasons, the done lines) are in phone-map §4 and redesign v1.0.0 §1 to §6. Plural forms follow the language.

## Proof

On Bo's iPhone, a named TestFlight build, against the befores above:

1. **Rank 1 holds:** with the computer asleep and the phone offline, Today opens on the last brief, under its own time, with the amber line, within a second of launch; Hear it plays. A screen recording from launch.
2. **Replaced in place:** when the 6:00 PM brief lands, the card changes to it without a banner and without a tap. A recording across the moment.
3. **One list:** the brief's items, a watch follow-up and a topic's proposal all appear in Your work in one day; no Tasks tab exists. Stills.
4. **Answers count:** three answers in under fifteen seconds, one with a reason; the computer's Your answers shows them with "phone" and the reason.
5. **Folded, not gone:** after the first answer the brief folds to two lines and is still on the first screen.

## On the map (phone)

- P216 · Today opens on the latest brief, from what this phone saved; a newer one replaces it in place; every state keeps a brief on screen.
- P217 · The brief folds to two lines once you've moved on, and never leaves the first screen.
- P218 · Your work: the one list under the brief (open item, Up next, With your agent, Done today).
- P219 · The Tasks tab goes; its work is Your work (replaces P199).
- P220 · No carries an optional reason; "Not now" is a reason; the brief's Plan next steps / Not now become Yes, No, Not mine.
