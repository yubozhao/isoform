package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
)

type backgroundTestMonitor struct {
	running bool
}

func (monitor *backgroundTestMonitor) Start(
	context.Context,
	func(activitymon.ActivityEvent),
) error {
	monitor.running = true
	return nil
}

func (monitor *backgroundTestMonitor) Stop() {
	monitor.running = false
}

func (monitor *backgroundTestMonitor) IsRunning() bool {
	return monitor.running
}

func newBackgroundUnderstandingTestStore(
	t *testing.T,
) (*ActivityStore, *backgroundUnderstandingStore) {
	t.Helper()
	activity, err := NewActivityStore(
		filepath.Join(t.TempDir(), "activity"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := activity.Close(); err != nil {
			t.Errorf("close activity store: %v", err)
		}
	})
	store, err := newBackgroundUnderstandingStore(activity.db)
	if err != nil {
		t.Fatal(err)
	}
	return activity, store
}

func addBackgroundUnderstandingTestRun(
	t *testing.T,
	store *backgroundUnderstandingStore,
	idempotencyKey, mission, source string,
	at int64,
) BackgroundUnderstandingRun {
	t.Helper()
	run, added, err := store.AddRun(
		BackgroundUnderstandingRun{
			IdempotencyKey: idempotencyKey,
			TriggerKind:    BackgroundTriggerAutomatic,
			TriggerReason:  "Use available background capacity.",
			MissionKind:    mission,
			Source:         source,
			GrantID: func() string {
				if mission == BackgroundMissionGmail {
					return "test-gmail-grant"
				}
				return ""
			}(),
			SourceScopeJSON: `{}`,
			StartedAt:       at,
		},
		map[string]any{"test": true},
	)
	if err != nil || !added {
		t.Fatalf("add background test run: added=%v err=%v", added, err)
	}
	return run
}

