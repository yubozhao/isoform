package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func duplicateFixtureHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv(agentHomeEnv, filepath.Join(root, "data"))
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeHistoryBelief writes a history belief the way pass 2 does.
func writeHistoryBelief(t *testing.T, dir, id, title, project, assistant string, learned time.Time, sessions ...time.Time) string {
	t.Helper()
	fields := map[string]any{
		"type":               "Isoform Memory",
		"title":              title,
		"timestamp":          okfTimestamp(learned),
		"source":             assistantHistorySource,
		"dimension":          normalizeMemoryDimension(""),
		"area":               "work",
		"history_assistant":  assistant,
		"history_session_at": okfTimestamp(sessions[0]),
		"history_project":    project,
	}
	if len(sessions) > 1 {
		stamps := make([]string, 0, len(sessions))
		for _, at := range sessions {
			stamps = append(stamps, okfTimestamp(at))
		}
		fields["history_sessions_at"] = stamps
	}
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(renderOKFDocument(fields, "## Memory Points\n\n- "+title)), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func historyStampsOf(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := parseOKFDocument(string(data))
	if !ok {
		t.Fatalf("%s has no frontmatter", path)
	}
	return okfStringSlice(doc.Frontmatter, "history_sessions_at")
}

func memoryFileCount(t *testing.T, dir string) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	return len(files)
}

// The words test is the creation-time gate: paraphrases that differ only in
// function words or endings are one belief, and two beliefs that say the
// opposite are never folded together.
func TestNearDuplicateBeliefsFoldsParaphrasesAndKeepsContradictions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"You run the tests before every push", "Runs tests before each push", true},
		{"Keeps the release notes short", "You keep release notes short", true},
		{"Reviews the diff before merging a pull request", "You review diffs before merging pull requests", true},
		{"You run the tests before every push", "You never run the tests before every push", false},
		{"Runs the tests before every push", "Doesn't run the tests before every push", false},
		{"Prefers bun over npm", "Prefers npm over bun", false},
		{"Prefers short replies rather than long explanations", "Prefers long explanations rather than short replies", false},
		{"Writes tests first", "Writes tests last", false},
		{"Keeps the backup hub read-only", "Keeps the backup hub writable", false},
	} {
		if got := nearDuplicateBeliefs(tc.a, tc.b); got != tc.want {
			t.Errorf("nearDuplicateBeliefs(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// Pass 2 reads a project again on every new session. A belief it already
// stored, restated in other words, must strengthen the stored belief instead
// of adding a second copy to About you.
func TestAssistantHistoryRepeatStrengthensStoredBelief(t *testing.T) {
	dir := duplicateFixtureHome(t)
	base := time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local)
	app := &App{}
	project := HistoryProject{ID: "p", Name: "isoform"}
	first := []assistantHistoryBelief{{Text: "Runs the tests before every push", Assistant: "claude", At: base}}
	if written, err := app.storeAssistantHistoryBeliefs(project, first, base); err != nil || written != 1 {
		t.Fatalf("first written = %d, %v", written, err)
	}
	later := base.Add(72 * time.Hour)
	repeat := []assistantHistoryBelief{
		{Text: "You run tests before each push", Assistant: "claude", At: later},
		{Text: "Runs tests before each push", Assistant: "claude", At: later.Add(time.Hour)},
	}
	if written, err := app.storeAssistantHistoryBeliefs(project, repeat, later); err != nil || written != 0 {
		t.Fatalf("repeat written = %d, %v (a restated belief is not new)", written, err)
	}
	if n := memoryFileCount(t, dir); n != 1 {
		t.Fatalf("memory files = %d, want 1", n)
	}
	entries, err := app.GetMemoryEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, %v", len(entries), err)
	}
	stamps := historyStampsOf(t, filepath.Join(dir, entries[0].ID+".md"))
	if len(stamps) != 3 {
		t.Fatalf("history_sessions_at = %v, want the first session and both repeats", stamps)
	}
	if got := len(portraitImportedObservations(entries[0])); got != 3 {
		t.Fatalf("portrait observations = %d, want 3", got)
	}

	// The opposite belief is its own belief.
	opposite := []assistantHistoryBelief{{Text: "Never runs the tests before every push", Assistant: "claude", At: later}}
	if written, err := app.storeAssistantHistoryBeliefs(project, opposite, later); err != nil || written != 1 {
		t.Fatalf("opposite written = %d, %v (a contradiction must not fold)", written, err)
	}

	// Another project's repeat adds no copy and does not borrow its sessions.
	other := []assistantHistoryBelief{{Text: "Runs tests before every push", Assistant: "claude", At: later.Add(48 * time.Hour)}}
	if written, err := app.storeAssistantHistoryBeliefs(HistoryProject{ID: "q", Name: "halcyon"}, other, later); err != nil || written != 0 {
		t.Fatalf("other project written = %d, %v", written, err)
	}
	if again := historyStampsOf(t, filepath.Join(dir, entries[0].ID+".md")); len(again) != 3 {
		t.Fatalf("history_sessions_at = %v after another project's repeat", again)
	}
}

// A belief the person edited keeps their words and frontmatter as written.
func TestAssistantHistoryRepeatLeavesEditedBeliefAlone(t *testing.T) {
	dir := duplicateFixtureHome(t)
	base := time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local)
	path := writeHistoryBelief(t, dir, "memory-history-edited", "Runs the tests before every push", "isoform", "claude", base, base)
	data, _ := os.ReadFile(path)
	edited := strings.Replace(string(data), "source: "+assistantHistorySource, "source: user_edit", 1)
	if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	repeat := []assistantHistoryBelief{{Text: "Runs tests before each push", Assistant: "claude", At: base.Add(time.Hour)}}
	if written, err := app.storeAssistantHistoryBeliefs(HistoryProject{ID: "p", Name: "isoform"}, repeat, base); err != nil || written != 0 {
		t.Fatalf("written = %d, %v", written, err)
	}
	if after, _ := os.ReadFile(path); string(after) != edited {
		t.Fatal("an edited belief was rewritten")
	}
}

