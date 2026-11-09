package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type workProfileTestSources struct {
	entries []KnowledgeEntry
}

func (s workProfileTestSources) KnowHow() ([]KnowledgeEntry, error) { return s.entries, nil }
func (s workProfileTestSources) Insights(entries []KnowledgeEntry, name string) YourAgentInsights {
	return (knowHowSpecialtySource{}).Insights(entries, name)
}

func TestWorkProfileCandidatesExcludeSourcesAndPrivateValues(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	makeEntry := func(id, source, app, text string, at time.Time, ids []string) KnowledgeEntry {
		return parseKnowledgeFile(id+".md", workProfileTestNote(id, "Client updates", text, at, source, []string{app}, ids, nil))
	}
	entries := []KnowledgeEntry{
		makeEntry("one", "activity", "Editor", "You write concise client updates for Mina.", old, []string{"a", "b", "c"}),
		makeEntry("two", "activity", "Editor", "You open with the decision.", old.Add(15*24*time.Hour), []string{"d", "e"}),
		makeEntry("mail", "gmail", "Gmail", "You include CANARY-MAIL-7731.", old, []string{"x"}),
	}
	candidates, _, err := workProfileCandidates(workProfileTestSources{entries}, "Mina", []string{"owner@example.com", "hub-1234"})
	if err != nil {
		t.Fatal(err)
	}
	input, err := workProfileModelInput(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 {
		t.Fatal("no eligible candidates")
	}
	for _, private := range []string{"Mina", "CANARY-MAIL", "owner@example.com", "hub-1234"} {
		if strings.Contains(input, private) {
			t.Fatalf("model input contains %q", private)
		}
	}
}

func TestWorkProfileModelTraceAndStrictJSON(t *testing.T) {
	candidates := []workProfileCandidate{{ID: "c1", Kind: "taste", Text: "You write concise client updates."}, {ID: "c2", Kind: "specialty", Text: "client updates"}}
	output := `{"lines":[{"text":"You write concise client updates.","from":["c1"]},{"text":"You write for Acme.","from":["c1"]},{"text":"You write updates.","from":["missing"]}],"publicSpecialties":["client updates"]}`
	result, err := workProfileDecode(output, candidates, "Mina", []string{"owner@example.com"})
	if err != nil || len(result.Lines) != 1 || len(result.PublicSpecialties) != 1 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	data, err := json.Marshal(result)
	if err != nil || strings.Contains(string(data), "Acme") {
		t.Fatalf("untraced data survived: %s, %v", data, err)
	}
	for _, invalid := range []string{`{"lines":[],"extra":true}`, `{"lines":[]} {}`} {
		if _, err := workProfileDecode(invalid, candidates, "", nil); err == nil {
			t.Fatalf("accepted invalid response %s", invalid)
		}
	}
}

func TestWorkProfileFilteredMarkerFollowsCandidate(t *testing.T) {
	candidates := []workProfileCandidate{{ID: "c1", Kind: "taste", Text: "You write clear updates.", Filtered: true}}
	input, err := workProfileModelInput(candidates)
	if err != nil || strings.Contains(input, "filtered") {
		t.Fatalf("private marker in model input: %s, %v", input, err)
	}
	result, err := workProfileDecode(`{"lines":[{"text":"You write clear updates.","from":["c1"]}],"publicSpecialties":[]}`, candidates, "", nil)
	if err != nil || len(result.Lines) != 1 || !result.Lines[0].Filtered {
		t.Fatalf("filtered marker lost: %+v, %v", result, err)
	}
}

func TestWorkProfileLineRequiresKnowHowAndKeepsGroup(t *testing.T) {
	candidates := []workProfileCandidate{
		{ID: "c1", Kind: "specialty", Text: "Client updates", Group: "client update"},
		{ID: "c2", Kind: "taste", Text: "You write clear updates.", Group: "client update"},
	}
	result, err := workProfileDecode(`{"lines":[{"text":"Client updates","from":["c1"]},{"text":"You write clear updates.","from":["c2"]}],"publicSpecialties":[]}`, candidates, "", nil)
	if err != nil || len(result.Lines) != 1 || result.Lines[0].Group != "client update" {
		t.Fatalf("line provenance or grouping lost: %+v, %v", result, err)
	}
}

func TestWorkProfileUsesSettingsSpecialtyRanking(t *testing.T) {
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := []KnowledgeEntry{
		parseKnowledgeFile("one.md", workProfileTestNote("one", "Client updates", "You write clear updates.", first, "activity", []string{"Editor"}, []string{"a", "b", "c"}, nil)),
		parseKnowledgeFile("two.md", workProfileTestNote("two", "Client updates", "You lead with decisions.", first.Add(15*24*time.Hour), "activity", []string{"Editor"}, []string{"d", "e"}, nil)),
	}
	sources := workProfileTestSources{entries}
	settings := sources.Insights(entries, "")
	candidates, _, err := workProfileCandidates(sources, "", nil)
	if err != nil || len(settings.Specialties) != 1 || len(candidates) == 0 || candidates[0].Text != settings.Specialties[0].Name {
		t.Fatalf("Settings and profile disagree: %+v, %+v, %v", settings.Specialties, candidates, err)
	}
	mail := parseKnowledgeFile("extra.md", workProfileTestNote("extra", "Client updates", "You write private details.", first, "gmail", []string{"Gmail"}, []string{"f", "g", "h"}, nil))
	withMail := append(append([]KnowledgeEntry(nil), entries...), mail)
	candidates, _, err = workProfileCandidates(workProfileTestSources{withMail}, "", nil)
	if err != nil || len(candidates) == 0 || candidates[0].Sessions != 5 {
		t.Fatalf("excluded sessions entered the candidate: %+v, %v", candidates, err)
	}
	// A name only supported by excluded notes cannot be passed to the model.
	for i := range entries {
		entries[i] = parseKnowledgeFile("mail.md", knowledgeProvenance(entries[i].Content, entries[i].Content, []string{"gmail"}, []string{"Gmail"}, true))
	}
	candidates, _, err = workProfileCandidates(workProfileTestSources{entries}, "", nil)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("excluded specialty entered model input: %+v, %v", candidates, err)
	}
}
