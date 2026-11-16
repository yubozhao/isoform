# Computer find v1.0.0: a topic opens from its item, ⌘K finds anything, and topics' work follows agents-join

Version 1.0.0 · written November 15, 2026, 9:14 PM Pacific, by Claude Design.

**Why this file:** computer-map v1.0.0 took the Topics page and the Search page out of the sidebar (rules 6 and 10) and said where they go, but didn't draw either. And topics-v1.0.0's proposal states say "Working on it" for any running proposal, which agents-join v1.0.0 now forbids where Isoform can't see the work. This file draws both and fixes the states.

**Board:** Isoform Computer, artboard 18 (A topic, and ⌘K).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **A topic's next piece of work is answered like any item,** with the keys.
2. **Anything Isoform keeps is one ⌘K away,** ready work first.
3. **Topics never say more than Isoform knows** about the agent doing their work.

## 18a · A topic, opened

1. **It opens from an item's source line** ("Halcyon import · from your topics") and from ⌘K, inside Today's column, with ‹ Today (topics v1.0.0 rules 11 to 13, phone-today rule 17).
2. **Its parts:** the kind ("Project" in the accent, "Following" in grey) and the name; "Seen {n} of the last 7 days" and ••• (switch kind, Not this topic) at the right; **Next**, its one proposal as the shared item with Y, N, M, E; **Done for this topic**, each with what happened and Undo until tomorrow's build; **What Isoform saw**, by day, each line opening its moment; and **Ask about {topic} ›**, which opens Chat with it attached.
3. **Two columns,** Next and Done on the left (600), What Isoform saw on the right; they stack under 1,100 points (pass-access rule 6).

## 18b · ⌘K

4. **⌘K opens over any page,** with the field focused and Esc to close. Results are grouped, in this order: **Ready** (prep, drafts, results, with Open ↩), **Conversations** (watch, calls, mail, screen moments), **People**, **Claims**, **Your answers**, **Go to** (pages, with their keys).
5. **Each found thing** has its source line and Why and Forget (redesign-work rules 18 and 19); ⌘↩ opens it in Chat; ↑↓ move.
6. **What you search for is kept on this computer** and shows in Activity under Your day (redesign-work rule 20); the palette's foot says so.

## Topics' work, by agents-join

7. **A topic's proposal goes to your agent like any offered work** (the hand-off sheet, agents-join §6). Its states replace topics v1.0.0 rules 15 and 16:
   - **Sent / {agent} has it / Working** follow agents-join rule 22: "Working" only where Isoform can see it;
   - **Waiting its turn** applies only to agents Isoform starts on this computer: at most two run at once; a third reads "Waiting its turn · starts when {other} finishes. Two run on this computer at once." with Cancel.
8. **Done, Declined, Not mine and Couldn't finish** stand as topics v1.0.0 rules 17 to 20, with "Couldn't finish" quoting the agent's own words (computer-today v1.1.0 rule 10).

## Every word

| Key | English | Where |
| --- | --- | --- |
| topic.back | ‹ Today | Topic |
| topic.seen | Seen {n} of the last 7 days | Topic |
| topic.next | Next | Topic |
| topic.done | Done for this topic | Topic |
| topic.saw | What Isoform saw | Topic |
| topic.ask | Ask about {topic} › | Topic |
| topic.waitingTurn | Waiting its turn · starts when {other} finishes. Two run on this computer at once. | Proposal |
| find.ready | Ready | ⌘K group |
| find.conversations | Conversations | ⌘K group |
| find.people | People | ⌘K group |
| find.answers | Your answers | ⌘K group |
| find.goTo | Go to | ⌘K group |
| find.foot | What you search for is kept on this computer, in Activity. | ⌘K foot |

## Proof

On Bo's computer, a named build:

1. **A topic from its item:** clicking "Halcyon import · from your topics" opens the topic in Today's column; Y on Next opens the hand-off sheet.
2. **⌘K:** typing "lena" shows Prep for Lena first; ↩ opens it; the search appears in Activity.
3. **No false Working:** a topic's proposal handed to an agent in its own app reads "Opened in…" then "has it", never "Working on it".

## On the map (shell)

- FD1 · A topic opens from its item's source line and from ⌘K, in Today's column: Next, Done, What Isoform saw, Ask about it.
- FD2 · ⌘K over any page: Ready first, then conversations, people, claims, your answers, go to; Why and Forget on each; searches kept in Activity.
- FD3 · Topics' proposals follow agents-join's status words; "Waiting its turn" only for agents Isoform starts on this computer.
