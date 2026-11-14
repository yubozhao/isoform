# Hosting the relay

The iPhone reaches the Mac through the relay (`relay/`): a store-and-forward
broker that holds credential digests, ciphertext and routing metadata only
(`relay/README.md`, `mobile-protocol/PROTOCOL.md`). The phone must be able to
reach it at any time, so it runs on an always-on host at a stable address,
not on the developer's Mac behind a tunnel (`docs/relay-tunnel.md`).

This directory holds two equivalent deployments of the same image
(`relay/Dockerfile`):

| File | Host | TLS | Client address |
| --- | --- | --- | --- |
| `fly.toml` | Fly.io, one `shared-cpu-1x` machine with a volume | Fly's edge; the relay listens in plaintext on the private network (`ISOFORM_RELAY_BEHIND_TLS_PROXY=1`) | `Fly-Client-IP` from the Fly proxy (`fdaa::/16`, `172.16.0.0/12`) |
| `docker-compose.yml` + `Caddyfile` | any VPS with Docker | Caddy, Let's Encrypt, in the relay's network namespace | `X-Forwarded-For` from Caddy on loopback |

Both give the relay a persistent state volume, a health check on
`/v1/health`, WebSocket pass-through for `/v1/hub/ws`, resource limits, and a
`SIGTERM` with a grace period long enough for the relay's shutdown. Neither
holds a secret: `.env.example` names every variable with a placeholder.

`smoke-test.sh` builds the image, runs it the Fly way (plaintext behind a
trusted proxy, JSON logs) and checks health, the anonymous WebSocket upgrade
(401), an authenticated upgrade (101), the browser-origin refusal (403) and a
clean `SIGTERM` shutdown. Run it before a deploy: `relay/deploy/smoke-test.sh`.

## Environment variables

All of them are documented in `relay/README.md` ("Configuration") and listed
with placeholders in `.env.example`. The ones a hosted deploy sets:

| Variable | Fly | Compose | Meaning |
| --- | --- | --- | --- |
| `ISOFORM_RELAY_ADDR` | `0.0.0.0:8080` | `127.0.0.1:8791` | Listener. Compose keeps loopback because Caddy shares the namespace. |
| `ISOFORM_RELAY_STATE_DIR` | `/var/lib/isoform` | `/var/lib/isoform` | The volume. Holds `relay.sqlite` and the attachments. |
| `ISOFORM_RELAY_BEHIND_TLS_PROXY` | `1` | unset | Plaintext off loopback because the named proxy terminates TLS. |
| `ISOFORM_RELAY_TRUSTED_PROXIES` | `fdaa::/16,172.16.0.0/12` | default (loopback) | Peers whose client header is believed. |
| `ISOFORM_RELAY_CLIENT_IP_HEADER` | `Fly-Client-IP` | default (`X-Forwarded-For`) | Where the proxy puts the client address. |
| `ISOFORM_RELAY_LOG_FORMAT` | `json` | `json` | One JSON object per line for the log collector. |
| `ISOFORM_RELAY_MAX_CONNS` | `4096` | `4096` | Open connections per process. |
| `ISOFORM_RELAY_APNS_*` | `fly secrets set` | env file | The five APNs settings, all-or-nothing. Push wakes phones for a new brief; without it they poll when opened. |
| `ISOFORM_RELAY_SMS_*`, `ISOFORM_RELAY_TWILIO_*` | `fly secrets set` | env file | Optional text messages (`docs/sms-channel.md`). |
| `ISOFORM_RELAY_EMAIL_*`, `ISOFORM_RELAY_SMTP_*` | `fly secrets set` | env file | Optional email (`docs/email-channel.md`). |

Secrets never go in `fly.toml`, the compose file or the checkout. On Fly they
are `fly secrets`; on a VPS they live in an env file outside the checkout
(mode 0600) that `ISOFORM_RELAY_ENV_FILE` points at.

## Fly.io

### First deploy

```sh
cd relay
fly apps create isoform-relay                       # once; pick the name in fly.toml
fly volumes create relay_state --region sjc --size 3 --app isoform-relay   # once
fly deploy --config deploy/fly.toml --dockerfile Dockerfile .
fly certs add relay.example.com                   # then point a CNAME at the app
```

