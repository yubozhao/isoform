package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkProfileGoldenDraftAndUpload(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", "work-profile", "mina", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSuffix(string(data), "\n")
	}
	var notes []struct {
		ID, Title, Description, Source, App string
		Day                                 int
		Sessions                            []string
	}
	if err := json.Unmarshal([]byte(read("notes.json")), &notes); err != nil {
		t.Fatal(err)
	}
	var entries []KnowledgeEntry
	for _, note := range notes {
		at := time.Date(2026, 1, note.Day, 12, 0, 0, 0, time.Local)
		entries = append(entries, parseKnowledgeFile(note.ID+".md", workProfileTestNote(note.ID, note.Title, note.Description, at, note.Source, []string{note.App}, note.Sessions, nil)))
	}
	candidates, learned, err := workProfileCandidates(workProfileTestSources{entries}, "Mina", []string{"owner@example.com", "hub-1234"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(candidates)
	if string(encoded) != read("candidates.json") {
		t.Fatalf("candidates: %s", encoded)
	}
	model := &workProfileFakeModel{output: read("model-output.json")}
	result, err := workProfileGenerate(context.Background(), model, candidates, "Mina", []string{"owner@example.com", "hub-1234"})
	if err != nil || len(model.prompts) != 1 || model.prompts[0] != read("model-input.txt") {
		t.Fatalf("model input: %v, %v", model.prompts, err)
	}
	for _, secret := range []string{"Mina", "owner@example.com", "hub-1234", "CANARY-MAIL"} {
		if strings.Contains(model.prompts[0], secret) {
			t.Fatalf("private input %q", secret)
		}
	}
	draft, err := workProfileAssembleDraft(candidates, result, learned, "fixture", nil, 0, nil, "Mina", []string{"owner@example.com", "hub-1234"}, time.Date(2026, 1, 21, 0, 0, 0, 0, time.UTC))
	if err != nil || len(draft.Lines) != 1 {
		t.Fatalf("draft: %+v, %v", draft, err)
	}
	draft.ID, draft.Lines[0].ID = "draft-fixture", "line-fixture"
	encoded, _ = json.Marshal(draft)
	if string(encoded) != read("draft.json") {
		t.Fatalf("draft: %s", encoded)
	}
	preview, err := workProfilePreview(workProfileState{Pending: draft}, "Mina", []string{"owner@example.com", "hub-1234"}, time.Date(2026, 1, 21, 0, 0, 0, 0, time.UTC))
	encoded, _ = json.Marshal(preview)
	if err != nil || string(encoded) != read("upload.json") {
		t.Fatalf("upload: %s, %v", encoded, err)
	}
}
