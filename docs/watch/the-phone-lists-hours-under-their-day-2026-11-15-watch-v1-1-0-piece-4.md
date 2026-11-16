# The phone lists hours under their day (2026-11-15, watch v1.1.0 piece 4)

- The phone's Watch page groups what the watch recorded by day ("Today", "Yesterday", then the
  date), newest first, and each row is an hour: "10 AM", "48 min", and where it is.
- An hour's page is titled by its day and hour. Lengths are whole minutes; no clock form (1:12:05)
  is left on the page.
- `WatchRecordingDay.days` does the grouping; `WatchRecordingDayTests` guards it.
- Not yet: the computer's names for what it heard under an hour, "Quiet" hours and paused
  stretches (those need talk detection and the shared pause).
