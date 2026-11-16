# Audio from a pause is dropped on the computer (2026-11-15, watch v1.1.0 piece 6a)

Design's v1.1.0, rule 20: "Nothing heard during a pause is kept, sent or transcribed."

- Before: a part recorded while observing was paused waited on the phone (409) and was sent,
  transcribed and put in memory after the pause ended. The moment someone paused for could show up
  in their brief.
- Now the computer compares each part with its pause log (`observation_pauses.go`). A part wholly
  inside a pause is deleted on arrival and never read. A part a pause touches gives only its words
  outside the pause, and its audio is deleted after transcription. `mobile_recording_pauses.go`,
  PROTOCOL.md 24.6.
- Follow-up (same day): a part a pause only touches keeps its audio outside the pause. The paused
  minutes, and a second on each side, are cut out with the bundled ffmpeg (`cutPausedAudio`); the
  file is deleted only when the cut fails. `TestNoPausedSecondSurvivesInATouchedPartsAudio` makes a
  minute of audio with a tone during the pause, cuts it with the real ffmpeg, and checks that no
  sample of the tone is left. A cut part plays shorter than its listed length.
- This is the second of design's two guards. The first (the watch stops when Isoform is paused,
  and deletes what it recorded since the pause began) needs the pause to reach the phone and the
  watch, which the protocol does not carry yet.
- What it costs: a few minutes of a recording when someone paused the computer by mistake while
  the watch was recording. There is no way to get them back.
