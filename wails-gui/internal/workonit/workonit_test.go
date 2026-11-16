package workonit

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPrepareTextCarriesTheItemAndAsksForTheResult(t *testing.T) {
	got := PrepareText(Input{SourceKind: SourceBriefItem, Title: "  Draft the  launch note ", Detail: "For Thursday's\nrelease.", Done: "A one-page note"},
		Words{DoneLabel: "Done looks like:", Closing: "When you're done, tell me what you did."})
	want := "Draft the launch note\n\nFor Thursday's release.\n\nDone looks like: A one-page note\n\nWhen you're done, tell me what you did."
	if got != want {
		t.Fatalf("text = %q", got)
	}
	long := PrepareText(Input{Title: strings.Repeat("a", MaxTaskRunes+50)}, Words{})
	if len([]rune(long)) != MaxTaskRunes {
		t.Fatalf("long text = %d runes", len([]rune(long)))
	}
}

func TestDeepLinkEncodesTheTask(t *testing.T) {
	claude, _ := AgentByID("claude")
	link, err := claude.DeepLinkURL("Fix the build & ship?")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "claude://claude.ai/new?q=") {
		t.Fatalf("link = %s", link)
	}
	parsed, err := url.Parse(link)
	if err != nil || parsed.Query().Get("q") != "Fix the build & ship?" {
		t.Fatalf("q = %q (%v)", parsed.Query().Get("q"), err)
	}
	chatgpt, _ := AgentByID("chatgpt")
	if _, err := chatgpt.DeepLinkURL("x"); err == nil {
		t.Fatal("an agent without a deep link made one")
	}
}

func TestMethodsBestFirst(t *testing.T) {
	claude, _ := AgentByID("claude")
	if got := strings.Join(claude.Methods(true, ""), ","); got != "deep_link,paste,open_copy" {
		t.Fatalf("claude installed = %s", got)
	}
	if got := strings.Join(claude.Methods(false, ""), ","); got != "paste,open_copy" {
		t.Fatalf("claude on the web = %s", got)
	}
	codex, _ := AgentByID("codex")
	if got := codex.Methods(false, ""); len(got) != 0 {
		t.Fatalf("codex without its app = %v", got)
	}
	instinct, _ := AgentByID("instinct")
	if got := instinct.Methods(false, ""); len(got) != 0 {
		t.Fatalf("instinct without a number = %v", got)
	}
	if got := strings.Join(instinct.Methods(false, "+15550100"), ","); got != "message" {
		t.Fatalf("instinct with a number = %s", got)
	}
	if instinct.TermsNote == "" {
		t.Fatal("instinct has no terms note")
	}
}

// TestRankWeighsUsedResultsOverPrompts: copies (results taken out) count
// four times a prompt; open time is not an input at all.
func TestRankWeighsUsedResultsOverPrompts(t *testing.T) {
	usage := map[string]Usage{
		"chatgpt": {Prompts: 30},
		"claude":  {Prompts: 10, Copies: 6},
		"grok":    {},
	}
	ranked := Rank(usage, map[string]bool{"claude": true}, nil)
	var ids []string
	for _, r := range ranked {
		ids = append(ids, r.Agent.ID)
	}
	if got := strings.Join(ids, ","); got != "claude,chatgpt,grok,gemini,muse" {
		t.Fatalf("rank = %s", got)
	}
}

func TestDueToAskOnceAfterADay(t *testing.T) {
	sent := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	r := Record{SentAt: sent.UnixMilli(), Status: StatusSent}
	if r.DueToAsk(sent.Add(23 * time.Hour)) {
		t.Fatal("asked before a day")
	}
	if !r.DueToAsk(sent.Add(25 * time.Hour)) {
		t.Fatal("not asked after a day")
	}
	r.AskedAt = sent.Add(25 * time.Hour).UnixMilli()
	if r.DueToAsk(sent.Add(50 * time.Hour)) {
		t.Fatal("asked twice")
	}
	if (Record{SentAt: sent.UnixMilli(), Status: StatusDone}).DueToAsk(sent.Add(48 * time.Hour)) {
		t.Fatal("asked about a done task")
	}
}

// TestEveryAgentHasAKind: the two choices (phone-agent v1.1.0 rule 1) cover
// the whole catalog, with the design's nine in it.
func TestEveryAgentHasAKind(t *testing.T) {
	want := map[string]string{
		"codex": KindCode, "claude-code": KindCode, "cursor": KindCode,
		"claude": KindElse, "chatgpt": KindElse, "grok": KindElse, "gemini": KindElse, "muse": KindElse, "instinct": KindElse,
	}
	for _, a := range Catalog() {
		if !ValidKind(a.Kind) {
			t.Errorf("%s has no kind", a.ID)
		}
		if k, ok := want[a.ID]; ok && k != a.Kind {
			t.Errorf("%s kind = %s, want %s", a.ID, a.Kind, k)
		}
		delete(want, a.ID)
	}
	if len(want) != 0 {
		t.Errorf("missing from the catalog: %v", want)
	}
}

func TestKindOfIsCodeOnlyWhenSaid(t *testing.T) {
	if KindOf(Input{}) != KindElse || KindOf(Input{Kind: "bogus"}) != KindElse || KindOf(Input{Kind: KindCode}) != KindCode {
		t.Fatal("kind of work")
	}
}
