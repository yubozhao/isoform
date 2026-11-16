# The phone pauses the computer (2026-11-15, pausing from the watch, computer)

- `POST /observing/pause` pauses observing on the computer for 5 to 240 minutes (60 when not
  said), as the menu bar's own control does; `POST /observing/resume` ends a pause a phone started
  (PROTOCOL.md 24.10, status flag `observingPauseControl`). `wails-gui/mobile_observing_pause.go`.
- A pause made at the computer is never shortened, taken over or ended from the phone.
- The watch has no route of its own; it asks the phone. That is the iOS half.
