# Recordings are sent until the computer has them (2026-11-16)

Before: a part the computer refused five times was given up; it stayed on the phone until the
person tapped "Try again". A full relay looked the same as a sleeping computer. On 2026-11-16 that
left hours of audio waiting for a tap nobody knew to make.

Now, on the phone (`ios/Yansu/WatchRecordingForwarder.swift`, `WatchLink.swift`):

- A refused part waits (1 min, 5, 15, 60 min, 3 h, then every 6 h) and is sent again by itself,
  for as long as it is on the phone. It ends when the computer stores it or the person deletes the
  recording. "Try again" still sends it at once.
- While a refused part waits, newer parts are not held behind it.
- While the app runs it makes the next pass by itself when a part's turn comes, or 5 minutes after
  a pass that could not finish. Each background refresh also makes a pass.
- A relay that keeps no more of this phone's recordings (HTTP 429) is said as that, above the
  list, and is not counted as a refusal.

Not built here: the computer's own line about a full relay.

Not proven on real devices.
