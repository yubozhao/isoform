# The wide phone splits at the fold, and Watch is one of its tabs

Design: `docs/handoff-2026-11-18/` phone-duo v1.0.0 (rules 1 to 7 and 13), phone-watch v1.1.0
(rules 2, 3, 5, 6) and phone-watch v1.2.0 (rule 7, the dot). Bo, 2026-11-18, on his open Duo:
Watch could only be reached through Settings, controls were on screen two and three times, and
the panes ignored the fold.

## The shell (`DuoWideRootView`, `DuoLayout`)

- **The fold splits the page**, on every tab and in every state: the left pane runs from the
  edge to the fold, the right pane from the fold to the rail (`DuoLayout.frames`). With no fold
  reported the split is the middle. The fixed widths (250, 360, 300) are gone.
- **24 pt inside each pane** (`DuoLayout.paneInset`); both panes begin on one line
  (`DuoLayout.paneTop`).
- **One title**, at the top of the left pane, in the large title style, scrolling with the pane
  (`DuoPaneTitle`). The right pane has no title and no bar while it shows the tab's own page.
- **The rail is the only chrome:** the person button (Settings), then Today · Watch · Chat. The
  "…" capsule and the second person button are gone.
- **Nothing is pinned by the shell**, and the band behind the status bar is solid.
- **A page opened in the right pane gets the system bar there** (`DuoOpenedPage`): a back button
  named for the tab, the title inline, Full screen at the trailing end
  (`DuoFullScreenButton`). Used for a topic, a Today item and a watch conversation.

## Watch

- A tab in the rail between Today and Chat when the phone has a watch (the same rule as the
  narrow bar, `showsWatchTab`). It opens in the main area; the sheet is gone.
- The item, in the rail and in the narrow tab bar, is the mark in the watch's pose
  (`WatchPoseMark`): the lifted brow in the accent while it listens, the cap in amber in Closed
  Eyes, the shut eye in grey otherwise. An amber dot sits on it for Not listening, an hour that
  could not be sent, or hours on the phone that cannot go (the last send did not get through and
  30 minutes have passed). Hours that are going leave it plain.
- Left pane: the title, Now, the hours link, the watch's settings. Right pane: today's hours,
  conversations, earlier days; a conversation opens there with "Watch" as its back button.
- The mark is gone from Today's title in both layouts; it lives only in the Watch item.

## The UI tests' window

The wide UI tests laid the phone out 890 wide on an upright iPhone, where only the left 393
points are on screen. With the split at the fold, the whole right pane was off screen. They now
turn the simulator on its side and use a window of 852 by 393 (`YANSU_UITEST_DUO_SIZE=852x393`),
where both panes and the rail are on screen. The 890 by 626 window still exists for a real Duo.

## Not in this change

- Today's content on the wide phone (left pane: date, line, the brief in full; right pane: Your
  work) and the watch line under the date: the Today session's.
- The Watch page itself (Now with a reason and a time, Where your hours are, Hours by place):
  the next PR.
- The drawing of the mark's poses is a first rendering from design's words; design has not
  seen it.
