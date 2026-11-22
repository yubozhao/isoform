# Every part request leaves a line

November 19, 2026.

## What happened

After the relay's request limit was fixed (#1326), a phone's parts reached the pinned relay
whole: four finished parts were on it by 2:16 PM. The computer took none of them for more
than five hours. Neither side could say why:

- The computer wrote a line only when a fetch failed. A part request it answered with
  "not on the relay", "observing is paused" or "not valid" left nothing, and neither did one
  it took.
- The relay wrote nothing about uploads. The tunnel in front of it logs the path of each
  request and never the answer.

So the logs could not tell a phone that never asked from a computer that kept saying no.

## What changed

The computer writes one line for every way a part request ends:

    [recordings] part 3 of <recording id> taken: received
    [recordings] part 3 of <recording id> is not on the relay
    [recordings] part 3 of <recording id> waits: observing is paused here
    [recordings] part 3 of <recording id> waits: the computer is not ready
    [recordings] part 3 of <recording id> refused: <which check>
    [recordings] part 3 of <recording id> not received
    [recordings] a part was refused: the recording is not valid

The last one is for a request that fails its checks before anything is fetched: nothing of
such a request is written, only that it came.

The relay writes one line for each upload request, from a phone or from the computer:

    phone PUT upload pieces 9a287bb9: Created (201)
    phone GET upload pieces 9a287bb9: OK (200)
    computer GET upload 9a287bb9: Not Found (404)
    computer DELETE upload 9a287bb9: OK (200)

A stored piece (200 to a piece PUT) is not written; a part has eleven, and the 201 that ends
the part says they arrived. Only the first eight characters of a well-formed upload id are
written, and a method other than GET, PUT and DELETE is written as "other", so nothing a
caller made up reaches the log.

These lines are counts, numbers and ids. No audio, no words, no keys.

## What it does not do

It finds the step that stops; it does not move a part. The fix for the step comes after the
next wake of the phone names it.

## A request leaves a line before it is answered (2026-11-19, evening)

On the 19:36 wake the phone read its uploads for eight seconds and the computer
wrote no line for a part. The lines above are written when a request ends, so
they could not say whether a request had been sent, had reached the relay, or
was waiting on the computer when the phone went back to sleep. Two lines close
that:

- the relay writes `phone request for the computer: Accepted (202)` for every
  request a phone sends for the computer. The request is sealed, so the line
  holds the status and nothing else;
- the computer writes `[recordings] a part request arrived` the moment a part
  request reaches it, before it reads or fetches anything.

A wake with the relay line and no computer line means the request did not get
from the relay to the computer in time. A wake with neither means the phone
sent none.

## 2026-11-20: the two requests that left no line

In the 08:30 wake (60 seconds, relay at 600 a minute, no 429) the relay accepted
49 requests and the computer's audit lists 45; in earlier wakes 24 and 19, 22 and
14. No part request appears in the audit. The computer runs a request and writes
the audit only after two checks that end without a word: a sequence number it
has seen before (acknowledged, not run), and a request it cannot read (answered
400). Both now leave a line in `yansu-gui.log`, with the sequence number and,
for the second, the size in bytes. Nothing of what the request holds is written.

This tells a request sent over both paths from one the computer turned away.

## 2026-11-20: a request the relay could not queue says why

At 10:18:06 the relay answered one phone request with 500, in the second the
computer fetched and deleted an upload. The relay's line said only "Internal
Server Error (500)". From the code: the relay opens its store with one
connection and a 5 second busy wait, so two requests inside the relay cannot
meet a locked store; they wait for the one connection. What ends such a wait
is the request's own context: the phone going away (a cancelled request, or
the app put to sleep) ends it and the handler then answers 500 to nobody. That
is the likely cause, not a finding: the log could not say.

`POST /v1/requests` now writes the cause beside the 500: "the phone went away
first", "timed out", or "store: " and the store's error text (a SQL error, no
values). Nothing of the request is written.

On the phone a 500 from the relay is not a refusal: the part is not counted
against, it gets no waiting time, and the next wake sends it again. It does
end the pass the request was in (until #1341).
