// Package workonit prepares a task Isoform proposes (a brief item, a topic
// proposal, a chat card) for the person's own agent, and ranks the agents they
// actually get work done with. Isoform keeps the context, the judgment and the
// routing; the agent does the work (docs/work-on-it-agents.md).
//
// v1 is the prepared-task path: the person sees the exact text, taps, and the
// text goes to the agent by a deep link, by opening the agent with the text on
// the clipboard, or as a message. Nothing is sent without that tap, and only
// counts are kept: never the text.
package workonit

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Source kinds a task can come from.
const (
	SourceBriefItem     = "brief_item"
	SourceTopicProposal = "topic_proposal"
	SourceChatCard      = "chat_card"
)

// ValidSource reports a known source kind.
func ValidSource(kind string) bool {
	switch kind {
	case SourceBriefItem, SourceTopicProposal, SourceChatCard:
		return true
	default:
		return false
	}
}

// Handoff methods, best first.
const (
	// MethodTaskLink makes a one-time /t/<token> link to the task and sends
	// the agent one line with the link, by the best carrier it has (see
	// TaskLinkCarrier). It is the default for every agent and ranks first
	// because a result comes back.
	MethodTaskLink = "task_link"
	// MethodDeepLink opens the agent with the task already in its composer;
	// the person sends it there.
	MethodDeepLink = "deep_link"
	// MethodPaste has an existing computer-use agent open the agent and paste
	// the task into its message box, then stop; the person sends it there.
	MethodPaste = "paste"
	// MethodOpenCopy opens the agent's app or site and puts the task on the
	// clipboard; the person pastes and sends.
	MethodOpenCopy = "open_copy"
	// MethodMessage sends the task as a message through Messages, on the
	// person's tap, to the agent's number they set.
	MethodMessage = "message"
	// MethodShare is the phone's share sheet.
	MethodShare = "share"
)

// Agent is one personal agent Isoform can hand work to.
type Agent struct {
	ID   string
	Name string
	// BundleIDs and AppNames find the agent's Mac app in capture.
	BundleIDs []string
	AppNames  []string
	// TitleWords find the agent in a browser tab title.
	TitleWords []string
	// DeepLink is a URL template with one %s for the URL-encoded task.
	DeepLink string
	// OpenURL opens the agent's app or site without a task.
	OpenURL string
	// Message is true for agents reached by a text (Instinct).
	Message bool
	// PhoneSchemes are the URL schemes the phone checks with canOpenURL,
	// only to build its menu; the answer never leaves the phone.
	PhoneSchemes []string
	// TermsNote names the one-line note the sheet shows before the first
	// handoff (an i18n key), empty when there is none.
	TermsNote string
}

// TaskLinkCarrier is how the one-line task-link prompt reaches the agent:
// its deep link, a message, or opening it with the line on the clipboard.
// Every personal agent can fetch a page (Bo, 2026-11-14: "all of them have
// browser"), so the task link is the default for each; MCP connectors are
// optional and later. Empty when the agent has no way to receive the line.
func (a Agent) TaskLinkCarrier(installed bool, handle string) string {
	switch {
	case a.DeepLink != "" && installed:
		return MethodDeepLink
	case a.Message && strings.TrimSpace(handle) != "":
		return MethodMessage
	case (installed && len(a.BundleIDs) > 0) || a.OpenURL != "":
		return MethodOpenCopy
	default:
		return ""
	}
}

// Catalog is the declared list. Order is the tie-break.
func Catalog() []Agent {
	return []Agent{
		{
			// Claude Desktop documents claude://claude.ai/new?q= (the
			// composer is prefilled for review, about 14,000 characters).
			ID: "claude", Name: "Claude",
			BundleIDs:  []string{"com.anthropic.claudefordesktop", "com.anthropic.claude"},
			AppNames:   []string{"Claude"},
			TitleWords: []string{"Claude"},
			DeepLink:   "claude://claude.ai/new?q=%s",
			OpenURL:    "https://claude.ai/new", PhoneSchemes: []string{"claude"},
		},
		{
			ID: "chatgpt", Name: "ChatGPT",
			BundleIDs:  []string{"com.openai.chat"},
			AppNames:   []string{"ChatGPT"},
			TitleWords: []string{"ChatGPT"},
			OpenURL:    "https://chatgpt.com/", PhoneSchemes: []string{"chatgpt"},
		},
		{
			ID: "codex", Name: "Codex",
			BundleIDs: []string{"com.openai.codex"},
			AppNames:  []string{"Codex"},
		},
		{
			ID: "grok", Name: "Grok",
			AppNames:   []string{"Grok", "Grok Bot"},
			TitleWords: []string{"Grok"},
			OpenURL:    "https://grok.com/bot", PhoneSchemes: []string{"grok"},
		},
		{
			ID: "muse", Name: "Muse",
			AppNames:   []string{"Muse"},
			TitleWords: []string{"Muse"},
			OpenURL:    "https://muse.ai/", PhoneSchemes: []string{"muse"},
		},
		{
			// Instinct is reached by a text. Its terms allow training on
			// what it receives, so the sheet says so first.
			ID: "instinct", Name: "Instinct",
			Message: true, TermsNote: "workOnIt.terms.instinct",
		},
	}
}

