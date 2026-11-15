# Task links: dogfood log

## 2026-11-14: the outside path with a real agent

- **Setup:** `wails-gui/cmd/tasklink-dogfood` serves one fixed, harmless test task with the same handler the app uses (`internal/tasklink`). It listens on 127.0.0.1:8793, behind an ephemeral zrok share on Bo's own account (`zrok share public --headless http://127.0.0.1:8793`). The share had a random URL, no reserved name was created, and it was stopped right after the run.
- **Agent:** Claude Code headless (`claude -p "<the one-line prompt>" --allowedTools WebFetch "Bash(curl:*)"`), given only the one line from `tasklink.Prompt`.
- **Result:**
  - The agent read the task with WebFetch (user agent `Claude-User (claude-code/...)`, `Accept: text/markdown, text/html, */*`). The first GET marked the task taken.
  - It POSTed `{"status":"done","summary":"I am Claude ... UTC date: 2026-11-14."}` with curl. The handler accepted it and the link was spent.

## Findings

1. **zrok's interstitial blocks browser-like agents.** zrok's public shares answer any browser user agent with an HTML interstitial page (`200 text/html`, "zrok interstitial") until it is clicked through. curl and Claude Code's fetcher went straight through. Agents that fetch with a browser user agent (likely ChatGPT and Claude.ai browsing) would read the interstitial, not the task. The bypass is a header the client must send, which an agent won't. zrok works for dogfooding with Claude Code, but not as the path for every agent. That is one more reason for the front in `docs/design/mcp-public-front.md`.
2. **An empty 204 left the agent unsure.** The agent said "the server returned an empty response, so I can't confirm it was accepted." A short confirmation body helps. #999 changes the accepted POST to `200` with `Result received. This link is now closed.`

## 2026-11-14 (evening): a second agent, and a Cloudflare quick tunnel

- **Setup:** the same tool, now with the page that says what to do, gives the task, marks quoted text as data, and explains how to answer (#999), behind a Cloudflare quick tunnel (`cloudflared tunnel --url http://127.0.0.1:8793`, no account). The tunnel was stopped after the run.
- **Codex** (`codex exec` with network on, given only the one-line prompt): it read the task with curl (marked taken), POSTed `{"status":"done","summary":"I am Codex, an OpenAI GPT-6 agent. The current UTC date is 2026-11-15."}`, and reported the confirmation body: "The endpoint confirmed receipt and closed the link."
- **Claude Code:**
  - With curl it read the task (marked taken). Its POST was held by my own `--allowedTools` pattern in headless mode, not by the link.
  - With WebFetch through the tunnel it got **403 from Cloudflare**, and the request never reached the computer.

## Findings (second run)

3. **Cloudflare quick tunnels block AI fetchers.** Through the tunnel, the user agents `Claude-User` (Claude's WebFetch) and `GPTBot` get a 403 from Cloudflare's bot blocking before the request reaches the computer. `ChatGPT-User` and browser user agents pass. There is no zrok-style interstitial, but no hosted tunnel serves every agent's fetcher. Our own front passes bytes without judging the user agent, so it serves them all.
4. **Two agents, one link format.** Claude Code (first run) and Codex (this run) each read the task from the one-line prompt alone and posted a valid result. Neither needed an MCP connector.

## Run it again

```bash
zrok share public --headless http://127.0.0.1:8793
cd wails-gui && go run ./cmd/tasklink-dogfood -addr 127.0.0.1:8793 -base <your-public-url>
```

Give the printed `PROMPT` line to the agent. The tool exits after the first result. Stop the share afterwards.
