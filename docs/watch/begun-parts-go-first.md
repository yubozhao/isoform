# Begun parts go first, and a full relay stops only new parts (2026-11-22)

## What happened

The night of Nov 21 Bo's phone was silent for 16.5 hours. When it came back, with about 255
parts waiting, the relay refused every new part: its places for this phone were all taken
(`limit=waiting_uploads count=48 cap=48`), mostly by uploads half sent the evening before. The
watch had handed the phone older parts in between. They sort first, each was refused, and the
pass ended at the first refusal. It never reached the half-sent parts, whose finishing is the
only thing that frees a place. The jam could not clear itself before those uploads expired, 48
hours after they began.

## Why the phone could not know

It marks a part as uploading before the first byte leaves. A part the relay refused and a part
the relay holds half of look the same in the phone's records.

## What changed (phone)

- At the start of a pass the phone asks the relay once what it holds of this phone's uploads
  (`GET /v1/uploads`, PROTOCOL.md 24.1c), and only when some part was begun and is not known to
  be whole. A relay without the route, or no answer in 6 seconds: the pass goes on as before.
- Parts the relay holds something of go first, into the hand-over places and in the pass; each
  group keeps its order, oldest first.
- When the relay answers "full" (429) to a part it holds nothing of, new parts stop for this
  pass and the parts it holds something of go on: their pieces need no new place. The pass
  record still says `part-relay-429` and the outcome is still "relay full".
- A 429 for a part the relay already holds is about the minute, not the places, and ends the
  pass as before.

Nothing is deleted. A part that waits keeps its audio and its record, and a full relay is not
counted against it.

## With the relay's cap

The relay's count of places goes from 48 to 512 (the bytes stay the bound). That makes the jam
rare; this makes the phone get out of one by itself.
