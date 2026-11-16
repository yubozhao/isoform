# Marking a moment on the watch (2026-11-15, watch v1.1.0 piece 7, phone and watch)

- While the watch records, a tap on the record control marks the moment: a haptic click and
  "Marked 2:41 PM" for a moment. A tap still never stops a recording; the hold does. The hint
  reads "Tap to mark. Hold to stop."
- A mark is the time into the running part. It is kept with the part on the watch until the phone
  has the part, travels in the transfer's metadata, and the phone sends it to a computer that says
  it takes marks (`recordingMarks`, PROTOCOL.md 24.9). For a computer that does not, the part goes
  without its marks.
- Nothing is marked while nothing is being recorded (stopped, paused, or a call has the microphone).
- The system double tap (watchOS 11, on watches that have the gesture) and the "Mark a moment"
  shortcut (for the Action button: Settings > Action Button > Shortcut) do what a tap does: mark
  while recording, start otherwise. They never stop a recording. The shortcut opens the app,
  because watchOS only lets an app open the microphone from the front.
- Not built: marked moments first in the brief.
