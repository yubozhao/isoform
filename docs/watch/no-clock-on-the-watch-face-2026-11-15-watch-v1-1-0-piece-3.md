# No clock on the watch face (2026-11-15, watch v1.1.0 piece 3)

- The complication no longer counts up. While the watch records it reads "Recording" (corner),
  "Recording in Isoform" (inline) and "Recording / Kept by the hour" (rectangular); the circular
  one shows the filled mark. Not recording, it is the record button as before.
- The watch app says "still recording" to the complication when recording starts and at every
  clock hour. The complication believes that for two hours (`WatchLiveState.longest`), so a watch
  app that was killed shows as not recording after two hours at most; it was sixteen.
- The mark is still the drawn record mark, not design's eye; that comes with the designed mark.
