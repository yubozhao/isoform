package main

import "strings"

// Chat reads your day and runs no commands (computer-chat v1.2.0 rule 6;
// computer-conformance-answers v1.4.0 S25 and S19). Its native turns lose the
// shell, the file writers, the generic web fetchers and computer control; it
// reads through its own tools (chat_native_reads.go, chat_context_tools.go)
// and searches the web only with its gated SearchWeb. The external route
// already runs with no built-in tools (external_runner_controlled.go).

// chatNoCommandTools are the built-in tools a main-chat turn never gets.
var chatNoCommandTools = []string{
	"Bash", "BashOutput", "KillShell",
	"Write", "Edit", "NotebookEdit",
	"WebFetch", "WebSearch",
	"cua_driver",
}

const chatMemoryHelp = `Memory is the user's short, durable notes: preferences, habits, current topics, and how they want Isoform to behave. Relevant notes are already in <memory-context> when they match this message. To look further, use MemorySearch for recall, MemoryList to browse (optionally by dimension: user_insight, topic_highlight, deep_user_model, agentic_memory, custom) and MemoryShow to read one. Cite the refs they print.`

const chatActivityHelp = `Activity is what the user did and heard on this computer and their watch: sessions by app and window, screen text, calls and meetings with their transcripts, and watch conversations. Use RecentActivity for the last few hours, ActivityDay for a date's narrative and sessions, ActivityList for recent sessions, ActivitySearch to find sessions about something, and ActivitySession for one session's detail or a meeting's transcript. Cite the refs they print.`

// mainChatProductContext is Chat's product context: who Isoform is in Chat
// and how it speaks. It names no CLI, automation, skill or shell routine.
func (a *App) mainChatProductContext(languageName string) string {
	return `<yansu-context>
You are Isoform, in Chat. Chat answers questions about the user's own day from what Isoform saw, and lets them set Isoform straight.
- Read with the Isoform tools: MemorySearch, MemoryList, MemoryShow, PeopleLookup, RecentActivity, ActivityDay, ActivityList, ActivitySearch, ActivitySession.
- Chat runs no commands and changes no files. Work the user asks for (writing, research, code, an automation or a schedule) is work for their agent: offer it with HandOffWork.
- Existing automations are changed only through <upcoming-tools> when they are offered.
RULES:
- ` + responseLanguageInstruction(languageName) + `
- NEVER show users code, config files, or dev setup steps. Users are non-technical.
- Keep responses concise — 2-3 sentences max.
- Be warm, personable, and conversational. Mirror the user's tone.
` + a.connectedIntegrationsContext() + `</yansu-context>

`
}

// mainChatMcpContext lists the tool servers Chat may use while one is
// connected, without the CLI that installs or manages them: no new one can
// be added from Chat (computer-conformance-answers v1.4.0 S19).
func (a *App) mainChatMcpContext() string {
	full := a.mcpHubContext()
	if full == "" || strings.Contains(full, "No MCP servers are currently enabled") {
		return ""
	}
	if cut := strings.Index(full, "\nManage MCP servers via the yansu CLI:"); cut >= 0 {
		return full[:cut] + "\n</mcp-servers>\n\n"
	}
	return full
}
