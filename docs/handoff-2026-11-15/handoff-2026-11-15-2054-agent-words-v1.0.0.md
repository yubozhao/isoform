# Agent words v1.0.0: "your agents" are the agents that do your work; Agent of You is always called by its name

Version 1.0.0 · written November 15, 2026, 8:54 PM Pacific, by Claude Design.

**Why this file:** the phone and computer passes made "your agents" mean the agents that do your work (Claude, ChatGPT, Codex, Muse…): the Agents page, Settings › Your agents, "Your agent for code". Agent of You's owner side (agent-of-you-beta v2.1.11) and the You page's tag (redesign-you v1.0.0) use "your agent" for something else: your own agent that clients hire. On the same screen a person could read "Your agents · Codex" and "Your agent delivered a job" and not know which is which. I checked every surface's words for the phrase; this file settles it.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **One phrase, one meaning,** on every surface people see.
2. **The product's own name** for the thing clients hire, which is also Bo's public name for it.

## The rule

1. **"Your agent" and "your agents" mean only the agents that do your work** (phone-agent, computer-agents, agents-join).
2. **Agent of You is always called "Agent of You",** as a name, without "your": "Agent of You then works on its own." Never "your agent", "your Agent of You" or "the agent" on the owner's side.
3. **A client's side is unchanged:** a client sees the owner's name and "this agent" ("This agent is an AI. {name} can read this chat."), which can't be confused.

## The words that change

Each replaces the English of the key named, in the file named; every other word in those files stands.

From agent-of-you-beta v2.1.11 (the owner's side):

| Key | Was | Now |
| --- | --- | --- |
| inL | Four steps, then clients can hire your agent at isoform.ai/agents/{name}. | Four steps, then clients can hire Agent of You at isoform.ai/agents/{name}. |
| st1L | What your agent knows about how you work. You read and approve every line, and nothing else leaves this computer. | What Agent of You knows about how you work. You read and approve every line, and nothing else leaves this computer. |
| st4L | Your agent then works on its own. You can approve each job or review the work first; both are off to start. | Agent of You then works on its own. You can approve each job or review the work first; both are off to start. |
| wpFirst | This is everything your agent will know about how you work. Read each line, change or remove what you like, then approve. Only the lines you approve leave this computer. | This is everything Agent of You will know about how you work. Read each line, change or remove what you like, then approve. Only the lines you approve leave this computer. |
| wpLearning | … Until then you can add a line yourself and turn your agent on with it. | … Until then you can add a line yourself and turn Agent of You on with it. |
| wkL | {a} new, {b} changed, {c} to remove. Your agent keeps using version {v} until you approve. | {a} new, {b} changed, {c} to remove. Agent of You keeps using version {v} until you approve. |
| draftFail | Couldn't draft this week. Your agent keeps using version {v}. | Couldn't draft this week. Agent of You keeps using version {v}. |
| rateL | Clients pay this for each minute your agent works, and at least ${min} each time it starts. | Clients pay this for each minute Agent of You works, and at least ${min} each time it starts. |
| pay1 | Clients pay your rate for each minute your agent works, and at least ${min} each time it starts. | Clients pay your rate for each minute Agent of You works, and at least ${min} each time it starts. |
| pay3 | … the free questions your agent asks before a job, … | … the free questions Agent of You asks before a job, … |
| onL | Clients can hire your agent at isoform.ai/agents/{name}. | Clients can hire Agent of You at isoform.ai/agents/{name}. |
| apPlan | What your agent will make | What Agent of You will make |
| rvNote | Your agent's note | Note from Agent of You |
| blL | They can't start new work with your agent. Work already started finishes and is paid. | They can't start new work with Agent of You. Work already started finishes and is paid. |
| puDone | Your agent delivered a job. | Agent of You delivered a job. |
| puIn | You're in. Set up your agent on your computer. | You're in. Set up Agent of You on your computer. |

From redesign-you v1.0.0 (the claims Agent of You may say):

| Key | Was | Now |
| --- | --- | --- |
| about.confirmedMeta | {n} · your agent can say {m} | {n} · Agent of You can say {m} |
| about.agentTag | Your agent | Agent of You |
| about.agentMenu | Your agent can say this | Agent of You can say this |

From phone-settings v1.0.0 (these already mean your work agents, so they follow phone-agent v1.1.0's plural):

| Key | Was | Now |
| --- | --- | --- |
| set.agent | Your agent | Your agents |
| data.sent | Sent to your agent | Sent to your agents |

## Proof

On named builds of the computer, the phone and the website:

1. **A search of the shipped strings** (every locale file's English, and the website's copy.json) finds "your agent" only in work-agent strings; a list from the hub.
2. **Side by side:** Settings › Your agents and Settings › Agent of You on Bo's computer, in one screenshot, read as two different things.

## On the map (meta)

- MW1 · "Your agent(s)" means only the agents that do your work; Agent of You is always called by its name.
