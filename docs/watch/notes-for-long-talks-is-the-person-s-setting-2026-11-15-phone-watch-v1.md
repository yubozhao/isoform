# "Notes for long talks" is the person's setting (2026-11-15, phone-watch v1.0.0, B8)

- Notes and follow-ups from watch conversations over five minutes were behind
  `YANSU_FEATURES=watch-notes` only. The choice is now stored on the computer
  (`wails-gui/watch_notes_setting.go`): once the person has chosen, their choice decides; until
  then the build switch decides, as before.
- The phone reads it in `GET /status` (`watchNotes: {on, chosen}`) and sets it with
  `POST /settings/watch-notes` (PROTOCOL.md 24.12).
- Turning it off stops new notes. Notes already written stay until their words go.
- Not decided here: design's "on by default". With no choice and no build switch it is off.
