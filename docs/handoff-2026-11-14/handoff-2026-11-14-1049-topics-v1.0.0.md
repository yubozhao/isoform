# Topics v1.0.0: projects and subjects you follow, each with Isoform's next piece of work, on the computer and the phone

Version 1.0.0 · written November 14, 2026, 10:49 AM Pacific, by Claude Design.

**This answers the hub's request of November 14,** with Bo's decisions from the rework plan (`docs/rework-2026-11.md`, read-only on 798c844d8). Design first; the build comes from this file.

**What Bo decided:**
- A topic is either a project the person is doing (a decision they're weighing, for example) or a subject they follow (a game, for example), and is labeled as such.
- Each topic shows what Isoform saw (days this week, last touched) and Isoform's proposed next piece of work for it, with Yes, No and "Not mine".
- Yes runs the work through Claude Code, and the result comes back to the topic. No and Not mine are feedback that teaches Isoform.
- Topics are built once a day, as a saved copy, filtered by the person's own choices (Forget, excluded apps, Closed Eyes) at build time. Anything that leaves the computer passes the same filter.
- **The guiding principle:** gather as much data as possible, and where none exists, create it by proactively doing work for the person so they give feedback.

**What I read:**
- **The rework plan.**
- **Today's topics in the code on 798c844d8.** `TopicView` already has a kind ("work or followed"), the activity days (a card needs two in the last seven) and a latest moment. The ranker keeps ten topics over 14 days.
- **The desktop Today on Bo's app on 92770f5c0 (November 13).** It had one topic card, "Moxgate MTG Simulator · 3 days this week · Last: yesterday", with Edit topics.
- **The iPhone's topic chips,** which open Chat filtered to the topic (walk-1114-ios).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Where to look

- **The board:** Isoform Topics, a Design canvas with six artboards:
  - 1 · Today, a few topics;
  - 2 · Today, many topics;
  - 3 · A topic opened;
  - 4 · A proposal and its states;
  - 5 · On the iPhone;
  - 6 · First run.
- The computer is drawn in the app's dark colours, the iPhone in light. Names are invented, except Moxgate, the game on Bo's own Today. Bracketed text is sample content.
- English only; zh-CN waits.
- **No design system:** the account's one system, Yansu, isn't a default, and the hub had no one to ask. The boards use the app's own colours.

## First principles

1. **A topic is a thread of your life that comes back across days.** Either you're doing it (a project) or you keep up with it (following). The difference changes what help looks like. A project wants work done; a subject wants catching up. So the label is on every topic, and you can switch it.
2. **Every topic carries one piece of work, ready to run.** A list of things Isoform noticed asks nothing of you and teaches it nothing. A proposal asks one easy question. Every answer, Yes, No or Not mine, is data that didn't exist before. That is the guiding principle made into a screen.
3. **One question, three honest answers.**
   - **Yes:** do this work.
   - **No:** not this work, but the topic is right.
   - **Not mine:** this isn't my topic at all.
   - Each teaches something different, so they stay separate.
4. **Safe to say yes in one tap.** A proposal only reads, researches, compares, summarises, drafts, or writes code on a new branch. It never sends, posts, buys, books or deletes; when the work would, the result is a draft and the last step is yours. Because of that rule, Yes needs no second confirmation.
5. **Show exactly what leaves.** Every proposal says what of yours it uses, and "What it gets" lists it item by item. Nothing else goes to the agent.
6. **A day's list stays put.** It's built once, with the morning brief. During the day only your answers change it.

## 1. A topic

1. **Its kind,** as a label before the name: "Project" in the accent, or "Following" in grey. Isoform decides; the person can switch it in •••.
2. **What Isoform saw,** under the name:
   - seven dots for the days this week, with today ringed;
   - "5 days this week · last touched today, 10:12 AM".
   - The rule from today-review v1.1.4 stands: a topic needs two days in the last seven. One visit isn't a topic.
3. **Next:** one proposal, in one sentence. Under it:
   - how long ("About 10 min");
   - what runs it ("Claude Code", or "Claude Code with Chrome" when it browses);
   - what of yours it uses ("uses your Halcyon notes and the repo").
4. **Yes, No, Not mine.**
5. **•••:**
   - Rename.
   - "It's something I follow" or "It's a project".
   - Not mine.
   - Don't show for a week.

## 2. Today on the computer

Artboards 1 and 2.

6. **Today is the brief, then Your topics.** The section's header says "Built this morning at 7:00 AM".
7. **A few topics (one to three): full cards,** one under another. The name and what was seen are on top, then Next with its buttons.
8. **Many topics (four to ten): rows, grouped by what they need:**
   - "Waiting for your answer · 4";
   - "Working · 2";
   - "Done today · 1";
   - "Answered · 3".
   - A row is the kind, name, days and last touch, with the proposal in one line and the three buttons at the right. Clicking a row opens the topic.
9. **Order:** within a group, the ranking stays as today: days in the last seven, then how recent.
10. **Replaced:** "New on your topics" (finds), "Picked for you" (news) and "A suggestion" leave Today.
    - Finds and news become the proposals of the topics you follow ("Find what changed in this week's Moxgate patch…").
    - The suggestion was for automations, which are off.
    - The topic dialog's finds switch and sidebar pin go. Rename and remove live in •••.

## 3. A topic opened

Artboard 3.

11. **Opened from a card or a row,** in Today's column, with ‹ Today:
    - the kind and name, with Ask about it and •••;
    - the week as seven day blocks, with the time Isoform saw you on it ("3 h 40 min in all");
    - **Next,** as on Today, with **What it gets**;
    - **What Isoform saw:** the moments that make it a topic, newest first, by day and source (VS Code, Chrome, a call, Notes). Each has **Not this**, which takes the moment out of the topic and is feedback;
    - **Done for this topic:** every result, kept with the topic, with how the person rated it.
12. **What it gets** opens the exact list of what goes to the agent: these notes, this repo, these two pages. Removing one rewrites the proposal without it.
13. **Ask about it** opens Chat with the topic attached, as topics do today.

## 4. A proposal and its states

Artboard 4.

14. **Proposed:** "Next", the sentence, time, runner, what it uses; Yes, No, Not mine.
15. **Running:** "Working on it".
    - "Claude Code · started 2:41 PM · about 4 min".
    - Its steps as Claude Code reports them, done ones ticked and the current one last.
    - **Stop** ends it and keeps what's done.
16. **Waiting its turn:** two run at once, at most. A third Yes reads "Waiting its turn", "Starts when Board deck finishes. Two run at once.", with Cancel.
17. **Done, with the result:**
    - "Done", "Claude Code · 2:45 PM · 4 min";
    - the result's title, what it found in two lines, Open, and Ask about it;
    - then one question: "Was it useful?", with Useful and Not useful.
    - The result stays with the topic in Done for this topic.
18. **Declined:**
    - "You said no", with the time;
    - four optional reasons: Already done, Not now, Not useful, I'll do it myself;
    - "Isoform will propose something else tomorrow." and Undo, until tomorrow's build.
19. **Not mine:** the topic goes, with "Removed Lena's hiring plan. It won't be a topic again." and Undo.
20. **Couldn't finish:**
    - the reason in plain words, from what Claude Code reported ("Claude Code isn't signed in on this computer.");
    - Try again, and Open Claude Code;
    - what was done is kept and shown.
21. **Every answer is recorded with its topic and proposal,** and the next build learns from it. A Yes, a Useful, a Not useful, each reason, each Not mine and each Not this all count.

## 5. On the iPhone

Artboard 5.

22. **Answering is the phone's job.**
    - Today's "Your topics" shows one idea at a time: the topic, its kind and what was seen, the proposal, "About 4 min · on your computer".
    - Big Yes, No and Not mine, and "1 of 3 · All topics ›".
    - Answer, and the next slides in. Three ideas take ten seconds.
23. **After a Yes:** the answered idea becomes one line above the next ("Thursday review: working on it, on your computer"). The work runs on the computer.
    - When the iPhone can't reach it: "Starts when your computer is reachable", with Undo.
24. **All topics:** the same groups as the computer.
25. **A topic** in phone order: Next first, then Done for this topic, then What Isoform saw, and "Ask about Halcyon import" at the bottom.
26. **A result:** text results read in full on the phone, with Useful and Not useful. A file result shows its name and "Open on your computer".
27. **One notification per result,** only when it finishes while the person isn't looking: "Warriors: the last three games are ready." Nothing for each step, and no notification for the day's ideas.

## 6. First run

Artboard 6.

28. **On day one there's no data, so Isoform makes some.**
29. **Your topics says when they come:** "Isoform finds your topics once it has seen two days of your work. The first ones come with tomorrow's morning brief."
30. **Start one now:** "What are you working on, or following?" with Add.
    - "A project, a decision you're weighing, a game, a team. It becomes a topic now, with its first idea in a minute."
    - A topic the person names skips the two-day rule and gets its first proposal at once. It's the one exception to the daily build.
31. **Things Isoform can do today:** proposals from what it already has on day one (the calendar, mail, what it has seen since setup).
    - For example "Prepare notes for your 2:00 with Lena, from your last emails with her."
    - Same runner and states, with Yes and No only: there's no topic yet to call not mine.
32. **The next morning,** the built topics replace this. A topic the person added keeps its place.
33. **On the iPhone,** the same three things, smaller.

## 7. Building, privacy and the agent

34. **Built once a day** with the morning brief, from the last 14 days, as a saved copy. The person's own choices are applied at build time: Forget, excluded apps and paused time (Closed Eyes).
35. **One proposal per topic per day.** After Done or No, the next proposal comes with tomorrow's build.
36. **What goes to Claude Code** is only the task and the items in What it gets, after the same filter. The phone gets the topic, the proposal and the result text, after the same filter.
37. **Computer use goes through the agents that already do it:** Claude Code with Claude in Chrome for the web, the person's computer-use agent for desktop apps. Isoform keeps the decision and the data and passes down only the task (the rework plan).
38. **Results are kept with the topic,** on the computer, by the computer's keep rules.

## What the build needs to give the pages

- **Per topic:** kind, name, the days this week, last touched, time in all, the moments (with their sources), and the proposal (sentence, time, runner, what it gets).
- **Per proposal:** state (proposed, queued, running with steps, done, declined, not mine, failed with reason), the result (title, two-line summary, the file or text) and the person's answers.
- **Claude Code progress** steps, and its failure reasons in words.
- **A first-proposal path for a topic the person adds,** within a minute.
- **The phone:** the same topics, proposals and results, and answers sent back, queued while the computer is out of reach.

## Every word

| Key | English | Where |
| --- | --- | --- |
| topics.title | Your topics | Today, section |
| topics.built | Built this morning at {time} | Section header |
| topics.count | {n} topics · built this morning at {time} | Section header, many |
| topics.kindProject | Project | Label |
| topics.kindFollowing | Following | Label |
| topics.seen | {n} days this week · last touched {when} | Under the name |
| topics.total | {duration} in all | Topic page, after the seen line |
| topics.next | Next | Proposal label |
| topics.meta | About {mins} min · {runner} · uses {what} | Proposal meta |
| topics.runnerCode | Claude Code | Runner |
| topics.runnerChrome | Claude Code with Chrome | Runner, when it browses |
| topics.whatItGets | What it gets | Proposal meta, link |
| topics.yes | Yes | Button |
| topics.no | No | Button |
| topics.notMine | Not mine | Button |
| topics.groupWaiting | Waiting for your answer | Group |
| topics.groupWorking | Working | Group |
| topics.groupDone | Done today | Group |
| topics.groupAnswered | Answered | Group |
| topics.running | Working on it | State label |
| topics.runningMeta | {runner} · started {time} · about {mins} min | State meta |
| topics.stop | Stop | Running |
| topics.queued | Waiting its turn | State label |
| topics.queuedBody | Starts when {topic} finishes. Two run at once. | Queued |
| topics.cancel | Cancel | Queued |
| topics.done | Done | State label |
| topics.resultReady | Result ready | Row, done |
| topics.open | Open | Result |
| topics.ask | Ask about it | Result, topic page |
| topics.useful | Was it useful? | Result |
| topics.usefulYes | Useful | Result |
| topics.usefulNo | Not useful | Result |
| topics.declined | You said no | State label |
| topics.declinedAt | You said no at {time} | State meta |
| topics.reasonDone | Already done | Reason chip |
| topics.reasonLater | Not now | Reason chip |
| topics.reasonUseless | Not useful | Reason chip |
| topics.reasonMyself | I'll do it myself | Reason chip |
| topics.tomorrow | Isoform will propose something else tomorrow. | Declined |
| topics.undo | Undo | Declined, not mine |
| topics.notMineToast | Removed {topic}. It won't be a topic again. | Toast |
| topics.failed | Couldn't finish | State label |
| topics.tryAgain | Try again | Failed |
| topics.openCode | Open Claude Code | Failed |
| topics.saw | What Isoform saw | Topic page |
| topics.notThis | Not this | A moment |
| topics.doneFor | Done for this topic | Topic page |
| topics.menuRename | Rename | ••• |
| topics.menuFollow | It's something I follow | ••• (on a project) |
| topics.menuProject | It's a project | ••• (on a following topic) |
| topics.menuHide | Don't show for a week | ••• |
| topics.phoneMeta | About {mins} min · on your computer | iPhone proposal |
| topics.phoneOf | {i} of {n} | iPhone, under the buttons |
| topics.phoneAll | All topics › | iPhone |
| topics.phoneIdeas | {n} ideas | iPhone, section |
| topics.phoneRunning | {topic}: working on it, on your computer | iPhone, answered line |
| topics.phoneWaiting | Starts when your computer is reachable | iPhone, answered line |
| topics.phoneFile | Open on your computer | iPhone, file result |
| topics.notifDone | {topic}: {result title} is ready. | Notification, one per result |
| topics.firstBody | Isoform finds your topics once it has seen two days of your work. The first ones come with tomorrow's morning brief. | First run |
| topics.firstStart | Start one now | First run |
| topics.firstField | What are you working on, or following? | First run, field |
| topics.firstAdd | Add | First run |
| topics.firstHint | A project, a decision you're weighing, a game, a team. It becomes a topic now, with its first idea in a minute. | First run |
| topics.firstToday | Things Isoform can do today | First run, section |
| topics.firstTodaySub | From what it has already | First run, section, right |
| topics.firstPhone | Your topics come with tomorrow's morning brief, once Isoform has seen two days of your work. | iPhone, first run |
| topics.firstPhoneField | Add one: what are you working on? | iPhone, first run |
| topics.firstPhoneToday | Isoform can do today | iPhone, first run, label |

## Proof

On Bo's app and phone, named builds, before and after:

1. **The morning build:** Today shows topics labeled Project and Following, with the days and last touch matching what Bo did, each with a proposal and "Built this morning at 7:00 AM".
2. **Yes on the computer:** the work runs through Claude Code with its steps, and the result comes back to the topic. Useful or Not useful is recorded.
3. **Yes on the phone:** it runs on the computer. With the computer out of reach it waits, then runs when it's back.
4. **No with a reason, and Not mine:** the next morning, the No'd topic has a different proposal and the Not mine topic is gone.
5. **What it gets:** the agent's log shows only the listed items. Remove one, and the rewritten proposal and the log both leave it out.
6. **First run** on a fresh data folder: the day-one proposals run; a topic Bo adds gets its first proposal within a minute.
7. **Privacy:** a Forgotten item, an excluded app and a paused hour never appear in What Isoform saw, a proposal, What it gets or the phone.

## The rework, on the map

From Bo's decisions of November 14:
- **Turned off (behind a switch, code kept):** automations and cron prompts; playbooks, SOPs, deliverables and goals; proactive voice cards in chat; calendar holds. Their rows are held, with a note; they come back one at a time.
- **Deleted:** the shadow agent and Agency. Their rows are cut.
- **Computer use** moves to the agents that already do it. No row had it.
- **Topics:**
  - new today rows R82 to R86;
  - new phone rows P191 to P193;
  - the finds rows (R67, T54, T72 to T74) are cut, replaced by proposals.
- **iphone-issues v1.0.0 issue 4** (Chat's waiting card, P185) is about a proactive voice card, so it's held with them.
