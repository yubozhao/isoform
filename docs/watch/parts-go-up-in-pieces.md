# With the app open, a part goes up in pieces

Step 3 of `docs/design/watch-parts-reach-the-computer.md`.

On 2026-11-18 the public address of a relay cut every request after 10 to 25 seconds. A part of
a watch recording is about 3 MB in one request, so none arrived, and every try began again at
zero. Three things went wrong on the phone that day, and each has its fix here.

## Pieces

`RecordingPieceUploads` (phone) sends the sealed part as pieces of 256 KiB to a relay that says
it takes them (`uploadPieces` in `/v1/health`, PROTOCOL.md 24.1a). Before the first piece it
asks the relay how much it holds, and goes on from there; a piece the relay did not expect is
answered with the held size, and the phone continues at that byte. A cut costs one piece.

The bytes and the upload id are the ones the whole-body upload uses, so the two can take turns
on one part. Pieces need the app to run. With the app in the background, or a relay that does
not take pieces (asked again after 10 minutes), the part goes as one background upload as
before. Before pieces start, an upload iOS still holds for the same part is stopped, so the same
bytes are not put twice.

## Uploads iOS held and did not finish

iOS waits longer and longer before it retries a background upload that failed. Uploads handed
over at 12:16 were still "held" at 12:41; the phone waited for them, and neither opening the app
nor Sync now started anything.

- An upload held for 5 minutes (`RecordingBackgroundUploads.stuckAfter`) is stopped and started
  afresh on the next pass. The time it was handed over is kept across launches; an upload from a
  build that did not keep it counts as old.
- Sync now stops everything held longer than 30 seconds first, so it really sends now.
- Each try has its own task and its own file (`<id>-<uuid>.bin`); a stopped try that ends late
  touches neither the new try's state nor its file.

## The Watch page that said it could not reach the computer

The page asked the computer once when it opened. That one answer was lost, and the page showed
"Couldn't reach your computer" until someone pulled down. Now a failed load is tried again every
15 seconds while the page is open, and when the app comes to the front.

## What does not change

Nothing is deleted on the phone until the computer says it holds the part. Sealing, upload ids,
the order (oldest first) and the Watch tab's lines are as before.

Guards: `RecordingPieceUploadsTests`, `RelayClientUploadPiecesTests`,
`RecordingBackgroundUploadsTests` in `ios/YansuTests/RecordingPieceUploadsTests.swift`.
