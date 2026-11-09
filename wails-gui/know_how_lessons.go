package main

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// One lesson, once (design review v1.0.1 W40): near-duplicate know-how shows
// as one card, and learning it again adds a moment to the copy already saved.

// KnowHowMoment is one time a lesson was learned, for Where it came from.
type KnowHowMoment struct {
	At        int64  `json:"at"`
	Assistant string `json:"assistant"` // "claude" or "codex" for assistant history
	App       string `json:"app"`
	SessionID string `json:"sessionId"`
}

const knowHowRelearnedKey = "relearned"

var knowHowLessonStopWords = map[string]bool{"the": true, "a": true, "an": true, "my": true, "your": true, "our": true}

func knowHowLessonTokens(title string) map[string]bool {
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	tokens := make(map[string]bool, len(words))
	for _, word := range words {
		if !knowHowLessonStopWords[word] {
			tokens[word] = true
		}
	}
	return tokens
}

func sameKnowHowLessonTokens(a, b map[string]bool) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if !sameKnowHowPolarity(a, b) {
		return false
	}
	shared := 0
	for word := range a {
		if b[word] {
			shared++
		}
	}
	smaller := len(a)
	if len(b) < smaller {
		smaller = len(b)
	}
	// One title is the other with a word dropped ("deploys after repo rename").
	if shared == smaller && smaller >= 4 {
		return true
	}
	if float64(shared)/float64(len(a)+len(b)-shared) >= 0.8 {
		return true
	}
	// The saved public-sync SSH variants describe the same retry even when
	// they name the failure differently. Keep this narrower than a generic
	// overlap threshold: similar titles can describe different lessons.
	return a["retry"] && b["retry"] && a["public"] && b["public"] && a["sync"] && b["sync"] && a["ssh"] && b["ssh"] && shared >= 4
}

func sameKnowHowPolarity(a, b map[string]bool) bool {
	// A missing negation can reverse the instruction, even when every other
	// word matches. Never merge these on token overlap alone.
	for _, word := range []string{"not", "never", "without", "no"} {
		if a[word] != b[word] {
			return false
		}
	}
	return true
}

func sameKnowHowLesson(a, b string) bool {
	return sameKnowHowLessonTokens(knowHowLessonTokens(a), knowHowLessonTokens(b))
}

func knowHowEntryMoments(entry KnowledgeEntry, card KnowHowCard) []KnowHowMoment {
	first := KnowHowMoment{At: card.SourceAt, Assistant: card.SourceAssistant}
	if first.At == 0 {
		first.At = card.SavedAt
	}
	// History and CLAUDE.md entries carry a project folder as their app.
	if card.Source != assistantHistorySource && card.Source != assistantHistoryClaudeMd && len(card.Apps) > 0 {
		first.App = card.Apps[0]
	}
	if len(entry.SessionIDs) > 0 {
		first.SessionID = entry.SessionIDs[0]
	}
	moments := []KnowHowMoment{first}
	if doc, ok := parseOKFDocument(entry.Content); ok {
		for _, raw := range okfStringSlice(doc.Frontmatter, knowHowRelearnedKey) {
			if moment, ok := parseKnowHowMoment(raw); ok {
				moments = append(moments, moment)
			}
		}
	}
	return moments
}

// A relearned moment is stored as "timestamp|assistant|app|session".
func formatKnowHowMoment(m KnowHowMoment) string {
	return strings.Join([]string{okfTimestamp(time.UnixMilli(m.At)), m.Assistant, m.App, m.SessionID}, "|")
}

func parseKnowHowMoment(raw string) (KnowHowMoment, bool) {
	parts := strings.SplitN(raw, "|", 4)
	for len(parts) < 4 {
		parts = append(parts, "")
	}
	at := parseOKFTimestampMs(parts[0])
	if at <= 0 {
		return KnowHowMoment{}, false
	}
	return KnowHowMoment{At: at, Assistant: parts[1], App: parts[2], SessionID: parts[3]}, true
}

