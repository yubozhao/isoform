package main

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

const mobileBriefPublishMinWords = 100

// Assembly and React's final display guard consume the same patterns. Ordinary
// brackets in source titles, code and email addresses are readable copy.
//
//go:embed frontend/src/lib/brief-copy-patterns.json
var mobileBriefCopyPatternsJSON []byte

type mobileBriefCopyPatternList struct {
	Unfinished, UnfinishedInsensitive []string
	PrivacyMask                       string
}

var mobileBriefCopyPatterns = func() mobileBriefCopyPatternList {
	var patterns mobileBriefCopyPatternList
	if err := json.Unmarshal(mobileBriefCopyPatternsJSON, &patterns); err != nil {
		panic(err)
	}
	return patterns
}()
var mobileBriefPlaceholder = regexp.MustCompile(strings.Join(mobileBriefCopyPatterns.Unfinished, "|"))
var mobileBriefPlaceholderInsensitive = regexp.MustCompile("(?i)(?:" + strings.Join(mobileBriefCopyPatterns.UnfinishedInsensitive, "|") + ")")
var mobileBriefPrivacyMask = regexp.MustCompile(mobileBriefCopyPatterns.PrivacyMask)

func mobileBriefCopySafe(text string) bool {
	text = mobileBriefPrivacyMask.ReplaceAllString(text, "")
	return !mobileBriefPlaceholder.MatchString(text) && !mobileBriefPlaceholderInsensitive.MatchString(text)
}

// A prepared request must name a readable subject. Prose can retain privacy
// markers, but a chip cannot offer to discuss a subject hidden by one.
func mobileBriefCardCopySafe(card MobileBriefCard) bool {
	return mobileBriefCopySafe(card.Label) && mobileBriefCopySafe(card.Message) &&
		!mobileBriefPrivacyMask.MatchString(card.Label) && !mobileBriefPrivacyMask.MatchString(card.Message)
}

