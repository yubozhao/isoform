# Task links: a one-time /t/<token> for any agent

Design, 2026-11-14. It builds on the public front in `docs/design/mcp-public-front.md` and the Work on it flow in `docs/work-on-it-agents.md`.

## Why

MCP needs setup per agent: a connector, OAuth and consent. Many agents can't do it today, or only on some plans: dots on Pro is read and fetch only, and Instinct has no MCP. But every agent that can browse can open a link.

A task link turns one task into a URL. The agent reads its task there and posts its result back. The person never configures anything. The prompt that reaches the agent shrinks to one line, "Your task is at https://…/t/8fK…; read it, do it, and post your result there", so it fits any deep link, paste or text message.

## What a link is

- **The URL.** `https://<label>.agents.isoform.ai/t/<token>`, served by the person's computer through the front. TLS ends on the computer, so the front sees only the label: never the path, the token or the task (mcp-public-front, "What the front can and can't see").
- **The token.** 128 random bits in base32. It is generated on the computer when the person taps Send, and is bound to one task and one agent. It is stored on the computer as a hash, the way relay credentials are.
- **One task, one use.** The link answers until the agent posts a result with status `done`, `blocked` or `declined`, or until 24 hours pass, whichever comes first. After that it answers `410 Gone` and the token's hash is deleted.
- **What is behind it.** Exactly the prepared text the person saw in the Work on it sheet, plus the fixed instructions below. Nothing else: no extra context, no tools, no other tasks. The link may end up in vendor logs and chat history, so it carries only what the person already chose to send.

## The two calls

### `GET /t/<token>`

The response is `text/markdown`, or JSON with `Accept: application/json`, and is agent-readable:

```markdown
# A task from Isoform

<the prepared text, exactly as the person saw it>

## When you're done

POST a JSON object to this same URL:
{"status": "done" | "blocked" | "declined", "summary": "<what you did, under 2,000 characters>", "links": ["<optional URLs to what you made>"]}

This link works for one result and expires in 24 hours.
```

- The first GET marks the task **taken**. A GET whose user agent is a known link-preview fetcher (iMessage, Slack, Discord and similar) is answered with a bare title only and doesn't count as taken.
- Headers: `Cache-Control: no-store`, `X-Robots-Tag: noindex`, `Referrer-Policy: no-referrer`, no cookies, and no CORS (no `Access-Control-Allow-Origin`), so a web page can't read it from a browser.

### `POST /t/<token>`

- **Input:** JSON only (`Content-Type: application/json`), at most 8 KB. `status`, `summary` and `links` are checked against the schema above; anything else is rejected with 400.
- **Result:** stored on the task record and shown on the item's card ("Claude: done. <summary>").
- **Measures:** counted as **done** (or blocked, or declined), feeding the sent, taken and done counts already in `GetWorkOnItCounts`.
- **The text is data.** The summary is shown and stored, and it is never run as an instruction or fed into a model prompt without the same quoting every other outside text gets.
- **Answers:** `200` with "Result received. This link is now closed." on success, then the link is spent. A second POST answers `410`. (It was `204`; an empty answer left a real agent unsure: `docs/design/task-link-dogfood.md`.)

## Prompt the agent receives

The fixed template, sent by deep link, by `paste_into_agent`, as a message, or by share:

> Your task from Isoform is at https://<label>.agents.isoform.ai/t/<token>. Open it, do what it asks, then POST your result to the same link as it explains. The link works once and expires in 24 hours.

The task text is not in the prompt. A short prompt also keeps Claude's `q` and text messages well within their limits.

## Privacy and safety

- **The person's tap makes the link.** No link exists before they tap Send, and the sheet shows the exact text the link will serve.
- **Closed Eyes withdraws open links.** Turning it on answers `410` on every open link at once. A task whose source the person forgets or withdraws is withdrawn the same way, because the computer checks the source at every GET, not only when the link was made.
- **Whoever has the link is the agent.** The token is a bearer secret in the URL, so anyone the agent shares it with can read the task and post one result. That is acceptable because the content is only what the person sent; a second secret in the same message would add nothing.
- **Limits per token:** 30 GETs, 3 POST attempts (malformed ones count) and one accepted result. Per label: the front's rate limits apply. Unknown tokens answer `404` after a constant-time hash compare.
- **When the computer is off,** the link fails at the front (the connection closes). The prompt says it may need a retry, and it still expires 24 hours from Send.
- **Nothing on Isoform's servers.** The front can't read the path or body. The link and its result live only on the person's computer.

## How it fits the front

The task link is a second resource behind the same front and TLS as remote MCP. It is simpler than MCP, with no OAuth, which makes it the easiest first agent integration once the front exists:

- **Build step 1** in mcp-public-front: add `/t/` beside the MCP endpoint on the computer's HTTPS server.
- **Dogfood:** through the bridge on Bo's own account before the front ships.
- **Methods:** Work on it gains `task_link`, offered for agents that browse (Claude, ChatGPT, Grok Bot, Muse, Instinct) once the front is reachable. It ranks above `open_copy`, because a result can come back.

## Build order

1. **Computer:**
   - a task-link store: token hash, task text, agent, expiry, result;
   - the `GET` and `POST` handlers on the local HTTPS server;
   - expiry, Closed Eyes and forget checks;
   - preview-fetcher handling.
   - Tests cover expiry, a single result, a 410 after the result and after Closed Eyes, the schema, size limits, no CORS, and that the content is only the prepared text.
2. **Work on it:** the `task_link` method and the one-line prompt template, the result shown on the card, and taken and done from the link.
3. **Dogfood** through the bridge with Claude and Grok Bot, then go live behind the front.
4. **Measures:** taken from the first real GET, done from the POST, in the morning report.

## Open questions

- Should a result's `links` open from the card? They are links from an outside agent, so the card shows the host first.
- Should a `blocked` result reopen the task with the agent's question for the person? Design decides.
