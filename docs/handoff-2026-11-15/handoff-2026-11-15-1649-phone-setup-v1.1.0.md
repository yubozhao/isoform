# Phone setup v1.1.0: four screens on what Isoform does, before pairing

Version 1.1.0 · written November 15, 2026, 4:49 PM Pacific, by Claude Design.

**This file changes phone-setup v1.0.0; read that first.** It replaces v1.0.0's first run (rules 1 and 2) with four screens before pairing, and gives the pair screen a line for people without Isoform on their computer. Everything else in v1.0.0 stands.

**Bo, November 15:** "First run is great. But before pairing with your computer, we should tell them what can they do. So have a little bit of onboarding process that show up."

**Board:** Isoform iPhone, artboard 9, frames 9a to 9e.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Say what Isoform does, in the app's own order,** in under a minute.
2. **Paired in one scan;** nothing asked before it.
3. Then v1.0.0's goals: the latest brief at once, permissions when needed, a lost pairing never blanks Today.

## Four screens, then pairing

1. **Each screen opens on the real thing** (a brief card, an item, a conversation, the Closed Eyes switch) in a soft panel at the top, then a title, one or two sentences, page dots, **Continue**, and **Skip** at the top right. Skip goes straight to pairing. Nothing is asked on these screens.
2. **In the app's order of importance:**
   - **9a · "Your day, in two minutes."** "Every morning and evening Isoform gives you a brief of what matters, made from your day on your computer. Read it or hear it." (the evening brief card)
   - **9b · "Say yes, and your agent does it."** "Isoform finds the work: prep, replies, the next step. You say yes, no or not mine. A yes goes to the agent you already use; what it makes comes back for you to judge." (an item with Yes, No, Not mine, and the agents by name: Claude, ChatGPT, Codex, Muse, Instinct)
   - **9c · "Your watch hears what was said."** labelled "With Apple Watch": "It listens while it's on your wrist; tap to mark what matters. Your computer turns the talk into notes, and into what you said you'd do." (a conversation with its note and "1 to answer")
   - **9d · "Yours, and on your devices."** "Everything Isoform keeps stays on your devices. Nothing goes to an agent until you say yes, and you see exactly what goes. Closed Eyes stops it everywhere at once." (the Closed Eyes switch and "Kept on your computer")
3. **9c shows for everyone,** labelled so no one thinks a watch is needed; it isn't skipped for people without one, because the watch is part of what Isoform is.
4. **The samples are the app's own parts with sample content,** drawn from the same components as Today, Your work and the Watch tab, so the screens change when the app does.

## The pair screen

Frame 9e. Replaces v1.0.0 rule 1.

5. **The mark, "Pair with your computer", and "Isoform runs on your computer. On it, open Isoform's Phone page and choose Pair a phone."**, then **Scan the code** (the camera is asked for here, v1.0.0 rule 3).
6. **"Don't have it yet? Open isoform.ai on your computer to download it."** under the button. Nothing to type or sign up for on the phone (the website's own rule since v19.0.0).

## Every word

| Key | English | Where |
| --- | --- | --- |
| ob.skip | Skip | Every screen |
| ob.continue | Continue | Every screen |
| ob.1.title | Your day, in two minutes. | 9a |
| ob.1.body | Every morning and evening Isoform gives you a brief of what matters, made from your day on your computer. Read it or hear it. | 9a |
| ob.2.title | Say yes, and your agent does it. | 9b |
| ob.2.body | Isoform finds the work: prep, replies, the next step. You say yes, no or not mine. A yes goes to the agent you already use; what it makes comes back for you to judge. | 9b |
| ob.2.agents | a Yes goes to your agent | 9b panel |
| ob.3.label | With Apple Watch | 9c |
| ob.3.title | Your watch hears what was said. | 9c |
| ob.3.body | It listens while it's on your wrist; tap to mark what matters. Your computer turns the talk into notes, and into what you said you'd do. | 9c |
| ob.4.title | Yours, and on your devices. | 9d |
| ob.4.body | Everything Isoform keeps stays on your devices. Nothing goes to an agent until you say yes, and you see exactly what goes. Closed Eyes stops it everywhere at once. | 9d |
| setup.pairTitle | Pair with your computer | 9e |
| setup.pairBody | Isoform runs on your computer. On it, open Isoform's Phone page and choose Pair a phone. | 9e |
| setup.scan | Scan the code | 9e |
| setup.noApp | Don't have it yet? Open isoform.ai on your computer to download it. | 9e |

v1.0.0's setup.title and setup.body ("Isoform on your phone") go; the rest of its words stand.

## Proof

On a fresh install on Bo's iPhone, a named TestFlight build. **Before:** phone-setup v1.0.0's single first-run screen (or the build's FirstRunView).

1. **Four screens, then pair:** a screen recording from first launch through 9a to 9d to the pair screen, under a minute.
2. **Skip works:** Skip on 9a lands on the pair screen.
3. **Nothing asked:** no system prompt appears before Scan the code.

## On the map (phone)

- P249 · Four screens before pairing, in the app's order (the brief, judging and your agents, the watch, trust), each opening on the real thing, with Skip (replaces part of P241).
- P250 · The pair screen tells people without Isoform on their computer to open isoform.ai there.
