package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
)

func TestCachedDailySummaryUsesRecordedSourcePrivacy(t *testing.T) {
	isolateActivityPrivacyTest(t)
	const date = "2026-04-20"
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	seedActivitySummaryI18nSession(t, store, "used-source", date, 9, []string{"Notes"})
	seedActivitySummaryI18nSession(t, store, "never-used", date, 10, []string{"Bitwarden"})
	if _, err := store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='never-used'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveSummary(date, "daily", "safe narrative", "test", false); err != nil {
		t.Fatal(err)
	}
	data, err := app.GetDailySummary(date)
	if err != nil {
		t.Fatal(err)
	}
	if data.AISummary != "safe narrative" || !app.activityDayAllowsModelRead(date) {
		t.Fatalf("capture-protected source vetoed safe digest: %+v", data)
	}
	week, err := app.GetWeeklySummary(date)
	if err != nil {
		t.Fatal(err)
	}
	if len(week.dailySummaries) != 1 {
		t.Fatalf("safe daily cache missing from weekly input: %v", week.dailySummaries)
	}
	pii.AddUserSensitiveApp("com.apple.Notes", "Notes")
	data, err = app.GetDailySummary(date)
	if err != nil {
		t.Fatal(err)
	}
	if data.AISummary != "" || data.AISummaryStatus != "none" || app.activityDayAllowsModelRead(date) {
		t.Fatalf("later-sensitive source escaped cached journal read gate: %+v", data)
	}
}

func TestRetentionPreservesDigestsAndPrivacyProvenance(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_%t", legacy), func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			store := newTestStore(t)
			app := &App{activity: &activityMonitorState{store: store}}
			day := time.Now().AddDate(0, 0, -40)
			date := day.Format("2006-01-02")
			week := day.AddDate(0, 0, -(int(day.Weekday())+6)%7).Format("2006-01-02") + "/week"
			seedActivitySummaryI18nSession(t, store, "expired", date, 9, []string{"Notes"})
			for _, item := range []struct{ date, kind string }{{date, "daily"}, {week, "weekly"}} {
				if _, err := store.SaveSummary(item.date, item.kind, "retained narrative", "test", false); err != nil {
					t.Fatal(err)
				}
			}
			if legacy {
				if _, err := store.db.Exec(`UPDATE activity_summaries SET source_provenance=''`); err != nil {
					t.Fatal(err)
				}
			}
			rotator := &StorageRotator{store: store}
			rotator.purgeExpiredSessions(RotatorConfig{ColdRetention: 30 * 24 * time.Hour})
			for _, item := range []struct{ date, kind string }{{date, "daily"}, {week, "weekly"}} {
				if _, _, _, found := store.GetCachedSummary(item.date, item.kind); !found {
					t.Fatalf("retention deleted %s digest", item.kind)
				}
			}
			data, err := app.GetDailySummary(date)
			if err != nil {
				t.Fatal(err)
			}
			if data.AISummary != "retained narrative" {
				t.Fatal("retained source proof did not keep safe daily digest readable")
			}
			weekly, err := app.GetWeeklySummary(date)
			if err != nil {
				t.Fatal(err)
			}
			if weekly.AISummary != "retained narrative" {
				t.Fatal("retained source proof did not keep safe weekly digest readable")
			}
			pii.AddUserSensitiveApp("com.apple.Notes", "Notes")
			data, err = app.GetDailySummary(date)
			if err != nil {
				t.Fatal(err)
			}
			weekly, err = app.GetWeeklySummary(date)
			if err != nil {
				t.Fatal(err)
			}
			if data.AISummary != "" || weekly.AISummary != "" {
				t.Fatal("retained digest lost app privacy after raw sources were pruned")
			}
		})
	}
}

