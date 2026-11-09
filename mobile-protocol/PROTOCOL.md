# Yansu mobile protocol, version 1

Status: contract for release 1 of the iPhone companion. The relay (`relay/`), the desktop
(`wails-gui/mobile_*.go`) and the iOS app (`ios/`) implement this document. When code and
this document disagree, fix the code. Every byte-level rule below is normative.

## 1. Roles

| Role | Runs | Holds |
| --- | --- | --- |
| Hub (the Mac, `wails-gui`) | the user's Mac | all user data, the hub X25519 key, the hub credential, paired device records |
| Device (the iPhone) | the user's phone | the device X25519 key, the device bearer, the hub public key, a cache of briefs |
| Relay (`relay/`) | a loopback sidecar on the user's computer, or an optional self-hosted container | hub and device credential hashes, ciphertext envelopes, encrypted briefs, APNs tokens, presence |

The relay never holds a private key, a plaintext brief, or a plaintext request. Everything
between device and hub is end-to-end encrypted (section 4). The hub only ever dials out. The
sidecar binds its HTTP/WebSocket listener to loopback; Dumbpipe carries the phone's connection
to that listener. The optional push gateway is separate from the relay data path.

## 2. Identifiers and encodings

- `hubId`, `deviceId`, `pairingId`, request ids: UUID v4, lowercase, 36 characters.
- Binary values (keys, secrets, bearers, ciphertext) are base64url **without padding**
  (RFC 4648 section 5) unless stated otherwise. Hashes are lowercase hex.
- Times are RFC 3339 UTC with second precision, e.g. `2026-10-09T07:00:00Z`.
- JSON bodies are UTF-8. Unknown fields are rejected by the relay and the hub
  (`DisallowUnknownFields`); the phone ignores unknown fields when decoding briefs.
- `hubCredential`, `deviceBearer`, `pairingSecret`: 32 random bytes each. Credential and
  bearer are presented as `Authorization: Bearer <base64url>` (43 characters). The relay
  stores only `sha256(value)`; it never returns a credential.

## 3. Pairing

### 3.1 Invitation (shown as a QR code and as a copyable link on the Mac)

```
isoform://pair#<base64url(JSON)>
```

Readers also accept the legacy `yansu://pair#` prefix.

JSON payload (the shown fields are required for a direct relay; a ticket-based invitation omits `relay` and includes `iroh`):

```json
{"v":1,"relay":"https://relay.example","hub":"<hubId>","pairing":"<pairingId>",
 "secret":"<base64url 32 bytes>","hubPub":"<base64url 32 bytes>","exp":1760000000}
```

- `relay` is an https origin with no path, query or fragment. `http://127.0.0.1:<port>` and
  `http://localhost:<port>` are accepted only for development.
- `exp` is Unix seconds; invitations last 10 minutes. The hub stores only `sha256(secret)`.
- `iroh`, when present, is the computer's dumbpipe endpoint ticket. A remote-mode phone uses it
  to reach the computer's relay from another network; it is never sent to the relay. A remote
  invitation is not created until the ticket is ready.
- The ticket matches `endpoint[a-z2-7]{20,}`. The phone uses its iroh forwarder to reach the computer relay. Ticket-only invitations require an updated phone app; older versions need an update to read them.
- The fragment is never sent over HTTP (fragments are not transmitted) and never logged.
- Optional `warning` is a localized, one-line advisory for the pairing screen. With a
  loopback relay and a known physical phone, it reads, for example,
  `"A phone on another network cannot reach 127.0.0.1."`. It does not invalidate the
  invitation or refuse pairing. The same warning is available in the desktop connection
  status (section 12). Additive `warningCode:"relay_loopback"` and `warningHost` identify
  the condition and host without embedding a display language. Desktop Settings and its
  pairing-code panel translate these fields through react-i18next. The localized `warning`
  string remains for older wire consumers; the current iOS invitation screen does not
  display these advisory fields. Clients that do not know them ignore them.

### 3.2 Claim token

Both the phone and the hub derive, with HMAC-SHA256:

```
claimToken = base64url( HMAC-SHA256( key = secret, msg = "yansu-claim-v1|" + pairingId ) )
```

When the hub opens a pairing it tells the relay `claimTokenHash = hex(sha256(claimToken))`.
The relay accepts a claim only when `sha256(presentedToken)` equals the stored hash
(constant-time compare), the pairing is `open`, and it has not expired. The relay learns the
claim token, which is single-use and reveals nothing about `secret`.

### 3.3 Pairing key

```
S      = X25519( devicePrivate, hubPublic )            (32 bytes; same as X25519(hubPrivate, devicePublic))
kPair  = HKDF-SHA256( ikm = S, salt = secret (32 raw bytes),
                      info = "yansu-pair-v1|" + hubId + "|" + pairingId, length = 32 )
```

The claim and the approval are ChaCha20-Poly1305 (IETF, 12-byte nonce, 16-byte tag,
ciphertext||tag) under `kPair`:

| Message | Nonce (12 bytes) | AAD (ASCII) |
| --- | --- | --- |
| claim (device to hub) | `00..00 01` (eleven zero bytes then 0x01) | `"yansu-claim-v1|" + hubId + "|" + pairingId` |
| approval (hub to device) | `00..00 02` | `"yansu-approval-v1|" + hubId + "|" + pairingId` |

Claim plaintext:

```json
{"deviceId":"<uuid>","deviceName":"Alex's iPhone","devicePub":"<base64url>",
 "bearerHash":"<hex sha256(deviceBearer)>","platform":"ios","appVersion":"1.0 (12)",
 "scopes":["brief:read","brief:refresh","presence:read","push"],"pushEnvironment":"development"}
```

`deviceName` is 1 to 120 bytes. `devicePub` inside the ciphertext must equal the clear
`devicePub` on the claim request (the hub checks). `pushEnvironment` is `development` or
`production` and records which APNs environment the build uses.

Optional `simulator` is a JSON boolean describing the device's execution environment, not
its name. Simulator builds must send `"simulator":true` inside this encrypted claim while
keeping `platform` as `"ios"`; physical-device builds may omit it or send `false`.
The hub retains it in the paired device record and uses it only to suppress the loopback
reachability warning. It never changes authentication, approval or scopes. Older clients
omit the field; a device name such as `"iPhone 17 Pro"` alone cannot distinguish a
Simulator from a physical phone. For older records the hub also recognizes an explicit
`Simulator` word in the device name or claim platform. Otherwise, an unspecified
environment can still receive the advisory; pairing remains available.
An existing Simulator pairing made without this field must be paired again once to update
the saved device metadata; the hub cannot infer that fact from its ordinary device name.

Approval plaintext:

```json
{"deviceId":"<uuid>","hubName":"Alex's MacBook Pro","scopes":["brief:read","brief:refresh","presence:read","push"],
 "expiresAt":"2027-01-07T07:00:00Z","hubPub":"<base64url>"}
```

### 3.4 Confirmation code

Both sides show the same 6 digits so the user can match them:

```
code = HKDF-SHA256( ikm = kPair, salt = empty, info = "yansu-confirm-v1", length = 4 )
digits = big-endian uint32(code) mod 1000000, zero-padded to 6 characters
```

### 3.5 Flow

1. Hub: creates `pairingId`, `secret`, `exp`; sends `pairing_open` (section 6) with
   `claimTokenHash`; shows the invitation. Generating a new invitation closes the previous one.
2. Device: scans or pastes the invitation, validates `exp`, generates `deviceId`, device key,
   bearer; derives `kPair` and `claimToken`; `POST /v1/pairings/{pairingId}/claim`.
3. Relay: validates the token, marks the pairing `claimed`, forwards `pairing_claim` to the hub
   (queued until the hub connects, at most until `exp`).
4. Hub: derives `kPair` from `devicePub`, decrypts the claim, shows the device name and the
   confirmation code, waits for the user. The device polls `GET /v1/pairings/{pairingId}` and
   shows the same code.
5. Approve: hub stores the device record (section 7), sends `pairing_approve` with the
   encrypted approval, `bearerHash`, `scopes`, `expiresAt`. Relay creates the device, stores the
   approval, answers the poll with `state: approved`. Device decrypts, checks `deviceId`, stores
   `hubId`, `hubPub`, `relay`, bearer, scopes, `expiresAt`.
   Decline: hub sends `pairing_decline`; the relay answers `declined`.
6. A pairing is consumed by approval, decline, expiry, or three failed decryptions on the hub.

## 4. Session keys and envelopes

```
S     = X25519( devicePrivate, hubPublic )
kD2H  = HKDF-SHA256( ikm = S, salt = "yansu-mobile-v1|" + hubId + "|" + deviceId (ASCII), info = "d2h", length = 32 )
kH2D  = HKDF-SHA256( ikm = S, salt = same,                                             info = "h2d", length = 32 )
```

Envelope (JSON; this is what the relay stores and forwards):

```json
{"v":1,"hub":"<hubId>","dev":"<deviceId>","dir":"d2h","seq":17,"kind":"req","ct":"<base64url>"}
```

- `dir` is `d2h` (device to hub, key `kD2H`) or `h2d` (hub to device, key `kH2D`).
- `kind` is `req`, `res`, `brief`, or `event`.
- `seq` is a uint64 that strictly increases per (device, direction). Senders persist their
  counter and, after a restart, skip ahead by 1000 to cover an unflushed write. Receivers
  persist `lastAccepted` per (device, direction, stream) and reject `seq <= lastAccepted`, where
  stream is `brief` for kind `brief` and `message` for kinds `req`, `res` and `event`. The two
  streams share one sender counter (so nonces stay unique) but are checked separately, because
  a stored brief may be fetched after a later response was already accepted.
- Nonce: 12 bytes = four zero bytes followed by big-endian uint64 `seq`.
- AAD (ASCII): `"yansu-env-v1|" + v + "|" + hub + "|" + dev + "|" + dir + "|" + seq + "|" + kind`
  where `v` and `seq` are decimal. Any change to the header fails authentication, so the relay
  cannot relabel or reroute an envelope.
- `ct` is ChaCha20-Poly1305 ciphertext||tag of the plaintext below, base64url.
- Plaintext sizes: `req` and `res` at most 64 KiB before encryption; `brief` at most 256 KiB.
  Brief plaintext is the UTF-8 JSON followed by `0x00` padding to a multiple of 4096 bytes;
  the receiver trims trailing `0x00` bytes before decoding.

Plaintexts:

```json
// kind=req (d2h)
{"id":"<uuid>","method":"GET","path":"/brief/latest","query":"","body":""}
// kind=res (h2d)
{"id":"<uuid>","status":200,"body":"{\"ok\":true,\"data\":{...}}"}
// kind=brief (h2d): the MobileBriefV1 document (section 8), padded
// kind=event (h2d)
{"event":"brief_ready","briefId":"2026-10-09-morning","at":"2026-10-09T07:00:12Z"}
```

`path` is relative to the hub's `/mobile/v1` prefix. `body` is the raw JSON request body as a
string (empty for GET). `res.body` is the hub's standard `{ok,data,error}` envelope as a string.

## 5. Relay HTTP API (device side)

All responses are `{"ok":true,"data":...}` or `{"ok":false,"error":"..."}` with
`Cache-Control: no-store`. Any request carrying an `Origin` header is refused with 403. POST
and PUT require `Content-Type: application/json`, one JSON value, at most 64 KiB. Protected
routes require `Authorization: Bearer <deviceBearer>`; unknown, expired or revoked bearers get
401. A route that needs a scope the device lacks gets 403.

| Method and path | Auth | Scope | Body / result |
| --- | --- | --- | --- |
| `GET /v1/health` | none | | `{"protocolVersion":1,"service":"isoform-relay","status":"ok"}` |
| `POST /v1/pairings/{pairingId}/claim` | none, rate limited | | `{"hub","claimToken","deviceId","devicePub","ct"}` → 202 `{"state":"claimed"}` |
| `GET /v1/pairings/{pairingId}` | header `X-Yansu-Claim: <claimToken>` | | long-poll up to 25 s; `{"state":"claimed"\|"approved"\|"declined"\|"expired","approval":{"ct":"..."}}` (`approval` only when approved) |
| `GET /v1/presence` | bearer | `presence:read` | `{"hubOnline":true,"lastSeenAt":"..."}` |
| `GET /v1/brief/latest` | bearer | `brief:read` | the latest stored brief: `{"version":<seq>,"asOf":"...","env":{...envelope...}}`; 404 when none |
| `GET /v1/briefs?limit=7` | bearer | `brief:read` | `{"briefs":[{"version","asOf","env"}]}` newest first, at most 7 |
| `POST /v1/requests` | bearer | per request (see hub routes) | body is a `d2h` envelope of kind `req` → 202 `{"accepted":true}`; queued if the hub is offline (24 hour TTL, section 23) |
| `GET /v1/responses?after=<seq>&wait=25` | bearer | `brief:read` | long-poll; `{"envelopes":[...h2d envelopes with seq > after...]}` (kinds `res` and `event`) |
| `POST /v1/responses/ack` | bearer | `brief:read` | `{"upTo":<seq>}` → deletes delivered h2d envelopes (not briefs) |
| `PUT /v1/push/token` | bearer | `push` | `{"deviceToken":"<hex>","environment":"development"\|"production"}` → `{"enabled":true,"registeredAt":"..."}`; 503 when the relay has no APNs configuration for that environment |
| `DELETE /v1/push/token` | bearer | `push` | `{"enabled":false}` |
| `POST /v1/notices/{noticeId}/receipts` | bearer | `push` | `{"state":"delivered"\|"opened"\|"acted"\|"dismissed","action":"","at":"..."}` → 202 `{"accepted":true}`; see 14.6 |
| `DELETE /v1/devices/me` | bearer | | unpairs: deletes the device, its envelopes, briefs and push token; tells the hub `device_unpaired` |

Rate limits (fixed windows): 10 claim attempts per 10 minutes per pairing id and per client
IP; 60 requests per minute per device; one `POST /v1/requests` with `path=/brief/refresh`
per 10 minutes per device (429 with `Retry-After`).

Retention: at most 200 queued envelopes per (hub, device, direction) for request and response
envelopes, and at most 50 `event` envelopes per device to the phone, trimmed separately so an
event burst never evicts a response; the oldest of each kind go first. Request envelopes
expire after 24 hours (section 23); the latest 7 briefs per device are kept; approvals are deleted after
delivery; a pairing row is deleted at expiry.

Status codes (decided in the relay implementation):

- Claim: 202 accepted (an identical repeat claim also returns 202); 403 bad claim token;
  404 unknown pairing or hub mismatch; 409 pairing not open; 410 expired; 429 with
  `Retry-After`.
- Pairing poll: 401 missing or malformed `X-Yansu-Claim`; 403 wrong token; 409 when the
  pairing is still `open` (not yet claimed); 404 after the janitor removed an expired row;
  `{"state":"expired"}` for a claimed pairing past expiry. The approval is served exactly
  once and then deleted; if the response is lost the phone must pair again, so the phone
  persists the approval before anything else.
- Requests: 400 invalid envelope; 403 foreign device or hub; 409 conflicting `seq`; 429
  queue full with `Retry-After: 60`.
- Responses: `wait` defaults to 0 and is capped at 25; invalid values are 400; at most 50
  envelopes per reply. Ack replies `{"upTo":N}`; unpair replies `{"unpaired":true}`.
- Briefs: `limit` must be 1..7, else 400.
- Push: 400 bad token (64 to 200 hex characters, stored lowercase) or environment; 409 the
  per-hub cap of 10 enabled tokens; 503 APNs not configured or configured for another
  environment. A token belongs to one enabled device at a time: registering a token held by
  another device moves it and ends the previous registration and its pending sends;
  re-registering the same token keeps pending sends; a changed token or environment cancels
  this device's pending sends.
- Bodies: 413 over 64 KiB; 415 wrong content type; 400 unknown fields, trailing values or
  malformed JSON. Scope names are validated by format (lowercase, optional colon part, at
  most 16 per device), not against a fixed list.
- `X-Forwarded-For` is honoured only when the TCP peer is loopback or private, and only
  for claim rate limiting. Rate-limit state is in memory and resets on restart.

## 6. Relay WebSocket (hub side)

`GET /v1/hub/ws` with headers `Authorization: Bearer <hubCredential>` and
`X-Yansu-Hub: <hubId>`. A hub id seen for the first time is created with that credential
hash; afterwards the credential must match. One client address (an IPv4 address or an IPv6
/64) may create at most 20 new hub ids an hour; beyond that a new id is answered `429` with
`Retry-After`, while known hubs connect as before. Text frames carry JSON objects with a `t` field.
Frames are at most 256 KiB; brief frames may be up to 512 KiB.

Hub to relay:

| `t` | Fields | Meaning |
| --- | --- | --- |
| `register` | `proto:1`, `hubName` | first frame after connect; answered by `registered` |
| `heartbeat` | | every 30 s; updates presence |
| `pairing_open` | `pairing`, `claimTokenHash`, `expiresAt` | opens one pairing; closes any other open pairing of this hub |
| `pairing_close` | `pairing` | cancels an open pairing |
| `pairing_approve` | `pairing`, `deviceId`, `bearerHash`, `scopes`, `expiresAt`, `ct` | creates the device and delivers the approval |
| `pairing_decline` | `pairing` | declines the claim |
| `out` | `env`; for a moment push also `notify`, `push:"moment"`, `noticeId`, `kind`, `ct`, `category`, `collapse`, `level` | an `h2d` envelope of kind `res` or `event`; stored and delivered to the device poll; the notice fields queue one sealed push per enabled token (14.3, 14.4) |
| `brief` | `env`, `asOf`, `notify`, optional `deliverAt` | an `h2d` envelope of kind `brief`; stored as a brief; `notify:true` queues one push per enabled token; a `deliverAt` (RFC 3339 UTC, at most 24 hours after relay time) holds the brief from every read and makes its push due then, for a brief made ahead of its slot |
| `revoke` | `deviceId` | deletes the device and everything stored for it |
| `ack` | `dev`, `upTo` | the hub consumed `d2h` envelopes of device `dev` up to `upTo` (relay deletes them); a frame without `dev` is refused |
| `sms_ack` | `id` | the hub took a text reply (18.5); the relay deletes it |

Relay to hub:

| `t` | Fields | Meaning |
| --- | --- | --- |
| `registered` | `serverTime`, `devices:[{deviceId,lastSeenAt}]` | registration accepted |
| `replaced` | | a newer connection for this hub took over; the hub must go dormant for 30 s and reconnect |
| `pairing_claim` | `pairing`, `deviceId`, `devicePub`, `ct` | a device claimed the pairing |
| `in` | `env` | a `d2h` envelope from a device (also replayed on reconnect for queued envelopes) |
| `device_unpaired` | `deviceId` | the device removed itself |
| `receipt` | `dev`, `noticeId`, `state`, `action`, `at` | a device's receipt for a notice (14.7); sent once per stored receipt and replayed on connect |
| `sms_reply` | `id`, `body`, `at` | a text the user sent to the Isoform number (18.5); replayed on connect until acked |
| `sms_state` | `state` | the hub's text subscription changed without the hub asking (18.5) |
| `error` | `ref`, `error` | a hub frame was rejected (`ref` is the offending `t`) |

The hub sets a 90-second read deadline refreshed by any frame or ping, reconnects with a
backoff that starts at 2 s, doubles to a 5-minute cap, and resets after 30 s of stable
connection. On reconnect the relay replays queued `d2h` envelopes and open pairing claims.

Relay behaviours the hub must expect (decided in the relay implementation):

- `registered.devices` is the reconciliation source: a device missing from that list was
  unpaired or expired while the hub was offline, and the hub drops its local record.
  `device_unpaired` is only sent to a connected hub. `lastSeenAt` is `null` when never seen.
- The relay replays every unacked `d2h` envelope on each connect and whenever a new request
  arrives, so the hub deduplicates by `seq` (the replay check already does this) and acks
  per device.
- A rejected hub frame gets `{"t":"error","ref":"<t>","error":"..."}` and the socket stays
  open. `pairing_close` on a pairing that is not open is a silent no-op; `pairing_decline`
  on a pairing that is not claimed is an error.
- `pairing_open.expiresAt` may be at most 15 minutes ahead of relay time (10 minutes plus
  clock skew); `pairing_approve.expiresAt` at most 366 days ahead.
- A `brief` whose version (`env.seq`) is already stored is ignored: no replacement, no second
  push.
- A `brief` with `deliverAt` later than relay time is stored but not returned by `GET /brief/latest` or
  `GET /briefs` until that time, and its push is first due then. A `deliverAt` in the past or absent means
  at once. A frame whose `deliverAt` is unparseable or more than 24 hours ahead is refused. A duplicate `out` with identical bytes is accepted silently; different bytes under
  the same `seq` are an error.
- `seq` values are bounded to 2^53-1 so they survive JSON number handling.
- The hub credential is trust-on-first-use: the first connection presenting a hub id fixes
  its digest. Digests are SHA-256 of the exact 43-character base64url text (hub credential,
  device bearer, claim token).
- The relay enforces 60 requests per device per minute; the 10-minute `/brief/refresh`
  cooldown is enforced by the hub (the path is inside the ciphertext).
- The janitor deletes expired pairings, request envelopes older than 24 hours, briefs beyond
  the latest 7, exhausted pushes older than 24 hours, and devices whose bearer expired
  (cascading to their envelopes, briefs and push rows).

## 7. Hub (Mac) records and routes

Device record (`~/.yansu-agent/mobile/devices.json`, version 2, mode 0600):

```json
{"version":2,"hubId":"<uuid>","devices":[{"deviceId":"<uuid>","deviceName":"...","devicePub":"<base64url>",
 "scopes":["brief:read","brief:refresh","presence:read","push"],"createdAt":"...","expiresAt":"...",
 "lastSeenAt":"...","sendSeq":42,"lastAcceptedSeq":17}]}
```

Version 1 files (bearer-hash devices from the LAN build) are discarded on load; those phones
must pair again. Hub identity (`hub-identity.json`, 0600): `{"version":1,"hubId","hubCredential","privateKey":"<base64url 32 bytes>","publicKey":"<base64url 32 bytes>","createdAt"}`.
Device records and their public views may additionally carry `"simulator":true` from the
claim (section 3.3). The field is omitted for older or non-Simulator records.
Relay configuration (`relay.json`): `{"version":1,"enabled":true,"url":"https://relay.example"}`.
Brief schedule (`brief-schedule.json`): `{"version":1,"enabled":true,"morning":"07:00","evening":"18:00"}`.

Routes served over envelopes (`req.path` is relative to `/mobile/v1`):

| Method and path | Scope | Result |
| --- | --- | --- |
| `GET /status` | any | `{"hubName","appVersion","protocolVersion":1,"narrativeAvailable":true,"schedule":{...},"lastBriefAt","keepAwake","instantPush","briefSchedule"}` |
| `GET /settings/keep-awake` | `push` | `{"on","pairedPhones","onPower","hasBattery"}` (section 16) |
| `POST /settings/keep-awake` | `push` | body `{"on": bool}`; answers the same shape |
| `GET /brief/latest` | `brief:read` | the latest MobileBriefV1 (plaintext inside the encrypted response) or 404 |
| `GET /briefs?limit=14` | `brief:read` | `[{"briefId","slot","localDate","asOf","narrativeStatus"}]` newest first |
| `GET /briefs/{briefId}` | `brief:read` | one MobileBriefV1 |
| `GET /briefs/{briefId}/audio?version=<key>` | `brief:read` | the current generation's sealed audio attachment coordinates; section 17.4 |
| `GET /your-agent` | `brief:read` | Agent of You (coming soon): `{"specialties":[{"name","seen"}],"taste":[...],"stillLearning","computed","nameCandidates":[...],"hub","token","reservation":{"name","email","reservedAt"}\|null}`. Computed on the Mac from How you do things; the `token` is the agent token (base64url, 32 bytes) the phone uses for its calls to `https://www.isoform.ai/api/agents`. The phone must not show `seen`. `computed` is `false` until the Mac has computed the insights once; the phone then shows wordless placeholder rows and never Still learning. Read it only inside the sealed channel. |
| `POST /brief/refresh` | `brief:refresh` | `{"status":"queued"\|"making","readyAt"}`; a generated brief is delivered as a `brief` frame |
| `POST /briefs/{briefId}/week-intent` | `brief:read`, `chat:write` | body `{"idempotencyKey","answer"}`; saves the explicit one-line answer to the current week's offered question, returns `{"saved":true}`; 409 when the question is no longer available |
| `GET /devices` | any | the paired devices (no keys) |
| `DELETE /devices/{deviceId}` | any | a device may only revoke itself over this route |

Every served request is appended to `mobile_audit.jsonl` (0600) as
`{"ts","deviceId","method","path","status"}`. No bodies are logged.

## 8. MobileBriefV1

Optional `weeklyRecap` is an array of `{text, refs}` paragraphs, separate from
the daily `narrative`. On Sunday from 18:00 local time, it gives a one to two
minute reading of the previous seven days: receipt counts and currently readable
Purpose Map counts. The same recap appears in the main chat as `weekly_recap`;
`first_week` carries the paced onboarding plan. Both use the existing engagement
switches and generic doorbells. Older phones ignore `weeklyRecap` and retain the
full plain-text main-chat fallback. First-week chat cards offer `open` and
`mute_kind` ("Not for me, mute"); the existing authenticated mute route applies.

The weekly `yourWeek.ask.hold` object is optional and additive in protocol v1.
It describes a locally checked, read-only calendar suggestion; it does not
create or send a calendar event.

```json
{
  "version": 1,
  "briefId": "2026-10-09-morning",
  "slot": "morning",
  "localDate": "2026-10-09",
  "timezone": "America/Los_Angeles",
  "windowStart": "2026-10-08T18:00:00Z",
  "asOf": "2026-10-09T07:00:12Z",
  "hubName": "Alex's MacBook Pro",
  "coldStart": false,
  "narrativeStatus": "ok",
  "narrative": [{"text": "...", "refs": ["e1", "e2"]}],
  "needsYouCount": 0,
  "sections": {
    "moved": [], "doing": [], "watching": [], "meetings": [], "people": [],
    "openLoops": [], "learned": [], "deliverables": [], "schedules": []
  },
  "dayShape": {"activeMinutes": 312, "hours": [{"hour": 9, "activeMinutes": 42}], "topApps": [{"name": "Figma", "activeMinutes": 90}]},
  "counts": {"openLoopsByKind": {"commitment": 3}, "selfUpdatesPending": 0, "learningItems": 2, "needsYou": 0},
  "evidence": [{"ref": "e1", "sourceKind": "history", "sourceId": "...", "label": "...", "observedAt": "..."}],
  "limits": {"itemsPerSection": 8, "evidenceRefs": 60, "truncatedSections": []}
}
```

- `slot` is `morning`, `evening` or `on_request`. `briefId` is `<localDate>-<slot>` for scheduled
  briefs and `<localDate>-on_request-<HHMMSS>` otherwise. Optional `displaySlot` is `morning` or
  `evening`: the configured slot clock supplies the display name for an on-request brief while
  its identity remains unchanged. Clients reuse the localized Morning/Evening brief labels.
  History summaries expose the resolved display slot; `briefId` remains their identity.
- A generated reading below 100 words (using the shared Chinese reading-unit counter) completes
  a quiet slot locally. It sends no brief, notification, audio, question or chips to the phone.
  Quiet slots stay out of `/briefs` history; `/briefs/latest` retains the last posted brief.
  Today shows the local quiet state with the readable screen count and the actual next slot.
- Assembly and display omit lines with unresolved template tokens or missing question subjects.
  Dropped paragraphs also lose their question cards and audio bound to the removed text.
- `narrativeStatus` is `ok`, `unavailable` (no native runner, model failure, or validation
  failure) or `skipped` (cold start or nothing to say). `narrative` has at most 5 paragraphs,
  each at most 600 bytes, each with at least one `ref` that exists in `evidence`.
  `unavailable` may carry a deterministic template in the same personal voice; it does not
  mean the brief is empty. The narrative leads forward: decisions, work to review and plan next, counterparties
  needing attention, due commitments, and the day ahead. Optional Highlights follow
  in at most 3 one-line items (160 bytes each), then a required final paragraph
  with one or two concrete main-chat offers; no detailed retrospective. Speech
  reads the same paragraphs in the same order, including the no-model template.
