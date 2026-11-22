# A failing part does not loop the pass

2026-11-20, branch `claude/watch-more-uploads-at-once`.

## What happened

From 13:57 on 2026-11-20, with the app open on TestFlight 20261120205147, the
phone ran a recording pass every two to three seconds for minutes and nothing
moved. In four minutes the computer served 182 part requests, 169 of them
answered "expected" for the same few parts; the relay answered 140 "how much
do you hold" questions about the same four uploads; nine pieces were cut off;
no upload became whole. The pass lines read: 2 to 5 seconds, outcome waiting,
stopped by an error on a part, handed 4, told 4, part requests 0.

## Why

- Every answer or event from the computer asks for a pass, so the computer
  being back sends waiting parts at once. While a pass runs, such an ask is
  kept as "another pass is wanted".
- A pass that ended, however it ended, started that other pass at once.
- One upload had failed every try since noon. Each pass ended on it within
  seconds, and the answers it had drawn from the computer had already asked
  for the next pass.
- Each pass asked the relay about every part in flight and told the computer
  of each again.

Why that one upload's pieces are cut off is not known yet (the phone stopping
them, or the path dropping them); the next build's pass line names the answer
the phone saw.

## The change

- **No loop.** Another pass runs at once only after a pass that sent
  everything. After any other pass the next one comes at the retry time, or
  when an upload ends. In `WatchLink`, after such a pass no pass starts for 10
  seconds however often one is asked for; a tap on Try again and a background
  run are not held back.
- **A part told ahead is left alone for a minute.** It is not told again,
  asked about or sent again within 60 seconds of being told.
- **One failing part does not hold up the others.** A pass goes on to the
  parts behind a part that failed, and ends at the second failure, so a path
  that is down is not tried for every part.
- **Eight parts with the system at a time** (four before). The phone's queue
  was the limit: the relay held next to nothing whole, because the computer
  took each part as soon as it arrived. The relay's limits leave room: one
  phone may keep 48 parts and 192 MB waiting, and start 600 parts and send
  16000 pieces an hour.

## Guard

`testAFailingPartNeitherLoopsThePassNorHoldsUpTheOthers`: three parts wait, the
first fails, and another pass is asked for while the pass runs. One pass is
made, the two parts behind the failing one cross.
`testTheComputerIsToldWhenAPartIsHandedOverAndThePartStaysUntilItHasIt` now
also shows that a wake within the minute tells and sends nothing again.

## Not proven

Proof is an open app on Bo's phone, on a named TestFlight build, with pass
lines seconds apart at most a few times a minute, uploads becoming whole, and
the counts rising.

## An upload the relay keeps refusing starts afresh

One upload (short id af7a93ae) failed every try from noon until at least 14:00.
What the relay's lines say of it, by hour: 11h one question about it; 12h
eight pieces answered 400; 13h 93 questions about it, eight pieces answered
400 and one 500. No line says it was ever whole. The relay does not log a
piece it stored (only the one that finishes an upload), nor sizes or offsets,
so how far each try got is not in the log; neither is which of its three
reasons for a 400 applied (no offset or total, a bad size, or a piece that did
not arrive whole). Other uploads became whole in the same wakes, so the path
was not down.

An upload like that never heals by itself, and its part never reaches the
computer. After three refusals in a row by the relay (a 4xx or 5xx answer, not
401 or 429, not being offline, not a timeout) the part forgets that upload:
its next try seals it anew under a new id and sends every piece again. The
audio stays on the phone throughout. The relay has no way for a phone to
delete an upload, so what it holds of the old one runs out by itself after 48
hours; it counts against the phone's room on the relay until then (one part,
about 2.8 MB of 192).

Guard: `testAnUploadTheRelayKeepsRefusingStartsAfresh`.

Follow-up for the relay: say why a piece was answered 400, with its offset,
total, size and how many bytes arrived.

## The count of refusals is kept across launches

Branch `claude/watch-refusals-kept`. The count lived in memory, and Bo's phone
starts the app afresh often, so it might never reach three and the stuck part
would never heal. It is now in the forwarder's state file (`refusals`, beside
the pass records), written when it changes and dropped when the part crosses
or its upload starts afresh. Guard: `testRefusalsAreCountedAcrossLaunches`
(three runs of the app, one refusal each).

## At most two fresh starts per part in one version of the app

Each fresh start leaves a half-done upload on the relay for 48 hours, and it
counts against the phone's room there (48 parts, 192 MB). A part whose new
uploads are refused too would fill that room and stop every other part. So a
part starts afresh at most twice in one version of the app; after that it keeps
its upload and waits. A new version gets its own tries, since a fix for what
refuses the upload comes with a version. The counts are in the state file with
the version that made them. Guard: `testAPartStartsAfreshAtMostTwiceInOneVersion`.
