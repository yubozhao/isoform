# Watch recordings reach the computer over a bad connection

Status: design for review, no code yet. 2026-11-18.

## What happened

A watch recording travels in parts of up to 15 minutes, about 2.8 MB each. The phone seals a
part, puts it on the relay in one request (`PUT /v1/uploads/{id}`, PROTOCOL.md section 24), and
tells the computer; the computer fetches it, checks it, stores it and removes the relay's copy.

On 2026-11-18 Bo's phone paired again at 12:16 and tried four uploads in six minutes. Each one
was cut 10 to 25 seconds in, and none was stored. The phone, the pairing and the computer were
all fine. The public address of the relay (a zrok share) stopped carrying a request that large,
and then stopped answering at all. Two days of recordings stayed on the phone.

One request per part means a cut at 95% costs the whole part, and the next try starts at zero.
On a connection that drops every 20 seconds a 2.8 MB part never arrives.

## Opinion first

Do two things, in this order:

1. **Pieces.** A part goes to the relay in small pieces, and a cut costs only the piece in
   flight. This fixes every path, including the one a released app uses.
2. **The path that is up.** The phone sends pieces over whichever path answers, the pinned
   relay first (since 2026-11-20; the direct path came first before), and the computer fetches
   from the relay that holds the part.

Neither changes what is recorded, sealed or kept. The rule stays: nothing is deleted on the
phone or the relay until the computer confirms it has the copy.

## Why the phone goes through zrok, and whether iroh already solves it

The direct path (iroh, `docs/design/relay-open-source-hosting.md`) is built and running: the
computer runs its own relay behind dumbpipe, the phone dials it by key and talks to it through
a listener on `127.0.0.1` inside the app (`RemoteAccess.swift`, `mobile_direct_link.go`).

Bo's phone still uses zrok because his computer is started pinned to that relay
(`ISOFORM_RELAY_URL`). A pinned computer pairs phones on the pinned relay and offers the direct
path beside it (PROTOCOL.md 21.4). Requests to the computer already switch paths when one
fails, which is why Today and briefs kept working. Recording uploads do not switch.

A released app has no pinned relay: everything goes over iroh. So "just use iroh" is half the
answer, and it shows three gaps:

- **The tunnel lives in the app.** When the app leaves the screen the phone closes the tunnel
  (`pauseForBackground`). The background uploads from #1301 are handed to iOS, and iOS cannot
  reach an address inside a suspended app. On the direct path an upload only moves while the
  app has time to run. In a released app that is the only path.
- **The computer fetches from one relay.** `POST /recordings` reads the part from the pinned
  relay's client (`bridge.client`) whichever path the phone used. A part the phone put on the
  computer's own relay over the direct path would be answered "upload missing". Read from the
  code, not yet seen on a device.
- **Short windows.** The phone app gets seconds, not minutes, when the watch hands it a part in
  the background. A 2.8 MB request does not fit; a few pieces do.

So pieces are needed even with iroh, and they are what makes short background windows useful.

## Design

### 1. Pieces through the relay

The part is sealed once, as today (same id, key and nonce on every try, so the same bytes).
The sealed bytes go up as pieces of 256 KiB (the last one shorter); a 2.8 MB part is 11 pieces.

New on the relay, beside the existing route:

| Request | Meaning |
| --- | --- |
| `PUT /v1/uploads/{id}/pieces?offset=N&total=T` | Appends the body at byte `N` of an upload of `T` bytes. `N` must equal what the relay holds; otherwise `409` with the held size, and the phone continues from there. The piece that reaches `T` completes the upload. |
| `GET /v1/uploads/{id}/pieces` | The held size and whether it is complete. `404` when unknown. (A `GET`, not `HEAD`: relay answers are JSON.) |

