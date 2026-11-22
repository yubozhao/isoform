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

// writeHistoryFile writes lines to path and sets its modification time.
func writeHistoryFile(t *testing.T, path string, at time.Time, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func claudeLine(t *testing.T, cwd, kind, text string) string {
	t.Helper()
	row := map[string]any{
		"type":      kind,
		"cwd":       cwd,
		"timestamp": "2026-10-20T10:00:00Z",
		"message":   map[string]any{"role": kind, "content": text},
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func codexMetaLine(t *testing.T, cwd string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"cwd": cwd, "id": "s"}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestScanAssistantHistoryPassOne covers the 30-day cutoff, the 7 and 14 day
// counts, worktree collapse (Claude Code and git worktrees), the assistant list
// and the sort order, on a temporary home with fake Claude and Codex files.
func TestScanAssistantHistoryPassOne(t *testing.T) {
	home := t.TempDir()
	claudeProjects := filepath.Join(home, ".claude", "projects")
	codexSessions := filepath.Join(home, ".codex", "sessions")
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)
	ago := func(days float64) time.Time { return now.Add(-time.Duration(days * float64(24*time.Hour))) }

	halcyon := filepath.Join(home, "src", "halcyon")
	if err := os.MkdirAll(filepath.Join(halcyon, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	// A Claude Code worktree under the repository collapses into it.
	halcyonWorktree := filepath.Join(halcyon, ".claude", "worktrees", "feature-x")
	// A git worktree outside the repository points back into it.
	tool := filepath.Join(home, "src", "tool")
	if err := os.MkdirAll(filepath.Join(tool, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	toolWorktree := filepath.Join(home, "src", "tool-wt")
	if err := os.MkdirAll(toolWorktree, 0700); err != nil {
		t.Fatal(err)
	}
	gitFile := "gitdir: " + filepath.Join(tool, ".git", "worktrees", "tool-wt") + "\n"
	if err := os.WriteFile(filepath.Join(toolWorktree, ".git"), []byte(gitFile), 0600); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(home, "scratch", "notes") // no repository anywhere

	// Claude Code: two sessions in halcyon (one in the worktree), one
	// session 10 days old, one older than the window, and one in the home folder.
	writeHistoryFile(t, filepath.Join(claudeProjects, "-halcyon", "a.jsonl"), ago(0.5), claudeLine(t, halcyon, "user", "fix the tests"))
	writeHistoryFile(t, filepath.Join(claudeProjects, "-halcyon-wt", "b.jsonl"), ago(3), claudeLine(t, halcyonWorktree, "user", "next"))
	writeHistoryFile(t, filepath.Join(claudeProjects, "-halcyon-old", "c.jsonl"), ago(10), claudeLine(t, halcyon, "user", "older"))
	writeHistoryFile(t, filepath.Join(claudeProjects, "-ancient", "d.jsonl"), ago(40), claudeLine(t, halcyon, "user", "too old"))
	writeHistoryFile(t, filepath.Join(claudeProjects, "-home", "e.jsonl"), ago(1), claudeLine(t, home, "user", "home folder"))
	// Codex: one session in the git worktree of tool, one in the loose folder.
	writeHistoryFile(t, filepath.Join(codexSessions, "2026", "10", "27", "rollout-2026-10-27-a.jsonl"), ago(1.5), codexMetaLine(t, toolWorktree))
	writeHistoryFile(t, filepath.Join(codexSessions, "2026", "10", "25", "rollout-2026-10-25-b.jsonl"), ago(9), codexMetaLine(t, loose))

	scan := fixtureHistoryScan(t, assistantHistoryRoots{Home: home, Claude: claudeProjects, Codex: codexSessions}, now)

	if got := strings.Join(scan.Assistants, ","); got != "claude,codex" {
		t.Fatalf("assistants = %q", got)
	}
	byName := map[string]HistoryProject{}
	for _, project := range scan.Projects {
		byName[project.Name] = project
	}
	halcyonProject, ok := byName["halcyon"]
	if !ok {
		t.Fatalf("halcyon worktrees did not collapse into the repository: %+v", scan.Projects)
	}
	if halcyonProject.Path != halcyon || halcyonProject.Sessions14d != 3 || halcyonProject.SessionsThisWeek != 2 {
		t.Fatalf("halcyon counts: %+v", halcyonProject)
	}
	if strings.Join(halcyonProject.Assistants, ",") != "claude" {
		t.Fatalf("halcyon assistants: %v", halcyonProject.Assistants)
	}
	toolProject, ok := byName["tool"]
	if !ok || toolProject.Path != tool || toolProject.Sessions14d != 1 || toolProject.SessionsThisWeek != 1 {
		t.Fatalf("git worktree did not collapse: %+v", toolProject)
	}
	if strings.Join(toolProject.Assistants, ",") != "codex" {
		t.Fatalf("tool assistants: %v", toolProject.Assistants)
	}
	notes, ok := byName["notes"]
	if !ok || notes.Path != loose || notes.Sessions14d != 1 || notes.SessionsThisWeek != 0 {
		t.Fatalf("loose folder: %+v", notes)
	}
	if _, found := byName["home"]; found {
		t.Fatal("a session in the home folder became a project")
	}
	if len(scan.Projects) != 3 {
		t.Fatalf("projects = %d, want 3 (the 40-day session is outside the window)", len(scan.Projects))
	}
	// Sorted by sessions in 14 days, then by the last session.
	if scan.Projects[0].Name != "halcyon" || scan.Projects[1].Name != "notes" && scan.Projects[1].Name != "tool" {
		t.Fatalf("sort order: %s, %s, %s", scan.Projects[0].Name, scan.Projects[1].Name, scan.Projects[2].Name)
	}
	if want := halcyonProject.LastAt; want != ago(0.5).UnixMilli() {
		t.Fatalf("lastAt = %d, want the newest session", want)
	}
}

// TestScanAssistantHistoryCapsTheProjectList checks the ten-project cap.
func TestScanAssistantHistoryCapsTheProjectList(t *testing.T) {
	home := t.TempDir()
	claudeProjects := filepath.Join(home, ".claude", "projects")
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)
	for i := 0; i < 12; i++ {
		dir := filepath.Join(home, "work", string(rune('a'+i)))
		writeHistoryFile(t, filepath.Join(claudeProjects, "p", string(rune('a'+i))+".jsonl"), now.Add(-time.Duration(i+1)*time.Hour), claudeLine(t, dir, "user", "x"))
	}
	scan := fixtureHistoryScan(t, assistantHistoryRoots{Home: home, Claude: claudeProjects, Codex: filepath.Join(home, "none")}, now)
	if len(scan.Projects) != assistantHistoryMaxProject {
		t.Fatalf("projects = %d, want %d", len(scan.Projects), assistantHistoryMaxProject)
	}
}

// TestAssistantHistoryRootsHonorConfigDirs checks the overrides the rest of the
// app reads: CLAUDE_CONFIG_DIR and CODEX_HOME.
func TestAssistantHistoryRootsHonorConfigDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude-alt"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-alt"))
	roots, err := assistantHistoryRootsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if roots.Claude != filepath.Join(home, "claude-alt", "projects") || roots.Codex != filepath.Join(home, "codex-alt", "sessions") {
		t.Fatalf("roots: %+v", roots)
	}
}

func fixtureHistoryScan(t *testing.T, roots assistantHistoryRoots, now time.Time) assistantHistoryScan {
	t.Helper()
	t.Setenv(agentHomeEnv, filepath.Join(roots.Home, "agent-data"))
	settings := learnFromItSettings{Migrated: true, Choices: map[string]learnFromItChoice{}}
	for id := range historySources {
		settings.Choices[id] = learnFromItChoice{true, now.UnixMilli(), "person"}
	}
	if err := saveLearnFromIt(settings); err != nil {
		t.Fatal(err)
	}
	scan, err := (&App{}).scanAssistantHistory(context.Background(), roots, now, "")
	if err != nil {
		t.Fatal(err)
	}
	return scan
}