`fly deploy` runs from `relay/` so the Docker build context is the relay
module alone, as the Dockerfile expects. The health check in `fly.toml`
polls `/v1/health` every 15s; a deploy is rolled back if the new machine
never passes it.

The relay runs as UID 10001 and needs to own the mount point. If the first
start logs `unable to open relay state`, fix the volume's owner once:

```sh
fly ssh console --app isoform-relay -u root -C 'chown 10001:10001 /var/lib/isoform'
fly machine restart --app isoform-relay
```

### APNs on Fly

The `.p8` key is a file; Fly secrets are environment variables. Store the
key base64-encoded in a secret and write it to the volume at start, or copy
it onto the volume once:

```sh
fly ssh sftp shell --app isoform-relay
put AuthKey_ABCDE12345.p8 /var/lib/isoform/apns-key.p8
fly ssh console --app isoform-relay -u root -C 'chown 10001:10001 /var/lib/isoform/apns-key.p8 && chmod 0600 /var/lib/isoform/apns-key.p8'
fly secrets set --app isoform-relay \
    ISOFORM_RELAY_APNS_KEY_FILE=/var/lib/isoform/apns-key.p8 \
    ISOFORM_RELAY_APNS_KEY_ID=ABCDE12345 ISOFORM_RELAY_APNS_TEAM_ID=TEAMID1234 \
    ISOFORM_RELAY_APNS_TOPIC=app.yansu.ios ISOFORM_RELAY_APNS_ENVIRONMENT=production
```

The key is then inside the state volume, so a volume backup contains it:
treat backups as secrets.

### Update

```sh
cd relay && fly deploy --config deploy/fly.toml --dockerfile Dockerfile .
```

With one machine and one volume the deploy is a restart: the hub socket and
any long-poll drop for the seconds the new machine takes to pass its check.
The Mac reconnects on its own (its backoff is in `wails-gui`), and phones
retry their poll; queued envelopes wait on the volume. Deploy when phones are
least likely to be mid-request, and never during a pairing.

### Back up and restore the state volume

Everything the relay knows is in `/var/lib/isoform`: `relay.sqlite` (with its
`-wal` and `-shm` files) and the attachments directory. SQLite's online
backup produces a consistent copy while the relay runs:

```sh
fly ssh console --app isoform-relay -u root -C \
    'apk add --no-cache sqlite >/dev/null && sqlite3 /var/lib/isoform/relay.sqlite ".backup /tmp/relay-backup.sqlite"'
fly ssh sftp get /tmp/relay-backup.sqlite ./relay-$(date +%F).sqlite --app isoform-relay
```

Fly also snapshots volumes daily (`fly volumes snapshots list <volume id>`;
restore with `fly volumes create relay_state --snapshot-id <id>`). A snapshot
taken mid-write is as consistent as a power loss: SQLite recovers it from
the WAL on open.

To restore: stop the machine, replace the database, start it.

```sh
fly machine stop <machine id> --app isoform-relay
fly ssh sftp shell --app isoform-relay      # a stopped app still accepts sftp to a started machine; start, copy, restart
put ./relay-2026-10-25.sqlite /var/lib/isoform/relay.sqlite
fly ssh console --app isoform-relay -u root -C 'rm -f /var/lib/isoform/relay.sqlite-wal /var/lib/isoform/relay.sqlite-shm && chown 10001:10001 /var/lib/isoform/relay.sqlite'
fly machine restart <machine id> --app isoform-relay
```

A backup is sensitive even though it holds no plaintext: credential digests
let nobody in, but the APNs key (if stored there) does, and the ciphertext
queues are the users' data. Encrypt backups at rest and keep few.

### Monitor

- `fly status` and `fly checks list --app isoform-relay`: the machine and the
  `/v1/health` check.
- `fly logs --app isoform-relay`: JSON lines, one per event. Startup logs
  `Isoform relay listening on ...` with the features enabled; maintenance
  logs row counts; shutdown logs `Isoform relay shutting down`. No line
  carries an address, a credential, a device name or ciphertext.
- An external uptime monitor on `https://relay.example.com/v1/health`
  (200 and `"ok":true`) is the only check that sees what a phone sees.
  Alert after two misses a minute apart.
- `fly volumes list`: size and used space. The relay bounds its own growth
  (200 queued envelopes per device and direction, 7 briefs per device, 28
  attachments and 96 MiB per hub, 2 GiB of attachments in all, a WAL
  checkpoint every hour), so the volume fills only with more hubs.

