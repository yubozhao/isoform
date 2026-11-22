# Work on it: handing Isoform's tasks to personal agents

Research for the hub, 2026-11-14. "Work on it" sends a task Isoform proposes (a topic proposal, a brief item, a chat card) to the person's agent of choice. Isoform keeps the context, the judgment and the routing; the agent does the work (`docs/rework-2026-11.md`, "Swim up the chain"; backlog, Bo 2026-10-25: "isoform is a destination for muse/openai dot/claude duet/grokbot").

Most of these products are weeks old and in preview. Official pages were used where they could be read; several OpenAI pages refused automated reads (HTTP 403), and Meta has little public documentation, so some facts come from third-party guides. Those are marked "(3rd party)". Check again before building.

## The one fact that shapes everything

Every cloud agent below reaches MCP servers **from its own cloud**, over public HTTPS, usually with OAuth. None of them can reach a server on `localhost`:

- Claude: "Claude connects to your remote MCP server from Anthropic's cloud infrastructure", on HTTPS port 443 only, for claude.ai, Desktop, Cowork and the mobile apps [C2].
- Muse: "Muse connects from Meta's cloud", so a localhost server "is out of reach" (3rd party) [M3].
- Grok Bot: a custom connector needs a public HTTPS URL (3rd party) [G3].
- ChatGPT: connects to remote MCP servers; a private server needs OpenAI's Secure MCP Tunnel [O4].

Isoform's MCP server today (`yansu-agent/mcpread`) is **stdio only**, for Claude Code and Codex on the same computer, and **read only** (`activity_search`, `memory_search`, `memory_list`, `people_lookup`, `calendar_today`, `calendar_range`, `brief_today`, `playbook_status`, `run_history`). So the cloud agents cannot use it as it is. The MCP route needs a remote endpoint (below); until then, the handoff is a prepared prompt.

## Recheck against vendor docs (2026-11-14, second pass)

Where a vendor page could be read, it confirms or corrects the third-party facts used in the table below:

- **Claude Duet:**
  - **Corrected:** Anthropic's own article covers setup on iPhone and Android only. Plans are Pro, Max and Ultra, US only, by waitlist; Team and Enterprise are not supported yet [C7].
  - **Confirmed:** Duet uses the connectors already set up in Claude, and for sites without a connector it uses its own cloud browser [C7].
  - **Not covered:** the article doesn't say whether custom connectors work in Duet's background checks, so the "scheduled runs can't reach custom connectors" finding still rests on one review [C4].
  - **Also from the article:** background work counts toward the five-hour and weekly limits, and Duet can't make calls or send texts [C7].
- **OpenAI dots:**
  - **Confirmed:** Pro can connect custom MCP servers with read and fetch permissions only; full MCP is on Business and Enterprise/Edu; servers are added from Plugins, without Developer Mode [O5]. So no write-back from Pro.
  - **Confirmed:** dots are on Pro and Business Premium, with an Enterprise beta; Pro is not available in the EEA, Switzerland or the UK [O5].
- **Meta Muse:**
  - **Confirmed:** custom connectors exist and Meta does not review them [M5].
  - **Corrected:** Meta's help page describes building a custom connector by asking Muse in chat, which can involve API details kept in its Secure Credentials Store. It doesn't name a Settings "Add custom connector" URL field [M5]. So "paste an MCP URL" stays a third-party claim [M3].
  - **Confirmed:** Muse's connector guidelines require OAuth for user accounts, a read-only option where possible, and read, write and sensitive-write tool classes with approvals, and they forbid model training on connector data [M6]. They don't specify the MCP transport or PKCE.
- **Grok Bot:**
  - **Confirmed:** included on Premium, Super and Ultra, not on Plus, with a weekly pool shared with Grok and Cursor [G4]. Prices are on xAI's pricing page: Premium $30, Super $100, Ultra $200 (from search).
  - **Corrected:** mail to the `@mail.grokbot.com` address does not wake a Bot by itself; a routine has to watch for it. Email a routine writes waits as a draft until the person presses Send (from xAI docs, through search) [G5].
