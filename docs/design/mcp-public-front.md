# A public HTTPS front for the person's Isoform MCP

Design for step 2 of "Work on it" (`docs/work-on-it-agents.md`), 2026-11-14. Cloud agents (Claude Duet and Claude custom connectors, Meta Muse, Grok Bot, ChatGPT and dots) reach MCP servers from their own clouds over public HTTPS with OAuth [A1][A2][A3][A4]. Isoform's data lives on the person's computer. This doc decides how a cloud agent reaches it.

## Recommendation

Build **Isoform front**: a small gateway that Isoform runs. It reads only the hostname in the TLS handshake, then passes the still-encrypted bytes to the person's computer over iroh, the same peer-to-peer network the phone link already uses (`wails-gui/internal/remoteaccess`, Dumbpipe). TLS ends on the computer, which holds its own certificate and runs its own OAuth. The gateway stores nothing and can't read requests, tokens or tasks.

The two alternatives both end TLS at someone else's edge, so that edge sees every task and token in plain text:

- a zrok-style tunnel [Z1];
- a Cloudflare Tunnel per person [F2].

The Cloudflare option also costs $4.50 per tunnel a month after the first 1,000 [F1].

## How it works

```
cloud agent ──TLS──► front (reads SNI only) ──iroh QUIC──► person's computer
                         │                                   │ TLS ends here
                         │ stateless: hostname = endpoint id │ OAuth server + MCP server
```

