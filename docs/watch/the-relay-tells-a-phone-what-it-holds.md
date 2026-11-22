# The relay tells a phone what it holds of its uploads (2026-11-22)

## Why

On Nov 22 Bo's phone, with about 255 parts waiting, was refused every new part: the relay's 48
places for it were taken (`limit=waiting_uploads count=48 cap=48`), mostly by uploads half sent
the evening before. The phone asked about 14 uploads that wake and the relay knew none of them:
it was starting other parts than the ones holding the places. It could not have known better.
It marks a part as uploading before the first byte leaves, so a part the relay refused looks the
same in its records as one the relay holds half of. Asking the relay part by part managed 14
answers in a two-minute wake.

## What changed (relay)

`GET /v1/uploads` with the device credential and the upload scope answers the phone's own
uploads on the relay: id, bytes held, total, whole or not. Whole ones the computer has not
fetched first, then the ones still arriving, each oldest first. Health says `"uploadList": true`.
PROTOCOL.md 24.1c has the contract.

Asking changes nothing. The ids are ones the phone made; no other phone's uploads are listed.

## What follows (phone)

The phone asks once a pass. Parts the relay holds something of go first, and a relay that is
full stops only the starting of new parts.
