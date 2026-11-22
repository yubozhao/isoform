# The phone sends upload bodies as base64 text (2026-11-21)

The cause is in `docs/watch/pieces-as-text-on-the-relay.md`: the tunnel in front of the relay
refuses a request whose first 8 KB look like an attack to its firewall, sealed audio is random
bytes, and so about one piece in forty was refused, the same piece every time.

## What the phone does now

- The relay's health answer may say `"uploadsAsText": true` (PROTOCOL.md 24.1b). The phone
  keeps that per relay address, across starts (`RelayUploadBody`).
- For such a relay every upload body goes as `text/plain` with the bytes as base64: a piece the
  app sends itself, a piece handed to the system (the file it sends from holds the text), and a
  whole part either way. `offset`, `total` and `size` still count the bytes.
- For a relay that does not say so, bodies go as bytes, as before.
- A relay that answers `415` to text is taken as one that takes none (an older relay in its
  place): the next try goes as bytes.
- A relay that takes pieces in any order but not text yet is asked again every ten minutes, so
  a relay replaced while the app runs is noticed without a restart.

## A part that cannot pass is seen

A part the edge refuses says `part-edge-403` in the pass record. When the same part is refused
in three passes in a row or more it says `part-edge-403-in-<n>-passes`. Before, the same four
parts were refused in every pass for a day and no record said that it was always them.

## Limits

- Pieces already with the system as bytes stay bytes until they end; a refused one is handed
  over again by the next pass, and that hand-over is text.
- Nothing here deletes a part file or a pending record.
