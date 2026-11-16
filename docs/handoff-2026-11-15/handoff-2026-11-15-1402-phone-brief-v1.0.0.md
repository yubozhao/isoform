# Phone brief v1.0.0: the brief in full opens at once and plays, its items answered where they're mentioned

Version 1.0.0 · written November 15, 2026, 2:02 PM Pacific, by Claude Design.

**Part of the phone pass of November 15.** Read phone-map v1.0.0 first. This file is the brief read in full, its player, and Past briefs. Brief rules v1.0.0 (November 5) stand.

**Board:** Isoform iPhone, artboard 3 (The brief).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **It opens at once and plays,** from what this phone has saved: text, audio and items, with no network.
2. **Its quality:** the words are easy to follow while it plays; the time it was made is plain.
3. **Its items are judged where they're mentioned,** the same items as in Your work.
4. **Any sentence can be answered:** Reply about this, or Not right.
5. **Past briefs,** with what's left in each.

**Before:** walk-1114-ios 05 and 06 (the brief: Past briefs arrows, the player, paragraphs, chips "Prep me for my 2:00 with Lena"), 07 (Past briefs, empty).

## The brief in full

1. **Title: the kind and the time it was made,** "Evening brief · 6:00 PM", and under it the day and "made from today on your computer". The back button says "Today".
2. **The player at the top:** play and pause, the bar with where it is and its length, 15 seconds back, the speed, 15 seconds on.
3. **Read-along:** while it plays, the sentence being read is highlighted and the page keeps it in view. Scrolling away stops the follow until play is tapped again.
4. **Nothing waits on the network** (B1): the text, audio and items are the saved package. If the audio isn't on the phone yet, the text shows at once and the player says "The audio comes when your computer wakes."
5. **Its items sit where the brief mentions them,** as compact items: where it came from, the title, and Yes, No, Not mine (38 points tall). Edit and ••• as everywhere. An answered one is one line with a check, what happened and Undo. They are the same items as in Your work; an answer in either place is the answer in both (B2).
6. **Every paragraph can be answered** by pressing and holding it, or with the two words under the last one read: **Reply about this** (opens Chat with the sentence attached) and **Not right** (a No on that sentence, with the reasons for a claim: Was true once, Only sometimes, Not at all).
7. **"In this brief"** at the end lists its sources by day and kind, each opening its Why; the old "Sources" sheet becomes this list.
8. **Swiping left and right moves between briefs,** newest to the right.
9. **The mini player:** leaving the page while it plays keeps it in a bar above the tabs, with the title, where it is, and pause. Tapping it comes back.

## Past briefs

10. **By day, newest first:** Today, Yesterday, then the date. Each brief is one row: its kind and time, and one line on where you are with it:
    - "Part heard · 3 to answer"
    - "Played · all answered"
    - "Not opened"
    - "Played · 1 left unanswered"
11. **A dot** for a brief not yet played through; an open ring once played.
12. **Kept on this phone for 30 days;** "Your computer keeps every brief." under the list.
13. **Items in an old brief** stay answerable until tomorrow's build; after that they show as they were left, and the answers are on the computer.

## What goes

14. The ‹ Past briefs › arrows at the top of the page (swiping replaces them).
15. The chips that repeated the brief's items ("Prep me for my 2:00 with Lena"): the items themselves sit there now.
16. "Read all" opening a second, different reading of the same brief: there is one brief page.

## What the build needs

B1 (the package whole, with audio, saved before it's shown), B2 (the same item ids as Your work), B3 (answers, including Not right on a sentence), B10 (Reply about this carries the sentence's id).

## Every word

| Key | English | Where |
| --- | --- | --- |
| brief.made | {day} · made from today on your computer | Under the title |
| brief.back15 | 15 s back | Player |
| brief.on15 | 15 s on | Player |
| brief.audioLater | The audio comes when your computer wakes. | Player, no audio yet |
| brief.reply | Reply about this | Under a paragraph |
| brief.notRight | Not right | Under a paragraph |
| brief.sources | In this brief | End of the page |
| brief.mini | {title} · {at} of {length} | Mini player |
| past.title | Past briefs | Page |
| past.partHeard | Part heard | Row |
| past.played | Played | Row |
| past.notOpened | Not opened | Row |
| past.toAnswer | {n} to answer | Row |
| past.allAnswered | all answered | Row |
| past.leftOpen | {n} left unanswered | Row |
| past.kept | Kept on this phone for 30 days. Your computer keeps every brief. | Under the list |

## Proof

On Bo's iPhone, a named TestFlight build, against the befores above:

1. **Offline:** in airplane mode, the evening brief opens and plays from the first tap; a recording.
2. **Read-along:** the highlight follows the audio sentence by sentence over a full brief; a recording.
3. **One item, two places:** Prep for Lena answered inside the brief shows answered in Your work, and the reverse.
4. **Not right:** holding a sentence and choosing Not right records a No on it, which the computer's Your answers shows.
5. **Past briefs** shows the last three days with what's left in each.

## On the map (phone)

- P221 · The brief in full opens and plays offline; the sentence being read is highlighted.
- P222 · The brief's items sit where they're mentioned, the same items as in Your work; any sentence has Reply about this and Not right.
- P223 · Past briefs, by day, with where you are with each and what's left.
