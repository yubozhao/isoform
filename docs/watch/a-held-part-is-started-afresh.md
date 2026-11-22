# A part the system holds for minutes is started afresh, and the computer is told as it is handed over

2026-11-20, Bo's phone. Two faults kept parts from the computer while the app was away.

## A held part counted as "on its way" for ever

iOS holds a background upload that failed without ending it, and waits longer before each new try, for up to our twelve-hour limit. `beginKnowing` answered "handed" as soon as iOS held any piece of a part, and so never reached the code that starts pieces afresh after five minutes. Four parts handed over at noon were still "on their way" at 8 PM. The computer asked the relay for each every 30 seconds and got 404. They held four of the eight places new parts go up in; before 2026-11-20 there were four places, and they held all of them from 1:57 PM to 6:13 PM.

Now, when iOS has held a part for `stuckAfter` (five minutes) or longer:

1. The relay is asked what it holds of the part.
2. No answer: nothing is stopped. What iOS holds stays.
3. The relay has it whole: what iOS holds is stopped and the part counts as whole.
4. Otherwise what iOS holds is stopped and only the missing pieces are handed over again.

Only iOS's uploads and their own piece copies are stopped. The part's file and its place among the waiting parts are never touched: the phone deletes nothing until the computer confirms its copy.

The pass record counts these as `restarted`, and the computer's log line ends with "held parts started afresh N". The phone leaves the field out for a computer that does not advertise `recordingPassRestarts` (PROTOCOL.md 24.2b), since an older computer refuses a record with a field it does not know.

## The computer was told after the hand-over

The tell came after the hand-over and its question to the relay. At 7:46:58 PM four parts became whole on the relay in the second the app left; the tell had not gone out, and the computer asked for none of them until the phone woke again.

Now a part the computer has not been told of is told at the same moment as it is handed over. If the hand-over then fails, the part stays on the phone, is not marked as told, and goes up at the next pass; the computer asks the relay for a while and lets it go, as before (24.2a). The tell does not hold the hand-over back: a computer that is asleep costs the upload nothing.

Code: `RecordingPieceUploads.handAgain`, `RecordingBackgroundUploads.stuck`, `WatchRecordingForwarder.handOne`.
