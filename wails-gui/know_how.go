package main

import (
	"log"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
)

// KnowHowCard is the structured owner-page projection of a knowledge entry.
type KnowHowCard struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Kind          string   `json:"kind"`
	Takeaway      string   `json:"takeaway"`
	Trigger       string   `json:"trigger"` // when-to-use; only entries that carry one
	Points        []string `json:"points"`
	Apps          []string `json:"apps"`
	SessionCount  int      `json:"sessionCount"`
	SavedAt       int64    `json:"savedAt"`
	ReuseCount    int      `json:"reuseCount"` // -1 when the use log could not be read
	TopicID       string   `json:"topicId"`
	TopicName     string   `json:"topicName"`
	FormatVersion string   `json:"formatVersion"`
	Source        string   `json:"source"`
	// SourceAt is the session's time for an entry read from assistant history
	// (unix ms); 0 for entries without one. SourceAssistant is "claude" or
	// "codex" for those same entries.
	SourceAt        int64  `json:"sourceAt"`
	SourceAssistant string `json:"sourceAssistant"`
	Scope           string `json:"scope"` // mine by default; metadata, never authority to share
	// Moments are each time this lesson was learned, oldest first; MergedIDs
	// are the near-duplicate entries folded into this card.
	Moments   []KnowHowMoment `json:"moments"`
	MergedIDs []string        `json:"mergedIds"`
}

