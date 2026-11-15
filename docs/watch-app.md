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
| One-tap record and stop on the watch | yes | |
| Recording continues with the screen off (background audio session) | yes | |
| Start from a complication | yes: `YansuWatchComplication` (circular, corner, inline, rectangular) opens the app recording | show the live recording state on the face (needs an App Group) |
| Start from the Action button (Apple Watch Ultra) | yes: the "Start recording" shortcut, assigned in Settings > Action Button > Shortcut | |
| Long recordings | yes: cut into 15-minute files, each sent on its own | all-day mode with battery guard |
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
