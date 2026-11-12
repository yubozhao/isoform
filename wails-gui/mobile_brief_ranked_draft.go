package main

import (
	"encoding/json"
	"strings"

	briefcore "wails-gui/internal/brief"
)

// A writer may refer only to the capped selection. The client receives the
// existing evidence refs, never the scorer's private source identities.
type morningBriefDraftParagraph struct {
	Text  string                `json:"text"`
	Items []string              `json:"items"`
	Chip  *mobileBriefCardWords `json:"chip,omitempty"`
	// Part is done or next (mobile_brief_forward.go); absent in older replies.
	Part string `json:"part,omitempty"`
}

type morningBriefDraftReply struct {
	Paragraphs []morningBriefDraftParagraph `json:"paragraphs"`
	Answers    []mobileBriefCardWords       `json:"answers,omitempty"`
}

const morningBriefTooFewNextReason = "Include at least two distinct next items, each with a reason, and a short done paragraph when an observation is available."

var errMorningBriefTooFewNext = codeError("morning draft needs two distinct next items")

// parseMorningBriefDraft reads a scheduled morning draft (next is the larger part).
func parseMorningBriefDraft(raw string, selection morningBriefSelection) (morningBriefDraft, error) {
	return parseRankedBriefDraft(raw, selection, mobileBriefSlotMorning)
}

// parseRankedBriefDraft reads a ranked draft for slot: the scheduled morning or
// the scheduled evening. The slot sets which part must be the larger one.
func parseRankedBriefDraft(raw string, selection morningBriefSelection, slot string) (morningBriefDraft, error) {
	decoder := json.NewDecoder(strings.NewReader(stripMobileBriefCodeFence(raw)))
	decoder.DisallowUnknownFields()
	var reply morningBriefDraftReply
	if err := decoder.Decode(&reply); err != nil {
		return morningBriefDraft{}, err
	}
	if selection.Lead == nil || len(reply.Paragraphs) == 0 || len(reply.Paragraphs) > mobileBriefMaxWords {
		return morningBriefDraft{}, codeError("invalid draft structure")
	}
	items := map[string]morningBriefCandidate{selection.Lead.ID: *selection.Lead}
	for _, item := range selection.Needs {
		items[item.ID] = item
	}
	if selection.Slip != nil {
		items[selection.Slip.ID] = *selection.Slip
	}
	if selection.Handled != nil {
		items[selection.Handled.ID] = *selection.Handled
	}
	// Only source-backed work and meetings count toward the minimum. Generic
	// observations are context, not evidence of a task.
	nextEligible, doneEligible := 0, false
	for _, item := range items {
		switch item.BriefRole {
		case "next":
			nextEligible++
		case "done":
			doneEligible = true
		}
	}
	nextCited := map[string]bool{}
	nextParagraphs := 0
	doneCited := false
	var draft morningBriefDraft
	labelled := false
	for _, paragraph := range reply.Paragraphs {
		labelled = labelled || paragraph.Part != ""
	}
	leadCited := false
	for index, paragraph := range reply.Paragraphs {
		text := strings.TrimSpace(paragraph.Text)
		if text == "" || len(text) > mobileBriefNarrativeMaxBytes || !mobileBriefCopySafe(text) || redactThreadOutcomeText(text) != text || len(paragraph.Items) == 0 {
			return morningBriefDraft{}, codeError("invalid draft paragraph")
		}
		var refs []string
		paragraphHasNext := false
		lead := false
		seen := map[string]bool{}
		for _, id := range paragraph.Items {
			item, ok := items[id]
			if !ok || id == "" || seen[id] || len(item.Refs) == 0 {
				return morningBriefDraft{}, codeError("draft cites an unavailable item")
			}
			seen[id] = true
			if slot == mobileBriefSlotMorning && paragraph.Part != briefPartDone && morningBriefObservedSourceKind(item.SourceKind) && item.BriefRole != "next" {
				return morningBriefDraft{}, codeError("observation is not a next item")
			}
			if paragraph.Part == briefPartNext && item.BriefRole == "next" && !strings.ContainsAny(text, "?？") {
				nextCited[id] = true
				paragraphHasNext = true
			}
			if paragraph.Part == briefPartDone && item.BriefRole == "done" {
				doneCited = true
			}
			lead = lead || id == selection.Lead.ID
			for _, ref := range item.Refs {
				if ref == "" {
					return morningBriefDraft{}, codeError("draft cites an unavailable item")
				}
				if !containsString(refs, ref) {
					refs = append(refs, ref)
				}
			}
		}
		if paragraphHasNext && !strings.ContainsAny(text, "?？") {
			nextParagraphs++
		}
		leadCited = leadCited || lead
		// A labelled draft may put done first, so the lead is checked over the
		// whole draft below; an unlabelled draft keeps the lead-first rule.
		if index == 0 && !lead && !labelled {
			return morningBriefDraft{}, codeError("draft does not lead with the selected item")
		}
		draft.Paragraphs = append(draft.Paragraphs, MobileBriefPara{Text: text, Refs: refs, chipWords: paragraph.Chip, Part: paragraph.Part})
	}
	if slot == mobileBriefSlotMorning && nextEligible >= 2 && nextEligible+morningBriefBoolCount(doneEligible) >= 3 && (len(nextCited) < 2 || nextParagraphs < 2 || doneEligible && !doneCited) {
		return morningBriefDraft{}, errMorningBriefTooFewNext
	}
	if labelled && !leadCited {
		return morningBriefDraft{}, codeError("draft does not cite the selected item")
	}
	if mobileBriefNarrativeWords(draft.Paragraphs) > mobileBriefMaxWords {
		return morningBriefDraft{}, codeError("draft exceeds word ceiling")
	}
	var ordered []MobileBriefPara
	var err error
	if slot == mobileBriefSlotEvening && eveningIsThin(selectionEvidenceRefs(selection)) {
		// A thin evening day may have one short done paragraph, so no part must
		// be larger by words.
		ordered, err = orderParts(draft.Paragraphs, briefcore.CheckThinNarrativeParts)
	} else {
		ordered, err = orderRankedParts(draft.Paragraphs, rankedPartsBalance(slot))
	}
	if err != nil {
		return morningBriefDraft{}, err
	}
	draft.Paragraphs = ordered
	if len(reply.Answers) > 2 {
		return morningBriefDraft{}, codeError("too many closing answers")
	}
	draft.Answers = reply.Answers
	return draft, nil
}

func morningBriefBoolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
