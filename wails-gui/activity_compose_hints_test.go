package main

import (
	"strings"
	"testing"

	"wails-gui/internal/platform/activitymon"
)

func TestComposerTriageHintsDoNotSplitTaskAcrossSessionCut(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTimelineTestStore(t)
	tr := &ActivityTriager{app: &App{store: newIdentityTestStore(t), activity: &activityMonitorState{store: store}}}
	sessions := []ActivitySessionRow{
		{ID: "capture-terminal", StartTime: 10_000, EndTime: 30_000, AppNames: `["Terminal"]`},
		{ID: "capture-chat", StartTime: 390_000, EndTime: 410_000, AppNames: `["Claude"]`},
	}
	if groups := mergeSessions(sessions); len(groups) != 2 {
		t.Fatalf("fixture must cross two extraction groups, got %d", len(groups))
	}
	var runs []ActivityTimelineRunRow
	for i, session := range sessions {
		app, bundle := "Terminal", "com.apple.Terminal"
		if i == 1 {
			app, bundle = "Claude", "com.anthropic.claudefordesktop"
		}
		events := []activitymon.ActivityEvent{
			{Kind: activitymon.EventKeyboard, Timestamp: session.StartTime, AppName: app, AppBundleID: bundle, TypedText: "~/sample-agent/records 60 GB"},
			{Kind: activitymon.EventKeyboard, Timestamp: session.EndTime, AppName: app, AppBundleID: bundle, TypedText: "~/sample-agent/records 60 GB"},
		}
		saveTimelineTestSession(t, store, session, events)
		derived, err := store.GetTimelineRuns(1, 0, session.StartTime, session.EndTime)
		if err != nil || len(derived) != 1 {
			t.Fatalf("derive run: %+v, %v", derived, err)
		}
		run := derived[0]
		if ok, err := store.RefreshTimelineRunEvidence(&run); err != nil || !ok {
			t.Fatalf("refresh run: %v, %v", ok, err)
		}
		runs = append(runs, run)
	}
	summary := ActivityTimelineGroupSummary{Title: "Investigate sample records storage"}
	firstResponse := composerResponse{Route: "new", Bucket: composerBucketUpdate{Title: summary.Title, Status: activityStatusActive, Category: "sustain"}}
	if changed, err := tr.composeApply(store, runs[0], summary, firstResponse, runs[0].EndMs); err != nil || !changed {
		t.Fatalf("first activity: %v, %v", changed, err)
	}
	candidates, err := tr.activityAssignmentCandidates(store, runs[1], summary)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("cross-group candidates: %+v, %v", candidates, err)
	}
	transition := tr.composeTransitionContext(store, runs[1])
	if len(transition.TriageHints) != 1 || transition.TriageHints[0].SameBatch || !transition.TriageHints[0].CrossesSessionCut {
		t.Fatalf("wanted optional split-batch/session hint: %+v", transition.TriageHints)
	}
	prompt := buildComposerPrompt(candidates, runs[1], summary, transition, "")
	if !strings.Contains(prompt, "different triage batch is NEVER a reason to split") || !strings.Contains(prompt, "different extraction batches; crosses a capture-session cut") {
		t.Fatalf("prompt must make the extraction boundary optional: %s", prompt)
	}
	// Deterministic persistence replay at the model-output seam, not a mocked
	// model integration. Independent observed path/figure evidence admits it.
	response := composerResponse{
		Route: "join", JoinBucketID: candidates[0].ID, Confidence: "high", Reason: "shared_path", Bucket: firstResponse.Bucket,
		Admission: []composerAdmission{{BucketID: candidates[0].ID, Decision: "join", ObjectiveMatch: "medium", SubjectMatch: "strong", StateContinuity: "strong"}},
	}
	response = tr.guardComposerAssignment(response, candidates)
	if response.Route != "join" {
		t.Fatalf("session boundary rejected evidence-backed join: %+v", response)
	}
	if changed, err := tr.composeApply(store, runs[1], summary, response, runs[1].EndMs); err != nil || !changed {
		t.Fatalf("join activity: %v, %v", changed, err)
	}
	activities, err := store.GetActivitiesInRange(0, 0)
	if err != nil || len(activities) != 1 || len(composeParseStrings(activities[0].RunIDs)) != 2 {
		t.Fatalf("session cut split the task: %+v, %v", activities, err)
	}
}

