# Agent of You beta v2.1.12: words only; the owner's side calls it "Agent of You", never "your agent"

Version 2.1.12 · written November 15, 2026, 9:12 PM Pacific, by Claude Design.

**This file changes words only.** Everything in agent-of-you-beta v2.1.11 stands except the English of the strings below, which follow agent-words v1.0.0: "your agent(s)" now means the agents that do your work (Codex, Claude…), so Agent of You is called by its name on the owner's side. The client's side ("{name}'s agent", "this agent") doesn't change.

**The copy files:** `Claude outputs/agent-of-you-beta-2.1.12/copy.json` and `copy.md`, made from v2.1.11's with exactly these changes; point the website's copy script at the new copy.json. The Chinese column is untouched (zh-CN waits).

A change goes in a new file with a new version; this one isn't edited after it's written.

## The strings

| Key | Now |
| --- | --- |
| owner.inL | Four steps, then clients can hire Agent of You at isoform.ai/agents/{name}. |
| owner.st1L | What Agent of You knows about how you work. You read and approve every line, and nothing else leaves this computer. |
| owner.st4L | Agent of You then works on its own. You can approve each job or review the work first; both are off to start. |
| owner.wpFirst | This is everything Agent of You will know about how you work. Read each line, change or remove what you like, then approve. Only the lines you approve leave this computer. |
| owner.wpLearning | … Until then you can add a line yourself and turn Agent of You on with it. |
| owner.wkL | {a} new, {b} changed, {c} to remove. Agent of You keeps using version {v} until you approve. |
| owner.draftFail | Couldn't draft this week. Agent of You keeps using version {v}. |
| owner.rateL | Clients pay this for each minute Agent of You works, and at least ${min} each time it starts. |
| owner.pay1 | Clients pay your rate for each minute Agent of You works, and at least ${min} each time it starts. |
| owner.pay3 | … the free questions Agent of You asks before a job, … |
| owner.onL | Clients can hire Agent of You at isoform.ai/agents/{name}. |
| jobs.apPlan | What Agent of You will make |
| jobs.rvNote | Note from Agent of You |
| jobs.blL | They can't start new work with Agent of You. Work already started finishes and is paid. |
| jobs.puDone | Agent of You delivered a job. |
| jobs.puIn | You're in. Set up Agent of You on your computer. |
| Email "in" (to the owner), second paragraph | Nothing about you leaves your computer until you approve the profile. Once it's on, Agent of You works on its own. You can choose to approve each job or review the work first. |

The last row wasn't in agent-words v1.0.0's table (it's an email body, not a key); it follows the same rule.

## Proof

1. **A search of the new copy.json and copy.md** for "your agent" in English finds none (checked: zero in both).
2. **On a named build,** Settings › Agent of You's four steps and the owner emails read as above.

## On the map (shell)

No new row: shell MW1 (agent-words v1.0.0) covers it.