// AgentByID finds a catalog agent.
func AgentByID(id string) (Agent, bool) {
	for _, a := range Catalog() {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// Methods lists the handoffs an agent offers on the computer, best first.
// installed says whether its Mac app is there; handle is the number set for a
// text agent.
func (a Agent) Methods(installed bool, handle string) []string {
	var out []string
	if a.DeepLink != "" && installed {
		out = append(out, MethodDeepLink)
	}
	if (installed && len(a.BundleIDs) > 0) || a.OpenURL != "" {
		out = append(out, MethodPaste, MethodOpenCopy)
	}
	if a.Message && strings.TrimSpace(handle) != "" {
		out = append(out, MethodMessage)
	}
	return out
}

// DeepLinkURL fills the agent's deep link with the task.
func (a Agent) DeepLinkURL(text string) (string, error) {
	if a.DeepLink == "" {
		return "", fmt.Errorf("%s has no deep link", a.ID)
	}
	// Claude's q is about 14,000 characters; keep well under it.
	return fmt.Sprintf(a.DeepLink, url.QueryEscape(truncateRunes(text, DeepLinkMaxRunes))), nil
}

// DeepLinkMaxRunes bounds a task put in a URL.
const DeepLinkMaxRunes = 8000

// Input is what the surface shows for the item: its own words, nothing more.
type Input struct {
	SourceKind string `json:"sourceKind"`
	SourceID   string `json:"sourceId"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	// Done is what done looks like, when the item says.
	Done string `json:"done"`
}

// Task is a prepared task, held in memory until it is sent or dropped.
type Task struct {
	ID         string    `json:"id"`
	SourceKind string    `json:"sourceKind"`
	SourceID   string    `json:"sourceId"`
	Text       string    `json:"text"`
	PreparedAt time.Time `json:"preparedAt"`
}

// MaxTaskRunes bounds the prepared text.
const MaxTaskRunes = 4000

// PrepareText writes the task the person will see and send. It carries the
// item's own words and what done looks like, and asks the agent to reply
// with what it did. Lines are given in the person's language by the caller.
func PrepareText(in Input, words Words) string {
	var b strings.Builder
	title := clean(in.Title)
	detail := clean(in.Detail)
	done := clean(in.Done)
	b.WriteString(title)
	if detail != "" && detail != title {
		b.WriteString("\n\n")
		b.WriteString(detail)
	}
	if done != "" {
		b.WriteString("\n\n")
		b.WriteString(words.DoneLabel)
		b.WriteString(" ")
		b.WriteString(done)
	}
	if words.Closing != "" {
		b.WriteString("\n\n")
		b.WriteString(words.Closing)
	}
	return truncateRunes(b.String(), MaxTaskRunes)
}

// Words are the fixed lines of a prepared task, in the person's language.
type Words struct {
	DoneLabel string // "Done looks like:"
	Closing   string // "When you're done, tell me what you did."
}

func clean(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// Usage is what capture shows about one agent over the ranking window.
type Usage struct {
	// Prompts are keyboard bursts in the agent: the person asking for work.
	Prompts int
	// Copies are clipboard copies made in the agent: results taken out to
	// be used elsewhere.
	Copies int
}

// Score weighs results the person used above prompts. Open time is not
// counted at all: an agent that is open but never used ranks nowhere.
func (u Usage) Score() int {
	return u.Prompts + 4*u.Copies
}

// Ranked is one agent in the menu.
type Ranked struct {
	Agent   Agent
	Usage   Usage
	Methods []string
}

// Rank orders the agents that offer a handoff: by use, then catalog order.
// Agents with no handoff method are left out.
func Rank(usage map[string]Usage, installed map[string]bool, handles map[string]string) []Ranked {
	var out []Ranked
	order := map[string]int{}
	for i, a := range Catalog() {
		order[a.ID] = i
		methods := a.Methods(installed[a.ID], handles[a.ID])
		if len(methods) == 0 {
			continue
		}
		out = append(out, Ranked{Agent: a, Usage: usage[a.ID], Methods: methods})
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := out[i].Usage.Score(), out[j].Usage.Score()
		if si != sj {
			return si > sj
		}
		return order[out[i].Agent.ID] < order[out[j].Agent.ID]
	})
	return out
}

// Outcome states of a sent task.
const (
	StatusSent    = "sent"
	StatusTaken   = "taken"
	StatusDone    = "done"
	StatusNotDone = "not_done"
)

// Record is the measure kept for a sent task: counts and times, never text.
type Record struct {
	ID         string `json:"id"`
	SourceKind string `json:"sourceKind"`
	AgentID    string `json:"agentId"`
	Method     string `json:"method"`
	SentAt     int64  `json:"sentAt"`
	TakenAt    int64  `json:"takenAt,omitempty"`
	DoneAt     int64  `json:"doneAt,omitempty"`
	Status     string `json:"status"`
	AskedAt    int64  `json:"askedAt,omitempty"`
}

// AskAfter is how long Isoform waits for a result before asking the person
// whether the task got done.
const AskAfter = 24 * time.Hour

// DueToAsk reports a sent or taken task with no result after AskAfter that
// has not been asked about yet.
func (r Record) DueToAsk(now time.Time) bool {
	if r.Status == StatusDone || r.Status == StatusNotDone || r.AskedAt != 0 {
		return false
	}
	return now.UnixMilli()-r.SentAt >= AskAfter.Milliseconds()
}
