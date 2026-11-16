# Pausing Isoform from the watch (2026-11-15, phone and watch)

- A "Pause for an hour" row on the watch pauses Isoform: the watch stops recording at once, and
  asks the phone, which asks the computer (`POST /observing/pause`, PROTOCOL.md 24.10). The
  computer's pause then reaches every device through its status.
- The pause holds on the watch from the moment of the tap, also when the phone or the computer
  cannot be reached; it ends there after its hour. The ask is sent as user info, which the system
  delivers when the phone is next reachable, also when the phone app is not running.
- "Resume" on the watch ends a pause the wearer started there, and asks the computer to end it
  too. A pause made at the computer is not ended from the watch; the watch is told it still holds
  and stops again.
- The watch reads "Paused" for its own pause and "Paused on your computer" for the computer's.
- The row is a plain button under the recordings; design's hold-to-pause is not used, because
  Bo's hold stops a recording.
