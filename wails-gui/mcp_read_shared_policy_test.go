package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/sop"
	"wails-gui/storage"
)

// Dictation can name an additional source app even when the foreground app and
// title are ordinary. This source fact belongs to the shared chat privacy gate,
// and every MCP tool that follows capture provenance must honor that gate.
func TestMCPReadToolsUseSharedChatSourcePolicy(t *testing.T) {
	isolateActivityPrivacyTest(t)
	a := newMCPReadTestApp(t)
	now := time.Now()
	calendarAt := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	for _, kind := range []string{"safe", "private"} {
		id := kind + "-source"
		at := seedMCPReadActivity(t, a, id, "Editor", "com.example.editor", "project evidence", "project evidence", "done")
		if kind == "private" {
			if _, err := a.activity.store.db.Exec(`UPDATE activity_events SET data=json_set(data,'$.speechDictationApp','1Password') WHERE session_id=?`, id); err != nil {
				t.Fatal(err)
			}
		}
		row, err := a.activity.store.GetSessionByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if allowed := a.activitySessionAllowsModelRead(a.activity.store, *row); allowed != (kind == "safe") {
			t.Fatalf("shared chat gate did not establish fixture policy for %s: %v", kind, allowed)
		}
		writeMCPReadMemory(t, kind+"-memory", id, "project evidence")
		person, err := a.store.CreatePrincipal(a.ctx, storage.PrincipalRow{ID: kind + "-person", Kind: storage.PrincipalKindContact, DisplayName: "project " + kind, CreatedFrom: "activity"})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.store.BumpPrincipalSighting(a.ctx, person.ID, "Editor", id, at, ""); err != nil {
			t.Fatal(err)
		}
		if err := a.store.UpsertCalendarEvents(a.ctx, []storage.CalendarEventRow{{ID: kind + "-calendar", Source: "web", SessionID: id, Title: "project " + kind, StartAt: calendarAt.UnixMilli(), EndAt: calendarAt.Add(time.Hour).UnixMilli()}}, at); err != nil {
			t.Fatal(err)
		}
		if _, err := a.sopStore.Save(sop.Entry{ID: kind + "-playbook", Title: "project " + kind, Source: sop.SourceTriage, SourceIDs: []string{id}, Status: sop.StatusConfirmed, Body: "approved steps"}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.playbookPlans.Update(kind+"-playbook", func(p *sop.Plan) { p.BodyHash = "approved"; p.Schedule = &sop.Schedule{Enabled: true} }); err != nil {
			t.Fatal(err)
		}
	}
	storeMCPBriefReferences(t, MobileBriefRef{SourceKind: "memory", SourceID: "safe-memory"}, MobileBriefRef{SourceKind: "memory", SourceID: "private-memory"})
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"activity_search", map[string]any{"query": "project"}, "safe-source"},
		{"memory_search", map[string]any{"query": "project"}, "safe-memory"},
		{"memory_list", map[string]any{}, "safe-memory"},
		{"people_lookup", map[string]any{"name": "project"}, "safe-person"},
		{"calendar_today", map[string]any{}, "safe-calendar"},
		{"calendar_range", map[string]any{"from": now.Format("2006-01-02"), "to": now.Format("2006-01-02")}, "safe-calendar"},
		{"brief_today", map[string]any{}, "safe-memory"},
		{"playbook_status", map[string]any{}, "safe-playbook"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			got := callMCPReadTest(t, a, tc.tool, tc.args)
			if len(got.Items) != 1 || got.Items[0].ID.Text != tc.want {
				t.Fatalf("MCP bypassed shared source policy or lost safe control: %+v; want %s", got, tc.want)
			}
		})
	}
}

func TestMCPReadRunHistoryUsesSharedTimePolicyAndOnlyMetadata(t *testing.T) {
	isolateActivityPrivacyTest(t)
	a := newMCPReadTestApp(t)
	at := time.Now().Add(-time.Hour).UnixMilli()
	if err := a.store.SaveCronJob(a.ctx, storage.CronJobRow{ID: "job", Name: "private captured name", ScheduleKind: "manual", ScheduleConfig: "{}", TargetKind: "agent", TargetConfig: "{}", RetryPolicy: "{}"}); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"safe-run", "private-run"} {
		start := at + int64(index)*60000
		if err := a.store.InsertCronRun(a.ctx, storage.CronRunRow{ID: id, JobID: "job", StartedAt: start, FinishedAt: start + 1000, Status: "ok", ResultSummary: "private captured summary", Error: "private captured error"}); err != nil {
			t.Fatal(err)
		}
	}
	pauses, err := json.Marshal([]observationPause{{Start: at + 59999, End: at + 62000}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetUIState(a.ctx, observationPausesKey, string(pauses)); err != nil {
		t.Fatal(err)
	}
	policy, err := a.activityReadPrivacyPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.allows(activitySourceProvenance{StartTime: at + 60000, EndTime: at + 61000}) || !policy.allows(activitySourceProvenance{StartTime: at, EndTime: at + 1000}) {
		t.Fatal("shared time policy did not establish fixture exclusion")
	}
	got := callMCPReadTest(t, a, "run_history", map[string]any{})
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].ID.Text != "safe-run" || strings.Contains(string(raw), "private captured") {
		t.Fatalf("run metadata bypassed shared time policy or exposed captured text: %s", raw)
	}
}

func TestMCPReadMemoryUsesSharedCorrectionSuppression(t *testing.T) {
	isolateActivityPrivacyTest(t)
	a := newMCPReadTestApp(t)
	seedMCPReadActivity(t, a, "safe", "Editor", "com.example.editor", "project", "", "done")
	writeMCPReadMemory(t, "safe-memory", "safe", "project evidence")
	writeMCPReadMemory(t, "corrected-memory", "safe", "project superseded inference")
	seedLegacyLearningRun(t, a.activity.store, "run:one", []legacyLearningCandidate{{RevisionID: "revision:one", ProjectionKey: "projection:one", TargetKind: correctedLearningTargetMemory, Statement: "superseded inference"}}, 1000)
	seedLegacyLearningMaterialization(t, a.activity.store, "revision:one", correctedLearningTargetMemory, "corrected-memory", 1100)
	addLearningCorrection(t, a.activity.store, AgencyLearningCorrectionRecord{IdempotencyKey: "mcp-shared-correction", CapabilityKey: "coding.review", LearningRevisionID: "revision:one", OriginalStatement: "superseded inference", Correction: "Do not use this inference", CreatedAt: 1200})
	storeMCPBriefReferences(t, MobileBriefRef{SourceKind: "memory", SourceID: "safe-memory"}, MobileBriefRef{SourceKind: "memory", SourceID: "corrected-memory"})
	for _, name := range []string{"memory_list", "memory_search", "brief_today"} {
		t.Run(name, func(t *testing.T) {
			got := callMCPReadTest(t, a, name, map[string]any{"query": "project"})
			if len(got.Items) != 1 || got.Items[0].ID.Text != "safe-memory" {
				t.Fatalf("shared correction filter bypassed or safe control lost: %+v", got)
			}
		})
	}
}
