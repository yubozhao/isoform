# Notes and commitments from longer conversations (2026-11-15, behind the watch-notes switch)

- With `YANSU_FEATURES=watch-notes`, a conversation the watch heard that is longer than five
  minutes gets the after-call pass meetings get: a summary, what was decided, and what has to
  happen next (`wails-gui/watch_conversation_notes.go`; the same prompt, model seam, redaction and
  answer check as `meeting_followup.go`). The follow-ups the owner has to do become commitments in
  the Purpose Map. With the switch off nothing runs and no model call is made.
- It runs once the conversation is over (last words more than 70 minutes old), at most three
  calls per pass, after the naming pass.
- Never while observing is paused. A transcript marked private is never read, so never sent.
- The notes are kept in `conversation-notes.json` beside the recordings (0600), tied to the
  conversation's exact words. Delete an hour or withdraw its transcript and the notes are dropped
  and the commitments made from them are purged (on delete at once, otherwise at the next read
  of the conversations).
- A commitment's source is the transcript of the first hour the conversation draws from, so every
  reader that checks its source hides it when that hour is gone.
- Deleted means deleted (`wails-gui/purpose_purge.go`): when the notes go, the commitments made
  from them are purged from the Purpose Map, root, events, evidence and relations, with the
  erasure "Forget this" already uses for memory. Deleting a meeting's notes purges the commitments
  discovered from that meeting's transcript the same way. A commitment that began somewhere else
  and was only reinforced by the source stays.
- "From your watch" shows the notes above a conversation's words.
