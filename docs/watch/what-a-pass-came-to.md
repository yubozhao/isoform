# What a pass came to

2026-11-20, branch `claude/watch-pass-record`.

## Why

Four wakes on Bo's phone (08:00, 08:15, 08:30 and the evening before) put new
parts on the relay and sent no part request to the computer. From the relay and
the computer we could only see what did not arrive. Nothing on the phone said
whether its recording pass ran, what it found waiting, or where it stopped. The
only trace was the last outcome, in memory.

## What is kept

`RecordingPassRecord` in `ios/Yansu/WatchRecordingForwarder.swift`: the last 20
passes, in the forwarder's own state file. Each holds when the pass began, how
long it ran, and counts: parts whose turn it was, those the relay held whole,
those waiting after a refusal, parts handed to the system and what the relay
said of each, tells ahead and tells that got no answer, part requests sent,
parts taken, parts on their way, parts refused, the outcome, and a short word
for the kind of error that stopped it. Nothing of a recording is in it: no id,
no name, no time of a recording, no text.

- The record is written before the pass starts and again when it ends. A pass
  that is cut off, or hangs, leaves a record with no duration.
- A wake that finds a pass still running starts none. That adds one to the
  running record's `skipped`. A suspended app keeps its "a pass is running"
  flag for as long as the system keeps the app, so a pass that waits for an
  answer that never comes would stop every later wake; this count shows it.

## How it is read

The phone posts records the computer has not heard of to
`POST /recordings/passes` (PROTOCOL.md 24.2b), only to a computer that says
`recordingPasses` in its status. The computer writes one line per record to
`yansu-gui.log` (`[recordings] phone pass ...`) and keeps nothing else. A
running pass is told too, and told again when it ends. Nobody has to open
anything on the phone.

## The 08:30 wake, replayed

`testAPassLeavesARecordOfWhatItCameTo` builds that wake: ten parts whole on the
relay and a computer that answers no part request. The record reads pending 10,
whole 10, part requests 1, taken 0, stopped by `part-relay-timed-out`. That is
worth knowing by itself: one part request without an answer ends the pass, and
the phone waits up to 150 seconds for that answer while a wake lasts 4 to 60.
Whether that is what happens on Bo's phone, the record will say.

## Not a fix

This finds the step. Proof it works: a `[recordings] phone pass` line in the
computer's log from Bo's phone on a named TestFlight build.

## A stale pass no longer holds back a wake

A suspended app keeps "a pass is running" for as long as the system keeps the
app. If a pass waits for an answer that never comes, every later wake found it
running and started nothing. That is a suspicion, not a finding: it would
explain uploads that go on (the system does those) with no part request in any
wake.

- A running pass that has done nothing for 180 seconds no longer holds back
  `drain()`. 180 is longer than the longest wait for one answer (150 seconds,
  a part request), so a pass that only waits is left alone.
- The wake starts a pass of its own, and its record says
  `replacedStale: 1` ("a stale pass was replaced" in the computer's line).
- The replaced pass finishes the one thing it waits for and then stops: it
  sends nothing more, starts no hand-over, and does not change the outcome.
  A part both passes send is one part to the computer (its idempotency key).

The record proves or rules the suspicion out: `skipped` above zero and a
replaced pass mean it was this.

## 2026-11-20, later: a cut-off pass is told once, and the word carries the status

From the first day of records on Bo's phone:

- A pass the app was closed under left a record with no end. Such a record
  counted as "not told yet" for ever, so every later report sent it again
  (three of them by 13:02). At launch a record with no end is now closed with
  the outcome `cut-off` and a duration of 0, and told once.
- "stopped by part-RelayClientError" did not say which answer it was. The word
  now carries the status: `relay-500`, `relay-404`, `computer-no-answer`,
  `upload-on-the-way` and so on.

What "pending" counts: the parts whose audio is on the phone and whose turn it
is, read once when the pass starts. It does not fall while the pass runs, and
it never includes a part the computer has (that part's audio is removed when
the computer answers). The watch keeps handing the phone new parts, so the
number at the start of the next pass is this one, less what crossed, plus
what arrived.
