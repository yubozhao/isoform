package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"wails-gui/internal/platform/activitymon"
)

func TestActivityOutcomeMessageFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/movement/outcomes.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name, App, Bundle, Window, Text, Click, Key, Capture, Status, Reason, Target, URL string
			Modifiers                                                                         uint64
			Private, Secure                                                                   bool
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			event := activitymon.ActivityEvent{Kind: activitymon.EventKeyboard, Timestamp: 2000, AppName: tc.App, AppBundleID: tc.Bundle, TypedText: tc.Text, PrivateWindow: tc.Private, KeyChar: tc.Key, WindowTitle: tc.Window, TypedTextCaptureMethod: tc.Capture, Modifiers: tc.Modifiers}
			if tc.Secure {
				event.FocusRole = "AXSecureTextField"
			}
			events := []ActivityTimelineEvent{{ActivityEvent: event, EventID: "typed"}}
			if tc.Click != "" {
				click := event
				click.Kind = activitymon.EventClick
				click.Timestamp = 2100
				click.ClickLabel = tc.Click
				click.TypedText = ""
				events = append(events, ActivityTimelineEvent{ActivityEvent: click, EventID: "clicked"})
			}
			context := activityOutcomeInferenceContext{WebDestinations: map[string]string{}}
			if destination := activityOutcomeWebDestination(tc.URL); destination != "" {
				for _, event := range events {
					context.WebDestinations[event.EventID] = destination
				}
			}
			out := inferActivityMessageOutcomes("task", ActivityTimelineRunRow{ID: "run", AppName: tc.App}, extractActivityEvidenceTokens("~/.sample-agent/records 60GB", 1000), events, context)
			if tc.Status == "" {
				if len(out) != 0 {
					t.Fatalf("unexpected outcome %+v", out)
				}
				return
			}
			if len(out) != 1 || out[0].Status != tc.Status || out[0].Reason != tc.Reason {
				t.Fatalf("outcome=%+v", out)
			}
			if tc.Target != "" && out[0].Target != tc.Target {
				t.Fatalf("destination=%q want %q", out[0].Target, tc.Target)
			}
			encoded, _ := json.Marshal(out)
			if strings.Contains(string(encoded), "Please inspect") || strings.Contains(string(encoded), "I am done") {
				t.Fatalf("raw message persisted: %s", encoded)
			}
		})
	}
}

func outcomeTestRun(t *testing.T, store *ActivityStore, id, app, bundle string, at int64, text, click string) ActivityTimelineRunRow {
	t.Helper()
	events := []activitymon.ActivityEvent{{Kind: activitymon.EventKeyboard, Timestamp: at, AppName: app, AppBundleID: bundle, TypedText: text}}
	if click != "" {
		events = append(events, activitymon.ActivityEvent{Kind: activitymon.EventClick, Timestamp: at + 100, AppName: app, AppBundleID: bundle, ClickLabel: click})
	}
	saveTimelineTestSession(t, store, ActivitySessionRow{ID: id, StartTime: at, EndTime: at + 100, AppNames: composeJSON([]string{app})}, events)
	runs, err := store.GetTimelineRuns(1, 0, at, at+100)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	run := runs[0]
	if _, err := store.RefreshTimelineRunEvidence(&run); err != nil {
		t.Fatal(err)
	}
	return run
}

func outcomeTestLink(t *testing.T, store *ActivityStore, activityID string, run ActivityTimelineRunRow) {
	t.Helper()
	a, err := store.GetActivityByID(activityID)
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		a = &ActivityRow{ID: activityID, Status: activityStatusActive, StartMs: run.StartMs, EndMs: run.EndMs, RunIDs: composeJSON([]string{run.ID})}
		if err := store.InsertActivity(*a); err != nil {
			t.Fatal(err)
		}
	} else {
		ids := composeParseStrings(a.RunIDs)
		ids = append(ids, run.ID)
		a.RunIDs = composeJSON(ids)
		a.EndMs = run.EndMs
		if err := store.UpdateActivity(*a); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertActivityRunLink(run.ID, activityID, run.EventCount, run.EndMs); err != nil {
		t.Fatal(err)
	}
}