func TestDigestPublicationIgnoresUnrelatedDeletion(t *testing.T) {
	for _, kind := range []string{"daily", "weekly"} {
		t.Run(kind, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			const date = "2026-04-20"
			runner := &activityDeletingSummaryRunner{sessionID: "unrelated-old-source"}
			app := newActivitySummaryI18nTestApp(t, &runner.activitySummaryI18nFakeRunner)
			app.runner = runner
			runner.store = app.activity.store
			seedActivitySummaryI18nSession(t, runner.store, "current-source", date, 9, []string{"Notes"})
			seedActivitySummaryI18nSession(t, runner.store, "current-source-two", date, 10, []string{"Code"})
			seedActivitySummaryI18nSession(t, runner.store, runner.sessionID, "2026-03-01", 9, []string{"Notes"})
			var err error
			if kind == "daily" {
				runner.momentsDone = make(chan struct{})
				err = app.GenerateDailySummary(date)
			} else {
				err = app.GenerateWeeklySummary(date)
			}
			if err != nil {
				t.Fatalf("unrelated source deletion rejected %s generation: %v", kind, err)
			}
			if kind == "daily" {
				_ = waitForCachedMoments(t, runner.store, date)
			}
		})
	}
}

func TestMomentsPublicationReportsCoveredSourceDeletion(t *testing.T) {
	for _, covered := range []bool{false, true} {
		t.Run(fmt.Sprintf("covered_%t", covered), func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			const date = "2026-04-20"
			store := newTestStore(t)
			app := &App{activity: &activityMonitorState{store: store}}
			seedActivitySummaryI18nSession(t, store, "source", date, 9, []string{"Notes"})
			seedActivitySummaryI18nSession(t, store, "unrelated", "2026-03-01", 9, []string{"Notes"})
			sources, err := app.captureSummarySources(date, "daily")
			if err != nil {
				t.Fatal(err)
			}
			gen, err := store.SaveSummary(date, "daily", "summary", "test", false)
			if err != nil {
				t.Fatal(err)
			}
			id := "unrelated"
			if covered {
				id = "source"
			}
			if err := store.DeleteSession(id); err != nil {
				t.Fatal(err)
			}
			ch := make(chan []MomentData, 1)
			ch <- []MomentData{{Title: "moment", SessionIDs: []string{"source"}}}
			outcome := app.persistMomentsWithSources(date, gen, "", ch, sources)
			want := "ready"
			if covered {
				want = "error"
			}
			if outcome != want {
				t.Fatalf("moment outcome=%q want %q; only generation replacement may be silent", outcome, want)
			}
		})
	}
}