- Narrative paragraphs may carry `topicId` and `topicName` (both `omitempty`).
  The computer derives these from the paragraph refs and the published topic
  registry, validates the id with `validChatTopicID`, and rechecks the complete
  original-source closure before saving and delivery. The model never supplies
  an id. Unmatched, private, sensitive or Closed Eyes topics have neither field.
  `topicName` is the currently readable registry name for the topic button.
- Optional `offers` contains the final paragraph's one or two main-chat drafts:
  `{"id","action","label","prompt","text","refs":["e1"]}`. The label is the chip
  text, the prompt is editable chat text, and the refs identify the existing brief
  evidence. Optional `conversationKey` is the opaque revision of the delivered
  narrative, offers and evidence; it is distinct from the schema's `version: 1`.
  Phones send it as `briefVersion` to the brief-message route (15.3), never compute
  it themselves. Missing keys disable that action on older briefs. These offers
  are draft requests for main chat, not permission to execute an action.
- Optional `weekQuestion` is `{"weekStart":"YYYY-MM-DD","recommendation":"..."?}`. It is projected on Monday's morning brief while the current week's question has no answer. A recommendation is an editable suggestion from a currently readable item, never an answer until the person confirms it. The phone localizes the question and saves only an explicit answer through the route above.
- Optional `cards` (backlog item 56, additive) are the brief's prepared clicks:
  `{"id","kind","mode","label"?,"message","refs":["e1"],"itemKey"?,"paragraph"?,"answerTo"?}`. `kind` is
  `action` (a closing answer button with a short `label`, at most two;
  answers have opaque question-bound IDs, while legacy ordinary actions used
  matching `offers` IDs), `item` (the question a row's chat action
  sends; `itemKey` is the row's `key`, or its `id` without one) or `opener` (what
  the computer says first for "Talk about this brief"), or `question` (one
  subject-specific next-step chip for an eligible body paragraph; some
  paragraphs have none). A question card carries the optional `paragraph`
  field: the zero-based index into the delivered `narrative`, with zero
  explicitly encoded; it always has `mode: "send"`, no action target, and its
  `message` is the exact request the user would send. It is never an action
  button. Its opaque id binds the original paragraph text and source
  identities as well as its position, so a withdrawn paragraph cannot retarget
  an old card. Only typed subjects in eligible sections can have a chip; the
  paragraph text is never used as a fallback subject. There is at most one
  chip per paragraph and three per brief, ranked by the brief's attention
  order. An attention row with an existing item card reuses that card: the
  linked card carries `paragraph` and `label` while retaining its item identity
  and message, rather than adding a duplicate question. `paragraph` is omitted
  on unlinked card kinds. Chips share the existing card draft batch budget,
  with room reserved for existing cards and the whole-brief draft. The model
  writes chip words in the brief's own pass; missing or rejected words use
  `writer: "pattern"`, accepted words use `writer: "model"`. No additional
  model call is made.
  The last paragraph, when it asks a closing question, keeps at most two
  prepared answer requests as `action`/`send` cards, matching Today. The model
  writes these in the same pass as that question, from its facts. Each answer
  carries `answerTo` (string, optional, `omitempty`): an opaque digest of the
  exact closing question and its original source facts. It is omitted on other
  cards. Delivery and resolution discard an answer whose digest does not match
  the current question, including after the question or its facts change.
  Ordinary action cards without a digest never substitute for answers; when
  none match, no answers are shown and "Talk about this brief" remains. Both
  questions and answers fill the editable box and use the existing draft
  warming read; only Send sends. Neither preparation nor choosing an answer
  performs calendar or other writes. On Send, `brief_card` resolves through
  `resolveTodayChatBriefCard` again; paragraph questions retain their currently
  validated topic in the same main-chat admission and provenance path used by
  `SendMainChatWithTopic`. `TodayChatDraft.topicId` is optional for these drafts.
  These additions keep protocol version 1; older phones ignore the new fields
  and unknown card kinds. `mode` is `send` (the
  message is the user's and the answer starts at once) or `speak_first` (the
  computer says the message itself). The computer writes every card with the
  brief, keeps only cards whose sources it can read now (and none while
  observation is paused), rewrites a card whose facts changed, and checks it
  again at the click; words its model wrote go out only while every input that
  model read is unchanged, and the computer's own sentence otherwise. Clients show stored `label` and `message` as given and do not
  author or rewrite cards; the prepared request in the composer remains editable. Only anchored items (a meeting, a due date, a person
  waiting, a bill or order, a document, the computer's own work, or a goal or
  commitment) get cards; work only seen in activity, such as what was typed
  into an AI tool, gets none.
- `sections.noticed` is an optional additive section (version 1, omitted when empty):
  at most two checked "Isoform noticed" suggestions from today and yesterday, with
  `kind: "noticed"`, `status: "follow_up" | "routine"`, `title` from the suggestion's
  text, `detail` from its reason, and `at` from its moment's start. Evidence uses
  `sourceKind: "activity_noticed"` and `sourceId: "<YYYY-MM-DD>/<suggestionId>"`.
  Its anchor is `noticed`: Activity already checked the moment's anchor and privacy.
  Suggestions are the lowest brief-pick tier, after goals, and fill only unused
  places under the six-pick cap. They never enter "What needs your attention" or
  a separate Today list. They are observations, not commitments or permission.
  Every read (latest, by ID, MCP, phone push and queued re-delivery) rechecks
  `GetDayNoticed(date)`; the ref is readable only while that call still returns
  the suggestion, and any error fails closed. Not now, a deleted or unreadable
  moment, an accepted follow-up, summary withdrawal or a replaced generation can
  remove the suggestion, its card and derived text/audio on the next read.
- A picked suggestion can have an offer with `action: "open_activity"` and an
  action card with `mode: "open_activity"`. Both carry
  `target: {"date":"YYYY-MM-DD","suggestionId":"...","action":"follow_up"|"routine"}`.
  `message`/`prompt` are empty; this is navigation to an editable Activity field or
  routine draft, with fixed labels "Add follow-up" / "Make a routine" (zh-CN
  "添加跟进" / "设为例行事务"). The model never rewrites these cards, and the chat
  send route refuses them. Activity loads the day's suggestions again at click
  time; if the suggestion is gone, it still opens that day. Saving remains a
  separate user action on Activity. The phone cannot open the computer's Activity,
  so every phone copy removes these offers and cards and retains the brief text.
  When no chat opener remains, that phone copy also omits `conversationKey`, so
  older phones cannot offer a chat action that the computer cannot open.
  No phone owner check or execution capability is added. Older phones ignore the
  new optional section/target; older briefs without them still decode. Unknown
  card modes must never be treated as `send`. A computer downgrade to code that
  predates the `activity_noticed` read gate must first withdraw affected saved
  briefs/audio and queued frames, or retain a fail-closed gate for that source.
- `sections.review`, `sections.decide`, and `sections.plan` are optional additive groups,
  at most 3 items each. They reuse the permitted evidence behind existing sections, have
  their own stable section keys, and add no inferred promises or obligations. Review puts
  ready work and skipped/unknown-on-this-computer observations first, as next actions; Decide prioritizes blocked
  work and open commitments; Plan puts the next calendar events before open work.
  The retrospective has moved from Review to additive `sections.highlights`, at most
  3 single-line titles (160 bytes each), with empty `detail` and no preview or next
  step. Importance precedes recency: recorded outcomes, recorded decisions, confirmed
  meeting signals, then observed progress. Activity alone never proves completion;
  contact sightings alone never create an obligation or a highlight. Browsing and
  file-history rows stay out. Machine provenance timestamps remain intact.
  Additive `sections.dayAhead` carries at most 3 upcoming calendar items, earliest
  first, also retained in `plan` for existing readers. The narrative shows them
  once, after due commitments and before Highlights. Missing new fields mean empty.
  Recommended display order is `decide`, `review`, `plan`, `waitingOnYou`,
  `commitments`, `workingToward`, `dayAhead`, then `highlights`; legacy lists remain
  for compatibility. Review now contains forward review actions only.
  Older phones ignore these groups and can still read the narrative and legacy sections.
  Past display text has no clock times. Machine timestamps remain for provenance; only
  upcoming calendar items supply clock times to the narrative, when useful for preparation.
- Web evidence may include `webSource: {"browsers": ["Chrome"], "startMs": ..., "endMs": ...}`.
  This records the contributing browser identities and conservative capture interval,
  without URLs or page titles. The hub rechecks current privacy policy before delivery;
  legacy web refs without this provenance are withheld. Today refreshes preserve it,
  including after raw history retention. Clients may ignore this optional field.
