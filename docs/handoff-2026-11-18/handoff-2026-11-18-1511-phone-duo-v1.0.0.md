# Phone Duo v1.0.0: the fold splits the page, the left side reads, the right side answers, and nothing is drawn over either

Version 1.0.0 · written November 18, 2026, 3:11 PM Pacific, by Claude Design.

**This file answers the hub's urgent question for Bo ("Terrible quality", his open iPhone Duo, build 20261118212709, 3:00 PM). It sets the open Duo's layout for every tab and Today's in full. It changes phone-today v1.0.0 rule 1 (and rules 4 and 5 on the open Duo), phone-map v1.0.0 rule 12, hub-answers-night v1.0.0 rule 12, and phone-watch v1.1.0 rules 2, 5, 18 and 20 where it says so; everything else in them stands.**

A change goes in a new file with a new version; this one isn't edited after it's written.

**Before:** Bo's still, saved as `phone-duo-1.0/before-20261118212709.png` in Claude outputs. Read against `claude/watch-tab-in-the-wide-layout` at 804421684 (read-only): `DuoLayout.frames`, `DuoWideRootView`, `FeedView`.

## What's wrong now

1. **Pinned things are drawn over the page.** The watch line is pinned into the scroll's top inset under a clear bar, so the brief's last line runs under the header and the amber "1 hour couldn't be sent." covers "Read all ›".
2. **Every control is there twice or three times:**
   - "Today" is the left pane's title and is centred again in the right pane's bar.
   - The person button is at the top of the rail and again in the bar, and Settings is a third time in the rail's "…".
   - The mark is in a big circle in the bar and is also the Watch item (phone-watch v1.1.0).
3. **The panes ignore the fold.**
   - The left pane is 250 wide, so the right pane straddles the fold. Your work is pushed right of the fold while the brief card above it isn't, and the column breaks.
   - The left pane holds six lines of the brief and then nothing, and the same brief is in the right pane.
4. **Prep for 2:00 PM is still asked at 3:00 PM.**

## The open Duo, every tab

1. **The fold splits the page.**
   - The left pane runs from the screen's edge to the fold, and the right pane from the fold to the rail. When the phone reports no fold, the split is at the middle.
   - Nothing crosses the fold, so nothing has to dodge it, and the per-section fold clearing goes.
   - This replaces the fixed widths: Today's 250 or 360, Chat's 300, and Watch's 360 (phone-watch v1.1.0 rule 5).
2. **24 pt inside each pane, on both sides.** Every section in a pane starts on the same left edge.
3. **One title, at the top of the left pane,** in the large title style: "Today", "Watch" or "Chat". While the right pane shows the tab's own page, it has no title and no bar.
4. **The rail is the app's only chrome:** the person button at the top (Settings, the one Settings button on screen), then **Today · Watch · Chat**.
   - The "…" capsule goes. Settings is the person button, and Close is the open page's back button.
   - This changes phone-watch v1.1.0 rule 2's "Today · Watch · Chat · …".
5. **Both panes start on one line:** the left pane's title and the right pane's first heading share a top edge, just under the status bar.
6. **Nothing is pinned over the page.** Each pane scrolls on its own, and titles, lines and headings scroll with their content. The band behind the status bar is solid in the page's color (P137), so nothing scrolled reads under the clock.
7. **A page opened in the right pane gets the system bar, in that pane only.** That covers a topic, a hand-off, Past briefs and a conversation.
   - The bar holds the back button named for the tab ("Today", "Watch" or "Chat"), the page's title inline, and Full screen at the trailing end.
   - It's solid in the page's color (P137), and the content starts under it.
   - The left pane stays as it was.

## Today on the open Duo

8. **The left pane is for reading.** It holds:
   - "Today";
   - the date;
   - the line (rule 12);
   - the brief card in full: its kind and time, Hear it with its length, the ••• menu, and the whole brief as Read all shows it (phone-brief v1.0.0).

   There's no "Read all ›", and the card doesn't fold. phone-today rule 5 is for the narrow layout, where the brief and Your work share one screen. Every state in phone-today rule 7 stands, in this card.
