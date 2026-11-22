# A watch that had Isoform is "away", not "missing" (2026-11-21)

## What happened

Twice, right after a TestFlight update of the phone app, the Watch tab said "Put Isoform on your
watch. Your Apple Watch is paired with this phone. Isoform isn't on it yet." and showed nothing
else: no hours, no conversations, no settings.

## What was checked

- The build carries the watch app. In the archive of build 20261121224433, `Yansu.app`
  (`com.isoform.companion`, 1.0.6) holds `Watch/YansuWatch.app`
  (`com.isoform.companion.watchkitapp`, same version and build, companion id set).
- The phone's only source for the card was `WCSession.isWatchAppInstalled`, read as it is at
  that moment. When the phone app is updated, the watch gets its own copy from the phone later,
  on the system's schedule, and until then the system reports the watch app as not installed.
- Whether the watch app was really gone or only reported so cannot be told from the computer.

## What changed (phone only)

- The phone remembers that the watch has had Isoform: set when the system says it is installed,
  and when a file arrives from the watch (`watch.hadApp`).
- Paired, not installed, and had it before is a state of its own, `appAway`. The card then says
  what is known: the watch is not answering, Isoform comes back by itself after an update, and
  where to install it if it does not. The rest of the Watch tab stays.
- Paired, not installed, never had it: "Put Isoform on your watch", as before.

## Words for design to replace

- Card word: "Your watch isn't answering"
- Card line: "After an update Isoform comes back on your watch by itself. If it doesn't,
  install it from the Watch app on this phone." (with "Last heard 9:40 AM." in front when known)
- Recordings page line: "Isoform on your watch isn't answering right now. After an update it
  comes back by itself."

## Recordings

Nothing that sends, keeps or deletes a recording is changed. While the system says the app is
not installed the phone does not push its states to the watch, as before. One risk is outside
this app: if watchOS really removed the watch app and it is installed again, watchOS deletes
that app's files, and hours recorded but not yet sent to the phone go with them.
