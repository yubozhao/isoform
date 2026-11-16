package main

import (
	"context"
	"testing"
)

func TestForgetWorkProfileRemovesTheApprovedProfileAndDrafts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := workProfilePath()
	if err != nil {
		t.Fatal(err)
	}
	draft := &WorkProfileDraft{ID: "d1", Lines: []WorkProfileLine{{ID: "l1", Text: "Ships the Halcyon import"}}}
	if err := workProfileWriteState(path, workProfileState{Format: 1, ApprovedVersion: 3, Approved: draft, Pending: draft, RemovedFingerprints: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if err := app.forgetWorkProfile(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := workProfileReadState(path)
	if err != nil || state.Approved != nil || state.Pending != nil || state.ApprovedVersion != 0 || len(state.RemovedFingerprints) != 0 {
		t.Fatalf("state after forget = %+v %v", state, err)
	}
	if err := app.forgetWorkProfile(context.Background()); err != nil {
		t.Fatal("forgetting twice must be fine")
	}
}
