# A 403 the relay did not write

## What was seen

2026-11-21, 08:30, Bo's phone: one pass ended "stopped by part-relay-403". The relay's own log
has no refusal for the phone in that minute. The relay answers every refusal as
`{"ok":false,"error":"..."}`; an answer with status 403 and any other body was written by
something between the phone and the relay, here the tunnel's edge.

The phone took it for the relay's word. For a piece iOS was sending, that stopped the part's
other pieces, and it counted against the part as a refusal.

## What changed (phone only)

For an upload (`PUT /v1/uploads/{id}`, `PUT` and `GET /v1/uploads/{id}/pieces`), a 403 whose
body is not the relay's shape is `RelayEdgeForbidden`:

- It is about the moment, like 408 and 5xx. A piece iOS was sending goes again by itself, up to
  twice, and the part's other pieces go on.
- It does not count as a refusal of the part, so it never leads to a fresh upload.
- The pass record says `part-edge-403` (a held part: `held-ask-edge-403`).

A 403 the relay wrote stays what it was: a refusal, `part-relay-403`.

For uploads iOS sends, the first 2048 bytes of an answer are kept in memory until the upload
ends, only to read this. Nothing of it is stored or logged.

## Limit

If iOS ends an upload while the app is closed and does not hand the body over on the next
start, a 403 is read as the edge's. The piece is then sent again twice instead of stopping the
part; the pass still gets the relay's real refusal from its own ask.
