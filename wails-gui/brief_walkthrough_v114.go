package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode"
)

func mobileBriefExcludedValue(terms []string, value any) bool {
	encoded, err := json.Marshal(value)
	return err != nil || mobileBriefExcluded(terms, string(encoded))
}

// ── Box 2: one next time, the time the brief is ready ──

// mobileBriefReadyAt is when an adaptive prediction's brief is ready, the time
// Today, Tasks and the phone show (today-review v1.1.4 #2): its slot's
// scheduled time while that is still ahead, never when preparing starts. A
// late slot made now, or at its retry, keeps the prediction's own time.
func mobileBriefReadyAt(p mobileBriefTimingPrediction, now time.Time, loc *time.Location, settings mobileBriefSettings) time.Time {
	if loc == nil {
		loc = time.Local
	}
	for _, fixed := range mobileBriefTodaySlots(p.ScheduledAt.In(loc), loc, settings) {
		if fixed.BriefID == p.BriefID && fixed.Slot == p.Slot && fixed.ScheduledAt.After(now) && fixed.ScheduledAt.After(p.ScheduledAt) {
			return fixed.ScheduledAt
		}
	}
	return p.ScheduledAt
}

// ── Box 1: a stated exclusion holds ──

var mobileBriefExclusionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:keep|leave)\s+(.{2,40}?)\s+out\s+of\s+(?:my|your|the)\s+(?:morning\s+|evening\s+|daily\s+)?briefs?\b`),
	regexp.MustCompile(`(?i)\b(?:don't|do\s+not|never)\s+(?:include|put|mention|bring\s+up)\s+(.{2,40}?)\s+in\s+(?:my|your|the)\s+(?:morning\s+|evening\s+|daily\s+)?briefs?\b`),
	regexp.MustCompile(`(?i)\bno\s+more\s+(.{2,40}?)\s+in\s+(?:my|your|the)\s+(?:morning\s+|evening\s+|daily\s+)?briefs?\b`),
	regexp.MustCompile(`(?:不要|别|不用)把(.{1,15}?)(?:放|写|列)(?:进|到|在)?(?:我的)?简报`),
	regexp.MustCompile(`简报(?:里|中)(?:不要|别|不用)(?:再)?(?:提|放|写|出现|包括)?(.{1,15}?)(?:了)?(?:[。，,.;；]|$)`),
}

var mobileBriefExclusionLead = regexp.MustCompile(`(?i)^(?:the|my|any|all|anything\s+about|stuff\s+about|items?\s+about)\s+`)

// mobileBriefStatedExclusions reads what the person said to keep out of their
// briefs from their own About-you notes, newest first. Each is a short term
// ("PT"); it holds until the note is changed or removed.
func mobileBriefStatedExclusions(entries []MemoryEntry) []string {
	var out []string
	seen := map[string]bool{}
	for _, entry := range entries {
		for _, text := range []string{entry.Title, entry.Content} {
			for _, line := range strings.Split(text, "\n") {
				for _, pattern := range mobileBriefExclusionPatterns {
					for _, match := range pattern.FindAllStringSubmatch(line, -1) {
						term := strings.Trim(strings.TrimSpace(match[1]), `"'“”‘’「」`)
						for {
							trimmed := mobileBriefExclusionLead.ReplaceAllString(term, "")
							if trimmed == term {
								break
							}
							term = trimmed
						}
						key := strings.ToLower(term)
						if term == "" || seen[key] || len([]rune(term)) > 40 {
							continue
						}
						seen[key] = true
						out = append(out, term)
					}
				}
			}
		}
	}
	return out
}

// mobileBriefExcluded reports whether text names one of the excluded terms:
// as a whole word in Latin text, anywhere in Han text.
func mobileBriefExcluded(terms []string, texts ...string) bool {
	for _, term := range terms {
		if term == "" {
			continue
		}
		var pattern *regexp.Regexp
		if mobileBriefHasHan(term) {
			pattern = regexp.MustCompile(regexp.QuoteMeta(term))
		} else {
			pattern = regexp.MustCompile(`(?i)(?:^|[^\pL\pN])` + regexp.QuoteMeta(term) + `(?:s|es)?(?:$|[^\pL\pN])`)
		}
		for _, text := range texts {
			if pattern.MatchString(text) {
				return true
			}
			// PT can be written out or described as AFO calf exercises in a
			// commitment without repeating the abbreviation.
			if strings.EqualFold(strings.TrimSpace(term), "PT") && (mobileBriefPTWords.MatchString(text) || mobileBriefAFOExercise.MatchString(text)) {
				return true
			}
		}
	}
	return false
}

var mobileBriefPTWords = regexp.MustCompile(`(?i)\bphysical\s+therap(?:y|ist)\b`)
var mobileBriefAFOExercise = regexp.MustCompile(`(?i)(?:\bafo\b.*\b(?:calf|exercise|raises?)\b|\b(?:calf|exercise|raises?)\b.*\bafo\b)`)

