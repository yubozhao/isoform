# The microphone is opened once (2026-11-16)

Bo's ranked goal for the watch: (1) record everything; (2) then battery, size, sync and polish.

## What went wrong

On 2026-11-16 a meeting from 11:45 to 12:15 was recorded only until 12:00. The same happened at
every hour edge that day: after the hour there was nothing, or a stub of a few seconds, until the
app was next brought to the front.

The watch recorded in parts, and each part was its own recorder: when one ended, the app closed
its file, saved it, handed it to the phone, and then started the next recorder. At the hour it
also closed the hour's recording and opened the next. All of that happened with the screen off.

watchOS does not allow that. Apple's answer on the developer forum (thread 750432): recording
cannot be started or resumed while a watchOS app is in the background; it must start from the
foreground, and may then continue in the background. Starting a recorder for the next part is
starting a recording. Sometimes watchOS let it through; at the hour edge it mostly did not, and
nothing told the wearer.

## What the watch does now

- One capture: an audio engine taps the microphone from the moment listening starts (in front)
  until it ends (`ios/YansuWatch/WatchCapture.swift`). It is never stopped or restarted for a
  reason of time.
- A part ends by switching the file the capture writes into. Every buffer goes into exactly one
  file, so no audio falls between two parts. Each finished file is closed and handed to the phone
  at once.
- The clock hour is a label: at the hour the next file belongs to a new recording. Everything
  after the watch (phone hours, the computer's conversations across the hour edge, names, purge,
  delete) sees the same recordings by the hour as before.
- Parts are 5 minutes (they were 15): if the system ends the app, at most the running part is at
  risk.
- A watchdog looks every 2 seconds: no audio for more than 2 seconds while listening is on means
  the microphone was lost; it is taken back at once when the app is in front, and the gap is
  logged. It also rolls a part or an hour whose own signal was missed.
- When the microphone is lost (a call, Siri, a route change, gone quiet), what was recorded is
  kept, listening stays on, the watch buzzes once and shows it, and the phone is told. It comes
  back the moment the app is in front; in the background watchOS refuses, so it is asked for
  again every 30 seconds and whenever the app comes to the front.
- If the system ended the app while listening was on, the next launch closes the unfinished
  recording, logs the gap, and listening starts again by itself when the app is in front.
- A gap log per day (`WatchGapLog`): seconds listening was on, seconds recorded, and each stretch
  without the microphone with its reason. Numbers only.

The bookkeeping is `ios/YansuWatchShared/WatchRecordingSession.swift`, tested with a capture that
only counts (`ios/YansuTests/WatchRecordingSessionTests.swift`).

## Limits that stay

- After a call or Siri with the screen off, no app can take the microphone back until it is in
  front. The watch cannot restart within a second there; it can only say so.
- If the system ends the app mid-part, the running part's file may be unreadable (an AAC file is
  only complete when closed). That is up to 5 minutes. Keeping it to the last second needs a
  stream format (ADTS) and the computer and phone taking it; not built.
- More parts per hour reach the relay (12 instead of 4); its limit of 48 waiting uploads per phone
  is now about 4 hours of backlog instead of 12. The phone waits and retries when it is full.
- The gap numbers are not shown on the phone yet.

Not proven on a real watch. A simulator cannot show what watchOS does with the screen off.