// A history belief seen in more sessions ranks above a one-off: its own
// session times count toward the sessions and days behind its confidence.
func TestAboutYouCountsHistorySessions(t *testing.T) {
	dir := duplicateFixtureHome(t)
	base := time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local)
	writeHistoryBelief(t, dir, "memory-history-often", "Runs the tests before every push", "isoform", "claude", base, base, base.Add(24*time.Hour), base.Add(48*time.Hour))
	writeHistoryBelief(t, dir, "memory-history-once", "Keeps the changelog in the repo root", "isoform", "claude", base, base)
	app := &App{}
	entries, err := app.GetMemoryEntries()
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %d, %v", len(entries), err)
	}
	now := base.Add(30 * 24 * time.Hour)
	beliefs := map[string]AboutYouBelief{}
	for _, entry := range entries {
		belief, err := app.aboutYouBelief(dir, entry, now)
		if err != nil {
			t.Fatal(err)
		}
		beliefs[belief.ID] = belief
	}
	often, once := beliefs["memory-history-often"], beliefs["memory-history-once"]
	if often.SessionCount != 3 || often.DayCount != 3 || often.Confidence != 3 {
		t.Fatalf("often = sessions %d days %d confidence %d, want 3 3 3", often.SessionCount, often.DayCount, often.Confidence)
	}
	if once.SessionCount != 1 || once.Confidence != 1 {
		t.Fatalf("once = sessions %d confidence %d, want 1 1", once.SessionCount, once.Confidence)
	}
}