// Identical substantive takeaways describe the same lesson even when titles differ.
func sameKnowHowContent(a, b KnowHowCard) bool {
	left, right := strings.TrimSpace(a.Takeaway), strings.TrimSpace(b.Takeaway)
	if len([]rune(left)) >= 40 && strings.EqualFold(left, right) {
		return true
	}
	if len(a.Points) == 0 || len(a.Points) != len(b.Points) {
		return false
	}
	joined := strings.TrimSpace(strings.Join(a.Points, " "))
	return len([]rune(joined)) >= 40 && strings.EqualFold(joined, strings.TrimSpace(strings.Join(b.Points, " ")))
}

// mergeKnowHowLessons folds near-duplicate cards into the first one saved.
// Cards arrive newest first and leave in the same order.
func mergeKnowHowLessons(cards []KnowHowCard) []KnowHowCard {
	type lesson struct {
		card   KnowHowCard
		tokens map[string]bool
	}
	lessons := make([]lesson, 0, len(cards))
	for i := len(cards) - 1; i >= 0; i-- {
		card := cards[i]
		tokens := knowHowLessonTokens(card.Title)
		merged := false
		for j := range lessons {
			if !sameKnowHowPolarity(lessons[j].tokens, tokens) || (!sameKnowHowLessonTokens(lessons[j].tokens, tokens) && !sameKnowHowContent(lessons[j].card, card)) {
				continue
			}
			into := &lessons[j].card
			into.MergedIDs = append(into.MergedIDs, card.ID)
			into.MergedIDs = append(into.MergedIDs, card.MergedIDs...)
			into.Moments = append(into.Moments, card.Moments...)
			into.SessionCount += card.SessionCount
			if into.ReuseCount >= 0 && card.ReuseCount >= 0 {
				into.ReuseCount += card.ReuseCount
			} else {
				into.ReuseCount = -1
			}
			if card.SavedAt > into.SavedAt {
				into.SavedAt = card.SavedAt
			}
			into.Apps = compactStrings(append(into.Apps, card.Apps...))
			merged = true
			break
		}
		if !merged {
			lessons = append(lessons, lesson{card: card, tokens: tokens})
		}
	}
	out := make([]KnowHowCard, 0, len(lessons))
	for i := len(lessons) - 1; i >= 0; i-- {
		card := lessons[i].card
		sort.SliceStable(card.Moments, func(a, b int) bool { return card.Moments[a].At < card.Moments[b].At })
		out = append(out, card)
	}
	return out
}

// relearnKnowHow adds a moment to an already-saved copy of the same lesson
// and returns its ID, so learning it again adds nothing new. except is the
// entry being written, which is an update of itself rather than a copy.
func relearnKnowHow(dir, title, except string, moment KnowHowMoment, sessionIDs ...string) (string, bool) {
	return relearnKnowHowWithPoints(dir, title, except, moment, nil, sessionIDs...)
}

// Preserve newly learned details as well as the source moment. A match is not
// evidence that the new extraction contains no additional information.
func relearnKnowHowWithPoints(dir, title, except string, moment KnowHowMoment, points []string, sessionIDs ...string) (string, bool) {
	return relearnKnowHowWithProvenance(dir, title, except, moment, points, "unknown", nil, sessionIDs...)
}

func relearnKnowHowWithProvenance(dir, title, except string, moment KnowHowMoment, points []string, source string, apps []string, sessionIDs ...string) (string, bool) {
	return relearnKnowHowWithEvidence(dir, title, except, moment, points, source, apps, newKnowledgeGenerationProvenance(), sessionIDs...)
}

