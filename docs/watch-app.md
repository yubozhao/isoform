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
| Highlights | through the brief and memory: what was said feeds them like any speech | a "From your watch" block in the brief; search on the phone |
| Pause observing respected | yes: the computer refuses while paused, the phone holds and retries | |
| Heart rate, stress, location | no | decide with design; location is not sent in v1 |
| Playback on the watch | no | |

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
5. Decision: whether to send heart rate or location beside audio (v1 sends neither).
6. Decision: recordings ride on the existing `chat:write` scope so paired phones need no
   re-pair. A separate `recording:write` scope would be stricter but forces re-pairing.
7. Design: the watch screens were built from the design-system tokens without a design
   board; they need a pass by Isoform design.
8. App ID `com.isoform.companion.watchkitapp.complication` for the complication, with the
   watch app's (item 1).
9. Versions: the two watch targets carry their own `MARKETING_VERSION` (1.0.5 today). Once the
   watch app is embedded it must match the iPhone app's version, so the version bump has to
   touch all three.
