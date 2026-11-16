# Brief agents v1.0.0: what your agents did, in the brief

Version 1.0.0 · written November 15, 2026, 9:03 PM Pacific, by Claude Design.

**Why this file:** the brief is rank 1 on every device, and since today a yes sends work to your agents. Nothing says what the brief tells you about them. Without a rule, either the brief stays silent about work you handed out, or it guesses ("still working on…"). This file sets one paragraph, its order, and its words. It adds to redesign-today v1.0.0 (the brief's parts) and phone-brief v1.0.0 (the brief in full); both stand.

**Board:** Isoform iPhone (https://claude.ai/artifact/WKCdoNzqXTrsGG4aerRFrs), new artboard 13 (Your agents in the brief), frames 1 and 2.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **The brief says what came back** since the last one, so judging it is one tap away.
2. **It never guesses** what an agent is doing.
3. **It stays short:** one paragraph, at most two items.

## The paragraph

1. **One paragraph, "Your agents:",** when anything happened with your agents since the last brief; none when nothing did. It sits after what's due before the next brief and For your meeting, and before Asked of you (redesign-today rule 5's order, with this between).
2. **In this order:** results back to judge ("Codex finished the test plan for the Halcyon import, 14 cases; two fail today, the ones Lena will ask about."), then requests between agents waiting for you, then hand-offs with no word for more than two hours ("Claude has had the Q4 plan outline since 4:10 PM, with no word yet.").
3. **Overnight, in the morning brief,** work a cloud agent or this computer finished leads: "Overnight: Devin finished the migration script on a new branch, and its checks pass. It's waiting for you to judge."
4. **Only what Isoform knows,** in agents-join §7's words: "finished", "has had … since", "no word yet". Never "working on", "almost done", or a time it'll be done.

## Items

5. **Up to two agent items sit under the paragraph,** as compact items: a result ("Back from Codex · you said yes at 4:10 PM", its one sentence, Useful and Not useful) and a request ("Codex asks Muse · waits until {time} tomorrow", the question, what would move, Yes and No). They count toward the brief's five items (redesign-today rule 5).
6. **One answer counts in both places:** answered in the brief, it's answered in Your work, and the reverse (phone-brief rule 5).
7. **Hand-offs with no word aren't items;** the sentence names them and where to act ("you can ask again or take it back from Your work").

## The summary

8. **The brief's two or three sentences mention agents only when an agent item is among its items** (redesign-today rule 2: the summary names only what the items hold).

## What the build needs

The brief package (B1) carries, since the last brief: each result back (with its sentence and file), each request waiting, and each hand-off with no word for two hours, from the same records as Agents (C2, C3).

## Every word

| Key | English | Where |
| --- | --- | --- |
| brief.agentsLead | Your agents: | Paragraph start |
| brief.agentFinished | {agent} finished {title}{, what it found}. | Paragraph |
| brief.agentAsks | {from} also wants {to} to {what}. | Paragraph |
| brief.agentNoWord | {agent} has had {title} since {time}, with no word yet. | Paragraph |
| brief.overnight | Overnight: {agent} finished {title}{, what it found}. It's waiting for you to judge. | Morning paragraph |
| brief.whereToAct | You can ask again or take it back from Your work. | After no word |
| brief.requestWaits | {from} asks {to} · waits until {time} tomorrow | Compact item |

## Proof

On Bo's iPhone and computer, named builds, over one day with two hand-offs:

1. **It says what came back:** the evening brief's agents paragraph names the result, and its compact item answered in the brief shows answered in Your work.
2. **It never guesses:** with Claude holding a hand-off in its app, the brief says "has had … since" and nothing about working.
3. **Silent when nothing happened:** a day with no hand-offs has no agents paragraph.

## On the map (today)

- R94 · The brief's "Your agents:" paragraph: results to judge, requests waiting, then no word since; up to two agent items; never a guess.
