# Pass access v1.0.0: the phone and computer passes by keyboard, VoiceOver, large text and reduced motion; the brief's 38-point buttons become 44

Version 1.0.0 · written November 15, 2026, 9:06 PM Pacific, by Claude Design.

**Why this file:** the passes say how pages look and what they say, not how they work without a mouse, without sight, at large text, or with motion reduced. Judging work is rank 2, and it has to be one key or one gesture for everyone. This file sets those rules for every page of the phone and computer passes, and fixes one target that's too small.

**Board:** Isoform Computer, artboard 17 (Keyboard and VoiceOver); Isoform iPhone, artboard 14 (Large text and VoiceOver).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Answering an item is one key or one gesture** with a keyboard, VoiceOver, Switch Control or large text.
2. **Nothing is said by colour or motion alone.**
3. **Every target is at least 44 points on the phone.**

## The computer

1. **Keys on every page** (artboard 17): ⌘1 to ⌘5 for Today, Chat, Agents, Activity, You; ⌘, Settings; ⌘K find; ⌥Space Quick chat and ⌘⌃⌥E Closed Eyes from any app (pass-questions v1.0.0); ↑ ↓ move between items; Y N M E answer the focused item; ⌘Z undo; Space plays the brief; ↩ opens and ⌘↩ sends in a sheet; Esc closes a sheet and focus returns to its item.
2. **Y, N, M and E work only when an item has focus and no text field does.** Typing in Chat or Edit never answers anything.
3. **Focus order on Today:** the sidebar, the brief, its words and links, the open item, Up next, With your agents, Done today. The focus ring is the accent, 2 px, never removed; the open item's ring is the same ring.
4. **VoiceOver reads an item as one element,** in reading order (source, sentence, the work, what Yes does), then its actions (Yes, No, Not mine, Edit). Key hints are hidden from VoiceOver; each button's name is read once.
5. **Counts and states are words:** "Today, 4 to answer"; "Agents, 1 waiting for your yes"; an agent row reads "Codex, for code, on this computer, working: the test plan". The green dot and amber are never the only signal; the words beside them are what VoiceOver reads.
6. **Zoom:** ⌘+ and ⌘− from 90% to 150%; below 1,100 points of content width, Today's two columns stack (the open item first) and Agents' three columns become one, in the same order.

## The phone

7. **Large text:** every page follows Dynamic Type to the largest accessibility size. From the first accessibility size, an item's answers stack full width, Yes first, each 56 points tall; text wraps and never truncates; the brief card folds to its title so Your work starts on the first screen (artboard 14, frame 1).
8. **The brief's compact items are 44 points tall,** not 38 (changes phone-brief v1.0.0 rule 5): 44 is Apple's minimum target.
9. **VoiceOver:** an item is one element with its actions (Yes, No, Not mine, Edit) as custom actions; a result's are Useful and Not useful; an agents' request's are Yes and No. The brief's player reads "Hear it, 2 minutes" and its position as "41 seconds of 2 minutes 14".
10. **Haptics** confirm an answer (a light tap), never stand in for words.

## Both, and the watch

11. **Reduce Motion:** the mark's moments (glance, blink, the hello) and its ripple stop; it shows its still pose. Sheets fade instead of sliding. Hear it's waveform stands still.
12. **The mark always has a label:** "Listening", "Not listening", "Closed Eyes", on the watch, the phone's header, the sidebar's watch line and the menu bar.
13. **On the watch,** the mark button reads "Listening. Tap to mark a moment; hold for Closed Eyes." (watch v1.2.0's gestures).

## Proof

On named builds, with screen recordings:

1. **Keyboard only:** on the computer, from launch, Bo answers four items, hands one to Codex and opens Agents without touching the trackpad.
2. **VoiceOver:** on the phone, VoiceOver reads an item whole and answers it with the Yes action; on the computer, the same with VO-Space.
3. **Large text:** at the largest accessibility size, Today on the phone shows the stacked answers and nothing truncated (a screenshot).
4. **Targets:** the brief's compact buttons measure 44 points in the view debugger.

## On the map

- shell AX1 · The computer by keyboard: the keys, the focus order, Y N M E only on a focused item.
- shell AX2 · VoiceOver and zoom on the computer: one element per item with actions; states in words; columns stack under 1,100 points.
- phone P259 · Large text and VoiceOver on the phone: stacked answers, items as one element with actions; the brief's compact items 44 points.
- phone P260 · Reduce Motion and labels for the mark on every device.