- An unfinished upload is not visible to the computer; `GET /v1/hub/uploads/{id}` answers 404
  until the last piece is in. The computer's side does not change, and it already resumes its
  own download (#1297).
- Quotas count the upload, not the piece: 48 waiting uploads and 192 MiB per phone as today,
  with `total` reserved at the first piece. The 60-an-hour limit counts the first piece only.
  An unfinished upload expires like a finished one (48 hours) and is removed with the phone.
- Whole-body `PUT /v1/uploads/{id}` stays for older phones. The phone uses pieces only with a
  relay that says it takes them (a field in `/v1/health`), so an old relay keeps working.
- The integrity check is unchanged: the computer opens the sealed bytes with the key the phone
  sent and compares the SHA-256. A relay that mixed pieces up produces bytes that do not open,
  and the phone sends the part again.

On the phone:

- With the app open, pieces go one after another on an ordinary session. Each takes a second or
  two on a normal connection; a cut is retried from the relay's held size.
- With the app closed and a relay iOS can reach (a public address), each piece is a background
  upload from its own small file, as #1301 does for whole parts.
- When the watch delivers a part in the background, the phone asks iOS for the short run time
  it gives and sends as many pieces as fit.

### 2. The path that is up

- The forwarder asks for a path per part: the pinned relay first, and the direct path only
  when the pinned relay cannot be reached. A part that fails on the direct path, in whatever
  way, is tried on the pinned relay in the same pass.
  **Changed 2026-11-20; direct came first before.** That day nothing from the phone arrived
  over the direct path (the computer's own relay logged no request and no piece from it), a
  part the pass sent itself spent 30 to 40 seconds there in every pass, and the pass ended
  on it. The pinned relay is also the only one the system can upload to while the app
  sleeps. Why the direct path carried nothing is a fault of its own, not looked at here. An upload stays on the relay where its first piece landed; if
  that relay stops answering for long, the part starts again on the other one under a new id.
- The phone tells the computer where the part is: `upload.relay` is `pinned` or `direct` in
  `POST /recordings`. The computer fetches from that relay's client; without the field it reads
  the pinned one as today and, on 404, tries the other. This also closes the gap above.
- The relay refusing the phone (401) on one path no longer stops recordings while the other
  path accepts it. The Watch tab still says to pair again (#1304).

### What does not change

Recording, part length, sealing, the computer's storage and transcription, the Watch tab's
states, and deletion rules. Audio never leaves the user's devices unsealed; the relay still
holds only ciphertext.

## One example

Bo records a 40-minute walk. The watch hands the phone three parts while the phone is in his
pocket. Each time, the phone wakes for a few seconds, opens the direct path and sends four or
five pieces; the rest wait. At his desk he opens the app: the remaining pieces go in about ten
seconds, on the direct path because it answers and the public relay is slow today. One piece is
cut when he walks out of Wi-Fi range; it is sent again over 5G from where the relay stopped
counting. The computer fetches each finished part from its own relay and the Watch tab moves
them to "On your computer".

## Rejected

- **Shorter parts on the watch** (2 minutes, about 375 KB). No protocol change, but it does
  nothing for the recordings already made, multiplies requests and transcription seams, and a
  cut still costs a whole part.
- **Only fixing the tunnel** (restart zrok, or drop it for iroh). Worth doing for Bo's setup,
  but any path drops sometimes, and the direct path cannot run while the app is closed.
- **A generic resumable-upload protocol (tus).** More surface than two routes; nothing else in
  the relay needs it.

## Open questions

1. Piece size: 256 KiB is a guess. Measure a 5 MB transfer over the direct path on 5G and on
   the public relay (the first design left this unmeasured) and pick from that.
2. How much background time the phone really gets when the watch delivers a part; if it is
   under about 5 seconds, the first piece may not finish and the app-open case carries it all.
3. Should Bo's own computer stop being pinned to zrok now that the direct path exists? That is
   a setup choice, not part of this design.

## Build order

Each step is a small PR and useful by itself.

1. Computer: fetch from the relay that holds the part (field plus the 404 fallback). Small.
2. Relay: pieces and the progress route, with tests for offset, quota, expiry and the hidden unfinished
   upload. PROTOCOL.md section 24.
3. Phone: pieces with the app open, on the path that is up. This alone would have delivered
   on 2026-11-18.
4. Phone: pieces as background uploads on a public relay, and in the background window when
   the watch delivers a part.
5. Proof on real devices: a named build, a deliberately bad connection, before and after counts.
