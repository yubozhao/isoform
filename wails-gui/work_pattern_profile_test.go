package main

import (
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
)

func saveWorkPatternSession(
	t *testing.T,
	store *ActivityStore,
	id, status string,
	startAt int64,
	apps []string,
) int64 {
	t.Helper()
	events := make([]activitymon.ActivityEvent, 0, len(apps))
	for index, app := range apps {
		events = append(events, activitymon.ActivityEvent{
			Timestamp:   startAt + int64(index)*10_000,
			Kind:        activitymon.EventAppSwitch,
			AppBundleID: "test." + strings.ToLower(app),
			AppName:     app,
		})
	}
	endAt := startAt + int64(len(apps))*10_000
	if err := store.SaveSession(ActivitySessionRow{
		ID:           id,
		StartTime:    startAt,
		EndTime:      endAt,
		TriggerKind:  "test",
		AppNames:     `[]`,
		EventCount:   len(events),
		TriageStatus: status,
		CreatedAt:    endAt,
	}, events, nil); err != nil {
		t.Fatalf("SaveSession(%s): %v", id, err)
	}
	return endAt
}

func metricByName(
	t *testing.T,
	projection WorkPatternProjection,
	name string,
) WorkPatternMetric {
	t.Helper()
	for _, metric := range projection.Metrics {
		if metric.Name == name {
			return metric
		}
	}
	t.Fatalf("projection %s missing metric %s", projection.ScopeKey, name)
	return WorkPatternMetric{}
}

func projectionByScope(
	t *testing.T,
	snapshot WorkPatternProfileSnapshot,
	scopeKind, scopeKey string,
) WorkPatternProjection {
	t.Helper()
	for _, projection := range snapshot.Projections {
		if projection.ScopeKind == scopeKind &&
			projection.ScopeKey == scopeKey {
			return projection
		}
	}
	t.Fatalf(
		"profile missing projection %s/%s: %+v",
		scopeKind,
		scopeKey,
		snapshot.Projections,
	)
	return WorkPatternProjection{}
}

func TestWorkPatternProfileDerivesRapidSwitchingWithoutPersonalityLabels(
	t *testing.T,
) {
	store := newTestStore(t)
	cutoff := int64(0)
	for session := 0; session < 3; session++ {
		cutoff = saveWorkPatternSession(
			t,
			store,
			"rapid-"+string(rune('a'+session)),
			"done",
			10_000+int64(session)*200_000,
			[]string{
				"Safari",
				"ChatGPT",
				"Terminal",
				"Safari",
				"ChatGPT",
				"Terminal",
			},
		)
	}
	snapshot, added, err := store.EnsureWorkPatternProfileSnapshot(
		cutoff,
		cutoff+1_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("first profile snapshot was not added")
	}
	if snapshot.Status != WorkPatternProfileStatusQualified {
		t.Fatalf("status=%s, want qualified", snapshot.Status)
	}
	global := projectionByScope(
		t,
		snapshot,
		WorkPatternScopeGlobal,
		"all",
	)
	switchRate := metricByName(t, global, "switches_per_active_hour")
	if switchRate.Value <= 0 || switchRate.SampleCount == 0 {
		t.Fatalf("switch rate=%+v", switchRate)
	}
	if len(global.IntervalRefs) != global.SampleCount ||
		len(global.SessionRefs) != global.SessionCount {
		t.Fatalf(
			"global projection evidence is not exact: %+v",
			global,
		)
	}
	transition := projectionByScope(
		t,
		snapshot,
		WorkPatternScopeTransition,
		"test.safari->test.chatgpt",
	)
	if transition.Status != WorkPatternProfileStatusQualified {
		t.Fatalf("transition status=%s, want qualified", transition.Status)
	}
	payload := strings.ToLower(snapshot.CanonicalJSON)
	for _, forbidden := range []string{"distracted", "impulsive", "attention span"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("profile contains personality label %q", forbidden)
		}
	}
}

