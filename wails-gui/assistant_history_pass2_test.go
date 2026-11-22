package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// historyFixture is a temporary home with one project (a CLAUDE.md, and a
// Claude Code session that carries a secret) and an isolated agent data folder.
func historyFixture(t *testing.T) (assistantHistoryRoots, string, time.Time) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(agentHomeEnv, filepath.Join(home, "agent-data"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	now := time.Now().Truncate(time.Second)
	repo := filepath.Join(home, "src", "halcyon")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	claudeMd := "# Halcyon\n\nRun `go test ./...` before every push.\nAPI_TOKEN=example-api-token-value\n"
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte(claudeMd), 0600); err != nil {
		t.Fatal(err)
	}
	claudeProjects := filepath.Join(home, ".claude", "projects")
	session := filepath.Join(claudeProjects, "-halcyon", "s1.jsonl")
	writeHistoryFile(t, session, now.Add(-2*time.Hour),
		claudeLine(t, repo, "user", "The onboarding tests are failing again. Why?"),
		claudeLine(t, repo, "assistant", "Try: export DEPLOY_SECRET=example-deploy-secret and rerun."),
		claudeLine(t, repo, "user", "Fixed the fixture store. Run the tests before every push."),
	)
	if err := (&App{}).SetLearnFromIt("claude-code", true); err != nil {
		t.Fatal(err)
	}
	return assistantHistoryRoots{Home: home, Claude: claudeProjects, Codex: filepath.Join(home, "none")}, repo, now
}

// TestAssistantHistoryPassTwoStoresCheckedDrafts runs pass 2 with the model
// stubbed. It checks the prompt holds no secret, that beliefs are stored as
// Inferred history memories with their session, and that know-how is stored
// from the session and from CLAUDE.md.
func TestAssistantHistoryPassTwoStoresCheckedDrafts(t *testing.T) {
	roots, _, now := historyFixture(t)
	reply := `{"status":"Onboarding tests are failing on main.","openProblem":"the onboarding tests failing","openQuestion":"Why are the onboarding tests failing?","openChip":"What's left on the onboarding tests?","openSession":1,"beliefs":[{"text":"Runs the tests before every push","session":1},{"text":"Keeps password=hunter2222 in the fixtures","session":1}],"knowHow":[{"title":"Run tests before push","takeaway":"Run go test before pushing.","kind":"how_to","fromClaudeMd":true,"session":0},{"title":"Reset the fixture store","takeaway":"Reset the store before the onboarding test.","kind":"fix","fromClaudeMd":false,"session":1}]}`
	var prompts []string
	oldCall := assistantHistoryModelCall
	t.Cleanup(func() { assistantHistoryModelCall = oldCall })
	assistantHistoryModelCall = func(ctx context.Context, a *App, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		return reply, nil
	}

	app := &App{}
	scan := fixtureHistoryScan(t, roots, now)
	if len(scan.Projects) != 1 {
		t.Fatalf("projects: %+v", scan.Projects)
	}
	project := scan.Projects[0]
	update, called, err := app.runAssistantHistoryProject(context.Background(), project, scan.Sessions[project.ID], "English")
	if err != nil || !called {
		t.Fatalf("pass 2: called=%v err=%v", called, err)
	}
	if len(prompts) != 1 {
		t.Fatalf("model calls = %d, want one per project", len(prompts))
	}
	prompt := prompts[0]
	for _, secret := range []string{"example-deploy-secret", "example-api-token-value", "DEPLOY_SECRET", "hunter2222"} {
		if strings.Contains(prompt, secret) {
			t.Fatalf("secret %q reached the prompt", secret)
		}
	}
	if !strings.Contains(prompt, "Write every text value in English") || !strings.Contains(prompt, "Run `go test ./...` before every push.") {
		t.Fatalf("prompt missing the language or the project instructions:\n%s", prompt)
	}
	if update.OpenProblem != "the onboarding tests failing" || update.OpenQuestion == "" || update.OpenChip == "" || update.OpenProblemAt == 0 {
		t.Fatalf("open problem: %+v", update)
	}
	if len(update.Beliefs) != 1 || update.Beliefs[0].Text != "Runs the tests before every push" || update.Beliefs[0].Assistant != "claude" {
		t.Fatalf("beliefs (the secret one must be dropped): %+v", update.Beliefs)
	}
	if update.Beliefs[0].At.UnixMilli() != scan.Sessions[project.ID][0].At.UnixMilli() {
		t.Fatal("belief date is not the session's own time")
	}
	if len(update.KnowHow) != 2 || update.KnowHow[0].Source != assistantHistoryClaudeMd || update.KnowHow[1].Source != assistantHistorySource {
		t.Fatalf("know-how sources: %+v", update.KnowHow)
	}

	now2 := time.Now()
	written, err := app.storeAssistantHistoryBeliefs(project, update.Beliefs, now2)
	if err != nil || written != 1 {
		t.Fatalf("beliefs written = %d, %v", written, err)
	}
	entries, err := app.GetMemoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	var found MemoryEntry
	for _, entry := range entries {
		if entry.Source == assistantHistorySource {
			found = entry
		}
	}
	if found.ID == "" {
		t.Fatalf("no history memory stored: %+v", entries)
	}
	if found.Title != "You run the tests before every push" {
		t.Fatalf("history title = %q", found.Title)
	}
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	belief, err := app.aboutYouBelief(dir, found, now2)
	if err != nil {
		t.Fatal(err)
	}
	if belief.Source != assistantHistorySource || belief.SourceAssistant != "claude" || belief.SourceAt != update.Beliefs[0].At.UnixMilli() {
		t.Fatalf("belief provenance: %+v", belief)
	}
	if belief.Status == "confirmed" || belief.ConfirmedAt != 0 {
		t.Fatalf("history belief arrived confirmed: %+v", belief)
	}

	// Writing the same belief again adds nothing.
	if again, _ := app.storeAssistantHistoryBeliefs(project, update.Beliefs, now2); again != 0 {
		t.Fatalf("duplicate belief written: %d", again)
	}

	knowHow, err := app.storeAssistantHistoryKnowHow(project, update.KnowHow, now2)
	if err != nil || knowHow != 2 {
		t.Fatalf("know-how written = %d, %v", knowHow, err)
	}
	cards, err := app.GetKnowledgeEntries()
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]bool{}
	for _, entry := range cards {
		doc, ok := parseOKFDocument(entry.Content)
		if !ok {
			continue
		}
		card := knowHowCard(entry)
		if card.Source == assistantHistorySource && card.SourceAssistant == "claude" && card.SourceAt != 0 {
			sources["history"] = true
		}
		if card.Source == assistantHistoryClaudeMd {
			sources["claude-md"] = true
		}
		_ = doc
	}
	if !sources["history"] || !sources["claude-md"] {
		t.Fatalf("know-how provenance missing: %v", sources)
	}
}

