# A part the relay holds whole takes no place

November 19, 2026.

## What happened

The phone keeps four parts with iOS at a time (`WatchRecordingForwarder.uploadsAtOnce`), so
the relay's room for a phone is not used up by parts the computer has not fetched. The four
places went to the first four waiting parts, whatever their state.

At 3:12 PM four parts were whole on the relay and the computer had not taken them. In that
wake the phone asked the relay about them, was told they were whole, and uploaded nothing
new: all four places were taken. Old parts waiting for the computer had stopped new audio
from going up.

## What changed

Only a part that is on its way takes a place.

- The uploader says which it is (`RecordingPieceUploads.beginKnowing`): not handed over,
  handed to iOS, or whole on the relay.
- A part found whole is remembered as held (so the next wake tells the computer first and asks
  the relay nothing), the computer is told of it at once when it takes that, and the next
  waiting part gets the place in the same wake.
- The same holds for a part the computer answers it already has.
- A part that could not be handed over keeps its place, so a relay that is down is not asked
  about every waiting part.
- This needs no answer from the computer: the next parts go up while it sleeps.

The relay still keeps at most 48 uploads for a phone; past that it answers a piece with 429,
and those pieces wait in iOS's hands (#1327).

## What did not change

A part leaves the phone only when the computer answers with its own copy.

## Proof

Merged is not proven. Proof is Bo's phone on a named build: new parts going up in a wake
while older ones still wait, seen in the relay's upload lines.
