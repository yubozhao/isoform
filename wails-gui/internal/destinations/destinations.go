// Package destinations is the build's one list of the places the person's
// data goes off this computer (data-leaves v1.0.0 rule 4; the audit is
// docs/destinations.md).
//
// Every function in the shipped Go code that sends over the network names
// what it sends to with a directive in its doc comment:
//
//	//isoform:destination email
//	func (c *emailChannel) Send(ctx context.Context, subject, body string) error {
//
// A send that carries nothing about the person names a Metadata id (an
// update check, a model download). TestEverySendIsListed (send_scan_test.go)
// walks the source of wails-gui and yansu-agent and fails when a function
// that builds a request, a client, a connection or a network SDK client, or
// runs a program that sends, has no directive, or names an id this list
// doesn't hold. Your data reads its rows from List.
package destinations

// ID names a destination. The ids are stable: Your data and the call record
// keep them.
type ID string

const (
	Assistant       ID = "assistant"
	AssistantSearch ID = "assistant_search"
	AgentsHandoff   ID = "agents_handoff"
	AgentsMCPRead   ID = "agents_mcp_read"
	Relay           ID = "relay"
	Push            ID = "push"
	TextRelay       ID = "text_relay"
	TextMessages    ID = "text_messages"
	Email           ID = "email"
	AgentOfYou      ID = "agent_of_you"
	ChatBridges     ID = "chat_bridges"
	Composio        ID = "composio"
	MCPServers      ID = "mcp_servers"
	EmbeddingsAPI   ID = "embeddings_api"
	Telemetry       ID = "telemetry"
	YansuBackend    ID = "yansu_backend"
	Metadata        ID = "metadata"
	Local           ID = "local"
)

// Content says what of the person's goes.
type Content string

const (
	// ContentYes: the person's content (screen text, words, names, results).
	ContentYes Content = "content"
	// ContentSealed: content, sealed end to end; the destination can't read it.
	ContentSealed Content = "sealed"
	// ContentChosen: content only when the person turned it on (names in texts).
	ContentChosen Content = "chosen"
	// ContentNone: nothing about the person (a fixed line, a version, a count).
	ContentNone Content = "none"
)

// Destination is one row of the list.
type Destination struct {
	ID ID
	// Where is who receives it, in plain words.
	Where string
	// What is what goes, in plain words. Never the person's data itself.
	What    string
	Content Content
	// When is the condition under which anything goes at all.
	When string
}

// list is ordered as Your data shows it: the largest flow first.
var list = []Destination{
	{Assistant, "The assistant the person chose: Claude Code, Codex, or a provider through magpie or their own key",
		"Text from the screen, calls and watch, pictures of WeChat windows, Messages for asks, briefs, Chat questions with what they're written from, topic names for proposals",
		ContentYes, "once an assistant is set up; background reading stops under Closed Eyes"},
	{AssistantSearch, "That assistant's web search, or Grok, Gemini or Doubao in the person's Chrome",
		"Topic labels and interests' search words, Daily News topics, public search words from Chat questions that needed the web",
		ContentYes, "with topics or Daily News on, and when Chat searches"},
	{AgentsHandoff, "The agents the person hands work to",
		"What each hand-off's What went lists",
		ContentYes, "only when the person hands work off"},
	{AgentsMCPRead, "The person's own assistants, reading Isoform over MCP",
		"Activity search, memory, people, calendar, today's brief, playbooks, run history",
		ContentYes, "only after Connect in Settings; answers paused under Closed Eyes"},
	{Relay, "Isoform's relay, to the paired phone and watch",
		"Today's items, briefs and results",
		ContentSealed, "only with a paired phone"},
	{Push, "Apple's push service, through the relay",
		"A fixed line per kind, and the sealed notice",
		ContentNone, "only with a paired phone"},
	{TextRelay, "Texts through the relay and Twilio",
		"A generic line; the moment's details when names are on",
		ContentChosen, "only to a number the person verified"},
	{TextMessages, "Texts through Messages on this computer",
		"The notification line, or its title when names are on; a Work on it task sent by message",
		ContentChosen, "only when set up, or when the person sends a task by message"},
	{Email, "Email through the relay's mail server",
		"A playbook's result",
		ContentYes, "only to an address the person verified, for playbooks set to email"},
	{AgentOfYou, "Agent of You",
		"The name and email for the reservation, the work profile the person approves, owner chats",
		ContentYes, "only while the beta is on"},
	{ChatBridges, "Telegram, Slack, Discord, Feishu or Lark, WeChat",
		"What the person sends the bot and Isoform's answers",
		ContentYes, "only with a bot the person connected"},
	{Composio, "Composio, with the person's own key",
		"Which apps are connected, and what their tools are asked",
		ContentYes, "only with a Composio key"},
	{MCPServers, "The MCP servers the person added",
		"What Isoform's tools ask them",
		ContentYes, "only with a server the person added"},
	{EmbeddingsAPI, "OpenAI or the configured remote Ollama address",
		"Memory text and search queries, for embeddings",
		ContentYes, "only when the person selects that provider in Settings"},
	{Telemetry, "PostHog, Isoform's product analytics",
		"Allowlisted product events, never content",
		ContentNone, "only when the build carries a key and sharing isn't off"},
	{YansuBackend, "The retired Yansu backend",
		"Project knowledge and deliverables",
		ContentYes, "only with a legacy Yansu sign-in"},
	{Metadata, "Update, download and catalog servers",
		"Versions, file names, fixed probes",
		ContentNone, "always"},
	{Local, "Nothing leaves: a connection to this computer itself (loopback, the local magpie, Ollama, Chrome's debugging port)",
		"Nothing", ContentNone, "always"},
}

// List returns every destination, in Your data's order.
func List() []Destination {
	out := make([]Destination, len(list))
	copy(out, list)
	return out
}

// Lookup returns the destination with id.
func Lookup(id ID) (Destination, bool) {
	for _, d := range list {
		if d.ID == id {
			return d, true
		}
	}
	return Destination{}, false
}
