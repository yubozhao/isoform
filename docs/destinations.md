# Where the person's data goes: the audit

Settings batch E1 (data-leaves v1.0.0 rules 4 and 5; checklist `docs/conformance/settings.md` L4, L5, B1). Read from the code at `27a3bfa39` (`feat/iphone-companion`, 2026-11-21). This file says what the code does today. It is the input to E2, which turns these rows into a Go list (`destinations`) and adds a test that fails when a send isn't named in it.

**Content** below means anything about the person: what was on their screen, what they said, wrote or were sent, names of people and topics, results of their work. Metadata that names nothing (a version number, a fixed line, a count, an id) is listed separately at the end.

The id in each heading is the destination's id in the build's list, `wails-gui/internal/destinations` (E2). How the build keeps the list complete is at the end.

## Summary

| Id | Destination | Carries content | On by default | Row in Your data today |
| --- | --- | --- | --- | --- |
| `assistant` | The assistant the person chose: Claude Code, Codex, or the native loop through magpie / a key they added | Yes, the most | Yes, once an assistant is set up | **No** (D5a) |
| `assistant_search` | That assistant's web search (Claude Code's WebSearch, or Grok / Gemini / Doubao in the person's Chrome through opencli) | Yes: topic labels, interests' search words, Chat queries | Yes, with topics | No |
| `agents_handoff` | The agents the person hands work to (Claude Code, Codex, others) | Yes: what each hand-off's What went lists | Only when the person taps | Yes (Sent to your agents) |
| `agents_mcp_read` | The person's own assistants reading Isoform over MCP (Claude Code, Codex) | Yes: activity search, memory, people, calendar, today's brief, playbooks, run history | Only after "Connect" in Settings | No |
| `agent_address` | Agents in their own apps (Claude, ChatGPT, Gemini, Grok), through the agent address (Isoform's front, or the bridge the person set up) | Yes: the hand-offs each agent reads, the progress and results it posts, its requests and their answers | Only while an agent in its own app is connected | No (Settings owner, after design) |
| `relay` | Isoform's relay, to the paired phone and watch | Sealed end to end; the relay can't read it | Only once a phone is paired | Yes |
| `push` | Apple's push service, through the relay | No: a fixed line per kind | Only with a paired phone | Yes |
| `text_relay` | Texts: relay, then Twilio | Generic line by default; **content when "include names" is on** | Off until the person verifies a number | No |
| `text_messages` | Texts: Messages on this computer, then Apple iMessage / the carrier | The push line, or the moment's title when names are allowed. Also "Work on it" sent by message carries the task text | Off until set up | No |
| `email` | Email: relay, then the relay's SMTP server | **Yes: a playbook's full result** | Off until the person verifies an address | No |
| `agent_of_you` | Agent of You (www.isoform.ai/api/agents, api.isoform.ai) | Yes: name and email for the reservation, the work profile the person approves, owner chats | Only while the beta is on | Only while on (D5e) |
| `chat_bridges` | Telegram, Slack, Discord, Feishu / Lark, WeChat (iLink) bots | Yes: what the person sends the bot and Isoform's answers | Only when the person connects one | No |
| `composio` | Composio, with the person's own key | What the connected apps' tools are asked | Only with a Composio key | No |
| `mcp_servers` | Remote MCP servers the person added (and the OAuth sign-in for them) | What Isoform's tools ask them | Only with a server the person added | No |
| `embeddings_api` | OpenAI's embeddings API, for memory search | Yes: memory text | Only when the person chooses OpenAI in Settings › Memory; otherwise embeddings are made on this computer (E2a removed the environment fallback) | No |
| `tavily` | Tavily web search, for researching a person | A search query about the person | Dormant: needs `TAVILY_API_KEY`, which no build sets | No |
| `telemetry` | PostHog product events | Allowlisted events only, never content (see below) | Only when the build has a key and sharing isn't off | No |
| `yansu_backend` | The retired Yansu backend (dashboard.yansu.ai and its git proxy) | Project knowledge and deliverables | Only with a legacy Yansu sign-in | No |

The rest are metadata only (end of file). Transcription and speech stay on this computer (proved below).

## L5, answered

**Topic web searches send the topic's words.** "New on your topics" (`wails-gui/topic_finds.go`, `startTopicFinds`) builds a prompt with up to six topic labels and each interest's short searches (`buildTopicFindsPromptWithSearches`). Each label is cut down to letters, digits, spaces and `.-+#'` (`topicFindsSafeLabel`). A found label that has since become a person's name or a sensitive subject is skipped (`interestsSvc().safeToSend`). A label the person typed always goes. No page, title, link or time goes. The prompt runs as a hidden schedule on the person's assistant with search only (`wails-gui/research_cli.go`). That is either their own Claude Code with only WebSearch, in an empty folder with nothing kept, so the words reach Anthropic and its search, or opencli driving Grok, Gemini or Doubao signed in in their Chrome. Daily News uses the same path. Chat's SearchWeb (`wails-gui/chat_web_search.go`) sends one query to the same sandboxed Claude Code. It refuses a query with an email address, a phone or card number, a long number, a link with parameters, or the full name of the person or someone they know. The Tavily path (`wails-gui/web_search.go`) is dormant.

Gate: the daily search runs only while observing isn't paused and "Watch my screen" is on (`topicLabelsMayLeave`). Since E2a, Closed Eyes stops every topic and Daily News search, Find new included (`topicSearchMayLeave`). It is checked when a search starts, when its job runs, and just before the words are sent. Chat's SearchWeb still works: it is a question the person typed.

**Email carries content.** `wails-gui/email_channel.go` mails only `playbook_result` moments, only to the address verified with a code (`sendEngagementEmail`). The body is the result's full `{detail}`, then the playbook's title and time (`internal/engagement/copy.go`, `renderEmailBody`). Path: computer, relay `POST /v1/hub/email/…`, relay SMTP (`relay/internal/email/smtp.go`). The relay sees the plain text: it isn't sealed, because the relay has to send it. Gate: `engagementDeliveryAllowsRead`, which refuses under Closed Eyes and when the source was protected or deleted. Reachable from Settings › Notifications (`EmailSettings`).

**Texts carry content only when the person allowed names.** Through the relay (`wails-gui/sms_channel.go`, `sms_channel_app.go`, then `relay/internal/sms/twilio.go`): without names, `SendGeneric` sends the kind's generic line, which names nothing. With names, `SendMoment` sends the kind's template filled in, for example "Isoform: {title}: {result}", "new on {topic}: {title}", "{name} came up in {where}", "I learned \"{statement}\"". Through Messages on this computer (`wails-gui/text_channel.go`, AppleScript in `text_channel_messages.go`): the push line, or the moment's title when names are allowed. It is used only for time-sensitive or requested moments when no relay subscription is active. Both use the same delivery gate as email. Separately, "Work on it" by message (`wails-gui/work_on_it.go`, `MethodMessage`) sends the prepared task's text to the handle the person picked, on their tap.

## Each destination

### `assistant`: the assistant the person chose

**Path.** Almost every model call goes through the app's agent runner, `runner.ExecuteTask` (`yansu-agent/agent/routing_runner.go`). The runner picks Claude Code or Codex, run as the person's own CLI and sign-in (so their content reaches Anthropic or OpenAI), or the native loop (`yansu-agent/agent/native_runner.go`). The native loop goes through the bundled magpie gateway (`wails-gui/magpie_sidecar.go`, a loopback process that forwards to the provider the person set up) or straight to a provider with the person's key: Anthropic, OpenAI, DeepSeek, Moonshot, DashScope, Zhipu (`wails-gui/ai_provider_presets.go`). Background one-shot calls go through `runEphemeralClaudeModelObserved` (`wails-gui/app.go`). It refuses under Closed Eyes, runs `redactActivityModelText` over the prompt, and uses no tools and no system prompt.

**What goes, by purpose** (files that call the runner):

- **Making sense of the day:** screen text extracted into events (`activity_event_extract.go`, `activity_event_stitch.go`, `activity_triage.go`, `activity_threads.go`, `activity_thread_outcome.go`, `activity_timeline_summary.go`, `activity_timeline_batch.go`, `activity_summary.go`, `activity_compose.go`, `activity_transcript.go`). **WeChat windows as pictures** (`activity_event_vision.go`, refuses under Closed Eyes). Call and watch transcripts (text only; see "Stays on this computer"). Watch conversation names (`watch_conversation_names.go`).
- **Messages notes:** the last two days of each conversation with people the person knows, redacted, for asks and commitments (`imessage_capture.go` through `runThreadOutcomePrompt`). Skipped under Closed Eyes, for protected Messages, and for people left out.
- **Meeting notes learning:** notes text, bounded (`meeting_notes_learning.go`, through the triager's knowledge and memory generation).
- **Learning:** memory and knowledge (`memory_generator.go`, `memory_knowledge_generator.go`, `memory.go`, `memory_dream.go`, `memory_tombstone_semantic.go`, `self_dream_runtime.go`, `know_how_backfill.go`), playbooks (`sop_detect.go`, `sop_describe.go`, `sop_author.go`), purpose (`purpose_extraction.go`), work profile (`work_profile_model.go`), About you (`about_you_portrait.go`), your agents' history (`assistant_history_pass2.go`), interests (`interests_service.go`), people research synthesis (`person_research.go`), meeting follow-ups (`meeting_followup.go`), setup claims (`setup_claims.go`).
- **Briefs:** `mobile_brief_narrative.go`, `mobile_brief_shadow.go`, `mobile_brief_assistant_error.go` (`mobileBriefModelCall`), `proactive_voice.go`.
- **Answers in Chat:** `streaming.go` (`executePrompt`), `main_chat_send.go`, `side_chat.go`, `mobile_chat.go` (questions from the phone), `today_chat_send.go`, `spotlight_app.go`, `brain_dump.go`. Images and files the person attaches go with them. Chat is started by the person; these paths don't check Closed Eyes themselves.
- **Suggestions:** `main_chat_chips.go`, `suggestion_generator.go`, `recommended_questions.go`.
- **Topic proposals:** topic names (`topics_daily.go`; off by default behind its switch).
- **Scheduled runs and playbooks:** the person's own prompts (`cron.go`, `cron_exec.go`), and `handoff_verify.go`, `bg_shell_tracker.go`, `worktree.go`.
- **Chat bridges:** what arrives from a connected bot (`daemon.go`, `telegram_local.go`, `slack_local.go`, `slack_pipeline.go`, `discord_local.go`, `feishu_local.go`, `wechat_monitor.go`, including images from WeChat).

**Gate.** Closed Eyes stops the one-shot helper and the activity pipeline (the triager and the vision pass check it). Each source's switch and the protected-apps list are checked by the `*AllowsModelRead` filters. Calls the person starts (Chat, playbooks) are not stopped by Closed Eyes; topic and Daily News searches are (E2a).

**For E3.** Brief, answer and proposal calls all reach the runner, so a record written at the runner boundary (or at the three named entry points) sees every call. The runner already tags each call with a caller label (`agent.RegisterCallerLabelHelpers`, `[routing] caller=`).

### `assistant_search`: the assistant's web search

As in L5 above. The native loop's own WebFetch (`yansu-agent/agent/tools/web.go`) fetches pages the model picks. `research_cli.go` (Claude Code with only WebSearch, no WebFetch, so a result link can't carry data out), `cron_app_research.go` (opencli in Chrome), `topic_finds.go`, `today_news.go` (Daily News), `chat_web_search.go`.

### `agents_handoff`: hand-offs to the person's agents

`handoff_run.go` (refuses under Closed Eyes), `main_chat_handoff.go`, `chat_handoff_card.go`, `mobile_handoff_start.go`, `work_handoff_actions.go`, `work_on_it.go` (deep link, clipboard, or Messages). What goes is what the card's What went lists. The agent then sends it to its own provider. This is the existing "Sent to your agents" row.

### `agents_mcp_read`: the person's assistants reading Isoform

`yansu-agent/mcpread/server.go` and `wails-gui/mcp_read_*.go`. Tools: `activity_search`, `memory_search`, `memory_list`, `people_lookup`, `calendar_today`, `calendar_range`, `brief_today`, `playbook_status`, `run_history`. A token is issued only by the Settings connection action (`issueMCPReadToken`). Closed Eyes answers "paused" (`mcp_read_integration_test.go`). What an assistant reads this way goes to that assistant's provider. **It has no row today.**


### `agent_address`: agents in their own apps, through the agent address

`agent_mcp_service.go` (`recordAddressAnswer`: one count per answered tool call from an agent that joined through the address; refusals and agents on this computer aren't counted) and `agent_address.go`. The address opens only while such an agent is connected or a join for one is waiting. With Isoform's front, TLS ends on this computer; with a configured bridge, the bridge can read what passes. Stop is `CloseAgentAddress`: it ends a waiting address join and disconnects every agent connected through the address, and the address closes with the last one. Your data's leaves row is `agentAddress` (open, service, apps, this week).
### `relay` and `push`

`mobile_bridge.go` (every byte over the outbound relay socket as end-to-end sealed envelopes, PROTOCOL.md §4 and §6), `mobile_attachment.go` (the spoken brief, sealed with a per-file key), `mobile_recordings.go` (the computer fetches the watch and phone recordings the phone sealed). Push: `relay/internal/apnspush/client.go` sends Apple the title "Isoform" and a fixed line per kind (`momentFallbackText`, or "Isoform needs you."), plus the sealed notice as ciphertext. Both match the current rows.

### `text_relay`, `text_messages`, `email`

As in L5 above.

### `agent_of_you`

`your_agent.go` and `your_agent_client.go` (`https://www.isoform.ai/api/agents`): reserving a name sends the name and email; verify, resend, choose, cancel. `agent_owner.go` and `agent_server_client.go` (`https://api.isoform.ai`): the work profile, sent only by the local approval (`UploadApprovedProfile`, PUT `/v1/owner/profile`); owner settings, client decisions and owner chats; payouts onboarding. `front_service.go`: a cloud agent's TLS reaches this computer through `agents.isoform.ai` still encrypted; TLS ends on this computer. Off unless `front_enabled`.

### `chat_bridges`

`telegram_local.go` (api.telegram.org), `slack_local.go` (Socket Mode), `discord_local.go`, `feishu_local.go` (open.feishu.cn / open.larksuite.com), `wechat_api.go` and `wechat_download.go` (ilinkai.weixin.qq.com and its CDN). Each runs only once the person connects their own bot, and carries what they send it and Isoform's answers.

### `composio`

`composio_direct.go` (backend.composio.dev, the person's own key): status, connect, disconnect, and MCP configuration for the apps they connect. Tool calls then go to those apps.

### `embeddings_api`

`memory_embedding.go`, `resolveEmbeddingProvider`: memory text goes to OpenAI only when the person chose OpenAI in Settings › Memory (`memory.json`, `provider: "openai"`). Until E2a, an `OPENAI_API_KEY` in the app's environment chose it with no setting showing it; that fallback is gone (`TestEmbeddingsIgnoreOpenAIKeyInEnvironment`). Otherwise: local ONNX, then a local Ollama.

### `tavily`

`web_search.go`, used only by `person_research.go` (`gatherWebFindings`): a query generated about the person being researched. It runs only when `TAVILY_API_KEY` is set; no build sets it.

### `telemetry`

`telemetry.go`, `telemetry_product.go`: PostHog, with a key and host stamped in by `build-wails.sh` only. An allowlist of product events and properties ("agent traces, context snapshots and arbitrary properties never leave here"). Rechecked at the network boundary (`telemetryTransport`), and off when the person turns sharing off. It is metadata about use, not content; whether Your data names it is design's call.

### `yansu_backend`

`remote_api.go`, `clone.go` (`PushKnowledge`, `PullKnowledge`, git proxy with a token), `deliverables.go` (multipart upload), `auth.go` (`dashboard.yansu.ai`). Only with a legacy Yansu sign-in. The Yansu gateway is retired (`ai_provider.go`). E2 should either list it or remove it.

## Metadata only (no content)

- Update checks and downloads: `update_staging.go`, `version.go`, `tool_downloads.go`, `bundled_tts.go` and `bundled_sherpa.go` (model files), `memory_model_download.go`, `magpie_bootstrap.go`, the `download-*.sh` binaries.
- Setup probes: `assistant_setup.go` and `setup_agents.go` (install scripts from claude.ai / chatgpt.com, a local Ollama probe). The one "tiny real call" in setup is a fixed probe prompt.
- Catalogs: `mcp_hub_registry.go` (GitHub READMEs, glama.ai), `skill_hub.go` and `skill_hub_github.go` (clawhub.ai, GitHub).
- `front_ct_watch.go`: asks certspotter about the front's own hostname, once a day.
- `mcp_oauth.go`: the OAuth flow for a remote MCP server the person adds.

## Stays on this computer (proved)

- **Watch and call audio** is transcribed on this computer with sherpa-onnx (`asr_sherpa.go`). No build path sends audio to a model: the runner's input has `Images` and files, no audio (`yansu-agent/agent/types.go`), and no code calls a remote speech API. **The transcript text is then read by the assistant** (making sense of the day), so data-leaves v1.1.0's "Watch audio transcribed on your computer" is true about the audio only.
- **Your voice:** speech is made by the bundled sherpa TTS models (`bundled_tts.go`); only the model files are downloaded.
- Embeddings default to the local ONNX model (`newLocalONNXProvider`), apart from `embeddings_api` above.

## What this means for the page

1. "That's every place your data goes." can't be shown yet, and with these rows it needs E2's test: the page today lacks rows for `assistant`, `assistant_search`, `agents_mcp_read`, `text_relay`, `text_messages`, `email`, `chat_bridges`, `composio`, and the conditional `embeddings_api`.
2. Rows design needs words for, beyond data-leaves v1.0.0 rule 1: the assistant's web search (topic labels and Chat queries), email (playbook results), texts (only with names on), your assistants reading Isoform over MCP, and connected chat bots. Composio, embeddings and the legacy backend are for E2 to settle in code (list or remove) before they need words.
3. Find new under Closed Eyes now returns `paused`; Today needs words for it (design).

## How the build keeps this list complete (E2)

- **The list.** `wails-gui/internal/destinations/destinations.go` holds every id above, plus `metadata` (no content) and `local` (a connection to this computer itself). Your data reads its rows from `destinations.List()`. Wiring the rows into `GetYourDataSummary` lands with E3, after batch B's change to `your_data_summary.go`.
- **The tag.** Every function or package-level value that sends over the network carries `//isoform:destination <id>[,<id>]` in its doc comment.
- **The test.** `TestEverySendIsListed` (`wails-gui/internal/destinations/send_scan_test.go`) parses the shipped Go source of `wails-gui` (with `quick-chat-spotlight`) and `yansu-agent`, skipping tests, `frontend`, `scripts` and `wails-gui/cmd`. It fails when a function that sends has no tag, or names an id that isn't in the list. It counts as sending: building an HTTP request or client, dialing (net, tls, websocket), constructing a network SDK's client, and running a sending program named by a literal (curl, git, ssh, npm and the like).
- **Dependencies.** `TestEveryDependencyIsClassified` fails when a direct dependency in either module's `go.mod` isn't marked as able to send or not. One that can send must have its constructors in the scan.
- **Programs found at run time** (the person's Claude Code, magpie, osascript for Messages, the research CLI, the MCP read server's stdout) can't be seen from a literal. Those functions are in `requiredDirectives`: renaming one or removing its tag fails the test.
- **What it can't see:** a sending program started through a path variable in a function that isn't in `requiredDirectives`, and what a program Isoform starts does on its own (the assistant's tools, an MCP server the person added). Those are covered by their parent's row (`assistant`, `mcp_servers`).
