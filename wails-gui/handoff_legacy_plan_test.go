package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

type handoffTestRig struct {
	app      *App
	activity *ActivityStore
	state    *storage.Store
	handoffs *handoff.Store
}

// newHandoffTestRig is an app with the activity store, the session store and
// the handoff store that starting one-time work needs.
func newHandoffTestRig(t *testing.T) *handoffTestRig {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	activity, err := NewActivityStore(filepath.Join(root, "activity"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := storage.New(filepath.Join(root, "state.db"))
	if err != nil {
		activity.Close()
		t.Fatal(err)
	}
	handoffs, err := handoff.NewStore(filepath.Join(root, "handoffs"))
	if err != nil {
		state.Close()
		activity.Close()
		t.Fatal(err)
	}
	app := &App{store: state, handoffStore: handoffs, activity: &activityMonitorState{store: activity}}
	t.Cleanup(func() {
		// A Today read refreshes its signals in the background; let it land
		// before the stores close and the folders are removed.
		if svc, ok := app.mobileBriefSvc.(*mobileBriefService); ok && svc != nil {
			svc.todayRefreshWG.Wait()
		}
		if err := state.Close(); err != nil {
			t.Errorf("close state store: %v", err)
		}
		if err := activity.Close(); err != nil {
			t.Errorf("close activity store: %v", err)
		}
	})
	return &handoffTestRig{
		app:      app,
		activity: activity,
		state:    state,
		handoffs: handoffs,
	}
}

// agencyHandoffTestPlan is the plan an Agency handoff in the tests carries.
type agencyHandoffTestPlan struct {
	Purpose        string
	DesiredOutcome string
	Summary        string
	Includes       []string
	Excludes       []string
	Rationale      string
}

type agencyHandoffTestRig struct {
	*handoffTestRig
	entry handoff.Entry
	plan  agencyHandoffTestPlan
}

// newAgencyHandoffTestRig adds one-time work the Agency wrote, with the
// permission request its plan is read from, the way the Agency stored both
// before it was removed.
func newAgencyHandoffTestRig(t *testing.T) *agencyHandoffTestRig {
	t.Helper()
	rig := &agencyHandoffTestRig{
		handoffTestRig: newHandoffTestRig(t),
		plan: agencyHandoffTestPlan{
			Purpose:        "Help finish the release brief already in progress",
			DesiredOutcome: "A customer release brief is ready for review",
			Summary:        "Finish only the current local release brief.",
			Includes:       []string{"Prepare the local release brief"},
			Excludes:       []string{"Send or publish the brief"},
			Rationale:      "The brief is half written and the release is tomorrow.",
		},
	}
	now := time.Now().UnixMilli()
	rig.entry = handoff.Entry{
		ID:        "agency-handoff-1",
		Title:     "Finish the customer release brief",
		Status:    handoff.StatusActive,
		Source:    handoff.SourceAgency,
		SourceID:  "origination-run-1",
		Body:      "Complete this bounded Yansu-originated work.\n\nRequired evidence references: current-work:1",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := rig.handoffs.Save(rig.entry); err != nil {
		t.Fatal(err)
	}
	storeAgencyHandoffPlanRequest(t, rig.activity, rig.entry, rig.plan)
	return rig
}

// storeAgencyHandoffPlanRequest writes the permission request row for entry.
// The request's origination run and candidate rows are not needed to read the
// plan, so foreign keys are off for the insert.
func storeAgencyHandoffPlanRequest(
	t *testing.T,
	activity *ActivityStore,
	entry handoff.Entry,
	plan agencyHandoffTestPlan,
) {
	t.Helper()
	request := map[string]any{
		"contextHash": "context-hash-1",
		"candidate": map[string]any{
			"kind":           "handoff",
			"name":           entry.Title,
			"purpose":        plan.Purpose,
			"desiredOutcome": plan.DesiredOutcome,
			"scope": map[string]any{
				"summary":  plan.Summary,
				"includes": plan.Includes,
				"excludes": plan.Excludes,
			},
			"rationale":             plan.Rationale,
			"reversibility":         "reversible",
			"risk":                  map[string]any{"level": "low", "summary": "Local edits only."},
			"expectedCost":          map[string]any{"effortMinutes": 10},
			"authorityCapabilities": []string{"tool.read", "tool.edit"},
		},
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	payloadJSON, payloadHash, err := agencyStructPayload(map[string]string{"requestId": entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activity.db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := activity.db.Exec("PRAGMA foreign_keys=ON"); err != nil {
			t.Fatal(err)
		}
	}()
	if _, err := activity.db.Exec(`
		INSERT INTO agency_origination_permission_requests_v1 (
			id, idempotency_key, run_id, candidate_id, kind, permission_contract,
			recurrence, request_json, payload_json, payload_hash, created_at
		) VALUES (?, ?, ?, 'candidate-1', 'handoff', 'jit', '', ?, ?, ?, ?)`,
		entry.ID, "request:"+entry.ID, entry.SourceID, string(requestJSON),
		payloadJSON, payloadHash, entry.CreatedAt,
	); err != nil {
		t.Fatal(err)
	}
}

func TestAgencyHandoffPlanReadsTheStoredRequest(t *testing.T) {
	rig := newAgencyHandoffTestRig(t)
	plan := agencyHandoffPlan(rig.activity.db, rig.entry)
	if plan == nil {
		t.Fatal("the stored plan was not read")
	}
	if plan.Purpose != rig.plan.Purpose || plan.DesiredResult != rig.plan.DesiredOutcome ||
		plan.ScopeSummary != rig.plan.Summary || plan.WhyNow != rig.plan.Rationale ||
		!slices.Equal(plan.Includes, rig.plan.Includes) || !slices.Equal(plan.Excludes, rig.plan.Excludes) {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Risk != "Low risk" || plan.Reversibility != "Reversible plan" ||
		plan.ExpectedEffort != "About 10 minutes" ||
		!slices.Equal(plan.Access, []string{"Read local files and information", "Change local files"}) {
		t.Fatalf("plan labels = %+v", plan)
	}
}

func TestAgencyHandoffPlanFailsClosed(t *testing.T) {
	rig := newAgencyHandoffTestRig(t)
	for name, entry := range map[string]handoff.Entry{
		"another source run": func() handoff.Entry { e := rig.entry; e.SourceID = "other-run"; return e }(),
		"another title":      func() handoff.Entry { e := rig.entry; e.Title = "Something else"; return e }(),
		"no request":         func() handoff.Entry { e := rig.entry; e.ID = "agency-handoff-missing"; return e }(),
		"not Agency work":    func() handoff.Entry { e := rig.entry; e.Source = handoff.SourceTriage; return e }(),
	} {
		if plan := agencyHandoffPlan(rig.activity.db, entry); plan != nil {
			t.Fatalf("%s: plan = %+v", name, plan)
		}
	}
	if _, err := rig.activity.db.Exec(`DROP TRIGGER IF EXISTS agency_origination_permission_requests_v1_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.activity.db.Exec(`
		UPDATE agency_origination_permission_requests_v1
		SET payload_json = '{"requestId":"changed"}'
		WHERE id = ?`, rig.entry.ID); err != nil {
		t.Fatal(err)
	}
	if plan := agencyHandoffPlan(rig.activity.db, rig.entry); plan != nil {
		t.Fatalf("a damaged request still gave a plan: %+v", plan)
	}
}

func handoffTestTableCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
