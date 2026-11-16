# The pause reaches the phone (2026-11-15, watch v1.1.0 piece 6b, computer)

- `GET /status` carries `observingPause` (`since`, `until`) while observing is paused on the
  computer (PROTOCOL.md 24.8). The phone and the watch acting on it is the iOS half.
- It is the first of design's two guards and a best effort: the phone only learns of a pause when
  it next reads the status. The computer's own guard (24.6) is the one that holds.
