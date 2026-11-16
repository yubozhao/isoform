# Recording by itself (2026-11-15, watch v1.1.0 piece 2)

- Recording is the standing state. When the watch app comes to the front (opened, the complication
  tapped, the Action button), it records with nothing to tap. The rule is `WatchAutoListen`,
  guarded by `WatchAutoListenTests`.
- A stop by the wearer (the hold, or the Stop shortcut) is the off for that day: it holds across
  launches until they tap to start, tap the complication, run the Start shortcut, or the day ends.
  Raising the wrist never restarts what they just ended; the next day the watch records again by
  itself when its app comes to the front.
- Nothing is recorded before the first-run sheet is read. After "Continue" the system asks for the
  microphone and recording begins.
- After a call or Siri, recording comes back by itself when watchOS says the interruption ended;
  when it does not say so, it comes back the next time the app comes to the front. While the
  microphone is held the watch reads "Waiting for the microphone", not "Recording". Before this, a
  failed resume stopped the recording.
- watchOS does not let an app open the microphone from the background, so the watch cannot begin
  when it is put on the wrist without one touch (the complication is that touch).
