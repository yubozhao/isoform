package topicday

import "time"

// Proposal quality (Bo Nov 14: the judgments are the best data). A topic's
// proposal is skipped for a day's build when the person's own answers say
// the work would be turned down again: the work was already done, it isn't
// the time, they turned down its proposals twice in a row, or the topic has
// gone cold. The topic still shows; only its proposal rests. The reasons are
// the ones a No carries in Your work (phone-map v1.0.0 rule 7).

// VerdictUndo takes back the answer before it on the same proposal.
const VerdictUndo = "undo"

// The reasons a No may carry.
const (
	ReasonAlreadyDone = "already_done"
	ReasonNotNow      = "not_now"
	ReasonNotUseful   = "not_useful"
	ReasonMyself      = "myself"
)

// Why a topic's proposal rests.
const (
	RestAlreadyDone = "already_done" // done already, and nothing new on the topic since
	RestNotNow      = "not_now"      // "not now", a few days ago
	RestDeclined    = "declined"     // turned down twice in a row lately
	RestStale       = "stale"        // not seen for two weeks
)

const (
	// StaleAfter is how long a topic goes unseen before its work is stale.
	StaleAfter = 14 * 24 * time.Hour
	// NotNowFor is how long "not now" holds a topic's proposals back.
	NotNowFor = 3 * 24 * time.Hour
	// DeclinedFor is how long two Nos in a row hold a topic back, and the
	// longest "already done" holds a topic whose last activity is unknown.
	DeclinedFor = 7 * 24 * time.Hour
	// DeclinedRun is how many Nos in a row rest a topic.
	DeclinedRun = 2
)

// Standing is the feedback still standing, oldest first: an undo removes
// the answer before it on its proposal.
func Standing(feedback []Feedback) []Feedback {
	out := make([]Feedback, 0, len(feedback))
	for _, f := range feedback {
		if f.Verdict != VerdictUndo {
			out = append(out, f)
			continue
		}
		for i := len(out) - 1; i >= 0; i-- {
			if out[i].TopicID == f.TopicID && out[i].ProposalID == f.ProposalID {
				out = append(out[:i], out[i+1:]...)
				break
			}
		}
	}
	return out
}

// Rest says why a topic gets no proposal at now, from its standing feedback,
// or "" when it gets one.
func Rest(t Topic, standing []Feedback, now time.Time) string {
	seen, _ := time.Parse(time.RFC3339, t.LastSeenAt)
	var mine []Feedback
	for _, f := range standing {
		if f.TopicID == t.ID {
			mine = append(mine, f)
		}
	}
	if n := len(mine); n > 0 && mine[n-1].Verdict == VerdictNo {
		last := mine[n-1]
		at := time.UnixMilli(last.At)
		switch last.Reason {
		case ReasonAlreadyDone:
			moved := !seen.IsZero() && seen.After(at)
			if !moved && (!seen.IsZero() || now.Sub(at) < DeclinedFor) {
				return RestAlreadyDone
			}
		case ReasonNotNow:
			if now.Sub(at) < NotNowFor {
				return RestNotNow
			}
		}
		run := 0
		for i := n - 1; i >= 0 && mine[i].Verdict == VerdictNo; i-- {
			switch mine[i].Reason {
			case "", ReasonNotUseful, ReasonMyself:
				run++
			}
		}
		if run >= DeclinedRun && now.Sub(at) < DeclinedFor {
			return RestDeclined
		}
	}
	if !seen.IsZero() && now.Sub(seen) > StaleAfter {
		return RestStale
	}
	return ""
}