- **Instinct:**
  - **Confirmed:** its terms allow training on user materials. The opt-out applies going forward only, has a safety-review exception, and doesn't affect models already trained [I4].

## Comparison

| Agent | Maker, status | Runs on | MCP client | Ways to hand it a task | Result back to Isoform | Auth, price, terms |
|---|---|---|---|---|---|---|
| **Claude Duet** | Anthropic, research preview since 2026-10-21, waitlist, US Pro and Max [C1][C3] | Standalone app on iOS and Android, and web [C3][C4] | Indirect: Duet uses the connectors in the Claude account (Anthropic statement relayed by a vendor, 3rd party) [C5]; custom connectors are remote MCP over HTTPS 443 with OAuth or a fixed header [C2]. Scheduled runs cannot reach custom connectors [C4] | By message in the Duet app [C3]. No Duet deep link is documented; Claude Desktop links open chats, Claude Code and Cowork: `claude://claude.ai/new?q=`, `claude://cowork/new?q=`, prompt up to about 14,000 characters, shown for review before sending [C6] | Only through an Isoform MCP write tool the agent calls. No callback or webhook is documented | Pro ($20) and up; preview shares usage with other Claude apps; checks connected apps every few hours [C3]. Custom connectors are not verified by Anthropic [C2] |
| **OpenAI dots** | OpenAI, launched 2026-09-29 at DevDay [O1] | ChatGPT on web, desktop, iOS and Android; Slack and Teams [O1][O3] | Plugins (over 4,000 apps, 3rd party) [O2]; custom MCP in ChatGPT: full MCP for Business and Enterprise/Edu, Pro only with read/fetch permissions, private servers through Secure MCP Tunnel [O4]. Whether dots call custom MCP apps is not documented | Describe the task in ChatGPT; Slack channels it is told to watch (3rd party) [O2]. ChatGPT accepts `chatgpt.com/?q=` (3rd party, not dots-specific) [D1]. Phone and text "later" [O1] | Through an Isoform MCP write tool, where custom MCP is allowed. No webhook is documented | Pro ($100) and Business Premium; not on Free or Plus; Pro not yet in the EEA, Switzerland or the UK [O1][O3] |
| **Meta Muse** | Meta, launched 2026-09-08; over 10 million agents set up in about six weeks [M1][C1] | iOS, Android, muse.ai on the web, WhatsApp; a Mac client is reported (3rd party) [M2] | Settings, Connectors, Add custom connector: remote streamable HTTP with OAuth, not reviewed by Meta (3rd party) [M3]. Muse Connector Platform: a listed connector, either a hosted MCP endpoint or a raw API, with API keys or OAuth with PKCE, reviewed by Meta; over 2,000 submissions by 2026-09-24, review takes weeks [M4] | Chat in the app or on WhatsApp [M3]. No deep link or share extension is documented [M2] | Through an Isoform MCP write tool. No push into Muse is documented [M4] | Free, Power $20, Maximum $100 (3rd party) [M2]; US and Canada, 18 and older [M3] |
| **Grok Bot** | xAI, beta since 2026-08-11 [G1][G3] | Web (grok.com/bot), app for Mac, Windows, iPhone and Android, Slack, X [G1][G3] | "Can interact with any MCP or API" [G1]; custom connector at grok.com/connectors: public HTTPS URL, OAuth sign-in, or a bearer key in a header (3rd party) [G3] | Chat or voice [G1]; email to the account's address at `mail.grokbot.com`, which Routines can start on (since 2026-10-09, 3rd party) [G3]; Routines on Slack or GitHub events [G3] | Through an Isoform MCP write tool. No API is documented [G1][G3] | Comes with the X subscription: Premium $30, Super $100, Ultra $200, shared weekly pool with Grok and Cursor; not in X Plus (3rd party) [G3] |
| **Instinct** | Spear Street Technology, invite only [I1][I2] | iMessage, WhatsApp, phone calls; a Mac app and web workspace are mentioned by one review [I1][I3] | None documented [I2] | A text or a call: the task is a message [I1][I2] | Only as a reply in the person's messages; nothing comes back to Isoform | Free today; terms grant a perpetual, sub-licensable licence over user materials, including for training; the training opt-out applies going forward only [I2] |
| **Gemini Spark** (other, real traction) | Google, announced at I/O 2026-05-19, limited access [S1][S2] | Runs on Google Cloud, Gmail first [S1][S2] | MCP launch partners (Canva, OpenTable, Instacart); custom MCP not confirmed [S3] | Email it "like a colleague" [S2] | Not documented | Limited access; tiers not confirmed [S3] |

