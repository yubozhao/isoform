# Your watch today (2026-11-15, the lane on the computer)

- At the top of "From your watch": one column for each clock hour since the watch first recorded
  today, as tall as the talk heard in it. An hour observing was paused in has a dashed outline, a
  quiet hour is an empty track, and the hour that is not over yet is lighter. No numbers and no
  running clock are printed; each column says its hour and its talk on hover and to assistive
  technology. It shows nothing until the watch has recorded something today.
- `wails-gui/watch_today_lane.go` computes it from what is stored: the recordings' parts, the
  speech in their transcripts (talk is estimated from the words, 2.5 a second), and the pause log.
- Design's v1.1.0 puts the lane at the top of Activity > Screen timeline. Here it is on the watch's
  own page: the Screen timeline is another owner's surface. Moving it there is a follow-up for
  whoever owns that page.
