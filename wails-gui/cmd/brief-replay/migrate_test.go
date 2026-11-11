package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// dayAgo is the local day, YYYY-MM-DD, that is the given number of days before today.
func dayAgo(days int) string {
	return time.Now().AddDate(0, 0, -days).Format("2006-01-02")
}

// writeBuilt writes a built brief (the stored .json) with the given local day,
// narrative paragraphs (each citing the refs it names) and evidence per ref.
func writeBuilt(t *testing.T, dir, id, day string, paragraphs [][]string, sources map[string]string) {
	t.Helper()
	var narrative []map[string]any
	for _, refs := range paragraphs {
		narrative = append(narrative, map[string]any{"text": "written words", "refs": refs})
	}
	var evidence []map[string]any
	for ref, source := range sources {
		evidence = append(evidence, map[string]any{"ref": ref, "sourceKind": "memory", "sourceId": source})
	}
	built := map[string]any{"briefId": id, "localDate": day, "narrative": narrative, "cards": []any{map[string]any{"id": "c1"}}, "evidence": evidence}
	data, err := json.Marshal(built)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeOldCopy writes a copy as the old edited-source rule left it: schema 2,
// no rule field, hidden as source_withdrawn.
func writeOldCopy(t *testing.T, dir, id, print string) {
	t.Helper()
	copied := map[string]any{"schema": 2, "print": print, "withheld_reason": "source_withdrawn", "withheld_kind": "memory", "brief": map[string]any{"briefId": id, "narrative": []any{}}}
	data, err := json.Marshal(copied)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".read"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReplayReportsBuiltCountsAndMigrationPrediction(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, privacyEpochFile), []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Edited-only source: the old rule hid it, the migration shows it.
	writeBuilt(t, dir, "2026-11-10-evening", dayAgo(1), [][]string{{"r1"}, {"r2"}}, map[string]string{"r1": "safe", "r2": "other"})
	writeOldCopy(t, dir, "2026-11-10-evening", "3|false|false|abc")
	// A deleted keyed source is cited by the only paragraph: it stays out.
	writeBuilt(t, dir, "2026-11-08-evening", dayAgo(1), [][]string{{"r3"}}, map[string]string{"r3": "gone"})
	writeOldCopy(t, dir, "2026-11-08-evening", "3|false|false|abc")
	// A copy that is not an old-rule candidate is not migrated.
	writeBuilt(t, dir, "2026-11-07-morning", dayAgo(1), [][]string{{"r1"}}, map[string]string{"r1": "safe"})
	writeFixtureBrief(t, dir, "2026-11-07-morning", 2, "3|false|false|abc", "")
	// A 5-day-old old-rule copy is outside the limit and stays as it is.
	writeBuilt(t, dir, "2026-11-03-morning", dayAgo(5), [][]string{{"r1"}}, map[string]string{"r1": "safe"})
	writeOldCopy(t, dir, "2026-11-03-morning", "3|false|false|abc")
	logData, err := json.Marshal(map[string]any{"floor": 0, "entries": []any{map[string]any{"epoch": 3, "keys": []any{map[string]any{"kind": "memory", "id": "gone"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, withdrawalLogFile), logData, 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := replay(dir)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]result{}
	for _, r := range results {
		byID[r.ID] = r
	}
	edited := byID["2026-11-10-evening"]
	if edited.Migrate != "shown" || edited.Built != [3]int{2, 1, 2} || !edited.BuiltOK {
		t.Fatalf("edited-only copy: migrate=%q built=%v ok=%v", edited.Migrate, edited.Built, edited.BuiltOK)
	}
	if withdrawn := byID["2026-11-08-evening"]; withdrawn.Migrate != "withheld" {
		t.Fatalf("copy citing a withdrawn key: migrate=%q, want withheld", withdrawn.Migrate)
	}
	if plain := byID["2026-11-07-morning"]; plain.Migrate != "none" {
		t.Fatalf("copy under seal v2 is a migration candidate: migrate=%q", plain.Migrate)
	}
	if old := byID["2026-11-03-morning"]; old.Migrate != "none" {
		t.Fatalf("a 5-day-old copy is a migration candidate: migrate=%q", old.Migrate)
	}
}

func TestBriefsDirResolvesUnderMobile(t *testing.T) {
	want := filepath.Join("/data", "mobile", "briefs")
	if got := briefsDirFor("/data"); got != want {
		t.Fatalf("briefs folder = %q, want %q", got, want)
	}
}
