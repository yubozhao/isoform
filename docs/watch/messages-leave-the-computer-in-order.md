# Messages leave the computer in the order of their numbers

2026-11-20, branch `claude/mobile-send-in-order`. The other direction of
`requests-reach-the-computer-in-any-order.md` (#1343).

## The weakness

Answers and events for a phone share one counter. The relay hands a phone only
messages numbered above the one it asked after, an acknowledgement deletes
every lower one, and the phone itself keeps a highest number. A message that
leaves the computer after a later-numbered one is lost to the phone: a request
then waits for an answer that never comes, which looks the same as a request
that never arrived.

Four senders numbered a message under the bridge's lock and sent it after
releasing the lock: answers (`sendMobileResponse`), events
(`publishEventForDevices`), notices (`publishMobileNotice`) and the forget
request (`sendForgetAll`). An answer and an event started at the same instant
could leave in the other order. It needs two senders within microseconds, so
it is rare, and it is not known to have happened.

## The change

`mobileBridge.sendOrder`: held from numbering a message until it is on the
wire, in those four senders. It is taken before the bridge's lock and never
while holding it. `sendForgetAll` lets go of it before it takes the job file's
lock.

Briefs use the same counter but their own queue on the relay and their own
highest number on the phone, so their three senders are left as they are.

## Guard

`TestMobileMessagesLeaveInTheOrderOfTheirNumbers`: forty answers and forty
events sent at once reach the relay in the order of their numbers.

## Not changed

The relay and the phone still use a highest number for this direction. With
the computer sending in order over one connection that is enough; a message
that takes the direct path beside the relay is a separate question.
