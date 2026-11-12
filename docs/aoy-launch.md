# Agent of You launch checklist

Target: Thursday, November 19, 2026. **Do not launch yet.** The Fly files are a deployment scaffold, not a working hosted runner. The deployment script is deliberately blocked until the sandbox, isolation, storage and operational checks below are resolved. `/healthz` only reports that the HTTP server is running; it does not test a job or a payment.

## Before launch

- [ ] **Hub / server:** Implement and test a hosted sandbox driver; verify isolated per-chat workspaces, network restrictions, proxy-only model credentials, stop, recovery and deletion against the selected provider.
- [ ] **Hub / infrastructure:** Design a private route to the model proxy, per-client IP rate limiting behind Fly's proxy, persistent volume ownership for UID 65532, backups and deletion of backups; verify by an end-to-end and restore exercise. The present server uses the socket peer for rate limits, so a shared proxy address can rate-limit unrelated clients.
- [ ] **Design / legal:** Supply approved Terms and Privacy copy, review retention and no-training claims, and reconcile the requested client-paid five-second Stop buffer with the implemented signed Stop figure; verify the final words match the actual behavior and provider contracts.
- [ ] **Hub / website:** Provide an access gate for the 75-person waitlist; verify a waitlisted person can enter and a non-waitlisted person cannot. `AOY_CHAT=1` currently enables the feature globally, not for a cohort.
- [ ] **Bo / hub:** Complete the Stripe test-mode money plan before live mode; verify holds, capture, refunds, Connect, reconciliation and both webhook endpoints in test mode.

