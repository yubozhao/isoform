package main

import (
	"strings"
	"testing"
	"time"
)

func purposeRowCounts(t *testing.T, app *App) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{"purpose_open_loops_v1", "purpose_open_loop_events_v1", "purpose_open_loop_evidence_v1"} {
		var n int
		if err := app.activity.store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	return counts
}

// Two commitments came from one transcript and one from another. Purging what
// was learned from the first removes its two commitments with their events
// and evidence, leaves the other whole, and leaves the ledger as immutable as
// it was: nothing else can delete from it.
func TestPurgingASourceRemovesItsCommitmentsAndNothingElse(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	at := time.Now().Add(-time.Hour)
	first := ActivitySessionRow{ID: "rec-first-hour", EndTime: at.UnixMilli()}
	second := ActivitySessionRow{ID: "rec-second-hour", EndTime: at.UnixMilli()}
	app.writeMeetingCommitments(first, "Pricing with Sam", "first transcript", meetingFollowupExtraction{Followups: []meetingFollowup{
		{Text: "Send the pricing page tonight", Owner: meetingFollowupOwnerMe},
		{Text: "Ask legal about the top tier", Owner: meetingFollowupOwnerMe},
		{Text: "Review it", Owner: meetingFollowupOwnerOther, Person: "Sam"},
	}})
	app.writeMeetingCommitments(second, "Standup", "second transcript", meetingFollowupExtraction{Followups: []meetingFollowup{
		{Text: "Book the venue", Owner: meetingFollowupOwnerMe},
	}})
	before := purposeRowCounts(t, app)
	if before["purpose_open_loops_v1"] != 3 {
		t.Fatalf("commitments before: %+v", before)
	}

	// The wrong kind and an unknown source purge nothing.
	if n, err := app.purgePurposeLearnedFrom("activity_session", []string{first.ID}); err != nil || n != 0 {
		t.Fatalf("purged %d by the wrong kind: %v", n, err)
	}
	if n, err := app.purgePurposeLearnedFrom(meetingFollowupSourceKind, []string{"rec-nobody"}); err != nil || n != 0 {
		t.Fatalf("purged %d of an unknown source: %v", n, err)
	}

	n, err := app.purgePurposeLearnedFrom(meetingFollowupSourceKind, []string{first.ID})
	if err != nil || n != 2 {
		t.Fatalf("purged %d: %v", n, err)
	}
	after := purposeRowCounts(t, app)
	if after["purpose_open_loops_v1"] != 1 || after["purpose_open_loop_events_v1"] != before["purpose_open_loop_events_v1"]-2 ||
		after["purpose_open_loop_evidence_v1"] != before["purpose_open_loop_evidence_v1"]-2 {
		t.Fatalf("rows before %+v after %+v", before, after)
	}
	db := app.activity.store.db
	for _, gone := range []string{"pricing page", "legal"} {
		var left int
		if err := db.QueryRow(`SELECT COUNT(*) FROM purpose_open_loop_events_v1 WHERE title LIKE ?`, "%"+gone+"%").Scan(&left); err != nil || left != 0 {
			t.Fatalf("%q is still stored: %d rows %v", gone, left, err)
		}
	}
	var sourced int
	if err := db.QueryRow(`SELECT COUNT(*) FROM purpose_open_loop_evidence_v1 WHERE source_id = ?`, first.ID).Scan(&sourced); err != nil || sourced != 0 {
		t.Fatalf("evidence of the purged source is still stored: %d %v", sourced, err)
	}
	purpose, _ := app.purposeProductStore()
	loops, err := purpose.ListDetailsAll()
	if err != nil || len(loops) != 1 || loops[0].Current.Title != "Book the venue" {
		t.Fatalf("what is left: %+v %v", loops, err)
	}

	// The ledger is as immutable as before: the guards are back.
	for _, table := range []string{"purpose_open_loops_v1", "purpose_open_loop_events_v1", "purpose_open_loop_evidence_v1"} {
		if _, err := db.Exec(`DELETE FROM ` + table); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("%s can be deleted from after a purge: %v", table, err)
		}
	}
	// And it still takes new commitments, including the same one again.
	app.writeMeetingCommitments(first, "Pricing with Sam", "first transcript", meetingFollowupExtraction{Followups: []meetingFollowup{
		{Text: "Send the pricing page tonight", Owner: meetingFollowupOwnerMe},
	}})
	if loops, err := purpose.ListDetailsAll(); err != nil || len(loops) != 2 {
		t.Fatalf("after writing again: %d %v", len(loops), err)
	}
	// Purging nothing is not an error.
	if n, err := app.purgePurposeOpenLoops(nil); err != nil || n != 0 {
		t.Fatalf("purging nothing: %d %v", n, err)
	}
}
