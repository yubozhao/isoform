# The app asks what the computer can do once

2026-11-20, branch `claude/watch-parts-first`. Follows
`opening-the-app-leaves-room-for-part-requests.md` (#1335, the relay's budget).

## What happened

In the 08:00 wake on Bo's phone the computer's request audit showed eight
`GET /status` in thirty seconds, and no part request. Each screen and store
asked the computer "can you do this?" with its own status request before its
real one. At the relay every request also costs a long-poll and an
acknowledgement.

## The change

- `HubChannel.abilities()` (`ios/Yansu/HubRequests.swift`): callers that ask at
  the same moment share one status request, and its answer is reused for 30
  seconds. A new pairing starts fresh. An error is never kept.
- The 26 checks of the form `status().<ability> == true` now read
  `abilities()`. So does the recording pass (`recordingAbilities()`), which
  means a pass that starts while the app opens goes to its part request
  without a status round trip of its own.
- Things that change while the app runs still read `status()` each time:
  the pause, the watch's listening and notes settings, keep awake, Settings.

What the computer can do changes only when the computer is updated, so 30
seconds of reuse shows nothing stale on screen.

## Guard

`testOpeningTheAppAsksTheComputerWhatItCanDoOnce` in
`ios/YansuTests/HubRequestsTests.swift`: six checks, three of them at once, one
request sent.

## Not done here

The channel has no order of its own: a part request still starts beside the
app's reads, not ahead of them. With the relay's budget at 600 and one status
request, a 30-second open has room for both. If a wake still ends without a
part request, the next step is to hold the app's reads until the pass has sent
its first request.

## Not proven

Merged is not proven. Proof is a wake on Bo's phone on a named TestFlight
build: at most a couple of `GET /status` in the computer's audit, a
`POST /recordings`, and parts from Nov 16 and 17 taken.