// TestAssistantHistoryRunStoresViewAndCounts runs the whole worker once with
// the model stubbed and checks the cache: projects with their open problem,
// counts, pass 2 done, and a view that answers without scanning.
func TestAssistantHistoryRunStoresViewAndCounts(t *testing.T) {
	roots, _, now := historyFixture(t)
	_ = roots
	reply := `{"status":"","openProblem":"","openQuestion":"","openChip":"","openSession":0,"beliefs":[{"text":"Runs the tests before every push","session":1}],"knowHow":[]}`
	oldCall := assistantHistoryModelCall
	t.Cleanup(func() { assistantHistoryModelCall = oldCall })
	assistantHistoryModelCall = func(ctx context.Context, a *App, prompt string) (string, error) { return reply, nil }
	_ = now

	app := &App{}
	app.runAssistantHistoryScan(context.Background(), app.assistantHistory.generation, "")

	view := app.GetAssistantHistory()
	if !view.Enabled || view.State != "ready" || len(view.Projects) != 1 || !view.Pass2Done {
		t.Fatalf("view after the run: %+v", view)
	}
	if view.BeliefCount != 1 || view.ScannedAt == 0 || len(view.Assistants) != 1 || view.Assistants[0] != "claude" {
		t.Fatalf("counts and assistants: %+v", view)
	}
	if view.Projects[0].Name != "halcyon" || view.Projects[0].OpenProblem != "" {
		t.Fatalf("project: %+v", view.Projects[0])
	}
	// A second run with no new sessions makes no model call.
	calls := 0
	assistantHistoryModelCall = func(ctx context.Context, a *App, prompt string) (string, error) {
		calls++
		return reply, nil
	}
	app.runAssistantHistoryScan(context.Background(), app.assistantHistory.generation, "")
	if calls != 0 {
		t.Fatalf("pass 2 ran again without new sessions: %d calls", calls)
	}
}

