package main

import (
	"strings"
	"testing"
	"time"
)

func TestWorkProfileActionsFilterAndStillLearning(t *testing.T) {
	state := workProfileState{Format: 1}
	if err := workProfileAddLine(&state, "", "You write 12 updates for Mina.", "Mina", nil, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	line := state.Pending.Lines[0]
	if !line.Filtered || strings.Contains(line.Text, "12") || strings.Contains(line.Text, "Mina") || line.Status != "new" {
		t.Fatalf("unfiltered line: %+v", line)
	}
	if err := workProfileEditLine(&state, line.ID, "You make concise updates.", "Mina", nil); err != nil || state.Pending.Lines[0].Text != "You make concise updates." {
		t.Fatalf("edit failed: %+v, %v", state.Pending.Lines, err)
	}
}

func TestWorkProfileActionsRemoveUndoRenameAndSelect(t *testing.T) {
	state := workProfileState{Format: 1, Pending: &WorkProfileDraft{
		Specialties: []WorkProfileSpecialty{{ID: "g1", Name: "Updates", Selected: true}},
		Lines:       []WorkProfileLine{{ID: "l1", Group: "g1", Text: "You write clearly.", Status: "unchanged"}},
	}}
	if _, err := workProfileRename(&state, "g1", "Client SOWs", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := workProfileSetRemoved(&state, "l1", true); err != nil || len(state.RemovedFingerprints) != 1 || state.Pending.Specialties[0].Selected {
		t.Fatalf("remove failed: %+v, %v", state, err)
	}
	if err := workProfileSelect(&state, []string{"g1"}); err == nil {
		t.Fatal("selected specialty without a line")
	}
	if err := workProfileSetRemoved(&state, "l1", false); err != nil || len(state.RemovedFingerprints) != 0 {
		t.Fatalf("undo failed: %+v, %v", state, err)
	}
	if err := workProfileSelect(&state, []string{"g1"}); err != nil || !state.Pending.Specialties[0].Selected {
		t.Fatalf("selection failed: %+v, %v", state, err)
	}
	if err := workProfileEditLine(&state, "l1", "You write directly.", "", nil); err != nil || state.Pending.Lines[0].Previous != "You write clearly." || state.Pending.Lines[0].Status != "changed" {
		t.Fatalf("changed line did not retain old text: %+v, %v", state.Pending.Lines, err)
	}
}