func TestWorkPatternProfileKeepsFocusedWorkInsufficientUntilSupported(
	t *testing.T,
) {
	events := []workPatternSourceEvent{
		{
			RowID:        1,
			SessionID:    "one",
			SessionStart: 1_000,
			SessionEnd:   61_000,
			Timestamp:    1_000,
			AppKey:       "test.editor",
			AppName:      "Editor",
		},
		{
			RowID:        2,
			SessionID:    "one",
			SessionStart: 1_000,
			SessionEnd:   61_000,
			Timestamp:    30_000,
			AppKey:       "test.editor",
			AppName:      "Editor",
		},
		{
			RowID:        3,
			SessionID:    "two",
			SessionStart: 100_000,
			SessionEnd:   160_000,
			Timestamp:    100_000,
			AppKey:       "test.editor",
			AppName:      "Editor",
		},
	}
	runs, transitions := deriveWorkPatternRuns(events)
	if len(runs) != 2 {
		t.Fatalf("runs=%d, want 2", len(runs))
	}
	if len(transitions) != 0 {
		t.Fatalf("transitions=%d, want 0", len(transitions))
	}
	projections := buildWorkPatternProjections(runs, transitions, 200_000)
	if projections[0].Status != WorkPatternProfileStatusInsufficient {
		t.Fatalf("status=%s, want insufficient", projections[0].Status)
	}
}

