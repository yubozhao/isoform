# A part with iOS is judged by what arrives, not by minutes

## What was seen

2026-11-21, Bo's phone, relay log only (status, short id, counts). The phone woke at 08:00,
08:15 and 08:30, for 31, 33 and 19 seconds.

- One part (7f271f39) lost the same two pieces in all three wakes. Each was cut at exactly
  65536 bytes, seconds after the phone's own ask for that part (asked 08:30:43, cut 08:30:45).
  A cut two seconds into a wake is not the wake ending. It is a cancel.
- The four parts from noon the day before (af7a93ae, a850d4ae, b50be502, 0e6cbc7d) had 130
  answered asks, 16 pieces cut the same way, and not one piece stored.
- Every pass record said "stopped by nothing" and "held parts started afresh 0".

## Why

A part counted as stuck after five minutes with iOS. The app wakes every fifteen minutes for
about half a minute, and iOS sends what it holds mostly while the app is awake. So at every
wake every part still with iOS was five minutes old, was stopped, and was handed over again.
The new uploads waited for the next wake, where they were stopped again.

There were two places that did this. `lookAtHeld` (then `handAgain`) counted it. The hand-over
itself (`RecordingBackgroundUploads.handOver`) also stopped pieces older than five minutes, and
the pass reaches it for every part it comes to send. That one counted nothing and left no word.

The pass also waited up to 20 seconds for the first part that was on its way, in a wake of 19
to 33 seconds.

## What changed (phone only)

- Minutes only decide when a part is looked at. The relay is asked what it holds. The part is
  left alone the first time, and every time the relay holds more than at the last look.
- After three looks in a row that found no more, each at least five minutes after the one
  before, what iOS holds is stopped and the missing pieces are handed over again (in a wake the
  app sends them itself when the computer allows it). From the first look that is about an
  hour of wakes.
- The hand-over never stops pieces. One place decides a fresh start.
- In the background the pass hands a part over and goes on. It waits only with the app open.
- Every way out of a held part leaves a word in `stoppedBy`: `held-young`, `held-seen`,
  `held-growing`, `held-no-progress-<n>`, `held-afresh-no-progress`, `held-afresh-in-wake`,
  `held-whole`, and as before `held-ask-<why>`, `held-afresh-unasked`, `held-nothing-missing`,
  `held-hand-over-<why>`.

The fresh start after three asks without an answer stays. Nothing on the phone is deleted: only
iOS's upload tasks and their piece copies are stopped, never the part's file or its place among
the waiting parts.

## What to look for

- No more pieces cut at 65536 bytes seconds after an ask for the same part.
- `held-seen` and `held-growing` in the pass lines, then parts turning whole.
- The four noon parts: stored pieces at last, or `held-afresh-no-progress` after about an hour.

## Not known

Whether iOS sends anything it was given in the background between wakes. If not, a part needs
at least two wakes; the send in a wake (#1356) is the answer to that.
