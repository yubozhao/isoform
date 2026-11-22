# The relay takes an upload body as base64 text (2026-11-21)

## What went wrong

About one part in ten of Bo's recordings never reached the computer, and it was always the same
parts. The four parts from noon on Nov 20 were tried in every wake for a day.

The relay is reached through a zrok public share. The share's edge is an AWS load balancer
(`server: awselb/2.0`) with a content firewall. It reads the first 8 KB of every request body and
answers `403` itself when those bytes look like an attack. Sealed audio is random bytes, so a
piece now and then contains a few bytes the firewall dislikes (quotes, backticks and the like in
the wrong order), and the same piece is refused every time it is sent.

## How it was found

With random bytes only, no credentials and a made-up upload id (the relay answers 401):

- 80 bodies of 262144 random bytes through the public address: 78 reached the relay, 2 got the
  edge's HTML `403` after 65 to 131 KB were sent. 20 sent to `127.0.0.1:8791`: all reached the relay.
- A refused body sent five more times: refused five times. Sent straight to the relay: accepted.
- A 128-byte stretch of it is refused alone. The same stretch after 8192 bytes of padding passes.
- The same body as base64 text passes. Another content type or `POST` does not help.

The relay's "the piece did not arrive whole, 65536 of 262144 bytes" lines are the same event:
the edge passes on the first 64 KB or so and then drops the request.

## What changed here

`PUT /v1/uploads/{id}` and `PUT /v1/uploads/{id}/pieces` take `Content-Type: text/plain` with
the body as standard base64. The relay decodes it and stores the bytes; everything after that is
as before. `GET /v1/health` says `"uploadsAsText": true`. Bodies sent as bytes work as before, so
an old phone is not affected. PROTOCOL.md 24.1b has the contract.

Base64 text is letters, digits, `+`, `/` and `=`. Nothing in it can look like a query, a script
or a path, so this does not depend on which rules the firewall has. It costs a third more bytes
on the way to the relay.

## What is left

The phone has to send text (the next PR). Until then nothing changes for Bo's parts.
Smaller pieces would make this worse, not better: every body has its own first 8 KB.
