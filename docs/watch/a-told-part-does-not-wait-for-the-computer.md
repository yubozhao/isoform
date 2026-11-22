# A part the computer was told of does not wait for its answer

## What was seen

2026-11-20, Bo's phone, two background wakes. For every part the computer had been told of
before, the pass told it again and waited for the answer before handing the part over.

- 21:36:53: the computer answered the four tells within half a second (its log). The phone's
  next request for those parts reached the relay at 21:37:07, and the wake ended there.
- 21:49:13: the same, answered within a second; the phone went on at 21:49:21.

So 13 and 8 seconds of a 20 to 50 second wake passed between the computer's answer and the
phone acting on it, and no piece of those parts went. Where the seconds go (the relay's answer
poll, the tunnel, or the phone) is not known from these logs.

## What changed

`WatchRecordingForwarder.handOne` tells the computer again beside the hand-over, not before it.
The answer is still read, after the hand-over.

The answer is waited for in one case only, because there it decides something: the relay holds
nothing of a part that went up before. That is how a part looks once the computer has taken it
(the relay's copy is removed), and sending it again would put the whole part up for nothing.
`RecordingPieceUploads.beginKnowing(..., taken:)` asks then, and a part the computer has does
not go up. When the relay holds pieces of the part, the computer cannot have taken it, and the
missing pieces go at once.

Nothing about what is kept changes: a part leaves the phone only when the computer says it has it.

## Not proven yet

Tests only. Proof is from Bo's phone: in a wake, pieces of told parts in the same seconds as the
first requests, by the relay's pieces-by-the-minute line and the pass line.

## Open

Why an answer the computer gives in under a second reaches the phone 8 to 13 seconds later. A
relay line with how long each answer poll took and how many answers it carried would show
whether the relay or the phone holds them.
