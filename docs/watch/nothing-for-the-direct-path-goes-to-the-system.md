# Nothing for the direct path is handed to the system

On 2026-11-18 at 19:40, on build 20261119030739, the phone opened with 29 hours waiting and sent
nothing. Both relays' upload folders stayed empty and the tunnel's log had no upload request.

What happened, read from the computer's logs and the code (the phone's own log was not at hand):

1. The computer now says it fetches a part from the relay the phone names, so the phone takes
   the direct path first (`a-part-takes-the-path-that-is-up.md`).
2. The direct path did not answer when asked what it takes.
3. For a relay that does not say what it takes, the phone fell back to one whole upload handed
   to iOS. It did that for the direct path's address too. That address is a door inside the
   app; iOS cannot reach it, so the upload never ends.
4. Four parts were handed over that way. The phone waited 20 seconds on each, counted them as
   "with the system", and went no further. The same wait kept "Trying…" grey and Conversations
   on "Reading from your computer" for a minute and a half.

## The rule now

Nothing for an address inside the app is ever handed to iOS, in any state
(`RecordingPieceUploads.putUpload`, and again in `RecordingBackgroundUploads.start` and
`startPiece`). A direct path that does not answer is a path that is down: the part takes the
pinned relay at once. The wait for the direct path's answer is 4 seconds, not 10.

When a part is next sent over the direct path, whatever iOS still holds for it is stopped
first, so parts stuck by the old behavior are freed by the new build.

## Try again

The card said "1 hour couldn't reach your computer. Update Isoform on your computer, then try
again." No reply from the updated computer caused that: it was the phone's memory of five
refusals earlier in the day, by a computer build that has since been replaced. Refused parts
wait for their next turn, up to six hours, and Try again did not touch them. Now Try again
gives every refused part another round at once (`WatchRecordingForwarder.retryRefused`).

Guards: `testNothingForTheDirectPathIsEverHandedToTheSystem`
(`RecordingPieceUploadsTests`), `testTryAgainSendsRefusedPartsNow` (`WatchRecordingTests`).

Not changed: "On your computer" counts today's hours, as design's rule 11 says; with nothing of
today across it read "0 hours" beside a computer that held earlier days. That is a wording
question for design.
