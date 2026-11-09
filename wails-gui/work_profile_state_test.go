package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkProfileStateOwnerOnlyAndFailedMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "work-profile.json")
	_, err := workProfileUpdateState(path, func(state *workProfileState) error {
		state.Pending = &WorkProfileDraft{ID: "draft", Lines: []WorkProfileLine{{ID: "line", Text: "You write clearly."}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state mode: %v, %v", info, err)
	}
	_, err = workProfileUpdateState(path, func(state *workProfileState) error {
		state.Pending.Lines[0].Text = "unsaved"
		return errors.New("reject")
	})
	if err == nil {
		t.Fatal("failed mutation accepted")
	}
	state, err := workProfileReadState(path)
	if err != nil || state.Pending.Lines[0].Text != "You write clearly." {
		t.Fatalf("failed mutation changed state: %+v, %v", state, err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := workProfileUpdateState(path, func(*workProfileState) error { return nil }); err == nil {
		t.Fatal("corrupt state silently replaced")
	}
}
