package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// countingBriefSource lists stored briefs and counts each sealed read.
type countingBriefSource struct {
	mobileBriefSource
	rows  []MobileBriefSummary
	reads map[string]int
}

func (s *countingBriefSource) ListMobileBriefs(int) []MobileBriefSummary { return s.rows }
func (s *countingBriefSource) Schedule() (MobileBriefSchedule, error) {
	return MobileBriefSchedule{}, nil
}
func (s *countingBriefSource) GetMobileBrief(id string) (MobileBriefV1, bool) {
	s.reads[id]++
	for _, row := range s.rows {
		if row.BriefID == id {
			return MobileBriefV1{Version: 1, BriefID: id, Slot: row.Slot, AsOf: row.AsOf, NarrativeStatus: row.NarrativeStatus,
				Narrative: []MobileBriefPara{{Text: "Review is at 2:00 PM."}}}, true
		}
	}
	return MobileBriefV1{}, false
}
func (s *countingBriefSource) BriefPackageContext(_ context.Context, id string) (mobileBriefPackage, bool) {
	return mobileBriefPackage{BriefID: id, Version: "v-" + id}, true
}

// A phone Today rebuild read every stored brief for its lead, then each recent
// written brief again (63 sealed reads per rebuild on 2026-11-15). It reads
// only the last 14 days' written briefs, once each.
func TestPhoneTodayReadsEachRecentWrittenBriefOnce(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	src := &countingBriefSource{reads: map[string]int{}}
	add := func(daysAgo int, status string) string {
		at := now.AddDate(0, 0, -daysAgo)
		id := fmt.Sprintf("%s-morning", at.Format("2006-01-02"))
		src.rows = append(src.rows, MobileBriefSummary{BriefID: id, Slot: mobileBriefSlotMorning, AsOf: at.Format(time.RFC3339), NarrativeStatus: status})
		return id
	}
	recent := []string{add(0, "ok"), add(1, "ok"), add(3, "ok")}
	skipped := []string{add(2, "unavailable"), add(4, "quiet"), add(20, "ok"), add(30, "ok")}
	app := &App{}
	app.mobileBriefSvc = src
	bridge := newMobileBriefPrivacyBridge(t, app)
	// The bridge warms its own Today in the background through the same
	// source; hold that build off (or wait out one in flight) so only this
	// pass's reads are counted. The count raced it before.
	bridge.today.buildMu.Lock()
	defer bridge.today.buildMu.Unlock()
	clear(src.reads)
	briefs := bridge.todayBriefs(src, now, MobileBriefSchedule{})
	if len(briefs) != len(recent) {
		t.Fatalf("listed %d briefs, want %d", len(briefs), len(recent))
	}
	for _, id := range recent {
		if src.reads[id] != 1 {
			t.Errorf("recent written brief %s read %d times, want 1", id, src.reads[id])
		}
	}
	for _, id := range skipped {
		if src.reads[id] != 0 {
			t.Errorf("brief %s is never listed but was read %d times", id, src.reads[id])
		}
	}
}
