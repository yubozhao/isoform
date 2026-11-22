# Whole parts are told side by side

2026-11-20, branch `claude/watch-whole-parts-side-by-side`.

Held until the pass record confirmed it. It did, at 12:35 on 2026-11-20: Bo's
phone reported pending 296, whole 14, and part requests 8 with 6 taken after
two minutes of open app. Part requests go one after the other, one every 10 to
15 seconds, so 296 parts take hours of open app.

## What the replay showed

A part the relay holds whole needs one thing: a part request to the computer.
The pass sent those one after the other and waited up to 150 seconds for each
answer. A wake lasts 4 to 60 seconds. So a wake told at most one part, and one
request without an answer ended the pass for all the others. Before any of
that the pass asked the computer what it takes, and an ask that failed ended
the pass with nothing told.

## The change

- When a pass starts, every whole part (up to 16) gets its part request at
  once, side by side, before anything is asked. They go with what the computer
  said it takes the last time; that is now kept across wakes (`takes` in the
  forwarder's state file).
- Once a request is with the relay the computer takes the part, whether or not
  the phone is still awake for the answer. The phone removes a part only on the
  computer's answer, in this wake or the next.
- A part request with no answer no longer ends the pass. The requests sent
  beside it are waited for; no new one is started after it, so a computer that
  is away costs one wait, not one per part.
- Before the pass ends it waits for every request it sent.

## What stays

Nothing leaves the phone or the relay before the computer has the part. Parts
that are not whole yet go as before (hand-over, tell ahead, then their request
in order). A computer that never said what it takes is asked first, as before.

## Guards

`testWholePartsAreToldSideBySideAndOneWithoutAnAnswerDoesNotStopTheOthers`:
ten whole parts, the computer cannot say what it takes and leaves one request
unanswered; ten requests go out, nine parts cross, the tenth crosses on the
next wake with one request. `testAPassLeavesARecordOfWhatItCameTo` now reads
ten part requests where it read one.

## Not proven

This follows a replay, not a finding from the phone. The pass record decides:
if Bo's phone shows part requests 1 and "stopped by part-..." per wake, this is
the fix; if it shows none sent, it is not.

## Also here

A wake that finds a pass running is written down and told to the computer at
most once a minute. The same session skipped 88 times in two minutes (every
piece that ends asks for a pass), and each skip wrote the state file and asked
the computer. The count itself is kept in memory between tellings and goes out
with the next one.

What asks so often: every answer or event from the computer calls
`WatchLink.shared.drain()` (`AppEnvironment`, "heard from the computer"), so
the computer being back sends waiting parts at once. With 87 answers in two
minutes that is most of one ask a second. Each ask also took background time,
read the list of recordings and reset the retry timer. Now, while a pass asked
from there runs, an ask only tells the forwarder that another pass is wanted.
