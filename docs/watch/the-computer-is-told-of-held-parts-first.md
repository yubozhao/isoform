# The computer is told of held parts first

November 19, 2026.

## What happened

After the relay's request limit was fixed (#1326), parts reached the pinned relay whole. Four
were on it by 2:16 PM, and the computer took none in more than five hours. The phone woke
seven times in that span, about 30 seconds each.

In each wake the phone first handed waiting parts to iOS, one after the other, and only then
asked the computer anything. The tunnel's log shows a 20 to 25 second gap inside that
hand-over in three wakes (1:16, 1:31 and 2:16 PM). The wake was over before the phone told
the computer of a single part.

Why one hand-over took that long is not known from the computer's side of the logs.

## What changed

Order inside one pass of the phone's forwarder (`WatchRecordingForwarder.pass`):

- Each waiting part is handed to iOS by itself, all at the same time. The pass does not wait
  for them.
- Parts the relay already holds whole come first. Telling the computer is all they need, and
  they wait for no hand-over.
- A part that is being handed over is sent only after its own hand-over ends, never while it
  runs, so no piece is given to iOS twice.
- Every hand-over begun still ends before the pass does.
- A held part takes no place among the four that are handed ahead.

Nothing about what is deleted changes: a part leaves the phone only after the computer says
it has it.

## What it does not do

The phone still has to be awake to tell the computer. A wake that ends before that leaves the
part on the relay until the next one. The change after this one has the phone tell the
computer when it hands the part to iOS, and the computer fetches when the relay has it.

## Proof

Merged, not proven until Bo's phone drains on a named build: the four parts on the relay, then
November 16 and 17, on the computer with counts and minutes.