// The one-time cleanup hides each repeat with a reversible line, adds its
// sessions to the belief it repeats, never hides what the person answered,
// and leaves other projects and contradictions alone.
func TestHideDuplicateHistoryBeliefs(t *testing.T) {
	dir := duplicateFixtureHome(t)
	base := time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local)
	keep := writeHistoryBelief(t, dir, "memory-history-a", "Runs the tests before every push", "isoform", "claude", base, base)
	repeat := writeHistoryBelief(t, dir, "memory-history-b", "You run tests before each push", "isoform", "claude", base.Add(time.Hour), base.Add(24*time.Hour))
	opposite := writeHistoryBelief(t, dir, "memory-history-c", "Never runs the tests before every push", "isoform", "claude", base.Add(2*time.Hour), base.Add(48*time.Hour))
	otherProject := writeHistoryBelief(t, dir, "memory-history-d", "Runs tests before every push", "halcyon", "claude", base.Add(3*time.Hour), base.Add(72*time.Hour))
	// A newer repeat the person confirmed is kept, and an older unanswered
	// copy folds into it.
	confirmedOld := writeHistoryBelief(t, dir, "memory-history-e", "Keeps release notes short", "isoform", "claude", base, base)
	confirmedNew := writeHistoryBelief(t, dir, "memory-history-f", "You keep the release notes short", "isoform", "claude", base.Add(time.Hour), base.Add(96*time.Hour))
	data, _ := os.ReadFile(confirmedNew)
	confirmedContent, err := setMemoryConfirmedAt(string(data), base.Add(5*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(confirmedNew, []byte(confirmedContent), 0600); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(repeat)

	hidden, strengthened, err := hideDuplicateHistoryBeliefs(context.Background(), dir)
	if err != nil || hidden != 2 || strengthened != 2 {
		t.Fatalf("hidden=%d strengthened=%d err=%v, want 2 2", hidden, strengthened, err)
	}
	if again, _, _ := hideDuplicateHistoryBeliefs(context.Background(), dir); again != 0 {
		t.Fatalf("second run hid %d", again)
	}
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if !learnedDuplicate(read(repeat)) || !strings.Contains(read(repeat), learnedDuplicateKey+": memory-history-a") {
		t.Fatal("the repeat is not marked as a duplicate of the first belief")
	}
	if !learnedDuplicate(read(confirmedOld)) || learnedDuplicate(read(confirmedNew)) {
		t.Fatal("the confirmed belief must be the one kept")
	}
	if memoryConfirmedAt(read(confirmedNew)) == 0 {
		t.Fatal("strengthening dropped the confirmation")
	}
	for _, path := range []string{keep, opposite, otherProject} {
		if learnedHidden(read(path)) {
			t.Fatalf("%s hidden", filepath.Base(path))
		}
	}
	if stamps := historyStampsOf(t, keep); len(stamps) != 2 {
		t.Fatalf("kept belief sessions = %v, want its own and the repeat's", stamps)
	}
	if stamps := historyStampsOf(t, confirmedNew); len(stamps) != 2 {
		t.Fatalf("confirmed belief sessions = %v", stamps)
	}

	entries, err := readMemoryEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	visible := map[string]bool{}
	for _, entry := range entries {
		visible[entry.ID] = true
	}
	if len(entries) != 4 || visible["memory-history-b"] || visible["memory-history-e"] {
		t.Fatalf("visible = %v", visible)
	}

	// Removing the line brings the belief back unchanged.
	restored := strings.Replace(read(repeat), learnedDuplicateKey+": memory-history-a\n", "", 1)
	if restored != string(original) {
		t.Fatal("the duplicate mark is not the only change to the hidden file")
	}
}

// The prompt lists the project's stored beliefs, so the model stops restating
// them; another project's beliefs are never listed.
func TestAssistantHistoryPromptListsKnownBeliefs(t *testing.T) {
	dir := duplicateFixtureHome(t)
	base := time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local)
	writeHistoryBelief(t, dir, "memory-history-a", "Runs the tests before every push", "isoform", "claude", base, base)
	writeHistoryBelief(t, dir, "memory-history-b", "Keeps the staging database seeded", "halcyon", "claude", base, base)
	app := &App{}
	known := app.assistantHistoryKnownBeliefs(HistoryProject{ID: "p", Name: "isoform"})
	if len(known) != 1 || known[0] != "You run the tests before every push" {
		t.Fatalf("known = %v", known)
	}
	prompt := buildAssistantHistoryPrompt("isoform", "S1 ...", "", "", "English", known)
	if !strings.Contains(prompt, "Already known about how they work on this project:\n- You run the tests before every push\n") {
		t.Fatal("the prompt does not list the stored belief")
	}
	if strings.Contains(prompt, "staging database") {
		t.Fatal("the prompt lists another project's belief")
	}
	if !strings.Contains(prompt, "never repeat one listed under Already known") {
		t.Fatal("the prompt does not ask the model to skip known beliefs")
	}
	if empty := buildAssistantHistoryPrompt("isoform", "S1 ...", "", "", "English", nil); strings.Contains(empty, "Already known about") {
		t.Fatal("an empty list still prints its heading")
	}
}

// A hidden belief is not reinforced by the activity path, and is never
// offered to the triage model as an update target.
func TestHiddenBeliefIsNotAnUpdateTarget(t *testing.T) {
	dir := duplicateFixtureHome(t)
	base := time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local)
	path := writeHistoryBelief(t, dir, "memory-history-hidden", "Runs the tests before every push", "isoform", "claude", base, base)
	data, _ := os.ReadFile(path)
	marked, ok := markLearnedDuplicate(string(data), "memory-history-other")
	if !ok {
		t.Fatal("not marked")
	}
	if err := os.WriteFile(path, []byte(marked), 0600); err != nil {
		t.Fatal(err)
	}
	if id := nearDuplicateMemoryID("Runs the tests before every push"); id != "" {
		t.Fatalf("nearDuplicateMemoryID = %q, want no hidden match", id)
	}
	writeHistoryBelief(t, dir, "memory-history-visible", "Keeps the changelog in the repo root", "isoform", "claude", base, base)
	summary := (&App{}).loadExistingMemorySummaryWithBudget(4000)
	if !strings.Contains(summary, "memory-history-visible") {
		t.Fatalf("the visible belief is missing from the dedup list: %q", summary)
	}
	if strings.Contains(summary, "memory-history-hidden") {
		t.Fatal("a hidden belief reached the dedup list")
	}
}
