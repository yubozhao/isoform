# A recording part takes the path that is up

Step 2 of `docs/design/watch-parts-reach-the-computer.md`, phone side. The computer side is
"the computer fetches each part from the relay the phone names" (`upload.relay`, PROTOCOL.md 24.2).

On 2026-11-18 the public address of the pinned relay stopped answering twice in one afternoon.
Each time the recordings stopped, although the phone still reached the computer over the direct
path: requests switch paths, recording uploads did not.

## What the phone does now

- It asks the computer whether it fetches a part from the relay the phone names
  (`recordingRelay` in the status answer).
- If yes, a part goes over the **direct path first** when that path is open, and over the pinned
  relay otherwise. A path that is down, or that refuses the phone, hands the part to the other
  one (`WatchRecordingForwarder.paths`). The request to the computer names the relay that holds
  the part: `upload.relay` is `direct` or `pinned`.
- If no (an older computer), every part goes to the pinned relay, where that computer looks, and
  no new field is sent. This also closes a gap that existed before: a phone whose requests had
  moved to the direct path put parts on the computer's own relay, and an older computer answered
  "upload missing".
- A part that began on one relay goes on there while that path is up, so pieces already there
  count. The same sealed bytes and the same upload id are used on either relay.
- The direct path is a listener inside the app. With the app in the background nothing is handed
  to iOS for that address (`RecordingPieceUploads.isInsideTheApp`); the pinned relay carries the
  part then, in pieces as before.

## What does not change

Nothing is deleted on the phone until the computer says it holds the part. A pairing with one
path (pinned only, or direct only) behaves as before. The Watch tab's lines are the same: when
both paths fail, the pinned relay's reason is the one said (pair again, relay full, waiting).

Guards in `ios/YansuTests/WatchRecordingTests.swift`:
`testAPartGoesOverTheDirectPathFirstAndNamesItsRelay`, `testWhenOnePathIsDownThePartTakesTheOther`,
`testAComputerThatDoesNotNameRelaysGetsThePartOnThePinnedRelay`,
`testAPartStaysOnTheRelayWhereItBeganAndAHeldCopyKeepsItsRelay`, `testThePathsAPartMayTake`.
