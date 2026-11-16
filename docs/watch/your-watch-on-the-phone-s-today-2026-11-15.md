# "Your watch" on the phone's Today (2026-11-15)

Bo, on the TestFlight build: "where is the apple watch related screen on the iphone app". It was
under Settings > Watch > "Recordings and sync", and only once a watch was known.

- Today now has a "Your watch" card whenever Isoform is installed on the paired watch, also before
  any recording has arrived. It reads what the watch is doing now ("Recording", "Waiting for the
  microphone", "Paused on your computer", "Paused", "Stopped for today", "Not recording", "Allow
  the microphone on your watch") and, under it, the newest hour and where it is, or the line about
  hours that are waiting. Tapping it opens the Watch page.
- The watch says what it is doing in the same application context as what it still holds
  (`watchStatus`), each time that changes. Until it has said, the card reads "Open Isoform on your
  watch to start".
- The card replaces the line that showed only while a recording was stuck. Settings > Watch stays.
- Design's "Your watch today" for the phone (hours as columns, the last three hours, "All of
  today") is the designed version of this card; this one is plain on purpose.
