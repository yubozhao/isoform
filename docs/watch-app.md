# Isoform on the watch

Bo, 2026-11-13: "develop [Auday-like] watch app... we should have that capability asap".

Isoform builds its own watch recorder: its own code, design and name. Nothing from Auday is
copied. This file is the feature list, what version 1 covers, how it works, and what needs Bo.

## What Auday does (public sources, read 2026-11-13)

From its Product Hunt launch and press coverage:

- Turns the watch already on the wrist into an all-day recorder, started with one tap.
- Recording continues with the screen off; a reviewer reported over 12 hours on one charge.
- Transcription (English and Chinese), search by meaning, and AI "Highlights" of the day.
- Everything stays on the person's devices; a desktop skill reads the phone's memories.
- Heart rate, a stress estimate and location are recorded beside the audio.
- One-time purchase.

## What Isoform does with the same idea

Isoform already has the second half: a computer that transcribes speech locally, keeps memory,
and writes the brief. The watch only has to capture and deliver. So the watch app is thin
("apps of apps": it builds on the phone app, the relay and the desktop's speech engine).

```
watch (records AAC, mono) -> phone (WatchConnectivity file transfer)
  -> relay (sealed ciphertext, PROTOCOL.md section 24) -> computer
  -> local speech engine (Qwen3-ASR when installed) -> activity session of speech
  -> memory and the brief, through the existing privacy gates
```

| Feature | v1 | Later |
| --- | --- | --- |
| Tap to record, hold one second to stop on the watch: a tap never stops (`WatchRecordControl`), a ring fills while holding, VoiceOver activate stops | yes | |
| Recording continues with the screen off (background audio session) | yes | |
| Start from a complication | yes: `YansuWatchComplication` (circular, corner, inline, rectangular) opens the app recording | show the live recording state on the face (needs an App Group) |
| Start from the Action button (Apple Watch Ultra) | yes: the "Start recording" shortcut, assigned in Settings > Action Button > Shortcut | |
| Long recordings | yes: kept by the clock hour, each hour its own recording that is closed, sent and turned into text on its own (see "Kept by the hour"); inside an hour, 15-minute files | talk-only capture, battery guard |
| Recent recordings list with state | yes | |
| Recordings listed on the phone | yes: Settings > Watch > Recordings, shown once a recording has arrived | the designed place; play back, delete |
| Upload state per recording (On watch, Sent to phone, On your computer, Transcribed) | yes | |
| Sync to the phone, then to the computer | yes | watch on cellular without the phone |
| Transcription | yes, on the computer | on-watch or on-phone preview |
| Knowing the owner's voice | yes: each stretch is labelled "you" or "Speaker N" on the computer from the owner's enrolled voice; without an enrollment voices are numbered and the phone is told so | enroll from the phone; show the labels on the phone |
| Highlights | through the brief and memory: what was said feeds them like any speech | a "From your watch" block in the brief; search on the phone |
| Pause observing respected | yes: the computer refuses while paused, the phone holds and retries | |
| Heart rate, stress, location | no | decide with design; location is not sent in v1 |
| Playback on the watch | no | |

## Knowing the owner's voice

Bo, 2026-11-14: "same capability of recognizing the owner's voice; Auday has that."

What the public sources say about Auday here: nothing usable. Its launch post and press
coverage (read 2026-11-13 and 2026-11-14) describe transcription, search and highlights, not how
it tells the owner from others or how a voice is enrolled. So this is built from what Isoform
already has, not from Auday's design.

Isoform's computer already keeps the owner's voice (Settings, enrolled samples, a local sherpa
speaker model) and checks every microphone chunk against it. Watch recordings now use the same
check per stretch of speech:

- A stretch that matches is "you". Other voices are Speaker 1, 2 and so on, numbered in the
  order first heard and kept across the parts of one recording.
- The verdict is stored on each speech event, in the fields memory and tasks already read, so
  what someone else said near the watch is not taken as something the owner said or promised.
- Without an enrolled voice, voices are still told apart but nobody is "you", and the
  recording's state says `not_enrolled` so the phone can say so and offer to add a voice.
- Where this can be better than a watch-only recorder: the check runs on the computer with the
  larger model and several enrolled samples (best match wins, so a different microphone or a
  hoarse day still matches), and the fingerprints never leave the computer.

Still to do: enrolling from the phone (a short sample, sealed to the computer, deletable
everywhere), and showing the labels on the phone and watch once design has drawn them.
## Adding the owner's voice from the phone

PROTOCOL.md section 25. The phone records 3 to 30 seconds of the owner speaking, seals it like a
recording part, and the computer turns it into a fingerprint with its local speaker model. Only
the fingerprint is kept, on the computer. The audio is deleted from the relay when the computer
has read it, from the computer when the request ends, and from the phone when it is answered.
The phone can list the owner's samples (including ones made on the computer) and remove one or
all. Computer side: `wails-gui/mobile_voice.go`.

## The phone's Watch page (2026-11-14)

Bo could not tell where a recording was or how to move it along. The phone now has one page for
that, reached from Settings › Watch › "Recordings and sync" (shown whenever a watch is paired, or
a recording exists) and from a line on Today that shows only while a recording has not reached
the computer.

- **Top card:** a count of what is where ("1 on your watch · 2 on this phone", or "Everything is
  on your computer"), whether the watch is connected, why recordings are waiting, **Sync now**
  and when the phone last checked. Sync now asks the watch to queue what it holds (when the watch
  is reachable), sends what the phone holds, and reads the computer's states again. Pulling down
  does the same.
- **Recordings:** each row says where it is: Recording now, On your watch, On this phone,
  Sending to your computer, On your computer, Transcribed, Couldn't send, Your computer couldn't
  read it.
- **A recording's page:** the state with one line on what happens next; "Where the audio is" for
  the watch, this phone and the computer; Play while the audio is on the phone; "What it heard"
  with You / Speaker N once transcribed (PROTOCOL.md 24.5; shown, never stored on the phone);
  Try again for a recording the computer refused; Delete everywhere.
- **"Where a recording goes":** three lines: the watch keeps it until the phone has it, the phone
  until the computer has it, the computer keeps the audio 14 days.

Plumbing added for it:

- **Watch to phone:** the watch's application context lists what it still holds
  (`WatchRecordingWire.summaryContext`: id, start, length, unsent parts, finished), so the phone
  can show "On your watch" before any part arrives. The phone's context to the watch still
  carries states; the two directions do not read each other's payload.
- **Delete:** a new state `deleted` crosses the watch link; the watch removes the recording and
  its files when it reads it (a recording still running is left alone). The phone deletes its
  parts, and when any part had reached the computer it sends `POST /recordings/delete`; a
  computer that cannot be reached is asked again on every later pass, across relaunches. A
  recording deleted on the computer comes back through `GET /recordings?deleted=1` and is
  removed on the phone and the watch the same way.
- **Sync now message:** `syncRecordings` over `WCSession.sendMessage`, only when reachable.

Differences from the handoff (watch v1.0.0, section 7): the Today line shows as soon as something
is waiting, not after ten minutes; there is no byte progress bar, no daily notification, no
"Ways to record" list, and the transcript is the first 300 lines rather than "a few lines".

## Kept by the hour (2026-11-15, watch v1.1.0 piece 1)

Bo, on the first design: "it records automatically, and I think they splice it into hours. In the
design I see 'we have been recording for 2hr'. I don't think that's the right way."

- While the watch records, the open recording is closed at the next clock hour and a new one
  begins at once, by itself, with the screen off. A morning from 9:40 to 11:20 is three recordings:
  9:40 to 10:00, 10:00 to 11:00, 11:00 to 11:20. The rule is `WatchHourClock`
  (`ios/YansuWatchShared`), guarded by `WatchHourClockTests`.
- Each hour has its own id, so the pieces that already work on a recording work on an hour with
  no change: the phone forwards it, the computer transcribes it when its last part arrives and
  matches the owner's voice ("Knowing the owner's voice"), and its state, transcript, playback and delete are per
  hour on the phone's Watch page and in "From your watch" on the computer.
- Inside an hour the audio is still written in 15-minute files, so it starts travelling before the
  hour ends. A file that would run past the hour is shortened to end on it.
- An hour begun in its last minute runs through the next one, so nothing is cut seconds after it
  began. After a call that ran across the hour, what follows is a new recording.
- The watch shows no running clock. While recording it reads "Recording" and "Kept by the hour.
  Hold to stop."; the list names each row by its hour ("Sat 10 AM") with whole minutes.
- The watch keeps a week of rows (168) instead of 30.
- There is no 16-hour end any more: a recording used to stop at 64 files; an hour never gets there.

Not in this piece, from design's v1.1.0: starting by itself when the watch goes on the wrist, keeping
only talk, the shared pause (Closed Eyes), marks, Forget, the mark on the watch face in place of
the timer, and one named conversation across an hour's edge on the computer. Between two hours
there is a gap of a fraction of a second while one file closes and the next opens; not measured on
a real watch.
## Marked moments (2026-11-15, watch v1.1.0 piece 7, computer)

- A part may carry `marks`, the moments the wearer marked, in seconds from the part's start
  (PROTOCOL.md 24.9, status flag `recordingMarks`). The computer stores them with the part and
  removes them with the recording (`wails-gui/mobile_recording_marks.go`).
- A transcript line is marked when a mark points at it; "From your watch" and the phone's
  transcript show it.
- Not yet: marking on the watch (the iOS half), and marked moments first in the brief.

## Marked moments open the brief (2026-11-15, watch v1.1.0 piece 7, the brief)

A mark is the one thing in a day of listening the wearer pointed at on purpose, so the brief
opens with it. Agreed with the Today owner, who owns the brief writer.

- `wails-gui/mobile_brief_watch_marks.go` gathers one item for each watch recording that holds a
  mark in the brief's window, newest first, at most three. Its source is the recording's activity
  session (`activity_session`, `rec-<id>`), so the brief's own checks for a deleted, private or
  withdrawn session apply when the brief is bound and read.
- The writer's item carries `"marked": true` and a day word ("today", "yesterday"), never a clock
  time. One prompt rule tells the writer to open with marked items and say when only from the
  item's when value.
- What was being said goes to the writer only when the marked line is the owner's own voice, and
  the writer is told to put it in its own words and never quote it. Another person's words never
  go. With no owner line, the brief can only say that a moment was marked.
- Nothing is gathered while observing is paused, when the pause log cannot be read, from a mark
  made during a pause, or from a deleted recording. No transcript text is logged.
- Only the new brief writer uses these items; the brief's sections are unchanged.

Not proven: no brief has been written from a real marked recording yet. Whether the writer leads
with the mark, and whether its paragraph keeps its evidence through binding, is to be read off a
real brief.

## Privacy

- Audio is recorded to the watch's own storage, moved to the phone, and deleted from the watch
  once the phone has it. The phone deletes its copy once the computer answers that it holds it.
- Between phone and computer the audio is sealed with a per-file key (ChaCha20-Poly1305). The
  relay sees ciphertext only and deletes it when the computer takes it (48-hour expiry
  otherwise). With remote access over iroh the relay is the user's own computer.
- The computer keeps the audio under `~/.isoform-agent/mobile/recordings/` (files 0600) and
  deletes it 14 days after it was transcribed.
- The computer transcribes locally. Audio is never sent to a model. The transcript is ordinary
  microphone speech in an activity session, so every existing gate applies: pause observing,
  the protected-app rules, memory provenance, and brief delivery checks.
- The watch shows a clear recording state, and the system shows its own microphone indicator.
  Recording other people can need their consent; the first-run sheet on the watch says so.
- Location with a recording is off until the wearer turns it on, and is coarse (about 1 km). The
  area name comes from Apple Maps: the watch sends Apple that rough position only, once per
  area, and keeps the newest 30 names on the watch until Location is turned off.

## Pieces

- `relay/internal/store/uploads.go`, `relay/internal/httpapi/uploads.go`: sealed uploads from a
  phone, fetched and deleted by its computer.
- `wails-gui/mobile_recordings.go`: `POST /recordings`, `GET /recordings`, the recordings store
  under `~/.isoform-agent/mobile/recordings/`, and the transcription worker.
- `ios/YansuWatch/`: the watch app (SwiftUI, watchOS 10+): recorder, phone link, list, shortcuts.
- `ios/YansuWatchShared/`: the watch-to-phone wire format and the watch's recording index,
  compiled into both apps and tested with the phone's unit tests.
- `ios/Yansu/WatchLink.swift`, `ios/Yansu/WatchRecordingForwarder.swift`: the phone's receiver
  and forwarder.
- `ios/YansuWatchComplication/`: the watch face complication (WidgetKit), embedded in the watch app.
- `ios/Yansu/WatchRecordingsView.swift`: the phone's plain list of watch recordings.
- `scripts/build-watch.sh`: builds the watch app for the watchOS Simulator.
- `mobile-protocol/PROTOCOL.md` section 24: the contract.

## Heart rate and location

PROTOCOL.md section 24.4. While it records, the watch may note the wearer's heart rate (HealthKit,
read only) and roughly where they are (location when in use, rounded to about 1 km), one summary
per part. Each is its own permission, asked on its own with its reason; declining keeps recording
working exactly as before. Nothing is sampled when the watch is not recording.

The summary rides inside the sealed request that carries the part's key, so the relay never sees
it. The computer stores it with the part, deletes it with the part's audio, and uses it for one
thing: a line of context on the part's words for the brief ("at Mission District · heart rate
raised"). Coordinates are never written into text. Computer side:
`wails-gui/mobile_recording_context.go`.

## Seeing, playing and deleting recordings (2026-11-14)

- **On the computer:** Activity has a "From your watch" section once a recording has arrived. It
  lists recordings by day; each one shows its parts with the place and heart rate line, plays
  the audio while it is kept (14 days after transcription), shows what was said with who said
  it, opens the Screen timeline at its time, and deletes it. Bindings: `GetWatchRecordings`,
  `GetWatchRecordingTranscript`, `DeleteWatchRecording`; audio from the host bridge route
  `/api/activity/recording?id=&part=` (media token, file name taken from the index).
- **On the phone:** the transcript and the delete go through PROTOCOL.md 24.5.
- **Delete is one delete.** Deleting on the computer removes the audio, the heart rate and
  place, and the transcript with what was learned from it; the phone reads the deletion at its
  next check and removes its copy and the watch's. Deleting on the phone asks the computer to
  do the same. A row that says only "deleted" stays on the computer so a late part is never
  imported again.
- Not built: the computer does not name a recording yet (the handoff's "Roadmap call" title), and
  the transcript does not show pause gaps.

## Needs Bo

Nothing here blocks simulator builds or tests. Each item needs his Apple account or a decision.

1. App ID `com.isoform.companion.watchkitapp` in the developer account, and a provisioning
   profile (or automatic signing with his team) for the watch target. Until it exists the watch
   app stays out of the iPhone app: the Yansu target has an "Embed Watch Content" phase, but
   its one file is excluded unless `ISOFORM_EMBED_WATCH=YES` (default `NO`), and the Yansu
   scheme does not build the watch target, so today's iPhone builds and TestFlight uploads are
   unchanged. To ship the watch app once the App ID exists, build the `YansuWithWatch` scheme
   with `ISOFORM_EMBED_WATCH=YES` (it builds the watch app first, then embeds it at
   `Yansu.app/Watch/YansuWatch.app`), and make that the default in `scripts/distribute-ios.sh`.
2. The watch app in App Store Connect: it ships inside the iPhone app's record, but the record
   needs watch screenshots and the microphone purpose text reviewed.
3. A real watch to prove screen-off recording, the Action button and battery use. The
   simulator proves build, UI and transfer logic only.
4. Decision: all-day recording as a mode (battery, consent copy) or session recording only.
5. Decided 2026-11-14 (Bo): heart rate and coarse location travel with watch recordings, each
   behind its own permission. See "Heart rate and location" above.
6. Decision: recordings ride on the existing `chat:write` scope so paired phones need no
   re-pair. A separate `recording:write` scope would be stricter but forces re-pairing.
7. Design: the watch screens were built from the design-system tokens without a design
   board; they need a pass by Isoform design.
8. App ID `com.isoform.companion.watchkitapp.face` for the complication, with the
   watch app's (item 1).
9. Versions: the two watch targets carry their own `MARKETING_VERSION` (1.0.5 today). Once the
   watch app is embedded it must match the iPhone app's version, so the version bump has to
   touch all three.
