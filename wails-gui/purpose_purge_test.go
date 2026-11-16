package main

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
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

func purposeTestCommitment(t *testing.T, app *App, kind, sourceID, text string) {
	t.Helper()
	store, err := app.purposeProductStore()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(sourceID))
	input := meetingFollowupCommitmentInput(sourceID, "", hex.EncodeToString(sum[:]), 0, meetingFollowup{Text: text, Owner: meetingFollowupOwnerMe}, time.Now().Add(-time.Hour))
	input.IdempotencyKey = kind + ":" + sourceID + ":" + text
	input.Evidence[0].SourceKind = kind
	if _, _, err := store.Create(input); err != nil {
		t.Fatal(err)
	}
}

func purposeTitles(t *testing.T, app *App) string {
	t.Helper()
	store, _ := app.purposeProductStore()
	loops, err := store.ListDetailsAll()
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, loop := range loops {
		titles = append(titles, loop.Current.Title)
	}
	sort.Strings(titles)
	return strings.Join(titles, "|")
}

// A person deletes an activity session. The commitments learned from that
// session go with it, whatever kind the ledger cited it under; the ones from
// another session, the ones the person added themselves, and everything when
// it was retention that removed the session, stay.
func TestDeletingAnActivitySessionPurgesWhatWasLearnedFromIt(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	old := purposePurgeApp.Swap(app)
	t.Cleanup(func() { purposePurgeApp.Store(old) })

	purposeTestCommitment(t, app, "activity_session", "session-gone", "Reply to the vendor")
	purposeTestCommitment(t, app, "session", "session-gone", "Renew the certificate")
	purposeTestCommitment(t, app, meetingFollowupSourceKind, "session-gone", "Send the minutes")
	purposeTestCommitment(t, app, activityFollowUpSourceKind, "session-gone", "My own note to self")
	purposeTestCommitment(t, app, "activity_session", "session-kept", "Book the venue")
	purposeTestCommitment(t, app, "activity", "activity-gone", "File the expense report")
	purposeTestCommitment(t, app, "activity", "activity-kept", "Water the plants")

	// Retention removing the session is not the person deleting it.
	publishRetentionRemoval(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: "session-gone"})
	// Withdrawals that name no source the ledger cites change nothing.
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalApp, ID: "session-gone"})
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: "activity-gone"})
	if got := purposeTitles(t, app); strings.Count(got, "|") != 6 {
		t.Fatalf("something was purged that should not be: %s", got)
	}

	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: "session-gone"})
	if got, want := purposeTitles(t, app), "Book the venue|File the expense report|My own note to self|Water the plants"; got != want {
		t.Fatalf("after deleting the session: %s, want %s", got, want)
	}
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "activity", ID: "activity-gone"})
	if got, want := purposeTitles(t, app), "Book the venue|My own note to self|Water the plants"; got != want {
		t.Fatalf("after deleting the activity: %s, want %s", got, want)
	}
	var left int
	if err := app.activity.store.db.QueryRow(`SELECT COUNT(*) FROM purpose_open_loop_events_v1 WHERE title IN ('Reply to the vendor','Renew the certificate','Send the minutes','File the expense report')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("purged commitments are still stored: %d rows %v", left, err)
	}
}

// The real delete: an activity session removed through the store takes the
// commitment learned from it.
func TestTheStoresSessionDeletePurgesItsCommitment(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	old := purposePurgeApp.Swap(app)
	t.Cleanup(func() { purposePurgeApp.Store(old) })
	start := time.Now().Add(-time.Hour)
	session := ActivitySessionRow{ID: "session-to-delete", StartTime: start.UnixMilli(), EndTime: start.Add(time.Minute).UnixMilli(),
		TriggerKind: "manual", AppNames: `["Notes"]`, TriageStatus: "pending", CreatedAt: time.Now().UnixMilli()}
	if err := app.activity.store.SaveSession(session, nil, nil); err != nil {
		t.Fatal(err)
	}
	purposeTestCommitment(t, app, "activity_session", session.ID, "Reply to the vendor")
	purposeTestCommitment(t, app, "activity_session", "another-session", "Book the venue")
	if err := app.activity.store.DeleteSession(session.ID); err != nil {
		t.Fatal(err)
	}
	if got := purposeTitles(t, app); got != "Book the venue" {
		t.Fatalf("after the delete: %s", got)
	}
}
