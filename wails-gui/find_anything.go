package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"wails-gui/internal/pii"
)

// ⌘K (computer-find v1.0.0 §18b): one search over what Isoform keeps, grouped
// Ready (Your work), Conversations, People, Claims (About you) and Your
// answers, best match first in each group. Every group reads what its page
// already shows: the page snapshots (built through the privacy gates and
// dropped by any withdrawal), the answers ledger as Your answers shows it, and
// the gated activity search for conversations. So nothing forgotten or
// withdrawn can come back through ⌘K that its page would not show. The whole
// search answers within findBudget; a group that misses it is named in
// FailedGroups and the rest still show. Queries are not kept here (see
// NoteFindSearch).
const (
	findBudget       = 90 * time.Millisecond
	findDefaultLimit = 5
	findMaxLimit     = 20
)

// Group names, in the order ⌘K shows them.
const (
	findGroupReady         = "ready"
	findGroupConversations = "conversations"
	findGroupPeople        = "people"
	findGroupClaims        = "claims"
	findGroupAnswers       = "answers"
)

// FindForget names what Forget removes for a found thing; ForgetFound takes
// it. "screen" is a screen moment, "belief" an About you line (its Forget
// needs the confirmation token of the belief's forget preview), "answer" one
// Your answers entry. A found thing with no Forget of its own (a Your work
// item, which is answered instead, a thread, a person) has none. A belief's
// token comes from GetMemoryForgetPreview.
type FindForget struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// FindResult is one found thing. Source is a fixed key the client words:
// Your work's brief, topic, prep or agent; "activity" for a conversation;
// "about_you" for a claim; an answer's own source (brief, topic, watch, prep,
// agent, about_you, chat). A person has none.
type FindResult struct {
	ID      string       `json:"id"`
	Group   string       `json:"group"`
	Title   string       `json:"title"`
	Snippet string       `json:"snippet,omitempty"`
	Source  string       `json:"source,omitempty"`
	At      int64        `json:"at,omitempty"`
	Target  SearchTarget `json:"target"`
	Forget  *FindForget  `json:"forget,omitempty"`
}

type FindGroup struct {
	Group   string       `json:"group"`
	Results []FindResult `json:"results"`
}

type FindResults struct {
	Query        string      `json:"query"`
	Groups       []FindGroup `json:"groups"`
	FailedGroups []string    `json:"failedGroups"`
}

type findSource struct {
	group string
	read  func(context.Context, []string) ([]searchCandidate, error)
}

// FindAnything is ⌘K's search.
func (a *App) FindAnything(query string, limit int) (perfValue FindResults, perfErr error) {
	defer logPageBinding("FindAnything", time.Now(), &perfValue)
	return a.findAnything(context.Background(), query, limit, a.findSources())
}

func (a *App) findSources() []findSource {
	return []findSource{
		{findGroupReady, a.findReady},
		{findGroupConversations, a.searchActivity},
		{findGroupPeople, a.findPeople},
		{findGroupClaims, a.findClaims},
		{findGroupAnswers, a.findAnswers},
	}
}

// findQueryWords applies SearchEverything's query rules: 2 to 200 characters,
// or one CJK character.
func findQueryWords(query string) []string {
	runes := []rune(query)
	if len(runes) > 200 {
		return nil
	}
	if len(runes) < 2 && (len(runes) != 1 || !(unicode.Is(unicode.Han, runes[0]) || unicode.Is(unicode.Hiragana, runes[0]) || unicode.Is(unicode.Katakana, runes[0]) || unicode.Is(unicode.Hangul, runes[0]))) {
		return nil
	}
	return strings.Fields(strings.ToLower(query))
}

func (a *App) findAnything(ctx context.Context, query string, limit int, sources []findSource) (FindResults, error) {
	query = strings.TrimSpace(query)
	out := FindResults{Query: query, Groups: []FindGroup{}, FailedGroups: []string{}}
	words := findQueryWords(query)
	if len(words) == 0 {
		return out, nil
	}
	if limit <= 0 {
		limit = findDefaultLimit
	}
	limit = min(limit, findMaxLimit)
	ctx, cancel := context.WithTimeout(ctx, findBudget)
	defer cancel()
	type answer struct {
		results []FindResult
		err     error
	}
	answers := make([]chan answer, len(sources))
	var wg sync.WaitGroup
	for i, source := range sources {
		answers[i] = make(chan answer, 1)
		wg.Add(1)
		go func(source findSource, done chan<- answer) {
			defer wg.Done()
			candidates, err := source.read(ctx, words)
			if err != nil {
				done <- answer{err: err}
				return
			}
			// The ranker looks at its first candidates only; a page snapshot
			// lists everything, so only the ones that match go to it.
			ranked := rankSearchCandidates(ctx, findMatching(candidates, words), words, query, limit)
			if err := ctx.Err(); err != nil {
				done <- answer{err: err}
				return
			}
			results := make([]FindResult, 0, len(ranked))
			for _, r := range ranked {
				results = append(results, findResultOf(source.group, r))
			}
			done <- answer{results: results}
		}(source, answers[i])
	}
	for i, source := range sources {
		var got answer
		select {
		case got = <-answers[i]:
		default:
			// A group that finished in time is kept even when a slower one
			// used up the budget first.
			select {
			case got = <-answers[i]:
			case <-ctx.Done():
				select {
				case got = <-answers[i]:
				default:
					got.err = ctx.Err()
				}
			}
		}
		if got.err != nil {
			out.FailedGroups = append(out.FailedGroups, source.group)
			continue
		}
		if len(got.results) > 0 {
			out.Groups = append(out.Groups, FindGroup{Group: source.group, Results: got.results})
		}
	}
	return out, nil
}