9. **The right pane is for answering.** "Your work · 2 to answer" is at the top, then the open item, Up next, With your agent and Done today (phone-today rules 8 to 16).
   - Tapping an Up next row makes it the open item in place (rule 13).
   - Nothing replaces the left pane: the pane that switched to a list of cards when an item was picked goes.
10. **The brief is on screen once.** The right pane has no brief card.
11. **Today doesn't hold Now.** Now is the Watch tab's (phone-watch v1.1.0 rule 8). Today carries the one line, and the line opens the tab.

## The line and the mark (both layouts)

12. **Today's line** (phone-watch v1.1.0 rules 18 and 19) sits under the date, scrolls with the page and opens Watch. It's never pinned.
    - It adds Closed Eyes, which comes before the other states, in amber: "Closed Eyes until 12:04 PM · nothing is seen or heard", or "Closed Eyes until you end it · nothing is seen or heard".
    - With no watch paired, the line shows only during Closed Eyes.
13. **The mark is in one place: the Watch item** in the rail and the tab bar (phone-watch v1.1.0 rule 3), with its pose, its motion at rest and its amber dot.
    - Titles are words only. Today's and Chat's titles lose the mark in both layouts, and the circle goes.
    - Why: since v1.1.0 put the mark in the rail and tab bar, a mark in a title says the same thing a second time on the same screen.
    - This changes phone-map rule 12, phone-today rule 1, hub-answers-night rule 12 and phone-watch v1.1.0 rule 20.
    - With no watch paired, there's no Watch item and no mark; Closed Eyes shows in Today's line (rule 12).
14. **Narrow:** the header is "Today", the date under it and the person button at the right. Everything else in phone-today stands.

## Work whose time has passed

15. **Prep leaves Your work when its event starts.**
    - Unanswered prep for a 2:00 PM meeting is gone at 2:00 PM, with no line about it; nothing is ever "missed".
    - Answered prep stays where its answer put it: Done today, or With your agent.
    - The same goes for any item whose question names a time that has passed.

## Every word

| Key | English | Where |
| --- | --- | --- |
| today.watch.closedEyes | Closed Eyes until {time} · nothing is seen or heard | Today's line, amber |
| today.watch.closedEyesOpen | Closed Eyes until you end it · nothing is seen or heard | Today's line, amber |

Retired on the open Duo:
- the title in the right pane's bar, and the bar's person button;
- the mark's circle;
- the rail's "…" and its "More", "Settings" and "Close";
- "Read all ›".

## Proof

On Bo's open Duo, on a named TestFlight build, against the before:

1. **Today at the top, and with each pane scrolled to its end:**
   - one title, one person button and no "…";
   - nothing drawn over content and nothing across the fold;
   - Your work, Up next and Done today on one left edge.
2. **The brief once, in full, in the left pane,** with Hear it.
3. **A page opened from Your work** opens in the right pane with "Today" as its back button, and the brief stays.
4. **Today's line**, plain, then with Closed Eyes on (amber, first).
5. **At 2:00 PM**, that hour's unanswered prep is gone from Your work.
6. **On a narrow iPhone**, "Today" has no mark, and the Watch tab item is the mark.

## On the map (phone)

- P272 · The open Duo splits at the fold: each pane on its own side, with 24 pt margins and nothing across the fold, on every tab (replaces the fixed pane widths).
- P273 · One title, at the top of the left pane; the rail is the only chrome (the person button, then Today · Watch · Chat); no "…".
- P274 · Nothing pinned over the page on the Duo: each pane scrolls on its own; there's no bar in the right pane until a page opens there, then the system bar with a back button, solid (P137).
- P275 · Today on the open Duo: the left pane reads (title, date, line, the brief in full) and the right pane answers (Your work from the top); the brief is on screen once.
- P276 · The mark is only the Watch item; titles are words; Today's line shows Closed Eyes first, and alone when no watch is paired.
- P277 · Work whose time has passed leaves Your work without a word.