// mobileBriefWithoutExcluded drops every gathered item that names a stated
// exclusion, before any model sees it, so neither route can write it in.
func mobileBriefWithoutExcluded(in mobileBriefInput, terms []string) mobileBriefInput {
	if len(terms) == 0 {
		return in
	}
	keep := func(items []mobileBriefCandidate) []mobileBriefCandidate {
		if items == nil {
			return nil
		}
		out := make([]mobileBriefCandidate, 0, len(items))
		for _, item := range items {
			if !mobileBriefExcluded(terms, item.Title, item.Detail, item.Kind, item.EvidenceLabel, item.NextStep) {
				out = append(out, item)
			}
		}
		return out
	}
	in.Moved, in.Doing, in.Watching, in.Meetings = keep(in.Moved), keep(in.Doing), keep(in.Watching), keep(in.Meetings)
	in.TomorrowMeetings, in.People, in.OpenLoops = keep(in.TomorrowMeetings), keep(in.People), keep(in.OpenLoops)
	in.Learned, in.Deliverables, in.Schedules = keep(in.Learned), keep(in.Deliverables), keep(in.Schedules)
	in.WorkingToward, in.Commitments, in.Noticed, in.WaitingOnYou = keep(in.WorkingToward), keep(in.Commitments), keep(in.Noticed), keep(in.WaitingOnYou)
	in.Conversations = keep(in.Conversations)
	return in
}

// mobileBriefBlankExcluded empties every paragraph that speaks of an
// excluded item, by its cited items or its words, so a brief stored before
// the exclusion leaves it out too. mobileBriefHygiene then drops the
// paragraph with its chips.
func mobileBriefBlankExcluded(brief MobileBriefV1, terms []string) MobileBriefV1 {
	if len(terms) == 0 {
		return brief
	}
	items := mobileBriefItemsByRef(brief)
	excludedRefs := map[string]bool{}
	for ref, rows := range items {
		for _, item := range rows {
			if mobileBriefExcludedValue(terms, item) {
				excludedRefs[ref] = true
			}
		}
	}
	for _, ref := range brief.Evidence {
		if mobileBriefExcluded(terms, ref.Label) {
			excludedRefs[ref.Ref] = true
		}
	}
	excluded := func(refs []string, texts ...string) bool {
		if mobileBriefExcluded(terms, texts...) {
			return true
		}
		for _, ref := range refs {
			if excludedRefs[ref] {
				return true
			}
		}
		return false
	}
	changed := false
	narrative := append([]MobileBriefPara(nil), brief.Narrative...)
	for i, para := range narrative {
		if excluded(para.Refs, para.Text) {
			narrative[i].Text = ""
			changed = true
		}
	}
	brief.Narrative = narrative
	for _, section := range []*[]MobileBriefItem{
		&brief.Sections.Noticed, &brief.Sections.Decide, &brief.Sections.Review, &brief.Sections.Plan,
		&brief.Sections.WaitingOnYou, &brief.Sections.Commitments, &brief.Sections.WorkingToward,
		&brief.Sections.DayAhead, &brief.Sections.Highlights, &brief.Sections.Moved, &brief.Sections.Doing,
		&brief.Sections.Watching, &brief.Sections.Meetings, &brief.Sections.People, &brief.Sections.OpenLoops,
		&brief.Sections.Learned, &brief.Sections.Deliverables, &brief.Sections.Schedules, &brief.Upcoming,
	} {
		var kept []MobileBriefItem
		for _, item := range *section {
			if excluded(item.Refs) || mobileBriefExcludedValue(terms, item) {
				changed = true
				continue
			}
			kept = append(kept, item)
		}
		*section = kept
	}
	var cards []MobileBriefCard
	for _, card := range brief.Cards {
		if excluded(card.Refs, card.Label, card.Message) {
			changed = true
			continue
		}
		cards = append(cards, card)
	}
	brief.Cards = cards
	var offers []MobileBriefOffer
	for _, offer := range brief.Offers {
		if excluded(offer.Refs, offer.Label, offer.Prompt, offer.Text) {
			changed = true
			continue
		}
		offers = append(offers, offer)
	}
	brief.Offers = offers
	var evidence []MobileBriefRef
	for _, ref := range brief.Evidence {
		if excludedRefs[ref.Ref] {
			changed = true
			continue
		}
		evidence = append(evidence, ref)
	}
	brief.Evidence = evidence
	for _, para := range brief.WeeklyRecap {
		if excluded(para.Refs, para.Text) {
			brief.WeeklyRecap = nil
			brief.YourWeek = nil
			changed = true
			break
		}
	}
	if week := brief.YourWeek; week != nil {
		unsafe := mobileBriefExcluded(terms, week.Headline)
		for _, row := range week.Time {
			unsafe = unsafe || mobileBriefExcluded(terms, row.Name)
		}
		for _, row := range week.Done {
			unsafe = unsafe || mobileBriefExcluded(terms, row.Title)
		}
		for _, row := range week.Open {
			unsafe = unsafe || mobileBriefExcluded(terms, row.Title, row.Label)
		}
		for _, row := range week.Learned {
			unsafe = unsafe || mobileBriefExcluded(terms, row.Text)
		}
		if week.Ask != nil {
			unsafe = unsafe || excluded(week.Ask.Refs, week.Ask.Text, week.Ask.Message, week.Ask.Subject)
		}
		if unsafe {
			brief.YourWeek, brief.WeeklyRecap = nil, nil
			changed = true
		}
	}
	if changed {
		brief.Audio = mobileBriefWithoutStaleAudio(brief.Audio)
	}
	return brief
}

