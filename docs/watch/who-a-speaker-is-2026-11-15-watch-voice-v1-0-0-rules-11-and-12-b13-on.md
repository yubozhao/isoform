# Who a speaker is (2026-11-15, watch-voice v1.0.0 rules 11 and 12, B13 on the computer)

- A watch transcript says "you" and Speaker 1, 2. The person can now name a speaker, and the
  computer offers a guess to judge when the calendar leaves one person it could be: the
  conversation has one unnamed speaker, exactly one event overlaps it, and that event has exactly
  one other person with a name. Anything less and nobody is guessed
  (`wails-gui/watch_speaker_names.go`).
- The phone answers with `POST /recordings/conversation/speaker`: Yes, No (that name is not
  offered again for that speaker), or Edit with a typed name (PROTOCOL.md 24.13). Conversations
  for the phone carry `speakerNames` and `speakerGuess`.
- Names are kept in `speaker-names.json` in the recordings folder (0600), tied to the
  conversation's words: a deleted or withdrawn recording takes them. They are shown with the
  conversation only, and are not used in the brief, claims or work.
- Not built: guessing from a voice named before ("it sounds like ..."), a stranger "only heard
  once", recording the answer in Your answers, and the computer's own page for it.
