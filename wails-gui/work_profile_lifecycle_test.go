package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkProfileDraftLimitAndEditedPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "work-profile.json")
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := []KnowledgeEntry{
		parseKnowledgeFile("one.md", workProfileTestNote("one", "Client updates", "You write clear updates.", first, "activity", []string{"Editor"}, []string{"a", "b", "c"}, nil)),
		parseKnowledgeFile("two.md", workProfileTestNote("two", "Client updates", "You lead with decisions.", first.Add(15*24*time.Hour), "activity", []string{"Editor"}, []string{"d", "e"}, nil)),
	}
	initial := workProfileState{Format: 1, Pending: &WorkProfileDraft{ID: "edited", Edited: true}}
	if err := workProfileWriteState(path, initial); err != nil {
		t.Fatal(err)
	}
	model := &workProfileFakeModel{output: `{"lines":[{"text":"You lead with decisions.","from":["c2"]}],"publicSpecialties":[]}`}
	fingerprint := func() (string, error) { return "changed", nil }
	now := first.Add(20 * 24 * time.Hour)
	state, err := workProfileRunDraft(context.Background(), path, workProfileTestSources{entries}, model, fingerprint, "", nil, now, true)
	if err != nil || state.Pending.ID != "edited" || state.Ready == nil || len(model.prompts) != 1 {
		t.Fatalf("edited draft replaced: %+v, %v", state, err)
	}
	if _, err := workProfileRunDraft(context.Background(), path, workProfileTestSources{entries}, model, fingerprint, "", nil, now.Add(23*time.Hour), true); err == nil || len(model.prompts) != 1 {
		t.Fatal("manual rate limit failed")
	}
	if _, err := workProfileRunDraft(context.Background(), path, workProfileTestSources{entries}, model, fingerprint, "", nil, now.Add(24*time.Hour), false); err != nil || len(model.prompts) != 1 {
		t.Fatalf("unchanged weekly slot made a call: %v", err)
	}
}

func TestWorkProfileQueuedDraftRebuildsOnCurrentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := []KnowledgeEntry{
		parseKnowledgeFile("one.md", workProfileTestNote("one", "Client updates", "You write clear updates.", first, "activity", []string{"Editor"}, []string{"a", "b", "c"}, nil)),
		parseKnowledgeFile("two.md", workProfileTestNote("two", "Client updates", "You lead with decisions.", first.Add(15*24*time.Hour), "activity", []string{"Editor"}, []string{"d", "e"}, nil)),
	}
	state := workProfileState{Format: 1, ApprovedVersion: 2, Approved: &WorkProfileDraft{ID: "approved"}, Ready: &WorkProfileDraft{ID: "old", BasedOnVersion: 1}, LastFingerprint: "same"}
	if err := workProfileWriteState(path, state); err != nil {
		t.Fatal(err)
	}
	model := &workProfileFakeModel{output: `{"lines":[{"text":"You lead with decisions.","from":["c2"]}],"publicSpecialties":[]}`}
	result, err := workProfileRunDraft(context.Background(), path, workProfileTestSources{entries}, model, func() (string, error) { return "same", nil }, "", nil, first.Add(20*24*time.Hour), false)
	if err != nil || result.Pending == nil || result.Pending.ID == "old" || result.Pending.BasedOnVersion != 2 || result.Ready != nil || len(model.prompts) != 1 {
		t.Fatalf("reconciliation: %+v, %v", result, err)
	}
}

func TestWorkProfileWeeklySlotLocalTime(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ now, want string }{
		{"2026-11-09T08:59:00-08:00", "2026-11-02T09:00:00-08:00"},
		{"2026-11-09T09:00:00-08:00", "2026-11-09T09:00:00-08:00"},
		{"2026-03-09T10:00:00-07:00", "2026-03-09T09:00:00-07:00"},
	} {
		now, err := time.Parse(time.RFC3339, tc.now)
		if err != nil {
			t.Fatal(err)
		}
		if got := workProfileWeeklySlot(now.In(loc)).Format(time.RFC3339); got != tc.want {
			t.Fatalf("slot %s: got %s want %s", tc.now, got, tc.want)
		}
	}
}

func TestAgentOwnerWorkProfileNeedCounts(t *testing.T) {
	state := workProfileState{ApprovedVersion: 1, Pending: &WorkProfileDraft{ID: "draft", Lines: []WorkProfileLine{{Status: "new"}, {Status: "changed"}, {Status: "new", Removed: true}}}}
	needs := agentOwnerAddWorkProfileNeed(nil, state)
	if len(needs) != 1 || needs[0].ID != "draft" || needs[0].NewCount != 1 || needs[0].ChangedCount != 1 {
		t.Fatalf("needs: %+v", needs)
	}
	state.ApprovedVersion = 0
	if len(agentOwnerAddWorkProfileNeed(nil, state)) != 0 {
		t.Fatal("first setup draft became a weekly need")
	}
}

func TestWorkProfileScheduleChecksAtStartupAndWake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	kick := make(chan struct{}, 1)
	checked := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		runWorkProfileSchedule(ctx, wake, kick, func() time.Time { return time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC) }, func() { checked <- struct{}{} })
		close(done)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-checked:
		case <-time.After(time.Second):
			t.Fatal("schedule did not check")
		}
		if i == 0 {
			wake <- struct{}{}
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("schedule did not stop")
	}
}

func TestWorkProfileViewReportsActiveBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	workProfileBuilds.Store(path, true)
	defer workProfileBuilds.Delete(path)
	if !workProfileViewAtPath(workProfileState{}, path).Building {
		t.Fatal("active build missing from view")
	}
	workProfileBuilds.Delete(path)
	if workProfileViewAtPath(workProfileState{}, path).Building {
		t.Fatal("finished build still shown")
	}
}