// KnowHowMatch identifies a local error's earlier fix without retaining the error.
type KnowHowMatch struct {
	EntryID    string `json:"entryId"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	CurrentApp string `json:"currentApp"`
	PreviousAt int64  `json:"previousAt"`
	Signature  string `json:"signature"`
}

// Production uses App.GetEntryUseCounts; tests can substitute the counter.
type knowHowUseCounter interface {
	GetEntryUseCounts(kind string, ids []string) (map[string]int, error)
}

var _ knowHowUseCounter = (*App)(nil)

type knowHowState struct {
	matchMu    sync.Mutex
	cacheRunID string
	cacheKey   string
	cacheAt    time.Time
	cached     *KnowHowMatch
	counter    knowHowUseCounter
	backfillMu sync.Mutex
	// The candidate proposal: one at a time, recomputed at most every TTL, and
	// served warm only while the privacy fingerprint it was computed under
	// still matches exactly (source revision, pause log, sensitive apps,
	// Closed Eyes).
	candMu      sync.Mutex
	candAt      time.Time
	candCached  *KnowHowCandidate
	candPrivacy knowHowCandidatePrivacy
}

func validKnowHowKind(kind string) bool {
	switch kind {
	case "decision", "how_to", "fix", "gotcha", "reference":
		return true
	default:
		return false
	}
}
func normalizeKnowHowKind(kind string) string {
	if validKnowHowKind(kind) {
		return kind
	}
	return "reference"
}
func updatedKnowHowKind(content, proposed string) string {
	if validKnowHowKind(proposed) {
		return proposed
	}
	kind, _ := knowHowEntryKind(content)
	return normalizeKnowHowKind(kind)
}

// ListKnowHow returns the same owner-visible entries and order as GetKnowledgeEntries.
func (a *App) ListKnowHow() (perfValue []KnowHowCard, perfErr error) {
	defer logPageBinding("ListKnowHow", time.Now(), &perfValue)
	return derivedRead(a, derivedKnowHow, "", a.listKnowHowNow, derivedMemoryEditStamp)
}

func (a *App) listKnowHowNow() ([]KnowHowCard, error) {
	entries, err := a.GetKnowledgeEntries()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	counter := a.knowHow.counter
	if counter == nil {
		counter = a
	}
	// Counts are extra: a use-log failure still lists every entry, with
	// its count marked unknown.
	counts, err := counter.GetEntryUseCounts("knowledge", ids)
	if err != nil {
		log.Printf("[know-how] reuse counts unavailable: %v", err)
	}
	cards := make([]KnowHowCard, 0, len(entries))
	for _, entry := range entries {
		card := knowHowCard(entry)
		card.ReuseCount = counts[entry.ID]
		if err != nil {
			card.ReuseCount = -1
		}
		card.Moments = knowHowEntryMoments(entry, card)
		cards = append(cards, card)
	}
	return mergeKnowHowLessons(cards), nil
}

func knowHowCard(entry KnowledgeEntry) KnowHowCard {
	doc, ok := parseOKFDocument(entry.Content)
	body := entry.Content
	if ok {
		body = doc.Body
	}
	kind, _ := knowHowEntryKind(entry.Content)
	card := KnowHowCard{
		ID: entry.ID, Title: pii.DisplayText(entry.Title),
		Kind:         normalizeKnowHowKind(kind),
		Takeaway:     okfString(doc.Frontmatter, "description"),
		Trigger:      okfString(doc.Frontmatter, "trigger"),
		Points:       knowHowSectionPoints(body, "## Key Insights", 6),
		Apps:         compactStrings(splitCommaList(entry.Apps)),
		SessionCount: len(entry.SessionIDs), SavedAt: parseOKFTimestampMs(entry.Date),
		FormatVersion:   okfString(doc.Frontmatter, "okf_version"),
		Source:          okfString(doc.Frontmatter, "source"),
		SourceAt:        parseOKFTimestampMs(okfString(doc.Frontmatter, "history_session_at")),
		SourceAssistant: okfString(doc.Frontmatter, "history_assistant"),
		Scope:           "mine",
	}
	if scope := okfString(doc.Frontmatter, "scope"); scope != "" {
		card.Scope = scope
	}
	if card.Takeaway == "" {
		card.Takeaway = knowHowSection(body, "## Description")
	}
	if !ok {
		card.Source, _ = knowHowLegacyMetadataValue(entry.Content, "Source")
	}
	// An observed how-to is the confirmed procedure, so its detail must keep
	// every reviewed step rather than the usual six-point summary.
	if card.Kind == "how_to" && card.Source == "observed" {
		card.Points = knowHowSectionPoints(body, "## Key Insights", knowHowCandidateMaxEditStep)
	}
	card.Takeaway = pii.DisplayText(card.Takeaway)
	// The when-to-use line is display text like the title and points: the
	// stored file keeps the owner's raw words, the card never shows them.
	card.Trigger = pii.DisplayText(card.Trigger)
	for i := range card.Points {
		card.Points[i] = pii.DisplayText(card.Points[i])
	}
	return card
}

func knowHowSection(body, heading string) string {
	lines := strings.Split(body, "\n")
	var section []string
	inside := false
	for _, line := range lines {
		if strings.TrimSpace(line) == heading {
			inside = true
			continue
		}
		if inside && strings.HasPrefix(strings.TrimSpace(line), "#") {
			break
		}
		if inside {
			section = append(section, line)
		}
	}
	return strings.TrimSpace(strings.Join(section, "\n"))
}
func knowHowSectionPoints(body, heading string, limit int) []string {
	points := make([]string, 0)
	for _, line := range strings.Split(knowHowSection(body, heading), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			points = append(points, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- ")))
			if len(points) == limit {
				break
			}
		}
	}
	return points
}

// Older knowledge uses Markdown metadata as its frontmatter. Keep that dialect
// intact: converting it to YAML would change the existing owner-page parser.
func knowHowLegacyMetadataStart(content string) int {
	offset := 0
	for _, line := range strings.SplitAfter(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			return -1
		}
		if strings.HasPrefix(trimmed, "- **") {
			return offset
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "# ") {
			return -1
		}
		offset += len(line)
	}
	return -1
}

func knowHowEntryKind(content string) (string, bool) {
	if doc, ok := parseOKFDocument(content); ok {
		_, exists := doc.Frontmatter["kind"]
		return okfString(doc.Frontmatter, "kind"), exists
	}
	return knowHowLegacyMetadataValue(content, "Kind")
}

func knowHowLegacyMetadataValue(content, key string) (string, bool) {
	start := knowHowLegacyMetadataStart(content)
	if start < 0 {
		return "", false
	}
	for _, line := range strings.Split(content[start:], "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			break
		}
		if match := knowledgeMetaRe.FindStringSubmatch(trimmed); match != nil && match[1] == key {
			return strings.TrimSpace(match[2]), true
		}
	}
	return "", false
}
