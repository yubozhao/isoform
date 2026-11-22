# The relay counts the pieces of each minute

2026-11-20. A stored piece leaves no line on the relay (a part has eleven of them), so the log could say when a part became whole but not whether a phone sent anything while its app was away. On Bo's phone eight parts sat with iOS for seven minutes and the log could not say whether any piece had moved.

The relay now writes one line for each minute in which a phone sent pieces, when the minute is over:

    pieces in the minute from 19:39: 22 stored, 2 that made a part whole, 1 refused, 0 failed, at most 6 arriving at once

- stored: answered 200. That made a part whole: answered 201. Refused: 4xx. Failed: anything else.
- Numbers only. Nothing of a piece, an upload or a phone. A minute without pieces leaves no line.

How to read it: lines in minutes where the phone made no other request mean iOS is sending while the app is away.

Code: `relay/internal/httpapi/upload_log.go` (`pieceMinute`).

## How many at once (2026-11-20)

The line ends with the most pieces that were arriving at the same moment in that minute. A
phone hands up to eight parts of eleven pieces to its system at a time, so up to 88 uploads
may be waiting there; this number says how many of them were on the wire together. It is the
measure for whether a full uplink is why small answers reach the phone late.
