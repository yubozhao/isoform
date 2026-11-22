package main

import "time"

// How each confirmed claim was confirmed, for About you's Confirmed rows
// (computer-activity v1.1.0 rule 5; redesign-you v1.0.0 rule 5): "You
// confirmed" (Yes), "You edited" (Edit, said it my way) or "You said" (the
// person's own note: Chat's Remember, a new note).
const (
	confirmedHowConfirmed = "confirmed"
	confirmedHowEdited    = "edited"
	confirmedHowSaid      = "said"
)

// beliefAnswers reads the ledger's standing belief answers once per build of
// About you, keyed by memory id.
func (a *App) beliefAnswers() map[string]AnswerRecord {
	out := map[string]AnswerRecord{}
	for item, record := range a.answersLedger().current() {
		if record.Kind != answerKindBelief {
			continue
		}
		if id, ok := cutBeliefItem(item); ok {
			out[id] = record
		}
	}
	return out
}

func cutBeliefItem(item string) (string, bool) {
	const prefix = "belief:"
	if len(item) <= len(prefix) || item[:len(prefix)] != prefix {
		return "", false
	}
	return item[len(prefix):], true
}

// confirmedHow says how a confirmed belief was confirmed, and when (unix ms,
// 0 when unknown). An unconfirmed belief has no how.
func confirmedHow(belief AboutYouBelief, answer AnswerRecord, answered bool) (string, int64) {
	if belief.Status != "confirmed" {
		return "", 0
	}
	at := int64(0)
	if answered {
		if parsed, err := time.Parse(time.RFC3339, answer.At); err == nil {
			at = parsed.UnixMilli()
		}
		switch answer.Action {
		case workActionEdit:
			return confirmedHowEdited, at
		case workActionYes:
			if belief.ConfirmedAt != 0 {
				at = belief.ConfirmedAt
			}
			return confirmedHowConfirmed, at
		}
	}
	if belief.ConfirmedAt != 0 {
		return confirmedHowConfirmed, belief.ConfirmedAt
	}
	// A user_edit with no answer in the ledger is the person's own words.
	return confirmedHowSaid, belief.UpdatedAt
}
