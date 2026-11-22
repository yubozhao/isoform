# Waiting parts go to the system first

On 2026-11-18 at 15:00 the phone app ran for 25 seconds on a build with piece uploads. In the
tunnel's log the phone asks the relay whether it takes pieces at 15:01:05, twenty seconds after
the app came up, and at 15:01:10 the app is gone. It had sent nothing and handed nothing to iOS,
so nothing moved for an hour: no upload was in iOS's hands, and nothing else woke the app.

Two things were wrong, and both are changed here.

## The first upload came too late

A pass asked the computer three things over the relay (does it take recordings, context, marks)
before it touched the relay for the first part. The upload needs only the relay.

Now a pass begins by leaving the first four waiting parts with iOS (`handAhead` in
`WatchRecordingForwarder`), before it asks the computer anything and before it sends deletes.
It does this for a computer that last said it takes recordings, which is remembered across
launches, and only to the pinned relay, where every computer looks. Handing over takes
milliseconds: every piece iOS does not hold yet is handed over, and a piece the relay already
has is answered as stored. Parts go to the relay also while the computer cannot be reached; the
relay keeps them for it.

## With the app open, the app sent the pieces itself

That dies with the app. Now the pieces for a relay iOS can reach are iOS's whether the app is
open or not (`RecordingPieceUploads`): they start at once and go on when the app leaves. The
app's own piece loop remains for the direct path only, which is a door inside the app.

## What wakes the app when nobody opens it

1. The watch hands over a new part: iOS starts the app for about half a minute.
2. Background refresh, when iOS grants one.
3. An upload iOS finished: the app is started, tells the computer about the part, and hands
   over the next four. This is what carries a backlog, once the first parts are in iOS's hands.
4. New here: a background processing run (`BackgroundRecordingSend`), asked for while parts
   wait. iOS gives it when the phone is idle, often while charging, for minutes.

What nothing can do: an app the person swiped away gets none of these until it is opened again.
iOS also stops that app's uploads.

Guards: `testWaitingPartsAreLeftWithTheSystemBeforeTheComputerIsAsked` in
`ios/YansuTests/WatchRecordingTests.swift`; `testWithTheAppOpenThePiecesForThePinnedRelayAreTheSystems`
and `testAPartIsLeftWithTheSystemAhead` in `ios/YansuTests/RecordingPieceUploadsTests.swift`.
