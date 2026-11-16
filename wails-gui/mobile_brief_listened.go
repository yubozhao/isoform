package main

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Past briefs say where the person is with each brief (computer-brief v1.0.0
// rule 9, phone-brief rules 10 and 11): whether it was played, and how many of
// its items are left to answer. Playing is learned from the one brief player,
// which reports its position about once a second (SetMenuBarBriefPlayback);
// only the furthest point reached is kept, per brief, on this computer.

const (
	briefListenedKey     = "brief.listened"
	briefListenedEvent   = "brief-listened:changed"
	briefListenedKeepMax = 400

	briefListenedNotPlayed = "not_played"
	briefListenedPartHeard = "part_heard"
	briefListenedPlayed    = "played"

	// briefPartHeardSeconds is how much must be heard before a brief is part
	// heard; briefPlayedTailSeconds is how close to the end counts as played.
	briefPartHeardSeconds  = 5.0
	briefPlayedTailSeconds = 3.0
	briefPlayedShare       = 0.95
)

// briefListenedRecord is the furthest point heard in one brief.
type briefListenedRecord struct {
	State        string  `json:"state"`
	HeardSeconds float64 `json:"heardSeconds"`
	At           string  `json:"at"`
}

var briefListenedMu sync.Mutex

// briefListenedState returns the state a position reaches.
func briefListenedState(position, duration float64) string {
	switch {
	case duration > 0 && (position >= duration-briefPlayedTailSeconds || position >= duration*briefPlayedShare):
		return briefListenedPlayed
	case position >= briefPartHeardSeconds:
		return briefListenedPartHeard
	default:
		return briefListenedNotPlayed
	}
}

func briefListenedRank(state string) int {
	switch state {
	case briefListenedPlayed:
		return 2
	case briefListenedPartHeard:
		return 1
	default:
		return 0
	}
}

// briefListenedRecords reads every brief's record.
func (a *App) briefListenedRecords() map[string]briefListenedRecord {
	out := map[string]briefListenedRecord{}
	if a == nil || a.store == nil {
		return out
	}
	raw, err := a.store.GetUIState(context.Background(), briefListenedKey)
	if err != nil || raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// noteBriefHeard keeps how far a brief was heard. It writes only when the
// brief moves to a further state, so the once-a-second reports stay cheap,
// and tells Past briefs when it does.
func (a *App) noteBriefHeard(briefID string, position, duration float64, now time.Time) bool {
	if a == nil || a.store == nil || briefID == "" || !mobileValidBriefID(briefID) {
		return false
	}
	state := briefListenedState(position, duration)
	if state == briefListenedNotPlayed {
		return false
	}
	briefListenedMu.Lock()
	records := a.briefListenedRecords()
	if briefListenedRank(records[briefID].State) >= briefListenedRank(state) {
		briefListenedMu.Unlock()
		return false
	}
	records[briefID] = briefListenedRecord{State: state, HeardSeconds: position, At: now.UTC().Format(time.RFC3339)}
	for len(records) > briefListenedKeepMax {
		oldest := ""
		for id, record := range records {
			if oldest == "" || record.At < records[oldest].At {
				oldest = id
			}
		}
		delete(records, oldest)
	}
	raw, err := json.Marshal(records)
	if err == nil {
		err = a.store.SetUIState(context.Background(), briefListenedKey, string(raw))
	}
	briefListenedMu.Unlock()
	if err != nil {
		return false
	}
	if a.ctx != nil {
		a.emitBrowserEvent(briefListenedEvent, briefID)
	}
	return true
}

// withPastBriefState adds to Past briefs' rows whether each brief was played
// and how many of its items are left to answer. It reads the stored briefs
// and the answers ledger only, so the page opens at once.
func (a *App) withPastBriefState(rows []MobileBriefSummary) []MobileBriefSummary {
	if a == nil || len(rows) == 0 {
		return rows
	}
	listened := a.briefListenedRecords()
	answers := a.answersLedger().current()
	store := mobileBriefStoreOf(a.mobileBriefSource())
	language := a.currentUILanguageName()
	out := make([]MobileBriefSummary, len(rows))
	for i, row := range rows {
		row.Listened = briefListenedNotPlayed
		if record, ok := listened[row.BriefID]; ok && record.State != "" {
			row.Listened = record.State
		}
		row.Played = row.Listened == briefListenedPlayed
		if store != nil {
			if brief, ok := store.load(row.BriefID); ok {
				row.ToAnswer, row.Answered = pastBriefAnswerCounts(brief, language, answers)
			}
		}
		out[i] = row
	}
	return out
}

// mobileBriefStoreOf is the brief service's store, when the source is it.
func mobileBriefStoreOf(source mobileBriefSource) *mobileBriefStore {
	if svc, ok := source.(*mobileBriefService); ok && svc != nil {
		return svc.store
	}
	return nil
}

// pastBriefAnswerCounts counts a brief's items, as Your work names them
// (workBriefOfferID), left to answer and answered.
func pastBriefAnswerCounts(brief MobileBriefV1, language string, answers map[string]AnswerRecord) (int, int) {
	offers := brief.Offers
	if len(offers) == 0 && brief.NarrativeStatus == "ok" {
		_, offers = mobileBriefClosing(brief, language)
	}
	toAnswer, answered := 0, 0
	for _, offer := range offers {
		if offer.ID == "" || offer.Label == "" {
			continue
		}
		if _, ok := answers[workBriefOfferID(brief.BriefID, offer.ID)]; ok {
			answered++
		} else {
			toAnswer++
		}
	}
	return toAnswer, answered
}
