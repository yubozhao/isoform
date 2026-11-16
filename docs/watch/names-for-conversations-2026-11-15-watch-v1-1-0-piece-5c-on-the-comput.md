# Names for conversations (2026-11-15, watch v1.1.0 piece 5c, on the computer)

- A conversation longer than two minutes gets a short name ("Pricing with Sam") from one model
  call, through the same seam and the same redaction as the after-call pass for meetings
  (`wails-gui/watch_conversation_names.go`). Shorter ones get none and cost no call.
- It is named once it is over: its last words are more than 70 minutes old, so an hour that has
  not arrived yet cannot still add to it. At most six calls per pass; a pass runs after watch
  recordings are transcribed and once after the phone connects.
- The name is kept in `conversation-names.json` beside the recordings (mode 0600), tied to the
  conversation and a hash of its words. When the words change (an hour deleted, a transcript
  withdrawn), the name no longer fits: it is not shown, and it is removed from the file when an
  hour is deleted and whenever the list is read.
- Nothing is named while observing is paused. A transcript marked private is never read, so it is
  never sent.
- The meeting-notes pass was not reused: it writes a summary, decisions and follow-ups and turns
  the owner's follow-ups into commitments; a meeting's title there is its calendar event's.
- "From your watch" titles a named conversation by its name, with the time in the line under it.
- Not yet: the names on the phone and the watch (a protocol addition).
