# Agents join v1.1.0: how each app agent really connects today, and what that changes

Version 1.1.0 · written November 15, 2026, 9:21 PM Pacific, by Claude Design.

**This file changes agents-join v1.0.0 where it says so; everything else in it stands.** v1.0.0 treated "a chat app with the Isoform connector" as one kind. I checked tonight (company pages for Claude and Grok, third-party guides for ChatGPT and Gemini): the apps differ in who can add a tool server, where, and whether it works on the phone. The design has to say what's true for each, or it promises a connection people can't make.

**Board:** Isoform Computer, new artboard 21 (Connecting an app agent). Artboards 4, 13, 20 and the phone's 11 and 13 now use Grok, not Muse, as the connected agent another agent asks (Muse has no way to add a tool server that I could find).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **The connect sheet shows the steps that work in that app,** or says plainly that the app can't connect.
2. **"Connected" is never shown for an agent that can't send results back by itself.**

## 1. What each app allows, as of today

Checked November 15, 2026, against the sources at the end (two are third-party guides). The build keeps this table and rechecks it on each company's own pages at each release; any change to it is a new version of this file.

| Agent | Kind | How it connects |
| --- | --- | --- |
| Claude Code, Codex, Cursor's agent | Runs on this computer | One click from Agents; Isoform starts it (v1.0.0 §6) |
| Claude (app) | Connected, in its app | Customize › Connectors › Add › Add custom connector, paste the address; on every plan (Free allows one custom connector); works on the phone too, because the connection runs from Anthropic's cloud |
| ChatGPT | Connected, two ways | (a) the Isoform app in ChatGPT's app directory, one click, phone too, **once Isoform is listed** (C7 below); (b) until then, only with Developer mode, a beta on the web, on Plus and higher plans: Settings › Security and login › Developer mode, then add the server, and choose Isoform from + in each chat |
| Gemini | Connected, in its app | gemini.google.com › Settings › Connected Apps › Custom apps, paste the address; added on the web, then it's in the phone app too |
| Grok | Connected, in its app | grok.com/connectors › New Connector › Custom, paste the address |
| Muse, Instinct, others with no tool servers | Not connected | The task is written in (Messages or the app); results through the report link (v1.0.0 §9) |

## 2. The connect sheet, per app

Artboard 21. Changes v1.0.0 §2 rule 4 and the join page's step 3.

1. **After the agent says where it works,** the sheet shows that app's steps in three: **Copy this address** (the join address, with Copy); **Add it in {app}** (the path from §1, in the app's own words); **Check the code** (v1.0.0 rule 5). Under them, "Waiting for {app} · the address works once, for 10 minutes".
2. **The person adds it in the app's own screen.** Isoform never asks for the app's password and never drives the app's settings.
3. **For ChatGPT before Isoform is listed,** the sheet says: "ChatGPT can connect to Isoform only with Developer mode, on the web, on Plus and higher. Without it, ChatGPT gets each task written in, and sends results back through a link." with **Set up Developer mode** (steps, as above) and **Use it without connecting**. The second choice adds ChatGPT as Not connected.
4. **For an app with no way to connect** (Muse today), the sheet says so before anyone pastes anything: "{app} can't connect to Isoform yet. It gets each task written in, and sends results back through a link." with **Add {app}** (as Not connected).

## 3. What changes in the hand-off

5. **ChatGPT through Developer mode, on the phone, is Not connected:** Developer mode works on the web only, so the phone's hand-off writes the task in, with the report link (phone-agent v1.2.0 rule 8). On the computer, **Open in ChatGPT** opens chatgpt.com with the hand-off written in, and the sheet's foot says "In ChatGPT, choose Isoform from + before you send."
6. **Claude, Gemini and Grok** keep **Open in {app}** (v1.0.0 §6) on both devices.
7. **The join page's step 3 for chat apps** now reads: "Chat app: tell your person where to add the Isoform address in your app's settings (Claude: Customize › Connectors; Gemini: Settings › Connected Apps; Grok: grok.com/connectors; ChatGPT: its app directory, or Developer mode on the web). If your app can't add one, say so; Isoform will write each task in for you instead."

## What the backend needs

- **C7 · List Isoform in ChatGPT's app directory,** so ChatGPT users connect in one click, on the phone too. Until it's listed, rule 3 applies.
- **The per-app table (§1) as data,** with each app's steps and limits, so the sheet and the join page read from one place.

## Every word

| Key | English | Where |
| --- | --- | --- |
| connect.copyAddress | Copy this address | Connect sheet |
| connect.addIn | Add it in {app} | Connect sheet |
| connect.checkCode | Check the code | Connect sheet |
| connect.waitingApp | Waiting for {app} · the address works once, for 10 minutes | Connect sheet |
| connect.claudeSteps | Customize › Connectors › Add › Add custom connector. Paste the address, then Add. Claude's own screen asks you to confirm. | Connect sheet, Claude |
| connect.chatgptDev | ChatGPT can connect to Isoform only with Developer mode, on the web, on Plus and higher. Without it, ChatGPT gets each task written in, and sends results back through a link. | Connect sheet, ChatGPT |
| connect.setUpDev | Set up Developer mode | Connect sheet, ChatGPT |
| connect.useWithout | Use it without connecting | Connect sheet, ChatGPT |
| connect.cantYet | {app} can't connect to Isoform yet. It gets each task written in, and sends results back through a link. | Connect sheet |
| connect.addNotConnected | Add {app} | Connect sheet |
| handoff.chatgptChoose | In ChatGPT, choose Isoform from + before you send. | Hand-off sheet foot |

## Proof

On Bo's computer and phone, named builds, with recordings:

1. **Claude:** the sheet's steps connect the Claude app; a hand-off opened in Claude on the phone is read there and comes back as Back from Claude.
2. **ChatGPT without Developer mode:** the sheet says so, and Use it without connecting adds it as Not connected; its hand-off is written in with the report link.
3. **Muse:** the sheet says it can't connect before anything is pasted.

## On the map (shell)

- JN7 · The connect sheet per app (Claude, ChatGPT, Gemini, Grok: copy the address, add it in the app, check the code); apps that can't connect say so first; ChatGPT needs its app directory listing (C7) or Developer mode on the web.

## Sources

- Anthropic: [Get started with custom connectors using remote MCP](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp)
- ChatGPT: [ChatGPT MCP: How to Connect Any MCP Server to ChatGPT (2026)](https://hjarni.com/blog/how-to-use-mcp-with-chatgpt) (a third-party guide, May 2026; to recheck on OpenAI's own help pages at release)
- Gemini: [Gemini Custom Apps (MCP)](https://skillsplayground.com/integrations/gemini/) (third-party; to recheck on Google's pages at release)
- xAI: [Grok connectors](https://docs.x.ai/grok/connectors) (updated October 10, 2026)
