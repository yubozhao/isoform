package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

type workProfileTestUploader struct {
	calls int
	input WorkProfileUpload
	err   error
}

func (u *workProfileTestUploader) UploadApprovedProfile(_ context.Context, input WorkProfileUpload) (WorkProfileUploadResult, error) {
	u.calls++
	u.input = input
	return WorkProfileUploadResult{Version: 2, ApprovedAt: "2026-11-09T12:00:00Z"}, u.err
}

func TestWorkProfileApproveSendsPreviewAndKeepsDraftOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "work-profile.json")
	state := workProfileState{Format: 1, ApprovedVersion: 1, Pending: &WorkProfileDraft{
		BasedOnVersion: 1,
		LearnedThrough: "2026-11-08",
		Specialties:    []WorkProfileSpecialty{{ID: "g", Name: "Client SOWs", Selected: true}},
		Lines:          []WorkProfileLine{{ID: "l", Group: "g", Text: "You write clearly."}, {ID: "removed", Text: "Not sent.", Removed: true}},
	}}
	if err := workProfileWriteState(path, state); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	preview, err := workProfilePreview(state, "Mina", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	uploader := &workProfileTestUploader{err: errors.New("offline")}
	if _, err := workProfileApprove(context.Background(), path, uploader, "Mina", nil, now); err == nil {
		t.Fatal("failed upload was approved")
	}
	kept, err := workProfileReadState(path)
	if err != nil || kept.Pending == nil || kept.ApprovedVersion != 1 {
		t.Fatalf("failed upload changed state: %+v, %v", kept, err)
	}
	uploader.err = nil
	approved, err := workProfileApprove(context.Background(), path, uploader, "Mina", nil, now)
	want, _ := json.Marshal(preview)
	got, _ := json.Marshal(uploader.input)
	if err != nil || string(got) != string(want) || approved.ApprovedVersion != 2 || approved.Pending != nil || len(approved.Approved.Lines) != 1 {
		t.Fatalf("approval: %s, want %s, state %+v, error %v", got, want, approved, err)
	}
}

func TestWorkProfileApproveRejectsPrivateEditBeforeUpload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "work-profile.json")
	state := workProfileState{Format: 1, Pending: &WorkProfileDraft{Lines: []WorkProfileLine{{ID: "l", Text: "Mina wrote this."}}}}
	if err := workProfileWriteState(path, state); err != nil {
		t.Fatal(err)
	}
	uploader := &workProfileTestUploader{}
	if _, err := workProfileApprove(context.Background(), path, uploader, "Mina", nil, time.Now()); err == nil || uploader.calls != 0 {
		t.Fatal("unfiltered draft reached uploader")
	}
}

func TestWorkProfileSuggestedRemovalCanBeKept(t *testing.T) {
	state := workProfileState{Format: 1, Pending: &WorkProfileDraft{Lines: []WorkProfileLine{
		{ID: "current", Text: "You write clearly."},
		{ID: "old", Text: "You check the wording.", Status: "suggested_removal"},
	}}}
	preview, err := workProfilePreview(state, "", nil, time.Now())
	if err != nil || len(preview.Lines) != 1 {
		t.Fatalf("suggested removal included: %+v, %v", preview, err)
	}
	if err := workProfileKeepLine(&state, "old"); err != nil {
		t.Fatal(err)
	}
	preview, err = workProfilePreview(state, "", nil, time.Now())
	if err != nil || len(preview.Lines) != 2 {
		t.Fatalf("kept line missing: %+v, %v", preview, err)
	}
}

func TestWorkProfileOwnerUploadConflictAndExactPreview(t *testing.T) {
	var sent WorkProfileUpload
	conflict := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.URL.Path != "/v1/owner/profile" || r.Header.Get("X-Isoform-Hub") != "test-hub" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("wrong upload endpoint or authentication")
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		if conflict {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"profile_version_conflict","currentVersion":4}`))
			return
		}
		_, _ = w.Write([]byte(`{"version":2,"approvedAt":"2026-11-09T12:00:00Z"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "identity.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "test-hub", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json")}
	waitAgentOwnerRefreshes(t, env.cachePath)
	path := filepath.Join(dir, "profile.json")
	state := workProfileState{Format: 1, ApprovedVersion: 1, Pending: &WorkProfileDraft{BasedOnVersion: 1, LearnedThrough: "2026-11-08", Specialties: []WorkProfileSpecialty{{ID: "s", Name: "Project proposals", Selected: true}}, Lines: []WorkProfileLine{{ID: "l", Group: "s", Text: "You lead with the price."}}}}
	if err := workProfileWriteState(path, state); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	preview, err := workProfilePreview(state, "", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = workProfileApprove(context.Background(), path, agentOwnerProfileUploader{env}, "", nil, now)
	var refusal *agentServerError
	if !errors.As(err, &refusal) || refusal.Status != http.StatusConflict || refusal.CurrentVersion == nil || *refusal.CurrentVersion != 4 {
		t.Fatalf("conflict: %v", err)
	}
	kept, err := workProfileReadState(path)
	if err != nil || kept.ApprovedVersion != 1 || kept.Pending == nil {
		t.Fatal("conflict overwrote local draft")
	}
	want, _ := json.Marshal(preview)
	got, _ := json.Marshal(sent)
	if string(got) != string(want) {
		t.Fatalf("sent %s, preview %s", got, want)
	}
	conflict = false
	approved, err := workProfileApprove(context.Background(), path, agentOwnerProfileUploader{env}, "", nil, now)
	if err != nil || approved.ApprovedVersion != 2 || approved.Pending != nil {
		t.Fatalf("approval: %+v %v", approved, err)
	}
}

func TestWorkProfileApproveRequiresReviewedPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	state := workProfileState{Format: 1, Pending: &WorkProfileDraft{ID: "draft", Lines: []WorkProfileLine{{ID: "line", Text: "You write clearly."}}}}
	if err := workProfileWriteState(path, state); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	preview, err := workProfilePreview(state, "", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	state.Pending.Lines[0].Text = "You write concisely."
	if err := workProfileWriteState(path, state); err != nil {
		t.Fatal(err)
	}
	uploader := &workProfileTestUploader{}
	if _, err := workProfileApproveDraft(context.Background(), path, uploader, "draft", &preview, "", nil, now); err == nil || uploader.calls != 0 {
		t.Fatal("changed preview was sent")
	}
	if _, err := workProfileApproveDraft(context.Background(), path, uploader, "older", &preview, "", nil, now); err == nil || uploader.calls != 0 {
		t.Fatal("stale draft was sent")
	}
}

func TestWorkProfileDraftLeakCheckCoversPreviousAndSpecialty(t *testing.T) {
	for _, draft := range []*WorkProfileDraft{
		{Lines: []WorkProfileLine{{Text: "You write clearly.", Previous: "Mina wrote this."}}},
		{Specialties: []WorkProfileSpecialty{{ID: "s", Name: "Mina projects"}}, Lines: []WorkProfileLine{{Text: "You write clearly."}}},
	} {
		if err := workProfileCheckDraftPrivacy(draft, "Mina", nil); err == nil {
			t.Fatal("private draft text passed")
		}
		if _, err := workProfilePreview(workProfileState{Pending: draft}, "Mina", nil, time.Now()); err == nil {
			t.Fatal("private draft passed preview")
		}
	}
}
