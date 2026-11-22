# With the app closed, the pieces go to the system

Step 4 of `docs/design/watch-parts-reach-the-computer.md`. It builds on
`docs/watch/parts-go-up-in-pieces.md` (app open) and on the relay taking pieces in any order
(PROTOCOL.md 24.1a, "Pieces in any order").

## What happens

When the phone app is in the background (the watch just handed it a part, or iOS woke it) and
the relay takes pieces in any order:

1. The phone asks the relay which pieces of the part it holds.
2. Every missing piece is handed to iOS as its own background upload, from its own small file
   (`RecordingBackgroundUploads.putPieces`). Nothing is handed over twice.
3. iOS sends them while the app is closed, when and in the order it likes, and retries each by
   itself. A cut costs one piece of 256 KiB.
4. When none is left with iOS, the forwarder makes a pass, asks the relay again, and only the
   relay's "complete" makes the part sent. Pieces that did not arrive are handed over again.

With the app open the app sends the missing pieces itself, and first stops what iOS still holds
for that part. Both ways use the same sealed bytes, the same upload id and the same piece size,
so they take turns on one part without sending a held piece again.

## Why any order

iOS decides when each background upload runs. If pieces had to arrive in order, every piece
behind a late one would be refused after its 256 KiB were sent, and the app would have to be
woken to send each next piece.

## Refusals

The first piece the relay refuses (the phone is no longer paired, the relay is full) is kept for
the forwarder, and the part's other pieces are stopped: they would be refused the same way. The
Watch tab then says what it says for a whole upload (pair again, too many waiting).

## Fallbacks, unchanged

- A relay that takes pieces only in order, with the app closed: one background upload of the
  whole part.
- A relay that takes no pieces: the same.
- Pieces iOS has held for 5 minutes without an end are stopped and handed over afresh; Sync now
  does that at once.

Nothing is deleted on the phone until the computer says it holds the part.

Guards: `RecordingPieceUploadsTests` (any order, background, what is missing),
`RelayClientUploadPiecesTests`, `RecordingBackgroundUploadsTests`.
