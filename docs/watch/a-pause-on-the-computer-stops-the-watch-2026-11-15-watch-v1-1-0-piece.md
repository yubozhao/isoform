# A pause on the computer stops the watch (2026-11-15, watch v1.1.0 piece 6b, phone and watch)

- The phone reads `observingPause` from the computer's status (PROTOCOL.md 24.8) whenever it
  checks on recordings, and passes it to the watch in the application context with the states.
- The watch stops recording, deletes the audio it still holds from the pause's start on
  (`WatchRecordingLibrary.dropUnsent(since:)`), and reads "Paused on your computer" with the time
  it ends when there is one. While the pause holds it neither starts by itself nor on a tap.
- When the phone says the pause is over, or its end time passes, the watch records again by itself
  the next time its app is in front.
- An unreachable computer leaves the last word standing: the watch stays paused.
- This is design's first guard and a best effort. The phone hears of a pause only when it next
  talks to the computer, and the watch only when the phone's context reaches it. The second guard
  (the computer drops audio from a pause) is the one that always holds.
- Not built: pausing from the watch (design's hold to pause), which needs a way for the phone to
  pause the computer.