func TestWorkPatternProfileExcludesAmbientSystemSurfaces(
	t *testing.T,
) {
	store := newTestStore(t)
	cutoff := int64(0)
	for session := 0; session < 3; session++ {
		cutoff = saveWorkPatternSession(
			t,
			store,
			"ambient-system-"+string(rune('a'+session)),
			"done",
			10_000+int64(session)*200_000,
			[]string{
				"Safari",
				"loginwindow",
				"ChatGPT",
				"Safari",
				"loginwindow",
				"ChatGPT",
			},
		)
	}
	snapshot, _, err := store.EnsureWorkPatternProfileSnapshot(
		cutoff,
		cutoff+1_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(
		strings.ToLower(snapshot.CanonicalJSON),
		"loginwindow",
	) {
		t.Fatalf(
			"ambient system surface entered profile metrics: %s",
			snapshot.CanonicalJSON,
		)
	}
	foundExclusion := false
	for _, exclusion := range snapshot.Exclusions {
		if exclusion.SourceKind == "ambient_system_surface" &&
			exclusion.Reason == "non_work_system_surface" {
			foundExclusion = true
		}
	}
	if !foundExclusion {
		t.Fatalf("ambient exclusion missing: %+v", snapshot.Exclusions)
	}
	evidence, err := store.ListWorkPatternProfileEvidence(snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundEvidence := false
	for _, item := range evidence {
		if item.SourceKind == "ambient_system_surface" &&
			item.Reason == "non_work_system_surface" &&
			!item.Included {
			foundEvidence = true
		}
	}
	if !foundEvidence {
		t.Fatalf("ambient exclusion evidence missing: %+v", evidence)
	}
}

func TestWorkPatternProfileExcludesProtectedSessionsAndIsIdempotent(
	t *testing.T,
) {
	store := newTestStore(t)
	publicCutoff := saveWorkPatternSession(
		t,
		store,
		"public-session",
		"done",
		10_000,
		[]string{"Editor", "Terminal", "Editor"},
	)
	protectedCutoff := saveWorkPatternSession(
		t,
		store,
		"protected-session",
		"protected",
		100_000,
		[]string{"Passwords", "Mail", "Passwords", "Mail"},
	)
	snapshot, added, err := store.EnsureWorkPatternProfileSnapshot(
		protectedCutoff,
		protectedCutoff+1_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("first profile snapshot was not added")
	}
	if snapshot.SourceCutoffAt != protectedCutoff {
		t.Fatalf(
			"cutoff=%d, want %d",
			snapshot.SourceCutoffAt,
			protectedCutoff,
		)
	}
	evidence, err := store.ListWorkPatternProfileEvidence(snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundPublic := false
	foundPrivacyExclusion := false
	for _, item := range evidence {
		if item.SourceID == "public-session" && item.Included {
			foundPublic = true
		}
		if item.Reason == "privacy_excluded" && !item.Included {
			foundPrivacyExclusion = true
		}
		if item.SourceID == "protected-session" {
			t.Fatal("protected session identifier entered profile evidence")
		}
	}
	if !foundPublic || !foundPrivacyExclusion {
		t.Fatalf("evidence=%+v", evidence)
	}
	if strings.Contains(snapshot.CanonicalJSON, "Passwords") ||
		strings.Contains(snapshot.CanonicalJSON, "Mail") {
		t.Fatalf("protected app names entered profile: %s", snapshot.CanonicalJSON)
	}

	existing, added, err := store.EnsureWorkPatternProfileSnapshot(
		protectedCutoff,
		protectedCutoff+9_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if added || existing.ID != snapshot.ID || existing.SHA256 != snapshot.SHA256 {
		t.Fatalf("idempotent snapshot mismatch: %+v %+v", snapshot, existing)
	}
	if publicCutoff <= 0 {
		t.Fatal("public cutoff was not recorded")
	}
}

func TestWorkPatternProfileLedgersAndFrozenColumnsAreImmutable(
	t *testing.T,
) {
	store := newTestStore(t)
	cutoff := saveWorkPatternSession(
		t,
		store,
		"immutable-session",
		"done",
		10_000,
		[]string{"Editor", "Terminal", "Editor"},
	)
	snapshot, _, err := store.EnsureWorkPatternProfileSnapshot(
		cutoff,
		cutoff+1_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE work_pattern_profile_snapshots_v1
		SET status = 'qualified'
		WHERE id = ?`,
		snapshot.ID,
	); err == nil {
		t.Fatal("profile snapshot update unexpectedly succeeded")
	}
	if _, err := store.db.Exec(`
		DELETE FROM work_pattern_profile_snapshots_v1 WHERE id = ?`,
		snapshot.ID,
	); err == nil {
		t.Fatal("profile snapshot delete unexpectedly succeeded")
	}
	for _, test := range []struct {
		table  string
		column string
	}{
		{"task_episode_context_snapshots", "work_profile_snapshot_id"},
		{"task_episode_context_snapshots", "work_profile_sha256"},
		{"shadow_runs", "work_profile_snapshot_id"},
		{"shadow_runs", "work_profile_sha256"},
	} {
		has, err := store.hasColumn(test.table, test.column)
		if err != nil {
			t.Fatal(err)
		}
		if !has {
			t.Fatalf("%s.%s is missing", test.table, test.column)
		}
	}
}

func TestWorkPatternProfileTracksInactivityStabilityOutliersAndDecay(
	t *testing.T,
) {
	values := make([]float64, 0, 20)
	for index := 0; index < 19; index++ {
		values = append(values, 30+float64(index%3))
	}
	values = append(values, 36_000)
	robust, outliers, stability := workPatternRobustSeries(values)
	if len(robust) != len(values) || outliers == 0 || stability <= 0 {
		t.Fatalf(
			"robust series len=%d outliers=%d stability=%v",
			len(robust),
			outliers,
			stability,
		)
	}
	if robust[len(robust)-1] >= values[len(values)-1] {
		t.Fatalf("extreme outlier was not winsorized: %v", robust)
	}

	const dayMS = int64((24 * time.Hour) / time.Millisecond)
	runs := []workPatternRun{
		{
			SessionID: "session-1",
			AppKey:    "test.editor",
			AppName:   "Editor",
			StartAt:   dayMS,
			EndAt:     dayMS + 30_000,
		},
		{
			SessionID: "session-1",
			AppKey:    "test.terminal",
			AppName:   "Terminal",
			StartAt:   dayMS + 10*60_000,
			EndAt:     dayMS + 11*60_000,
		},
	}
	projection := buildWorkPatternProjection(
		WorkPatternScopeGlobal,
		"all",
		"All observed work",
		nil,
		30*dayMS,
		runs,
		nil,
	)
	gap := metricByName(t, projection, "inactivity_gap_count")
	if gap.Value != 1 || gap.SourceWindowStart == 0 ||
		gap.SourceWindowEnd == 0 || gap.DecayWeight >= 1 {
		t.Fatalf("inactivity/freshness metric=%+v", gap)
	}
	if projection.FreshnessAt != runs[1].EndAt ||
		projection.DecayWeight >= 1 {
		t.Fatalf("projection freshness=%+v", projection)
	}
}

func TestWorkPatternProfileSupportsEvaluatedCapabilityScope(t *testing.T) {
	reliability := 0.9
	projections := workPatternCapabilityProjections(
		[]WorkPatternCapabilitySignal{{
			ProfileID:          "capability-profile-1",
			CapabilityKey:      "review.pull.request",
			Ready:              true,
			Status:             "qualified",
			ActionReliability:  &reliability,
			EvaluationCount:    5,
			QualifiedCount:     4,
			UnknownReasoning:   2,
			SourceContextCount: 3,
			CreatedAt:          10_000,
		}},
		20_000,
	)
	if len(projections) != 1 {
		t.Fatalf("capability projections=%+v", projections)
	}
	projection := projections[0]
	if projection.ScopeKind != WorkPatternScopeCapability ||
		projection.EvidenceKind != WorkPatternEvidenceEvaluated ||
		projection.Status != WorkPatternProfileStatusQualified {
		t.Fatalf("capability projection=%+v", projection)
	}
	if metricByName(t, projection, "action_reliability").Value != reliability {
		t.Fatalf("capability metrics=%+v", projection.Metrics)
	}
}
