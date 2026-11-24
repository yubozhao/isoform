package main

import (
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
	"golang.org/x/text/unicode/norm"
	"wails-gui/internal/platform/activitymon/appparser"
	"yansu-agent/agent"
)

const mainChatWebInstructions = "Use SearchWeb only when a question about the user's own day needs a public fact about a company before a meeting, a product, a place or a public topic. Search only the public name or words the user typed in this message. Never search for a person, or send mail, Messages, screen or watch text. Cite the returned web ref with CiteSources. If refused, retry with just the public name or answer without searching. Never use another search route."

type chatWebTurnKey struct{}
type chatWebLaunchKey struct{}
type chatWebLaunch struct {
	app     *App
	turn    *chatWebTurn
	started func()
}

var errChatWebTurnEnded = errors.New("this Chat turn has ended")
var errChatWebClosedEyes = errors.New("Closed Eyes is on")

// CCA14 C1 orders the outbound launch against the Closed Eyes transition.
func (l *chatWebLaunch) start(cmd *exec.Cmd) error {
	l.app.closedEyes.mu.Lock()
	defer l.app.closedEyes.mu.Unlock()
	if !l.turn.active() {
		return errChatWebTurnEnded
	}
	if l.app.IsClosedEyesModeEnabled() {
		return errChatWebClosedEyes
	}
	return startResearchProcess(cmd)
}

// CCA14 C1 binds the words and sources to the admitted turn, including when
// a tool server calls a tool with its own request context.
type chatWebTurn struct {
	tag, typed string
	done       <-chan struct{}
	mu         sync.Mutex
	blocks     []string
	sources    map[string]chatSource
	hidden     sync.Map
}

func (t *chatWebTurn) active() bool {
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

// CCA14 C1 keeps search arguments and results out of renderer-written thread
// rows. Some runners identify the result by call ID alone.
func (t *chatWebTurn) observeToolEvent(event *agent.StreamEvent) bool {
	switch event.Type {
	case "tool_use_start", "tool_use", "tool_result":
		name := strings.TrimPrefix(event.ToolName, "mcp__isoform__")
		if name == chatWebSearchToolName || name == chatSourcesToolName {
			if event.ToolUseID != "" {
				t.hidden.Store(event.ToolUseID, true)
			}
			return true
		}
		if _, ok := t.hidden.Load(event.ToolUseID); ok {
			return true
		}
		if event.Type == "tool_result" {
			t.addContext(event.Content)
		}
	}
	return false
}

func searchWords(text string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(norm.NFKC.String(text)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsNumber(r)
	}), " ")
}

func (t *chatWebTurn) addContext(text string) {
	if text == "" {
		return
	}
	block := searchWords(text)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.blocks = append(t.blocks, block)
}

func (t *chatWebTurn) refusal(query string, names []string) string {
	typed := searchWords(t.typed)
	words := searchWords(query)
	if words == "" {
		return "the query needs a public name or words from this message."
	}
	for _, name := range names {
		name = searchWords(name)
		if containsWholePhrase(words, name) && !containsWholePhrase(typed, name) {
			return "the query contains a known person's name missing from this message."
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.blocks) == 0 {
		return "the turn's privacy context is unavailable."
	}
	tokens := strings.Fields(words)
	for i := 0; i+4 <= len(tokens); i++ {
		// Check longer runs too when two overlapping phrases appeared separately in the message.
		for j := i + 4; j <= len(tokens); j++ {
			phrase := strings.Join(tokens[i:j], " ")
			if containsWholePhrase(typed, phrase) {
				continue
			}
			for _, block := range t.blocks {
				if containsWholePhrase(block, phrase) {
					return "the query copies words from private context that you did not type together."
				}
			}
			break
		}
	}
	// A short private fragment also needs provenance. Independently published
	// product and region names can be checked without sending a lookup.
	if publicSearchNames()[strings.ToLower(query)] {
		return ""
	}
	for _, word := range strings.Fields(query) {
		if !containsWholePhrase(strings.ToLower(t.typed), strings.ToLower(word)) {
			return "the query contains words whose public origin could not be verified."
		}
	}
	return ""
}

var publicSearchNames = sync.OnceValue(func() map[string]bool {
	names := map[string]bool{}
	for _, product := range appparser.Catalog() {
		names[strings.ToLower(product.Label)] = true
	}
	for _, region := range language.Supported.Regions() {
		if region.IsCountry() {
			for _, dict := range []*display.Dictionary{display.English, display.Chinese} {
				names[strings.ToLower(dict.Regions().Name(region))] = true
			}
		}
	}
	return names
})

func (t *chatWebTurn) started(query string) chatSource {
	source := chatSource{Kind: "web", ID: uuid.NewString(), Title: query, At: time.Now().UnixMilli(), Target: mainChatTarget{Kind: mainChatTargetNone}}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sources == nil {
		t.sources = make(map[string]chatSource)
	}
	t.sources[source.ID] = source
	return source
}
