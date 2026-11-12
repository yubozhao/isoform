package main

import (
	"time"

	briefcore "wails-gui/internal/brief"
)

// The forward-looking narrative has two parts: done (what got done, from
// observation) and next (what needs to happen next). The live ranked writer
// labels each paragraph with its part. The labels only order and count the
// paragraphs the writer already wrote and bound; they add no new text and no
// new source, so the model binding and the delivery gate see the same words.
const (
	briefPartDone = briefcore.PartDone
	briefPartNext = briefcore.PartNext
)

// MobileBriefNarrativePart is one part's paragraph count. Narrative is the two
// parts joined in order, so the first Count paragraphs are the first part.
type MobileBriefNarrativePart struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// orderRankedParts checks the part labels the writer gave and puts the done
// paragraphs before the next paragraphs. larger is the part the slot makes
// larger (briefcore.BalanceFor): next for a morning brief, done for an evening
// one. With no labels at all it returns the paragraphs unchanged, so a writer
// reply without parts keeps the old shape. Mixed or unknown labels, and a draft
// the deterministic check refuses, are a draft error: the pipeline then retries
// or falls back to the fixed order.
func orderRankedParts(paragraphs []MobileBriefPara, larger string) ([]MobileBriefPara, error) {
	return orderParts(paragraphs, func(done, next []briefcore.Paragraph) error {
		return briefcore.CheckNarrativeParts(larger, done, next)
	})
}

// orderParts is orderRankedParts with the part check given by the caller, so a
// thin evening day can use briefcore.CheckThinNarrativeParts.
func orderParts(paragraphs []MobileBriefPara, check func(done, next []briefcore.Paragraph) error) ([]MobileBriefPara, error) {
	labelled := 0
	for _, p := range paragraphs {
		switch p.Part {
		case "":
		case briefPartDone, briefPartNext:
			labelled++
		default:
			return nil, codeError("draft part is not done or next")
		}
	}
	if labelled == 0 {
		return paragraphs, nil
	}
	if labelled != len(paragraphs) {
		return nil, codeError("draft labels only some paragraphs with a part")
	}
	var done, next []MobileBriefPara
	for _, p := range paragraphs {
		if p.Part == briefPartDone {
			done = append(done, p)
		} else {
			next = append(next, p)
		}
	}
	if err := check(briefParagraphs(done), briefParagraphs(next)); err != nil {
		return nil, codeError("draft parts failed the check")
	}
	return append(done, next...), nil
}

// mobileBriefNarrativePartsOf counts the parts of the final narrative. It
// returns nil when no paragraph carries a part, or when the labels are not
// done-then-next, so the wire then omits narrativeParts.
func mobileBriefNarrativePartsOf(paragraphs []MobileBriefPara) []MobileBriefNarrativePart {
	var parts []MobileBriefNarrativePart
	labelled := 0
	for _, p := range paragraphs {
		switch p.Part {
		case briefPartDone, briefPartNext:
			labelled++
		default:
			return nil
		}
		if n := len(parts); n == 0 || parts[n-1].Kind != p.Part {
			if p.Part == briefPartDone && n > 0 {
				return nil
			}
			parts = append(parts, MobileBriefNarrativePart{Kind: p.Part})
		}
		parts[len(parts)-1].Count++
	}
	if labelled == 0 {
		return nil
	}
	return parts
}

// mobileBriefWithNarrativeParts sets the part counts once the narrative is
// final: after trimming and greeting joins, before the brief is stored.
func mobileBriefWithNarrativeParts(brief MobileBriefV1) MobileBriefV1 {
	brief.NarrativeParts = mobileBriefNarrativePartsOf(brief.Narrative)
	return brief
}

func briefParagraphs(in []MobileBriefPara) []briefcore.Paragraph {
	out := make([]briefcore.Paragraph, 0, len(in))
	for _, p := range in {
		out = append(out, briefcore.Paragraph{Text: p.Text, Refs: p.Refs})
	}
	return out
}

// rankedBalanceSlot is the slot whose rules a ranked brief follows. A scheduled
// brief keeps its own slot. An on-request brief takes the balance of the local
// time it is made: the morning before noon, the evening from noon. The brief
// itself keeps its on_request slot.
func rankedBalanceSlot(slot string, localNow time.Time) string {
	if slot != mobileBriefSlotOnRequest {
		return slot
	}
	if localNow.Hour() < 12 {
		return mobileBriefSlotMorning
	}
	return mobileBriefSlotEvening
}

// rankedPartsBalance is the larger part for a ranked brief in balance slot: next
// for the morning, done for the evening (briefcore.BalanceFor). Callers pass the
// result of rankedBalanceSlot.
func rankedPartsBalance(slot string) string {
	if slot == mobileBriefSlotEvening {
		return briefPartDone
	}
	return briefPartNext
}

// Evening length. A reflective done part is at least two paragraphs and seventy
// words, unless the day has too little evidence to fill it: then one short done
// paragraph is allowed, so a quiet day is not padded.
const (
	eveningMinDoneParagraphs = 2
	eveningMinDoneWords      = 70
	eveningThinEvidenceRefs  = 3
)

// eveningDoneStats counts the done paragraphs and their words.
func eveningDoneStats(paragraphs []MobileBriefPara) (count, words int) {
	for _, p := range paragraphs {
		if p.Part == briefPartDone {
			count++
			words += mobileBriefWordCount(p.Text)
		}
	}
	return count, words
}

