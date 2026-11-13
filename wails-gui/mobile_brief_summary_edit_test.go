package main

import (
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
)

// Seal v2 rule 2: re-summarizing the runs a brief cites (the hourly batch
// rewrites a summary and its thread outcome) is an edit, not a withdrawal.
// The written words stay; only a privacy gate removes them.
func TestResummarizedSummaryLeavesSealedBriefShown(t *testing.T) {
	for _, cited := range []string{"thread_outcome"} {
		t.Run(cited, func(t *testing.T) {
			app := newPrivacyReadTestApp(t)
			previous := pii.GetUserSensitiveApps()
			t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
			pii.SetUserSensitiveApps(nil)
			now := time.Now().Truncate(time.Second)
			start := now.Add(-2 * time.Hour).UnixMilli()
			session := ActivitySessionRow{ID: "edit-source", StartTime: start, EndTime: start + 60_000, AppNames: `["Safari"]`, TriageStatus: "done", CreatedAt: start}
			saveTimelineTestSession(t, app.activity.store, session, []activitymon.ActivityEvent{{Kind: activitymon.EventAppSwitch, Timestamp: start, AppName: "Safari"}, {Kind: activitymon.EventAppSwitch, Timestamp: start + 30_000, AppName: "Safari"}})
			runs, err := app.activity.store.GetTimelineRuns(1, 0, 0, start+120_000)
			if err != nil || len(runs) == 0 {
				t.Fatalf("runs %v %v", runs, err)
			}
			row := ActivityRow{ID: "act_edit", Status: activityStatusSettled, Title: "Review the release notes",
				RunIDs: composeJSON([]string{runs[0].ID}), ActiveMsByApp: `{"Safari":60000}`,
				StartMs: now.Add(-2 * time.Hour).UnixMilli(), EndMs: now.Add(-time.Hour).UnixMilli()}
			if err := app.activity.store.InsertActivity(row); err != nil {
				t.Fatal(err)
			}
			sourceID := row.ID
			if cited == "thread_outcome" {
				// The id of the outcome the brief was written from; the summary
				// has since been written again and no longer carries it.
				sourceID = threadOutcomeMomentPrefix + row.ID + ":0123456789abcdef"
			}
			brief := buildMobileBriefV1(mobileBriefInput{AsOf: now, WindowStart: now.Add(-3 * time.Hour), Slot: mobileBriefSlotEvening, Location: time.UTC,
				Learned: []mobileBriefCandidate{{SourceKind: "memory", SourceID: "safe-recall", Title: "Safe recall title", At: now.Add(-time.Minute)}}})
			writePrivacyMemory(t, "safe-recall", "# Safe recall title\n\nA manually written note.\n")
			brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: "e90", SourceKind: mobileBriefRecapSource, SourceID: sourceID, Label: "Review the release notes", ObservedAt: formatMobileBriefTime(now)})
			brief.NarrativeStatus = "ok"
			brief.Narrative = []MobileBriefPara{{Text: "You reviewed the release notes.", Refs: []string{"e90"}}}
			got, withheld := app.mobileBriefDelivery(brief)
			if withheld.Reason == mobileBriefWithheldSource || len(got.Narrative) == 0 {
				t.Fatalf("a re-summarized source withdrew the written words: reason=%q kind=%q", withheld.Reason, withheld.SourceKind)
			}
			// A real privacy action still removes them: the summary is deleted.
			if _, err := app.activity.store.db.Exec(`DELETE FROM activities WHERE id = ?`, row.ID); err != nil {
				t.Fatal(err)
			}
			if got, _ := app.mobileBriefDelivery(brief); len(got.Narrative) != 0 {
				t.Fatal("a removed summary kept the words that cite it")
			}
		})
	}
}

// Retention is not a privacy action: it never moves the brief privacy print.
func TestRetentionRemovalDoesNotMoveBriefPrint(t *testing.T) {
	before := mobileBriefPrivacyEpoch.Load()
	publishRetentionRemoval(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: "old-session"})
	if after := mobileBriefPrivacyEpoch.Load(); after != before {
		t.Fatalf("retention moved the privacy epoch: %d -> %d", before, after)
	}
}

// A Dream merge archives a note; the watcher's report of that removal is an
// edit and withdraws nothing. A note the user deletes is still withdrawn.
func TestDreamArchiveRemovalIsNotAWithdrawal(t *testing.T) {
	noteMemoryRemovedByRewrite("merged-note")
	got := withoutRewriteRemovals([]briefWithdrawn{{Kind: "memory", ID: "merged-note"}, {Kind: "memory", ID: "user-deleted"}}, time.Now())
	if len(got) != 1 || got[0].ID != "user-deleted" {
		t.Fatalf("withdrawals after a merge: %+v", got)
	}
	if again := withoutRewriteRemovals([]briefWithdrawn{{Kind: "memory", ID: "merged-note"}}, time.Now()); len(again) != 1 {
		t.Fatal("one noted rewrite must cover one removal only")
	}
}
