# Phone chat v1.0.0: Chat narrows to your own life, corrections, and replies attached to what Isoform showed

Version 1.0.0 · written November 15, 2026, 2:02 PM Pacific, by Claude Design.

**Part of the phone pass of November 15.** Read phone-map v1.0.0 first. This file replaces redesign-phone v1.0.0's Chat (rules 13 and 14).

**The hub, from Bo's question whether we still need Chat** (14 days: 35 chat turns from 18 of 47 active users): "Chat stays but narrows: ask about your own life, correct and steer Isoform, and reply attached to an item (a brief paragraph, a topic, a proposal). No general-assistant features; anything task-like goes through Work on it."

**Board:** Isoform iPhone, artboard 7 (Chat: ask and correct).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Your message reaches the computer,** or waits on this phone and says so; never lost.
2. **Ask about your own life:** answered from what Isoform saw, with where it came from.
3. **Correct and steer:** a correction is a judgment; it says what changed, with Undo.
4. **Replies attached:** to a paragraph, an item, a topic or a conversation.
5. **Anything task-like becomes a hand-off,** not chat work.

**Before:** walk-1114-ios 08 (a topic opening in Chat) and 09 (Chat: "As of 10:23 AM", a proactive card "Priya is waiting on your reply" with Read the draft, Review, Not now).

## The page

1. **Title "Chat"; under it, what it's for:** "About your own day, and setting Isoform straight."
2. **The field:** "Ask about your day, or correct Isoform", with the microphone (hold to talk stands). Above it, the attachment chip when there is one ("About: Prep for Lena ×").
3. **Chat never speaks first.** No cards arrive in Chat by themselves (the held proactive cards stay held).

### Asking

4. **An answer comes from what Isoform saw,** and says where: a chip under it, "From your watch · Pricing with Sam, 2:05 PM ›", opening that conversation, email, call or brief.
5. **The claims it leans on are underlined;** tapping one opens its Why (redesign v1.0.0, rule 16), with Not this on each moment.
6. **An answer that points at work** names where it is ("the reply is drafted in Your work"), rather than repeating the work in Chat.
7. **"I don't know"** is said plainly when Isoform didn't see it: "I didn't see anything about hiring from Priya this week." Nothing is made up to fill it.

### Correcting

8. **A correction is a judgment.** The answer says what it changed, where, and offers Undo: "Got it. Halcyon goes with Priya from now on, not Lena." / "Changed: Who you know › Lena, Priya · Undo".
9. **It reaches what Isoform made:** a correction about a brief sentence changes tomorrow's brief ("Tomorrow's brief won't say it."); about an item, the item ("Prep for Lena no longer mentions the tests; it's still kept for 10:00 tomorrow.", with See the prep ›).

### Replies attached

10. **Reply opens Chat with the thing attached:** from an item's ••• (Reply), a brief sentence (Reply about this), a topic, a conversation, or a result ("Reply" at its top right). The chip names it: "About: Prep for Lena", "About your brief: "The Thursday review is slipping."".
11. **The reply acts on what it's attached to,** and the answer says what changed. This is the most common way into Chat from Today.

### Task-like asks

12. **Chat doesn't do the work.** "Draft the Q4 plan outline for Monday." gets "That's work for your agent. Here's what would go:" and a card: the title, "For your agent: it goes to Claude with your notes from Friday and last quarter's plan.", **Hand it to Claude** and **No**. Hand it to Claude opens the same sheet as a Yes in Your work (phone-agent v1.0.0); asking counts as a Yes for its source (redesign v1.0.0, rule 14).
13. **No general-assistant work:** no essays, code, trivia or web answers from Chat; they're either about the person's own life, or work for their agent.

### When the computer is away

14. **A message waits on this phone** with a clock and "Waits for your computer"; under it, once: "Your computer is asleep. Your question goes when it wakes; the answer comes here." The answer arrives in place.

## What goes

15. The proactive card in Chat ("Priya is waiting on your reply", Read the draft, Review, Not now).
16. "As of {time}" at the top of the thread.
17. Topic pages that open as a filtered Chat (a topic opens its own page from Your work, phone-today rule 17; Reply about this topic attaches it here).

## What the build needs

B10 (replies carry what they're attached to; a correction returns what changed, with Undo; a task-like ask returns a hand-off card; claims marked with their Why), B3 (corrections recorded as answers, surface "phone"), B4 (the hand-off).

## Every word

| Key | English | Where |
| --- | --- | --- |
| chat.title | Chat | Title |
| chat.sub | About your own day, and setting Isoform straight | Under the title |
| chat.field | Ask about your day, or correct Isoform | Field |
| chat.about | About: {title} | Attachment chip |
| chat.aboutBrief | About your brief: "{sentence}" | Attachment chip |
| chat.from | From {source} · {title}, {time} › | Source chip |
| chat.changed | Changed: {where} · Undo | Under a correction |
| chat.agentWork | That's work for your agent. Here's what would go: | Answer to a task-like ask |
| chat.agentFor | For your agent: it goes to {agent} with {what}. | Hand-off card |
| chat.handIt | Hand it to {agent} | Hand-off card |
| chat.sent | Sent to your computer | Under a message |
| chat.waits | Waits for your computer | Under a message |
| chat.asleep | Your computer is asleep. Your question goes when it wakes; the answer comes here. | Once, under a waiting message |
| chat.didntSee | I didn't see anything about {subject} {when}. | Answer, when nothing was seen |

## Proof

On Bo's iPhone, a named TestFlight build, against the befores above:

1. **Asked and sourced:** "What did Sam and I agree on at lunch?" answers with the watch's conversation as its source chip; tapping an underlined claim opens its Why.
2. **A correction sticks:** "Lena isn't on Halcyon anymore" changes Who you know, shows Undo, and the next brief doesn't put Halcyon with Lena.
3. **A reply attached:** Reply on Prep for Lena changes the prep; the item in Your work shows the change.
4. **Task-like:** "Draft the Q4 plan outline" returns the hand-off card, not a draft in Chat.
5. **Nothing proactive:** over a day, Chat shows no message Bo didn't start.

## On the map (phone)

- P235 · Chat asks about your own life and says where each answer came from, with claims underlined.
- P236 · A correction says what changed, where, with Undo; replies attach to a paragraph, item, topic, conversation or result.
- P237 · A task-like ask becomes the hand-off card; Chat's proactive card and "As of" go.