// eveningIsThin reports whether the day has too few distinct evidence refs to
// fill a fuller reflection.
func eveningIsThin(evidenceRefs int) bool {
	return evidenceRefs < eveningThinEvidenceRefs
}

// eveningDoneLengthReason says why an evening draft's done part is too short, or
// returns "" when it is long enough (or the day is thin).
func eveningDoneLengthReason(paragraphs []MobileBriefPara, evidenceRefs int) string {
	if eveningIsThin(evidenceRefs) {
		return ""
	}
	count, words := eveningDoneStats(paragraphs)
	if count >= eveningMinDoneParagraphs && words >= eveningMinDoneWords {
		return ""
	}
	return "The done part needs two or three paragraphs and at least seventy words about the day."
}

// selectionEvidenceRefs counts the distinct evidence refs of the selected items.
func selectionEvidenceRefs(selection morningBriefSelection) int {
	seen := map[string]bool{}
	add := func(item *morningBriefCandidate) {
		if item == nil {
			return
		}
		for _, ref := range item.Refs {
			seen[ref] = true
		}
	}
	add(selection.Lead)
	for i := range selection.Needs {
		add(&selection.Needs[i])
	}
	add(selection.Slip)
	add(selection.Handled)
	return len(seen)
}

// writeRankedWithLengthRetry runs one writer attempt. For an evening draft whose
// done part is too short, it makes exactly one more attempt, with the reason
// passed in. The better draft is kept: a passing retry, or else the one with
// more done words. A failed first attempt returns its error, so the pipeline's
// own retry handles it; a failed retry keeps the first draft, never an empty one.
func writeRankedWithLengthRetry(balance string, evidenceRefs int, attempt func(reason string) (morningBriefDraft, error)) (morningBriefDraft, error) {
	draft, err := attempt("")
	if err != nil || balance != briefPartDone {
		return draft, err
	}
	reason := eveningDoneLengthReason(draft.Paragraphs, evidenceRefs)
	if reason == "" {
		return draft, nil
	}
	retry, retryErr := attempt(reason)
	if retryErr != nil || len(retry.Paragraphs) == 0 {
		return draft, nil
	}
	if eveningDoneLengthReason(retry.Paragraphs, evidenceRefs) == "" {
		return retry, nil
	}
	_, firstWords := eveningDoneStats(draft.Paragraphs)
	if _, retryWords := eveningDoneStats(retry.Paragraphs); retryWords > firstWords {
		return retry, nil
	}
	return draft, nil
}

// rankedPartsPromptRule is the writer instruction for the two parts of a
// ranked brief in balance slot. A morning looks forward; an evening reflects back.
func rankedPartsPromptRule(slot string) string {
	if slot == mobileBriefSlotEvening {
		return "Label each paragraph with a part. Part \"done\": two or three paragraphs, each two to four sentences, reflecting on the day: what got done, what moved forward, what slipped or stayed open, and a pattern when one is visible. Each sentence rests on the items it cites; from observation only; name no clock times. Together the done paragraphs run at least seventy words. If the day has little to observe, one short done paragraph is enough. This part is the larger one. Part \"next\": one or two short items for tomorrow. Put the closing question last, as a next paragraph."
	}
	return "Label each paragraph with a part. Part \"done\": one short paragraph covering yesterday and since, from observation only; name no clock times. Part \"next\": two to four paragraphs saying what needs to happen today, each with a short reason tied to its items; this part is the larger one. Use items with briefRole next for the next part, and items with briefRole done for yesterday. A meeting is not evidence of preparation or an obligation. Observation, automated activity and assistant work are never the user's next step. Put the closing question last, as a next paragraph."
}

// Empty and quiet lines name the slot they are for. Each is a fixed sentence,
// so mobileBriefStaticEmpty can recognise it and skip the reference check.
const (
	eveningEmptyEN = "The available items did not surface a decision for this evening."
	eveningEmptyZH = "可用的事项中，今晚没有发现需要决定的事。"
	eveningQuietEN = "The checked items did not surface a decision for this evening."
	eveningQuietZH = "已检查的事项中，今晚没有需要决定的事。"
)

// rankedQuietLine is the short statement for a quiet ranked brief in slot.
func rankedQuietLine(slot, lang string) string {
	if slot == mobileBriefSlotEvening {
		return pick(lang, eveningQuietEN, eveningQuietZH)
	}
	return pick(lang, "The checked items did not surface a decision for this morning.", "已检查的事项中，今早没有需要决定的事。")
}

// rankedEmptyLine is the fixed line for an empty ranked brief in slot.
func rankedEmptyLine(slot, lang string) string {
	if slot == mobileBriefSlotEvening {
		return pick(lang, eveningEmptyEN, eveningEmptyZH)
	}
	return pick(lang, morningBriefEmptyEN, morningBriefEmptyZH)
}

// Retry an underfilled morning once with the deterministic reason.
func writeRankedMorningWithRetry(critique string, write func(string) (morningBriefDraft, error)) (morningBriefDraft, error) {
	draft, err := write(critique)
	if err != errMorningBriefTooFewNext {
		return draft, err
	}
	if critique != "" {
		critique += " "
	}
	return write(critique + morningBriefTooFewNextReason)
}
