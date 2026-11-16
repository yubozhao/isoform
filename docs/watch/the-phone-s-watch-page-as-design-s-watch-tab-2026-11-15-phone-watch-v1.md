# The phone's Watch page, as design's Watch tab (2026-11-15, phone-watch v1.0.0, part 1)

Design's phone pass makes Watch a tab. This is its content; the tab bar entry is a later piece
(after the Today redesign, #1136, removes Tasks).

- `WatchTabView` (`ios/Yansu/WatchTabView.swift`): Now (the watch's state in a word and a line),
  today's hours as columns, today's conversations, earlier days folded, and "Notes for long
  talks". It opens from the "Your watch" card on Today and from Settings › Watch ("What your watch
  heard"), where the old page opened.
- Conversations come from the computer (`GET /recordings/conversations`, PROTOCOL.md 24.11, #1106)
  and are kept in memory only. A computer that does not serve them yet shows "Update Isoform on
  your computer…".
- The old page (hours, where each is, Sync now, play, delete) stays one tap away: a plain row,
  "Hours and where they are", always on the page, and the amber line, which shows only when an
  hour has waited for the computer 30 minutes or one could not be sent. The row stays until a
  conversation's own page can play and delete (hub, 2026-11-15: no regressions).
- Nothing on the page starts or stops listening: that is on the watch.
- Not yet, from the handoff: the mark's poses, a conversation's own page, the designed Where
  sheet, the Closed Eyes striped rows, "heard talk a minute ago", the setup card's Install button,
  heart rate, place and the other watch settings on this page, and the tab itself.
