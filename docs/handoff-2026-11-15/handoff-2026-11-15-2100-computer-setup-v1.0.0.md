# Computer setup v1.0.0: from the first launch to the first day, in four screens and five steps

Version 1.0.0 · written November 15, 2026, 9:00 PM Pacific, by Claude Design.

**Why this file:** computer-map v1.0.0 rules 12 and 13 said what setup on the computer becomes (the phone's four screens first, then permissions one at a time, then Your agents, then the phone) but didn't draw it. Setup is the first thing anyone sees, and Bo's onboarding focus. This file draws it whole and settles its order. It changes redesign-shell v1.0.0's Setup (rules 1 to 6) where it says so; the rest stands.

**Board:** Isoform Computer, artboard 16 (Setup on the computer), frames 16a to 16f.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **People know what Isoform does before anything is asked.**
2. **Each permission says what it gives** at the moment it's asked.
3. **The first day has real work in it,** and the first brief is minutes away, not "tomorrow".
4. **Every step can be skipped** and done later in Settings.

## The four screens (16a, 16b)

1. **The phone's four screens, at the computer's size** (phone-setup v1.1.0 rules 1 to 4, and its words ob.*): Your day, in two minutes; Say yes, and your agent does it; Your watch hears what was said (labelled With Apple Watch); Yours, and on your devices.
2. **Layout:** the real thing on the left, drawn from the app's own parts with "Sample" under it; the title, two sentences, page dots and **Continue (↩)** on the right; **Skip** at the top right goes to step 1. No sidebar until setup is done.
3. **Nothing is asked on these screens:** no permission, no account, no name.

## The five steps

A rail on the left, "Set up Isoform", with the five steps, a tick on each one done, and "Each step can be skipped and done later in Settings." at its foot. Each step has **Skip this step** and **Continue (↩)**, bottom right. This replaces redesign-shell rule 1's four steps.

4. **1 · What Isoform sees** (16c): one row per permission, with what it gives in one line (redesign-shell rule 2's words), **Allow…** on each, the next one filled, then macOS's own prompt; "Allowed" with a tick after. Under the rows, "So far:" and what it can see now. **Messages and your agents' history are never asked in setup;** the line says they stay off until turned on in Settings (computer-settings rules 2 and 4).
5. **2 · Is this right?** redesign-shell rule 3 stands: three to five claims from calendar and mail, read a minute ago, each with Right? and Y, N, E.
6. **3 · What it can do today** (16d): day-one work as real items, answered with the same keys, and "What are you working on, or following?" with Add (Topics v1.0.0 rules 28 to 33). **No samples here:** with nothing to offer yet, one line says so and only the field shows.
7. **4 · Your agents** (16e, new): agents found on this computer with **Connect** (one click and the confirmation of computer-agents rule 12); the first one connected takes the job its kind suggests ("Connected · for code"); the message for any other agent, with Copy (agents-join §2). Skipped, a yes on offered work copies the hand-off until an agent connects.
8. **5 · Your phone and watch** (16f): the pairing code, "Open Isoform on your phone and choose Scan the code.", "Don't have it yet? Get it from the App Store on your phone; it's free.", "With Apple Watch, the watch app comes with it. After pairing, the phone asks once to hear a sentence of your voice." and a line while it waits ("Waiting for your phone · the code works for 10 minutes"). The primary button reads **Finish setup**.
9. **Finish setup lands on Today's first day** (computer-today v1.1.0 rules 1 to 3): the band says when the first brief comes, and Your work holds what step 3 left.

## The first brief

10. **The first brief comes at the next brief time, if that's at least an hour away;** otherwise a first brief is made an hour after setup, from what step 1 allowed. The band says the time either way ("Your first brief comes at 6:00 PM"). No one waits until tomorrow to hear what Isoform is.

## Every word

The four screens use phone-setup v1.1.0's ob.* keys. redesign-shell's setup.* words stand except where this replaces them.

| Key | English | Where |
| --- | --- | --- |
| setup.railTitle | Set up Isoform | Rail |
| setup.railFoot | Each step can be skipped and done later in Settings. | Rail |
| setup.step4 | Your agents | Rail |
| setup.step5 | Your phone and watch | Rail |
| setup.skipStep | Skip this step | Every step |
| setup.sample | Sample | Four screens |
| setup.allow | Allow… | Step 1 |
| setup.allowed | Allowed | Step 1 |
| setup.soFar | So far: {what it can see}. Messages and your agents' history stay off until you turn them on in Settings. | Step 1 |
| setup.todayLead | Made from what Isoform has already seen. Answer one, or skip; it waits in Your work. | Step 3 |
| setup.todayNothing | Nothing to offer yet. Tell Isoform what you're working on, and it starts there. | Step 3, empty |
| setup.agentsLead | A yes on offered work goes straight to the agents you already use. They see only what each hand-off sends. | Step 4 |
| setup.phoneLead | Your brief and your work on your phone; your watch hears what was said. | Step 5 |
| setup.scan | Open Isoform on your phone and choose Scan the code. | Step 5 |
| setup.noApp | Don't have it yet? Get it from the App Store on your phone; it's free. | Step 5 |
| setup.watchLine | With Apple Watch, the watch app comes with it. After pairing, the phone asks once to hear a sentence of your voice. | Step 5 |
| setup.waitingPhone | Waiting for your phone · the code works for 10 minutes | Step 5 |
| setup.finish | Finish setup | Step 5 |

## Proof

On a fresh account on Bo's computer, a named desktop build, with a screen recording from first launch:

1. **Nothing asked first:** the four screens show before any macOS prompt.
2. **One at a time:** each macOS prompt appears only after its row's Allow….
3. **Your agents:** Claude Code is found and connected in setup with one click and the confirmation.
4. **The first brief:** set up at 2 PM, the band says 6:00 PM and the brief arrives then; set up at 5:30 PM, it says 6:30 PM.

## On the map (shell)

- SU1 · Setup: four screens before anything is asked, then five steps in a rail, each skippable.
- SU2 · Step 4, Your agents, in setup: found on this computer with Connect; the message for any other.
- SU3 · The first brief comes the same day: at the next brief time if an hour away, otherwise an hour after setup.