// findMatching keeps the candidates whose shown words match, redacted as
// the ranker redacts them. Raw text is checked first, which is cheap; only a
// raw match is redacted and checked again, so a secret's words never match.
func findMatching(candidates []searchCandidate, words []string) []searchCandidate {
	out := candidates[:0:0]
	for _, c := range candidates {
		if !searchMatches(c.result.Title+"\n"+c.body, words) {
			continue
		}
		if searchMatches(pii.DisplayText(c.result.Title)+"\n"+pii.DisplayText(c.body), words) {
			out = append(out, c)
		}
	}
	return out
}

// findResultOf turns a ranked candidate into a found thing. Place carries the
// source key and Target.Kind what Forget removes.
func findResultOf(group string, r SearchResult) FindResult {
	var forget *FindForget
	switch group {
	case findGroupClaims, findGroupAnswers:
		forget = &FindForget{Kind: r.Target.Kind, ID: r.Target.ID}
	case findGroupConversations:
		if r.Target.ActivityID != "" {
			forget = &FindForget{Kind: "screen", ID: r.Target.ActivityID}
		}
	}
	source := r.Place
	return FindResult{ID: group + ":" + r.ID, Group: group, Title: r.Title, Snippet: r.Snippet, Source: source, At: r.At, Target: r.Target, Forget: forget}
}

func findMillis(stamp string) int64 {
	if at, err := time.Parse(time.RFC3339, stamp); err == nil {
		return at.UnixMilli()
	}
	return 0
}

// findReady is Your work as Today shows it (the work-list snapshot).
func (a *App) findReady(ctx context.Context, _ []string) ([]searchCandidate, error) {
	list, err := a.workListNow()
	if err != nil {
		return nil, err
	}
	out := make([]searchCandidate, 0, len(list.Items))
	for _, item := range list.Items {
		title := item.Work.Title
		if title == "" {
			title = item.Row
		}
		out = append(out, searchCandidate{
			result: SearchResult{ID: item.ID, Place: item.Source, Title: title, At: findMillis(item.CreatedAt), Target: SearchTarget{Kind: "work", ID: item.ID}},
			body:   item.Row + "\n" + item.Work.Text,
		})
	}
	return out, ctx.Err()
}

// findPeople is Who you know's directory (the people-overview snapshot):
// names and relationships, never notes, research or messages.
func (a *App) findPeople(ctx context.Context, _ []string) ([]searchCandidate, error) {
	overview, err := a.ListPeopleOverview()
	if err != nil {
		return nil, err
	}
	out := make([]searchCandidate, 0, len(overview.Everyone))
	for _, card := range overview.Everyone {
		out = append(out, searchCandidate{
			result: SearchResult{ID: card.ID, Title: card.Name, At: card.LastSeenAt, Target: SearchTarget{Kind: "person", ID: card.ID}},
			body:   card.Relationship,
		})
	}
	return out, ctx.Err()
}

// findClaims is About you's beliefs (the about-you snapshot).
func (a *App) findClaims(ctx context.Context, _ []string) ([]searchCandidate, error) {
	view, err := a.GetAboutYou()
	if err != nil {
		return nil, err
	}
	out := make([]searchCandidate, 0, len(view.Beliefs))
	for _, belief := range view.Beliefs {
		out = append(out, searchCandidate{
			result: SearchResult{ID: belief.ID, Place: "about_you", Title: belief.Title, At: belief.UpdatedAt, Target: SearchTarget{Kind: "belief", ID: belief.ID}},
		})
	}
	return out, ctx.Err()
}

// findAnswers is Your answers as its page lists them (the ledger, newest
// first, without the ones an Undo took back).
func (a *App) findAnswers(ctx context.Context, _ []string) ([]searchCandidate, error) {
	page, err := a.GetAnswers(answersPageMax, "")
	if err != nil {
		return nil, err
	}
	out := make([]searchCandidate, 0, len(page.Answers))
	for _, answer := range page.Answers {
		if answer.Undone || answer.Title == "" {
			continue
		}
		out = append(out, searchCandidate{
			result: SearchResult{ID: answer.EntryID, Place: answer.Source, Title: answer.Title, At: findMillis(answer.At), Target: SearchTarget{Kind: "answer", ID: answer.EntryID}},
			body:   answer.Note,
		})
	}
	return out, ctx.Err()
}

// ForgetFound forgets a found thing through the binding its page uses, so ⌘K
// forgets exactly what Forget there would. confirmationToken is the belief's
// forget-preview token; other kinds ignore it.
func (a *App) ForgetFound(kind, id, confirmationToken string) error {
	switch kind {
	case "screen":
		return a.ForgetActivityFeedRow("screen", id)
	case "belief":
		return a.ForgetMemoryEntry(id, confirmationToken)
	case "answer":
		return a.ForgetAnswer(id)
	default:
		return fmt.Errorf("nothing to forget for %q", kind)
	}
}
