# Conversations for the phone's Watch tab (2026-11-15, phone-watch v1.0.0, B6 part 1)

Design's phone pass makes Watch a tab that shows what the watch heard as conversations
(`docs/handoff-2026-11-15/`, phone-watch v1.0.0). Until now the phone read only an hour's names.

- `GET /recordings/conversations` (PROTOCOL.md 24.11): the last seven days' conversations, newest
  first, each with its name, start and end, an estimate of the talk in it, who spoke, its marks,
  its notes and follow-ups when it has them, and its place and heart-rate word when the wearer
  shares those. With it, today's hours (recorded, talk, paused) for the strip and today's pauses
  for the striped rows.
- `GET /recordings/conversation?id=`: one conversation's words, with "marked" on the line a mark
  points at.
- A phone reads only the conversations from the recordings it sent. While observing is paused
  neither route reads anything out. Nothing is stored for them
  (`wails-gui/mobile_recording_conversations.go`).
- Status flag `recordingConversations`.

Not in this piece: deleting one conversation everywhere (today a delete is by the hour),
follow-ups as items shared with Your work (B2), where each conversation is (the phone knows what
it and the watch still hold), speaker guesses (B13), and the phone's side.
