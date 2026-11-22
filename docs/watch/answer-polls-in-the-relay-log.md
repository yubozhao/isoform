# The relay says how a phone's wait for answers ended

2026-11-20: the computer answered a phone's requests within a second (its own log), and the
phone acted on the answers 13 and 8 seconds later, in wakes of 20 to 50 seconds. Nothing said
whether the relay held the answers or the phone was slow to take them.

The relay now writes one line when a phone's wait for answers (`GET /v1/responses`) ends:

- `phone answer poll: N answers after S s`: the relay held the request S seconds and handed
  over N answers.
- `phone answer poll: the phone went away after S s`: the phone closed the request first.

A wait that ends with nothing leaves no line: an open app makes one every 25 seconds.

How to read it: put the computer's log time of an answer beside the poll line that follows. A
poll line within a second of the answer means the relay handed it over at once and the delay is
after the relay (the tunnel or the phone). No poll line until seconds later means no request of
the phone was waiting, so the phone was not asking.

Numbers only: nothing of an answer, a device or a credential.
