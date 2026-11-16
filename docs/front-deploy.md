# Deploying the Isoform front and its DNS

The front (`front/`, design: `docs/design/mcp-public-front.md`) answers every `*.agents.isoform.ai` connection. It reads only the hostname in the TLS ClientHello and passes the connection, still encrypted, to that person's computer over iroh. TLS ends on the computer, which holds its own Let's Encrypt certificate (autocert, on the computer, in `<data>/front/acme`). The front holds no certificate and no person's data. The only thing it keeps is its own iroh key (`/data/front.key` on its Fly volume), whose id the computers accept.

Bo runs every command here, signed in to Fly and Cloudflare himself. No agent runs them and no key passes through an agent.

## What is in the repo

| File | What it does |
|---|---|
| `front/Dockerfile` | Builds `isoform-front` with Rust 1.93.1 (the pinned toolchain) into a distroless image. |
| `front/fly.toml` | App `isoform-front`, one machine in `sjc`. Port 443 is raw TCP (no TLS at Fly), with a PROXY v2 header so the front's per-client limits see the client, not Fly's proxy. Volume `isoform_front_data` at `/data` for the key. |
| `front/scripts/deploy.sh` | `fly deploy`, behind `ISOFORM_DEPLOY_APPROVED=yes`, like `server/scripts/deploy.sh`. |

## Order

Fly first (the DNS records need the front's addresses), then DNS, then the front's id goes to the hub.

### 1. Create the app and its volume (once)

```bash
fly apps create isoform-front
```

```bash
fly volumes create isoform_front_data --app isoform-front --region sjc --size 1
```

### 2. Deploy

```bash
ISOFORM_DEPLOY_APPROVED=yes front/scripts/deploy.sh
```

### 3. Give it its own addresses (once)

A dedicated IPv4 is needed for raw TCP on 443. Fly bills it, about $2 a month. The IPv6 is free.

```bash
fly ips allocate-v4 --app isoform-front
```

```bash
fly ips allocate-v6 --app isoform-front
```

```bash
fly ips list --app isoform-front
```

Note the `v4` and `v6` addresses. Don't use the shared ones.

### 4. Read the front's id

```bash
fly logs --app isoform-front --no-tail | grep 'front endpoint id'
```

Send the 64-character id to the hub. It's public, not a secret. A follow-up PR builds it into the app, so computers accept this front (today it comes from `ISOFORM_FRONT_IDS`). The same PR moves the computer's certificates from Let's Encrypt staging to production.

### 5. DNS in Cloudflare (zone `isoform.ai`)

| Type | Name | Content | Proxy status | TTL |
|---|---|---|---|---|
| A | `*.agents` | the dedicated v4 from step 3 | DNS only (grey cloud) | Auto |
| AAAA | `*.agents` | the v6 from step 3 | DNS only (grey cloud) | Auto |

- **DNS only, never proxied.** Cloudflare's proxy would terminate TLS, which breaks the design: the computer must end TLS itself.
- **CAA.** If the zone has CAA records, add `isoform.ai CAA 0 issue "letsencrypt.org"`, or the computers can't get certificates. With no CAA records at all, nothing is needed.
- No record is needed for `agents.isoform.ai` itself; only the wildcard.

### 6. Check

```bash
dig +short test.agents.isoform.ai A
```

```bash
dig +short test.agents.isoform.ai AAAA
```

Both should print the front's addresses. Then a TLS attempt to a made-up label should connect and be closed by the front without an answer, because no computer has that label:

```bash
openssl s_client -connect test.agents.isoform.ai:443 -servername aaaa.agents.isoform.ai </dev/null
```

The front's minute line in `fly logs --app isoform-front` counts it as `bad_label`.

## Later

- **A second machine** (`fly scale count 2`) gets its own volume and so its own key. Computers must accept both ids, so send the hub both before scaling.
- **Let's Encrypt limits.** Each computer has its own certificate under `isoform.ai`. Past about 50 new computers a week, the "certificates per registered domain" limit applies. Request a rate-limit override from Let's Encrypt before launch (on the Needs Bo list).
- **The Public Suffix List** (listing `agents.isoform.ai` so browsers keep each person's hostname apart) is still Bo's decision; it doesn't block DNS.