func mobileBriefItemsByRef(brief MobileBriefV1) map[string][]MobileBriefItem {
	out := map[string][]MobileBriefItem{}
	for _, section := range mobileBriefSectionLines(brief.Sections) {
		for _, item := range section.items {
			for _, ref := range item.Refs {
				out[ref] = append(out[ref], item)
			}
		}
	}
	for _, item := range brief.Upcoming {
		for _, ref := range item.Refs {
			out[ref] = append(out[ref], item)
		}
	}
	return out
}

// ── Boxes 3 and 4: plain words, proof-only "today", one offer ──

// The classifier's own labels never describe an item (#3).
var mobileBriefSelfLabel = regexp.MustCompile(`(?i)(?:^|[^\pL])(?:optional|suggestions?|suggested|urgent|non-urgent|tasks?)(?:$|[^\pL])|可选|可有可无|紧急|不急|任务|一条建议|这条建议|是建议`)

// A body paragraph never announces an offer; the chip or the closing
// question makes it (#4).
var mobileBriefOfferSentence = regexp.MustCompile(`(?i)^(?:i\s+can|i\s+could|i'll|i\s+will|want\s+me\s+to|shall\s+i|should\s+i|would\s+you\s+like\s+me\s+to)\b|^(?:我可以|我能|我来|要我|需要我|要不要我)`)

var mobileBriefTodayWord = regexp.MustCompile(`(?i)(?:^|[^\pL])today(?:$|[^\pL])|今天|今日`)

// mobileBriefSentences splits prose after ., ?, ! and their Chinese forms,
// keeping each sentence's own punctuation and the space after it.
func mobileBriefSentences(text string) []string {
	var out []string
	runes := []rune(text)
	start := 0
	for i, r := range runes {
		end := strings.ContainsRune("。？！", r) || strings.ContainsRune(".?!", r) && (i+1 == len(runes) || unicode.IsSpace(runes[i+1]))
		if !end {
			continue
		}
		j := i + 1
		for j < len(runes) && unicode.IsSpace(runes[j]) {
			j++
		}
		out = append(out, string(runes[start:j]))
		start = j
	}
	if start < len(runes) {
		out = append(out, string(runes[start:]))
	}
	return out
}

// mobileBriefPlainParagraph removes the sentences a brief may not say: a
// classifier label, an announced offer in a body paragraph, and a "today"
// that no record from today proves.
func mobileBriefPlainParagraph(text string, closing bool, today bool) string {
	var kept []string
	for _, sentence := range mobileBriefSentences(text) {
		trimmed := strings.TrimSpace(sentence)
		if trimmed == "" {
			continue
		}
		if mobileBriefSelfLabel.MatchString(trimmed) || !closing && mobileBriefOfferSentence.MatchString(trimmed) || !today && mobileBriefTodayWord.MatchString(trimmed) {
			continue
		}
		kept = append(kept, sentence)
	}
	return strings.TrimSpace(strings.Join(kept, ""))
}

// mobileBriefParagraphProvesToday reports a paragraph citing a record from
// the brief's own local day: an item that happened, is on or is due that day,
// or a source observed that day.
func mobileBriefParagraphProvesToday(brief MobileBriefV1, para MobileBriefPara, items map[string][]MobileBriefItem, evidence map[string]MobileBriefRef) bool {
	loc := mobileBriefNarrativeLocation(brief)
	day := strings.TrimSpace(brief.LocalDate)
	if day == "" {
		asOf := mobileBriefAsOfTime(brief)
		if asOf.IsZero() {
			return true
		}
		day = asOf.In(loc).Format("2006-01-02")
	}
	onDay := func(value string) bool {
		at := mobileBriefParseTime(value)
		return !at.IsZero() && at.In(loc).Format("2006-01-02") == day
	}
	for _, ref := range para.Refs {
		if onDay(evidence[ref].ObservedAt) {
			return true
		}
		for _, item := range items[ref] {
			if onDay(item.At) || onDay(item.EndAt) || onDay(item.LastTouch) || onDay(item.DueAt) {
				return true
			}
		}
	}
	return false
}

// mobileBriefExclusions is what the person said to keep out of their briefs,
// read from their notes. The scan is cached until memory changes, and every
// memory write drops the cache, so a change takes effect at once.
func (a *App) mobileBriefExclusions() []string {
	if a == nil {
		return nil
	}
	allowRead, err := a.memoryReadFilter()
	if err != nil {
		return nil
	}
	snapshot, err := a.memoryExternalSnapshot()
	if err != nil {
		return nil
	}
	return snapshot.statedExclusions(snapshot.allowed(allowRead))
}