func TestComposerTriageHintsAreBoundedMetadataOnly(t *testing.T) {
	current := ActivityTimelineRunRow{ID: "current", StartMs: 100_000, SessionIDs: []string{"private-session-current"}}
	groups := []sessionGroup{{Sessions: []ActivitySessionRow{{ID: current.SessionIDs[0]}}}}
	prior := make([]ActivityTimelineRunRow, 4)
	for i := range prior {
		id := "prior-" + string(rune('a'+i))
		prior[i] = ActivityTimelineRunRow{ID: id, EndMs: current.StartMs - int64(i+1)*1_000, SessionIDs: []string{"private-session-" + id}}
		groups[0].Sessions = append(groups[0].Sessions, ActivitySessionRow{ID: prior[i].SessionIDs[0]})
	}
	hints := composerTriageHintsForGroups(current, prior, groups)
	if len(hints) != 3 {
		t.Fatalf("hints = %+v, want last three runs only", hints)
	}
	for _, hint := range hints {
		if !hint.SameBatch || !hint.CrossesSessionCut {
			t.Fatalf("same batch must remain distinct from same session: %+v", hint)
		}
	}
	prompt := buildComposerPrompt(nil, current, ActivityTimelineGroupSummary{}, composerTransition{TriageHints: hints}, "")
	if strings.Contains(prompt, "private-session") || strings.Contains(prompt, "prior-d") {
		t.Fatalf("prompt leaked session identifiers or a fourth run: %s", prompt)
	}
	if !strings.Contains(prompt, "not task boundaries or identity evidence") || !strings.Contains(prompt, "being in the same batch is NEVER sufficient evidence to join") {
		t.Fatalf("prompt lost hint-only contract: %s", prompt)
	}
	// Batching is never consulted by the scorer/admission guard. A shared
	// batch cannot turn weak objective/subject evidence into a valid join.
	tr := &ActivityTriager{}
	response := tr.guardComposerAssignment(composerResponse{
		Route: "join", JoinBucketID: "candidate", Confidence: "high",
		Admission: []composerAdmission{{BucketID: "candidate", Decision: "join", ObjectiveMatch: "weak", SubjectMatch: "weak", StateContinuity: "strong", Reason: "same extraction batch"}},
	}, []ActivityRow{{ID: "candidate"}})
	if response.Route != "new" {
		t.Fatalf("batch hint manufactured identity evidence: %+v", response)
	}
}

func TestComposerTriageHintsOmitExcludedMissingAndExpiredSources(t *testing.T) {
	current := ActivityTimelineRunRow{ID: "current", StartMs: activityWeakLinkMs + 10_000, SessionIDs: []string{"current-session"}}
	prior := ActivityTimelineRunRow{ID: "prior", EndMs: current.StartMs - 1_000, SessionIDs: []string{"prior-session"}}
	for _, tc := range []struct {
		name      string
		current   ActivityTimelineRunRow
		prior     ActivityTimelineRunRow
		protected bool
		missing   bool
	}{
		{name: "excluded current", current: func() ActivityTimelineRunRow { r := current; r.EvidenceExcluded = true; return r }(), prior: prior},
		{name: "excluded prior", current: current, prior: func() ActivityTimelineRunRow { r := prior; r.EvidenceExcluded = true; return r }()},
		{name: "protected source", current: current, prior: prior, protected: true},
		{name: "missing source", current: current, prior: prior, missing: true},
		{name: "expired prior", current: current, prior: func() ActivityTimelineRunRow { r := prior; r.EndMs = 1; return r }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups := []sessionGroup{{Sessions: []ActivitySessionRow{{ID: current.SessionIDs[0]}}}}
			if !tc.missing {
				session := ActivitySessionRow{ID: prior.SessionIDs[0]}
				if tc.protected {
					session.TriageStatus = "protected"
				}
				groups[0].Sessions = append(groups[0].Sessions, session)
			}
			if hints := composerTriageHintsForGroups(tc.current, []ActivityTimelineRunRow{tc.prior}, groups); len(hints) != 0 {
				t.Fatalf("unsafe or stale source contributed hints: %+v", hints)
			}
		})
	}
}

func TestComposerTriageHintsRecheckProtectedSourceAtRead(t *testing.T) {
	store := newTimelineTestStore(t)
	current := ActivityTimelineRunRow{ID: "current", AppName: "Terminal", StartMs: 100_000, EndMs: 110_000, SessionIDs: []string{"current-session"}}
	prior := ActivityTimelineRunRow{ID: "prior", AppName: "Terminal", StartMs: 80_000, EndMs: 90_000, SessionIDs: []string{"prior-session"}}
	for _, run := range []ActivityTimelineRunRow{prior, current} {
		if err := store.SaveSession(ActivitySessionRow{ID: run.SessionIDs[0], StartTime: run.StartMs, EndTime: run.EndMs, AppNames: `["Terminal"]`}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if hints := store.composerTriageHints(current, []ActivityTimelineRunRow{prior}); len(hints) != 1 {
		t.Fatalf("initial eligible hints = %+v", hints)
	}
	if _, err := store.db.Exec(`UPDATE activity_sessions SET triage_status = 'protected' WHERE id = ?`, prior.SessionIDs[0]); err != nil {
		t.Fatal(err)
	}
	if hints := store.composerTriageHints(current, []ActivityTimelineRunRow{prior}); len(hints) != 0 {
		t.Fatalf("new privacy exclusion survived composer read: %+v", hints)
	}
}
