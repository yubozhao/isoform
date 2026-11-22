# Phone watch v1.2.0: when Isoform was swiped away, say so once, in Where your hours are, and only when iOS says so

Version 1.2.0 · written November 18, 2026, 4:00 PM Pacific, by Claude Design.

**This file answers the hub's question on hours that wait because Isoform was swiped away on the phone. It adds to phone-watch v1.1.0 rules 12 and 18 and the Watch item's dot (rule 3); everything else in v1.1.0 and phone-duo v1.0.0 to v1.1.0 stands.**

When the person swipes Isoform away, iOS stops its background uploads and doesn't wake it until it's opened again, so hours wait on the phone or the watch. The phone learns this only when it's next opened. The coming fix hands every part to iOS at once, so a few seconds open is enough.

A change goes in a new file with a new version; this one isn't edited after it's written.

## The rules

1. **Say the cause only when iOS says it.** iOS reports why a background upload was cancelled, in the error's `NSURLErrorBackgroundTaskCancelledReasonKey`. Isoform says "Isoform was closed" only when an upload it had handed over comes back with `NSURLErrorCancelledReasonUserForceQuitApplication`.
   - "None ran" alone isn't proof: the phone may have been off, out of battery or in Low Power Mode. If Isoform doesn't know, it doesn't say.
   - The same key can say `NSURLErrorCancelledReasonBackgroundUpdatesDisabled`. That's one the person can fix, so it gets its own sentence in the same place, by the same rules: "Background App Refresh is off for Isoform, so {n} hours waited. They're going to your computer now. Turn it on in Settings › General › Background App Refresh."
2. **Where your hours are, on the next open, while the hours go:**
   - The sentence: "Isoform was closed on this phone, so 6 hours waited. They're going to your computer now."
   - Under it, the one fact the person can act on: "Swiping Isoform away stops sending until you open it again." It's a fact with no instruction, and it shows each time this happens, never otherwise.
   - The three places above the sentence still say where the hours are, the watch's among them.
3. **Once they're all on the computer,** the card reads "Every hour is on your computer, the last at {time}." as always (v1.1.0 rule 12). The cause doesn't linger.
4. **The current reason wins.** If the computer is asleep or can't be reached when the phone opens, the card says that sentence instead (v1.1.0 rule 12); the swipe isn't what's holding the hours any more.
5. **Hours going with no known cause** get their own plain sentence: "6 hours are going to your computer now." It's for any send under way when no other sentence applies, once the send has run a few seconds, so a quick send doesn't flash.
6. **No notice.** No banner and no push: the phone can't notify while it's closed, and by the time it's open the hours are already going. A notice for something already fixing itself asks for attention it doesn't need. The card says it once.
7. **Today's line, and the Watch item's dot, stay plain while hours are going.** This changes v1.1.0 rules 3 and 18, which turned amber at 30 minutes of waiting.
   - While the phone is sending, the line reads "6 hours from your watch are going to your computer". Plain, without the cause; the card is the one place that says why.
   - Amber, and the dot, are only for hours that can't go: the computer asleep or unreachable for 30 minutes or more, or an hour that couldn't be sent.

## Every word

| Key | English | Where |
| --- | --- | --- |
| watch.where.closed | Isoform was closed on this phone, so {n} hours waited. They're going to your computer now. | Where your hours are |
| watch.where.closedWhy | Swiping Isoform away stops sending until you open it again. | Where your hours are, under it |
| watch.where.refreshOff | Background App Refresh is off for Isoform, so {n} hours waited. They're going to your computer now. Turn it on in Settings › General › Background App Refresh. | Where your hours are |
| watch.where.going | {n} hours are going to your computer now. | Where your hours are |
| today.watch.going | {n} hours from your watch are going to your computer | Today's line, plain |

Plural forms follow the language ("1 hour waited").

## Proof

On Bo's phone with his watch, on a named TestFlight build with the hand-everything-to-iOS fix:

1. **With hours on the phone, swipe Isoform away and wait, then open it.**
   - Within a second, the card shows the closed sentence and the fact under it, and Today's line reads "… are going to your computer" in plain.
   - Leave after a few seconds; within a minute the computer has every hour, with no tap. That shows as the card's "Every hour is on your computer" on the next open, and the computer's own list.
   - A recording, and the log line with iOS's cancel reason.
2. **Restart the phone instead of swiping, with hours waiting.** On open, the card says "… are going to your computer now." with no "Isoform was closed". A still.
3. **Computer asleep when the phone opens:** the asleep sentence, not the closed one. A still.

## On the map (phone)

- P280 · "Isoform was closed" in Where your hours are only when iOS reports the force-quit cancel reason (and "Background App Refresh is off" for that reason), with "Swiping Isoform away stops sending until you open it again."; gone once every hour is on the computer; the current reason wins; "going now" when no cause is known.
- P281 · No notice for hours that waited; Today's line and the Watch item's dot stay plain while hours are going; amber only when they can't go.