The only production sandbox driver today invokes Docker to create an egress container, per-chat isolated networks and workspace volumes. The image here contains neither a Docker daemon nor the sandbox images; it cannot start with that driver on an ordinary Fly Machine. The local driver executes on the host and is for tests only. The selected Runta adapter must implement `sandbox.Driver` and `sandbox.Box` (create, find, put, execute, list, open, stop and destroy), preserve per-chat state and recovery, enforce network isolation and LLM-proxy-only access, prevent access to credentials and other chats, enforce resource limits and deletion, and be wired into configuration and startup. Provider network controls and proxy connectivity must be demonstrated before real client work. The Runta adapter scaffold is disabled and is not wired into server startup. Confirm which Runta account holds the credits: [runta.com](https://runta.com/docs/overview/) documents managed runtimes and uses `RUNTA_TOKEN`, while [runtahq.com](https://runtahq.com/docs/quickstart) documents `RUNTA_API_KEY` but its linked API reference could not be confirmed. Do not assume they are the same provider.

## Configuration

The app name and region in `server/fly.toml` are placeholders for Bo to confirm. Create one volume in the same region. Fly's volume mount can hide image directory ownership; provision the mounted directory for UID/GID 65532 and verify it is writable without granting the app root privileges. A single machine and volume do not provide high availability; test restore and account for snapshot retention in the privacy policy. A one-machine deploy uses `--ha=false`; keep the machine running for background work. Restrict `/llm` access to the sandbox route before launch, and do not rely on the public HTTP health check for sandbox readiness.

Runtime settings read by the server (defaults apply when omitted):

| Variable | Purpose |
| --- | --- |
| `ISOFORM_SERVER_ADDR` | Listen address; Fly config uses `0.0.0.0:8790`. |
| `ISOFORM_SERVER_DATA` | SQLite database and uploaded files; Fly config uses `/data`. |
| `ISOFORM_SERVER_PUBLIC_URL` | Public HTTPS origin for links and Stripe callbacks; required. |
| `ISOFORM_SERVER_SECRET` | Secret of at least 32 bytes for IP rate-limit identifiers; required. |
| `ISOFORM_WEB_ORIGINS` | Exact allowed website origins; first HTTPS origin is the Stripe return site. |
| `ISOFORM_AGENTS_API` | Reservation verification API; defaults to the production website API. |
| `ISOFORM_OWNER_VERIFY_TTL` | Maximum age of a successful owner verification; default `0s` disables expiry. |
| `ISOFORM_EMAIL_PROVIDER` | `resend` for actual email; Fly config selects it. |
| `RESEND_API_KEY` | Resend key, required by the Resend provider. |
| `ISOFORM_ADMIN_EMAIL` | Recipient of join requests; default is the existing admin address. |
| `ISOFORM_SPEEDS_FILE` | Optional speed map path; embedded map is the default. |
| `ANTHROPIC_API_KEY` | Isoform's model key, used by the server and its authenticated model proxy. |
| `ISOFORM_PAYMENTS` | Payment provider; Fly config selects `stripe`. |
| `STRIPE_SECRET_KEY` | Stripe API key; required for Stripe. |
| `STRIPE_WEBHOOK_SECRET` | Platform webhook signing secret; required for Stripe. |
| `STRIPE_CONNECT_WEBHOOK_SECRET` | Connect webhook signing secret; required for Stripe. |
| `ISOFORM_STRIPE_LIVE` | Set to `1` to permit live Stripe credentials after test-mode verification. |
| `ISOFORM_CONNECT_COUNTRY` | Country for new Express accounts; defaults to `US`. |
| `ISOFORM_MIN_TRANSFER_CENTS` | Minimum owner transfer; defaults to 1000 cents. |
| `ISOFORM_MAX_LIMIT_CENTS` | Maximum per-pass hold; defaults to 50000 cents. |
| `ISOFORM_REVIEW_DECLINE_CHARGES` | Set to `1` to charge on owner decline at review; otherwise the client is not charged. |
| `ISOFORM_SANDBOX` | `docker` or test-only `local`; no hosted adapter is available yet. |
| `ISOFORM_SANDBOX_IMAGE`, `ISOFORM_EGRESS_IMAGE` | Docker runner and egress image names. |
| `ISOFORM_EGRESS_ALLOW` | Exact host and port allowed to the LLM proxy; must match `ISOFORM_LLM_PROXY_URL`. |
| `ISOFORM_LLM_PROXY_URL` | Address through which the executor reaches the model proxy. |
| `ISOFORM_CLAMD_ADDR` | Optional clamd address for upload scanning; absent means antivirus is off. |
| `ISOFORM_ALLOW_LOCAL_SANDBOX` | Authorizes the test-only local driver; never enable in production. |
| `ISOFORM_DEV_MODE`, `ISOFORM_DEV_WORK_SECONDS`, `ISOFORM_DEV_IDENTITIES` | Local development mode, simulated work duration and test identities; never enable in production. |
| `ISOFORM_APPROVAL_TTL`, `ISOFORM_REVIEW_TTL`, `ISOFORM_IDLE_STOP`, `ISOFORM_RETENTION`, `ISOFORM_UNATTACHED_TTL` | Local-only positive duration overrides; production uses defaults. |
| `ISOFORM_TEST_MODEL_CAP_CENTS` | Test model cost cap override; leave unset in production. |

The Docker egress process itself reads `ISOFORM_EGRESS_ALLOW`. The disabled Runta scaffold reads `RUNTA_API_KEY` and optional HTTPS `RUNTA_BASE_URL` only when explicitly constructed; the server does not construct it or use either setting yet. Development and opt-in integration test variables are not production configuration. The website separately needs `AOY_CHAT` and `AOY_API_ORIGIN`; optional `AOY_TERMS_URL` overrides the draft Terms link. These are Pages settings, not server secrets.

## Bo's setup

Use an authenticated Fly CLI and create the app in the intended organization before adding settings. Do not put secret values in the repository, shell history, files, chat, or command arguments. `fly secrets set` takes values as command arguments; the stdin-based `fly secrets import` below avoids that exposure. Run each line separately in zsh, type the value at the hidden prompt, and verify each name appears in `fly secrets list` without viewing its value. Staging avoids restarting an existing machine for each value.

- [ ] **Bo:** Create the Fly account and app matching `server/fly.toml`; verify `fly status` names the intended private organization's app.
- [ ] **Bo:** Create Stripe live credentials and separate platform and Connect webhook endpoints; verify their events and API version against `server/README.md`.
- [ ] **Bo:** Create the Anthropic API key and Resend account and verified sending domain; verify provider dashboards show the intended account and domain.
- [ ] **Bo:** Confirm the Runta account with the credits; verify its API, network controls and credentials can support the adapter before creating production jobs.
- [ ] **Bo:** Set the server HMAC secret: `read -rs 'aoy_value?Value: '; printf 'ISOFORM_SERVER_SECRET=%s\n' "$aoy_value" | fly secrets import --stage; unset aoy_value` (run from `server/`); verify its name with `fly secrets list`.
- [ ] **Bo:** Set the model key: `read -rs 'aoy_value?Value: '; printf 'ANTHROPIC_API_KEY=%s\n' "$aoy_value" | fly secrets import --stage; unset aoy_value`; verify its name with `fly secrets list`.
- [ ] **Bo:** Set the email key: `read -rs 'aoy_value?Value: '; printf 'RESEND_API_KEY=%s\n' "$aoy_value" | fly secrets import --stage; unset aoy_value`; verify its name with `fly secrets list`.
- [ ] **Bo:** Set the Stripe key: `read -rs 'aoy_value?Value: '; printf 'STRIPE_SECRET_KEY=%s\n' "$aoy_value" | fly secrets import --stage; unset aoy_value`; verify its name with `fly secrets list`.
- [ ] **Bo:** Set the platform signing secret: `read -rs 'aoy_value?Value: '; printf 'STRIPE_WEBHOOK_SECRET=%s\n' "$aoy_value" | fly secrets import --stage; unset aoy_value`; verify its name with `fly secrets list`.
- [ ] **Bo:** Set the Connect signing secret: `read -rs 'aoy_value?Value: '; printf 'STRIPE_CONNECT_WEBHOOK_SECRET=%s\n' "$aoy_value" | fly secrets import --stage; unset aoy_value`; verify its name with `fly secrets list`.
- [ ] **Bo:** Set `ISOFORM_SERVER_PUBLIC_URL`, `ISOFORM_WEB_ORIGINS` and `ISOFORM_STRIPE_LIVE=1` in Fly configuration after confirming the domains and test plan; verify the configured names and public HTTPS origin.
- [ ] **Bo:** After confirming the intended Runta provider and API, set `RUNTA_API_KEY` from a hidden prompt: `read -rs 'aoy_value?Value: '; printf 'RUNTA_API_KEY=%s\n' "$aoy_value" | fly secrets import --stage; unset aoy_value`; verify its name with `fly secrets list`. This stages the same secret as `fly secrets set RUNTA_API_KEY=...` without putting its value in command arguments or shell history. The scaffold cannot run jobs yet.

## Hub launch

- [ ] **Hub:** After every prerequisite is complete, remove the deployment guard and run `server/scripts/deploy.sh`; verify exactly one running machine, its attached writable volume and a passing `/healthz` check.
- [ ] **Hub:** Test a real end-to-end hire, stop, delivery, receipt, refund and webhook with agreed controlled accounts; verify the displayed amounts, Stripe records and owner ledger agree.
- [ ] **Hub:** Publish legally reviewed pages; if the approved Terms page moves, configure `AOY_TERMS_URL`. Verify both legal links under Start resolve to the approved pages.
- [ ] **Hub:** Enable chat only through the verified waitlist gate for the 75 people; verify an allowed and a disallowed identity before inviting the cohort.
- [ ] **Hub:** Monitor Fly health, logs, volume capacity, sandbox failures, provider usage, payment and email failures; verify alerts reach the named operator and review receipts daily.
- [ ] **Hub:** Document rollback before enabling access; verify the website gate can be turned off without interrupting settlement of work already started.