func mobileBriefSafeLines(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if mobileBriefCopySafe(line) {
			lines = append(lines, line)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// Apply during assembly and again after rebuilding templates/cards on reads.
// A dropped paragraph takes its prepared questions with it; never retarget one
// to the paragraph that shifted into its old position.
func mobileBriefHygiene(brief MobileBriefV1) MobileBriefV1 {
	original := brief.Narrative
	for _, items := range []*[]MobileBriefItem{
		&brief.Sections.Noticed, &brief.Sections.Decide, &brief.Sections.Review,
		&brief.Sections.Plan, &brief.Sections.WaitingOnYou, &brief.Sections.Commitments,
		&brief.Sections.WorkingToward, &brief.Sections.DayAhead, &brief.Sections.Highlights,
		&brief.Sections.Moved, &brief.Sections.Doing, &brief.Sections.Watching,
		&brief.Sections.Meetings, &brief.Sections.People, &brief.Sections.OpenLoops,
		&brief.Sections.Learned, &brief.Sections.Deliverables, &brief.Sections.Schedules, &brief.Upcoming,
	} {
		if *items == nil {
			continue
		}
		safe := make([]MobileBriefItem, 0, len(*items))
		for _, item := range *items {
			if !mobileBriefCopySafe(item.Title) {
				continue
			}
			item.Detail, item.NextStep = mobileBriefSafeLines(item.Detail), mobileBriefSafeLines(item.NextStep)
			item.AttentionReason = mobileBriefSafeLines(item.AttentionReason)
			if item.Context != nil {
				extra := *item.Context
				extra.Preview = mobileBriefSafeLines(extra.Preview)
				item.Context = &extra
			}
			safe = append(safe, item)
		}
		*items = safe
	}
	brief.Evidence = append([]MobileBriefRef{}, brief.Evidence...)
	for i := range brief.Evidence {
		brief.Evidence[i].Label = mobileBriefSafeLines(brief.Evidence[i].Label)
	}
	brief.Narrative = []MobileBriefPara{}
	positions := map[int]int{}
	byRef, evidenceByRef := mobileBriefItemsByRef(brief), mobileBriefEvidenceByRef(brief)
	closingIndex := -1
	if n := len(original); n > 0 && strings.ContainsAny(original[n-1].Text, "?？") {
		closingIndex = n - 1
	}
	unknownAttendance := map[string]bool{}
	for _, attendance := range brief.MeetingAttendance {
		if attendance.State == "unknown" {
			for _, ref := range attendance.Refs {
				unknownAttendance[ref] = true
			}
		}
	}
	for index, para := range original {
		para.Text = mobileBriefSafeLines(para.Text)
		uncertain := mobileBriefUncertainLead(para.Text)
		for _, ref := range para.Refs {
			uncertain = uncertain || unknownAttendance[ref]
		}
		if len(brief.Narrative) == 0 && uncertain {
			continue
		}
		if brief.NarrativeStatus == "ok" && !mobileBriefStaticEmpty(brief) {
			para.Text = mobileBriefPlainParagraph(para.Text, index == closingIndex, mobileBriefParagraphProvesToday(brief, para, byRef, evidenceByRef))
		}
		if para.Text == "" {
			continue
		}
		if !topicDisplayNameSafe(para.TopicName) || !mobileBriefCopySafe(para.TopicName) {
			para.TopicID, para.TopicName = "", ""
		}
		positions[index] = len(brief.Narrative)
		brief.Narrative = append(brief.Narrative, para)
	}
	brief.WeeklyRecap = append([]MobileBriefPara(nil), brief.WeeklyRecap...)
	for len(brief.WeeklyRecap) > 0 && mobileBriefUncertainLead(brief.WeeklyRecap[0].Text) {
		brief.WeeklyRecap = brief.WeeklyRecap[1:]
		brief.Audio = nil
	}
	for i := range brief.WeeklyRecap {
		brief.WeeklyRecap[i].Text = mobileBriefSafeLines(brief.WeeklyRecap[i].Text)
	}
	brief.YourWeek = yourWeekHygiene(brief.YourWeek)
	var cards []MobileBriefCard
	for _, card := range brief.Cards {
		if !mobileBriefCardCopySafe(card) {
			continue
		}
		if card.Paragraph != nil {
			position, ok := positions[*card.Paragraph]
			if !ok {
				if card.Kind != mobileBriefCardItem {
					continue
				}
				card.Paragraph, card.Label = nil, ""
			} else {
				card.Paragraph = &position
			}
		}
		if card.Kind == mobileBriefCardAction && !mobileBriefAnswerMatches(brief, card) {
			continue
		}
		cards = append(cards, card)
	}
	brief.Cards = cards
	var offers []MobileBriefOffer
	for _, offer := range brief.Offers {
		if mobileBriefCopySafe(offer.Label) && mobileBriefCopySafe(offer.Prompt) && mobileBriefCopySafe(offer.Text) {
			offers = append(offers, offer)
		}
	}
	brief.Offers = offers
	if mobileBriefNarrativeWords(original) != mobileBriefNarrativeWords(brief.Narrative) {
		brief.Audio = nil
	}
	if len(original) > 0 && len(brief.Narrative) == 0 {
		if brief.rankedEdition {
			brief.NarrativeStatus = "unavailable"
		} else {
			brief.NarrativeStatus = "quiet"
		}
	}
	brief = mobileBriefWithMatchingGreeting(brief)
	if brief.NarrativeStatus == "quiet" {
		brief.Narrative, brief.WeeklyRecap, brief.Cards, brief.Offers, brief.Audio = []MobileBriefPara{}, nil, nil, nil, nil
		brief.YourWeek = nil
	}
	return brief
}

// A brief asked for is named by its time: before noon the morning brief,
// from noon the evening brief (today-review v1.1.16 D14). Scheduled slots keep
// their own name.
func mobileBriefDisplaySlot(brief MobileBriefV1, settings mobileBriefSettings) string {
	switch brief.Slot {
	case mobileBriefSlotMorning, mobileBriefSlotEvening:
		return brief.Slot
	}
	if _, err := time.Parse(time.RFC3339Nano, brief.AsOf); err != nil {
		if brief.DisplaySlot == mobileBriefSlotMorning || brief.DisplaySlot == mobileBriefSlotEvening {
			return brief.DisplaySlot
		}
		return mobileBriefSlotMorning
	}
	if mobileBriefAsOfTime(brief).In(mobileBriefNarrativeLocation(brief)).Hour() < 12 {
		return mobileBriefSlotMorning
	}
	return mobileBriefSlotEvening
}

// Quiet scheduled slots are completed local observations, never a posted brief.
// Requests honor concise readings. Retain section rows so the existing More
// from today destinations remain reachable.
func mobileBriefForPublication(brief MobileBriefV1) MobileBriefV1 {
	brief = mobileBriefHygiene(brief)
	scheduled := brief.Slot == mobileBriefSlotMorning || brief.Slot == mobileBriefSlotEvening
	// The word floor is for model text. A template opening is short by design (design
	// R74) and publishes; a brief with no paragraphs has nothing in it, so it is quiet.
	// The new writer's words are short by design on a quiet day; only an empty
	// narrative makes its slot quiet.
	floor := !brief.rankedEdition && brief.writerItems == 0
	if scheduled && (len(brief.Narrative) == 0 && brief.writerItems > 0 || floor && (len(brief.Narrative) == 0 || brief.NarrativeStatus == "ok" && mobileBriefNarrativeWords(brief.Narrative) < mobileBriefPublishMinWords)) {
		brief.NarrativeStatus = "quiet"
		brief = mobileBriefHygiene(brief)
	}
	return brief
}

// Count only readable captures in the exact coverage window, without reading
// their text or images. An unavailable count stays absent instead of saying 0.
func (a *App) mobileBriefQuietScreens(start, end time.Time) *int {
	if a == nil || a.activity == nil || a.activity.store == nil || a.IsClosedEyesModeEnabled() {
		return nil
	}
	var count int
	err := a.activity.store.reader().QueryRow(`SELECT COUNT(*) FROM activity_snapshots snap
        JOIN activity_sessions s ON s.id = snap.session_id
        WHERE snap.timestamp >= ? AND snap.timestamp < ?`+sqlExcludeProtected, start.UnixMilli(), end.UnixMilli()).Scan(&count)
	if err != nil {
		return nil
	}
	return &count
}

// Old briefs can contain the retired uncertainty opener. Withhold that whole
// paragraph rather than detach its claims or cards from their evidence.
func mobileBriefUncertainLead(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{
		"isoform couldn't tell", "isoform could not tell", "isoform can't tell", "isoform cannot tell",
		"i couldn't tell", "i could not tell", "i can't tell", "i cannot tell", "couldn't tell", "could not tell",
		"isoform 没法确定", "isoform 无法确定", "我无法确定", "我没法确定",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