## Any VPS with Docker Compose

A 1 vCPU, 1 GB host is plenty. Open ports 80 and 443 in its firewall, point
an `A`/`AAAA` record at it, and:

```sh
git clone <repo> && cd <repo>
sudo install -d -m 0700 /srv/isoform-relay
sudo install -m 0600 relay/deploy/.env.example /srv/isoform-relay/relay.env   # then edit it
export RELAY_DOMAIN=relay.example.com CADDY_ACME_EMAIL=ops@example.com
export ISOFORM_RELAY_ENV_FILE=/srv/isoform-relay/relay.env
docker compose -f relay/deploy/docker-compose.yml up -d --build
docker compose -f relay/deploy/docker-compose.yml ps     # relay: healthy, caddy: running
curl -fsS https://relay.example.com/v1/health
```

Caddy requests the certificate on first start and renews it itself; its
state lives in the `caddy-data` volume. It forwards `/v1/hub/ws` upgrades
as WebSockets and replaces any `X-Forwarded-For` a client sent with the
client's address, which the relay (whose trusted proxy is loopback by
default) counts limits by.

For APNs, put the `.p8` key at a path outside the checkout (owner UID 10001,
mode 0600), export `ISOFORM_RELAY_APNS_KEY_HOST_FILE=<that path>`, and set the
five `ISOFORM_RELAY_APNS_*` variables in the env file with
`ISOFORM_RELAY_APNS_KEY_FILE=/run/secrets/apns-key.p8`.

### Update

```sh
git pull
docker compose -f relay/deploy/docker-compose.yml up -d --build
```

The Compose file pins the relay and Caddy volumes to their existing
`yansu-relay_*` names so the project rename preserves their state.

Compose recreates only the relay container when its image changed; Caddy
and the certificates stay. Downtime is the relay's restart, a few seconds.

### Back up and restore

```sh
# Back up: a consistent copy from inside the running container's volume.
docker run --rm -v isoform-relay_relay-state:/state:ro -v "$PWD":/out alpine \
    sh -c 'apk add --no-cache sqlite >/dev/null && sqlite3 /state/relay.sqlite ".backup /out/relay-$(date +%F).sqlite" && tar -C /state -czf /out/attachments-$(date +%F).tgz attachments 2>/dev/null || true'

# Restore: stop the relay, replace the database, start it.
docker compose -f relay/deploy/docker-compose.yml stop relay
docker run --rm -v isoform-relay_relay-state:/state -v "$PWD":/in alpine \
    sh -c 'rm -f /state/relay.sqlite-wal /state/relay.sqlite-shm && cp /in/relay-2026-10-25.sqlite /state/relay.sqlite && chown 10001:10001 /state/relay.sqlite && chmod 0600 /state/relay.sqlite'
docker compose -f relay/deploy/docker-compose.yml start relay
```

### Monitor

`docker compose ps` shows the relay's health (the image's own check on
`/v1/health`); `docker compose logs -f relay` streams the JSON log lines;
`docker system df -v` shows the volume's size. Add the same external uptime
monitor as for Fly.

## Rotate credentials

Nothing in the relay's own configuration is a credential a client holds, so
rotation is per party:

- **Hub credential** (the Mac's bearer for `/v1/hub/ws`): the hub's key
  rotation in the desktop app (`wails-gui`, "rotate" in the mobile
  settings) presents the new credential; the relay stores only its digest.
  A relay operator cannot rotate it for the hub.
- **Device bearer** (a phone's credential): the hub revokes the device and
  the phone pairs again; revoked digests stop working at once.
- **APNs key**: create a new key at developer.apple.com, put it where the
  old one was, update `ISOFORM_RELAY_APNS_KEY_ID`, restart the relay, then
  revoke the old key at Apple. One process uses one key.
- **Text message and email keys**: `ISOFORM_RELAY_SMS_KEY_PREVIOUS` and the
  steps in `docs/sms-channel.md`; the email key is documented in
  `docs/email-channel.md`.
- **Twilio and SMTP passwords**: rotate at the provider, update the secret,
  restart.
- **Host access**: `fly tokens` or the VPS's SSH keys; the relay has no
  admin API.

The relay never logs a credential, so a leaked log forces no rotation.

## What an outage means for phones

The relay is the only path between a phone and its Mac, so while it is down:

- **Nothing is lost that was already stored.** Queued requests (phone to
  Mac), queued responses and events (Mac to phone), the kept briefs and
  attachments are on the volume and are delivered when the relay returns,
  within their own TTLs (`relay/README.md`: requests expire after an hour,
  200 envelopes per device and direction, 7 briefs).
- **The phone shows its last brief** and cannot refresh, ask the Mac or
  pair. Its requests queue on the phone only as far as the app keeps them;
  a request made during the outage fails and the app says so.
- **The Mac's hub socket drops** and the desktop app reconnects with
  backoff; it keeps working locally and queues nothing on its side for the
  phone, so an event that happens during the outage reaches the phone only
  if the Mac re-sends it (briefs are re-published; one-off events are not).
- **Push notifications stop**: the relay is what calls APNs. Phones get no
  wake-up for briefs produced during the outage; they see them on next open.
- **Text messages and email** bounce at the provider and are not retried
  by it beyond its own policy.
- **Pairing is impossible**: an invitation shown during the outage expires
  unused.

Health is `GET /v1/health`: 200 means the process and its database answer.
A relay that answers but whose volume is full logs `maintenance pass
failed` and refuses writes with 500s; free space or grow the volume.

## Cost

| Item | Fly.io | VPS |
| --- | --- | --- |
| Compute | `shared-cpu-1x`, 512 MB: about US$3.20/month (Fly's published per-second price, October 2026) | Hetzner CX22 or equivalent 2 vCPU/4 GB: about EUR 4/month; DigitalOcean 1 GB droplet US$6/month |
| Storage | 3 GB volume: about US$0.45/month (US$0.15/GB) | included |
| Bandwidth | first 100 GB/month free in North America and Europe; the relay moves kilobytes per request | included (20 TB on Hetzner) |
| Public IPv4 | shared IPv4 is free; a dedicated one is US$2/month (not needed) | included |
| TLS | included | Let's Encrypt, free |
| **Total** | **about US$4/month** | **about US$5-6/month** |

Both are far below the first paid tier of a managed WebSocket service, and
the relay's own limits (`relay/README.md`) keep a single machine enough for
thousands of hubs: a hub socket is one idle connection with a ping every
30s, and a phone's long-poll is one connection for up to 25s.

## The relay on this computer (launchd)

The development relay runs on the developer's computer behind a zrok public share
(a reserved zrok public share name). `macos/isoform-relay-host.sh` keeps both alive across
restarts with two launchd agents in the user's session:

| Agent | Runs | Log |
| --- | --- | --- |
| `ai.isoform.relay` | the relay, built once into `~/isoform-relay-state/bin`, on `127.0.0.1:8791` | `~/isoform-relay-state/relay.out` |
| `ai.isoform.relay-share` | `zrok share public --headless -n public:isoform-relay`, after the relay answers `/v1/health` | `~/isoform-relay-state/zrok.out` |

Both restart when they exit. Before sharing, the share agent checks who holds the name. A share
from this computer's zrok environment that no running zrok process serves is stale: it is left
over from a process that died with the computer. The agent deletes it, then shares. A share from
another environment, or one a running process still serves, is left alone, and the agent retries
in five minutes.

```bash
relay/deploy/macos/isoform-relay-host.sh install --dry-run   # print the agents and what install would do
relay/deploy/macos/isoform-relay-host.sh install             # build the relay, write and load the agents
relay/deploy/macos/isoform-relay-host.sh status              # agent state, local and public health
relay/deploy/macos/isoform-relay-host.sh release-stale-share --dry-run   # which share a release would delete
relay/deploy/macos/isoform-relay-host.sh uninstall           # unload and remove the agents; state is kept
```

Install refuses while a relay or zrok share started by hand is running, and names their PIDs: stop
those first. It never stops a process it did not start. Re-run `install` to pick up a newer relay.
The agents start when the user logs in, which a FileVault disk needs after a restart anyway.
Extra relay settings go in `~/isoform-relay-state/relay.env`, so the plists hold no secret; the zrok
account token stays in `~/.zrok2`.

`macos/test-isoform-relay-host.sh` checks the script with the real relay binary, a temporary HOME,
and recorders in place of `zrok` and `launchctl`, so it never touches the account or the installed
agents.
