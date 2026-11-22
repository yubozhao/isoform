# A busy relay does not stop a part, and pieces go up once

2026-11-19. Phone only. The relay half is `pieces-have-their-own-request-count.md`.

## What happened

Bo opened build 20261119040114 at 08:15 with about 29 hours on the phone. Four parts went to iOS in
11 pieces each. Within seconds the relay answered "too many requests" (429). In 3.5 minutes one
part reached the computer, and each piece had gone up 3 to 4 times.

## Two causes on the phone

1. A piece that came back with any refusal stopped the part's other pieces. That is right for a
   real refusal, which every piece would get. A 429 is about the minute, not the part: the ten
   pieces still on their way were wanted, and were cancelled.
2. Each pass handed every piece of the next four parts to iOS without asking the relay, whenever
   iOS held none of them. After the first time that sent bytes the relay already held.

## The rule now

- A 429 on a piece is kept and said on the next pass, and the part's other pieces go on
  (`RecordingBackgroundUploads.stopsThePart`).
- The first hand-over of a part still asks nothing, so a few seconds in the app are enough. A part
  handed over before is asked about first, and only the pieces the relay does not hold go again.
  Which parts were handed over is kept across launches (the last 400), because iOS starts the app
  again for seconds at a time. With no answer from the relay, nothing is handed over blind; the
  pass sends the part.
- Unchanged: the phone deletes a part only when the computer confirms it has it.

## Guards

`testAPartHandedOverOnceIsNotHandedOverWholeAgain`, `testWhatIsHandedOverIsKeptAcrossLaunches`,
and the 429 lines in `testATaskNamesItsPartAndItsOwnFile`.

## Not covered

When the relay says 429 the Watch page still reads as a full relay ("relayFull"). With the relay's
own budget for uploads that answer should be rare; the wording is design's.
