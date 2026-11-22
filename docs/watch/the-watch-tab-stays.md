# The Watch tab stays for a phone that has had a watch

Bo, 2026-11-18: "Why the phone app don't have watch screen sometime?"

The tab showed only while the system had reported a paired watch. That report comes when the
watch session finishes activating, so the tab was missing at every launch until then, and stayed
missing when the activation failed or never reported.

Now:

- The phone remembers that it has had a watch (`watch.seenBefore` in UserDefaults, set the first
  time a paired watch is reported). `WatchConnection.showsTab` shows the tab while the system
  has not said yet if a watch was seen before.
- Only a plain report of no paired watch, after activating, takes the tab away. The memory is
  kept, so the tab is back as soon as the system reports the watch again.
- Each time the app comes to the front, `WatchLink.readSession` reads the session again, or
  activates it again if it never activated.
- The Settings row that opens the Watch tab follows the same rule (`showsWatchTab`).

Guard: `testTheWatchTabStaysForAPhoneThatHasHadAWatch` in `ios/YansuTests/WatchRecordingTests.swift`.

Not done here: the wide (Duo) layout has no Watch tab at all; where it goes there is a design
question.
