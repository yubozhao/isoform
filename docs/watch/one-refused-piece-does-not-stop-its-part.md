# One refused piece does not stop its part

2026-11-20, branch `claude/watch-piece-retry`. The phone's side of
`a-slow-piece-is-stored.md` (#1344, the relay).

## What happened

At noon the relay answered eight pieces with 500. For each, the phone stopped
everything the system held for that part, and fourteen pieces that were going
were cut off (the relay's 400 lines). Three parts went nowhere in that wake.
No audio was at risk: the phone keeps its copy.

The rule behind it: a refused piece stops its part, since its other pieces
would be refused the same way. True for an answer about the part (409, 413,
401). Not for an answer about the moment: the relay timing out or failing
inside says nothing about the next piece.

## The change (`ios/Yansu/RecordingBackgroundUploads.swift`)

- 408 and 5xx (except 507, a full relay) are answers about the moment. They
  no longer stop the part's other pieces.
- Such a piece is sent again by itself, from the same file, up to two times,
  while the others go on. This happens when the system reports the piece's
  end, also while the app is in the background.
- When its tries are used up the piece is over; the part's other pieces still
  finish, and the next pass hands over only what the relay does not hold.
- Everything else is as it was: 429 stops nothing and is not sent again here
  (the minute has to pass); other refusals stop the part.

## Guard

`RecordingPieceUploadsTests`: 500, 503 and 408 stop nothing, 507 and 401 do;
`afterPiece` says again, again, over for a 500, and stop for 409 and 507.
The sending itself runs through the system's background session and has no
unit test.

## Not proven

Proof is a wake on Bo's phone, on a named TestFlight build, where the relay
answers a piece 5xx and no 400 lines follow for that part.