Also seen with traction, not covered in depth: Perplexity Comet's Background Assistants (on the $200 Max plan) and agent modes in browsers [S4].

## Recommended handoff per agent

Two routes, chosen per agent, always started by the person's tap:

- **Task link (first, Bo 2026-11-14: "make the task link that way. i think that's a good way to do things"):** for agents that browse (Claude, ChatGPT, Grok, Muse, Instinct), Isoform makes a one-time `/t/<token>` link to the prepared task and sends one line naming it, by the agent's best carrier (deep link, message, or open with the line on the clipboard). The agent reads the task there and posts its result back, which counts as taken and done. It is offered only while a public https origin reaches the computer, and sending refuses under Closed Eyes (the `task_links_enabled` switch was removed in agents G2, 2026-11-21) (the dogfood bridge, then the front). Design: `docs/design/task-link-handoff.md`.
- **Pull (preferred):** Isoform publishes the task as an MCP resource. The person's agent, connected once to Isoform's remote MCP endpoint, reads the task and its context through Isoform's own filters, and writes the result back with a tool. Isoform sees "taken" and "done" without asking.
- **Push a prepared prompt (fallback):** Isoform writes a self-contained prompt from what it may share, shows it, and opens the agent with it (deep link, share sheet, message or email). Nothing comes back automatically; the person can mark it done, or the agent can write back if it is also connected over MCP.

| Agent | Now (no remote endpoint yet) | Once Isoform has a remote MCP endpoint |
|---|---|---|
| Claude Duet | Copy the prepared prompt and open the Duet app; on the computer, `claude://claude.ai/new?q=` or `claude://cowork/new?q=` for work that suits Claude itself | Custom connector in the Claude account; Duet reads `isoform://task/<id>` and calls `task_result`. Not for scheduled Duet runs, which cannot reach custom connectors |
| OpenAI dots | Copy the prompt and open ChatGPT (`chatgpt.com/?q=` opens a chat, not the dot) | Custom MCP app where the plan allows (Business, Enterprise/Edu; Pro read/fetch only means no write-back); the dot's plugins otherwise |
| Meta Muse | Share the prompt to Muse or WhatsApp | Custom connector first (no review); the Connector Platform listing later, as Isoform's distribution channel |
| Grok Bot | Email the prepared task to the person's `mail.grokbot.com` address (sent by the person's own mail, after the tap) | Custom connector with OAuth; a Routine can watch for new Isoform tasks |
| Instinct | A text to Instinct with the prepared prompt (opens Messages with the text filled in; the person sends it) | No MCP: stays a message |
| Gemini Spark | Email the prepared task | Revisit when custom MCP is documented |

Default when the person has not picked an agent: Claude Code or Codex on the computer, which Isoform already runs.

## What Isoform's MCP server must add

1. **A remote endpoint.** Streamable HTTP on a public HTTPS URL, port 443, served from the person's own computer through the remote-access path Isoform already ships (Dumbpipe/iroh, `wails-gui/internal/remoteaccess`); no task content is stored on our servers. While the computer is off, the endpoint answers "Isoform is offline" and the agent waits. Dumbpipe gives a peer-to-peer address, not the public HTTPS URL on port 443 that the cloud agents require. The design for the public front (SNI passthrough over iroh, TLS ending on the computer) is in `docs/design/mcp-public-front.md`.
2. **Auth per agent.** OAuth 2.1 with PKCE and dynamic client registration (Claude's "Register automatically", Muse's "OAuth with PKCE") [C2][M4], plus a revocable bearer key for agents that take only a header (Grok Bot fallback) [G3]. One grant per agent, shown in Settings with "last used" and Revoke. Scopes: `tasks:read`, `tasks:write_result`, and the existing read tools only if the person turns them on for that agent.
3. **A task resource.** `isoform://task/<id>`: the task title, what done looks like, the context Isoform chose for it (snippets with source and time, never whole transcripts), and a deadline. Tools: `task_list` (open tasks handed to this agent), `task_get`.
4. **A result write-back tool.** `task_result(id, status: taken|done|blocked|declined, summary, links[])`. Isoform stores it on the task, shows it on the card, and treats it as the person's judgment data (the thesis in `docs/rework-2026-11.md`). Text from the agent is untrusted: it is shown and stored, never run as an instruction.
5. **Listing.** A Muse Connector Platform submission once the endpoint is stable: name, icon, example prompts, privacy policy, terms, the MCP endpoint and OAuth [M4].