- Section item: `{"id","title","detail","status","at","kind","refs":["e1"]}`; `title` and
  `detail` are at most 320 bytes; at most 8 items per section; `refs` entries must exist.
  An item may carry `url` (optional, added with Today's brief): an `http` or `https` link to
  open it, the join link of a meeting that has not ended or the page of a find. Any other
  scheme is never sent. A phone that does not know the field ignores it.
  A calendar meeting may also carry `endAt` (optional RFC 3339 time). Together with
  `at` (the start), it lets readers determine whether a stored meeting is ongoing or
  finished at read time, rather than rely on its generation-time `status`. Older
  briefs omit it; Today's next-meeting list then expires the item at its start.
  `dueAt` is an optional RFC 3339 deadline, separate from a Purpose row's
  `at` (recorded touch). A dated Purpose or commitment carries the same deadline in its
  Purpose Map section and legacy `openLoops` copy. Undated, invalid, and
  non-date due values omit it; no deadline is inferred from `at` or `status`.
  This is additive in version 1: older phones ignore the unknown field, and
  readers accept older documents without it. Readers classify "due today"
  and "overdue" at read time by calendar day in the brief's `timezone`
  (falling back to the reader's timezone), without clock times for past deadlines. Missing-evidence fallback rows carry no deadline.
- Purpose Map sections are additive in version 1 and are omitted when empty (and
  may be absent in older briefs); clients treat absence as an empty array:
  `workingToward` has at most 5 active purposes, `commitments` has at most 8 user-owned
  commitments due today or slipping, and `waitingOnYou` has at most 3 counterparties
  with no recorded touch for at least 21 days. Their `detail` is a bounded progress
  or evidence summary. Purpose ordering combines due urgency and observed attention;
  no observation is a completion claim or permission to act.
- Purpose items may also carry `lastTouch` (RFC 3339, recorded activity rather than a
  map revision), `nextStep` (at most 320 bytes, only from an actionable map relation),
  and `dueAt` (RFC 3339). Missing values remain absent, not invented. Evidence refs
  identify the purpose and the permitted observations behind the statement. The
  first ref is the item's own purpose identity, not activity evidence; clients
  use only that identity to deduplicate legacy open loops. All `purpose` refs are
  omitted from item and narrative source chips/lists: later purpose refs identify
  next steps already shown as `nextStep`, not additional observed sources or a
  separately shown purpose. Labels use the canonical purpose title, even when a
  waiting card names a counterparty. Source kind distinguishes activity evidence,
  map links, reminders and main-chat references; a link is not an observed touch.
- Retention receipts keep only the source kind, opaque ID and the privacy exclusion
  decision made at deletion. They retain no app names, timestamps, session lists or
  activity content. A permitted purpose may remain after a recorded purge, with
  detail explaining that its linked activity is no longer retained; deleted sources
  are not evidence and cannot supply `lastTouch`. Unknown retained touch also cannot
  establish that a counterparty has waited 21 days. A newly excluded app or a later
  pause-history correction cannot be rechecked against a deleted source; surviving
  memory/knowledge metadata is still checked. Retention uses persisted capture
  protection (`protected` sessions and excluded runs), not the mutable seven-day
  pause log: a later toggle, expired interval or transient log read failure cannot
  permanently change an old receipt. Already-excluded receipts remain excluded:
  their single bit cannot distinguish an old read failure from a genuine private
  source. Older, metadata-bearing receipts are minimized conservatively as excluded
  during migration. These opaque provenance tombstones
  are retained for the lifetime of the activity database, including after a purpose
  closes, because surviving memory, knowledge and Self records can still cite them.
  They are not an app-usage history and contain no reconstructable activity content.
- A goal whose referenced activity is missing without a receipt (including purges
  before receipts existed) stays visible in `workingToward` and legacy `openLoops`
  as title, lifecycle `status`, and the localized line "Evidence no longer on this
  Mac." Optional `evidenceUnavailable: true` marks this content-free fallback.
  It carries no progress, desired outcome, touch/date, next step, due commitment,
  counterparty or chat reference. Its `refs` array is empty and it contributes no
  evidence-table entry. Optional `purposeId` preserves deduplication, stable keys
  and feedback identity without acting as a source; clients prefer it to the
  ordinary first purpose ref when present. The fallback never enters the model
  prompt. Existing sensitive/protected exclusions still omit a purpose; a database
  read failure does not establish that evidence was deleted. Old phones ignore the
  new marker and display the same safe title/state/detail through `openLoops`.
  When a run outlives a member purged before receipts existed, retention preserves
  a separate unavailable-evidence marker on the run and its members before their
  membership disappears. The marker stores only source kind and opaque ID, with
  the same database lifetime as retention receipts. It is neither a privacy
  exclusion nor proof of deletion: it can exist while a failed deletion leaves a
  run or member alive. Live and retained sources with this marker keep the same
  content-free fallback; real privacy exclusions still take precedence.
  Chat-reported commitments also retain their legacy row; richer clients and the
  narrative deduplicate that row instead of deleting it from the wire document.
- A commitment may carry `momentId` for an existing `commitment_slipping` engagement
  moment, including a pending one. `chatMessageId` is present only when its report
  already exists in main chat; `chatSessionId` names the mobile-visible conversation
  when available. A moment ID alone does not mean delivery. The brief references
  that report and opens the existing conversation; it neither creates a second
  moment nor copies the chat message body. Clients without exact-message navigation
  may open the referenced conversation or main chat. These identifiers are optional.
- `limits.excludedSensitiveItems` is an optional, nonidentifying count of Purpose
  Map items excluded by sensitive-app, protected/private or observation-pause
  checks. Excluded source names, titles and content are never
  included. The narrative model receives only permitted titles and bounded progress
  summaries; never raw transcripts, OCR, message bodies or outcome evidence tokens.
  Working-toward summaries can connect the opening paragraph to observed work; main
  chat reminders and their exclusive evidence are omitted from narrative input
  to avoid repetition. Independent purpose progress stays eligible; a pending
  or desktop-toast-only moment without a chat report stays eligible too.
- `sourceKind` is one of `history`, `initiative`, `watching`, `purpose`, `meeting`, `memory`,
  `knowledge`, `deliverable`, `schedule_run`, `principal`, `self_update`, `learning`, `summary`,
  `web`, `file`, `calendar`, `calendar_day`, `find`, `news`, `activity`, `activity_run`,
  `activity_session`, `activity_outcome`, `engagement_moment`, `chat_message`, `meeting_attendance`.
- `dayShape` may be `null` on older documents. Arrays, when present, are never
  `null`; use `[]`. The phone brief reads (`GET /brief/latest` and
  `GET /briefs/{briefId}`) add these live calendar-strip fields to the existing
  activity-statistics object, preserving `activeMinutes`, `hours`, `topApps`
  and the optional activity bounds:
  `{"rangeStart","rangeEnd","now","events":[{"id","start","end","title",
  "shortName","shortestName","kind"}],"hold":null,"calendarConnected"}`.
  All times are RFC3339 with the brief's local timezone offset. The default
  range is 8:00 AM to 7:00 PM, widened to the first visible event minus one
  hour or the last visible event's end plus one hour. Events overlap that
  local day, sorted by start/title; all-day, deleted, canceled and declined
  events are omitted. `kind` is `personal` for a known personal calendar or
  an event with no other attendees, otherwise `meeting`. Short names derive
  only from title and attendee names. Current source/session privacy and the
  brief's mail holdback apply before events can affect labels or range.
  A successful calendar read establishes `calendarConnected`, including an
  empty day; detection alone does not. False means the client omits the strip.
  Live strip dates are limited to the computer's current local today and
  tomorrow. An evening brief draws tomorrow; a morning brief reread later on
  the same date keeps today. A brief whose strip date falls outside that
  window retains its original prose and activity statistics without live
  strip fields. Clients draw a now marker only when the displayed day is
  today. These live fields are optional on older responses and push documents.
  The desktop `GetTodayDayShape(dateISO)` binding accepts only those two
  `YYYY-MM-DD` dates. Malformed or out-of-window arguments return Go's typed
  `*TodayDayShapeDateError`, with stable wire code
  `TODAY_DAY_SHAPE_DATE_INVALID`.
- A closing-question narrative paragraph can carry optional
  `hold: {"start","end","label"}`. It is a proposed time range, validated
  within the strip day, future against the current post-model acceptance
  clock, and free of overlaps with the gated calendar. Clock ranges in the
  question must match its ordered endpoints. A single clock may name either
  validated endpoint; two clocks left outside a recognised ordered range
  cause rejection and retry, even if each occurs in cited source facts.
  It is attached to that question's
  refs and privacy checks. Delivery rechecks the current clock and calendar;
  an expired or conflicting strip hold is withheld as `null`, without moving
  its endpoints or changing the stored prose. A changed hold also changes its answers' opaque
  identity. The strip's `hold` is the same proposal, or explicit `null` when
  no visible question proposes one. Reading or choosing an answer only
  previews/prepares; the existing Send path remains the execution boundary.
  This field never creates a calendar event. Older/non-hold paragraphs omit
  it. See the [exact day-strip contract and verification](../docs/hub-reviews/claude-codex-dayshape-backend-self.md).
- `meetingAttendance` (optional) is a bounded observation of each calendar slot on
  **this computer**, not proof that a person missed a meeting elsewhere. At most
  32 records are sent: `{"eventId","title","state","reason","scope":"computer",
  "refs":["e1"]}`. `state` is `attended`, `skipped`, or `unknown`; `eventId` is the
  existing calendar occurrence ID, `title` is the bounded calendar title without
  past clock times, and refs point to `sourceKind: "meeting_attendance"` evidence.
  `attended` requires sustained matching call-window or in-call audio metadata.
  `skipped` means continuous captured activity elsewhere on this computer during
  an ended remote slot, with known microphone/camera state and no matching call
  or device ambiguity. Initial and resumed observation records device snapshots;
  a missing or protected state never means a device was off. It must never
  be rendered as proof that the user did not attend on a phone or in person.
  Missing coverage, sleeping, paused observation, protected or purged evidence,
  meetings elsewhere, unfinished slots, and ambiguous overlapping slots remain
  `unknown`. `reason` is a machine code: `matching_call_activity`,
  `active_elsewhere_on_computer`, `insufficient_observation`, `observing_paused`,
  `computer_away`, `meeting_elsewhere`, `slot_not_finished`,
  `ambiguous_calendar_slots`, or `evidence_unavailable`. The daily observation
  projection is recomputed from currently readable evidence when building a brief;
  its cache never restores deleted evidence. Current Closed Eyes or an active
  observation pause suppresses collection. Local stored documents retain backing
  session identities separately from the wire document. Reads and queued pushes
  recheck that provenance; withdrawn attendance and its citations are omitted.
  Raw window titles, transcript text,
  audio, and source session IDs are not sent in these records. Older phones
  ignore this additive v1 field; it does not alter the encryption vectors.
- `doing` lists documents from Spotlight's metadata on the computer
  (`sourceKind` `file`, `title` the file name with personal details and codes redacted, `status`
  `opened` or `edited`, `kind` `document`, `spreadsheet`, `presentation`, `pdf`, `image`, `text`,
  `code` or `other`, `detail` "Opened" or "Edited", then the app and the commitment the name
  matches when known). These rows support Today's document list and Chat about this.
  Never a path or anything inside a file; file-history items stay in `doing` and
  never feed `review`, `decide`, `plan`, `highlights` or the narrative model.
- `watching` lists browser history for the window: the sites visited most
  (`sourceKind` `web`, `kind` `site`, `title` the host, `detail` the visit count, `status`
  `visited`) and research threads (`kind` `research`, `title` the search, `detail` the pages read
  after it as a count, `status` `researched`). Page titles stay on the Mac; hosts and queries
  come from scrubbed history (no query strings, personal details and codes redacted).
  Browsing-history items stay in `watching` and never feed `review`, `decide`,
  `plan`, `highlights` or the narrative model. The narrative does not turn feeds into a reading list.
  New finds on the user's topics lead the section once the day's search ran (at most 3:
  `sourceKind` `find`, `kind` `find`, `status` `new`, `title` the page title, `detail` "You
  spent time on <topic> <day>; <what is new>", `url` the page). The search runs after the
  morning brief, so the Mac stores the brief again with its finds and sends it without a
  second push. Only the topic labels went to the search; the brief names the topic, never
  what the user read.
- `needsYouCount`, `counts.needsYou` and `counts.selfUpdatesPending` are always 0 since the
  Desk was removed (section 13). They stay so older phones still decode the brief.
- `moved` lists the one-time work whose state changed in the window: `sourceKind` `history`,
  `kind` `one_time_work`, `status` the work's state, `detail` a short phrase for it. `openLoops`
  are the current Purposes and `learned` the patterns shadow learning supports, with the user's
  correction in place of a corrected pattern.
- The morning brief recaps the last day the computer was in use. When nothing happened since local
  midnight, the recap reaches back to the beginning of that day (at most seven days back); the
  brief's `windowStart`, its meetings and its browsing keep the brief's own window. `moved` then
  also lists that day's activity summaries (at most 6: `sourceKind` `summary`, `kind` `recap`,
  `status` `done`, `detail` the observed minutes without clock times) and the one-time work that moved
  since that day. A summary with no readable apps, one that involved a sensitive, messaging or
  mail app, one from a private window, or one from a stretch when observing was paused stays
  out. `openLoops` also lists the one-time work still open whenever it last moved (`sourceKind`
  `history`, `kind` `one_time_work`). `watching` also lists up to 3 stories from the newest Daily
  News run when Daily News is on and that run finished well in the last day (`sourceKind`
  `news`, `kind` `news`, `status` `news`, `detail` the publication, `url` the story); a run that
  searched for the user's topics counts only while observing is not paused and Watch my screen
  is on. Evening and on-request briefs also use permitted activity summaries from their
  observation window. Evening calendar context covers the whole local day and tomorrow,
  so a brief can plan forward; attendance covers the current local day. Future displayed
  clock times use the user's local time. A summary's `done` describes its processing state,
  never proof that the user's work was completed.
  Every time of day in a brief, in `detail` and in the narrative, is the user's local
  time.
- `audio` (optional, added with the audio brief) describes either the spoken version stored
  on the Mac or a voice download in progress. Its presence alone does not mean audio is
  playable. The additive `status` and `statusMessage` fields have these forms:

  | State | Audio fields | Client behavior |
  | --- | --- | --- |
  | Spoken audio ready | `"status":"ok","mime":"audio/mp4"` (or `"audio/wav"`), positive `bytes` and `durationSeconds` | Play on the phone only when `attachment` is present. |
  | Voice downloading | `"status":"downloading","statusMessage":"Voice downloading","mime":"","bytes":0,"durationSeconds":0` | Show the localized status; there is no audio file or `attachment` to play. |
  | Older brief | `status` and `statusMessage` omitted | Use the existing media fields and `attachment` to determine availability. |

  `statusMessage` is localized to the Mac's UI language (`"正在下载语音"` in zh-CN) and is
  omitted when ready. Missing voices start a background download; a later brief can contain
  spoken audio after installation succeeds. Unknown status values must not imply playback.
  `attachment` is `{"id":"<uuid>","key":"<base64url 32 bytes>","nonce":"<base64url 12 bytes>"}`
  and appears only after the Mac uploads a sealed copy to the relay (section 17). For ready
  audio without `attachment`, the file exists on the Mac only. A phone that does not know
  these fields continues to show the text.
- `generation` (optional) explains a scheduled brief generated after its slot: `mac_asleep`
  when the Mac was observed sleeping through its scheduled slot, or `late` when it came more
  than 30 minutes after its slot because Isoform was not running then or its first tries
  failed. It is made as soon as it can be, until the next brief is due within the same local
  date:

  ```json
  {"reason":"mac_asleep","scheduledAt":"2026-10-09T07:00:00Z",
   "generatedAt":"2026-10-09T14:00:00Z",
   "message":"Your morning brief, a bit late."}
  ```

  `scheduledAt` is the missed slot and `generatedAt` matches the brief's `asOf`, both RFC 3339
  timestamps. `message` is a plain label in the Mac's UI language (en or zh-CN). The field is
  omitted for normal generation. Only `mac_asleep` says the Mac was asleep; an overdue brief
  alone is `late`, never evidence of sleep. A reader that shows the field shows `message` for
  any reason, including ones it does not know. The metadata does not
  change the brief's schema version, scheduled slot or identity.
- Today also reads its local signals from this stored document. Optional `upcoming` lists
  up to three enabled automations due within 48 hours, including overdue ones, earliest first
  (`sourceKind` `schedule`, `status` `upcoming`). It shares the 60-reference evidence budget.
  Optional item `context` carries `preview`, `sourceLabel`, `scheduleSpec`, `jobId`,
  `fileCount` and `blockedReason` when present. `scheduleSpec` contains `kind`, `nextRunAt`
  (Unix milliseconds), and, when applicable, `expression`, `timezone`, `everyMs` and `atMs`.
  The desktop localizes these structured values with the Automations formatter. The old
  English `schedule` field is read for compatibility but no longer produced or displayed.
  Previews and expression/source labels are limited to 320 bytes and attached only after
  the normal privacy filter and section caps. Today's failed
  runs, memories, knowledge and deliverables are included even when they precede the brief's
  window; existing records are enriched rather than repeated. Failed runs retain their job
  ID for Retry, and upcoming jobs for Run now. The activity AI summary has no source
  provenance and is excluded from the stored and relayed brief. The desktop-only
  `GetLatestBrief` response carries `dailySummary` (`text`, up to 8000 bytes, and `at`)
  alongside the shared `brief`. Legacy `dailySummary` fields in brief JSON are ignored
  when decoding and never republished. Context is not added to the opening narrative's
  model prompt. Older documents without these fields still render normally. Reading the latest brief refreshes these local signals
  between scheduled slots, without generating a narrative or sending a brief-ready notice.
  Failed source reads retain that source's previous rows; a successful read removes
  records that are no longer present.
  The generated recap keeps its ID, `asOf`, audio and source-linked citations; upcoming
  actions and current failed-run actions reserve evidence before ordinary section rows.
  Within the shared 60-reference cap, reserve up to 3 upcoming actions, 8 failed-run
  actions and one citation per recap paragraph (up to 4), then atomic Purpose rows.
  Additional recap citations and ordinary sections use the remaining slots. Shared
  source IDs deduplicate across reservations; a Purpose row never loses part of its
  backing evidence to fit the cap.
  When no generated brief exists for today's local date, the desktop stores the same V1
  shape in `briefs/today.json` and serves it through the latest and by-ID routes. This
  snapshot is available by ID only on its local date, follows the same 14-day retention
  sweep, and is outside generated-brief history. It does not open the daily finds gate or
  consume a slot, generation cooldown or the next brief's coverage window. Source
  collection and relay delivery happen outside the brief-write lock, and unchanged
  serialized documents are not rewritten. Local work therefore remains available
  with scheduled briefs off, before the first slot, and during desktop startup.
- `audio` (optional, added with the audio brief) describes the spoken version of the brief the
  Mac stored beside it: `{"mime":"audio/mp4"|"audio/wav","bytes":N,"durationSeconds":S,
  "attachment":{"id":"<uuid>","key":"<base64url 32 bytes>","nonce":"<base64url 12 bytes>"}}`.
  `attachment` is present only when the Mac uploaded a sealed copy to the relay (section 17);
  without it the audio exists on the Mac only. A phone that does not know the field shows the
  text as before.
- The phone decodes with every field optional and ignores unknown fields.

### 8.1 The brief package (1.0, 2026-10-28)

Design: `docs/handoff-2026-10-28/handoff-2026-10-28-1140-brief-on-phone-v1.0.0.md`; build plan:
`docs/briefs/brief-on-phone-1.0/README.md`. Everything here is additive in protocol v1: older
phones keep reading `MobileBriefV1` objects and ignore the new fields.

**One package per brief.** The Mac stores each brief's phone document once, as the exact bytes
it serves (`briefs/<briefId>.package`, mode 0600), and serves those bytes everywhere: the pushed
`brief` envelope (zero-padded, section 4), `GET /briefs/{briefId}` and its parts. The package is
a `MobileBriefV1` JSON object. Its **version** is the lowercase hex SHA-256 of the package bytes
(after removing the envelope's zero padding). A phone that hashes what it received gets the
version the Mac names.

- **Immutable once made.** Text, chips, sections, the day's statistics, audio and paragraph
  timings are fixed when the package is written. Live rows (waiting on you, the calendar, new
  finds, topic buttons) never rewrite a package: they live in the Today snapshot (20.7a). The
  package's `dayShape` carries only the brief's own activity statistics, never the live
  calendar strip.
- **A package is replaced, never edited.** A new version replaces the whole package when the
  audio is attached (text first, then the package with `audio`), when the current read policy
  withdraws something from it or the display language changes (the Mac checks the source brief
  through the same read gates on every read and writes a new package when the result differs),
  or when the same `briefId` is generated again. A phone never mixes parts of two versions.
- **The greeting is never a paragraph of its own.** A first paragraph that is only a greeting
  ("Good morning.", "早上好。") is joined to the next paragraph's first sentence when the brief
  is written, and again when an older stored brief is packaged.
- `kind` (string, additive) names the card title: `morning`, `evening`, `week` (the Friday
  evening "Your week"), or `on_request` (asked for, in Chat, the menu bar or the phone). A brief
  made late because the Mac slept keeps its scheduled kind and carries `generation`
  (`reason: "mac_asleep"`, `scheduledAt`, `generatedAt`); `dueAt` (RFC 3339, additive) is when a
  scheduled brief was due, omitted for `on_request`. `asOf` is when it was made.
- `audio.paragraphs` (additive) lists where each spoken paragraph starts:
  `[{"source":"narrative"|"weeklyRecap","index":0,"startSeconds":0}]`, ascending by
  `startSeconds`, `index` zero-based into that array. It is present only when the reading
  follows the written paragraphs; a reading built from Isoform's template has none, and the
  phone then plays without following along. `audio.durationSeconds` is the length.

**Routes** (inner hub routes, `brief:read`):

| Method and path | Result |
| --- | --- |
| `GET /briefs/{briefId}` | the package object when it is at most 48 KiB; otherwise `{"chunked":true,"version":"<hex>","parts":P}` |
| `GET /briefs/{briefId}?package=1` | always `{"briefId","version":"<hex>","parts":P,"data":"<part 0>"}` |
| `GET /briefs/{briefId}?version=<hex>&part=<i>` | `{"version","part","data"}`; `409 version_changed` when the package changed, `400` for a bad part |
| `POST /briefs/{briefId}/received` | body `{"version":"<hex>"}` → `{"received":true}`; the phone has saved that package |

Parts are consecutive 24 KiB slices of the package bytes, base64url without padding. The phone
concatenates the decoded parts, checks the SHA-256 against `version`, and only then treats the
brief as arrived. A `409` means the phone starts again from `?package=1`. `404` means the brief
is gone (retention or withdrawn as quiet).

**The current brief.** The Mac alone chooses it: the newest stored brief, by `asOf`, of any
kind, whose generation finished with written text (`narrativeStatus: "ok"`). Quiet slots and
briefs without text are never current, and regenerating an older `briefId` never moves the
current brief back in time. `latest.json` follows the same rule. Every Mac reader (`/today`,
`/brief/latest`, `/status`, the desktop Today page, the menu bar and Chat) reads this one
choice. The phone applies the same rule to the packages it has fully received.
A written brief wins over a newer brief that has only Isoform's template; a newer written brief always wins. With no written brief stored yet, the newest stored brief stands in.

**Outbox.** For each paired phone the Mac keeps the newest published brief in
`brief-outbox.json` (0600) as `{deviceId, briefId, version, notify, queuedAt, handedAt}` until
that phone acknowledges it with `POST /briefs/{briefId}/received` (any version of that brief or a
newer brief's acknowledgement clears it), or for 7 days. The outbox survives restarts. After each
relay registration the Mac sends again every entry the relay never took, and every entry the
relay took more than an hour earlier without an acknowledgement; a resend never asks for a
second push (`notify:false`). Before sending, the Mac reads the brief's current package, so a
withdrawn or replaced package is never replayed.

**Audio on the phone.** Superseding 17.3's "never in the background": after a package with
`audio.attachment` is saved, the phone downloads and opens the attachment at once, in the
background, and keeps it beside the package. Nothing waits for it except Play. The phone keeps
package text for 30 days and audio for 7 days; an older brief's audio is fetched through
`GET /briefs/{briefId}/audio` (17.4) when the Mac can be reached.

## 9. Push

A push is an APNs alert with the title `Yansu`. The relay chooses its body from a
short fixed list, and the body names no person, count or content. Two kinds exist:

For gateway-backed push registration and the encrypted grant delivery flow, see
section 21.3. The phone registers after acquiring its APNs token, applies each
background `push_grant` notification to the paired hub, and sends the grant as
`gatewayGrant` when registering with that hub's relay. It re-registers within
30 days of grant expiry or when the relay reports an invalid grant.

- `brief` carries no content at all. The phone fetches the brief after a tap or on
  foreground.
- `moment` carries a sealed notice (section 14). The sealed text travels in `yansu.ct`
  and the phone's notification extension decrypts it. The relay and APNs see only the
  ciphertext and the fixed fallback body.

Release 2's `needs_you` kind is retired with the Desk (section 13). The relay still accepts
it on an event `out` frame and sends it ("Isoform needs you.", time-sensitive) so a Mac on an
older build keeps working, but current hubs never send it and the phone opens the Brief for it.

Brief push (unchanged):

```json
{"aps":{"alert":{"title":"Isoform","body":"Your brief is ready."},"sound":"default","mutable-content":1},
 "yansu":{"kind":"brief","hub":"<hubId>","eid":"<brief version>"}}
```

Moment push (section 14.4):

```json
{"aps":{"alert":{"title":"Isoform","body":"Work is waiting on you."},"sound":"default","mutable-content":1,
        "category":"YANSU_MOMENT","thread-id":"<hubId>","interruption-level":"time-sensitive"},
 "yansu":{"kind":"work_blocked","hub":"<hubId>","id":"<noticeId>","dev":"<deviceId>","ct":"<sealed notice>"}}
```

One push per stored brief with `notify:true` and one per moment for each device that has
an enabled push token. Retries and leases follow the outbox policy (5 attempts,
15-minute backoff doubling, 24-hour expiry). A payload over 4096 bytes is never sent.

## 10. Scopes

| Scope | Grants |
| --- | --- |
| `brief:read` | read briefs and poll responses |
| `brief:refresh` | ask the hub for a fresh brief (rate limited) |
| `presence:read` | hub online state |
| `push` | register an APNs token at the relay |
| `desk:read`, `decide` | release 2, retired with the Desk (section 13): the hub still accepts them so older phones can pair and keep working, but no route needs them |
| `chat:read`, `chat:write`, `memory:correct`, `handoff:create` | release 3 (section 15.1) |
| `brief:feedback`, `offers:read`, `offers:decide`, `goals:read`, `goals:write`, `library:read` | release 4 (section 20.1) |

## 11. Test vectors

`mobile-protocol/vectors-v1.json` is generated by a Go test in `wails-gui`
(`go test -run TestMobileProtocolVectors -update`) and consumed by Go and XCTest:

```json
{"version":1,
 "hubPrivate":"<base64url>","hubPublic":"<base64url>","devicePrivate":"<base64url>","devicePublic":"<base64url>",
 "hubId":"...","deviceId":"...","pairingId":"...","secret":"<base64url>",
 "kPair":"<base64url>","claimToken":"<base64url>","confirmCode":"123456",
 "kD2H":"<base64url>","kH2D":"<base64url>",
 "claim":{"plaintext":"<json string>","ct":"<base64url>"},
 "approval":{"plaintext":"<json string>","ct":"<base64url>"},
 "envelopes":[{"dir":"d2h","seq":1,"kind":"req","plaintext":"<json string>","ct":"<base64url>"},
              {"dir":"h2d","seq":1,"kind":"res","plaintext":"<json string>","ct":"<base64url>"}],
 "remoteInvitation":{"plaintext":"<JSON invitation containing iroh>","link":"isoform://pair#..."}}
```

## 12. Desktop bindings (Wails `App` methods; re-exported in `wails-shim-app.ts`)

```
GetMobileConnectionStatus() (MobileConnectionStatus, error)
EnableMobileConnection(relayURL string) (MobileConnectionStatus, error)   // empty relayURL keeps the saved one
DisableMobileConnection() (MobileConnectionStatus, error)
GenerateMobileConnectionCode() (MobileConnectionStatus, error)              // opens a pairing, returns the invitation
ConfirmMobilePairing(pairingID string, approve bool) (MobileConnectionStatus, error)
RevokeMobileDevice(deviceID string) (MobileConnectionStatus, error)
ResetMobileRemoteAccess() (MobileConnectionStatus, error)              // rotates the tunnel key and revokes paired phones
GetMobileKeepAwake() (MobileKeepAwakeStatus, error)
SetMobileKeepAwake(on bool) (MobileKeepAwakeStatus, error)
GetMobileBriefSchedule() (MobileBriefSchedule, error)
SetMobileBriefSchedule(schedule MobileBriefSchedule) (MobileBriefSchedule, error)
GenerateMobileBriefNow() (MobileBriefRequestResult, error)
```

```go
type MobileConnectionStatus struct {
    Enabled        bool                 `json:"enabled"`
    RelayURL       string               `json:"relayUrl"`
    SavedRelayURL  string               `json:"savedRelayUrl"`   // relay typed in Settings; empty when the default or remote access applies
    RelayState     string               `json:"relayState"`      // disabled | connecting | connected | offline
    RelayLastSeen  string               `json:"relayLastSeenAt"`
    Warning        string               `json:"warning,omitempty"` // localized loopback reachability advisory
    WarningCode    string               `json:"warningCode,omitempty"` // relay_loopback
    WarningHost    string               `json:"warningHost,omitempty"` // host for client translation
    RemoteAccessOffReason string         `json:"remoteAccessOffReason,omitempty"` // saved_relay | environment_relay | build_relay | disabled; omitted while remote access applies
    HubID          string               `json:"hubId"`
    HubName        string               `json:"hubName"`
    ConnectionCode string               `json:"connectionCode"`  // isoform://pair#... while a pairing is open
    ExpiresAt      string               `json:"expiresAt"`
    PendingPairing *MobilePendingPairing `json:"pendingPairing"` // {pairingId, deviceName, code} while a claim awaits approval
    Devices        []MobileDeviceView   `json:"devices"`         // {deviceId, deviceName, createdAt, expiresAt, lastSeenAt, scopes, simulator?}
    RemoteAccess   *MobileRemoteAccessView `json:"remoteAccess,omitempty"` // state, optional endpointId and lastError; never the ticket
    InstantPush    bool                 `json:"instantPush"`     // true when this computer's relay has a push gateway (APNs)
}
type MobileKeepAwakeStatus struct {
    On           bool `json:"on"`
    PairedPhones int  `json:"pairedPhones"`
    OnPower      bool `json:"onPower"`
    HasBattery   bool `json:"hasBattery"`
}
type MobileRemoteAccessView struct {
    State      string `json:"state"` // off | starting | ready | restarting | failed
    EndpointID string `json:"endpointId,omitempty"`
    LastError  string `json:"lastError,omitempty"`
}
type MobileBriefSchedule struct {
    Enabled            bool   `json:"enabled"`
    Morning            string `json:"morning"`            // "07:00"
    Evening            string `json:"evening"`            // "18:00"
    LastGeneratedAt    string `json:"lastGeneratedAt"`
    LastSlot           string `json:"lastSlot"`
    NarrativeAvailable bool   `json:"narrativeAvailable"` // native runner present
    FeedEnabled        bool   `json:"feedEnabled"`        // private podcast feed on this Mac
    FeedURL            string `json:"feedUrl"`            // set while the feed is on
    AdaptiveTiming     *bool  `json:"adaptiveTiming,omitempty"`
    NextPredictedAt    string `json:"nextPredictedAt,omitempty"` // RFC 3339 UTC; predicted in the computer's zone
    NextPredictedSlot  string `json:"nextPredictedSlot,omitempty"` // morning | evening
    TimingReason       string `json:"timingReason,omitempty"` // fixed reason code
}
type MobileBriefRequestResult struct {
    Status        string `json:"status"`        // started | running | cooldown
    NextAllowedAt string `json:"nextAllowedAt"`
}
```

Adaptive brief timing adds these optional schedule fields to desktop bindings
and the existing `GET /status` schedule JSON; protocol version 1 is unchanged.
`adaptiveTiming` is returned explicitly by new hubs. An omitted value in a
schedule write preserves the saved choice, so older clients cannot turn it off.
Dogfood Isoform builds default to on; other flavors default to off. Predictions
are estimates, recomputed from the prior 14 local days and current timing gates,
and are absent when scheduling or adaptive timing is off. A hub may return
`timingReason: "no_window"` with no time when neither today nor tomorrow has an
eligible slot. Consumers must handle missing fields and unknown reason codes.

Reason codes are `fixed_slot`, `learned_opens`, `learned_presence`,
`learned_activity`, `before_meeting`, `work_settled`, `quiet_hours`,
`waiting_for_activity`, `cap` and `no_window`. `learned_presence` is reserved until
an authenticated foreground signal exists; background relay contact is not a
phone-open observation. The wire carries only the predicted time, slot and
reason, never timing history or source content. The fixed slots remain the
ordinary ceilings; settling defers delivery at most 90 minutes after a fixed
slot while the computer is awake (`cap`), unless a meeting is still going on.
A slot stays eligible until the next brief is due, within the same local date,
so quiet hours and sleep defer it at most until then. Existing daily
brief IDs still prevent duplicate automatic generation. See
[`docs/brief-timing.md`](../docs/brief-timing.md) for the rules and source limits.

`MobileConnectionStatus.warning` is omitted when there is no loopback reachability concern.
For a loopback relay, a non-Simulator paired device or pending claim sets the advisory;
expired device records do not. The English form is `"A phone on another network cannot
reach <host>."`, with a corresponding zh-CN translation. All existing connection and pairing
bindings return this status. Desktop Settings and its pairing-code panel render
`warningCode:"relay_loopback"` with `warningHost` using the current frontend language and
the `settings:mobileConnection.loopbackWarning` key in both en and zh-CN; they do not render
the Go-generated `warning` message. The metadata fields are also omitted when there is no
warning. Unknown codes are ignored. The warning never disables pairing or relay connections.

The desktop `GetMobileBriefAudio(briefId)` response also has optional `status` and
`statusMessage` fields matching section 8. During a voice download it returns
`status:"downloading"` with empty `mime` and `base64` and zero `durationSeconds`; consumers
must not attempt playback based only on the presence of an audio response.

Added with the notification work (`docs/notifications-design.md`):

```go
func (a *App) SetEngagementLevel(level string) (EngagementSettingsView, error) // off | fewer | normal | more
func (a *App) GetEngagementWeekSummary() (EngagementWeekSummary, error)        // {available, sent, actedOn, days, kinds:[{kind, created, scheduled, sent, delivered, opened, acted, dismissed, muted, actRate}]}
func (a *App) GetMobileBriefHealth() MobileBriefHealth                        // last run, next run, missed runs, failing slot, latest brief id, audio
func (a *App) GetMobileBriefAudio(briefID string) (MobileBriefAudioData, error) // {briefId, mime, base64, durationSeconds}; "" means the newest brief
func (a *App) GetTodaySuggestion() (TodaySuggestionView, error)
func (a *App) ResolveTodaySuggestion(input TodaySuggestionDecision) (TodaySuggestionView, error) // add_to_upcoming | do_once | not_now | dismiss
```

The desktop emits the Wails event `mobile:updated` (no payload) whenever connection status,
pairing state, device list or brief schedule changes; the settings UI re-reads status on it.

Added with Today's brief and the finds on the user's topics (not on the phone's API; the three
reads are on the browser bridge allowlist):

```go
func (a *App) GetLatestBrief() (TodayBriefView, error)          // today's brief, building, next slot, empty, missing permissions
func (a *App) GetTopicFinds() (TopicFindsView, error)           // status off | unconfigured | no_topics | finding | ready | none | error
func (a *App) FindNewOnTopics() (TopicFindsView, error)         // starts a search now; 10-minute cooldown
func (a *App) RecordTopicFindFeedback(input TopicFindFeedbackInput) (TopicFindsView, error) // open | more | less | not_interested
func (a *App) GetUserTopics() (UserTopicsView, error)
func (a *App) UpdateUserTopic(input UserTopicUpdate) (UserTopicsView, error) // pin | unpin | rename | remove | restore
func (a *App) SetTopicFindsEnabled(enabled bool) (UserTopicsView, error)
```

The desktop emits `brief:stored` (payload: the brief id) when a brief, its spoken version or
its finds are stored, `topics:finds` when a search starts or ends or feedback changes the
finds, and `topics:updated` when the topic list changes.

## 13. Release 2: decisions on the go (removed)

Release 2 gave the phone a Desk: a read model of the Mac's Agency (`GET /desk`), permission
decisions (`POST /decision`), initiative detail and commands, Self-update review, new Purposes
and their revision and dismissal, and `needs_you` events and pushes. The Agency and the Desk
were removed from the Mac on 2026-10-13, and the phone's Desk tab with them. Since then:

- `GET /desk`, `POST /decision`, `GET /initiatives/{id}`, `POST /initiatives/{id}/command`,
  `POST /initiatives/{id}/feedback`, `POST /self/updates/{id}/review`, `POST /purposes`,
  `POST /purposes/{id}/revise` and `POST /purposes/{id}/dismiss` answer 404 like any unknown
  route.
- The hub sends no `needs_you` event and asks for no `needs_you` push (see section 9 for the
  relay's handling of older hubs).
- The scopes `desk:read` and `decide` stay known (section 10) and grant nothing. A current
  phone does not ask for them.
- One-time work is started from the phone's Chat tab (`POST /handoffs`, section 15).

## 14. Notices and receipts

A notice is one moment the hub wants the user to see: a decision, a commitment, a
finished piece of work. The hub seals each notice for one device, so the relay and APNs
can route it without reading it. Receipts are the phone's reports about a notice. This
section is added with the notification foundations (Phase 0b); the relay, the desktop
hub and the iPhone implement it.

### 14.1 Notice key

```
S       = X25519( hubPrivate, devicePublic )          (the session secret of section 4)
kNotice = HKDF-SHA256( ikm = S, salt = "yansu-mobile-v1|" + hubId + "|" + deviceId (ASCII),
                       info = "notice", length = 32 )
```

The salt is the envelope salt of section 4. The info string differs, so `kNotice` never
equals `kD2H` or `kH2D`. The phone derives the same key from its private key and the hub
public key.

### 14.2 Sealed notice

The plaintext is UTF-8 JSON with these fields in this order:

```json
{"v":1,"id":"<noticeId>","kind":"work_blocked","title":"The Q3 reply to Sam needs your attention.",
 "body":"","category":"YANSU_MOMENT",
 "actions":[{"id":"answer","label":"Answer","auth":false},{"id":"stop","label":"Stop","auth":true}],
 "subjectId":"<id the actions target>","momentId":"<chat card id>",
 "createdAt":"2026-10-09T18:00:00Z","expiresAt":"2026-10-10T18:00:00Z"}
```

- `noticeId` is a lowercase UUID version 4. `kind` matches `[a-z][a-z_]{0,39}`.
- `title` is 1 to 120 bytes; `body` is at most 300 bytes and may be empty; `subjectId` is at
  most 128 bytes and may be empty.
- `momentId` is an optional additive v1 field, present when the notice corresponds to a proactive
  chat card. It identifies that card; `subjectId` continues to identify the action target. When
  present it follows `subjectId` in the plaintext field order.
- `category` is `YANSU_MOMENT` or `YANSU_BRIEF`. Release 2's `YANSU_DECISION` (Approve and
  Decline on a `decision_due` notice) is retired; the phone no longer registers it, so such a
  notice from an older Mac shows the fallback body.
- At most four actions. An action `id` matches `[a-z][a-z_]{0,31}`; its `label` is 1 to 40
  bytes. `auth` asks the phone for the owner's authentication before the action runs.
- `expiresAt` is RFC 3339 or the empty string. `createdAt` is required.
- The plaintext is padded with spaces to a multiple of 64 bytes. A JSON object ends with
  `}`, so the opener removes trailing spaces and the decoded text is unchanged.

Sealing uses ChaCha20-Poly1305 (IETF, 16-byte tag) with a random 12-byte nonce. The AAD is
ASCII:

```
"yansu-notice-v1|" + hubId + "|" + deviceId + "|" + noticeId
```

The wire form is `ct = base64url(nonce || ciphertext || tag)`, unpadded. It is at most
3,500 characters; a larger notice is refused by the hub. A text that escapes in JSON (a
control character or `<`) grows, so the limit can refuse a notice whose visible length is
within the field limits.

### 14.3 Notice on the hub-to-relay path

A notice goes to each device that holds the `push` scope as one `out` frame. The frame
extends section 6 with these fields:

```json
{"t":"out",
 "env":{"v":1,"hub":"<hubId>","dev":"<deviceId>","dir":"h2d","seq":18,"kind":"event","ct":"<base64url>"},
 "notify":true,"push":"moment","noticeId":"<uuid>","kind":"work_blocked",
 "ct":"<sealed notice>","category":"YANSU_MOMENT","collapse":"<collapse id>","level":"time-sensitive"}
```

- `env` is an `h2d` event envelope (section 4) whose plaintext is
  `{"event":"notice","noticeId":"<uuid>","kind":"<moment kind>","at":"<RFC 3339>"}`. The phone
  reads it on poll, so a phone that missed the push still learns of the notice.
- `ct` is the sealed notice of section 14.2, sealed under the device's `kNotice`. Each device
  receives its own `ct` and its own envelope; the `noticeId` is the same for all of them.
- `level` is `passive`, `active` or `time-sensitive`. `collapse` is optional: 1 to 64
  printable ASCII characters; when absent the relay uses the `noticeId`.
- The relay refuses the frame (`error`, `ref:"out"`) and stores nothing when `notify` is
  false but a notice field is set, when `notify` is set on a `res` envelope, when a field is
  malformed, or when a field is unknown. A valid frame stores the envelope and queues one
  push per enabled token of the device. A device with no token gets the envelope only.

### 14.4 Moment push

| Field | Value |
| --- | --- |
| `aps.alert.title` | `Yansu` |
| `aps.alert.body` | the fallback text below for `yansu.kind` |
| `aps.sound` | `default` for `active` and `time-sensitive` only; absent for `passive` |
| `aps.mutable-content` | `1` |
| `aps.category` | the notice category |
| `aps.thread-id` | the hub id |
| `aps.interruption-level` | the notice `level` |
| `yansu.kind` | the moment kind |
| `yansu.id` | the `noticeId` |
| `yansu.hub`, `yansu.dev` | the hub id and the device id |
| `yansu.ct` | the sealed notice |
| `apns-collapse-id` | the notice `collapse` |
| `apns-priority` | `5` for `passive`, `10` otherwise |
| `apns-id` | a relay-generated id, not the notice id |

The fallback text is shown when the phone cannot decrypt the notice. It names no content:

| Moment kind | Fallback body |
| --- | --- |
| `decision_due` (older Macs only) | Isoform needs a decision. |
| `commitment_slipping` | A commitment needs attention. |
| `meeting_ended` | Your meeting notes are ready. |
| `meeting_prep` | A meeting is about to start. |
| `calendar_conflict` | Something on your calendar needs a look. |
| `meeting_followup` | A meeting left you follow-ups. |
| `person_reappeared` | Someone you know came up. |
| `goal_check_in` | A quick check on a goal. |
| `work_finished` | Work finished. |
| `work_blocked` | Work is waiting on you. |
| `learned_to_confirm` | Isoform learned something. |
| `capture_paused` | Isoform stopped watching for a while. |
| `brief_missed` | Your brief did not arrive. |
| `weekly_recap` | Your week is ready. |
| `schedule_missed` | A schedule did not run. |
| `suggestion` | Isoform has a suggestion. |
| `waiting_on_reply` | Someone is waiting on your reply. |
| `playbook_suggested` | Isoform can take a routine off your hands. |
| `playbook_result` | A playbook finished. |
| `topic_finds` | Something new on a topic you follow. |
| `chat_answer` | Isoform answered. |
| any other kind | Isoform has something for you. |

When a `weekly_recap` notice cannot be opened, the notification extension localizes its fixed fallback on
the device: “Your week is ready.” or “你的一周已准备好。” A `chat_answer` fallback is localized the same way:
“Isoform answered.” or “Isoform 已回复。” The decrypted title and body remain the Mac's copy.

### 14.5 Phone behaviour (notification extension)

- The extension derives `kNotice` from the shared session secret, opens `yansu.ct` with the
  AAD built from `yansu.hub`, `yansu.dev` and `yansu.id`, and shows the decoded title and
  actions. It shows the fallback body from section 14.4 when any step fails.
- It posts a `delivered` receipt once the push is handled, and an `opened` receipt when the
  user opens the notification.

### 14.6 Receipts (phone to relay)

`POST /v1/notices/{noticeId}/receipts`, authenticated with the device bearer. The device
needs the `push` scope.

```json
{"state":"delivered","action":"","at":"2026-10-10T18:00:12Z"}
```

- `state` is `delivered`, `opened`, `acted` or `dismissed`.
- `action` is the action id, or empty. An `acted` receipt must name its action. Other
  states may carry an action id when the phone has one.
- `at` is RFC 3339 UTC with second precision.
- For engagement measurement the hub treats a legacy `acted` button receipt as `opened`
  (`dismiss` as `dismissed`). Only the linked item's recorded completion within two hours
  of its first send produces an `acted` measurement. The relay wire format is unchanged.
- Success is `202 {"ok":true,"data":{"accepted":true}}`. A repeat is accepted and stored
  once, so the phone may retry freely.
- Errors: `400` for an invalid body; `401` without a valid bearer; `403` when the device lacks
  `push`; `404` for an unknown route or a `noticeId` that is not a UUID; `405` for a method
  other than POST; `429` under the device rate limit; the usual `413` and `415`.

The relay stores each receipt in `notice_receipts` (`notification_id` holds the `noticeId`,
with `device_id`, `state`, `action`, `at`, `received_at`, `forwarded`), unique on
`(notification_id, device_id, state, action)`. The relay does not check that the notice
exists; receipts are measurements, and the device rate limit bounds them. Receipts are kept
for 30 days.

### 14.7 Receipts (relay to hub)

Each newly stored receipt is queued to its hub as a relay frame:

```json
{"t":"receipt","dev":"<deviceId>","noticeId":"<uuid>","state":"opened","action":"","at":"2026-10-10T18:00:12Z"}
```

- The relay queues a receipt when it is stored. A receipt stored while the hub is away is
  queued when the hub next connects. The relay marks a receipt forwarded once it is queued.
  A receipt queued on a socket that drops before the write is not replayed.
- This frame is not sealed, and it is not an `in` frame. The relay must read `state` and
  `action` to store and deduplicate receipts, and an `h2d` envelope can only be made by the
  hub. The receipt carries identifiers, a state and a time, and it reaches the hub only over
  the hub's authenticated socket. The hub trusts the relay for receipts, not for notices.
- The hub accepts a receipt only when `dev` is a paired device, `noticeId` is a UUID, `state`
  and `action` follow section 14.6, and `at` parses. Other receipts are logged and dropped.
  Receipts are idempotent; a receipt may arrive again after a reconnect.

### 14.8 Vectors

`mobile-protocol/vectors-v1.json` carries `noticeSecret` (`S`), `kNotice`, and `notice`
with `id`, `nonce`, `plaintext` (the padded text) and `ct`. The nonce is fixed for the
vector only; the production sealer draws a random nonce for every notice. The vector's
plaintext is a release 2 `decision_due` notice; the kind is opaque to sealing, so the vector
stays valid.

## 15. Release 3: chat, corrections and one-time work

The phone is a client of the desktop's own chat sessions. A turn it starts runs through the
composer's path, and it reads projections of the stored stream items; it never sees the
desktop's internal event stream. Corrections and one-time work use the same stores the desktop
views use, so the phone gains no authority the Mac does not already have.

### 15.1 Scopes

| Scope | Grants |
| --- | --- |
| `chat:read` | list chats, read a chat, receive `chat_turn` events, report delivered/opened proactive cards |
| `chat:write` | create a chat, send a message, cancel a turn, answer a tool permission, choose a stored proactive card action, request proactive-card mute with `push` |
| `memory:correct` | reject a memory attribution, retract a Self directive |
| `handoff:create` | start one-time work |

A current phone requests eight scopes: the four of release 1 and the four above. Devices paired
earlier keep the scopes they hold; a route answers 403 with the scope name when one is missing,
and the phone shows its re-pair card.

### 15.2 Idempotency and receipts

Every POST carries `idempotencyKey` (1 to 128 bytes). The hub namespaces it as
`mobile:<deviceId>:<key>`, so two phones never collide. The hub stores the outcome of each
successful POST (status 2xx, with the route and the body) in `mobile_receipts`. A repeat with the
same key and route replays the stored status and body without running the action again. The same
key on another route answers 409. Failed answers (4xx and 5xx) are not stored, so a retry after
`busy` or a failed action runs again. Receipts are kept for 7 days and pruned at startup.

The action and its receipt are written in two steps, so a crash between them can repeat the action
once. Requests from one device are handled one at a time, so a repeat never runs beside its first
attempt.

### 15.3 Hub routes (over envelopes, paths relative to `/mobile/v1`)

Every route answers 403 without its scope, 400 for invalid input, 404 for an unknown id, and 503
when the Mac's store is not ready.

| Method and path | Scope | Body | Result |
| --- | --- | --- | --- |
| `GET /chats?limit=20` | `chat:read` | | `{"chats":[{"id","title","updatedAt","state","kind"}]}`, the main chat first, then newest first; limit 1 to 50 |
| `POST /chats` | `chat:write` | `{"idempotencyKey","title"}`, title at most 200 bytes (default `New chat`) | 201 `{"id"}` |
| `GET /chats/{id}?after=<seq>&limit=50` | `chat:read` | | `{"chat":{"id","title","updatedAt","state","kind"},"items":[...],"cursor","hasMore"}` (15.4) |
| `POST /chats/{id}/messages` | `chat:write` | `{"idempotencyKey","text","queuedAt"?,"undone"?}`, 1 to 16384 bytes after trimming; optional `queuedAt` and `undone` as below | 202 `{"turnId","seq"}`, `seq` being the stored user item (15.5); 409 `{"ok":false,"error":"busy"}` while a turn runs in the chat; 400 for invalid waiting fields |
| `POST /chats/main/messages` | `chat:write` | The same body as `POST /chats/{id}/messages`; the `main` alias sends to the computer's main chat when the phone does not list one yet, and the computer makes sure it exists first (15.5a) | 202 as below; 503 if the main chat cannot be prepared; 404 on a computer without `chatMainSend` |
| `POST /chats/{id}/offer-messages` | `chat:write`, `offers:read` | `{"idempotencyKey","text","offerReferences":[{"id","version"}]}`; same text limit, 1 to 16 references (20.6) | 202 as above; 409 if an idea changed or its sources are no longer readable; 400 for malformed references |
| `POST /chats/{id}/brief-messages` | `chat:write`, `brief:read` | `{"idempotencyKey","text","briefId","briefVersion","offerId"?,"cardId"?,"cardMessage"?}`; same text limit, `briefVersion` is the delivered `conversationKey`, optional `offerId` is an ID from that brief's `offers`, optional `cardId` an ID from its `cards` with `cardMessage` the card's words as shown | 202 as above (also for a stale `briefVersion` or `cardId` when the brief still has words; see below); main chat only, 404 for another chat; 409 `busy` or `This brief changed. Refresh and try again.`; 400 for malformed references |
| `POST /chats/{id}/brief-opener` | `chat:write`, `brief:read` | `{"idempotencyKey","briefId","briefVersion"}`, `briefVersion` the delivered `conversationKey` | 200 `{"posted":true}`; main chat only, 404 for another chat; 409 `This brief changed. Refresh and try again.` |
| `POST /chats/{id}/brief-paragraph` | `chat:write`, `brief:read` | `{"idempotencyKey","briefId","briefVersion","paragraph","text"?}`, `paragraph` is a zero-based narrative index, optional `text` the paragraph as the phone showed it | 200 `{"posted":true}`; main chat only, 404 for another chat; 409 for a changed brief, missing/empty/closing paragraph, out-of-range index or Closed Eyes; 400 for malformed input |
| `POST /chats/{id}/permissions/{requestId}` | `chat:write` | `{"idempotencyKey","optionId"}` | 200 `{"answered":true}`; 400 option not offered; 404 no such pending request; 409 no longer pending |
| `POST /chats/{id}/proactive/{momentId}/receipts` | `chat:read` | `{"idempotencyKey","state"}`, state `delivered` or `opened` | 200 `{"accepted":true}`; a card, digest child or linked status entry in the visible main chat |
| `POST /chats/{id}/proactive/{momentId}/actions` | `chat:write` and `push` | `{"idempotencyKey","actionId":"mute_kind"}` | 200 `{"state"}`; 403 any other action; turns off this notification kind |
| `POST /chats/{id}/proactive/actions` | `chat:write` | `{"idempotencyKey","momentId","actionId"}` | 200 `{"state"}`; 403 `mute_kind` (use the dedicated mute route with `push`); 400 action not offered; 404 unknown moment or chat is not the visible main chat; 409 action refused or retry key reused for a different choice; 503 permission result persistence unavailable, retry the same key |
| `POST /chats/{id}/cancel` | `chat:write` | `{"idempotencyKey"}` | 200 `{"cancelled":true}` or `{"cancelled":false}` when nothing ran |
| `POST /memory/{id}/reject-attribution` | `memory:correct` | `{"idempotencyKey","reason"}`, reason optional, at most 1000 bytes | 200 `{"removed":true}`; 404 unknown memory |
| `POST /self/directives/{id}/retract` | `memory:correct` | `{"idempotencyKey"}`, optional `expectedSelfEntryId` and `reason` | 200 `{"retracted":true}` |
| `POST /handoffs` | `handoff:create` | `{"idempotencyKey","title","brief","workDir"}`; title 1 to 200 bytes, brief at most 8 KiB, `workDir` empty or an absolute folder | 201 `{"id"}` (`hf_...`) |
| `POST /handoffs/{id}/start` | `handoff:create` | `{"idempotencyKey"}`; claims actionable work with the Mac's acceptance and verified-plan checks, then sends its prompt through the normal phone chat execution path | 202 `{"id","sessionId"}`; task becomes `in_progress` / `running`; 409 when not startable or chat send fails (claim rolled back); successful retries with the same key replay without starting twice |

The brief-message route is additive in protocol v1. Phones before backlog item
58 open the main-chat composer with an editable opener naming the displayed
brief; sending that draft, or tapping a final-paragraph offer, uses this route
with the displayed revision, and an offer includes its `offerId` and `prompt` as
`text`. From item 58 the phone taps as the computer clicks: a button or a row's
"Chat about this" puts the card's `message` in the main chat's box, unsent, and
Send uses this route with `cardId` and `cardMessage`. These brief offers do not
use the separate ideas feed's `offerReferences` or require `offers:read`. An
`cardId` (backlog item 64, additive) names one of the brief's `send` cards, a
button or a row's question (section 8, `cards`): the phone put that card's
`message` in its chat box, and `text` is what the user sends, edited or not. The
computer sends `text` as the visible user row with the card's sources read
again, naming the subject as it reads now. `cardMessage` is the card's words as
the phone showed them; `text` equal to `cardMessage` after the card's words
changed returns the 409 brief-changed message. A legacy `offerId` names the
brief's action card with that ID: the computer sends that card's current words
and ignores the phone's `text`. Either tap must carry the `briefVersion` it was
shown: a tap on another revision, or on a card that left the brief, returns the
409 brief-changed message, and the phone reads the brief again.
The computer reloads its stored brief and constructs the hidden brief/evidence
bundle through the same Today resolver and atomic send admission as the computer
action. It repeats `memory_read_provenance`, brief delivery, source synthesis and
Closed Eyes checks before publishing the user row. The phone never supplies
context or evidence. The visible user row contains only the edited text; the
turn retains phone tool permissions. Changed or withdrawn sources, a missing
brief, a stale revision or an unknown offer return the exact 409 message above
without dispatching or appending history. Accepted idempotent retries return the
original `turnId` and `seq`, including after the brief has changed. Existing
envelope encryption, schema version and cryptographic vectors are unchanged.

The brief-opener route (backlog item 58) is additive in protocol v1. It is the
phone's "Talk about this brief", as on the computer: the computer says the
brief's `opener` card (section 8, `cards`) as an Isoform row at the end of the
main chat, once (an identical last row is not repeated, and a retried key
replays its receipt), and starts no turn. The phone then opens the main chat
with an empty box tied to the brief, so the user's answer goes through
brief-messages with that `briefVersion`. The phone sends identities only; the
words are the computer's. A stale revision, a brief with no opener, or Closed
Eyes returns the 409 brief-changed message. Older phones never call this route
and keep their editable opener. A computer without the route answers 404; the
phone then says to refresh the brief from the computer, and calls the computer
offline only when no answer came.

The brief-paragraph route is additive in protocol v1. A paragraph tap sends
only the brief identity, revision and zero-based index; the computer reads its
delivered narrative, checks the revision, and posts the selected paragraph as
a quoted Isoform row in the main chat without starting a turn. It rejects the
closing question, empty text, an out-of-range index and Closed Eyes with the
409 brief-changed message. A computer without this route answers 404, which
the phone handles like brief-opener compatibility.

A brief that moved on does not strand the user's own words. When brief-messages
names a revision or card the computer no longer delivers, but the brief still
has written paragraphs and the message names no `offerId`, the computer answers
the text as an ordinary turn in the main chat, without the old brief as
context, and returns 202 under the same receipt. An offer, a brief withdrawn by
read policy, and Closed Eyes keep the 409. The phone shows a 409 or any other
refusal as "Not sent" with Try again, which sends the words as an ordinary
message; "Will send when your computer is back" is only for a computer that is
out of reach. After a 409, a paragraph tap asks once more with optional `text`,
the paragraph as the phone showed it: the computer quotes it only when its
current brief still has a paragraph with exactly those words at that index or
elsewhere, and otherwise keeps the 409, so a chat never quotes words the brief
no longer says. Older computers never receive `text`.

### 15.4 Chat lists, pages and items

Only visible sessions are listed: not hidden, and not under a hidden project or workspace. Cron and
other background sessions are hidden and never appear. `kind` is `main` for the one main chat the
user keeps talking in (background work reports into it as `system` items), `side` for a side chat
split off from the main chat, `handoff` for a chat whose id is a handoff's execution session, and
`chat` for every other chat. The main chat is always the first entry of the list, even when the
limit would otherwise drop it (the list then holds `limit` + 1 chats), so the Chat tab leads with
it and lists side chats under it. A phone that predates these kinds treats `main` and `side` as
unknown kinds and still opens them.
Titles are at most 120 bytes.
`state` is `idle`, `running` (a turn is in flight) or `waiting` (a tool permission is pending).

An item is `{"seq","role","kind","text","toolName","at"}`, with additive `id` on stored rows
and optional `permission`, `proactive` or `status` presentation data on the corresponding kind.
`id` is the stable storage identity; transient permission rows omit it. A client uses `id` to
replace a previously seen item whose sequence has changed, falling back to `seq` for older hubs:

| Stored kind | `role` | `kind` | `text` |
| --- | --- | --- | --- |
| `user` | `user` | `text` | the message |
| `assistant` | `assistant` | `text` | the reply |
| `system` | `system` | `text` | the status line |
| `system`, tool name `main_chat_proactive` | `system` | `proactive` | plain-text fallback for the proactive message |
| `system`, tool name `main_chat_status` | `system` | `status` | plain-text summary of the folded status row |
| `error` | `system` | `error` | the error |
| `tool` | `tool` | `tool_use` | one-line summary of the tool input: keys sorted, string values cut at 80 bytes, the whole at 240 bytes |
| `result` | `tool` | `tool_result` | the tool result |

Thinking and compaction rows are not sent. Text is at most 4096 bytes; a cut ends with
` [truncated]`.

An `error` item may add optional `code` and `assistant` strings, so the phone's error line
matches the computer's. `code` is the proven cause, one of `no_assistant`, `signed_out`,
`runner_unavailable`, `server` or `assistant_error`; `assistant` is the AI the line names
("Claude Code", "Codex" or the API key's provider), at most 120 bytes. Both are omitted when
unknown, and older phones ignore them and show `text`.

Main-chat pages add optional presentation fields. Older phones ignore unknown fields and keep
reading the existing chat, item, cursor and pagination fields. These additions do not create a
conversation or send a message. Other chats and transient permission items omit the additions.

| Field | Type | Meaning and omission |
| --- | --- | --- |
| item `rowClass` | string | C1's shared computer/phone classification: `conversation`, `waiting`, `finished`, `routine`, or `other`. Omitted when empty or outside the main chat. Routine rows fold by `localDay`; no stored row is removed. |
| item `topicIds` | array of strings | Stored topic memberships intersected with the current, gated and size-bounded `GET /topics` list (section 15.8). System-row memberships also pass C1's evidence read-time check. Omitted when empty, unavailable, outside the main chat, or under Closed Eyes. |
| item `localDay` | string | `YYYY-MM-DD` of the stored row's creation time in the computer's local timezone, shared with the computer's day folds. Omitted when empty or outside the main chat. |
| item `chips` | array of strings | C2's stored prepared requests, only on main-chat assistant rows: at most two strings, each at most 60 Unicode runes. Omitted when empty, unavailable, under Closed Eyes, or on other roles/chats. A chip fills the message box; only Send sends. Reads never generate chips. |
| item `askedAt` | string | RFC 3339 time of the user item the answer replies to, on the first assistant text item after each user item, so the phone can say "You asked at". Omitted on other items and when the question is not on the page. Also sent outside the main chat. |
| page `waitingCount` | integer | The latest completed waiting-state count across the main chat, including cards outside this page. Uses C1's shared rule: a non-routine proactive card in `pending` or `opened` state, with expiry and digest-child checks. Present even when zero when the current main-chat count is ready; omitted during resync, if unavailable, and on other chats. |

A message the phone queued while the computer was away (phone-connection v1.1.0 section 2) may add
two optional fields to `POST /chats/{id}/messages`. Older computers ignore them, so a phone still
sends the same key, text and order.

| Field | Type | Meaning |
| --- | --- | --- |
| `queuedAt` | string | RFC 3339 time the phone queued the message, at most five minutes ahead of the computer's clock. Present only on a message that waited. Chat messages never expire on the phone. The computer records the content-free `phone_chat.queued` and `phone_chat.sent_after_wait` `{wait_minutes}` events, and sends the answer as a `chat_answer` notice (14.4). |
| `undone` | integer | 0 to 100: how many waiting messages the person took back with Undo since the phone's last message. The computer records `phone_chat.undone` `{count}`. No text is sent. |

The answers to waited messages share one `chat_answer` notice per chat, with one collapse id. One
answer shows the start of the answer. Several show "Isoform answered the {n} messages you sent
while your computer was away." (你在电脑离开时发的 {n} 条消息，Isoform 都回答了。). The notice
`momentId` is the first answer's item `id`, so opening it shows Chat at that answer, and
`destination` is `chat` with the chat id. The batch ends when the phone reads the chat or after
12 hours. A message that did not wait gets no notice.

Routine classification is presentation only: folding neither deletes a saved item nor grants authority. Routine rows include `main_chat_status`, routine schedule API reports,
`brief_ready`, `capture_paused`, and the withdrawn `thread_update_unavailable` kind.
A retained muted passive card is also routine, except when it contains a `permission`
object or its kind is `decision_due`, `work_blocked`, `commitment_slipping`, or
`waiting_on_reply`. Those needs-you cards stay `waiting` while pending/opened and
unexpired, even if notification preferences are off or muted; canonical expiry and
terminal state still end the wait. Direct kinds retain their ordinary chat record and
a temporary kind mute alone does not mark them `kindMuted` in delivery projection.
The enabled switch and auto-pause still govern future delivery through the existing
pacing policy.

A pending/opened, unexpired digest is `waiting` when any child is waiting. A nonempty
digest whose children are all routine is routine itself; an empty digest, a digest with
only finished/other children, or a terminal/expired digest is `other`. Mixed digests keep
their needs-you children visible. Hide routine must never remove a pending permission
or another waiting needs-you card. Current mute display state may change on Resume,
while stored receipt state and expiry remain canonical.

`thread_update_unavailable` is a display-only replacement when an earlier thread
outcome can no longer be read. It carries localized unavailable-copy text and a Today
destination, with no action ids, draft, work permission, or source evidence. It is a
routine update, not a new engagement kind the phone can mute or execute. Its state is
`expired` ("No longer waiting"), never `pending`, so it never counts toward `waitingCount`.
The phone shows it as one quiet routine line, not a waiting card. This replacement does not
relax any source-read gate or alter the underlying saved item.

The 4096-byte text cap, default and maximum page limit of 50, 48 KiB escaped-item budget,
and 64 KiB envelope plaintext limit still apply. New item fields are measured before pagination;
structured payloads that exceed the item budget fall back to the existing bounded text.
Filtering and topic-tagged sends use the existing topic routes; no route is added here.

`proactive` is an additive structured message. Its fields are:

| Field | Type | Meaning |
| --- | --- | --- |
| `momentId` | string | Stable identity of the moment or digest. |
| `kind` | string | Engagement kind, `digest` for a bundled digest, or `thread_update_unavailable` for a withdrawn thread update (see above). |
| `headline` | string | One-line headline in the user's language. |
| `body` | string | Short message body in the user's language. |
| `draft` | boolean | Whether the proactive item contains a prepared draft. Display data, never permission to send. |
| `draftContent` | object, optional | The stored draft `{to, subject, text}` (all strings), plus server-owned `channel`, `recipient`, `principalId`, and `revision` when Send is available, marshalled with the proactive item when present. The existing delivery projection rechecks generated voice/source provenance and engagement read receipts, including Closed Eyes and source withdrawal; unreadable drafts are removed (`draft: false`, no `draftContent`) together with their derived copy. Invalid or oversized structured payloads fall back to bounded plain text. |
| `actionIds` | array of strings | Only actions implemented for this kind; never inferred permissions. |
| `actionLabels` | object mapping action id to string | Labels from the same localized Go copy catalog. |
| `target` | object | Trusted app destination, `{"kind","id"?,"runId"?}`; not a URL extracted from message text. |
| `evidence` | array of objects | Source references, each `{"kind","id","at"?,"target"}`. When known, `at` is an RFC 3339 timestamp. |
| `state` | string | Lifecycle and interaction state of this item, such as `pending`, `opened`, `acted`, `dismissed`, `deferred` or `expired`. `deferred` is a card-only outcome for Not now, not a notice receipt state. `expired` means a work permission ended or was replaced without an answer on this card. |
| `kindMuted` | optional boolean | The kind is off or auto-paused for chat delivery, or a non-Direct kind is temporarily muted. Direct kinds (currently `reminder`) ignore temporary kind mutes. This flag never resolves a card or hides pending work/permission actions. Retained passive cards can fold as routine; needs-you exceptions are listed above. |
| `items` | optional array of proactive objects | Digest children, at most five selected by score. |
| `localized` | optional object keyed by `en` and `zh-CN` | Display-only `{headline, body, actionLabels}` from the Go catalog. A renderer selects the current UI language, including after a language change; existing fields remain the fallback. |
| `work` | optional object | One-time work snapshot: `{"handoffId","status","summary","result","deliverables":[{"label","url"}]}`. The result and links are display data; the handoff id identifies the existing work. |
| `permission` | optional object | Running-work request: `{"requestId","sessionId","handoffId","toolName","details","approveOptionId","declineOptionId"}`. The request id identifies one pending permission; the option ids are the exact choices accepted by the existing permission endpoint. |

A sendable draft offers the existing proactive action id `send`. The phone shows the
full draft before enabling that action, then sends only the stored moment id and
action id through the established proactive-action route. The hub revalidates the
stored draft and source; draft fields are never execution authority. A completed
send also projects an additive `report` object on its main-chat receipt:
`{"source":"message_send","channel":"mail"|"slack","sentAt":milliseconds,"principalId"?}`.
The row retains its bounded text fallback for older phones. Accepted sends with an
unfinished local receipt and uncertain transport outcomes use a retryable 503, not a
definitive "not sent" result.

For example, the stored `main_chat_proactive` payload is projected into:

```json
{"seq":1,"id":"moment-brief","role":"system","kind":"proactive","text":"Your morning brief is ready.","toolName":"main_chat_proactive","at":"2026-10-16T18:00:00Z","proactive":{"momentId":"moment-brief","kind":"brief_ready","headline":"Your morning brief is ready.","body":"Open your brief when you have a moment.","draft":false,"actionIds":["open"],"actionLabels":{"open":"Open brief"},"target":{"kind":"today"},"evidence":[{"kind":"brief","id":"brief-2026-10-16","at":"2026-10-16T18:00:00Z","target":{"kind":"today"}}],"state":"pending"}}
```

Each known proactive kind adds `mute_kind` to `actionIds`, with "Mute this kind" and
"静音此类消息" in the corresponding localized labels. Older saved cards and digest children
receive the action when read. Digest wrappers have no kind to mute. The action turns the
kind's `engagement_prefs` switch off, records a `muted` receipt, and sets `kindMuted: true`.
The response returns the unchanged card state. It neither resolves the current card nor
approves or declines any pending work permission; clients keep needs-you actions visible
and disable the mute action. Retained passive messages may fold under the routine rules above.
Settings can turn the kind on again. The additive actions route accepts only `mute_kind`,
with both `chat:write` and the existing notification-preference `push` scope. Other actions
retain their established routes and scopes: the route cannot create one-time work, add a
schedule or answer a work permission, even when those actions appear on a card.

A phone reports `delivered` after rendering a card and `opened` when the card is visible in
its active chat. A digest reports each child separately. Reading a chat page alone does not
prove either observation. Both receipt routes and actions use the existing idempotency keys;
receipt identity also deduplicates retries with different keys. The hub uses the receipt's
phone channel and its own receive time, and rejects client claims of completion. A card's
interaction `state` is distinct from the engagement `acted` measurement: an approved work
permission can be terminal `acted` while the linked work has not completed.

One-time work reports use `work_finished` for completed work or work that needs review and
`work_blocked` for blocked or stopped work. They appear as durable proactive items in the main
chat, with the result summary in the existing text fallback. Review on the Mac appends the
saved result and deliverable links as an assistant message in that same chat. Reading the
main chat therefore includes both the report and the reviewed result; the work's execution
chat remains available as history. Replaying a report or Review does not append it again.

A running work's pending permission is a durable `decision_due` proactive item in the main
chat, with `approve` and `decline` action ids and the additive `permission` object above.
The existing `POST /chats/{id}/permissions/{requestId}` route also accepts the main chat id
for that request. Its `optionId` must be the saved `approveOptionId` or `declineOptionId`.
The hub resolves the owning runner's request through the same permission response path used
on the Mac; answering from either surface resolves the main-chat item and the needs-you card.
Repeated delivery of a request keeps its identity. Cached presentation data alone never
grants permission or revives a resolved request. Older phone versions ignore the additive
objects and continue to show the report and request details through `text`.
The execution chat's transient permission uses the same request id, and both chat paths
validate that exact request before answering, so a delayed answer cannot affect a later
permission from the same work.

`status` holds `{"period","entries":[{"id","kind","text","target"}]}`. The period identifies
the folded row; entries retain their own identity, localized text and trusted app destination.
Service-backed entries add an optional `momentId` linking the measured proactive message,
and optional `sentAt` (Unix milliseconds) preserving that entry's original durable chat time
when the folded row moves or a receipt write is retried.
Clients report receipts with the entry's `id`, not the folded row's id: `delivered` when
rendered, `opened` once the expanded entry is visible. Entries without `momentId` summarize
other events (such as digest completion) and do not create another measured message.
It is one expandable tertiary status line, not an assistant bubble. Routine capture pauses under
60 minutes, a single schedule miss, a missed brief, finished digests and
playbook results use this presentation.
Folding keeps one stored row and its stable `id`, but atomically moves it to the newest `seq` and
updates `at`. A read after the previous cursor therefore receives the latest status. Clients
replace the old snapshot by `id` before sorting by `seq`. The iPhone decoder, transcript merger
and cache preserve this stable identity. Phone versions predating stable-id support still read
the text, but
can retain both snapshots during incremental reads until a fresh transcript replacement (for
example after reopening the chat). Proactive action-state updates retain their existing `seq`;
clients fetch the newest page to re-read those revisions.

The phone asks for the owner's Face ID, Touch ID or passcode before a proactive action, then
posts the stored `momentId` and offered `actionId` through the chat action route in section 15.3.
The Mac verifies that the chat is the visible main chat and the moment belongs to it, including
digest children, and delegates to the same `ResolveMainChatProactiveAction` path as the desktop.
The phone cannot supply a destination or a new action. The returned `state` is `opened`, `acted`,
`dismissed` or `deferred`; a refusal leaves the card available and shows its reason inline.
An unanswered write keeps its idempotency key across relaunch, and the phone waits for that
choice's result before submitting a different choice on the same card. The exact `momentId`
travels in JSON because stored work identities can contain paths, spaces and Unicode; it must
be nonempty and at most 48 KiB in UTF-8, matching the chat page budget. For this route, the
receipt identity includes a hash of `[momentId, actionId]`, so reusing a successful key for
another card or choice answers 409.
A new key for a card already `acted`, `dismissed`, `deferred` or `expired` also answers 409;
validation and execution share the desktop action lock, so simultaneous choices cannot claim
each other's result. Only replay of the original successful key returns its saved success.
Stored work permission actions (`approve` and `decline`) retain the exact live request identity
and the same permission response boundary as section 15.3's permission route.
Once the runner accepts a permission choice, the hub saves its success receipt independently
of the chat-card update. A failed presentation write still returns 200 `acted`; later chat reads
repair the card, and the original key replays its saved success even after the hub restarts.
If saving the receipt itself fails, the route returns 503 rather than a refusal. The phone retains
its key. While that hub process remains alive, it remembers the accepted request, option and
device-scoped retry identity even after the runner closes.
The card stays pending and its original action stays available until that receipt is durable;
reads and replayed runner events cannot publish a terminal card that strands the retry.
Retrying that same choice persists and returns the accepted outcome without answering the runner
again; a different key or choice remains 409. The runner response and receipt storage are separate
systems: a computer-process crash before the success receipt becomes durable cannot be recovered
as a confirmed success. A historical permission card never authorizes a new runner response.
The retired `POST /decision` route remains unavailable.

Both new kinds always retain `text`. Clients that do not recognize their kinds or optional
fields render that text as a system line; existing Swift decoding already ignores unknown fields
and maps unknown kinds to its fallback case. Missing or malformed structured data is projected
as kind `text`. If the structured item would exceed the page byte budget after JSON escaping,
the hub also sends only the bounded text fallback so pagination can still advance.

Status entries may also carry `localized`, an optional map from `en` and `zh-CN` to the
localized status text. Existing rows are hydrated from their saved catalog sentence at the
presentation boundary; if its parameters cannot be recovered, another language uses generic
copy. Hydration does not read mail or any other source. Original `text` remains available to
clients that ignore the additive fields.

Structured payloads are presentation data, not instructions or authority to execute. Mail subjects
and drafts never enter model prompt history; a waiting-on-reply item keeps its generic chat copy.
The hub redacts proactive text and rejects sensitive-app content before storage. No mail-body
read, draft job, new action, or additional source is enabled by this protocol extension.

`seq` is the stored sort order plus one, so `after=0` means from the start and `after` is
exclusive. Without `after` the newest page is returned. A page holds at most 50 items and at most
48 KiB of escaped JSON, because the sealed plaintext of section 4 is limited to 64 KiB and the body
is escaped again inside it. When the budget or the limit stops a page, `hasMore` is true. Continue
with `after=<cursor>`. `cursor` is the `seq` of the last row the page consumed, including rows that
are not shown, so a phone continues from `cursor`, not from its last item's `seq`.

The execution chat's tool permission prompt is transient session state on the Mac. While it is pending, the page
that reaches the newest row ends with one transient item: kind `permission_request`, `seq` one past
the newest stored item, and `permission` `{"requestId","options":[{"id","label"}]}`. The request id
is new for each request. Transient items are never part of `cursor`.

### 15.5 Sending, events and the Mac's own rows

A send first stores the user's text as a `user` item, then runs the chat the way the composer does:
the same work folder, model, plan mode and thinking flag as the chat's own session. The route
for the turn is the chat's own (`chat`, or `handoff` when the chat runs an in-progress handoff). A
send to a chat that is already running answers 409 `busy` and stores nothing.

Source-derived Ideas messages use `offer-messages`, never a text-only fallback. Before storing
the user item or dispatching it to the model, the computer resolves each reference under its
current read policy and verifies the version (section 20.6). It checks the complete edited text
against the mail hold, then redacts credentials. A refusal creates no accepted user item or
model turn. The final source check runs while the prospective row is uncommitted and invisible
to chat readers; rejection rolls back that insert, without deleting a published row. The final
read captures the activity database revision before traversal, checks it after the last source
and mail read, and reserves that database through the chat commit. Main-store policy writes
serialize with the chat transaction; file/policy publication barriers and file digests keep
the source graph stable through publication.

Successful rows keep `offerReferences` and a server-generated `offerSources` graph in the
existing `automationContext` JSON. The graph records identifiers, provenance digests and topic
search evidence; it grants no execution authority. It survives offer dismissal, expiry and
replacement. Model history and phone chat reads recheck those stored dependencies against
current read policy, independently of the current waiting-offer list. Missing or malformed
source proof fails closed. A withdrawn source withholds its user row and all later rows that
may depend on it, including assistant/tool text and compaction summaries. Phone pagination
still advances across those hidden rows and withholds dependent permission prompts.

Before runner dispatch, source-bound history and the current prompt are checked again, including
after deferred prompt composition. Source-bound conversations use the native transport, whose
read gate runs immediately before each model request, retry and compaction request. External
assistant CLIs cannot enforce that gate after process startup and are excluded for these turns.
History checks include decoded JSON tool arguments (rejecting duplicate decoded keys) and live
privacy settings that have not yet been persisted. If access changed or current redaction changes old context,
the chat refuses continuation because a resumed provider may still retain that context. This
release has no automatic context-reset flow. Already delivered text is not remotely erased from
offline phone caches or provider requests; these gates control subsequent reads and dispatch.
The ordinary `messages` route also enforces these checks when references are supplied, but the
phone must use the dedicated route: an older computer that ignores unknown JSON fields must
answer 404 instead of accepting source-derived text as an ordinary message.

The 202 names the stored item's `seq`, so the phone ties its message to that row without comparing
text, even when the row is older than the page it reads; a replay under the same key answers the
same `seq`. `seq` was added after release 3 shipped: a phone treats it as optional, and an older
phone ignores it. The phone shows its message as Sent from the 202, and as Seen once a `chat_turn`
event carries its `turnId` or a stored row other than a status line follows its `seq`.

The assistant, tool and error rows of a turn are stored by the Mac's chat view, as for every chat.
The phone reads them from storage, so a reply appears once it is stored, a moment after the turn.
The hub keeps its own copy of a phone turn's rows while the turn runs; when no chat view stored
anything for the turn by the time it settles (the app is still starting, or no window was ever
mounted), the hub stores that copy before it sends the completion event, so the phone's re-read
always finds the reply. A turn a chat view did store is left alone, so no row is stored twice.

`chat_turn` is an h2d `event` envelope, sent to devices that hold `chat:read`:

```json
{"event":"chat_turn","chatId":"<id>","turnId":"<id>","state":"running","seq":18,"at":"2026-10-10T18:00:12Z"}
```

- While a turn streams, `state` is `running`, `permission` or `error`. Events are coalesced to at
  most one per chat per second, with one trailing event at the end of the window.
- When the turn closes, one final event carries `done` or `error`. It is sent two seconds after the
  close, so the rows of the turn are stored before the phone reads them.
- `seq` is the newest stored `seq` when the event is sent (0 for an empty chat). No content travels in an
  event. The phone re-reads `GET /chats/{id}?after=<cursor>` on each event and after a reconnect.
- The relay keeps the newest 200 h2d envelopes per device (section 5). A phone that stops polling
  for a long streamed turn can lose its oldest events; it re-reads the chat after any gap.

The hub listens to the agent event stream in process (`App.onAgentEvent`), so the desktop's own
event path is unchanged.

### 15.6 Corrections and one-time work

- A memory rejection removes the memory the way the Mac's memory view does.
- A Self directive retraction reads the current Self entry on the Mac when the phone does not send
  `expectedSelfEntryId`. A missing `reason` is stored as `Retracted on iPhone.`.
- One-time work is stored as a handoff with status `active` and source `mobile`. It waits in
  One-time work on the Mac and does not start until the user starts it there. The phone starts
  it from the Chat tab.

### 15.7 Test seams

`mobileChatRunnerOverride` answers a turn without a model, and `mobileMemoryRejectOverride` replaces
the memory store. Both are nil in production. The relay harness (`mobile_e2e_test.go`) installs them
to exercise the chat routes end to end through the relay binary.

### 15.8 RC 1: phone topics

These additive routes keep protocol version 1. `GET /status` includes
`"topics":true` (optional boolean, omitted when false). This is route support,
not current source availability or a new grant. Older phones ignore the field;
older computers omit it and answer 404 on these new routes. The computer ships
first. A phone treats an absent flag or route 404 like brief-opener compatibility;
it must not silently send an untagged message to another route.

Paths below are relative to `/mobile/v1`, over the existing encrypted request and
response envelopes. Pairing, expiry, device authorization and rate limits are
unchanged. No new scope or phone owner check (Face ID, Touch ID or passcode) is
introduced for composing a message in the user's main chat, including the RC 1
"Send the reply" entry point. Existing permission and execution gates remain.

| Method and path | Scope | Request | Result (`data` inside the normal response) |
| --- | --- | --- | --- |
| `GET /topics` | `brief:read` | no body | 200 `{"topics":[topic],"refreshing":true,"hasTopics":true}`; optional flags described below; empty array when no readable topics or under Closed Eyes |
| `GET /topics/{id}?cursor=&limit=50` | `chat:read` | opaque cursor, empty for newest page; limit 1 to 50, default 50 | 200 `{"sessionId","topicId","items":[item],"hasMore","nextCursor"}` |
| `POST /topics/{id}/messages` | `chat:write` | `{"text":"Review this release","idempotencyKey":"<key>"}` | 202 `{"turnId":"<uuid>","seq":123}`, the existing chat turn result |

Topic detail requires `chat:read`, the same as `GET /chats/{id}`, because it
returns main-chat transcript, not just brief/topic summaries. This is the hub's
Round 1 scope decision, superseding the plan's `brief:read` detail assumption.
A phone holding only `brief:read` can load the topic list (200), but detail
answers 403 until it holds `chat:read`. Sending still requires only `chat:write`;
these are existing scopes, with no new grant type.

The list's optional `refreshing` boolean means the computer is still building
its full topic views. An empty bootstrap is pending, not an authoritative empty
store. The optional `hasTopics` boolean means non-removed retained topic records
exist even if their labels are currently withheld. Neither flag authorizes a
source read or chat send. False flags may be omitted; older computers omit both
and newer phones default them to false (presence also follows nonempty topics).
Under Closed Eyes both flags are false and the phone list is empty. Older phones
ignore the additive flags. Protocol version and scopes remain unchanged.

The phone keeps the Feed topic section visible while `refreshing` is true,
including an empty bootstrap, and re-reads every two seconds while that Feed is
visible until a completed publication is returned. Completed known-but-withheld
topics offer Retry; authoritative empty results after last-topic removal hide
the section. Leaving the Feed cancels its follow-up task; unpair clears local
pending/presence state and fences late responses. No topic publication push is
required and the relay sees only the existing encrypted requests/responses.

The list preserves `GetTodayTopics` order: work projects with their status,
latest, next and people, then followed topics with their existing reason and new
story count. Each topic has stable `id`, `kind` (`project` or `following`), `name`,
`status`; optional `latest` and `next` are `{"text","at"}` (`at` optional).
Optional `peopleLine` is `{"names":["name"],"count":3}`; optional `why` is
`{"lastSeenAt","interestWhy"}` (`interestWhy` optional); `newStories` is omitted
when zero. Status codes are the registry's existing `waiting_on_you`, `today`,
`new_stories` and `moving`. Source refs, source counts and sidebar pins are not
sent. At most 12 topics are included. Names are bounded to 160 UTF-8 bytes;
latest/next text and the following reason to 384; at most three people names,
each 96; timestamps to 64. Truncated text includes ` [truncated]` within its byte
bound. If JSON escaping still exceeds the page budget, topics are removed from
the end, preserving the order of the retained prefix. No topic id is rewritten.

Detail is the main chat filtered by that topic, never a separate conversation.
Items have `id`, `kind` (`event` or `message`), and `at` (Unix milliseconds).
Events have optional `actor` (stable role code, bounded to 64 bytes) and `text`
(bounded to 4096 bytes). A message's `message` object uses the existing phone
chat item projection from section 15.4 (`seq`, `id`, `role`, `kind`, `text`,
`toolName`, `at`, and optional structured presentation). Internal thinking,
compaction, attachments, raw tool input, executor and automation metadata are
excluded. Text uses the same truncation and tool summaries as ordinary phone
chat. Rows on each page are oldest first (newest last). Prepend older pages;
when `hasMore` is true, request `nextCursor` to continue strictly before this
page. Cursors belong to one topic and advance through hidden rows too; a page
may therefore be empty while still having more. Never derive a cursor from
visible items or use it with another topic.

Both reads fit one message envelope: the complete normal response is measured
with the second JSON escaping layer used by the encrypted response's `body`
string, against the existing 48 KiB chat page budget. This leaves room for the
UUID/status wrapper under the 64 KiB plaintext limit (section 4). Detail retries
the same cursor with a smaller limit (halving, rounded up) until it fits. If a
single structured item uses that budget, it falls back to its bounded plain
text representation, retaining its cursor. Pages never skip an oversized row.

Topic ids obey the existing phone id/path bounds (1 to 128 printable ASCII
bytes, no slash, and the envelope path character rules). Invalid or unknown ids,
and topics whose sources are no longer readable, answer 404. Missing scope is
403. Invalid cursor or limit is 400; cursor errors carry `TOPIC_CURSOR_INVALID`.
Closed Eyes refuses detail and new sends with 409 and the desktop's
`TODAY_CHAT_OBSERVATION_PAUSED` code. Busy sends, changed sources or a changed
read revision answer 409 and require refresh/retry; unavailable stores answer
503. Send text is 1 to 16384 bytes after the existing nonblank check; JSON and
idempotency validation/errors are the same as ordinary chat send (15.2, 15.3).
If even one item cannot fit after the bounded plain-text fallback, detail
answers 413 rather than skipping that row or advancing its cursor. Other
failures answer 500 without internal error details; send failures use the
ordinary chat message error, `the message could not be sent`.

Reads reuse the computer's source/privacy gates, including sensitive apps,
private browser windows, protected/deleted sources, and current provenance.
The list hides these topics. Detail rechecks sources on every page, including
when saved messages remain tagged after withdrawal. Reads retain the existing
source revision/publication fences through phone projection and response
assembly; a concurrent withdrawal refuses the response. Opening a topic by id
refreshes the registry even when the phone has not first loaded the list.

Sends use the same topic admission as `SendMainChatWithTopic`, preserving phone
executor origin and the existing main chat's admission, cancellation and turn
events. The persisted user bubble contains only the user's words, with the topic
association and provenance stored by the computer. Receipts use the existing
15.2 helper and device namespace: repeating the same key and route returns the
same 202 result, including after reconnect/restart or source withdrawal, without
sending again; another route with the same key answers 409. The existing
seven-day receipt retention and action/receipt crash window remain unchanged.
For topic sends, receipt lookup, admission and receipt persistence are serialized
under the send lock, so a retry arriving during admission checks the saved
receipt after waiting and replays the first send's accepted result.
A replay is an acknowledgement of accepted intent, not permission for new work.

## 16. Notification controls

The Mac decides what to reach out about (section 14 carries the notices). The phone can read and
change the switches the Mac keeps, one per kind, so Settings on the phone and on the Mac show the
same settings. All notification routes need the `push` scope.

`GET /notifications` answers the Mac's view:

```json
{
  "available": true,
  "kinds": [
    {"kind": "work_blocked", "enabled": true, "defaultOn": true, "paused": false,
     "sent": 2, "opened": 1, "acted": 1, "dismissed": 0, "muted": 0},
    {"kind": "goal_check_in", "enabled": false, "defaultOn": false, "paused": true,
     "mutedUntil": "2026-10-11T00:00:00Z", "sent": 20, "opened": 0, "acted": 0, "dismissed": 13, "muted": 0}
  ],
  "level": "normal",
  "quietStart": "22:00", "quietEnd": "08:00", "dailyInterruptBudget": 6,
  "digestTimes": ["08:30", "13:00", "18:00"], "statsDays": 30
}
```

- `kinds` lists every kind the Mac knows, in the Mac's order; a phone shows an unknown kind by its
  key. `paused` means the kind paused itself after most deliveries were dismissed or muted; turning
  it on ends the pause. The counts cover the last `statsDays` days. The phone exposes the four
  first-run kinds: `work_blocked`, `work_finished`, `brief_ready`, and `capture_paused`.
  The daily budget and digest times remain read-only on the phone.
- `level` is the Mac's intensity dial: `off`, `fewer`, `normal`, `more`, or `custom` once a switch or
  rule was changed by hand (`docs/notifications-design.md` section 3). It is read-only on the phone
  and absent from Macs older than this field.
- RC 1 A4 adds `deliveryMode` (`normal` or `quiet_on_today`), `quietOnToday`
  and `quietAllowed` plus `quietSuggestionAllowed` to each kind. Older computers omit them; older phones
  ignore them. `decision_due` (also used for permission requests) cannot be
  quieted. A quiet kind records sent receipts on `today_quiet`, stays in the
  30-day counts, and sends no computer toast, phone notice, text, email or main
  chat report. Existing switch and quiet-hours writes preserve this mode;
  quiet deliveries do not spend or wait for notification pacing allowances.
  A reminder the user set can be explicitly quieted, but has
  `quietSuggestionAllowed: false`. Quiet delivery uses the same item-completion
  attribution window of two hours. This phase adds no phone route to change it. See
  [the computer binding contract](../docs/rc1/quiet-lines-api.md).
- `503` while the Mac has not started the engagement service yet.

`POST /notifications/{kind}` with `{"idempotencyKey": "...", "enabled": true}` turns one kind on or
off and answers the whole view again. It is replayed from the device's receipts like every other
write (section 15.2); an unknown kind is `400`.

`GET /status` also answers `keepAwake` (the saved keep-awake switch), `instantPush`
(true when the computer's relay can deliver notices at once) and `briefSchedule`: the scheduled brief
slots due in the next 24 hours, as `[{"slot":"morning","at":"2026-10-28T07:00:00-07:00"}]`, at their
slot times. The computer starts each build 10 minutes before its slot; the stated time stays the slot.

`GET /settings/keep-awake` answers `{"on": bool, "pairedPhones": int, "onPower": bool, "hasBattery": bool}`:
the saved switch, how many phones are paired, whether the computer is on AC power now, and whether
IOPSCopyPowerSourcesList contains an internal battery. The computer holds a PreventUserIdleSystemSleep assertion only while `on` is true, at least one phone is paired, Low Power Mode is off and the computer is on AC power. On battery it never holds the computer awake, whatever the charge; it releases the assertion when the computer goes to battery, when the setting is turned off, when the last phone is removed, and on quit. Power-source changes are observed through IOPowerSources events, and Low Power Mode through NSProcessInfoPowerStateDidChangeNotification on the same background run loop, without polling. The display can sleep and a closed laptop still sleeps. The setting is off until the owner turns it on, including for a phone paired later; removing the last phone turns it off and saves that. A `keep-awake.json` with no `on` key reads as off (a change from earlier builds, which were on once a phone was paired).
`POST /settings/keep-awake` takes `{"on": bool}` and answers the same shape; `on` is required (`400`
otherwise). Both need the `push` scope.

`POST /notifications/preferences` changes quiet hours with
`{"idempotencyKey":"...","quietStart":"22:00","quietEnd":"08:00"}` and answers the same complete
settings view. Both times are required, exactly `HH:MM` from `00:00` to `23:59`, in the computer's
local time zone. Equal start and end disables quiet hours. Unknown fields, malformed times or a
missing key return `400`; missing `push` scope returns `403`; an unavailable engagement service
returns `503`. The route updates the existing engagement rules atomically without changing the
interruption budget, digest times, kind switches, weights or mutes. It uses the same per-device
receipt replay as kind switches: once its receipt is saved, a replay returns the original response
without overwriting newer computer settings. As with the existing kind route, the rule write and
receipt save are separate transactions. A computer crash or receipt-save failure between them can
allow a later retry to apply the same values again; this route does not add a stronger transaction
guarantee.

The phone saves kind/quiet-hours edits and their original idempotency keys before sending,
scoped to the pairing. Its screen shows pending changes, retries on launch, foreground and refresh,
and clears the queue on unpairing. The permission explanation reads the real enabled values rather
than claiming defaults. With no saved settings it says the computer's choices are unavailable.

The phone's **Alerts** switch controls push registration for that phone (section 9); it does not
reset the computer's notification preset or other devices. Turning it off unregisters locally at
once and retains a pairing-scoped pending relay-token removal when offline, retrying on launch or
foreground. Turning it on uses the existing permission and token-registration retry path. No
notification credentials or APNs tokens are added to the preference queue.

### 16.1 RC 1 A5 ranked notification statistics

The existing `/notifications` responses add these fields; older phones ignore
unknown fields and older computers omit them. No scope or write route changes.

- `kinds` includes the 21 active catalog kinds in catalog order, including
  zero-send kinds with their effective switches. Retired `meeting_ended` and
  `meeting_followup` stay absent, preserving the existing Settings page.
- `channels` is a distinct, ordered array of destinations actually recorded by
  sent or delivered receipts in the 30-day sent cohort: `computer`, `phone`,
  `text`, `email`, `today_quiet`. Transport `desktop` maps to `computer` and
  `push` to `phone`; bookkeeping channels are excluded. An unsent quiet kind
  has an empty array, rather than invented delivery evidence.
- `sampleCopyKey` resolves through `engagement.NotificationSampleCopy` to the
  most recent sent notice's real generic `PushBody` in `en` or `zh-CN`. It is
  normally the catalog kind key; `decision_due.activity_thread` selects the
  existing unfinished-Activity-thread variant instead of the permission line.
  Storage tests only the presence of that code-owned variant marker in the
  latest sent row; no marker value, personal parameter, observed text or
  rendered personal copy is returned. It is empty without a send in the
  window. The UI resolves/localizes the key; it must not display the raw
  identifier as prose.
- `quiet` mirrors `quietOnToday`. `canQuiet` mirrors `quietAllowed`: the user
  may explicitly choose quiet for any active kind except `decision_due`,
  including reminders, `commitment_slipping`, `waiting_on_reply` and
  `work_blocked`. `quietSuggestionAllowed` is separate: Isoform must never
  suggest quieting those four needs-you kinds or a user-set reminder.
  Overview's quiet fixes honor this flag and omit kinds already quiet.
- Per-kind `notificationSent`, `notificationOpenedOrActed`, and
  `notificationActed` count the interruption receipt cohort only; they feed
  every tag threshold, attention rate and Overview noisy-kind hint, including
  the hint's displayed count, ordering and dismissal signature. `quietSent`
  and `quietOpenedOrActed` expose quiet-line history separately. Quiet-only
  history cannot produce a Noisy/Ignored hint when a user returns to normal
  delivery; quiet completions also cannot hide real interruption noise.
  Legacy per-kind `sent`, `opened`, `acted`, `dismissed`, `muted` lifecycle
  totals retain their all-channel meaning. The new per-kind bucket fields are
  additive and may be absent on older computers.
- `tag` is a localization token. Quiet kinds get `quiet`. Kinds with no
  interruptions otherwise have `""`, even if they have quiet-line history.
  All thresholds below use interruption counts only. When its acted rate is
  below 50%, `decision_due` gets
  `needs_you` ahead of `noisy` or `ignored`. Remaining priority: `noisy` for
  at least 20 sends with no opens or completions; `ignored` for at least 5
  sends with a distinct opened-or-acted rate below 10%; `needs_you` for the
  other needs-you kinds when `acted / sent < 0.5`; `rare` for 1 to 4 sends;
  otherwise `""`. A notice both opened and completed counts once toward the
  distinct attention rate. A Noisy/Ignored tag alone never authorizes a quiet
  suggestion: callers must also check `quietSuggestionAllowed`.
- `summary: {sent, openedOrActed, quietSent, quietOpenedOrActed}` totals the
  same active-kind 30-day cohort. `sent` counts distinct moments with an
  external notification sent/delivered receipt (`desktop`, `push`, `text`,
  `email`); `openedOrActed` is the distinct attention union for those moments.
  `quietSent` and `quietOpenedOrActed` are the corresponding counts for
  `today_quiet` receipts. Quiet-only lines never increase `sent`. These
  buckets follow actual receipts, independent of current quiet preferences;
  a moment reached in both modes counts once in each bucket. Existing
  per-kind lifecycle counts and two-hour completion attribution are retained.


## 17. Attachments

An attachment is a file the hub wants its phones to have, today the spoken brief
(`docs/notifications-design.md` section 7). The hub seals the file once with a random
per-file key and uploads the ciphertext; the key travels inside the brief document, which
is already sealed end to end for each device (section 8, `audio.attachment`). The relay
stores and serves the bytes as given and never opens them.

### 17.1 Sealing

```
key   = 32 random bytes, nonce = 12 random bytes, drawn per attachment
aad   = "yansu-attachment-v1|" + hubId + "|" + attachmentId          (ASCII)
body  = ChaCha20-Poly1305(key, nonce, plaintext, aad)                 (ciphertext || 16-byte tag)
```

- `attachmentId` is a lowercase UUID version 4 drawn by the hub.
- The brief carries `{"id": attachmentId, "key": base64url(key), "nonce": base64url(nonce)}`,
  both unpadded. A phone opens the body with that key, nonce and the AAD built from its hub id
  and the attachment id; any failure means the phone shows the text brief and no player.
- The plaintext is at most 16 MiB minus the tag. A larger file is not sent.

### 17.2 Routes

| Method and path | Auth | Scope | Body / result |
| --- | --- | --- | --- |
| `PUT /v1/hub/attachments/{attachmentId}` | `X-Yansu-Hub: <hubId>` and `Authorization: Bearer <hubCredential>` (the hub socket's credential) | | body `application/octet-stream`, at most 16 MiB → `201 {"id","bytes"}` |
| `GET /v1/attachments/{attachmentId}` | device bearer | `brief:read` | the stored bytes as `application/octet-stream`, `Cache-Control: no-store`; 404 when unknown, expired or owned by another hub |

- The upload route never creates a hub: the hub must already have connected its socket, and it
  must have a live paired device holding `brief:read`, otherwise nothing is stored. The body is
  streamed to disk under the size limit, never read into memory.
- Upload errors: `401` unknown hub id or wrong credential; `403` no live device with `brief:read`;
  `404` malformed id; `405` other methods; `409` the id belongs to another hub; `413` empty body,
  over 16 MiB, or alone over the hub's byte quota; `415` wrong content type; `429` with
  `Retry-After` over 12 uploads an hour per hub or 30 per client address; `507` when the relay's
  attachment storage is full. A repeat upload under the same id replaces the body.
- Download errors: `401` without a valid bearer; `403` without `brief:read`; `404` as above;
  `405` other methods; `429` under the device rate limit. Range requests are honoured.
- Retention and quotas: an attachment expires 14 days after upload; a hub keeps at most 28 and at
  most 96 MiB, its oldest going first to make room; the whole relay keeps at most 2 GiB and refuses
  beyond that rather than evicting another hub. Unpairing a device changes nothing here, because
  attachments belong to the hub.

### 17.3 Hub and phone behaviour

- The hub uploads the spoken brief when a paired device holds `brief:read` and the relay is
  connected; otherwise the audio stays on the Mac and the brief carries no `attachment`. The
  upload is not retried later for that brief.
- The phone fetches the attachment when the user presses play on the Brief tab, opens it, keeps
  the decrypted file in its cache beside the brief (same 14-day retention), and plays it. It never
  fetches attachments in the background.


### 17.4 Phone playback from the authorized bridge (additive, 2026-10-17)

`GET /briefs/{briefId}/audio?version=<key>` is an inner hub route, carried by the
same encrypted request/response envelopes as `GET /briefs/{briefId}`. It requires
`brief:read`; an expired device authorization remains `401`, and a missing scope
is `403`. The computer checks current source-read policy before returning any
attachment key, including after an upload. The route never synthesizes audio,
installs a voice, or starts a new brief.

The mandatory version key is the exact stored `briefId`, a literal `|`, and the
exact stored `asOf` string, URL-encoded as one query value. For example,
`2026-10-17-morning|2026-10-17T07:00:12Z`. It identifies the generated recording,
not the envelope delivery sequence or the schema's integer `version`. Audio
completion and refreshed live rows preserve this key; regenerating the same
brief ID with a new `asOf` changes it. Empty, repeated or overlong version values
are `400`. The success response uses the normal hub wrapper:

```json
{"ok":true,"data":{"briefId":"2026-10-17-morning","version":"2026-10-17-morning|2026-10-17T07:00:12Z","audio":{"status":"ok","mime":"audio/mp4","bytes":123456,"durationSeconds":95,"attachment":{"id":"<uuid>","key":"<base64url>","nonce":"<base64url>"}}}}
```

The response has `Cache-Control: no-store`. The phone downloads the ciphertext
once through section 17.2, authenticates and opens it using section 17.1, and
keys its decrypted app-container cache and resume position by hub identity and
this brief version. Neither raw audio nor base64 audio is placed inside the
64 KiB bridge response. The computer's private podcast-feed URL is never used.

Failures use `{"ok":false,"data":{"reason":"<code>"},"error":"<code>"}`:

| Status | Code | Meaning |
| --- | --- | --- |
| `409` | `version_changed` | The stored brief has been regenerated; refresh its text before requesting audio. No replacement recording is returned under the requested key. |
| `404` | `voice_not_installed` | The computer has no installed voice or the voice is downloading. |
| `404` | `audio_rendering` | The requested brief has no completed audio metadata yet. |
| `404` | `audio_unavailable` | The brief or deliverable recording is absent, withdrawn by current read policy, invalid, or cannot be uploaded. |

A disconnected computer is a transport failure, not a `404`; the phone can play
its last cached recording offline and otherwise explains that the computer is
offline. Unknown audio status values never imply that playback is ready.

The original upload during generation (section 17.3) remains supported. This
route additionally uploads an already generated local recording on demand if
pairing or reconnection happened later. New recordings store optional
`audio.sha256` (lowercase hex SHA-256 of the plaintext) to bind their bytes to the
generation. A late upload takes a bounded immutable snapshot, verifies its
length and digest, and rechecks version and source-read policy before storing
or returning its key. Late preparation runs in the background and immediately
returns `404 audio_rendering`; the phone's next play request receives the stored
descriptor when ready. Concurrent render/upload work is serialized and duplicate
preparations are coalesced without waiting. The background upload is bounded by
90 seconds and cancelled with the bridge or brief service; it never blocks the
serial device request worker or another chat/status request. A failed preparation
returns `audio_unavailable` on the next play request, then permits another retry.
The normal bridge response deadline remains unchanged. An old
local-only recording without this digest fails closed as `audio_unavailable`
until a new brief is generated; an already sealed older attachment still plays.
Uploading an earlier brief never changes which brief is latest.

The phone does not synthesize, and downloads only after the user chooses play.
It keeps at most the last recording, capped at the attachment plaintext limit,
under file protection and excluded from backups; unpairing or changing computers
removes it with the other brief caches. Its playback session uses the background
playback audio category, publishes title **Your brief** and artist **Isoform** to
Now Playing, and supports lock-screen play, pause and seeking.

## 18. Text messages

Text messages (SMS) are the mobile channel for people without the iPhone app
(`docs/notifications-design.md` section 7a). The Mac still decides what to say and writes
every text; the relay holds the verified number, sends through an SMS provider, applies the
carrier keywords and the caps, and hands replies back to the Mac. Texts travel through the
phone network in the clear, so nothing here is end-to-end encrypted: the relay sees each text
it sends and each reply while it handles it, and keeps neither longer than the protocol below
says. No paired phone is needed. The relay answers `503 {"code":"not_configured"}` on every
hub route (and `404` on the webhook) while no provider is configured.

### 18.1 Records on the relay

| Record | Kept | Contents |
| --- | --- | --- |
| subscription | until the hub deletes it, another hub confirms the same number, or the hub is gone | hub id, `HMAC-SHA256(numberKey, e164)` for lookup, the number sealed with AES-256-GCM under a relay key with AAD `number\|<hubId>`, state `active` or `stopped`, verified time |
| verification | until confirmed, replaced, or a day after its code expired | the same number hash and sealed number, `HMAC-SHA256(codeKey, hubId, numberHash, code)`, expiry (10 minutes), wrong attempts |
| usage | the current and the previous month | per number hash: texts this UTC day and this UTC month |
| opt-out | for good | number hash and time of the STOP |
| inbound id | 400 days | `HMAC-SHA256(idKey, providerMessageId)`, for replay protection |
| reply | until the hub acks it, at most 24 hours, newest 50 per hub | the reply text sealed under the relay key with AAD `reply\|<hubId>\|<replyId>` |
| enabled | until the hub is gone | hub id and the time the Mac turned text messages on (18.2) |

The sealing key is derived from `ISOFORM_RELAY_SMS_KEY` and the hash keys (`numberKey`, `codeKey`,
`idKey`) from `ISOFORM_RELAY_SMS_HASH_KEY`, which defaults to `ISOFORM_RELAY_SMS_KEY`, each with
HMAC-SHA256 over a fixed label. A sealed value is `<keyId>.<base64url(nonce ‖ ciphertext)>`,
where `keyId` is the first four bytes of `HMAC-SHA256(key, "yansu-sms-key-id-v1")` in hex, so a
rotation (`ISOFORM_RELAY_SMS_KEY_PREVIOUS`) opens values sealed under the old key, and the relay
reseals them under the new one at start. The hash key is never rotated: opt-outs are stored by
number hash, and a hash cannot be recomputed without the number. Outbound bodies are never
stored. Numbers and bodies never appear in relay logs or error messages; provider errors are
reduced to their numeric codes.

### 18.2 Hub routes

Every hub route authenticates like section 17.2 (`X-Yansu-Hub` and the hub credential as a
bearer), never creates a hub, allows 30 calls a minute per hub, and returns the status object
below on success. Request bodies are strict JSON (unknown fields refused).

```json
{"state":"none|pending_verification|active|stopped","codeExpiresAt":"2026-10-15T09:10:00Z"|null,
 "sentToday":1,"sentThisMonth":7,"dailyCap":10,"monthlyCap":200}
```

`state` is the verified subscription's state, or `pending_verification` when a code is out and
no number is verified yet, or `none`. `codeExpiresAt` is set while an unexpired code is out,
including a code for a new number while an older number is still `active` (the old number keeps
receiving until the new one is confirmed); an expired code is reported as no code at all, and
confirming it answers `410`. The counts belong to the subscribed number.

| Method and path | Body | Result |
| --- | --- | --- |
| `POST /v1/hub/sms/enable` | none | records that the Mac turned text messages on and returns status; enabling again is not an error |
| `GET /v1/hub/sms/subscription` | | status |
| `POST /v1/hub/sms/subscription` | `{"number":"+14155550123","lang":"en"\|"zh-CN"}` (`lang` optional) | texts a six-digit code to the number and returns status; the number already `active` for this hub needs no code |
| `POST /v1/hub/sms/subscription/confirm` | `{"code":"123456"}` | a right code makes the number the hub's `active` subscription and removes any other hub's subscription for it |
| `DELETE /v1/hub/sms/subscription` | | removes the subscription, a pending code and the sealed number; status `none` |
| `POST /v1/hub/sms/send` | `{"body":"Isoform: ..."}` | texts the body to the hub's number and returns status with the new counts |

Asking for a code and sending need an entitled hub: one that called `POST /v1/hub/sms/enable`,
or one with a paired phone whose bearer has not expired. This is a stub for the paid plan's
entitlement (`docs/sms-channel.md`); any Mac can enable itself, so today it only makes a new hub
id take one more call before it can spend the relay-wide code budget.

Refusals carry `{"ok":false,"data":{"code":"..."},"error":"..."}`; the hub maps `code` to its own
copy:

| Status | `code` | When |
| --- | --- | --- |
| 400 | `invalid_request` | a `lang` other than `en` or `zh-CN` (malformed JSON, unknown fields and a wrong content type answer `400`/`415` without a code, a body over 64 KiB `413`) |
| 400 | `invalid_number` | not E.164 after removing spaces, dashes, dots and parentheses |
| 400 | `unsupported_country` | the calling code is not configured (default `+1`), or a `+1` number is not a US or Canadian personal number (Caribbean countries, toll-free, premium and other non-geographic area codes are refused) |
| 400 | `invalid_code` | the code is not six digits (no attempt is counted) |
| 400 | `invalid_body` | empty, only space, a control character other than a newline, longer than 320 characters (160 when the text needs Unicode, that is any character outside the GSM 7-bit alphabet), not starting with `Isoform:` (or `Isoform：`), or carrying a link (a scheme, `www.`, a domain with a path, or a bare domain under a common link top-level domain) or a phone number (a run of digits, spaces, `().+-` with seven digits or more) |
| 403 | `code_wrong` | the code does not match; the attempt is counted |
| 403 | `not_entitled` | a code or a send for a hub that is not entitled (above); nothing is counted |
| 409 | `opted_out` | the number replied STOP (also when a send finds the subscription `stopped`) |
| 409 | `not_active` | a send without a verified subscription |
| 409 | `no_pending` | a confirmation without a code out |
| 410 | `code_spent` | the code expired or took five wrong attempts |
| 401 | | no hub credential, a wrong one, or a hub id the relay has never seen |
| 429 | `too_many` | a limit below, with `Retry-After` |
| 429 | `quota` | the number's daily or monthly cap, with `Retry-After` until midnight UTC or the first of next month |
| 502 | `send_failed` | the provider did not take the text |
| 503 | `busy` | the relay-wide hourly cap, with `Retry-After` |

Limits: three codes per hub an hour, five per number a day, ten per client address (an IPv4
address or an IPv6 /64) an hour, and a relay-wide code budget (default 30 an hour); ten wrong
codes per number within 24 hours of the first, across codes and hubs, after which the number
accepts no code until that window ends; texts per number 10 a UTC day and 200 a UTC month, counted
when the relay accepts a send and returned only when the provider refused it outright; a
relay-wide cap on texts an hour (default 500); 30 hub calls a minute and ten confirmations per hub
in ten minutes. The per-hub and per-number code limits, the wrong-code budget, the code budget and
the hourly text cap are stored, so a restart does not reset them; the per-address, per-call and
confirmation limits are in memory.

Two trade-offs are deliberate. A hub that asks for a code to a number that opted out is told
`opted_out` (after the code limits are counted), so the user learns why no code arrives; any hub
can learn that much about a number, at the cost of a code request. And the number-wide wrong-code
budget bounds guessing at ten a day, which also lets someone who requests codes for a number
lock it for a day; the owner of that number sees those codes arrive and can reply STOP.

### 18.3 Provider webhook

`POST /v1/sms/inbound` is the URL configured at the provider for incoming messages. It is
authenticated only by the provider's signature (Twilio: `X-Twilio-Signature`, base64
HMAC-SHA1 under the account's auth token over the configured webhook URL followed by every
form parameter name and value sorted by name; the account id must match). The signature is
checked before anything else. A missing or wrong signature is `403`, or `429` once one client
address (an IPv4 address or an IPv6 /64) sent 60 of them in a minute; signed posts are never
counted, so forged posts from an address the provider shares cannot crowd out real texts. A body
that is not a form or lacks the message id is `400`. A verified text is answered `200` with the
provider's empty reply (TwiML `<Response></Response>`), even when it is ignored. The relay handles
each provider message id once; when handling fails it answers `500` and forgets the id, so a
retry would be handled. Twilio does not retry an incoming-message webhook by default, so such a
text is lost. A lost STOP still takes effect: Twilio keeps its own opt-out list, refuses the next
send to that number (error 21610), and the relay records the opt-out then.

### 18.4 Keywords

A reply is a keyword when, after dropping control and invisible characters, surrounding space
and trailing `.` or `!`, it equals one of these words in any case:

| Keywords | Effect | Answer |
| --- | --- | --- |
| `STOP`, `STOPALL`, `UNSUBSCRIBE`, `CANCEL`, `END`, `QUIT`, `OPTOUT`, `REVOKE` | the number opts out: its subscription becomes `stopped`, a code out for it is removed, and nothing else is sent to it, codes included | once, when the number was not already opted out and the relay knew it (a subscription or a code): `Isoform: you are unsubscribed and will get no more texts. Reply START to resume.` |
| `START`, `UNSTOP` | the opt-out is lifted and a `stopped` subscription is `active` again | when something changed: `Isoform: texts are back on. Up to 10 msgs/day. ...` |
| `HELP`, `INFO` | none | `Isoform: alerts from Isoform on your Mac. Up to 10 msgs/day. Msg & data rates may apply. Reply STOP to cancel. Help: <contact>`, at most twice an hour per number, also to a number that opted out |

Keywords take effect for any sender, but only a number the relay could subscribe (an allowed
calling code, and for `+1` a US or Canadian personal number) gets an answer. HELP and START
answers spend a relay-wide keyword budget (default 20 an hour), apart from the hourly text cap,
so texting keywords from many numbers can neither pump answers nor hold back the hubs' texts.
The STOP confirmation spends no budget. A STOP the provider reports on a send (Twilio error
21610) opts the number out the same way.

### 18.5 Replies to the hub

Any other text from the number of an `active` subscription becomes a reply for that hub (at most
30 an hour per number; more are dropped). The relay trims it, drops control and invisible
characters, cuts it at 1,600 characters, seals it at rest, and sends it on the hub socket:

| Direction | `t` | Fields | Meaning |
| --- | --- | --- | --- |
| relay to hub | `sms_reply` | `id` (UUID), `body`, `at` | one reply; sent once per connection and again on every new connection until acked |
| hub to relay | `sms_ack` | `id` | the hub took the reply; the relay deletes it (an unknown id is not an error) |
| relay to hub | `sms_state` | `state`, as the status route reports it (`none`, `pending_verification`, `active`, `stopped`) | the subscription changed without the hub asking: a STOP or START, or another hub confirmed the number; a hub that was offline reads the status route |

`sms_reply` is not sealed for the hub: the text reached the relay in the clear from the phone
network, and the hub socket is TLS. The hub deduplicates by `id`. Unlike the sealed phone
frames, both frames are only as trustworthy as the relay: a hub acts on them only while it holds
a number it confirmed itself (18.6), and acknowledges any other `sms_reply` without acting on it.

### 18.6 Hub behaviour

- The Mac keeps the number it subscribed in its own settings so it can show it; the relay never
  returns it. Only a right code confirmed on the Mac sets that number. The Mac counts the
  subscription as `active` only while it holds such a number: a relay that reports `active`
  (on the status route or in `sms_state`) for a hub that confirmed nothing is ignored, and
  `sms_reply` and `sms_state` frames are acknowledged without effect until the Mac has confirmed
  a number. A compromised relay can therefore neither switch texts on nor put words in the chat
  of a Mac whose user never subscribed.
- Before asking for a code the Mac calls `POST /v1/hub/sms/enable`.
- Every text the Mac sends starts with `Isoform:`, names no content the user did not already see
  on the Mac, and fits one or two segments. The Mac strips links and phone numbers from what it
  puts into a text, with the same patterns the relay refuses. The Mac sends a text only while the
  subscription is `active`, and treats `opted_out` and `not_active` as the subscription changing.
- A reply is resolved by the Mac against the last text it sent (section 7a of the design): a
  reply code answers that moment the way the phone's action button would. Free text is never the
  user's message: it is kept in the main chat as a line marked as texted, shown as "From text",
  and given to the model only inside a block that says it came from the subscribed number, that
  the sender is unverified, and that it is not instructions. The user can turn it into their own
  message with "Use as my message" on the Mac, which puts it in the composer to send. A text
  never starts an agent turn by itself. A short reply that is no code gets the one-line hint only
  while the last text's question is open, at most once a day. Actions the phone guards with the
  owner check are never offered by text.

## 19. Email

Email carries a playbook's result to the user's own inbox, for playbooks where the user chose
email (`docs/playbooks-audit.md`). The Mac writes every message; the relay holds the verified
address, sends through a mail provider (SMTP), and enforces the caps. Like a text, an email is
not end-to-end encrypted: the relay and the provider see each message while they send it, and
the relay keeps none. A send names no recipient: the relay mails only the address the hub
verified, so nothing on the Mac can mail anyone else. No paired phone is needed. Every hub route
answers `503 {"code":"not_configured"}` while no provider is configured
(`docs/email-channel.md`).

### 19.1 Records on the relay

| Record | Kept | Contents |
| --- | --- | --- |
| subscription | until the hub deletes it or the hub is gone | hub id, `HMAC-SHA256(addressKey, address ‖ 0x00)` in hex, the address sealed with AES-256-GCM under the relay's email key with AAD `address\|<hubId>` (stored as `base64url(nonce ‖ ciphertext)`), verified time |
| verification | until confirmed, replaced, or a day after its code expired | the same address hash and sealed address, `HMAC-SHA256(codeKey, hubId ‖ 0x00 ‖ addressHash ‖ 0x00 ‖ code ‖ 0x00)` in hex, expiry (10 minutes), wrong attempts |
| usage | the current UTC day | per hub: emails sent today |

The keys are derived from `ISOFORM_RELAY_EMAIL_KEY` with HMAC-SHA256 over fixed labels
(`yansu-email-seal-v1`, `yansu-email-address-v1`, `yansu-email-code-v1`); the sealing key has no
rotation yet. Addresses
are lowercased before hashing and sealing. Messages are never stored, and neither addresses nor
messages appear in relay logs or error messages. Two hubs may verify the same address (two Macs
of one person).

### 19.2 Hub routes

Every hub route authenticates like section 17.2, never creates a hub, allows 30 calls a minute
per hub, takes strict JSON, and returns this status object on success:

```json
{"state":"none|pending_verification|active","codeExpiresAt":"2026-10-15T09:10:00Z"|null,
 "sentToday":1,"dailyCap":20}
```

| Method and path | Body | Result |
| --- | --- | --- |
| `GET /v1/hub/email/subscription` | | status |
| `POST /v1/hub/email/subscription` | `{"address":"you@example.com","lang":"en"\|"zh-CN"}` (`lang` optional) | mails a six-digit code to the address and returns status; an `active` address keeps receiving until the new one is confirmed |
| `POST /v1/hub/email/subscription/confirm` | `{"code":"123456"}` | a right code makes the address the hub's subscription |
| `DELETE /v1/hub/email/subscription` | | removes the subscription and a pending code; status `none` |
| `POST /v1/hub/email/send` | `{"subject":"Isoform: ...","body":"..."}` | mails the message to the hub's verified address and returns status with the new count |

Refusals use the shape of section 18.2:

| Status | `code` | When |
| --- | --- | --- |
| 400 | `invalid_request` | a `lang` other than `en` or `zh-CN` |
| 400 | `invalid_address` | not exactly one plain address (no display name, no list) |
| 400 | `invalid_code` | the code is not six digits |
| 400 | `invalid_body` | a subject that does not start with `Isoform`, is longer than 150 characters or has a control character; an empty body, longer than 8000 characters, or with a control character other than a newline or a tab |
| 403 | `code_wrong` | the code does not match; the attempt is counted |
| 409 | `not_active` | a send without a verified address |
| 409 | `no_pending` | a confirmation without a code out |
| 409 | `offline` | a code asked for by a hub that is not connected over the hub WebSocket right now (a Mac asking from its settings is); nothing is mailed |
| 410 | `code_spent` | the code expired or took five wrong attempts |
| 401 | | no hub credential, a wrong one, or a hub id the relay has never seen |
| 429 | `too_many` | a code limit below, with `Retry-After` |
| 429 | `quota` | the hub's daily cap (default 20 a UTC day) |
| 502 | `send_failed` | the provider did not take the message (a refused message is not counted) |
| 503 | `busy` | a relay-wide cap, with `Retry-After` |

Limits: three codes per hub an hour, five per address a day, ten per client address an hour, a
relay-wide code budget (default 30 an hour and 200 a day) and a relay-wide send cap (default 300
an hour), counted only after the hub's own address and cap allow the send. All but the
per-client-address limit are stored, so a restart does not reset them.

### 19.3 Messages

Mail goes out as plain text in UTF-8 (quoted-printable), from the configured sender, with
`Auto-Submitted: auto-generated` so mail servers send no automatic replies. The verification
email is the relay's own (`Your Isoform code: 123456`, in English or Chinese). Hub messages are the
Mac's: for a playbook's result, the subject `Isoform: <playbook title>` and a body with the result
lines and a closing line that says where to change the delivery.

### 19.4 Hub behaviour

- The Mac keeps the address it confirmed (`email-channel.json`, mode 0600) and believes an
  `active` state only after it confirmed a code itself.
- The Engagement router mails a moment only when the user chose email for it (a playbook's
  channels) and only kinds with email copy (`playbook_result`); everything else never goes by
  email.

## 20. Release 4: Today, Chat, Tasks and Library

The iPhone app has three tabs, Today, Chat and Tasks, with Settings in a sheet and Library reachable
from Today. Today reads the same briefs (section 8). Tasks lists handoffs and the existing Ideas
offers. Activity stays on the Mac. These routes use scopes declared here, so a user pairs again once.

### 20.1 Scopes

| Scope | Grants | Route |
| --- | --- | --- |
| `brief:feedback` | Like and Not right on a brief item | `POST /briefs/{briefId}/items/{itemId}/feedback` (20.3) |
| `offers:read` | list the work Isoform offers (Ideas), or find more | `GET /offers`, `POST /offers/find-more` (20.6) |
| `offers:decide` | decline an offer with `not_now` or `dismiss`; approval stays on the computer in this release | `POST /offers/{id}/decline` (20.6) |
| `goals:read` | list and read the goals the user tracks (Goals) | `GET /goals`, `GET /goals/{id}` (20.5) |
| `goals:write` | add a goal, check one off, reopen it, mark it waiting | `POST /goals`, `POST /goals/{id}/events` (20.5) |
| `library:read` | list artifacts and media, download a sealed file | `GET /library/...`, not built yet |

A current phone requests fourteen scopes: the eight of sections 10 and 15.1 and the six above.
A route that is not built yet answers 404; a route whose scope the device lacks answers 403 with
the scope name, and the phone shows its re-pair note. `drafts:send` is not declared until drafts
exist, and `desk:read` and `decide` are never reused (section 13). Every new POST carries
`idempotencyKey` and replays from the receipts of section 15.2.

Unknown scopes are handled differently at pairing and in a stored record:

- **Pairing.** The Mac rejects a claim (section 3.3) that names a scope it does not know
  (`mobileNormalizeScopes` in `wails-gui/mobile_pairing.go`), so a phone of this release pairs with
  a Mac of this release or newer.
- **Stored record.** When the Mac loads `devices.json` and a paired device holds a scope it does
  not know (one a newer Mac granted), it keeps the record and ignores only that scope: no route
  accepts it, and it is not shown as granted. The Mac logs the count once per load and writes the
  scope back unchanged whenever it saves the store, so running an older Mac build never unpairs a
  phone or takes a scope away from it. Only the phone pairing again replaces the record.

### 20.2 Item keys

Every section item carries `key`, `<section>.<16 hex digits>`: the section's JSON name, a dot, and
the first 8 bytes of SHA-256 over the item's source record, `sourceKind`, one zero byte, then
`sourceId`, as the item's first ref names them in `evidence`. A source-free goal fallback instead
uses `sourceKind = purpose` and its explicit `purposeId`, preserving the key after evidence expires.
The same record has the same key in
every rebuild of a brief and in later briefs. One record in two sections has two keys. A second
item from the same record in one section ends in `-2`, a third in `-3`. A find that joins the
watching section after the brief was built (section 8) follows the same rule. Keys are at most 128
bytes and safe in a path.

Briefs stored before keys have none. The phone computes the same key from the evidence when it
merges briefs, and names such an item by its `id` when it sends feedback.

### 20.3 Feedback

| Method and path | Scope | Body | Result |
| --- | --- | --- | --- |
| `POST /briefs/{briefId}/items/{itemId}/feedback` | `brief:feedback` | `{"idempotencyKey","action","reason"}`; `action` is `like` or `not_right`; `reason` optional, at most 1000 bytes | 200 `{"recorded":true,"effect":"<effect>"}` |

- `itemId` is the item's `key`, or its `id` in a brief stored before keys. 400 for an invalid id,
  action or reason; 404 when the brief or the item is no longer on the Mac; 503 while the brief
  service is not ready.
- A find (`sourceKind` `find`): Like is "more" and Not right is "less" on the find's topic, the
  answers the Mac's Today page gives (`effect` `topic_more` or `topic_less`). While the topic labels
  may not leave the Mac (observing is paused or Watch my screen is off), or once a newer search
  replaced the find, the answer is only recorded (`recorded`), so the phone never changes what the
  Mac searches for outside that gate.
- Not right on a memory (`sourceKind` `memory`) removes the memory the way
  `POST /memory/{id}/reject-attribution` does (`memory_removed`). It also needs `memory:correct`
  (403 otherwise) and answers 404 when the memory is gone. A missing reason is stored as
  `Marked not right in the Feed on iPhone.`. Like on a memory is only recorded.
- Every other item is recorded (`recorded`).
- The Mac appends each answer to `brief-feedback.json` (0600) in its mobile state folder and keeps
  the newest 500: the key, item id, brief id, section, source kind, action, effect, device and time.
  The item's text is never stored there.

### 20.4 Phone behaviour

- Feed reads the briefs cached on the phone (14 days, section 8 and `BriefCache`). It merges the
  briefs of one day by key, the newest brief's copy first, and shows today's cards, then the
  earlier days as a list that opens each brief. `openLoops` stays out of Feed; Goals will own it.
- Like and Not right go through the phone's write journal, so a retry reuses the key. Not right on
  a memory asks for a reason and for the owner's Face ID, Touch ID or passcode first, as the
  Brief's Not right did. While the Mac is offline both buttons are disabled; reading works.
- The interest sentence typed into Feed's prompt box stays on the phone in this release. No route
  carries it yet.
- Notices of new kinds show generic text on the lock screen; specifics stay in the app.

### 20.5 Goals

Goals are the Purpose Map's open loops the user tracks: commitments, responsibilities, goals,
concerns and opportunities (`purpose_map.go`). The routes are new; the release 2 `/purposes`
routes stay removed (section 13) and answer 404.

| Method and path | Scope | Body | Result |
| --- | --- | --- | --- |
| `GET /goals?limit=30&cursor=` | `goals:read` | | `{"goals":[goal],"counts":{"tracking","done","onMacOnly"},"cursor","hasMore"}`; limit 1 to 100 |
| `GET /goals/{id}` | `goals:read` | | `{"goal":goal,"outcome","history":[{"what","at"}]}`, at most 10 history entries, newest first |
| `POST /goals` | `goals:write` | `{"idempotencyKey","kind","text"}`; `kind` one of the five kinds; `text` 1 to 1000 bytes | 201 `{"goal":goal}`; the phone in this release does not call it (see Creating a goal) |
| `POST /goals/{id}/events` | `goals:write` | `{"idempotencyKey","action","expectedEventId","waitingOn"}`; `action` is `resolve`, `reopen`, `waiting`, `forget` or `restore`; `waitingOn` optional, at most 1000 bytes | 200 `{"goal":goal}` |

A goal is `{"id","kind","title","state","group","status","slipping","dueAt","updatedAt","eventId",
"addedByYou"}`:

- `state` is `active`, `waiting` or `resolved`. `group` is `tracking` (active or waiting, and
  supported) or `done` (resolved). Candidates, contradicted, dismissed and superseded Purposes
  stay on the Mac and answer 404. The one exception is the answer to `forget`: the goal comes
  back once with `state` `dismissed`, `group` `forgotten` and status `Forgotten today.`, so the
  phone can offer Undo; no list or read shows it again.
- `status` is the sentence under the title, two or three lines on the phone, written on the Mac
  so the phone never shows lifecycle words. A tracked goal says what is due or what it waits on,
  with the date or the person, then when it last moved, then the outcome when it says more than
  the title: `Due on Oct 21. You added it yesterday.`, `Due today, and nothing has moved on it
  yet. You added it 3 days ago.`, `Waiting on Ana since Tuesday: her signature.`, `Isoform noticed
  it 3 days ago. The aim: Finish the city half in under two hours.`, `Reopened yesterday.` or
  `Last updated on Oct 2.` A done goal says `Done today.` Titles and status sentences are at most
  320 bytes.
- `slipping` is set when the Mac's commitment-slipping moment would fire for the goal: a dated
  commitment due within a day, or due in the last day, with no update since it was made. It is
  the page's one accent.
- `dueAt` is present for a dated goal. `updatedAt` is the time of the current event. `eventId` is
  the current event; a write names it as `expectedEventId`, and a goal that moved on the Mac since
  answers 409 `this goal changed on your Mac`.
- `addedByYou` is set when the goal's first event rests on the user's own words (made in the main
  chat, typed on the Mac, or a reminder), not on what Isoform noticed.

Order: tracking goals first (slipping, then the soonest due, then the most recently touched), then
done goals, newest first. `cursor` is opaque: it names the last goal of a page by its place in that
order, so a goal that moves between two reads is not shown twice. `counts` cover every goal, not
only the page.

Writes:

- `POST /goals` adds an active Purpose of the chosen kind with the sentence as its title and
  outcome, owned by the user, with one piece of explicit user evidence (`sourceKind` `user_goal`).
  The same key returns the same goal.
- `resolve` checks off an active or waiting goal; `reopen` reopens a done one; `waiting` parks an
  active goal on `waitingOn` (`Someone else` when it is empty). Each is a Purpose event with
  explicit user evidence. Any other action answers 400. Snooze does not exist in this release.
- `forget` hides an active or waiting goal everywhere (Goals on both devices, Today, the brief):
  a dismissed event, nothing erased. The goal finder does not bring it back from the sources it
  already has or in the same words (`purposeClosedMatch`). `restore` is Undo for `resolve` and
  `forget`: it reopens the goal and, when it was waiting, puts it back to waiting on the same
  thing. `restore` is the one move that reaches a forgotten goal by id; reads still answer 404.
  It must name the done or forget it undoes as `expectedEventId` (400 without it, 409 for any
  other event). No read serves a dismissed goal's event, so in practice a phone restores only
  what its own done or forget answered with; this is a consistency check, not an authorization
  rule, and the answer to restore carries the goal as it comes back.
  The computer's Today and Goals page make the same moves (`MoveGoal`). Older phones never send
  `forget` or `restore`.
- The phone asks for no owner check before a goal move (check-off, reopen, waiting, forget or
  Undo): each is a local, reversible change on the user's own list (since #88). Face ID, Touch ID
  or the passcode stays for actions that reach other people.
- Both routes replay from the receipts of section 15.2. After a write the Mac's own views refresh
  (`agency:updated`).

Privacy. Purpose text is written on the Mac from what it observed, and some of it can repeat a
mail: activity summaries that saw Mail or a mail web app feed memory, memory feeds the Purpose
extraction, and reminder and calendar titles are used as written. So before a goal leaves the Mac
its title, outcome, waiting condition and counterparty are compared with the subjects of the mail
the Mac keeps (about a week, `mail_sync.go`), from 12 characters up, ignoring case and reply
markers. Words the user wrote for the goal are theirs and are not compared: the title and outcome
of the sentence they typed (in the main chat on either device, or on the phone) or edited on the
computer's Goals page, and the waiting condition of a waiting move they made. Everything else is,
including a title or waiting condition Isoform's goal finder wrote later, a counterparty, and every
word of a goal Isoform noticed, a reminder, or a meeting or Activity follow-up. A goal that contains
a subject, or is part of one, stays on the Mac and is counted in `counts.onMacOnly`; its id answers
404 on every route, `POST /goals/{id}/events` included, so a held goal cannot be moved from the
phone and its text never comes back in an answer. The hold fails closed: while the Mac cannot read
its mail, `GET /goals`, `GET /goals/{id}` and the events route answer 503 and the phone keeps what
it has. A goal whose every word is the user's own needs no mail read and is never held back. A paraphrase of a
mail cannot be detected this way; the extraction itself does not yet keep mail out. A write the Mac
could not save for a reason that is not a conflict answers 503, not 409, so the phone keeps its
journal key and the same write is retried; so does a goal read the Mac could not complete (503, never 404, for any error other than an unknown id). A retry of a move the Mac committed but could not answer returns the moved goal, not 409.

Creating a goal. A goal starts in the main chat, not in a form. Create a goal on the phone offers
categories (Health, Relationships, Finance, Career, Interests, Productivity, Something else); one
opens a sheet that explains the chat comes first, and its button opens the main chat with the
composer holding `I want to create a goal about <category>` (Something else: `I want to create a
goal.`), unsent. The sheet is only as tall as what it says. The chat asks a few questions, says the goal back, and once the user agrees calls
the main chat's `GoalCreate` tool (`goal_tools.go`) with the sentence, a kind and a category. The
categories are not kinds: the agent picks the kind that fits (a Health goal can be a goal, a
commitment or a concern), and the category is kept as the Purpose's scope (`health`, `finance`,
..., `other`). The tool is given to the main chat for a message typed on the Mac or sent from the
phone, and to no other chat or run; it is a reversible local write the authority policy allows in
an attended chat, and the tool call's id keys the goal so a replay adds it once. A chat run through
an external CLI (Claude Code or Codex) does not receive app tools, so there the chat says the goal
could not be added. The goal then appears in the list on the next read.

Phone behaviour: Goals lists the tracking goals with round checkboxes, a one-line title, the status
sentence and a menu (Mark done, Waiting on someone; Reopen for a done goal), a few at a time with
"Show N more", then the done ones, then Create a goal. A check-off, reopen, wait or forget shows at
once, without an owner check, and goes through the write journal; a check-off or forget offers
Undo for 10 seconds, and Undo of a waiting goal shows it waiting again at once, through refreshes,
until the Mac answers. One the Mac does not answer is kept on the phone, shown with "Not on your
Mac yet", and sent again under the same key on the next refresh; one whose key the journal no longer holds (a day) is put back with a notice, and a 409
or 404 puts it back and reads the list again. While the Mac is offline the checkboxes and menus
are disabled and the last list read is shown from the phone's cache; Create a goal still works,
since the chat keeps the message.

### 20.6 Ideas

These additive routes project suggestions and topic finds the computer already produced. Reading
Ideas creates no suggestion, schedule, chat, work item or authority grant.

| Method and path | Scope | Body | Result |
| --- | --- | --- | --- |
| `GET /offers` | `offers:read` | none | 200 `{"offers":[{"id","version","kind","title","why","whyLocalized","prompt"}]}` |
| `POST /offers/find-more` | `offers:read` | none | 200 same offer list, with optional `nextAllowedAt` (RFC3339) during the 10-minute manual-search cooldown. Runs the existing manual topic search and returns its fresh projection; today's cached result never starts a model. During cooldown returns the current list immediately. A search still running after two minutes returns 503; re-read `GET /offers` for its completed results. |
| `POST /offers/{id}/decline` | `offers:decide` | `{"idempotencyKey","decision":"dismiss"}`; `not_now` is also accepted | 200 `{"declined":true}` |

- `id` is `suggestion.<initiativeId>` or `find.<findId>` and stays the same while its underlying
  record exists. `kind` is `suggestion` or `topic_find`. Titles are bounded to 200 UTF-8 bytes,
  `why` is one short explanation, and `prompt` is bounded display text with an optional verified
  public URL for a find. The response has no execution-plan, raw-evidence, source-path or
  conversation fields; title and prompt remain generated display text.
- `version` is an opaque 64-character lowercase hexadecimal digest binding the generated source
  text and its source association. A changed prompt, suggestion evidence or frozen topic-search
  association invalidates the old version. `whyLocalized` supplies `en` and `zh-CN` explanations;
  source titles and prompts retain their source language. Native actions and accessibility copy
  use the phone's English or Chinese presentation language.
- The list contains at most one currently proposed Today suggestion and the current fresh topic
  finds. Dismissed suggestions, finds marked `less`, removed topics, expired suggestions and
  sources that no longer pass the read gates are absent. An empty result is `{"offers":[]}`.
- Source checks reuse the computer's current read policy, including pause/capture permissions,
  sensitive or ignored apps, source retention, corrected memory and bounded provenance traversal.
  A suggestion requires all its observations and evidence to remain readable; a find requires its
  frozen topic-search sources and an observed research URL. Unknown provenance fails closed.
  Mail-subject holds from section 20.5 check the complete original title, explanation and prompt
  before display bounds or redaction can disguise a match. Text is then redacted before it is
  bounded. Missing stores or unreadable privacy/mail settings answer 503; an
  active observation pause answers an empty list. A policy change during a read suppresses the
  response with 503.
- Decline first checks that the idea is still visible under those same gates. Missing, expired or
  held ideas answer 404 without changing feedback. Suggestions use `ResolveTodaySuggestion`:
  `dismiss` records the existing wrong-suggestion feedback, while `not_now` defers. Finds use the
  existing `less` feedback for either decision, downweighting their topic without deleting it.
  Receipts use the existing phone channel (`push`); desktop helpers retain their desktop default.
  The route rejects approvals and work-start decisions with 400.
- The key is required and scoped to the paired device. Successful requests replay through the
  receipts in section 15.2; reusing a key for another route answers 409. Concurrent phone declines
  are serialized so the same find is not downweighted twice. Scope checks precede replay.
- The phone shows a card with `title`, the one-line `why`, **Do it** and **Not for me**. Do it
  opens the main Chat tab with `prompt` and its `{id,version}` reference in its composer, unsent.
  Existing draft text and references are preserved. Pending handoffs are durable even before the
  phone discovers the main chat. The composer and outgoing messages save text and references
  together, including after edits, deleting all text, Undo, refused sends and relaunch. Multiple
  ideas retain the union of their references: editing does not make generated text user-authored.
  Sending atomically queues the text and its references and clears the composer; the view clears
  Undo and replaces the text field so old text cannot return without provenance. Explicitly
  discarding the draft clears both text and references, also clearing Undo. No approval or send
  request is made by Do it.
- Sending or retrying a queued idea requires `offers:read` as well as `chat:write`. The server
  re-resolves all references under current source/privacy gates at admission; a missing, withdrawn,
  hidden, expired, declined or changed offer fails closed with 409. An unreadable privacy store
  answers 503. The full edited text crosses the mail hold before redaction, then the redacted
  text alone can be stored and dispatched. Retries retain the original reference versions and
  idempotency key; they never silently refresh a version or fall back to ordinary chat.
- An older cached card without a version remains readable but cannot prepare a chat draft until
  refreshed. Legacy unsent queue records that lack a provenance field cannot safely be identified
  as user-authored; the phone preserves them as blocked drafts and requires explicit discard and
  a fresh idea. Saved composer text from the earlier envelope is migrated with the same restriction;
  recovery IDs survive the migration to prevent duplicated drafts. Accepted legacy messages remain history. The new queue writes an explicit empty
  reference array for ordinary user-authored messages, distinguishing them from those old records.
- Not for me asks for the owner's Face ID,
  Touch ID or passcode and submits `dismiss` through the write journal with its stable key.
  The phone can refresh the list by pulling down. While the computer is offline, cached ideas
  remain readable and Do it can prepare a draft; feedback waits until the computer is available.

### 20.7a Phone Today snapshot

`GET /calendar/free-time` requires `brief:read` and returns an array of up to
three `{start, end}` RFC 3339 ranges in the normal response envelope. The hub
reads its permitted, recently synchronized calendar records; it returns 503
when it cannot safely answer. The route never requests calendar access or
writes an event.

`GET /calendar/free-time-details` also requires `brief:read`. It returns
`{ranges, checkedCalendars, browserOnly}`: ranges are the same RFC 3339
intervals, checkedCalendars are the names read on the computer, and
browserOnly names calendars the person must check in their browser. The older
array route remains fail-closed for uncovered web calendars so older phones do
not present an unqualified free-time claim.

`GET /today` requires `brief:read` and returns a `MobileTodayV1` object in the
normal response envelope. The hub serves an in-memory projection; its first
read may build the projection, and later updates are built in the background.
`generatedAt` is RFC 3339. `latestBrief` and `briefs` contain `id`, `slot`,
`localDate`, `madeAt`, `lead`, and, when available, `scheduledAt`, `catchUp`,
and `durationSeconds`. Since 1.0 (section 8.1) each entry also carries `kind`
(`morning`, `evening`, `week` or `on_request`) and `version`, the SHA-256 hex
of the brief's package bytes; `bodyVersion` equals `version`, so older phones
refresh a cached body when the package is replaced. `currentBrief` names the
one current brief as `{id, version}`: the newest stored brief with written
text (section 8.1). A phone shows the package with that id and version and
fetches it when its library holds another version.
`briefs` contains readable, nonempty briefs from the
last 14 days. `schedule` uses the existing brief schedule shape. The `lead`
field is also additive on the existing `GET /briefs` list; it comes from the
same current delivery-gated narrative as the brief body.

`needsYou` contains entries with `id` and either a `card` plus its `chatId`,
or a `task`. Cards use the existing mobile chat item shape and action routes;
tasks use the existing Tasks shape. `needsYouCount` counts those entries.
Each entry also carries `noticeId`, `createdAt` (RFC 3339), `kind` (the
engagement kind) and `priority` (`passive`, `active`, or `time-sensitive`).
`expiresAt` is included when the engagement moment has an expiry.
The notice ID is the same engagement moment ID used for its APNs notice.
For an item without an engagement moment, the ID is `task:<id>`, its creation
time comes from the item, its kind is `work_blocked`, and its priority is
`active`. Older snapshots can omit these fields; clients must not invent a
notice identity for them.
When the engagement service is available, `noticePacing` contains `activeToday`
and may contain `lastQuietBreak` (RFC 3339), reflecting the Mac's current local-day
budget and quiet-hours break. Phones also count their own local deliveries.
`keepAwake`, `instantPush`, and `briefSchedule` use the same values as
`GET /status`; `briefSchedule` contains the next 24 hours of slot times.
`assistantSignOut` is additive (R75). It is present only while the chosen
assistant is confirmed signed out on the computer: one re-check must agree before
it appears, and it goes away on the next sign-in check. It is `{"assistant":
"claude-code"}` or `{"assistant":"codex"}`. The phone shows one line under the
date and has no button, because the sign-in happens on the computer. Older
phones ignore the field, and older computers omit it.
Only current waiting items are included. A device without `chat:read` receives
the task entries only and no topics. `day` contains `calendarConnected`, an
optional `next` calendar event and `free` ranges, each with `start` and `end`.
`topics` uses the existing phone topic projection. `briefRequest` is present
only for a requested run and contains `state` (`queued` or `making`) and
`readyAt`. `POST /brief/refresh` for the phone returns these states; queued
requests wait for the cooldown and duplicate requests coalesce. Requests may
supply an `idempotencyKey` to replay the same receipt after a lost answer;
older phones may continue to send an empty object. The estimate
may pass while a run continues and must not be presented as a future time then.

The 64 KiB response envelope cannot hold an unbounded number of waiting cards.
When the scoped snapshot exceeds its response budget, `GET /today` returns
`{"chunked":true,"version":N,"parts":P}` instead. The phone reads each
zero-based part with `GET /today?version=N&part=I`, concatenates the base64url
decoded bytes and decodes the resulting snapshot JSON. A changed snapshot
answers 409 to an old version; the phone starts again. Parts are prepared in
the background, are scoped to the device, and never contain audio files.

An h2d event has `event: "today_changed"`, a nondecreasing
`version`, and normally a scoped `snapshot`. When the snapshot does not fit
the event plaintext limit the event contains only the version and the phone
reads `GET /today`. Events are coalesced and are not stored for offline phones;
the phone also reads on activation and pull to refresh. The hub can repeat the
current version after reconnecting so active phones can retry queued writes.

### 20.7 Tasks

`GET /tasks` uses the existing `brief:read` scope and returns a bounded snapshot of handoffs that
need attention, are running, or completed in the previous seven days. It does not expose work
bodies, work directories, key files, source links, chat IDs, or verification notes.

| Method and path | Scope | Body | Result |
| --- | --- | --- | --- |
| `GET /tasks` | `brief:read` | none | 200 `{"tasks":[...],"counts":{"needsYou","running","doneThisWeek"}}` |
| `GET /tasks/{id}` | `brief:read` | none | 200 task fields plus `startedAt` and bounded `steps`; 404 if missing, stale, aborted before stops were recorded, or held for mail privacy |
| `POST /tasks/{id}/stop` | `handoff:create` | `{"idempotencyKey":"..."}` | 200 updated task; 404 if missing or mail-held; 409 if not stoppable |

Each task may contain `id`, `title`, `status`, `group`, `blockedReason`, `progress`,
`createdAt`, and `updatedAt`. `group` is `needs_you`, `running`, or `done`; timestamps are
RFC 3339 UTC. `blockedReason` is sent only as `interrupted` or `timeout`. `progress` is present
only when verification has a positive total and contains only `{"done":<met>,"total":<total>}`.
The optional `runner` field is described in 20.7c: it names the computer's current assistant, because
handoff records do not retain per-entry runner provenance.

A task never disappears (Tasks K1). Additive optional fields: `stopCause` is present on a Needs you
task that stopped without finishing and is one of `no_assistant` (no AI is configured on the Mac),
`signed_out` (the CLI's own auth status says signed out), `restarted` (blocked `interrupted`),
`timeout` (blocked `timeout`) or `assistant_error` (any other run error). `assistant` names the AI that
cause is about, `claude_code` or `codex`, and is absent for any other AI. `stoppedBy` is `person` on a
Done task the person stopped; that task has `status` `aborted`, `group` `done` and outcome `stopped`.
An `aborted` task stopped by the Mac (`stoppedBy` `system` on the Mac) is in Needs you with its
`stopCause`. Aborted work recorded before stops were tracked stays absent. Older phones that ignore
these fields still see every task in a known group. `POST /handoffs/{id}/start` also starts a task in
Needs you with a `stopCause`, and a person-stopped Done task, again in the same thread (Start again /
Start over); its thread gains a `line` event with `code` `started_again`.

Task detail may add `startedAt`, the RFC 3339 UTC start time of the current execution attempt,
and includes at most 12 checklist steps derived from its criteria. It is omitted when the task
has not started an attempt. Step text uses the offer redactor and is bounded to 200 bytes. Result criterion
flags determine completed and needs-review steps when present; otherwise verification marks the
first `Met` criteria done. Completed tasks without result flags have all steps done. For an
in-progress task, the first unfinished step is `current`. Detail and Stop use the same mail-subject
privacy hold as the list. Stop is idempotent per device and request key and can abort only `active`,
`in_progress`, `blocked`, and `needs_review` tasks.

Needs-you tasks include `active`, `blocked`, and `needs_review` statuses; running tasks are
`in_progress`. Completed tasks are included when their update time is within the previous seven
days; stale, aborted, older completions and unsupported statuses are excluded. Tasks are grouped
in `needs_you`, `running`, `done` order. Within needs-you, `needs_review` precedes `blocked`, then
`active`; the remaining order is newest `updatedAt` first. At most 20 tasks from each group are
returned, while counts remain uncapped. Titles matching the Mac's mail-subject hold are omitted
before counts are computed. The hold is checked on original text before redaction and the 200-byte
display bound; an unreadable subject source answers 503.

### 20.7b Task threads (Phone Threads v1.0.0, 2026-10-30)

Every task opens one thread. These additions are optional fields on the 20.7 shapes and two new
routes; a Mac that serves them sets `"taskThreads": true` in `GET /status`, and a phone sends the
new writes (including `origin` on `POST /handoffs`, whose body rejects unknown fields) only to such
a Mac. Shipped phones ignore the new fields.

`GET /tasks` adds `"threads": true` at the top level, and rows may add:

| Field | Meaning |
| --- | --- |
| `outcome` | Present only on Done tasks with an outcome sentence: `{"kind","by","at","params"?}`. `kind` is `sent`, `replied`, `you_did`, `already_done`, `hold_placed`, `routine_sent`, `marked_done`, `completed` or `stopped` (you stopped it; `params` may hold `n` and `m`, steps done of steps total); `by` is `isoform` or `you`; `at` is RFC 3339; `params` holds redacted, bounded strings (`person`, `thing`, `app`, `did`, `when`, `audience`, `summary`). The phone words the sentence from its own catalog in the current language. A new phone shows no Done row without an outcome. |
| `unread` | `true` on a Needs you task changed since its thread was last opened. |

A waiting task the person set to Not now is absent from `GET /tasks` (and its counts) for 24 hours.

`GET /tasks/{id}` adds `outcome` and `unread` as above, plus:

| Field | Meaning |
| --- | --- |
| `origin` | Optional `{"kind","at","label"?,"slot"?}`. `kind` is `up_next`, `brief_chip`, `chat`, `list` or `routine`; `label` is a brief chip's or routine's own words; `slot` is the brief or routine time. Absent for work whose origin was not recorded. |
| `events` | Always present (empty for older work), oldest first, at most 60: `{"id","kind","at","text"?,"choices"?,"chosenId"?,"file"?,"code"?,"params"?}`. `code` and `params` are additive (Tasks K1): `code` is `stopped_no_assistant` or `stopped` on Isoform's message saying why a run stopped (`params.cause` is a `stopCause`, `params.assistant` as above) and `started_again` on the `line` after Start again; `text` is always the Mac's own wording in its language, so a phone may ignore `code`. `kind` is `isoform` (Isoform's message), `you` (the person's reply or tapped choice), `line` (a quiet result line) or `file` (`file` is `{"name"}`, the base name only, never a folder). Text keeps paragraphs, is redacted and bounded to 4096 bytes. `choices` are `{"id","label"}` answer buttons on an Isoform message; `chosenId` is set once one was tapped, and the tap follows as a `you` event. Isoform's answer to a reply sent into the task's own chat is read from that chat and appears after the reply. Sensitive-app work shows only the person's own messages. |
| `actions` | The ··· items that apply now, in order: `mark_done` and `not_now` (waiting tasks), `rename`, `made` (when a file event exists), `stop` (running). |

| Method and path | Scope | Body | Result |
| --- | --- | --- | --- |
| `POST /tasks/{id}/messages` | `chat:write` | `{"idempotencyKey","text"}`, text 1 to 4096 bytes | 200 the updated thread detail. The reply stays in the task, never in main Chat. A task with its own chat gets it as that chat's next turn (409 while a turn is still running there; 503 when the chat cannot be reached). Unstarted work keeps it under "Notes from you" in its brief. |
| `POST /tasks/{id}/actions` | `handoff:create` | `{"idempotencyKey","action","title"?,"eventId"?,"choiceId"?}` | 200 the updated thread detail; 400 unknown action or bad input; 409 when the task's state does not allow it or a choice was already answered |

Actions: `mark_done` closes a waiting task (`active`, `blocked`, `needs_review`, `stale`) as
`completed` with outcome `marked_done` by `you`; running work completes only through verification.
`rename` sets a one-line title of 1 to 200 bytes. `not_now` hides a waiting task from Needs you for
24 hours. `opened` records that the person opened the thread (clears `unread`). Neither `opened`
nor `not_now` changes the task's `updatedAt`. `choose` answers one Isoform message's choice once;
the tap becomes a `you` event and, when the task has its own chat, that chat's next turn. Both
routes replay per device and key as in 15.2, and use the same mail-subject hold as 20.7.

`POST /handoffs` accepts an optional `origin` `{"kind","label"?}` with `kind` `up_next`,
`brief_chip`, `chat` or `list`; the Mac stamps its time.

When one-time work finishes and verifies, the Mac adds Isoform's closing message, a `file` event
for each declared deliverable and, when the closing message's first sentence fits 200 bytes, a
`completed` outcome whose `summary` is that sentence.

#### 20.7c Up next evidence, Scheduled and the runner (Phone Threads v1.0.0)

- `GET /offers` items add `evidence` `{"text","textLocalized"?,"source","at"}` and optional `deckKind`.
  - `evidence` is the fact Isoform saw: one or two sentences in `text`, with `en` and `zh-CN` in `textLocalized` when it has them.
  - `source` is `mail`, `calendar`, `claude_code`, `codex`, `call` or `screen`, and `at` (RFC 3339) is when it was seen.
  - `deckKind` is `reply`, `your_day` or `claude_code`, from the source.
  - The Mac drops an offer it can't state a dated fact for. A suggestion needs a dated observation. A topic find needs the time you looked at its topic.
  - The initiative's own evidence list, quotes and source IDs are never sent. Older phones ignore both fields.
- `GET /tasks` adds `scheduled`: at most five briefs and enabled routines due in the next 48 hours, soonest first.
  - Each is `{"id","kind","slot"?,"title"?,"at"}`.
  - `kind` is `brief` (with `slot` `morning` or `evening`, and `id` `brief.<slot>`) or `routine` (with the routine's own `title`, and `id` `routine.<jobId>`).
  - The app's own news job, system schedules and titles under the mail-subject hold are left out. The phone opens these rows as read-only threads and never requests their detail.
- Running tasks carry `runner` `claude_code` or `codex` when the computer's assistant is the person's own Claude Code or Codex. Otherwise it is omitted and the phone says "On your computer".

#### 20.7d Reply drafts as tasks (Phone Threads v1.0.0)

- A `waiting_on_reply` card with a sendable draft is also a task, with id `draft.<24 hex>` (a hash of its moment id). This covers the main Chat card and digest children.
  - In `GET /tasks` it leads Needs you with `status` `draft_ready` and `unread` true.
  - After it is sent, or answered by the person, it moves to Done for 7 days with an `outcome`: `replied` `{person}` by `isoform` ("{person} has your reply.") or `you_did` `{app: "Mail"}` by `you`.
  - It is listed only when the send gate passes. That gate is the voice receipt and a fresh read of its mail source, the same check Send makes.
- `GET /tasks/draft.*` returns the thread: Isoform's card text and "Here's the reply. Nothing goes until you tap Send." (in the computer's UI language), plus `draft`:
  - `channel`, `to`, `subject` and `body`.
  - `state`: `ready`, `sent` or `not_sent`. `sentAt` is set when sent.
  - `why` `{text, textLocalized, source: "mail", at}`: who is waiting, and when their message arrived.
  - `recipients` `{to, notIncluded}`. `notIncluded` lists the addresses on that message's To and Cc that the reply does not go to: not the person and not the user's own. It is empty until the Mac has read Mail once since it started.
  - `name`: the recipient's display name as the mail shows it, for example "Sam Lee" (the contact's name, else the sender's, else the name in the To header). Absent when the mail has no name. It is never an address: the phone shows "your reply" and "the reply" where it is absent, and nothing in Today's line or the ask-again prompt shows an address.
  - There is no `voice` yet. The Mac does not keep the person's past replies.
- `actions` is `send` and `not_now` while ready.
- `POST /tasks/draft.*/actions` with `send` needs `chat:write`.
  - It takes an optional `text`: the person's edit, up to 16 KiB. The edit is sent in place of the draft, under its own revision (`<revision>-edit-<hash>`), after the original draft passes the send gate.
  - **Check first:** when the Mail headers show the person already answered the thread after the draft's message, nothing is sent. The answer is the thread closed as `you_did`, with Isoform's line "You replied to {person} yourself from Mail, so I closed this."
  - An outcome Mail could not confirm answers 503. Other refusals answer 409.
  - `not_now` sets the card aside through the existing card action.
  - Both replay per device and key. The phone holds Send for 5 seconds with Undo before it calls this route.

#### 20.7e What Isoform sees and presence (Phone Threads v1.0.0)

- A Mac that serves these sets `"sees": true` in `GET /status`. A phone shows the Chat mark's page and the Settings row only when it is set; older Macs answer 404 and the phone says it couldn't reach the computer.
- `GET /privacy` (`brief:read`) answers `{sources: [{job, on}], assistant}`.
  - `job` is `mail`, `calendar` (Calendar or Reminders), `calls` (the microphone), `screen`, `assistant` and `contacts`, in that order. `assistant` is listed only when the person's own AI runs tasks.
  - `on` is true only when the Mac's switch is on and macOS granted what it needs.
  - `assistant` is `claude_code`, `codex`, or omitted for Isoform's built-in model. The phone words every row from its own catalog; the Mac sends only codes.
- `POST /privacy/show` `{idempotencyKey, job}` (`brief:read`) shows a notice on the computer: "Turn on {source} for Isoform". Clicking it opens Settings › Observation. Nothing changes on the phone's word; the switch and the macOS prompt stay on the computer. It replays per device and key; an unknown `job` answers 400.
- **Presence** is an h2d event on `chat:read`: `{"event": "presence", "text": {"en", "zh-CN"} | null, "at"}`.
  - The Mac publishes it when work starts ("Writing the reply to {name}…", "Reading your Claude Code history…"), again every 4 seconds while the work lasts, and with `text` null when it ends.
  - The phone shows the line under Chat's title only while its newest event is under 10 seconds old, so a lost clear never leaves it on screen. Older phones ignore the event.

#### 20.7f Phone display preferences and first copy

- The phone's app-icon count is a local preference, off by default. When enabled it uses the `needsYou` count in the existing Tasks response; switching it off clears the icon. The Tasks tab count is independent of that preference. The relay's APNs `aps` payloads do not carry a badge, so no preference is sent to the computer or relay.
- After a new pairing, Today and Tasks each show wordless shapes until their respective first response arrives. Today uses its existing show-once marker, reset on a new pairing; Tasks retains its pairing-scoped pending state across a phone restart. Subsequent refreshes keep the last copy visible.

#### 20.7g Brain dump

`GET /status` advertises `brainDump` when this route pair is available. Phones retain their existing composer if the flag is absent; older computers return 404 for these routes.

`POST /tasks/parse` requires `handoff:create` and accepts `{idempotencyKey, text}`. It returns `{items: [{id, title, who, when?, project?, person?, noRunner?}]}`; `who` is `draft`, `claude_code`, `codex`, or `reminder`. `noRunner` identifies code work offered as a reminder because no coding assistant is configured. A reminder time is RFC 3339 in the computer's local time zone. The model is tool-free, and a malformed answer falls back to one untimed plain task per nonempty line. The route is receipted per device and key. Parsing does not create or start work.

`POST /tasks/add` requires `handoff:create` and is receipted. It stages the checked selection before publishing it and returns `{ids: [taskId]}`. A reused key with a changed selection returns 409. Reminders use the existing durable reminder service; untimed tasks do not invent a time. A staged reminder is held until publication, and a completed or removed task cannot deliver a later reminder. Code tasks use the existing handoff start path and the selected runner; no alternate assistant is silently substituted. An unclear project is asked in the thread using its existing choices. Draft tasks wait in Running, link to an unambiguous source-checked waiting-on-reply draft when one appears, and report when none has appeared after 24 hours. Linked drafts retain the existing Send scope, source checks and send receipt; adding them sends nothing. List threads use the existing origin `{kind:"list", at}`.

#### 20.7h Phone Today sections (B9)

- `GET /tasks` (`brief:read`) may add `waitingItems` and `waitingCount`. The phone's Today and Tasks waiting sections and badge use this single response. Each item is either an existing task row or a phone-safe proactive card and its chat identity; cards require `chat:read`. The count includes rows beyond the bounded response. Older phones ignore the fields; older computers omit them. The computer retains the legacy `/today` fields for shipped clients and notices.
- Task rows may add `source` (an origin code) and `openedAt` (RFC 3339). The phone takes Done for you from `/tasks` outcomes in the last 24 hours. An opened result remains until a later brief. `tasksVersion` on `/today` is an opaque change token that tells the phone to refresh `/tasks`; it carries no task content.
- `GET /status` advertises `chatMainSend`. If true, `POST /chats/main/messages` (`chat:write`) sends to the main chat even when the phone lists none: the computer makes sure its main chat exists, then takes the message with the same body, scope and idempotency key as `POST /chats/{id}/messages`. Older computers omit the flag; the phone then tells the person to update Isoform on the computer and keeps the composer.
- `GET /status` advertises `todayMeetingPrep`. If true, `GET /today/meeting-prep?id={calendar ID}` (`brief:read`) resolves the selected upcoming meeting again under the computer's current calendar, person and privacy gates. A missing or withdrawn source answers 404; a read failure answers 503. Older computers omit the flag and the phone handles 404.
- A draft task detail may add `revision`, a hash of the draft fields. `POST /tasks/{id}/actions` may add `draftRevision` to `send`. The computer checks a supplied revision under the existing send lock and refuses a changed draft with 409. Old phones can omit the field. New phones retain the edit, revision and idempotency key in the pairing-scoped protected write journal, wait five seconds before dispatch, and allow Undo until the request may have reached the computer. An ambiguous dispatched request is retried only with the same key, within the receipt lifetime.
- The current meeting prep projects the existing Before you talk context. The separate B11–B13 promise ledger is not present in this base, so the 90-day promises view is not yet available.
- A `/today` Needs you row can add `destinationKind` (`task` or `chat`) and `destinationId`, naming the task or proactive chat moment for a local notice. Both fields are optional: older phones ignore them and older computers omit them. A batch preserves the destination of the first item named in its body. The phone validates the identifier and the pairing before routing it.
- An opened history item whose body is missing may use the existing `GET /briefs/{id}` (`brief:read`) to fetch the body, subject to the same computer-side privacy checks. The phone checks the returned ID and current pairing before caching it. This does not add a route or capability flag.
- A registered computer may send the relay `{"t":"goodbye","cause":"sleeping"|"shutting_down"|"closing"}` before sleep, power-off or quit. A sleep goodbye adds `"reason":"battery_floor"|"low_power"|"lid_closed"|"user_sleep"|"unknown"`; power-off and quit omit it. The computer chooses the first matching reason: Low Power Mode is `low_power`; IOKit AppleClamshellState reporting a closed lid is `lid_closed`; an otherwise unexplained sleep with the keep-awake assertion held is `user_sleep`; anything else, including a sleep on battery, is `unknown`. Builds from this change no longer send `battery_floor`; phones keep reading it from older computers. Older computers omit `reason`, so the receiver defaults a missing reason to `unknown`. The relay validates the cause against that authenticated connection, rejects any `reason` outside those five values, stores the last goodbye with its reason (a goodbye without one clears it) and returns optional `goodbyeCause`, `goodbyeAt` and `goodbyeReason` from the existing scoped `GET /v1/presence`. A new registered connection clears the old goodbye; an unannounced disconnect has no cause. Older relays reject the frame, and a relay older than `reason` rejects a frame that carries it; older phones ignore the optional fields. The phone maps `goodbyeReason` to the Chat thread line: `battery_floor` and `low_power` to "went to sleep to save its battery", `lid_closed` to "lid is closed", and `user_sleep`, `unknown` or no reason to "asleep". The phone determines its own network outage and relay failure locally; it never treats the absence of a goodbye as evidence of sleep.
- Widget snapshot version 3 is a local App Group file, not a new relay payload. The phone publishes its retained brief, waiting count and first destination, next event and schedule, and refreshes the timeline after changes and at midnight. A brief from another day is never displayed as today's. With app lock enabled the published snapshot omits the brief text, item and event. The widget's audio intent executes in the app and uses its pairing-scoped cached player.
- Sealed notice plaintext may add `destinationKind` (`brief`, `task` or `chat`) and `destinationId`. Both are optional for compatibility; the phone validates them and the current pairing before opening. Brief-ready uses the brief ID, weekly recap uses `source_brief_id`, and work notices use the task ID. Other supported proactive notices use the chat moment ID. A notice without a usable destination follows the older fallback; this does not guarantee that a retained chat page still contains the moment.
- `GET /status` may advertise `notificationTest`. When present, `POST /notifications/test` (`push` scope) accepts `{idempotencyKey, language}` where language is `en` or `zh-Hans`, and submits a sealed test notice to the requesting paired phone only. The response `{noticeId}` confirms submission to the relay connection, not APNs delivery. The phone reports arrival only when its notification extension authenticates and decrypts that pairing's test notice and records its ID; a timeout is unconfirmed. Older computers omit the capability and can answer 404.
- A Play request from a brief notice fetches an absent body through the existing brief route, then requests audio. A transient connection failure may leave a protected, pairing-scoped pending Play on the phone for retry when the computer returns. A missing or forbidden brief is not treated as an offline request. The request is removed when playback starts or the pairing is cleared.

#### 20.7i First read (Phone Threads v1.0.0)

A computer serving this feature advertises `firstRead: true` in `GET /status`. The phone opts in to the new proactive kind with `firstRead=1` on `GET /chats/{id}`. Without that query, the computer skips the item while advancing the cursor; older phones therefore never receive the new kind. A new phone treats a missing capability or a 404 from an older computer as unavailable.

The main-chat proactive item has `kind: "first_read"` and optional `sentences` (at most two checked portrait sentences), `helps` (two or three specific lines, otherwise omitted), `offerCount` (one to three current Up next offers, otherwise omitted), and `firstReadConfirmedAt` (Unix milliseconds). Localized help can also appear in `localized` as `helps`. The computer posts at most one per pairing and rechecks the portrait and source visibility on each read; it withholds a changed or withdrawn portrait. Only the matching pairing receives its item, and `offerCount` requires `offers:read`. No sentences means no first-read item.

`POST /first-read/answer` requires both `chat:write` and `memory:correct` and accepts `{idempotencyKey, momentId, action, text?}`. The pairing must own the moment. Actions are `confirm_portrait`, `undo_portrait`, and `correct_portrait`; a correction requires 1–500 characters. Confirmation records the current checked portrait version, Undo is accepted for five seconds and restores the prior confirmation, and correction stores the person's words as both a chat message and a portrait correction. The response contains `{state, confirmedAt?}`. Writes use the existing per-device receipt, binding the key to the complete choice; a retry replays the receipt rather than repeating the answer.

**B8 relevance feedback (additive).** `GET /status` may advertise
`briefRelevanceFeedback`. When it is true, the existing receipted
`POST /briefs/{briefId}/items/{itemId}/feedback` also accepts `not_relevant`.
The phone uses `like` for Useful and `not_relevant` for Not for me. On a find,
`not_relevant` applies the existing less-interest signal; on a memory it records
relevance only and never rejects or deletes that memory. The existing
`not_right` action remains a memory correction, including its permission
checks. Older computers omit the capability, and the phone does not send the
new action to them. Existing clients and their actions remain supported.

**B8 paragraph Ask (additive).** `GET /status` may advertise `briefAsk`.
With that capability, `POST /chats/{id}/brief-messages` accepts `ask: true`
and an optional `paragraph` (zero-based) plus `paragraphText` as displayed.
The computer validates the exact paragraph and delivered revision again, and
starts a read-only main-chat turn under the existing idempotency key. It refuses
a changed or withdrawn paragraph instead of dropping its context. The user
message is stored in Chat; the paragraph's verified context remains attached
to the turn. The phone journals the request for retry when the computer is
away. Older computers omit the flag and never receive these new fields.

While such a turn runs, `GET /chats/{id}` for a device with both `chat:read`
and `brief:read` can add `live` with
`turnId` and bounded `text`. The computer revalidates the turn's source proof
before publishing this field. Existing content-free `chat_turn` events prompt
the phone to read it again; the final answer remains a normal Chat row. Old
clients ignore the field and old computers omit it.

`GET /briefs/{id}/ask-answer` with `version`, `paragraph` and the displayed
`text` reopens the saved question and answer, or returns a null answer if none
exists. It requires `brief:read` and `chat:read`, validates the displayed
paragraph and revision, and uses the shared source-checked panel answer
reader. A stale or withdrawn paragraph is refused. A phone checks `briefAsk`
before using the route; older computers omit that capability.

For a brief question or answer card, the same route also accepts `version`,
`cardId`, and the displayed `cardMessage` in place of the paragraph fields.
The card, its words, revision, and sources are revalidated before returning a
saved answer. Card taps post `ask: true` with the card reference through the
same receipted brief-message route; they do not prepare a Chat composer draft.

An opener can be accompanied by two `opener_answer` send cards in the brief's
optional `cards` array. The existing receipted `brief-opener` response may also
include `message` and `answers`; older clients ignore those fields. Choosing an
answer sends its card as an Ask with the opener's verified source. The opener
itself still posts as the computer's Chat message through the existing route.

With `briefAsk`, a topic-based Your week recommendation can send `weekTopicId`
and `weekMessage` in `brief-messages` with `ask: true`. The computer validates
the current brief revision and recommendation, then resolves the topic through
the normal source checks. The saved-answer route accepts those same fields.

`POST /briefs/{id}/conversation` is the phone's "Talk about this brief" when the
brief on screen has no conversation reference of its own. The phone asks the
computer for that brief's context by its id. It requires `chat:write` and
`brief:read`, takes no body, and answers `{"briefId","briefVersion","chatId"}`:
the brief's delivered `conversationKey` and the main chat, made on the computer
if it does not exist yet. The computer reads the brief through the same
sealed-read gates as `GET /briefs/{id}` and the words Talk already uses. It
never makes a brief (`/brief/refresh` is not called), posts no opener and sends
no words. A brief that is withheld, withdrawn or seen in Closed Eyes gets 409
with the brief-changed message and no conversation; an unknown brief gets 404.
Asking again returns the same answer and the same chat. A computer without the
route answers 404; the phone then shows the same line it shows when the
computer cannot be reached. The phone's own conversation reference still
decides the opener and answer routes that follow.

`GET /status` may advertise `briefWeekDecline`. When present, `GET
/briefs/{id}/week-decline?version={conversationKey}` reads the stored choice;
`POST` to the same path accepts `idempotencyKey` and `version` and stores Not
now for the delivered week. The read requires `brief:read`; the write also
requires `chat:write` and is receipted. A changed or withdrawn brief is refused.
The phone journals an unanswered write under its original key and retries it
when the computer returns. Older computers omit the capability.

The existing `POST /chats/{id}/cancel` accepts an optional `turnId` when
`briefAsk` is advertised. With that field it cancels only the named live Ask,
using the cancellation captured for that turn; a later turn is never stopped
by a delayed request. Omitting the field retains the shipped chat behavior.
An Ask queued with a known-away computer keeps an undispatched marker alongside
its journal entry. Undo removes it only before dispatch is marked; once a
request may have reached the computer, retry uses the same receipt key.

#### 20.7j Calendar holds

`GET /status` may advertise `holds: true` only while calendar access is granted, Offer to hold time is enabled, and the hold coordinator is ready. Older computers omit the capability and may answer 404. All routes below require `handoff:create`.

`GET /holds/propose?source_kind=task|brief|up_next&source_id=...` returns `{proposal, calendars, why, token}`. The proposal contains H1's source, calendar ID, first slot and up to three alternatives; an existing or dismissed hold is reported using H1's fields. `calendars` names writable calendars, with `meetingsAccount` indicating when the busy-to-others line applies. The source is checked through the phone's existing privacy projection. The opaque token binds the exact proposal to this pairing and survives a computer restart; it grants no permission to write.

Proposal, status and new hold requests also need the source's read scope: `brief:read` for task or brief sources, `offers:read` for Up next. Undo and removal continue to work with `handoff:create` after a read scope is withdrawn; their receipt omits source details in that case.

`POST /holds/actions` accepts `{idempotencyKey, action, token?, slot?, calendarId?, holdId?}`. `hold` requires the token, a slot from its proposal and a writable calendar ID. `undo_hold`, `remove_hold` and `undo_remove_hold` require the hold ID. Each action is receipted with the full request bound to the key. Hold rechecks the source and calendar, and H1 persists the intent and waits five seconds before writing; Undo during that interval writes nothing. Removal and its Undo use H1's ownership and edit checks. Responses contain H1's hold status, including conflicts and calendar-unavailable states. `GET /holds/status?id=...` reads a hold's current state, including asynchronous write failure. A failed or expired proposal never authorizes a different time.

The conflict error codes `HOLD_TIME_PASSED`, `HOLD_EDITED`, and `HOLD_FAILED` distinguish an expired queued slot, a Calendar edit that must be left intact, and other failures. The phone supplies localized in-place wording. A previously accepted pending write can also finish with `failureReason` `time_passed` or `slot_unavailable` when its delayed recheck runs.

A capable phone may opt in with `GET /offers?holds=1`; older phones keep the existing filtered list. A hold offer includes a `hold` proposal and the phone displays its actual first slot in the offer title. On acceptance, an Up next hold creates a task thread keyed by the hold ID and returns `taskId` in the status. Its placement is projected as a Done outcome once the calendar write is confirmed; the underlying work remains available for a later Mark as done.

A `conflict` status may add `conflictTitle` and `conflictStart` (RFC 3339), naming the busy event that filled the chosen slot; its `start` and `end` then carry the next free offered slot. A `calendar_unavailable` status names the writable calendar to offer instead in `calendarId`.

Clean-up: when `mark_done` closes a task whose hold is on the calendar and has not started, the task's hold is marked as asked, and the task detail adds `holdCleanup: true` until the person answers. `POST /tasks/{id}/actions` with `keep_hold` ends the asking without touching Calendar; `remove_hold` on `/holds/actions` deletes the event and ends it too, and its `undo_remove_hold` puts the event back without asking again. A hold that has started, or was deleted in Calendar, is never asked about. A phone that finds its queued hold expired (the slot started, or 24 hours passed) reports that locally and never sends it.

### Activity thread outcome addition (v1)

Brief items may add `outcome`: a bounded observed thread end state with `version`,
`kind` (`purchase`, `assistant`, `document`, `research`), `state`, `summary`,
`confidence`, and `evidence` (run/session IDs, `activity_snapshot` or
`activity_event` IDs, observed times, and local frame refs). Optional `nextStep`
and `action` (`order`, `reply`, `schedule`, `review`) describe one proposal;
`question` is used only for an incomplete observation. Assistant outcomes carry
`userActed` (`observed` or `not_observed`) and, when an answer is visible,
`answerRef` identifying its cited frame. These fields never authorize execution.
Raw captured text and order numbers are absent. Outcomes use an opaque compound
`summary` evidence identity; older clients ignore the addition. Delivery rechecks
the current evidence and privacy closure. Highlights retain only the outcome's
single-line title and brief ref. The whole brief shares a 150 to 300-word reading
budget, staying shorter when evidence is sparse and shortening Highlights first.
Each Chinese character uses half a word, including in mixed text. The same text
is read aloud. Outcome version 2 applies both shared secret and generic-code
redaction before extraction, storage and replay. Version 1 projections and their
derived brief/proactive source identities are withheld until re-extracted.



### Brief conversation additions (v1, items 46 and 47)

A brief may include `offers` (at most two objects with `id`, `action`, `label`,
`text`, `prompt`, and evidence `refs`) and `conversationKey` (a revision hash of
its ID/as-of time, written narrative, offers and evidence). The required final
narrative paragraph contains these same offers. Offers are editable chat drafts,
not approval, execution authority or proof of completion. Current privacy
withdrawal clears narrative, offers and audio together. Older readers ignore
both fields. Desktop Today uses `brief` and `brief_offer` Today context refs to
reload the exact stored revision through Go before sending; a changed revision
requires a fresh draft. The phone interaction will be implemented separately.

### Today attention, ages and offers (v1, items 52 and 53)

Brief items may add `since` (RFC 3339): when Isoform first recorded the item (a
Purpose's creation, a thread's first continuation, one-time work's creation, a
reply reminder's creation). Readers name a carried-over item's age by calendar
day in the brief's `timezone` ("since last Thursday", "since October 12").
Items may add `attentionReason`, one short line in the brief's language, and
`attentionRank`: 1 overdue, 2 due today, 3 a follow-up the user added from a
moment in Activity, 4 a person waiting, 5 Isoform's own work waiting on the
user, 6 due within six days, 7 an observed unfinished state (a cart, a draft,
an answer not acted on), 8 recorded progress that stopped three or more days
ago. Both are omitted when the item has none of
this grounding; a title alone never qualifies. Desktop Today lists "What needs
your attention" from these fields (`docs/today-page.md`).

Offer `action` is now `send` (choosing the offer starts the work in main chat)
or `prefill` (an editable draft first). Briefs stored earlier carry values
such as `draft_plan` or `order` in `action`; readers treat any value other
than `send` as `prefill`, except the additive `open_activity` navigation described
in section 8, which is removed from every phone copy. The phone does not read `action` yet and sends an
offer's prompt as written, so every `prefill` prompt itself asks main chat to
wait for the user's confirmation before an irreversible step. Optional `kind`
names what the offer does: `plan`, `reply`, `follow_up`, `order`,
`note_decision`, `prepare` or `review`. `label` names the subject or the
concrete step ("Reply to Dana", "Draft the interview guide"), `prompt` is the
complete request, and `text` is the offer as a question. The final
narrative paragraph is one question naming the offers' subjects, said plainly
for speech; it is the brief's only question. Two offers never share a label or
a subject.

The `day_shape` item in `meetings` carries in `detail` one plain line about time
still ahead today, in the brief's language, with today's clock times and no
weekday. `dayShape.topApps` is now always an empty array: Today and the phone
show what moved, not time per app. Older readers ignore every addition in this
section.

## 21. Remote access over iroh

### 21.1 Roles and identity

The relay runs as a sidecar on the user's computer and binds only to `127.0.0.1`.
Dumbpipe runs beside it and forwards an iroh connection to that loopback listener. The desktop
is the hub and initiates the relay connection; the phone uses the optional invitation `iroh`
ticket to reach the hub from cellular or another network. The phone must not dial the invitation's
a `relay` address when `iroh` is present: its local forwarder maps that connection
to the computer's tunnel endpoint. A missing `iroh` means an explicitly configured HTTPS relay.

The computer stores one random 32-byte node secret as 64 lowercase hexadecimal characters in
`StateDir/node-key.json`, with `version:1` and an RFC 3339 `createdAt`, mode 0600 and atomic
replacement. Dumbpipe receives it only in `IROH_SECRET`; it is not a command-line argument, log
field, invitation, or relay configuration. The ticket format is `endpoint[a-z2-7]{20,}`. The
phone stores the endpoint ticket per hub in its
Keychain. A malformed key file is an error and is not silently replaced. Reset creates a new key,
restarts the tunnel, closes any open pairing, revokes all paired devices through the existing
device-revocation path, and requires each phone to pair again.

### 21.2 Paths and availability

Remote access is off by default until the iPhone companion supports iroh. A
build can opt in with `ISOFORM_DESKTOP_REMOTE_ACCESS=1`; the runtime
`ISOFORM_MOBILE_REMOTE_ACCESS=1` also enables it, and `=0` disables a stamped
default. Until then, the desktop uses its configured or hosted HTTPS relay.

Iroh first attempts a direct path, including UDP hole punching and IPv6 where available, and can
use its n0 relay fallback when a direct path is unavailable. It migrates the connection when a
network changes. If UDP and the iroh relay path are blocked, remote access is unavailable; the
phone may use a separately configured HTTPS relay instead. Remote access does not require the
phone and computer to share Wi-Fi and does not require a third-party account.

The computer must be awake and running Isoform. While it sleeps or the relay sidecar is stopped,
the hub cannot receive requests. The phone keeps its `PendingWriteJournal` and `BriefCache` and
reports that the computer is unreachable; queued writes resume when it reconnects. The tunnel
does not change relay encryption, pairing approval, scopes, or request replay rules.

### 21.3 Address handling and push

The local relay sidecar sets `ISOFORM_RELAY_TRUSTED_PROXIES=none`. It ignores
`X-Forwarded-For` and related forwarded address headers, because traffic reaches it through the
loopback tunnel and those headers are not a trusted client identity.

Push remains optional, and remote access is off by default until the phone companion supports iroh.
Enable it with the desktop build stamp `ISOFORM_DESKTOP_REMOTE_ACCESS=1` or runtime
`ISOFORM_MOBILE_REMOTE_ACCESS=1`; runtime `0` overrides a stamped default.

A self-hosted gateway needs a persistent X25519 private key in
`ISOFORM_PUSH_GATEWAY_KEY_FILE` (base64url, mode `0600`). `-generate-key <path>` creates one and
`-print-public-key` prints its public key; desktop builds stamp that public key alongside the
gateway URL and APNs environment. The gateway never persists hub pairings or grants.

The hub derives `K = HKDF-SHA256(X25519(hubPrivate,gatewayPublic), hubPublic||gatewayPublic,
"isoform-push-gateway hub v1", 32)`. The relay receives only hub ID, public key and `K`; it signs
the exact request body with HMAC-SHA256 and sends `Isoform-Hub-Key`, `Isoform-Timestamp` and
`Isoform-Signature` to `POST /v1/push`. Timestamps are accepted within five minutes and verified
signatures are replay-protected for ten minutes. The gateway validates strict JSON, environment,
payload and a required opaque grant before sending APNs.

After APNs token acquisition, the phone posts `token`, `environment`, `hubId` and `hubPub` to
`POST /v1/register`. The gateway derives a grant key from its private key and mints a grant valid for
180 days. The grant MAC binds the environment, token, hub ID, hub public key
and decimal Unix expiry:
`"isoform-push-grant v1\n" + environment + "\n" + token + "\n" + hubId + "\n" + base64url(hubPub) + "\n" + exp`.
The gateway sends the grant only in a background APNs payload:
`{"aps":{"content-available":1},"isoform":{"kind":"push_grant","hubId":"...","hubPub":"...","grant":"...","expiresAt":"..."}}`.
The phone stores the grant per paired hub and submits it as `gatewayGrant` to `PUT /v1/push/token`.
It re-registers within 30 days of expiry and whenever the relay reports an invalid grant. Gateway
key rotation invalidates grants and all hub MAC keys, requiring registration again. The gateway
returns only push outcomes; it never receives relay ciphertext or tunnel secrets.

## 22. Calendar holds on the phone (H2 placeholder)

The `hold` and `remove_hold` mobile actions are reserved for H2 and are not
implemented in this version. The phone must not send them or advertise a hold
button yet. H2 will define their route, scopes, replay and offline queue
semantics alongside the phone UI. A queued hold must preserve its Undo window,
recheck calendar availability on the computer, and fail when its proposed time
has passed while the computer was away. Removing a hold must only target an
event recorded as created by Isoform and requires the person's action.

## 23. The computer asleep: waits and answers (v1.0.0, 2026-11-07)

A sleeping computer means "later", not "no". Everything the person does on the phone
still works while the computer sleeps; it waits and happens when the computer wakes.

- **Queue.** A phone request sent while the hub is offline is stored by the relay as a `d2h`
  request envelope (section 5, `POST /v1/requests`, which answers 202) and replayed once when the
  hub reconnects (section 6). This is the only queue; the phone and the computer add none. The
  relay holds a request for 24 hours (`RequestTTL`), then drops it unanswered.
- **Once.** Every waiting write carries its `idempotencyKey` (section 15.2). The key is inside the
  sealed request, so the relay never sees it. A retry after wake reuses the key; the stored receipt
  replays the first outcome, so the action runs once. A refused answer (4xx, 5xx) is not stored,
  so the retry runs only if the first did not.
- **Stale sends to other people.** A task action `send` (a drafted reply, or the linked-list send)
  may carry `queuedAt` (RFC 3339 UTC, as for chat in 15.3). If the computer receives it more than
  one hour after `queuedAt`, it does not run the action. It answers `409` with
  `data: {"code":"confirm_send"}`, stores no receipt, and sends nothing. The phone then asks
  "Your computer is awake. Send the reply to Sam now?" with Send and Not now. On Send the phone
  posts a new request with a new key and no `queuedAt`, which runs at once. A `queuedAt` more than
  5 minutes ahead of the computer's clock is refused with 400, as in 15.3.
- **Expired.** A request the computer has not received within 24 hours is gone from the relay.
  The phone notices at its next open, from its own journal time (it uses no timer), and shows the
  line in the place the request is:
  - In Chat, under the message: "Your computer didn't wake, so this wasn't sent." with Send again as
    a text link.
  - On a Needs you card row: the same line, with the row's buttons back.
  - On the Tasks row of a held reply: the same line under the row, with Send again as a text link. Send
    again takes the reply back out of the journal and sends it as a new send, once the relay has let the
    first one go (a day after it was sent).
  - On Today, under the away block: one line with Show. When the only held send is one reply in Tasks, it
    names the person it is to, "Your computer didn't wake, so your reply to Sam wasn't sent." (without a name:
    "your reply"). Otherwise it counts them: "your message" for one, "so 2 messages weren't sent." for several.
    Show opens the oldest held send: a Chat message in Chat, scrolled to it, or a reply in Tasks, at its row.
  Nothing is sent again until the person says so; the words stay in the phone's journal until then.
- **Answers.** The answer to a chat message that waited is an `chat_answer` notification with the
  body "Isoform answered." (zh-CN "Isoform 已回复。"). It carries no answer text and no count. The
  phone opens Chat for the answer, which is read through the normal chat route.
- **Away line.** Chat says "since" only with the relay's goodbye time, the same rule Today follows (P133). Without one, Chat's line has no time: "Your computer is asleep. Isoform answers when it wakes." (zh-CN "你的电脑在睡眠。它醒来后 Isoform 就会回答。"). Today keeps "as of" with its snapshot time.
- **A reply's name.** A held reply is named by the draft's `name` (above) in Today's line ("Your computer didn't wake, so your reply to Sam Lee wasn't sent.") and in the ask-again prompt ("Your computer is awake. Send the reply to Sam Lee now?"). Without a name the words say "your reply" or "the reply".
- **Waiting state.** Every message or row that waits for the computer reads "Waits for your computer ·
  Undo" (zh-CN "等待电脑连接 · 撤销"), with Undo as the accent link. A message from a brief carries the same
  line. A Needs you row keeps its place and shows the line in place of its buttons, and the date line's
  count leaves it out while it waits. The phone decides this from its journal and the relay's presence;
  nothing new is sent to the phone for it.
- **Not changed.** Chat, brief-opener and "Talk about this brief" routes, the brief made ahead
  (a later version), keep-awake settings and the relay's ciphertext-only rule.

### Agent of You owner snapshots

The sealed routes `GET /agent-owner/status`, `GET /agent-owner/jobs`,
`GET /agent-owner/job?id=<id>`, and `GET /agent-owner/balance` require
`brief:read`. The status includes the server's owner settings; jobs returns its
cached list, job returns one cached detail, and balance returns the cached ledger.
Each returns `{ "available": boolean, "data": <server snapshot or null> }`
inside the normal response envelope. The computer starts a background refresh;
an unavailable first snapshot has `available: false`. Responses never include
reservation credentials or the approved profile text. An invalid job id returns
400, and an unavailable identity or store returns 503.

`POST /agent-owner/decision` requires both `brief:read` and `chat:write` and
accepts `{ "action": "accept|decline|send|redo", "jobId": string,
"passId": string, "decisionVersion": number, "note": string? }`. The pass
and version must come from the card the owner saw. On success its data contains
`accepted: true` and the server result. A stale card returns 409 with
`error: "stale_decision"` and data containing `accepted: false` and `current`
(`passId`, `state`, `decisionVersion`, or null). Older servers without decision
versions cannot receive a decision through this route.

`POST /agent-owner/action` requires the same scopes and accepts an `action` of
`pause`, `on`, `agree`, or `settings`. `agree` also needs `termsVersion` after
the owner has seen the terms; `settings` carries the owner `settings` object.
Turning on requires an agreement recorded by the server. These writes are not
automatically retried after an ambiguous network failure.
