# Late parts of a closed recording (2026-11-16)

What happened: a watch recording ran for hours on 2026-11-14. Its first 8 parts reached the
computer; then the phone could not send for the night. After 6 hours without a part the computer
closed the recording and transcribed it. When the phone sent parts 8 to 19 the next day, the
computer refused each one as not valid. The phone gave up on each after 5 refusals, and every
refused copy stayed on the relay for 48 hours. 48 copies is the relay's limit for one phone, so
from then on the relay turned away every new upload, and nothing recorded on 2026-11-16 could
reach the computer. Nothing was logged on the computer, because a refusal was silent.

What the computer does now (`wails-gui/mobile_recording_late_parts.go`):

- A part past the end of a closed recording is stored in a continuation: a recording row of its
  own (`continuationOf`, `firstPart`), with its parts numbered from 0, transcribed, listed and
  deleted like any other. The first recording is not touched and not transcribed again.
- Deleting a recording deletes its continuations.
- A part refused for good is removed from the relay at once, and the refusal is logged
  (`[recordings] part N of <id> refused: <reason>`, no content).
- A relay error during the fetch is "try again later", not a refusal.

What the phone still has to do: parts it gave up on are sent again when the person taps
"Try again" on the recording. Sending them again without a tap is not built.

Not proven on real devices yet.