## Privacy rules

- **The person sees exactly what is sent.** The Work on it sheet shows the prepared prompt or the task resource word for word, with the agent's name, before anything leaves the computer.
- **Nothing goes without the person's tap.** No automatic handoff, no background sending. Each task is sent once, on its own tap; agents can read only tasks handed to them.
- **The same filter as every other exit.** Closed Eyes, forgotten items, excluded apps, withdrawn sources and sensitive apps never go, and are checked again when the agent reads the task, not only when the task was made. A task whose context went away is withdrawn and its resource returns nothing.
- **Agent-driven activity stays out** of the context, by the rules in PRs #857, #878 and #888.
- **Prompts in links are visible to the system.** A `?q=` link puts the text in a URL that browsers and logs can record [D1]; Isoform uses links only for short prompts without personal details, and the clipboard or share sheet otherwise.
- **Say what each agent's terms mean.** For example, Instinct's terms allow training on what it receives [I2]; the sheet says so before the first handoff to it.
- **Revocable.** Revoking an agent in Settings ends its access at once and is logged.

## Measures

Counted per agent and per task kind (topic proposal, brief item, chat card), in the morning report (counts only, never content):

- **Sent:** the person tapped Work on it and the task left.
- **Taken:** the agent read the task (MCP), or the person confirmed it was handed over (prompt route).
- **Done:** a `task_result` of done, or the person marked it done; with time from sent to done.
- Also counted: blocked and declined results, revokes, and proposals answered no or "not mine", which are the judgment signal the thesis values most.

## Your agents and the hand-off log (2026-11-15, phone-agent v1.1.0 B4)

- **Two choices.** The person picks one agent for code (Codex, Claude Code, Cursor) and one for everything else (Claude, ChatGPT, Grok, Gemini, Muse, Instinct). The choice is one setting on the computer, read by the phone (`GET /agents`, PROTOCOL.md section 27). A task's kind is `code` only when its surface says so; the chosen agent for that kind is the sheet's default when it can take the hand-off.
- **Each hand-off kept.** Besides the counts above, the computer now keeps each hand-off with exactly what went: the task text, the one line a task link sent, the agent, how it went, the source and the time (`work-on-it/handoffs.json`, 0600, 90 days, on the computer only). It is the person's own data for Your data and the Agents page. Forget removes one or all; a withdrawn source removes its hand-offs; Closed Eyes stops new ones. The morning report still reads counts only.

## Sources