func relearnKnowHowWithEvidence(dir, title, except string, moment KnowHowMoment, points []string, source string, apps []string, evidence knowledgeGenerationProvenance, sessionIDs ...string) (string, bool) {
	tokens := knowHowLessonTokens(title)
	if len(tokens) == 0 {
		return "", false
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var match *KnowledgeEntry
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || !strings.HasSuffix(name, ".md") || strings.TrimSuffix(name, ".md") == except {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		entry := parseKnowledgeFile(name, string(data))
		if !sameKnowHowLessonTokens(tokens, knowHowLessonTokens(entry.Title)) {
			continue
		}
		if match == nil || parseOKFTimestampMs(entry.Date) < parseOKFTimestampMs(match.Date) {
			e := entry
			match = &e
		}
	}
	if match == nil {
		return "", false
	}
	unlock := memoryFileLocks.Acquire("knowledge:" + match.ID)
	defer unlock()
	path := filepath.Join(dir, match.ID+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	doc, ok := parseOKFDocument(string(data))
	if !ok {
		return "", false
	}
	if moment.At <= 0 {
		moment.At = time.Now().UnixMilli()
	}
	seen := okfStringSlice(doc.Frontmatter, knowHowRelearnedKey)
	duplicate := false
	for _, raw := range seen {
		if prior, ok := parseKnowHowMoment(raw); ok && ((moment.SessionID == "" && prior.At == moment.At && prior.Assistant == moment.Assistant && prior.App == moment.App) || (moment.SessionID != "" && prior.SessionID == moment.SessionID)) {
			duplicate = true
			break
		}
	}
	if !duplicate {
		doc.Frontmatter[knowHowRelearnedKey] = append(seen, formatKnowHowMoment(moment))
	}
	previousSessions := okfStringSlice(doc.Frontmatter, "activity_sessions")
	allSessions := make([]string, 0, len(previousSessions)+len(sessionIDs)+1)
	seenSessions := make(map[string]bool)
	for _, id := range append(append(append([]string{}, previousSessions...), sessionIDs...), moment.SessionID) {
		id = strings.TrimSpace(id)
		if id != "" && !seenSessions[id] {
			seenSessions[id] = true
			allSessions = append(allSessions, id)
		}
	}
	if len(allSessions) > 0 {
		doc.Frontmatter["activity_sessions"] = allSessions
	}
	body, addedPoints := addKnowHowPoints(doc.Body, points)
	doc.Body = body
	updated := knowledgeProvenance(renderOKFDocument(doc.Frontmatter, doc.Body), string(data), knowledgeProvenanceUnion([]string{source}, evidence.sources), knowledgeProvenanceUnion(apps, evidence.apps), evidence.complete && knowledgeProvenanceKnown(string(data)))
	if duplicate && len(previousSessions) == len(allSessions) && !addedPoints && updated == string(data) {
		return match.ID, true
	}
	if err := snapshotKnowledgeFile(dir, match.ID); err != nil {
		log.Printf("[know-how] snapshot before relearning failed: %v", err)
		return "", false
	}
	if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
		log.Printf("[know-how] relearned moment not saved: %v", err)
		return "", false
	}
	return match.ID, true
}

func addKnowHowPoints(body string, points []string) (string, bool) {
	known := make(map[string]bool)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			known[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "- ")))] = true
		}
	}
	var added []string
	for _, point := range points {
		point = strings.TrimSpace(point)
		key := strings.ToLower(point)
		if point != "" && !known[key] {
			known[key] = true
			added = append(added, "- "+point)
		}
	}
	if len(added) == 0 {
		return body, false
	}
	const heading = "## Key Insights"
	start := strings.Index(body, heading)
	if start < 0 {
		return strings.TrimSpace(body) + "\n\n" + heading + "\n\n" + strings.Join(added, "\n") + "\n", true
	}
	end := len(body)
	if next := strings.Index(body[start+len(heading):], "\n## "); next >= 0 {
		end = start + len(heading) + next
	}
	return strings.TrimRight(body[:end], "\n") + "\n" + strings.Join(added, "\n") + "\n" + body[end:], true
}

// Keep the accumulated source moments when an existing lesson is rewritten.
func preserveKnowHowMoments(content, previous string) string {
	old, oldOK := parseOKFDocument(previous)
	next, nextOK := parseOKFDocument(content)
	if !oldOK || !nextOK {
		return content
	}
	if moments := okfStringSlice(old.Frontmatter, knowHowRelearnedKey); len(moments) > 0 {
		next.Frontmatter[knowHowRelearnedKey] = moments
	}
	return renderOKFDocument(next.Frontmatter, next.Body)
}
