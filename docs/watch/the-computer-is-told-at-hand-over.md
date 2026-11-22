# The computer is told when a part is handed over

November 19, 2026. PROTOCOL.md 24.2a.

## What was wrong

A part reached the computer in three steps: the phone uploads it to the relay, the phone tells
the computer, the computer fetches it. The upload runs in iOS's hands with the app asleep. The
second step needed the app awake again after the upload, long enough to ask the computer and
hear back. Wakes are about hourly and about 30 seconds long. On November 19 four whole parts
sat on the relay for five hours.

## What changed

The phone tells the computer at the moment it hands the part to iOS, in the same wake. The
computer keeps that and asks the relay every 30 seconds until the part is there, then takes
it. Nothing waits for the phone to wake again.

- Computer: `wails-gui/mobile_recordings_ahead.go`. A request with `"ahead": true` whose upload
  is not on the relay is kept in `mobile/recordings/expected.json` (0600) and answered
  `202 {"state":"expected"}`. One loop per bridge fetches expected parts; it starts when a part
  is kept and at launch when the file has any.
- Phone: `WatchRecordingForwarder.handAhead`. After a part is with iOS, `tellAhead` sends the
  request and waits at most 10 seconds for the answer. A part answered "expected" is marked
  `told` on disk.
- Next wake, a `told` part is asked about first. If the computer has it, the part leaves the
  phone and nothing is uploaded. Otherwise the relay is asked what it holds and only missing
  pieces go again.

## In the log

A pass of the loop that took or dropped a part writes one line, counts only:

    [recordings] expected parts: took 2, dropped 0, 3 still expected

A pass that only waited writes nothing.

## The rule that did not change

The phone deletes a part only when the computer answers with its own copy (any state but
`expected`). The computer deletes the relay's copy only after it verified and stored the part.

## Old and new together

- Older phone, new computer: the phone never sends `ahead`; everything is as before,
  `404 upload_missing` included.
- New phone, older computer: the computer does not advertise `recordingAhead`; the phone never
  sends the field.

## Limits

- While Closed Eyes is on, the computer keeps nothing and fetches nothing; the phone tells it
  again on a later wake.
- If the computer takes a part between the phone asking it and the phone asking the relay, the
  phone uploads that part once more. The computer then answers with its copy and removes the
  second upload.
- A kept request holds the upload's key, on the user's own computer, beside the audio it opens.

## Proof

Merged is not proven. Proof is Bo's phone on a named build: November 16 and 17 on the computer,
with counts and minutes, without the app being opened.
