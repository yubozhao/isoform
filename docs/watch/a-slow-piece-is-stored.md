# A piece that arrives slowly is stored

2026-11-20, branch `claude/relay-slow-piece`.

## What happened

At noon Bo's phone sent pieces of three parts. The relay's lines: eight
`phone PUT upload pieces: Internal Server Error (500)` from 12:00:04 to
12:00:07, then fourteen `Bad Request (400)` for the same three parts in the
two seconds after. Nothing of those parts was kept on the relay. No audio was
at risk: the phone keeps its copy until the computer has the part.

## Why

- **The 500s.** `uploadPiecesRoute` (`relay/internal/httpapi/uploads.go`) made
  its five-second store context first and read the piece's bytes after. A
  piece of 256 KiB that took more than five seconds to arrive, on a slow link
  or beside the pieces of three other parts, had no time left when it reached
  the store, and was refused with 500 although it arrived whole. The first
  500 came six seconds into the wake.
- **The 400s.** On a refused piece the phone stops what the system holds for
  that part (`RecordingBackgroundUploads.stop`), which cancels its other
  pieces in mid-flight. The relay sees each as "The piece did not arrive
  whole" (400). They follow from the 500s.

This is read from the code and the timing; the relay did not log why it
answered 500.

## The change

- The store's five seconds start after the piece has been read.
- A piece that cannot be stored now writes its cause beside the 500:
  "piece not stored: the phone went away first", "timed out", or "store: "
  and the store's error text. Nothing of the piece is written.

## Guard

`TestAPieceThatArrivesSlowlyIsStored`: a piece that takes three times the
store's limit to arrive is answered 201.

## Not changed

- The route for a whole upload in one request (`PUT /v1/uploads/{id}`) reads
  its body inside the same five seconds. Phones that send pieces do not use
  it; follow-up.
- The relay's 15-second limit for reading one request stays. A piece slower
  than that is cut by the server and answered 400.
- The phone still stops a whole part on one refused piece. With the cause
  logged, the next refusal will say whether that needs changing.

## 2026-11-20, later: a refused piece says why

One upload had sixteen pieces answered 400 over two hours while other uploads
became whole. The relay has three reasons for a 400 on a piece (no usable
offset and total, no usable size, a piece that did not arrive whole) and wrote
none of them. Each now writes a line: the reason, the piece's offset in the
upload's total, the piece size, and how many of the announced bytes arrived.
Numbers only.