func outcomeTestSnapshot(t *testing.T, store *ActivityStore, run ActivityTimelineRunRow, text string) {
	t.Helper()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshots(tx, []ActivitySnapshotRow{{SessionID: run.SessionIDs[0], Timestamp: run.StartMs, App: run.AppName, AXText: text}}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshTimelineRunEvidence(&run); err != nil {
		t.Fatal(err)
	}
}

func TestActivityOutcomeTransitionsAndCorrection(t *testing.T) {
	store := newActivityEvidenceTestStore(t)
	terminal := outcomeTestRun(t, store, "baseline", "iTerm2", "com.googlecode.iterm2", 1000, "~/.sample-agent/records", "")
	outcomeTestSnapshot(t, store, terminal, "~/.sample-agent/records 60.3 GB")
	outcomeTestLink(t, store, "task", terminal)
	chat := outcomeTestRun(t, store, "handoff", "Claude", "com.anthropic.claudefordesktop", 400000, "Please inspect ~/.sample-agent/records at 60GB", "Send")
	outcomeTestLink(t, store, "task", chat)
	for range 2 {
		if err := store.ObserveActivityRunOutcome("task", chat, chat.EndMs); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.GetActivityOutcomes("task")
	if err != nil || len(items) != 1 || items[0].Status != activityOutcomeReported {
		t.Fatalf("reported=%+v err=%v", items, err)
	}
	a, _ := store.GetActivityByID("task")
	if a.Status != activityStatusActive {
		t.Fatalf("report closed task: %+v", a)
	}
	check := outcomeTestRun(t, store, "check", "iTerm2", "com.googlecode.iterm2", 500000, "~/.sample-agent/records", "")
	outcomeTestSnapshot(t, store, check, "~/.sample-agent/records 4 GB")
	outcomeTestLink(t, store, "task", check)
	if err := store.ObserveActivityRunOutcome("task", check, check.EndMs); err != nil {
		t.Fatal(err)
	}
	latest, err := store.GetLatestActivityOutcome("task")
	if err != nil || latest == nil || latest.Status != activityOutcomeFixed || len(latest.Evidence) != 2 {
		t.Fatalf("fixed=%+v err=%v", latest, err)
	}
	complete := outcomeTestRun(t, store, "completed", "Claude", "com.anthropic.claudefordesktop", 600000, "I am done with ~/.sample-agent/records", "Send")
	outcomeTestLink(t, store, "task", complete)
	if err := store.ObserveActivityRunOutcome("task", complete, complete.EndMs); err != nil {
		t.Fatal(err)
	}
	latest, _ = store.GetLatestActivityOutcome("task")
	if latest.Status != activityOutcomeDone {
		t.Fatalf("done=%+v", latest)
	}
	// Replaying older evidence cannot regress or duplicate terminal results.
	if err := store.ObserveActivityRunOutcome("task", chat, complete.EndMs); err != nil {
		t.Fatal(err)
	}
	items, _ = store.GetActivityOutcomes("task")
	if len(items) != 3 {
		t.Fatalf("replay outcomes=%+v", items)
	}
	if err := store.UnlinkActivityRun("task", complete.ID, complete.EndMs); err != nil {
		t.Fatal(err)
	}
	latest, _ = store.GetLatestActivityOutcome("task")
	if latest.Status != activityOutcomeFixed {
		t.Fatalf("correction did not restore fixed: %+v", latest)
	}
	if err := store.UnlinkActivityRun("task", check.ID, complete.EndMs); err != nil {
		t.Fatal(err)
	}
	latest, _ = store.GetLatestActivityOutcome("task")
	if latest.Status != activityOutcomeReported {
		t.Fatalf("correction did not restore report: %+v", latest)
	}
	a, _ = store.GetActivityByID("task")
	if !activityStatusResumable(a.Status) {
		t.Fatalf("corrected terminal kept task closed: %+v", a)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM activity_outcomes WHERE activity_id='task'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
}

func TestActivityOutcomeRejectsUnrelatedOrAmbiguousMeasurements(t *testing.T) {
	observation := func(text string, at int64) activityOutcomeObservation {
		return activityOutcomeObservation{evidence: ActivityOutcomeEvidence{SourceKind: "activity_snapshot", ObservedAt: at, Tokens: extractActivityEvidenceTokens(text, at)}}
	}
	before := []activityOutcomeObservation{observation("~/.sample-agent/records 60GB", 1000)}
	for _, text := range []string{"/other/folder 4GB", "~/.sample-agent/records 60.3GB", "~/.sample-agent/records 90GB", "~/.sample-agent/records 4s"} {
		if out := inferActivityChangedTarget("task", ActivityTimelineRunRow{ID: "run"}, before, []activityOutcomeObservation{observation(text, 2000)}); len(out) != 0 {
			t.Fatalf("false fixed for %q: %+v", text, out)
		}
	}
}

func TestActivityOutcomeCorrectionInvalidatesBaselineEvidence(t *testing.T) {
	store := newActivityEvidenceTestStore(t)
	baseline := outcomeTestRun(t, store, "baseline", "iTerm2", "com.googlecode.iterm2", 1000, "~/.sample-agent/records", "")
	outcomeTestSnapshot(t, store, baseline, "~/.sample-agent/records 60GB")
	outcomeTestLink(t, store, "task", baseline)
	chat := outcomeTestRun(t, store, "chat", "Claude", "com.anthropic.claudefordesktop", 2000, "Please inspect ~/.sample-agent/records", "Send")
	outcomeTestLink(t, store, "task", chat)
	if err := store.ObserveActivityRunOutcome("task", chat, chat.EndMs); err != nil {
		t.Fatal(err)
	}
	check := outcomeTestRun(t, store, "check", "iTerm2", "com.googlecode.iterm2", 3000, "~/.sample-agent/records", "")
	outcomeTestSnapshot(t, store, check, "~/.sample-agent/records 4GB")
	outcomeTestLink(t, store, "task", check)
	if err := store.ObserveActivityRunOutcome("task", check, check.EndMs); err != nil {
		t.Fatal(err)
	}
	latest, _ := store.GetLatestActivityOutcome("task")
	if latest == nil || latest.Status != activityOutcomeFixed {
		t.Fatalf("fixed=%+v", latest)
	}
	if latest.Evidence[0].RunID != baseline.ID || latest.Evidence[1].RunID != check.ID {
		t.Fatalf("missing per-source run provenance: %+v", latest)
	}
	if err := store.UnlinkActivityRun("task", baseline.ID, check.EndMs); err != nil {
		t.Fatal(err)
	}
	latest, _ = store.GetLatestActivityOutcome("task")
	if latest == nil || latest.Status != activityOutcomeReported {
		t.Fatalf("unrelated baseline retained fixed: %+v", latest)
	}
	a, _ := store.GetActivityByID("task")
	if !activityStatusResumable(a.Status) {
		t.Fatalf("invalid fixed kept activity closed: %+v", a)
	}
}

func TestActivityOutcomesMigrationRecovery(t *testing.T) {
	store := newActivityEvidenceTestStore(t)
	if _, err := store.db.Exec(`DELETE FROM migration_log`); err != nil {
		t.Fatal(err)
	}
	if err := store.bootstrapMigrationLog(); err != nil {
		t.Fatal(err)
	}
	if !store.loadAppliedMigrations()["activity_outcomes_and_corrections"] {
		t.Fatal("complete schema was not recognized")
	}
	if _, err := store.db.Exec(`DROP INDEX idx_activity_run_corrections_run`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM migration_log`); err != nil {
		t.Fatal(err)
	}
	if err := store.bootstrapMigrationLog(); err != nil {
		t.Fatal(err)
	}
	if store.loadAppliedMigrations()["activity_outcomes_and_corrections"] {
		t.Fatal("partial schema was incorrectly recognized")
	}
	store.runPendingMigrations()
	if exists, err := store.hasIndex("idx_activity_run_corrections_run"); err != nil || !exists {
		t.Fatalf("correction index recovered=%v err=%v", exists, err)
	}
}

func TestActivityOutcomeDiscardedDraftCannotReport(t *testing.T) {
	base := activitymon.ActivityEvent{Kind: activitymon.EventKeyboard, Timestamp: 1000, AppName: "Claude", AppBundleID: "com.anthropic.claudefordesktop", WindowTitle: "Task", TypedText: "~/.sample-agent/records", TypedTextCaptureMethod: activitymon.TypedTextCaptureMethodAXFieldDiff}
	for _, kind := range []string{"empty_edit", "focus_change", "window_change"} {
		t.Run(kind, func(t *testing.T) {
			changed := base
			changed.Timestamp = 1100
			changed.TypedText = ""
			switch kind {
			case "empty_edit":
				changed.KeyChar = "Delete"
			case "focus_change":
				changed.Kind = activitymon.EventFocus
				changed.FocusLabel = "Other field"
			case "window_change":
				changed.WindowTitle = "Other task"
			}
			send := base
			send.Timestamp = 1200
			send.Kind = activitymon.EventClick
			send.ClickLabel = "Send"
			send.TypedText = ""
			events := []ActivityTimelineEvent{{ActivityEvent: base, EventID: "typed"}, {ActivityEvent: changed, EventID: "changed"}, {ActivityEvent: send, EventID: "send"}}
			if got := inferActivityMessageOutcomes("task", ActivityTimelineRunRow{ID: "run", AppName: "Claude"}, extractActivityEvidenceTokens(base.TypedText, 1000), events); len(got) != 0 {
				t.Fatalf("discarded draft reported: %+v", got)
			}
		})
	}
}

func TestActivityOutcomeLateEvidenceAndMergedHistoriesDoNotRegress(t *testing.T) {
	store := newActivityEvidenceTestStore(t)
	run := outcomeTestRun(t, store, "run", "Claude", "com.anthropic.claudefordesktop", 1000, "~/.sample-agent/records", "Send")
	outcomeTestLink(t, store, "task", run)
	appendReceipt := func(activityID, status string, at int64) {
		t.Helper()
		tx, err := store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		item := ActivityOutcome{ActivityID: activityID, RunID: run.ID, Status: status, Reason: "test_observation", OccurredAt: at, Evidence: []ActivityOutcomeEvidence{{RunID: run.ID, SourceKind: "activity_event", SourceID: "event-" + status, ObservedAt: at, Tokens: run.EvidenceTokens}}}
		if status == activityOutcomeFixed {
			item.Evidence[0].SourceKind = "activity_snapshot"
		}
		if err := appendActivityOutcomeTx(tx, item, 5000); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := refreshActivityOutcomeStatusTx(tx, activityID, 5000); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	appendReceipt("task", activityOutcomeReported, 4000)
	appendReceipt("task", activityOutcomeFixed, 2000)
	latest, _ := store.GetLatestActivityOutcome("task")
	if latest == nil || latest.Status != activityOutcomeFixed {
		t.Fatalf("late fixed discarded: %+v", latest)
	}
	appendReceipt("task", activityOutcomeDone, 3000)
	latest, _ = store.GetLatestActivityOutcome("task")
	if latest.Status != activityOutcomeDone {
		t.Fatalf("late done discarded: %+v", latest)
	}
	items, _ := store.GetActivityOutcomes("task")
	if len(items) != 3 {
		t.Fatalf("audit lost out-of-order receipts: %+v", items)
	}
	other := outcomeTestRun(t, store, "other", "Mail", "com.apple.mail", 6000, "~/.sample-agent/records", "Send")
	outcomeTestLink(t, store, "other", other)
	if err := store.ObserveActivityRunOutcome("other", other, other.EndMs); err != nil {
		t.Fatal(err)
	}
	if err := store.RepointActivityRunLinks("task", "other"); err != nil {
		t.Fatal(err)
	}
	latest, _ = store.GetLatestActivityOutcome("other")
	if latest == nil || latest.Status != activityOutcomeDone {
		t.Fatalf("merge report reopened terminal: %+v", latest)
	}
}

func TestActivityOutcomeLifecycleBoundaryDoesNotReadFutureSend(t *testing.T) {
	store := newActivityEvidenceTestStore(t)
	run := outcomeTestRun(t, store, "run", "Claude", "com.anthropic.claudefordesktop", 1000, "~/.sample-agent/records", "Send")
	outcomeTestLink(t, store, "task", run)
	if err := store.ObserveActivityRunOutcome("task", run, 1050); err != nil {
		t.Fatal(err)
	}
	latest, err := store.GetLatestActivityOutcome("task")
	if err != nil || latest != nil {
		t.Fatalf("future send leaked through lifecycle cutoff: %+v %v", latest, err)
	}
}

func TestActivityOutcomeChatEvidenceNeverUsesProseOrErrorLines(t *testing.T) {
	for _, text := range []string{"Explain why the sync skipped the folder, then retry", "Edit this draft before sending", "ERROR: quoted diagnostic prose must not become a chat identity", "EACCES: permission denied while explaining the issue"} {
		t.Run(text, func(t *testing.T) {
			event := ActivityTimelineEvent{EventID: "typed", ActivityEvent: activitymon.ActivityEvent{Kind: activitymon.EventKeyboard, Timestamp: 1000, AppName: "Claude", AppBundleID: "com.anthropic.claudefordesktop", TypedText: text}}
			evidence := activityOutcomeEventEvidence(event, text)
			if len(evidence.Tokens) != 0 {
				t.Fatalf("chat prose persisted as tokens: %+v", evidence.Tokens)
			}
			// Even a legacy task containing the old unsafe token cannot make the
			// same prose into a reported outcome after the policy correction.
			legacy := []ActivityEvidenceToken{{Kind: "error", Value: text, LastSeenMs: 900}}
			click := event
			click.EventID = "send"
			click.Kind = activitymon.EventClick
			click.Timestamp = 1100
			click.ClickLabel = "Send"
			click.TypedText = ""
			if got := inferActivityMessageOutcomes("task", ActivityTimelineRunRow{ID: "run", AppName: "Claude"}, legacy, []ActivityTimelineEvent{event, click}); len(got) != 0 {
				t.Fatalf("prose-only match accepted a report: %+v", got)
			}
		})
	}
	text := "Explain the error for /tmp/example/records at 60GB"
	evidence := activityOutcomeEventEvidence(ActivityTimelineEvent{EventID: "typed", ActivityEvent: activitymon.ActivityEvent{Kind: activitymon.EventKeyboard, Timestamp: 1000, AppName: "Claude", AppBundleID: "com.anthropic.claudefordesktop"}}, text)
	if !evidenceTokenExists(evidence.Tokens, "path", "/tmp/example/records") || !evidenceTokenExists(evidence.Tokens, "figure", "60 gb") {
		t.Fatalf("safe identifiers lost: %+v", evidence.Tokens)
	}
	for _, token := range evidence.Tokens {
		if token.Kind == "error" || strings.Contains(token.Value, "Explain") {
			t.Fatalf("prose survived identifier extraction: %+v", token)
		}
	}
}

func TestActivityOutcomePersistsIdentifiersWithoutChatErrorProse(t *testing.T) {
	store := newActivityEvidenceTestStore(t)
	baseline := outcomeTestRun(t, store, "baseline", "iTerm2", "com.googlecode.iterm2", 1000, "/tmp/example/records", "")
	outcomeTestLink(t, store, "task", baseline)
	text := "ERROR: explain the ordinary conversation about /tmp/example/records at 60GB"
	chat := outcomeTestRun(t, store, "chat", "Claude", "com.anthropic.claudefordesktop", 2000, text, "Send")
	outcomeTestLink(t, store, "task", chat)
	if err := store.ObserveActivityRunOutcome("task", chat, chat.EndMs); err != nil {
		t.Fatal(err)
	}
	latest, err := store.GetLatestActivityOutcome("task")
	if err != nil || latest == nil || latest.Status != activityOutcomeReported {
		t.Fatalf("reported=%+v err=%v", latest, err)
	}
	var evidenceJSON string
	if err := store.db.QueryRow(`SELECT evidence FROM activity_outcomes WHERE id=?`, latest.ID).Scan(&evidenceJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(evidenceJSON, "ERROR") || strings.Contains(evidenceJSON, "ordinary conversation") {
		t.Fatalf("chat prose leaked into receipt: %s", evidenceJSON)
	}
	for _, item := range latest.Evidence {
		for _, token := range item.Tokens {
			if token.Kind == "error" {
				t.Fatalf("chat error token persisted: %+v", token)
			}
		}
	}
}

func TestActivityOutcomeStoreRejectsAndHidesUnsafeTokenOnlyReceipts(t *testing.T) {
	store := newActivityEvidenceTestStore(t)
	run := outcomeTestRun(t, store, "chat", "Claude", "com.anthropic.claudefordesktop", 1000, "/tmp/example/records", "Send")
	outcomeTestLink(t, store, "task", run)
	unsafe := ActivityOutcome{ActivityID: "task", RunID: run.ID, Status: activityOutcomeReported, Reason: "message_sent", OccurredAt: 1100, Evidence: []ActivityOutcomeEvidence{{RunID: run.ID, SourceKind: "activity_event", SourceID: "legacy", ObservedAt: 1100, Tokens: []ActivityEvidenceToken{{Kind: "error", Value: "Explain why this ordinary sentence was captured", LastSeenMs: 1100}, {Kind: "path", Value: "/compact", LastSeenMs: 1100}}}}}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := appendActivityOutcomeTx(tx, unsafe, 1200); err == nil {
		tx.Rollback()
		t.Fatal("unsafe-only outcome was accepted")
	}
	tx.Rollback()
	if _, err := store.db.Exec(`INSERT INTO activity_outcomes(id,activity_id,run_id,status,reason,occurred_at,target,evidence,created_at) VALUES('legacy','task',?,'reported','message_sent',1100,'Claude',?,1200)`, run.ID, composeJSON(unsafe.Evidence)); err != nil {
		t.Fatal(err)
	}
	latest, err := store.GetLatestActivityOutcome("task")
	if err != nil || latest != nil {
		t.Fatalf("unsafe legacy receipt remained effective: %+v err=%v", latest, err)
	}
	unsafe.Evidence[0].Tokens = append(unsafe.Evidence[0].Tokens, ActivityEvidenceToken{Kind: "path", Value: "/tmp/example/records", LastSeenMs: 1100})
	tx, err = store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := appendActivityOutcomeTx(tx, unsafe, 1200); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	latest, err = store.GetLatestActivityOutcome("task")
	if err != nil || latest == nil || len(latest.Evidence[0].Tokens) != 1 {
		t.Fatalf("safe receipt=%+v err=%v", latest, err)
	}
	if latest.Evidence[0].Tokens[0].Value != "/tmp/example/records" {
		t.Fatalf("unsafe tokens retained: %+v", latest.Evidence)
	}
}
