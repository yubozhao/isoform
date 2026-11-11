package main

import (
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
	if err := briefcore.CheckNarrativeParts(larger, briefParagraphs(done), briefParagraphs(next)); err != nil {
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

// rankedPartsBalance is the larger part for a scheduled ranked brief: next for
// the morning, done for the evening (briefcore.BalanceFor).
func rankedPartsBalance(slot string) string {
	if slot == mobileBriefSlotEvening {
		return briefPartDone
	}
	return briefPartNext
}

// rankedPartsPromptRule is the writer instruction for the two parts of a
// ranked brief. A morning looks forward; an evening reflects back.
func rankedPartsPromptRule(slot string) string {
	if slot == mobileBriefSlotEvening {
		return "Label each paragraph with a part. Part \"done\": two or three paragraphs reflecting on the day: what got done, what moved, what slipped, and a pattern if one is visible; from observation only; name no clock times. This part is the larger one. Part \"next\": one or two short items for tomorrow. Put the closing question last, as a next paragraph."
	}
	return "Label each paragraph with a part. Part \"done\": one short paragraph covering yesterday and since, from observation only; name no clock times. Part \"next\": two to four paragraphs saying what needs to happen today, each with a short reason tied to its items; this part is the larger one. Put the closing question last, as a next paragraph."
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
