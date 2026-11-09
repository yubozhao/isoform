package main

import (
	"context"
	"testing"
	"time"
)

func TestWorkProfileDraftChangesAndRemovalSuggestions(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	group := "client update"
	groupID := workProfileLineHash(group)
	candidates := []workProfileCandidate{{ID: "c1", Kind: "specialty", Text: "Client updates", Group: group}}
	approved := &WorkProfileDraft{
		Specialties: []WorkProfileSpecialty{{ID: groupID, Name: "Client SOWs", Selected: true}},
		Lines: []WorkProfileLine{
			{ID: "one", Group: groupID, Text: "You write clear updates.", LastSeen: "2026-10-01"},
			{ID: "two", Text: "You check the wording.", LastSeen: "2026-07-01"},
		},
	}
	result := workProfileModelResult{Lines: []workProfileModelLine{{Text: "You write clear updates first.", Group: group, LastSeen: "2026-11-08", Filtered: true}}}
	draft, err := workProfileAssembleDraft(candidates, result, "2026-11-08", "fingerprint", approved, 3, nil, "Mina", nil, now)
	if err != nil || len(draft.Lines) != 2 || draft.Specialties[0].Name != "Client SOWs" || !draft.Specialties[0].Selected {
		t.Fatalf("draft: %+v, %v", draft, err)
	}
	if draft.Lines[0].Status != "changed" || draft.Lines[0].Previous != "You write clear updates." || !draft.Lines[0].Filtered || draft.Lines[1].Status != "suggested_removal" {
		t.Fatalf("changes: %+v", draft.Lines)
	}
	_, err = workProfileAssembleDraft(candidates, result, "", "", &WorkProfileDraft{Lines: []WorkProfileLine{{Text: "Mina wrote this."}}}, 0, nil, "Mina", nil, now)
	if err == nil {
		t.Fatal("private prior line entered draft")
	}
}

func TestWorkProfileDraftRetriesChangedInputs(t *testing.T) {
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := []KnowledgeEntry{
		parseKnowledgeFile("one.md", workProfileTestNote("one", "Client updates", "You write clear updates.", first, "activity", []string{"Editor"}, []string{"a", "b", "c"}, nil)),
		parseKnowledgeFile("two.md", workProfileTestNote("two", "Client updates", "You lead with decisions.", first.Add(15*24*time.Hour), "activity", []string{"Editor"}, []string{"d", "e"}, nil)),
	}
	model := &workProfileFakeModel{output: `{"lines":[{"text":"You lead with decisions.","from":["c2"]}],"publicSpecialties":[]}`}
	calls := 0
	fingerprint := func() (string, error) {
		calls++
		if calls == 1 {
			return "old", nil
		}
		return "new", nil
	}
	draft, err := workProfileMakeDraft(context.Background(), workProfileTestSources{entries}, model, fingerprint, nil, 0, nil, "Mina", nil, first.Add(20*24*time.Hour))
	if err != nil || calls != 4 || len(model.prompts) != 2 || draft.Fingerprint != "new" || len(draft.Lines) != 1 || draft.Lines[0].Group == "" {
		t.Fatalf("draft retry: %+v, calls %d, prompts %d, error %v", draft, calls, len(model.prompts), err)
	}
}