- [C1] CNBC, "Anthropic announces preview of Claude Duet", 2026-10-21: https://www.cnbc.com/2026/10/21/anthropic-preview-of-claude-duet-personal-ai-agent-take-on-muse-dots.html
- [C2] Claude Help Center, "Get started with custom connectors using remote MCP": https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp
- [C3] Engadget, "How to use Claude Duet": https://www.engadget.com/2300253/how-to-use-claude-duet/
- [C4] Every, "Vibe Check: Duet": https://every.to/vibe-check/vibe-check-duet-anthropic-s-always-on-agent
- [C5] Sequenzy, "How to connect Claude Duet to Sequenzy" (3rd party): https://www.sequenzy.com/blog/how-to-connect-claude-duet-to-sequenzy
- [C6] Claude Help Center, "Open Claude Desktop with a link": https://support.claude.com/en/articles/14729294-open-claude-desktop-with-a-link
- [C7] Claude Help Center, "Get started with Claude Duet": https://support.claude.com/en/articles/17631495-get-started-with-claude-duet
- [O5] OpenAI Help Center, "Connecting custom MCP servers in ChatGPT" and "Getting started with your dot", read through search (direct reads return 403): https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt, https://help.openai.com/en/articles/20001530-getting-started-with-your-dot
- [M5] Meta Help Center, "How Muse works with Connectors": https://www.meta.com/help/artificial-intelligence/1687253048996149/
- [M6] Muse connector guidelines: https://platform.muse.ai/docs
- [G4] xAI docs, "Grok Bot on The X Subscription": https://docs.x.ai/x-subscription/grok-bot
- [G5] xAI docs, "Skills and routines" and FAQ: https://docs.x.ai/grok-bot/skills-routines-and-automations, https://docs.x.ai/grok-bot/faq
- [I4] Instinct Terms of Service and Privacy Policy: https://instinct.com/terms, https://instinct.com/privacy-policy
- [O1] CBS News, "Sam Altman unveils dots": https://www.cbsnews.com/news/sam-altman-openai-dots-chatgpt-agents-safety/
- [O2] Search summary of OpenAI's "Introducing dots" and help pages, and third-party guides (the OpenAI pages returned 403 to automated reads): https://openai.com/index/introducing-dots/, https://help.openai.com/en/articles/20001530-getting-started-with-your-dot, https://www.eesel.ai/blog/dots-for-work
- [O3] Every, "Dots: A Guide for Putting OpenAI's Always-on Agents to Work": https://every.to/guides/openai-dots-guide
- [O4] OpenAI Help Center, "Connecting custom MCP servers in ChatGPT" (read through search; 403 to direct reads): https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt
- [M1] Axios, "Meta debuts Muse", 2026-09-08: https://axios.com/2026/09/08/meta-debuts-muse-personal-ai-agent
- [M2] Third-party pricing and platform roundups: https://theneuron.ai/explainer-articles/how-to-get-started-with-meta-muse/, https://www.layer3labs.io/guides/is-meta-muse-free
- [M3] AnythingMCP, "Meta Muse MCP: Add Custom Connectors" (3rd party, checked against vendor docs 2026-09-25): https://anythingmcp.com/guides/clients/muse
- [M4] Stacktree, "Meta opened Muse to connectors" (3rd party): https://stacktr.ee/blog/muse-connector-platform
- [G1] xAI docs, "Grok Bot": https://docs.x.ai/grok-bot/overview
- [G3] Crosswalk, "Grok Bot: what it is and how to connect it" (3rd party): https://crosswalk.to/agents/grok-bot
- [I1] Doug Levin, "Just Text It: My First Look at Instinct's Personal AI Agent": https://douglevin.substack.com/p/just-text-it-my-first-look-at-instincts
- [I2] Vellum, "Official Instinct Breakdown (2026)": https://www.vellum.ai/blog/official-instinct-breakdown
- [I3] eesel, "Instinct AI review 2026": https://www.eesel.ai/blog/instinct-ai-review
- [S1] The Next Web, "Gemini Spark": https://thenextweb.com/news/google-gemini-spark-agentic-assistant-gmail-io-2026
- [S2] Tom's Guide, "Google unveils Gemini Spark": https://www.tomsguide.com/ai/google-gemini/google-unveils-gemini-spark-a-24-7-personal-ai-agent-that-could-be-a-game-changer-for-agentic-ai
- [S3] Decrypt, "Google Launches Gemini Spark": https://decrypt.co/368389/google-gemini-spark-ai-agent-challenge-hermes-openclaw
- [S4] Tech Insider, "Perplexity Comet vs ChatGPT Atlas vs Gemini Agent [2026]": https://tech-insider.org/comet-vs-gemini-agent-vs-chatgpt-atlas-2026/
- [D1] Prefill links for Claude, ChatGPT and Grok (3rd party): https://github.com/jcb79107/prefillprompt, https://treyhunner.com/2024/07/chatgpt-and-claude-from-your-browser-url-bar/
