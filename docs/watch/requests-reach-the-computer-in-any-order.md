# Requests reach the computer in any order

2026-11-20, branch `claude/relay-requests-any-order`. The cause of "the phone
uploads and the computer never hears of the part".

## What happened

For two days, wake after wake, new parts reached the relay whole and no part
request reached the computer. Each wake the relay accepted a few more phone
requests than the computer's audit listed (49 and 45, 24 and 19, 22 and 14,
21 and 17). From 09:26 on 2026-11-20 the computer logged the two ways it can
end a request without running it, and neither line appeared: the missing
requests never reached the computer at all.

## Why

A phone numbers a request, then seals it and sends it. When the app opens it
sends dozens at once, so they reach the relay in any order.

In the relay (`relay/internal/hub/manager.go`, `relay/internal/store/envelopes.go`):

- it handed the computer only requests numbered above the highest it had
  already sent on that connection;
- the computer acknowledges each request it served with "up to this number",
  and the relay deleted every queued request with that number or lower.

So a request that arrived after a later-numbered one was answered 202, never
handed over, and deleted by the next acknowledgement. The phone waited for an
answer that could not come: 150 seconds for a part request, in a wake of 4 to
60. A part request is the likely loser of that race: it is numbered early,
follows a hand-over to the system, and is larger than a read.

Busy wakes (many reads) lost their part requests. Quiet ones delivered them:
six at 11:02 and four at 11:18 on 2026-11-20. The computer had already been
given a replay window for this same ordering; the relay still had a highest
number in two places.

## The change

- The relay keeps, per connection and phone, the request numbers it handed
  over, in the order it handed them over. It hands over every queued request
  not among them, lowest number first.
- An acknowledgement deletes the acknowledged request and those handed over
  before it on this connection. The computer serves a phone's requests in the
  order it was handed them, so anything handed over later has not been served
  yet, whatever its number: it stays until its own acknowledgement (or one for
  a request handed over after it).
- An acknowledgement of a number this connection did not hand over deletes
  nothing.
- A request nobody acknowledges is handed over again after a reconnect, is
  dropped after 24 hours (`RequestTTL`), and at most 200 are kept per phone
  (`MaxQueued`). Both limits were there already.
- The frames are unchanged: an old or new computer and an old or new phone
  work with it.

## Guard

`TestARequestThatArrivesAfterLaterNumberedOnesStillReachesTheComputer`
(`relay/internal/httpapi/devices_test.go`) replays a wake: eight reads numbered
2 to 9 arrive and are acknowledged as they are served; the part request,
numbered 1, arrives last and is handed over. The acknowledgement of read 9
leaves it queued; after a reconnect only it is handed over again; an
acknowledgement of another number leaves it; its own deletes it. On the relay
before this change the test fails waiting for number 1.

## The same shape towards the phone

Answers and events to the phone are numbered by the computer and then sent,
in seven places (`record.SendSeq++` in `wails-gui/`), each outside the lock
that numbered them. Two senders can leave in the other order. The relay hands
the phone only numbers above the one it asked after, an acknowledgement
deletes every lower one, and the phone itself keeps a highest number. So an
answer overtaken by a later-numbered event would be lost the same way. It
needs two senders at the same instant and the phone's poll in between, so it
is rare; it is not fixed here. The small fix is on the computer: number and
send in one step.

## Not proven

Shown in code and in a test, and it fits every log line. Proof is on Bo's
phone after the pinned relay runs this: a busy wake in which the relay's
accepted requests and the computer's served requests match, part requests
arrive, and Nov 16 and 17 reach the computer.