// TestAssistantHistorySavedOnAndOffForgets covers the compatibility switch: the
// fixture has saved on, turning it off forgets what came from history, and the view
// is empty afterwards.
func TestAssistantHistorySavedOnAndOffForgets(t *testing.T) {
	roots, _, now := historyFixture(t)
	_ = roots
	_ = now
	app := &App{}
	if value, err := app.GetReadAssistantHistory(); err != nil || !value {
		t.Fatal("reading history must reflect the saved choice")
	}
	oldCall := assistantHistoryModelCall
	t.Cleanup(func() { assistantHistoryModelCall = oldCall })
	assistantHistoryModelCall = func(ctx context.Context, a *App, prompt string) (string, error) {
		return `{"beliefs":[{"text":"Runs the tests before every push","session":1}],"knowHow":[{"title":"Run tests before push","takeaway":"Run go test before pushing.","kind":"how_to","fromClaudeMd":true,"session":0}]}`, nil
	}
	app.runAssistantHistoryScan(context.Background(), app.assistantHistory.generation, "")
	if view := app.GetAssistantHistory(); view.BeliefCount != 1 || view.KnowHowCount != 1 {
		t.Fatalf("before off: %+v", view)
	}
	if err := app.SetReadAssistantHistory(false); err != nil {
		t.Fatal(err)
	}
	if value, err := app.GetReadAssistantHistory(); err != nil || value {
		t.Fatal("switch did not turn off")
	}
	view := app.GetAssistantHistory()
	if view.Enabled || view.State != "off" || len(view.Projects) != 0 || view.BeliefCount != 0 {
		t.Fatalf("view after off: %+v", view)
	}
	entries, err := app.GetMemoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Source == assistantHistorySource {
			t.Fatalf("history memory survived off: %s", entry.ID)
		}
	}
	cards, err := app.GetKnowledgeEntries()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range cards {
		if doc, ok := parseOKFDocument(entry.Content); ok {
			if source := okfString(doc.Frontmatter, "source"); source == assistantHistorySource || source == assistantHistoryClaudeMd {
				t.Fatalf("history know-how survived off: %s", entry.ID)
			}
		}
	}
	if err := app.SetReadAssistantHistory(true); err != nil {
		t.Fatal(err)
	}
	if value, err := app.GetReadAssistantHistory(); err != nil || !value {
		t.Fatal("switch did not turn back on")
	}
}

// TestAssistantHistoryMergeKeepsPassTwoFields checks that a rescan keeps the
// pass 2 text of a project that is still there.
func TestAssistantHistoryMergeKeepsPassTwoFields(t *testing.T) {
	old := assistantHistoryCache{Projects: []HistoryProject{{ID: "p1", Name: "halcyon", Status: "Tests fail.", OpenProblem: "the onboarding tests failing", OpenQuestion: "Why?", OpenChip: "What's left?", OpenProblemAt: 5}}, Pass2Marks: map[string]int64{}}
	scan := assistantHistoryScan{ScannedAt: time.UnixMilli(100), Assistants: []string{"claude"}, Projects: []HistoryProject{{ID: "p1", Name: "halcyon", Sessions14d: 4}}}
	merged := mergeAssistantHistoryPass1(old, scan)
	got := merged.Projects[0]
	if got.Status != "Tests fail." || got.OpenQuestion != "Why?" || got.OpenChip != "What's left?" || got.OpenProblemAt != 5 || got.Sessions14d != 4 {
		t.Fatalf("merged project: %+v", got)
	}
	if merged.ScannedAt != 100 {
		t.Fatalf("scannedAt = %d", merged.ScannedAt)
	}
}

// TestAssistantHistoryWelcomeWaitsOnlyForARunningScan checks that the day-0
// welcome never starts a scan and does not wait when none is running.
func TestAssistantHistoryWelcomeWaitsOnlyForARunningScan(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	app := &App{}
	start := time.Now()
	if got := app.assistantHistoryAssistantsWithin(assistantHistoryWelcomeWait); got != nil {
		t.Fatalf("assistants without a scan: %v", got)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the welcome waited with no scan running")
	}
	if key := assistantHistoryHistoryKey([]string{"claude", "codex"}); key != "both" {
		t.Fatalf("history key = %q", key)
	}
}

// A belief names every session that shows it; the store keeps each session's
// own time, so the portrait counts them on their own days.
func TestAssistantHistoryBeliefKeepsEverySessionDate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("YANSU_AGENT_HOME", filepath.Join(root, "data"))
	base := time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local)
	included := []assistantHistorySession{
		{Assistant: "claude", At: base},
		{Assistant: "claude", At: base.Add(48 * time.Hour)},
		{Assistant: "claude", At: base.Add(96 * time.Hour)},
		{Assistant: "codex", At: base.Add(120 * time.Hour)},
	}
	reply := assistantHistoryReply{Beliefs: []assistantHistoryReplyBelief{{Text: "Runs the tests before every push", Session: 1, Sessions: []int{1, 2, 3, 4, 9}}}}
	update := buildAssistantHistoryUpdate(reply, included, "", time.Time{})
	if len(update.Beliefs) != 1 || len(update.Beliefs[0].SessionsAt) != 3 {
		t.Fatalf("beliefs = %+v (another assistant's or a missing session must not count)", update.Beliefs)
	}
	app := &App{}
	imported := base.Add(20 * 24 * time.Hour)
	if written, err := app.storeAssistantHistoryBeliefs(HistoryProject{ID: "p", Name: "isoform"}, update.Beliefs, imported); err != nil || written != 1 {
		t.Fatalf("written = %d, %v", written, err)
	}
	entries, err := app.GetMemoryEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	observations := portraitImportedObservations(entries[0])
	days := map[string]bool{}
	for _, observation := range observations {
		days[observation.Day] = true
	}
	if len(observations) != 3 || len(days) != 3 || days[portraitLocalDay(imported.UnixMilli())] {
		t.Fatalf("observations = %+v", observations)
	}
}
