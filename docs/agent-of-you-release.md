# Agent of You release plan

Written 2026-11-13. Target: release next week. Owner: the Agent of You server session, reporting to the hub. This replaces the "Before launch" part of `docs/aoy-launch.md`; that file still holds the full settings table.

## What v1 is

- **One agent, run by Isoform.** The first agent is a persona built only from public material about a public figure (Bo suggested "elon"). It is labelled everywhere as an AI agent: the public agent data carries `persona.disclosure`, for example "An AI agent built from public material about Elon Musk. It is not Elon Musk and is not affiliated with or endorsed by them.", and every model prompt for it says it is not the person, must not claim to be them, and must not invent quotes or private facts.
- **Invite only.** The 75 people on the waitlist are invited. Only an invited email can get a sign-in code on the website; everyone else sees "This beta is open to invited people only". No email goes to an address that is not invited.
- **Real work, real sandbox.** A visitor describes a job, gets a quote, pays a capped hold by card, the agent works in an isolated sandbox, Isoform checks the result, and the visitor downloads the files and a receipt. This is the flow the website already passed 17 of 17 against the local server.
- **Later, not v1:** owners' own agents (the desktop owner screens exist; they open after v1), auto-send, a free tier.

## The user flow

1. Isoform sends an invite (Bo or the hub, by hand, after the hub's OK; never automatic).
2. The person opens `isoform.ai/agents/elon`, sees the AI-agent label, and writes what they need, with files.
3. They enter name and email and get a 6-digit code. Not invited: a plain refusal, no code.
4. The agent asks up to ten clarifying replies, then shows a quote: minutes, rate, minimum, and a spending limit.
5. They add a card for this chat (Stripe) and press Go. A hold for the limit is placed.
6. The agent works in its sandbox. They can Stop at any time and see what stopping costs.
7. Isoform checks the result. Files and a receipt appear in the chat and by email; only the used amount is charged and the rest of the hold is released.

## Hosting

**Choice: one Fly.io Machine that runs its own Docker engine with gVisor.** Fly Machines are full virtual machines, so the existing, already tested Docker sandbox driver runs unchanged inside it: one container, internal network and volume per chat, a separate egress proxy that allows only the model proxy, no credentials in the sandbox, and gVisor (`runsc`) as a second kernel boundary for every chat container. The image is `server/fly/Dockerfile`.

Why not the others for v1:
- **Runta:** we have credit, but its API is unconfirmed (two different providers use the name) and an adapter would need its own isolation proof. Keep as the next step when one Machine is not enough.
- **Cloudflare Containers:** no Docker engine inside, so it would need a new sandbox driver and a separate home for the database and files.
- **Fly Machine per chat:** stronger isolation, but Fly has no egress firewall for the "model proxy only" rule; it needs a new driver.

**Cost (estimates, Fly prices from 2026-10-01):**

| Item | Monthly |
| --- | --- |
| shared-cpu-4x, 8 GB, always on | about $51 |
| 40 GB volume | about $6 |
| Volume snapshots, outbound traffic | a few dollars |
| **Hosting total** | **about $60** |
| Model cost | paid per job by the client's hold; capped for Isoform at $50 per 24 hours |

Capacity: each chat sandbox gets 2 CPUs and 2 GB; four jobs run at once. That is enough for 75 invited people. Grow by raising the Machine size (one line in `server/fly.toml`).

Sources: [Fly.io pricing](https://fly.io/docs/about/pricing/), [Fly 2026 pricing update](https://fly.io/pricing-update/).

## Abuse and cost limits

Already in the server: per-client daily chat limits, 10 clarifying replies per cycle, rate limits per client and per network address on every route and on email codes, upload size and type checks (optional antivirus), a per-pass model cost cap at half the hold, a hard spending limit per pass, Stop with a signed cost figure, owner approval and review controls, emergency stop.

Added for the release:
- **Invite gate** (`ISOFORM_INVITE_ONLY=1`). The list stores only keyed digests of emails, never readable addresses. The check runs after the code rate limits, so the list cannot be probed faster than codes can be requested.
- **Daily model cost cap** (`ISOFORM_DAILY_MODEL_CAP_CENTS`, set to 5000 = $50 in `fly.toml`). Once that much model cost has been spent in the last 24 hours (work, clarifying replies and checking, counted when spent; the window rolls, so nothing resets at midnight), new model requests for work and new hires stop with a clear message. Clarifying replies already under way still finish; they are bounded by the daily chat limits.
- **Disk guard** (`ISOFORM_MIN_FREE_DISK_MB`, default 2048). Below that free space on the volume, new hires and uploads are refused and running work is stopped, so one job or a flood of uploads can't fill the disk the database shares. `check-live.sh` warns when free space nears it.
- **Real client addresses behind Fly** (`ISOFORM_CLIENT_IP_HEADER=Fly-Client-IP`), so rate limits apply per visitor instead of to Fly's proxy.
- **Private model proxy** (`ISOFORM_LLM_ADDR`). The model proxy listens only on a host-only address inside the Machine (`10.255.255.1`, a dummy interface outside every Docker network); the egress proxy reaches it as `host.docker.internal`, and the public address answers 404 for it.
- **gVisor runtime** (`ISOFORM_SANDBOX_RUNTIME=runsc`); a sandbox created without it is refused. gVisor runs with `--network=host`, which means its network calls use the Machine kernel inside each chat's own network namespace. That is needed for Docker's name lookup of the egress proxy; the chat network rules are unchanged and the test below checks them. The rest of gVisor's kernel boundary stays in place.

## Privacy

- The persona is built only from public material, and the public label says it is an AI agent.
- Invite list: digests only. Logs omit tokens, emails and email contents (unchanged).
- Client files and outputs live on the Fly volume; retention is 30 days after a chat closes (unchanged). Volume snapshots follow the same rule: Fly keeps 5 daily snapshots by default, so deleted data is gone from snapshots after 5 days. The privacy page must say so.
- The model key stays in the server; sandboxes reach the model only through the private proxy with a per-pass token.
- Open: the draft Terms and Privacy pages need a legal read before invites go out.

## Tested without accounts

- `cd server && go vet ./... && go test -race -count=1 ./...`: the whole server, including new tests for the invite gate over HTTP, invite import (CSV or one email per line), probe rate limiting, the proxy address header, the daily cap, the hosted settings, and the persona label and prompts.
- `server/scripts/e2e-fly-local.sh`: builds the Fly image and runs it on this computer as a stand-in for the Fly Machine. It runs the Docker egress isolation test inside the image's own Docker engine with gVisor (chat to chat blocked directly and through the proxy, private and other host ports blocked, public HTTPS and the model proxy allowed), boots the server as on Fly, and checks health, that the model proxy is not public, that the private listener refuses requests without a pass token, that the server runs unprivileged, the egress proxy, and the invite list. No model calls, no money.
- The website's full chat flow against the local server (`server/scripts/run-dev.sh`) passed 17 of 17 earlier (#792).

Last local run on 2026-11-13: both phases PASS. Not yet proven on a real Fly Machine; the entrypoint stops the boot if gVisor cannot start a container, so a failure there shows in `fly logs` instead of running jobs without it.

## What remains

1. **Persona content.** Write the persona's profile lines and public specialties from public sources only, with a source list kept beside it. Isoform design names the page and the label wording. A legal read of using a real person's name is needed first (right of publicity, trademark); "elon" is a placeholder until that read is done.
2. **Website:** show `persona.disclosure` on the agent page and in the chat, and show the `not_invited` message (website session and Isoform design).
3. **First real job on Fly** with Bo's test card in Stripe test mode, then live mode.
4. **Monitoring:** Fly health alerts to Bo's email and a daily look at receipts and the cost cap.
5. **Runta** adapter when one Machine is not enough.

## Needs Bo

Each step is one line; run commands from `server/` in your own terminal. None of these is done by an agent.

1. Fly: `fly auth login`, then `fly apps create isoform-aoy-server --org <your org>`.
2. Fly volume: `fly volumes create isoform_data --region sjc --size 40 --app isoform-aoy-server`.
3. Server secret: `read -rs 'v?Value: '; printf 'ISOFORM_SERVER_SECRET=%s\n' "$v" | fly secrets import --stage; unset v` (paste the output of `openssl rand -hex 32`).
4. Anthropic key (a new key just for this server, with a monthly limit set in the Anthropic console): `read -rs 'v?Value: '; printf 'ANTHROPIC_API_KEY=%s\n' "$v" | fly secrets import --stage; unset v`.
5. Resend: verify the sending domain in the Resend dashboard, then `read -rs 'v?Value: '; printf 'RESEND_API_KEY=%s\n' "$v" | fly secrets import --stage; unset v`.
6. Stripe test mode: create webhook endpoints `https://isoform-aoy-server.fly.dev/v1/stripe/webhook` and `.../v1/stripe/connect-webhook` in the dashboard, then import `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET` and `STRIPE_CONNECT_WEBHOOK_SECRET` the same way as step 3 (`sk_test_` key).
7. Tell the hub "deploy AoY"; the hub runs `ISOFORM_DEPLOY_APPROVED=yes server/scripts/deploy.sh`. The first boot builds the sandbox image (a few minutes). Then the hub runs `server/scripts/check-live.sh`: it reads health, the Machine and volume, the invite gate, the cost cap, gVisor, the private model proxy and the persona label, and changes nothing (add `--probe` once to see an uninvited address refused).
8. Website: in Cloudflare Pages settings, set `AOY_API_ORIGIN=https://isoform-aoy-server.fly.dev` and `AOY_CHAT=1`.
9. Optional custom domain: `fly certs add api.isoform.ai --app isoform-aoy-server`, add the CNAME it prints in Cloudflare DNS, then change `ISOFORM_SERVER_PUBLIC_URL` in `server/fly.toml`.
10. Persona owner: reserve the agent name on isoform.ai from Isoform's own account and approve its profile in the desktop app, then on the server `fly ssh console -C "sh -c 'ISOFORM_SERVER_DATA=/data/server setpriv --reuid 65532 --regid 65532 --init-groups isoform-server admin persona set elon \"Elon Musk\"'"`.
11. Invites: export the waitlist emails from Cloudflare D1 to `waitlist.txt` on your computer (one email per line, or the CSV export as is). Upload it with `fly ssh sftp shell` then `put waitlist.txt /data/server/waitlist.txt`, run `fly ssh console -C "sh -c 'ISOFORM_SERVER_DATA=/data/server setpriv --reuid 65532 --regid 65532 --init-groups isoform-server admin invite import /data/server/waitlist.txt && rm /data/server/waitlist.txt'"`, and check `... admin invite count` prints 75. This emails no one, and the server keeps only digests.
12. Run one real job with a Stripe test card; then switch to live keys (`sk_live_`, plus `ISOFORM_STRIPE_LIVE=1`).
13. Decide who sends the invite emails and when; nothing in the server sends them.