1. **Hostname per person.** `<label>.agents.isoform.ai`, where `<label>` is the base32 form of a dedicated iroh endpoint id that the computer makes just for this front: 32 bytes is 52 characters, which fits a DNS label. It is not the phone link's endpoint, whose ticket stays secret. A wildcard DNS record points every label at the front. The name carries no identity, only a public key.
2. **Front.** It listens on TCP 443 and parses the ClientHello far enough to read the SNI. From the label it gets the endpoint id, dials that endpoint over iroh (QUIC, direct when hole punching works, through an iroh relay otherwise; relays forward traffic they can't decrypt [I1]) with its own ALPN (`isoform/front/1`), and splices bytes both ways. It keeps no table: the route is in the name. Only rate-limit counters live in memory.
3. **Computer.** It accepts iroh connections on that ALPN **only from the front's own endpoint ids** (iroh connections are authenticated by key), then runs a TLS server with its certificate for its hostname. Behind TLS sit the OAuth authorization server and the remote MCP server (streamable HTTP).
4. **Certificate.** The computer gets its certificate from Let's Encrypt with the TLS-ALPN-01 challenge. The challenge is a TLS handshake on port 443 with the `acme-tls/1` ALPN [L2], so it travels through the same passthrough untouched. Go's `golang.org/x/crypto/acme/autocert` already answers it. The private key never leaves the computer.

## OAuth and consent on the computer

The computer is both the authorization server and the resource server. It follows the MCP authorization spec: protected resource metadata, authorization server metadata, dynamic client registration, PKCE with S256, and resource indicators bound to the hostname.

- **Registration.** Each agent registers itself (Claude's "Register automatically", Muse's "OAuth with PKCE") [A1][A2]. A bearer key, shown once and revocable, is offered to agents that only send a header (a Grok Bot fallback) [A3].
- **Consent.** The authorize page opens on whatever device the person is using when they add the connector, often their phone. The page itself can't prove who is there, so it shows a short code and waits. Isoform on the computer shows "Grok Bot wants to see tasks you hand it. Code 482 913. Allow?", and only a tap there grants access. No password is involved.
- **Scopes.** `tasks:read` and `tasks:result`. Each existing read tool (memory, calendar, people) is off by default per agent. Settings lists each agent with its scopes, last use and Revoke.
- **Tokens.** Short-lived access tokens and rotating refresh tokens, stored on the computer. Revoking is immediate because the computer checks every request itself.

## Rate limits

- **Front, per hostname:** new connections per second, concurrent connections and bytes per minute. These are generous for MCP, which is small JSON. Per source IP: a cap on handshakes, so a flood at random labels costs little; unknown or malformed labels are dropped before any iroh dial. Global caps protect the box.
- **Computer, per client:** requests per minute per token, failed OAuth attempts per minute, and one pending consent prompt at a time. Over a limit it answers 429; the front never needs to understand HTTP.

## What the front can and can't see

| Can see | Can't see |
|---|---|
| The hostname label (a public key, not a name or email) | Requests, responses, MCP tools called, tasks, results |
| The connecting IP (the agent vendor's cloud) and time | OAuth codes, tokens, client names |
| Bytes and connection counts per label | Anything about the person beyond their label |

**Honest limit.** Isoform controls the DNS for `agents.isoform.ai`, so a compromised front operator could get its own certificate for a label and intercept traffic. Defences:

- The computer watches Certificate Transparency logs for its hostname and alerts in Settings on any certificate it didn't request.
- Optionally, a CAA record per label limits issuance to the computer's ACME account.
- The front's code is open source and stays tiny.

**When the computer is off,** the front can't answer with an HTTP error without ending TLS, so it closes the connection, and the agent reports the server as unreachable. That is accepted; MCP clients retry.

## Alternatives

| | Isoform front (SNI passthrough over iroh) | zrok-style tunnel | Per-person Cloudflare Tunnel |
|---|---|---|---|
| Where TLS ends | **On the person's computer** | At the zrok frontend: zrok.io's, or ours if self-hosted [Z1][Z2] | At Cloudflare's edge, which decrypts every request [F2] |
| Who sees plain text | Nobody but the computer | The frontend operator, which the zrok docs note [Z1] | Cloudflare; end-to-end passthrough needs Spectrum, which is Enterprise only and priced by quote [F3] |
| Our state | None (route in the hostname) | Zrok controller, accounts, share tokens | One tunnel and token per person in our Cloudflare account, through the API [F1] |
| Builds on | iroh, which Isoform already ships | zrok, Apache 2.0, self-hostable [Z1] | `cloudflared`, Apache 2.0, bundled like magpie |
| Cost | One small server plus bandwidth (estimate: tens of dollars a month for thousands of people; MCP traffic is small) | Self-hosted server, or NetFoundry's plans | First 1,000 tunnels free, then $4.50 per tunnel a month [F1]: about $40,500 a month at 10,000 people |
| Build | Most work: a front of a few hundred lines, plus TLS and ACME on the computer | Least work | Little work, plus a provisioning service |
| Fit with the privacy rules | Meets "forwards without seeing" | Fails it | Fails it |

**Bridge for dogfooding now.** For Bo's own testing while the front is built, a Cloudflare Tunnel or a zrok share **on Bo's own account**, pointing at the computer's MCP server, lets Claude, Muse and Grok Bot connect this week. It is fine for one person who owns the account and sees the trade-off; it never ships to users.

## Certificate limits (the main risk)

Let's Encrypt allows 50 certificates per registered domain every 7 days, across all accounts [L1]. With `agents.isoform.ai` under `isoform.ai`, that is **50 new people a week**. Renewals coordinated through ARI are exempt, so only new sign-ups count [L1]. Options, in order:

1. Ask Let's Encrypt for an override for `isoform.ai`. They grant these for Certificates per Registered Domain, and a request takes a few weeks [L1]. File it on day one.
2. Use a separate registered domain for the front, so its limit doesn't compete with the website.
3. Submit the front's domain to the Public Suffix List. Let's Encrypt decides the registered domain from that list [L1], so each label would become its own registered domain. PSL review is slow and has requirements; plan for it, don't rely on it.
4. A second ACME CA as a fallback.

The duplicate limit (5 certificates per exact name per 7 days) and the failed-validation limit (5 an hour) mean the computer must keep its certificate and key across restarts and never loop on failures [L1].

## Build order

1. **Computer: remote MCP and OAuth** on a loopback HTTPS port: the streamable HTTP transport, the authorization server (metadata, dynamic registration, PKCE, resource indicators), consent on the computer, the task resource and tools (`task_list`, `task_get`, `task_result`), and per-agent scopes and revoke in Settings. Test it with the MCP inspector through the dogfooding bridge on Bo's account, using Claude custom connectors and Grok Bot first.
2. **Computer: TLS and ACME.** The dedicated iroh endpoint for the front, `autocert` with TLS-ALPN-01, a persistent key and certificate, and the CT-log watch.
3. **Front:** SNI parsing, label to endpoint id, iroh dial with the front's ALPN, splicing, rate limits and counts-only metrics. **In Rust**, because iroh is a Rust library and `iroh-ffi` covers Python, Swift and Kotlin, not Go [I2]. Running a `dumbpipe connect-tcp` per person instead [I3] wouldn't scale. Deploy to two small servers behind the wildcard DNS name.
4. **DNS and certificates:** the wildcard record, Let's Encrypt staging, then production; the override request and the PSL decision run in parallel from day one.
5. **Agents:** Claude custom connector (and Duet through it), Grok Bot custom connector, a Muse custom connector, then a Muse Connector Platform listing [A2][A5]; ChatGPT and dots where the plan allows custom MCP with writes (Business, Enterprise and Edu; Pro is read and fetch only [A4]).
6. **Measures:** sent, taken and done from `task_result`, in the morning report as counts only.

## Sources

- [A1] Claude Help Center, custom connectors using remote MCP (connects from Anthropic's cloud, port 443, OAuth or fixed header): https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp
- [A2] Meta Help Center, "How Muse works with Connectors" (custom connectors, not reviewed by Meta): https://www.meta.com/help/artificial-intelligence/1687253048996149/
- [A3] xAI, Grok Bot docs: https://docs.x.ai/grok-bot/overview and https://docs.x.ai/x-subscription/grok-bot; custom connector steps (3rd party): https://crosswalk.to/agents/grok-bot
- [A4] OpenAI Help Center, "Connecting custom MCP servers in ChatGPT" (full MCP on Business and Enterprise/Edu; Pro read and fetch only), read through search: https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt
- [A5] Muse connector guidelines (OAuth, a read-only option, read, write and sensitive-write tools, review steps): https://platform.muse.ai/docs
- [L1] Let's Encrypt, Rate Limits: https://letsencrypt.org/docs/rate-limits/
- [L2] RFC 8737, ACME TLS-ALPN-01: https://www.iana.org/go/rfc8737
- [I1] iroh docs, overview (relays forward traffic they can't decrypt; QUIC; dial by key): https://iroh.computer/docs/overview
- [I2] iroh on GitHub (Rust; `iroh-ffi` for other languages): https://github.com/n0-computer/iroh
- [I3] dumbpipe README (`listen-tcp`, `connect-tcp`, TLS over QUIC): https://docs.rs/crate/dumbpipe/latest/source/README.md
- [Z1] zrok, self-hosting docs and FAQ (hosted frontend ends TLS; the operator can see traffic): https://docs.zrok.io/docs/category/self-hosting
- [Z2] zrok, personalized frontend guide: https://docs.zrok.io/docs/guides/self-hosting/personalized-frontend
- [F1] Cloudflare, Tunnels for Platforms (1,000 free tunnels, $4.50 per tunnel a month beyond, limits): https://developers.cloudflare.com/tunnel/features/tunnels-for-platforms/
- [F2] Cloudflare Tunnel: the edge ends TLS (TLS 1.3 from the edge to `cloudflared`; the origin hop is the operator's choice), from Cloudflare docs and guides: https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/cipher-suites
- [F3] Cloudflare Spectrum settings by plan (passthrough options on Enterprise): https://cloudflare-docs-2tg.pages.dev/spectrum/reference/settings-by-plan
