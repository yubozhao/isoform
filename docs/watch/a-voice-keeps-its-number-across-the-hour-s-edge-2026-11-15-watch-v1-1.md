# A voice keeps its number across the hour's edge (2026-11-15, watch v1.1.0 piece 5b)

- Voices are told apart one recording at a time, and the watch cuts a recording at every clock
  hour. Without this, Sam can be "Speaker 1" until ten o'clock and "Speaker 2" after it, in the
  same talk.
- Each transcribed recording leaves the voices it heard (an averaged fingerprint, a number and a
  count each) in `voices.json` in its own folder. A recording from the same device that begins
  within two minutes of where another ended starts from those voices
  (`mobile_recording_voices.go`).
- A voice that said nothing in an hour is not passed on, but its number is not given to anyone
  else. After a break of more than two minutes numbering starts from one again.
- The fingerprints are in the recording's folder, so they are deleted with its audio: on delete,
  when the transcript is deleted, and at the end of the keep time. The owner is still "You" from
  the enrolled voice, as before.
- It only works when the hours are transcribed in order, which is how they arrive. An hour that
  arrives late starts from one.
