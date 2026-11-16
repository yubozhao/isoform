# "Listen on your watch": the phone can turn it off, never on (2026-11-15, computer side)

Design's pass-questions v1.0.0, relayed by the hub: the phone can only turn listening off. Off
takes effect at once; turning it back on from the phone says "Tap the mark on your watch to start
listening."

- The computer keeps the stretches when listening was turned off
  (`wails-gui/watch_listen_off.go`). Whatever the watch recorded after the off is dropped here
  exactly as audio from a pause is: an hour wholly after it loses its file on receipt, an hour it
  touches has those minutes cut and nothing from them is transcribed. This holds even before the
  watch has heard that it should stop.
- The off ends when the wearer starts listening on the watch; the phone passes the time the watch
  began, and the computer never ends the off before it began or after now.
- `POST /watch/listening` and `watchListening` in the status (PROTOCOL.md 24.14). A recording made
  on the phone is not affected.
- Not here: the switch on the phone's Watch tab and the watch stopping at its next sync.
