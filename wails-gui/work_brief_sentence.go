package main

import (
	"strconv"
	"strings"
	"time"
)

// Not right on a brief sentence (computer-brief v1.0.0 rules 7 and 8,
// phone-brief Not right; PROTOCOL.md 26.1): a No on brief:<briefId>:p<index>,
// with a reason or the words the person gave. It is kept in Your answers,
// the next briefs leave out what that sentence rested on, and what was made
// from it shows "From a sentence you said wasn't right" in Your work.

var workReasonsSentence = []string{"didnt_happen", "wrong_person", "already_done", "not_mine"}

const (
	// workSentenceNotRightFor is how long a Not right keeps a sentence's
	// sources out of new briefs.
	workSentenceNotRightFor = 7 * 24 * time.Hour
	// workFromNotRightKey is the first part's key on an item made from a
	// sentence the person said wasn't right.
	workFromNotRightKey  = "from_not_right"
	workFromNotRightText = "From a sentence you said wasn't right"
)

// workBriefSentenceID is the id of a brief's paragraph as an answerable item.
func workBriefSentenceID(briefID string, index int) string {
	return "brief:" + briefID + ":p" + strconv.Itoa(index)
}

// workParseBriefSentenceID reads brief:<briefId>:p<index>.
func workParseBriefSentenceID(id string) (string, int, bool) {
	rest, ok := strings.CutPrefix(id, "brief:")
	if !ok {
		return "", 0, false
	}
	cut := strings.LastIndex(rest, ":p")
	if cut <= 0 {
		return "", 0, false
	}
	index, err := strconv.Atoi(rest[cut+2:])
	if err != nil || index < 0 || strconv.Itoa(index) != rest[cut+2:] {
		return "", 0, false
	}
	return rest[:cut], index, true
}

// workBriefSentence is a kept brief's paragraph and the brief it is in.
func (a *App) workBriefSentence(briefID string, index int) (MobileBriefV1, MobileBriefPara, bool) {
	svc := a.mobileBriefSource()
	if svc == nil || briefID == "" {
		return MobileBriefV1{}, MobileBriefPara{}, false
	}
	brief, ok := svc.GetMobileBrief(briefID)
	if !ok || index >= len(brief.Narrative) || strings.TrimSpace(brief.Narrative[index].Text) == "" {
		return MobileBriefV1{}, MobileBriefPara{}, false
	}
	return brief, brief.Narrative[index], true
}

// workBriefSentenceItem is a sentence as an item to answer: it is never in
// the list, but answers like one, with its own reasons.
func (a *App) workBriefSentenceItem(id string) (WorkItem, bool) {
	briefID, index, ok := workParseBriefSentenceID(id)
	if !ok {
		return WorkItem{}, false
	}
	brief, para, ok := a.workBriefSentence(briefID, index)
	if !ok {
		return WorkItem{}, false
	}
	text := strings.TrimSpace(para.Text)
	paragraph := index
	item := WorkItem{
		ID: id, Kind: workKindClaim, Row: "check", State: workStateOpen, Source: workSourceBrief,
		Ref:     WorkRef{BriefID: brief.BriefID, Paragraph: &paragraph},
		First:   []WorkTextPart{{Text: "from the brief", Ref: "brief:" + brief.BriefID, Key: "from_brief"}},
		Noticed: text, Work: WorkBody{Title: answersBound(text)}, Yes: WorkYes{Does: "right"},
		Reasons: workReasonsSentence, CreatedAt: brief.AsOf,
	}
	return a.applyWorkAnswers([]WorkItem{item})[0], true
}

// workIsBriefSentence reports whether an item is a brief's sentence.
func workIsBriefSentence(item WorkItem) bool {
	_, _, ok := workParseBriefSentenceID(item.ID)
	return ok && item.Kind == workKindClaim
}

// workNotRightSentences are the sentences the person said weren't right, by
// brief and paragraph, that still stand (not undone).
func (a *App) workNotRightSentences(since time.Time) map[string]map[int]bool {
	out := map[string]map[int]bool{}
	for id, record := range a.answersLedger().current() {
		if record.Action != workActionNo {
			continue
		}
		briefID, index, ok := workParseBriefSentenceID(id)
		if !ok {
			continue
		}
		if at, err := time.Parse(time.RFC3339, record.At); err != nil || at.Before(since) {
			continue
		}
		if out[briefID] == nil {
			out[briefID] = map[int]bool{}
		}
		out[briefID][index] = true
	}
	return out
}

// briefNotRightSources are the source records that sentences the person said
// weren't right rested on, in the last week: new briefs leave them out, so
// tomorrow's brief doesn't say it again.
func (a *App) briefNotRightSources(now time.Time) map[string]bool {
	out := map[string]bool{}
	for briefID, indexes := range a.workNotRightSentences(now.Add(-workSentenceNotRightFor)) {
		svc := a.mobileBriefSource()
		if svc == nil {
			return out
		}
		brief, ok := svc.GetMobileBrief(briefID)
		if !ok {
			continue
		}
		evidence := map[string]MobileBriefRef{}
		for _, ref := range brief.Evidence {
			evidence[ref.Ref] = ref
		}
		for index := range indexes {
			if index >= len(brief.Narrative) {
				continue
			}
			for _, ref := range brief.Narrative[index].Refs {
				if source, ok := evidence[ref]; ok && source.SourceID != "" {
					out[mobileBriefSourceKey(source.SourceKind, source.SourceID)] = true
				}
			}
		}
	}
	return out
}

// workMarkNotRight adds "From a sentence you said wasn't right" to the
// brief's items that sit under such a sentence.
func (a *App) workMarkNotRight(items []WorkItem) {
	marked := a.workNotRightSentences(time.Time{})
	if len(marked) == 0 {
		return
	}
	for i := range items {
		item := &items[i]
		if item.Ref.BriefID == "" || item.Ref.Paragraph == nil || !marked[item.Ref.BriefID][*item.Ref.Paragraph] {
			continue
		}
		item.First = append(item.First, WorkTextPart{Text: workFromNotRightText, Key: workFromNotRightKey})
	}
}
