# An attachment may go to the relay as base64 text (2026-11-21)

Same cause as `docs/watch/pieces-as-text-on-the-relay.md`: a tunnel in front of the relay reads
the first 8 KB of a request body and refuses, with a 403 of its own, what looks like an attack.
Sealed bytes are random, so about one body in forty is refused, and the same body every time.
The computer's attachments for the phone (the brief's audio) are sealed bytes sent the same way
when the computer reaches its relay through such a tunnel.

- Relay: `PUT /v1/hub/attachments/{id}` takes `text/plain` with the body as standard base64 and
  stores the decoded bytes. Health says `"attachmentsAsText": true`. Bytes are taken as before.
- Computer: sends bytes. When the answer is a 403 that is not the relay's (no JSON with `ok`),
  it sends the same attachment once more as text. The relay's own 403 (no phone may read
  briefs) is an answer and nothing is sent again. No health request is needed: an older relay
  answers 415 to the text and the upload fails as it did before.
- The phone's download is not changed: answers are not read by such a firewall.

Bo's own computer reaches its relay on this computer, not through the tunnel, so this is for
computers whose relay is elsewhere.
