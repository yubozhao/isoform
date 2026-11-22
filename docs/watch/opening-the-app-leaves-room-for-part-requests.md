# Opening the app leaves room for part requests

2026-11-20, branch `claude/watch-relay-device-budget`.

## What happened

At 08:00:17 Bo's phone opened Isoform for thirty seconds. The relay's own lines
(status only) show 32 requests for the computer accepted in the first eleven
seconds, then 16 refused with 429 from 08:00:28 to 08:00:47. The computer's
request audit shows briefs, Today and status reads, and no part request. No
part was taken.

The relay allowed one device 60 authenticated requests a minute. A request for
the computer costs more than one of them: the phone also long-polls for the
answer and acknowledges it. Opening the app used the whole minute in eleven
seconds, and everything after that was refused, the request that hands a
recorded part to the computer among them. Upload pieces already had their own
budget (#1328 era), so the pieces arrived and then the hand-over could not be
asked for.

## The change

`deviceRequestsPerMinute` goes from 60 to 600 in
`relay/internal/httpapi/handler.go`. `mobile-protocol/PROTOCOL.md` says 600 in
its three places. The other limits stand: failed authentications per address,
open requests per address, the request queue's cap, and the upload budgets.

A relay holds only ciphertext and runs for one person's devices, so the limit
is there to stop a runaway client, not to ration the app.

## Guard

`TestOpeningTheAppLeavesRoomForTheRequestsThatFollow`: fifty reads, each with a
long-poll and an acknowledgement, then one more request is still served.

## Not proven

Merged is not proven. Proof is a wake on Bo's phone, after the pinned relay
runs this, with no 429 line and parts from Nov 16 and 17 taken by the computer.

## Follow-up

The phone sends about fifty requests on open (eight `/status` reads among
them). Fewer would be better, and part requests could go first.