func TestBackgroundUnderstandingSettingsAreAppendOnlyAndIdempotent(
	t *testing.T,
) {
	_, store := newBackgroundUnderstandingTestStore(t)
	settings, err := store.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled ||
		settings.DailyRunLimit != backgroundDefaultDailyRunLimit ||
		settings.MinIntervalMinutes != backgroundDefaultMinIntervalMinutes {
		t.Fatalf("unexpected defaults: %+v", settings)
	}

	input := SetBackgroundUnderstandingSettingsInput{
		IdempotencyKey:     "settings:pause",
		Enabled:            false,
		DailyRunLimit:      3,
		MinIntervalMinutes: 180,
		Reason:             "User paused it.",
	}
	first, added, err := store.SetSettings(input, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if !added || first.Enabled || first.DailyRunLimit != 3 {
		t.Fatalf("first=%+v added=%v", first, added)
	}
	replayed, added, err := store.SetSettings(input, 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if added || replayed.ID != first.ID || replayed.UpdatedAt != first.UpdatedAt {
		t.Fatalf("replayed=%+v added=%v", replayed, added)
	}
	if _, err := store.db.Exec(`
		UPDATE background_understanding_setting_events_v1
		SET enabled = 1
		WHERE id = ?`,
		first.ID,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("settings update was not rejected: %v", err)
	}
}

func TestBackgroundGmailGrantFreezesReadScopeAndSupersedesPriorGrant(
	t *testing.T,
) {
	_, store := newBackgroundUnderstandingTestStore(t)
	first, added, err := store.GrantSource(
		GrantBackgroundUnderstandingSourceInput{
			IdempotencyKey: "grant:gmail:work",
			Source:         BackgroundSourceGmail,
			ScopeLabel:     "Work-labeled email",
			SourceQuery:    "label:work -category:promotions",
			LookbackDays:   30,
			MaxItemsPerRun: 5,
		},
		1_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !added || first.Status != "active" ||
		len(first.AllowedOperations) != 1 ||
		first.AllowedOperations[0] != BackgroundGmailComputerUseRead {
		t.Fatalf("unexpected first grant: %+v added=%v", first, added)
	}

	second, added, err := store.GrantSource(
		GrantBackgroundUnderstandingSourceInput{
			IdempotencyKey: "grant:gmail:recent",
			Source:         BackgroundSourceGmail,
			ScopeLabel:     "Recent correspondence",
			SourceQuery:    "-category:promotions -category:social",
			LookbackDays:   14,
			MaxItemsPerRun: 10,
		},
		2_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !added || second.Status != "active" {
		t.Fatalf("unexpected second grant: %+v added=%v", second, added)
	}
	prior, err := store.grantByID(first.ID, 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if prior.Status != "revoked" {
		t.Fatalf("prior scope was not revoked: %+v", prior)
	}
	active, err := store.ActiveSourceGrant(BackgroundSourceGmail, 2_000)
	if err != nil || active.ID != second.ID {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	if _, err := store.db.Exec(`
		UPDATE background_source_grants_v1
		SET source_query = ''
		WHERE id = ?`,
		second.ID,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("grant update was not rejected: %v", err)
	}

	revoked, changed, err := store.RevokeSource(
		RevokeBackgroundUnderstandingSourceInput{
			IdempotencyKey: "revoke:gmail:recent",
			GrantID:        second.ID,
			Reason:         "No longer needed.",
		},
		3_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || revoked.Status != "revoked" {
		t.Fatalf("revoked=%+v changed=%v", revoked, changed)
	}
	replayed, changed, err := store.RevokeSource(
		RevokeBackgroundUnderstandingSourceInput{
			IdempotencyKey: "revoke:gmail:recent",
			GrantID:        second.ID,
			Reason:         "No longer needed.",
		},
		4_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if changed || replayed.Status != "revoked" {
		t.Fatalf("replayed=%+v changed=%v", replayed, changed)
	}
}

func TestBackgroundUnderstandingRunReceiptIsAppendOnly(
	t *testing.T,
) {
	_, store := newBackgroundUnderstandingTestStore(t)
	run, added, err := store.AddRun(
		BackgroundUnderstandingRun{
			IdempotencyKey:  "run:local:1",
			TriggerKind:     BackgroundTriggerManual,
			TriggerReason:   "Test retained evidence.",
			MissionKind:     BackgroundMissionRetainedActivity,
			Source:          BackgroundSourceLocalActivity,
			SourceScopeJSON: `{"retainedOnly":true}`,
			StartedAt:       1_000,
		},
		map[string]any{"selected": true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !added || run.CurrentState != BackgroundRunSelected {
		t.Fatalf("run=%+v added=%v", run, added)
	}
	if _, err := store.AppendRunEvent(
		run.ID,
		BackgroundRunRunning,
		map[string]any{},
		999,
	); err == nil || !strings.Contains(err.Error(), "precedes") {
		t.Fatalf("non-monotonic transition was accepted: %v", err)
	}
	if _, err := store.AppendRunEvent(
		run.ID,
		BackgroundRunRunning,
		map[string]any{"started": true},
		2_000,
	); err != nil {
		t.Fatal(err)
	}
	item, added, err := store.AddSourceItem(
		BackgroundUnderstandingSourceItem{
			RunID:           run.ID,
			Source:          BackgroundSourceLocalActivity,
			SourceItemKind:  "aggregate",
			ExternalIDHash:  "source-hash",
			ContentHash:     "content-hash",
			AttributionKind: "aggregate",
			MetadataJSON:    `{"count":2}`,
			CreatedAt:       3_000,
		},
	)
	if err != nil || !added {
		t.Fatalf("item=%+v added=%v err=%v", item, added, err)
	}
	artifact, added, err := store.AddMaterialization(
		BackgroundUnderstandingMaterialization{
			RunID:        run.ID,
			ArtifactKind: BackgroundArtifactWorkPatternProfile,
			ArtifactID:   "profile-1",
			Action:       BackgroundMaterializationCreated,
			Summary:      "Created profile.",
			CreatedAt:    3_000,
		},
	)
	if err != nil || !added {
		t.Fatalf("artifact=%+v added=%v err=%v", artifact, added, err)
	}
	if _, err := store.AppendRunEvent(
		run.ID,
		BackgroundRunCompleted,
		map[string]any{"summary": "Done."},
		3_000,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendRunEvent(
		run.ID,
		BackgroundRunRunning,
		map[string]any{},
		4_000,
	); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("terminal transition was accepted: %v", err)
	}
	if _, _, err := store.AddMaterialization(
		BackgroundUnderstandingMaterialization{
			RunID:        run.ID,
			ArtifactKind: BackgroundArtifactKnowledge,
			ArtifactID:   "late-artifact",
			Action:       BackgroundMaterializationCreated,
			CreatedAt:    4_000,
		},
	); err == nil || !strings.Contains(err.Error(), "running mission") {
		t.Fatalf("post-terminal materialization was accepted: %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.CurrentState != BackgroundRunCompleted ||
		len(detail.Events) != 3 ||
		len(detail.SourceItems) != 1 ||
		len(detail.Materializations) != 1 {
		t.Fatalf("unexpected detail: %+v", detail)
	}
	if _, err := store.db.Exec(`
		DELETE FROM background_understanding_runs_v1
		WHERE id = ?`,
		run.ID,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("run deletion was not rejected: %v", err)
	}
}

type backgroundGmailFakeCaller struct {
	results map[string][]json.RawMessage
	errors  map[string]error
	calls   []struct {
		name           string
		args           map[string]any
		sourceLocators bool
	}
}

func backgroundGmailFakeResult(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func (fake *backgroundGmailFakeCaller) Call(
	_ context.Context,
	name string,
	args any,
) (*CuaDriverResponse, error) {
	return fake.call(name, args, false)
}

func (fake *backgroundGmailFakeCaller) CallWithSourceLocators(
	_ context.Context,
	name string,
	args any,
) (*CuaDriverResponse, error) {
	return fake.call(name, args, true)
}

func (fake *backgroundGmailFakeCaller) call(
	name string,
	args any,
	sourceLocators bool,
) (*CuaDriverResponse, error) {
	arguments, _ := args.(map[string]any)
	fake.calls = append(fake.calls, struct {
		name           string
		args           map[string]any
		sourceLocators bool
	}{name: name, args: arguments, sourceLocators: sourceLocators})
	if err := fake.errors[name]; err != nil {
		return nil, err
	}
	results := fake.results[name]
	result := backgroundGmailFakeResult(map[string]any{})
	if len(results) > 0 {
		result = results[0]
		fake.results[name] = results[1:]
	}
	return &CuaDriverResponse{OK: true, Result: result}, nil
}

func TestBackgroundGmailReaderUsesComputerUseWithoutOAuthFallback(
	t *testing.T,
) {
	fake := &backgroundGmailFakeCaller{
		results: map[string][]json.RawMessage{
			"list_apps": {backgroundGmailFakeResult(map[string]any{
				"apps": []any{map[string]any{
					"bundle":    "com.apple.Safari",
					"name":      "Safari",
					"pid":       101,
					"running":   true,
					"use_count": 40,
					"windows":   []any{map[string]any{"title": "Inbox - Gmail"}},
				}},
			})},
			"launch_app": {backgroundGmailFakeResult(map[string]any{
				"app": map[string]any{"bundle": "com.apple.Safari", "name": "Safari", "pid": 101},
			})},
			"open_url": {backgroundGmailFakeResult(map[string]any{"opened": "gmail"})},
			"list_windows": {backgroundGmailFakeResult(map[string]any{
				"windows": []any{map[string]any{"id": 55, "title": "Search - Gmail", "focused": true}},
			})},
			"get_window_state": {backgroundGmailFakeResult(map[string]any{
				"snapshot_id": "snap-1",
				"ax_status":   "populated",
				"app":         map[string]any{"bundle": "com.apple.Safari", "name": "Safari", "pid": 101},
				"window":      map[string]any{"id": 55, "title": "Search - Gmail"},
				// The browser's tab strip and a find bar over the page are
				// the window's; only the page under its web area is read.
				"ui_elements": []any{
					map[string]any{"role": "AXWindow", "title": "Search - Gmail", "bounds": map[string]any{"x": 0, "y": 0, "w": 1200, "h": 900}},
					map[string]any{"role": "AXRadioButton", "title": "Chase - Account balance overview", "parent_index": 0, "bounds": map[string]any{"x": 10, "y": 30, "w": 220, "h": 30}},
					map[string]any{"role": "AXWebArea", "title": "Search - Gmail", "parent_index": 0, "bounds": map[string]any{"x": 0, "y": 80, "w": 1200, "h": 820}},
					map[string]any{"role": "AXTextField", "title": "Search mail", "value": "after:2026/07/16 -category:promotions", "parent_index": 2, "bounds": map[string]any{"x": 200, "y": 100, "w": 600, "h": 30}},
					map[string]any{"role": "AXStaticText", "title": "person@example.com", "parent_index": 2, "bounds": map[string]any{"x": 200, "y": 200, "w": 140, "h": 10}},
					map[string]any{"role": "AXStaticText", "value": "Launch plan Can you review the launch plan?", "parent_index": 2, "bounds": map[string]any{"x": 350, "y": 201, "w": 400, "h": 10}},
					map[string]any{"role": "AXStaticText", "title": "Bo <bo@example.com>", "parent_index": 2, "bounds": map[string]any{"x": 200, "y": 230, "w": 140, "h": 10}},
					map[string]any{"role": "AXStaticText", "value": "Re: Launch plan I will send the draft tomorrow.", "parent_index": 2, "bounds": map[string]any{"x": 350, "y": 231, "w": 400, "h": 10}},
					map[string]any{"role": "AXTextField", "title": "Find in page", "value": "Chase statement for October", "parent_index": 0, "bounds": map[string]any{"x": 800, "y": 300, "w": 300, "h": 30}},
				},
			})},
			"press_key": {backgroundGmailFakeResult(map[string]any{"pressed": true})},
		},
	}
	authorized := []string{}
	reader := newBackgroundGmailReader(
		func() (backgroundComputerUseCaller, error) {
			return fake, nil
		},
		func(_ context.Context, runID string, _ BackgroundSourceGrant, tool string) error {
			if runID != "run-gmail-reader" {
				t.Fatalf("authority run id = %q", runID)
			}
			authorized = append(authorized, tool)
			return nil
		},
	)
	reader.wait = func(context.Context, time.Duration) error { return nil }
	grant := BackgroundSourceGrant{
		ID:                "grant-1",
		Source:            BackgroundSourceGmail,
		ScopeLabel:        "Recent correspondence",
		SourceQuery:       "-category:promotions",
		LookbackDays:      14,
		MaxItemsPerRun:    10,
		AllowedOperations: []string{BackgroundGmailComputerUseRead},
		Status:            "active",
	}
	batch, err := reader.Read(
		context.Background(),
		"run-gmail-reader",
		grant,
		"bo@example.com",
		time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"list_apps", "launch_app", "open_url", "list_windows", "get_window_state", "press_key"}
	if len(fake.calls) != len(wantCalls) {
		t.Fatalf("calls=%+v, want %v", fake.calls, wantCalls)
	}
	if len(authorized) != len(wantCalls) {
		t.Fatalf("authority=%+v, want %v", authorized, wantCalls)
	}
	for index, want := range wantCalls {
		if fake.calls[index].name != want {
			t.Fatalf("call[%d]=%s, want %s", index, fake.calls[index].name, want)
		}
		if authorized[index] != want {
			t.Fatalf("authority[%d]=%s, want %s", index, authorized[index], want)
		}
	}
	if got := fake.calls[2].args["url"].(string); !strings.Contains(got, "after%3A2026%2F07%2F16") {
		t.Fatalf("unexpected bounded Gmail URL: %q", got)
	}
	if got := fake.calls[2].args["url"].(string); !strings.Contains(got, "/u/bo@example.com/") {
		t.Fatalf("Gmail URL did not preserve the granted mailbox: %q", got)
	}
	if fake.calls[4].args["include_screenshot"] != false ||
		fake.calls[4].args["interactive_only"] != false {
		t.Fatalf("snapshot crossed AX-only boundary: %+v", fake.calls[4].args)
	}
	if fake.calls[4].sourceLocators {
		t.Fatal("ordinary Background Understanding widened its snapshot to source locators")
	}
	if len(batch.Items) != 2 || batch.ReceiptHash == "" ||
		batch.Transport != backgroundGmailTransport || batch.SnapshotHash == "" {
		t.Fatalf("unexpected batch: %+v", batch)
	}
	if batch.CleanupOutcome != "completed" {
		t.Fatalf("confirmed target was not cleaned up: %+v", batch)
	}
	if batch.Items[0].AttributionKind != "other" &&
		batch.Items[1].AttributionKind != "other" {
		t.Fatalf("expected one OTHER observation: %+v", batch.Items)
	}
	if batch.Items[0].AttributionKind != "self" &&
		batch.Items[1].AttributionKind != "self" {
		t.Fatalf("expected one SELF observation: %+v", batch.Items)
	}
	summary := backgroundGmailSummary(batch, batch.Items)
	if strings.Contains(summary, "Chase") {
		t.Fatalf("the tab strip or a find bar became a Gmail row: %s", summary)
	}
	if !strings.Contains(summary, "[SELF]") ||
		!strings.Contains(summary, "[OTHER]") ||
		!strings.Contains(summary, "no OAuth") {
		t.Fatalf("unsafe or unattributed summary: %s", summary)
	}
}

func TestBackgroundGmailReadsOnlyTheGmailPage(t *testing.T) {
	snapshot := func(page string) json.RawMessage {
		return backgroundGmailFakeResult(map[string]any{
			"window": map[string]any{"id": 55, "title": "Search - Gmail"},
			"ui_elements": []any{
				map[string]any{"role": "AXWindow", "title": "Search - Gmail"},
				map[string]any{"role": "AXRadioButton", "title": "Chase - Account balance overview", "parent_index": 0},
				map[string]any{"role": "AXWebArea", "url": page, "parent_index": 0},
				map[string]any{"role": "AXStaticText", "value": "Launch plan Can you review the launch plan?", "parent_index": 2},
			},
		})
	}
	got, err := backgroundGmailPageSnapshot(snapshot("https://mail.google.com/mail/u/0/#search/x"), true)
	if err != nil || len(got.Elements) != 2 || got.Elements[0].Role != "AXWebArea" {
		t.Fatalf("Gmail page: %+v %v", got.Elements, err)
	}
	if _, err := backgroundGmailPageSnapshot(snapshot("https://onlinebanking.example.com/"), true); err == nil {
		t.Fatal("with addresses, a page other than Gmail must not be read")
	}
	if _, err := backgroundGmailPageSnapshot(backgroundGmailFakeResult(map[string]any{
		"ui_elements": []any{map[string]any{"role": "AXRadioButton", "title": "Chase - Account balance overview"}},
	}), false); err == nil {
		t.Fatal("a window without a web page has no rows to read")
	}
}

func TestBackgroundGmailSnapshotRequiresExactGrantedQuery(t *testing.T) {
	snapshot := backgroundCUASnapshot{
		Window: backgroundCUAWindow{ID: 1, Title: "Search - Gmail"},
		Elements: []backgroundCUAElement{
			{Role: "AXTextField", Title: "Search mail", Value: "after:2026/07/16 label:work"},
		},
	}
	if !backgroundGmailSnapshotMatchesQuery(snapshot, "after:2026/07/16 label:work") {
		t.Fatal("exact granted Gmail query was not recognized")
	}
	if backgroundGmailSnapshotMatchesQuery(snapshot, "after:2026/07/16 -category:promotions") {
		t.Fatal("a different Gmail query crossed the frozen source scope")
	}
	if got := backgroundSelectGmailWindow([]backgroundCUAWindow{{ID: 8, Title: "Private project - Safari"}}); got.ID != 0 {
		t.Fatalf("non-Gmail window was selected: %+v", got)
	}
}

func TestBackgroundGmailFailureKeepsBoundedComputerUseReceipt(t *testing.T) {
	failure := &backgroundGmailMissionError{
		err: errors.New("target query was not confirmed"),
		batch: backgroundGmailBatch{
			Transport:      backgroundGmailTransport,
			BrowserBundle:  "com.apple.Safari",
			BrowserName:    "Safari",
			BrowserPID:     101,
			WindowID:       55,
			SnapshotID:     "snap-1",
			SnapshotHash:   "snapshot-hash",
			AXStatus:       "populated",
			CleanupOutcome: "not_attempted_unconfirmed_target",
			Actions: []backgroundGmailActionReceipt{
				{Tool: "get_window_state", Outcome: "completed", DetailHash: "detail-hash"},
			},
		},
	}
	details := failure.BackgroundRunFailureDetails()
	if details["oauthFallback"] != false ||
		details["snapshotHash"] != "snapshot-hash" ||
		details["cleanup"] != "not_attempted_unconfirmed_target" {
		t.Fatalf("unsafe or incomplete failure receipt: %+v", details)
	}
	if strings.Contains(backgroundJSON(details, "{}"), "target query was not confirmed") {
		t.Fatal("failure receipt should not duplicate unbounded error text")
	}
}

func TestBackgroundGmailReaderRejectsBroaderGrant(
	t *testing.T,
) {
	fake := &backgroundGmailFakeCaller{}
	authorized := false
	reader := newBackgroundGmailReader(
		func() (backgroundComputerUseCaller, error) {
			return fake, nil
		},
		func(context.Context, string, BackgroundSourceGrant, string) error {
			authorized = true
			return nil
		},
	)
	_, err := reader.Read(
		context.Background(),
		"run-broader-grant",
		BackgroundSourceGrant{
			Source: BackgroundSourceGmail,
			Status: "active",
			AllowedOperations: []string{
				BackgroundGmailComputerUseRead,
				"gmail.message.send",
			},
		},
		"",
		time.Now(),
	)
	if err == nil ||
		!strings.Contains(err.Error(), "metadata-only policy") ||
		len(fake.calls) != 0 || authorized {
		t.Fatalf("broader grant was not rejected before a call: %v", err)
	}
}

func TestBackgroundGmailComputerUseActionsPassAuthorityGateway(t *testing.T) {
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	grant := BackgroundSourceGrant{
		ID:                "grant-authority",
		Source:            BackgroundSourceGmail,
		AllowedOperations: []string{BackgroundGmailComputerUseRead},
		PolicyVersion:     BackgroundUnderstandingPolicyVersion,
		Status:            "active",
	}
	tools := []string{
		"list_apps",
		"launch_app",
		"open_url",
		"list_windows",
		"get_window_state",
		"press_key",
	}
	for _, tool := range tools {
		if err := app.authorizeBackgroundGmailComputerUseAction(
			context.Background(),
			"run-gmail-authority",
			grant,
			tool,
		); err != nil {
			t.Fatalf("authorize %s: %v", tool, err)
		}
	}
	records, err := store.ListAuthorityDecisions("", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(tools) {
		t.Fatalf("authority decisions=%d, want %d", len(records), len(tools))
	}
	for _, record := range records {
		if record.Evaluation.Context.RunID != "run-gmail-authority" ||
			record.Evaluation.GrantID == "" ||
			record.Evaluation.Action.Resource != "background-source:gmail:grant-authority" {
			t.Fatalf("unscoped Gmail authority decision: %+v", record.Evaluation)
		}
	}
	if err := app.authorizeBackgroundGmailComputerUseAction(
		context.Background(),
		"run-gmail-authority",
		grant,
		"click",
	); err == nil {
		t.Fatal("operation outside the frozen Gmail compiler was authorized")
	}
}

func TestBackgroundShadowFallbackRecoversFromRecentOpenEpisode(t *testing.T) {
	now := time.Date(2026, 7, 31, 13, 0, 0, 0, time.Local)
	episode := TaskEpisode{
		ID:              "recent-insufficient-transform",
		InferredStartAt: now.Add(-time.Minute).UnixMilli(),
		CurrentStatus:   TaskEpisodeStatusActive,
		LatestHypothesis: TaskEpisodeHypothesisRevision{
			TaskHypothesis:          "Convert email content into a one-page document",
			DesiredResultHypothesis: "A one-page version of the email",
		},
	}
	reason := backgroundShadowFallbackReasonForEpisodes(
		[]TaskEpisode{episode},
		now,
	)
	if !strings.Contains(reason, "frozen onset context") {
		t.Fatalf("recovered reason=%q", reason)
	}

	outsideRollout := episode
	outsideRollout.InferredStartAt = now.Add(-30 * time.Second).UnixMilli()
	outsideRollout.LatestHypothesis.TaskHypothesis =
		"Inspect the latest deployment metrics"
	if reason := backgroundShadowFallbackReasonForEpisodes(
		[]TaskEpisode{outsideRollout, episode},
		now,
	); !strings.Contains(reason, "frozen onset context") {
		t.Fatalf("specific source-context blockage lost priority: %q", reason)
	}

	ready := episode
	ready.LatestHypothesis.TaskHypothesis = "Draft a new customer update"
	if reason := backgroundShadowFallbackReasonForEpisodes(
		[]TaskEpisode{ready, episode},
		now,
	); reason != "" {
		t.Fatalf("newest ready shadow work did not retain priority: %q", reason)
	}

	stale := episode
	stale.InferredStartAt = now.Add(
		-backgroundShadowFallbackLookback - time.Second,
	).UnixMilli()
	if reason := backgroundShadowFallbackReasonForEpisodes(
		[]TaskEpisode{stale},
		now,
	); reason != "" {
		t.Fatalf("stale episode restored fallback: %q", reason)
	}
}
