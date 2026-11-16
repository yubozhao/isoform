# Agent words v1.0.2: name the agent whenever Isoform knows which one

Version 1.0.2 · written November 15, 2026, 9:47 PM Pacific, by Claude Design.

**This file adds one rule to agent-words v1.0.0 and v1.0.1; everything in them stands.** One string slipped past v1.0.0's sweep: Chat's answer to a task says "That's work for your agent", then names Claude on the very next line and on the button. With Agent of You in the same app, "your agent" is the one phrase that can point at either.

**Board:** Isoform iPhone artboard 6 (Chat) and Isoform Computer artboard 6 (Chat and Quick chat) now show the new words.

A change goes in a new file with a new version; this one isn't edited after it's written.

## The rule

5. **When Isoform knows which agent, it says its name.** "Your agent" is only for an agent not yet chosen or still joining ("Waiting for your agent to join", "This link is for your agent." stand).

## The words that change

| Key | File | Now |
| --- | --- | --- |
| chat.agentWork | phone-chat v1.0.0 rule 12, computer-chat v1.0.0 rule 4 | That's work for {agent}. Here's what would go: |
| chat.agentFor | phone-chat v1.0.0 | It goes to {agent} with {what}. |

## Proof

A search of the shipped English strings for "your agent" finds only the joining words in agents-join v1.0.0 and phone-agent v1.1.0, "Your agent for code" and "Your agents" as agent-words v1.0.0 set them.

## On the map (meta)

Handoff list only; no new row.
