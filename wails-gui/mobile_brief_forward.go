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

// Empty and quiet lines name the slot they are for. Each is a fixed sentence,
// so mobileBriefStaticEmpty can recognise it and skip the reference check.
const (
	eveningEmptyEN = "The available items did not surface a decision for this evening."
	eveningEmptyZH = "可用的事项中，今晚没有发现需要决定的事。"
	eveningQuietEN = "The checked items did not surface a decision for this evening."
	eveningQuietZH = "已检查的事项中，今晚没有需要决定的事。"
)
