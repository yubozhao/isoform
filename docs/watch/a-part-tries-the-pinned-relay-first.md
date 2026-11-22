# A part tries the pinned relay first

2026-11-20, branch `claude/watch-pinned-first`.

## What happened

From 13:57 on 2026-11-20 no upload became whole. At 14:45 three passes each
spent 33 to 43 seconds on a part and ended in an error, and the pinned relay
logged no piece from the phone at all.

A part the pass sends itself (one the system does not hold) took the direct
path first whenever the computer fetches from the named relay. The direct path
is a door inside the app to the computer's own relay. The computer's own relay
logged nothing from the phone all day: no request, no piece. So each such part
spent half a minute on a path that carried nothing, and when that try ended in
an error the phone does not count as "the path is down", the pass ended there
and the part never went to the pinned relay.

## The change (`WatchRecordingForwarder.paths` and `send`)

- A part tries the pinned relay first. The direct path is tried only when the
  pinned relay cannot be reached (offline, a timeout, 5xx, a refused pairing).
- An error on the direct path, whatever it is, moves the part to the pinned
  relay in the same pass; it never ends the pass. This matters for a part that
  began on the direct path and goes on there first.
- When both fail, the pinned relay's reason is the one the pass reports.
- A computer that only looks on the pinned relay gets every part there, as
  before.

The pinned relay is also the only one the system can upload to while the app
sleeps. `docs/design/watch-parts-reach-the-computer.md` says so now, with the
date.

## Guards

`testAPartGoesToThePinnedRelayFirstAndNamesItsRelay`,
`testWhenOnePathIsDownThePartTakesTheOther` (the direct path is not tried while
the pinned relay answers; a part that began on the direct path moves to the
pinned relay on any error there), `testThePathsAPartMayTake`.

## Not here

Why the direct path carried nothing from the phone all day is a fault of its
own, in the phone's connection, and is not looked at in this change.

## Not proven

Proof is a pass line from Bo's phone, on a named TestFlight build, with no
half-minute passes, piece lines on the pinned relay, and counts rising.
