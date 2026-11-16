# Agents join your Isoform (B5, part 1)

Design: phone-agent v1.1.0 §2 and §4 (B5), computer-agents v1.0.0 rules 11 to 13. Learned from Station: one pasted message connects an agent, with no human clipboard afterwards.

## Flow

1. **Connect an agent** (`StartAgentJoin`) makes a join link that works for 10 minutes, and the message to paste: "Join my Isoform: read {link} and follow the steps there." A new link closes any open one. The link is on the front hostname (`https://<label>.agents.isoform.ai/j/<code>`) while the front runs; otherwise it is the loopback listener, which only agents on this computer can read (`local: true`).
2. The agent reads the page (`GET /j/<code>`, Markdown). It asks its owner three things: which device it works on, what it may open, and that the owner gave it the link.
3. It posts the answers (`POST /j/<code>`, `{name, device, mayOpen}`, one line each, 120 characters at most) and gets a 4-digit check number. Only the first agent to answer is accepted.
4. Isoform shows "{name} wants to join", with the device, what it may open, and the check number (`GetAgentJoin`). The person taps Allow or Deny (`AnswerAgentJoin`).
5. The agent polls `GET /j/<code>/status` (400 reads at most). After Allow, the next read returns its token once and spends the link.
6. The agent then uses `Authorization: Bearer <token>` on the agent channel (`/a/inbox`, part 2).

## Rules

- **Two steps by the person.** A leaked join link alone can't connect an agent: the person makes the link, and they allow the agent.
- **Tokens are kept as hashes.** They are 32 random bytes; only the SHA-256 is saved, in `agents/connected.json` (0600 in a 0700 folder).
- **Disconnect revokes at once** (`DisconnectAgent`).
- **One gate.** Joining answers only while task links are on and Closed Eyes is off, the same gate as task links (`agentsAllowed`). An open link that fails it is closed.
- **Known names map to known agents.** A name such as "Claude Code" or "Codex CLI" makes the agent show as that catalog agent in Your agents, connected. Any other agent is listed under the name it gave itself.
- **The task link follows the front.** `taskLinkHandoffReady` is now true while the front runs, so Work on it offers task links without the dogfood bridge's environment variable.

## Part 2: the agent channel

- **The inbox.** `GET /a/inbox`, with the agent's token, returns the agent's own waiting hand-offs: the task, when it was sent, and a `resultUrl`. It also returns the instructions, the data note and the limits (stop at a draft for anything that sends, posts, buys, books or deletes). The first read marks a hand-off taken.
- **Results.** `POST /a/results/<id>` takes the task link's result shape plus up to 5 UTF-8 text files of 256 KB each. Names are checked: letters, digits, dot, dash, underscore and space, no leading dot, no `..`. The result lands in `task-links/results.json` with `via: "inbox"` and the file names. The record is marked done or not done, and the files are kept under `agents/files/<id>/` (0600).
- **Work on it.** A connected agent gets the `connected` method first. A known agent that only joined (Claude Code) appears in the sheet once connected; any other joined agent appears under its own name. Send puts the task in its inbox, with no copying and nothing opened.
- **The phone.** `POST /agents/handoffs` (PROTOCOL.md 27.1) sends the phone's Yes to a connected agent.
- **Privacy.** Waiting hand-offs hold the task text, like open task links. Any privacy withdrawal spends them, a withdrawn source's files are removed, Disconnect drops the agent's waiting work, and the same gate applies (task links on, Closed Eyes off).

## Later

- `isoform.ai/j/{code}` as a short link (a redirect on our website to the person's front hostname).
- Joining when the computer is asleep (through the relay).

## Part 3: Isoform's tools (agents-join v1.0.0 §4 and §5, v1.1.0)

- **The tool server** is at `/mcp`, beside the task links, on loopback and through the front. It uses mark3labs/mcp-go, the library the app already uses, over streamable HTTP with sessions.
- **Joining with it.** The join page, in the design's words, gives the agent the address `<origin>/mcp?join=<key>`. The key is a second secret, shown only on the page and never in the pasted message. When a client connects with it, it asks to join under its client name (`clientInfo.name`), and the attempt gets its own check number. Until the person's Yes, every tool answers "Show your person this code: NNNN". Only the session that asked is told the code: a second client with the same address learns nothing and can't replace the first attempt. The Yes spends the link.
- **On the Yes, the key rotates.** The join page's address stops working at once, on every session, old or new. The agent gets a fresh key that only the tool-server session which completed the code can receive: its next tool call carries the new address (`/mcp?key=<key>`, or `Authorization: Bearer <key>`), shown once. That session keeps working without it; a new connection needs the fresh key. Someone who read the join page and kept its address is refused after the Yes. Disconnect ends both the key and the session at once. With only the pasted message (its join code), nothing opens.
- **Later:** OAuth on the tool server (dynamic client registration, which Claude, ChatGPT and Grok connectors support), so a chat-app connector picks up its credential without the person changing the address.
- **Six tools, nothing else:**
  - `get_handoffs` marks them "has it".
  - `get_handoff`.
  - `post_progress` keeps one line in the agent's own words, for "Working · {line}".
  - `post_result` takes a summary, the result text (kept as `result.md`) and up to 5 text files, with the same checks as `/a/results`.
  - `ask_agent` and `get_answer` answer "not available yet" until C2.
  - No tool reads the person's memory, day or files.
- The bearer-token inbox (`/a/inbox`) stays for agents without a tool server.