func TestModelSessionAllowSetIncludesLargeAndOvernightSources(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	const startMs, endMs = int64(100000), int64(200000)
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1001; i++ {
		if _, err := tx.Exec(`INSERT INTO activity_sessions(id,start_time,end_time,trigger_kind,app_names,event_count,segment_count,snapshot_count,created_at) VALUES(?,?,?,'test','["Notes"]',0,0,0,?)`, fmt.Sprintf("source-%d", i), startMs+int64(i), startMs+int64(i)+1, startMs); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cross-start", "cross-end"} {
		start, end := startMs-1000, startMs+1000
		if id == "cross-end" {
			start, end = endMs-1000, endMs+1000
		}
		if err := store.SaveSession(ActivitySessionRow{ID: id, StartTime: start, EndTime: end, AppNames: `["Notes"]`}, []activitymon.ActivityEvent{{Kind: activitymon.EventFocus, Timestamp: startMs + 500, AppName: "Notes", WindowTitle: id + " title"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids := app.activityModelSessionIDs(store, startMs, endMs)
	if len(ids) != 1003 || !ids["cross-start"] || !ids["cross-end"] {
		t.Fatalf("allow set lost eligible sources: count=%d overnight=%t/%t", len(ids), ids["cross-start"], ids["cross-end"])
	}
	titles := strings.Join(app.extractTopWindowTitles(store, startMs, endMs), " ")
	if !strings.Contains(titles, "cross-start") || !strings.Contains(titles, "cross-end") {
		t.Fatalf("overnight source titles lost: %s", titles)
	}
}

func TestRecentActivityCapAppliesAfterPrivacyFiltering(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	now := time.Now()
	for i := 0; i < activityContextMaxSessions+1; i++ {
		start := now.Add(-time.Duration(i+1) * time.Minute).UnixMilli()
		if err := store.SaveSession(ActivitySessionRow{ID: fmt.Sprintf("private-%d", i), StartTime: start, EndTime: start + 1000, AppNames: `["Private Notes"]`}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	start := now.Add(-3 * time.Hour).UnixMilli()
	if err := store.SaveSession(ActivitySessionRow{ID: "safe", StartTime: start, EndTime: start + 1000, AppNames: `["Useful Notes"]`}, nil, nil); err != nil {
		t.Fatal(err)
	}
	runner := &modelPrivacyTaskRecorder{memoryTranslationFakeRunner: memoryTranslationFakeRunner{ready: true, response: "safe context"}}
	app.runner = runner
	if got := app.generateRecentActivitySummary(); got != "safe context" {
		t.Fatalf("safe sessions starved by privacy exclusions: %q", got)
	}
	if len(runner.inputs) != 1 || !strings.Contains(runner.inputs[0].Prompt, "Useful Notes") {
		t.Fatalf("safe source omitted from capped context: %+v", runner.inputs)
	}
}

func TestActivitySessionReadGatePrivateWindowAndReadErrors(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	session := ActivitySessionRow{ID: "private", StartTime: 1000, EndTime: 2000, AppNames: `["Safari"]`}
	if err := store.SaveSession(session, []activitymon.ActivityEvent{{Kind: activitymon.EventFocus, PrivateWindow: true}}, nil); err != nil {
		t.Fatal(err)
	}
	if allowed, err := app.activitySessionAllowsModelReadResult(store, session); allowed || err != nil {
		t.Fatalf("private-window decision=%t,%v", allowed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if allowed, err := app.activitySessionAllowsModelReadResult(store, session); allowed || err == nil {
		t.Fatalf("storage error was confused with permanent privacy exclusion: %t,%v", allowed, err)
	}
}

func TestActivityContextLogsOnlyNamePresence(t *testing.T) {
	source, err := os.ReadFile("activity_context_inject.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "user=%q") || !strings.Contains(string(source), "has_user_name=%t") {
		t.Fatal("activity context log must include name presence, not user content")
	}
}

func TestExcludedSameDayDeletionKeepsDigestAndMoments(t *testing.T) {
	isolateActivityPrivacyTest(t)
	const date = "2026-04-20"
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	seedActivitySummaryI18nSession(t, store, "source", date, 9, []string{"Notes"})
	seedActivitySummaryI18nSession(t, store, "excluded", date, 10, []string{"Bitwarden"})
	if _, err := store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='excluded'`); err != nil {
		t.Fatal(err)
	}
	sources, err := app.captureSummarySources(date, "daily")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := store.saveSummaryWithSources(date, "daily", "safe digest", "test", false, "", sources.Sources)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSession("excluded"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, found := store.GetCachedSummary(date, "daily"); !found {
		t.Fatal("deleting excluded same-day row purged safe cached digest")
	}
	ch := make(chan []MomentData, 1)
	ch <- []MomentData{{Title: "safe moment", SessionIDs: []string{"source"}}}
	if outcome := app.persistMomentsWithSources(date, gen, "", ch, sources); outcome != "ready" {
		t.Fatalf("excluded same-day deletion rejected moments: %s", outcome)
	}
}

func TestDailyModelInputsUseCapturedSourceCohort(t *testing.T) {
	isolateActivityPrivacyTest(t)
	const date = "2026-04-20"
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	seedActivitySummaryI18nSession(t, store, "captured", date, 9, []string{"Notes"})
	sources, err := app.captureSummarySources(date, "daily")
	if err != nil {
		t.Fatal(err)
	}
	seedActivitySummaryI18nSession(t, store, "arrived-later", date, 10, []string{"Uncaptured App"})
	data, err := app.getDailySummary(date, &sources, true)
	if err != nil {
		t.Fatal(err)
	}
	prompt := app.buildRichSummaryPrompt(date, data)
	if data.SessionCount != 1 || data.modelSourceIDs["arrived-later"] || strings.Contains(prompt, "Uncaptured App") {
		t.Fatalf("uncaptured source entered generation: count=%d prompt=%s", data.SessionCount, prompt)
	}
}

func TestRetainedDigestKeepsLaterPrivateSourceRestrictions(t *testing.T) {
	for _, mode := range []string{"protected-status", "private-window"} {
		t.Run(mode, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			store := newTestStore(t)
			app := &App{activity: &activityMonitorState{store: store}}
			date := time.Now().AddDate(0, 0, -40).Format("2006-01-02")
			seedActivitySummaryI18nSession(t, store, "source", date, 9, []string{"Notes"})
			if _, err := store.SaveSummary(date, "daily", "old narrative", "test", false); err != nil {
				t.Fatal(err)
			}
			if mode == "protected-status" {
				if _, err := store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='source'`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := store.db.Exec(`INSERT INTO activity_events(session_id,timestamp,kind,data,sort_order) VALUES('source',1,'focus','{"privateWindow":true}',0)`); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, _, _, found := app.cachedActivitySummaryForRead(date, "daily", ""); found {
				t.Fatal("private source allowed before retention")
			}
			if err := store.deleteRetainedSession("source"); err != nil {
				t.Fatal(err)
			}
			if _, _, _, found := store.GetCachedSummary(date, "daily"); !found {
				t.Fatal("retention unexpectedly purged digest")
			}
			if _, _, _, _, found := app.cachedActivitySummaryForRead(date, "daily", ""); found {
				t.Fatal("retention discarded a later private source restriction")
			}
		})
	}
}

func TestCachedSummarySnapshotCannotBorrowReplacementProvenance(t *testing.T) {
	for _, mode := range []string{"protection", "deletion", "retention"} {
		t.Run(mode, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			const date = "2026-04-20"
			store := newTestStore(t)
			app := &App{activity: &activityMonitorState{store: store}}
			seedActivitySummaryI18nSession(t, store, "old", date, 9, []string{"Notes"})
			if _, err := store.SaveSummary(date, "daily", "old source narrative", "test", false); err != nil {
				t.Fatal(err)
			}
			snapshot, found := store.cachedActivitySummarySnapshot(date, "daily", "")
			if !found {
				t.Fatal("missing original snapshot")
			}
			switch mode {
			case "protection":
				pii.AddUserSensitiveApp("com.apple.Notes", "Notes")
			case "deletion":
				if err := store.DeleteSession("old"); err != nil {
					t.Fatal(err)
				}
			case "retention":
				if err := store.deleteRetainedSession("old"); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "retention" {
				seedActivitySummaryI18nSession(t, store, "new", date, 10, []string{"Code"})
				sources, err := app.captureSummarySources(date, "daily")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.saveSummaryWithSources(date, "daily", "replacement narrative", "test", false, "", sources.Sources); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, _, _, found := app.cachedActivitySummaryForRead(date, "daily", ""); !found {
				t.Fatal("new safe row should remain readable")
			}
			if app.activitySummarySnapshotAllowsRead(date, "daily", snapshot) {
				t.Fatal("old content borrowed privacy proof from replacement/retention row")
			}
		})
	}
}

func TestActivitySummarySourceProvenanceMigrationIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := migrateActivitySummarySourceProvenance(tx); err != nil {
		t.Fatalf("reapplying source provenance migration: %v", err)
	}
}

func TestCachedSummaryRejectsEmptyRecordedProvenance(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	for _, proof := range []string{"null", "[]", "{"} {
		snapshot := activitySummaryReadSnapshot{Summary: "unsupported narrative", Provenance: proof}
		if app.activitySummarySnapshotAllowsRead("2026-04-20", "daily", snapshot) {
			t.Fatalf("invalid/empty recorded proof was treated as authority: %q", proof)
		}
	}
}
