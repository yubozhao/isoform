# Conversations across the hours (2026-11-15, watch v1.1.0 piece 5, first of three)

- The watch keeps recordings by the clock hour, so a talk from 9:50 to 10:20 arrives as the end of
  one recording and the start of the next, and the 10 o'clock recording may also hold a standup.
- The hour stays the unit that is stored, played and deleted. A conversation is computed on top of
  it from the speech already stored (`wails-gui/watch_conversations.go`): a run of speech from one
  device with no silence longer than `watchConversationSilence` (two minutes; design can change the
  constant), across recordings. Nothing is copied or saved.
- Delete an hour and the conversations are computed again from what is left: the Sam talk becomes
  "10:00 to 10:20".
- "From your watch" on the Activity page lists each day's conversations above its hours: when it
  ran, how many words, how many hours it spans, who spoke, and the words on opening.
- A stretch of speech is stored with only its start, so its end is estimated from its words (2.5 a
  second).
- Next: voices keep their number across the hour's edge; names for conversations, sent back to the
  phone and the watch. The brief is not changed.
