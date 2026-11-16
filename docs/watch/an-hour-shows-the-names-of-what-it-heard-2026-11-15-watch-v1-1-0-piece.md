# An hour shows the names of what it heard (2026-11-15, watch v1.1.0 piece 5c, phone and watch)

- The phone reads `conversations` from the computer's view of an hour (PROTOCOL.md 24.7) at its
  usual check of recordings that are across, keeps the names with its states, and shows them under
  the hour in place of the state ("10 AM · 48 min" / "Pricing with Sam, Standup"). The hour's page
  shows them above where it is.
- The phone passes the names to the watch in the same application context as the states
  (`recordingNames`); the watch's row shows them in place of the state.
- The computer's word is the whole truth: when it stops giving a name (the hour was deleted, or
  its words withdrawn), the phone drops it and tells the watch.
- Names arrive late on purpose: the computer names a conversation about 70 minutes after it ended.
