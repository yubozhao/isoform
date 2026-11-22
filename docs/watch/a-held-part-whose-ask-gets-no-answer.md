# A held part whose ask gets no answer

## What was seen

2026-11-20, Bo's phone. Four parts handed to iOS at noon (af7a93ae, a850d4ae, b50be502,
0e6cbc7d) were asked about on every wake since #1355: the phone asked the relay which of their
pieces are missing, the relay answered 200 each time (21:37:07, 21:49:21, 22:58:18 to 22:59:27),
and no restart was ever counted. The computer still got 404 for all four at 23:13.

When that ask gets no answer in time, #1355 leaves what iOS holds as it is, on purpose. Nothing
said that this was what happened, and nothing ended it.

## What changed

- The ask for a part iOS holds waits 30 seconds for its answer (was 15).
- When such a part is left as it is, the pass record says why in `stoppedBy`:
  `held-ask-<why>` (no answer), `held-nothing-missing`, `held-hand-over-<why>`.
- After three asks in a row without an answer for the same part, what iOS holds of it is
  stopped and the part is started afresh from its file on the phone, every piece
  (`held-afresh-unasked`, counted in "held parts started afresh"). The count is kept across
  launches and starts again with each version of the app, and with any answer.

Nothing on the phone is deleted: only iOS's upload tasks and their piece copies are stopped. The
relay may get bytes it holds already; a piece it holds is answered without being stored again.

## Not proven yet

Tests only. Proof: the four noon uploads on the computer, and a pass line with "held parts
started afresh" above 0 or a `held-` word.
