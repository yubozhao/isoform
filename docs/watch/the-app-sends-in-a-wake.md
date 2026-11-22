# In a background wake the app sends the pieces itself

## What was seen

2026-11-20, Bo's phone on TestFlight 20261121042319, the relay's count of pieces by the minute
(#1354) beside the phone's requests by the minute:

- Two wakes, 21:36:48 to 21:37:07 and 21:49:12 to 21:50:06.
- Pieces reached the relay only in minutes the app was awake: 21:49 one stored and four cut
  short, 21:50 one stored and one cut short.
- In the 11 minutes between the wakes and the 11 minutes after: no piece at all.

Every piece had been handed to iOS as its own background upload. iOS treats an upload that is
started while the app is in the background as one that can wait, whatever the session's
`isDiscretionary` says, and sends it when it likes (in practice on power and Wi-Fi). Nearly all
our hand-overs happen in background wakes, so nearly nothing moved while the app was away.

## What changed

With the app in the background and time left, `RecordingPieceUploads.beginKnowing` sends the
missing pieces of a part itself, over the same piece requests (PROTOCOL.md 24.1a):

- One part at a time, in the order the pass asks, which is oldest first.
- The relay is asked first what it holds; only the missing pieces go.
- A piece is started only with 8 seconds of background time left, and given up when 3 are left.
  The relay keeps a piece whole or not at all, so a cut piece costs nothing.
- When the time runs out or a piece fails, what is still missing is handed to iOS as before.
- With too little time for one piece, or the app in front, everything goes as before.
- A part iOS has held for minutes (#1355): the relay is asked, what iOS holds is stopped, and
  the app sends what is missing. No answer from the relay: what iOS holds is left as it is.

Nothing is deleted: only iOS's upload tasks and their piece copies are stopped. The part's file
and its place among the waiting parts stay until the computer confirms its copy.

## The rollback switch

The computer's: `recordingInWakeSendEnabled` in `wails-gui/mobile_recording_passes.go`. On, the
status says `"recordingInWakeSend": true`. Off, or a computer from before, the phone hands every
piece to iOS as before. The phone uses what the computer said in the pass before, since parts
are handed over before anything is asked.

## How to see it

The computer's pass line ends with `parts sent in the wake N`. The relay's
`pieces in the minute from HH:MM` line shows stored pieces in the minutes of a wake.

## Not proven yet

Tests only. Proof is Bo's phone: pass lines with `parts sent in the wake` above 0 from short
wakes, and Nov 16 and 17 arriving on the computer.
