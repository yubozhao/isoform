# "Listen on your watch" on the phone and the watch (2026-11-15)

Design's pass-questions v1.0.0: the phone can only turn listening off, never on. The computer
side is #1192.

- The Watch page has the switch when the computer has the setting. Turning it off tells the
  computer first (it drops what the watch records from that moment), then the watch. If the
  computer cannot be reached the switch stays on and says so: an off the computer never heard
  would drop nothing.
- Turning it on from the phone does nothing but say "Tap the mark on your watch to start
  listening."
- The off travels to the watch in the phone's context as the time it was turned off. The watch
  acts on each off once: it stops and stays off, across days, until the wearer starts it on the
  watch (the mark, the complication or a shortcut). On the phone that reads as "Off for today".
- When the watch says it is recording again and its newest recording began after the off, the
  phone tells the computer the off ended at that recording's start.
- Until the watch next hears from the phone it may keep recording; the computer drops that audio.
