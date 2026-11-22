# The Watch page: where your hours are

Design: phone-watch v1.1.0 rules 8 to 16 and phone-watch v1.2.0, mirrored in
`docs/handoff-2026-11-18/`. Bo, 2026-11-18: "25 on this phone" said neither 25 of what, nor what
happens next, nor whether anything was his to do.

## Now

Every stop says since when: "Since 7:49 PM yesterday. Tap the mark on your watch to listen.",
"You held the mark to stop, at 3:00 PM. It listens again tomorrow.". The phone notes when it saw
the watch's state change (`WatchRecordingsModel.statusSince`, kept across launches). Not
listening and Closed Eyes are amber; the rest is plain. The amber pill under Now is gone: the
card below says it, once.

## Where your hours are

Always shown under Now (`WatchWhere`, `WatchTabView.whereCard`): three places, On your watch,
On this phone, On your computer, each with its count in hours; the place holding hours that
cannot go is ringed in amber. Under them one sentence:

- every hour across: "Every hour is on your computer, the last at 12:40 PM."
- the computer asleep, or out of reach (Try again after 30 minutes), or with Closed Eyes on;
- an hour that could not be sent, with Try again;
- hours on the watch;
- hours that are going: "6 hours are going to your computer now.", only once the send has run
  a few seconds;
- why they waited, only when iOS said so: "Isoform was closed on this phone, so 6 hours waited.
  They're going to your computer now." with "Swiping Isoform away stops sending until you open
  it again." under it, or the Background App Refresh sentence. iOS's cancel reason is read from
  the stopped upload (`NSURLErrorBackgroundTaskCancelledReasonKey`) and logged by its code only.
  The current reason wins, and the cause is forgotten once every hour is across.

Try again restarts any stuck send. "Sync now" and "Checked" are gone; pulling down checks at
once. "Hours by place" opens the list, which says "hours" everywhere and "This phone keeps the
audio only until your computer has it."

The forwarder now tells "going" from "cannot go": `Outcome.sending` when parts are only in the
system's hands.

## Conversations

Never an error alone: when the computer cannot be reached, the hours this phone holds are shown
in grey ("49 min of talk · waiting for your computer") under "Names and notes come from your
computer."

## Not in this change

- "Hours by place" grouped by day and then by place (rule 14); the list is still by day.
- "heard talk a minute ago" on Listening, and the reason for Not listening ("After your call",
  "Off your wrist", "Turned off on your phone"): the watch and the computer do not send them yet.
  Backend gap: a reason and a time with the watch's status.
- Today's watch line (rules 18 and 19, v1.2.0 rule 7), with the Today session.

Still: `docs/screenshots/watch-page/after-narrow-watch-page.png` (simulator, seeded rows).
