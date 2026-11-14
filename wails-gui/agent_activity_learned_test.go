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

// claudeOriginLine is a Claude Code line with a time and, when origin is not
// empty, the turnOrigin stamp current Claude Code writes on every prompt.
func claudeOriginLine(t *testing.T, cwd, kind, origin, text string, at time.Time) string {
	t.Helper()
	row := map[string]any{
		"type":      kind,
		"cwd":       cwd,
		"timestamp": at.UTC().Format(time.RFC3339Nano),
		"message":   map[string]any{"role": kind, "content": text},
	}
	if origin != "" {
		row["turnOrigin"] = origin
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func codexExecMetaLine(t *testing.T, cwd string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"cwd": cwd, "id": "s", "originator": "codex_exec", "source": "exec"}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestClaudeHistoryPromptOrigin(t *testing.T) {
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		line   string
		origin historyTurnOrigin
		prompt bool
	}{
		{"typed by the person", claudeOriginLine(t, "/r", "user", "human", "fix the login bug", at), historyOriginHuman, true},
		{"relayed from another session", claudeOriginLine(t, "/r", "user", "sdk", "Background (from the session that started this one): ...", at), historyOriginAgent, true},
		{"task notification", claudeOriginLine(t, "/r", "user", "task_notification", "<task-notification>done</task-notification>", at), historyOriginAgent, true},
		{"scheduled trigger", claudeOriginLine(t, "/r", "user", "scheduled", "Check open PRs every hour", at), historyOriginAgent, true},
		{"legacy prompt without a stamp", claudeOriginLine(t, "/r", "user", "", "why does the build fail?", at), historyOriginHuman, true},
		{"legacy scheduled task", claudeOriginLine(t, "/r", "user", "", "<scheduled-task name=\"hourly-pr-check\">check PRs</scheduled-task>", at), historyOriginAgent, true},
		{"legacy side session", claudeOriginLine(t, "/r", "user", "", "<system-reminder>x</system-reminder>Background (from the session that started this one): build it", at), historyOriginAgent, true},
		{"compaction keeps the prompt before it", claudeOriginLine(t, "/r", "user", "sdk", "This session is being continued from a previous conversation that ran out of context.", at), historyOriginUnknown, false},
		{"assistant line is not a prompt", claudeOriginLine(t, "/r", "assistant", "", "done", at), historyOriginUnknown, false},
	}
	for _, tc := range cases {
		origin, ok := claudeHistoryLineOrigin([]byte(tc.line))
		if origin != tc.origin || ok != tc.prompt {
			t.Errorf("%s: origin=%d prompt=%t, want %d %t", tc.name, origin, ok, tc.origin, tc.prompt)
		}
	}
	tool := `{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`
	if _, ok := claudeHistoryLineOrigin([]byte(tool)); ok {
		t.Error("a tool result opened a prompt")
	}
}

func TestCodexHistoryMetaIsAgent(t *testing.T) {
	for _, tc := range []struct {
		meta  string
		agent bool
	}{
		{`{"type":"session_meta","payload":{"originator":"codex_exec","source":"exec"}}`, true},
		{`{"type":"session_meta","payload":{"originator":"codex_work_desktop","source":{"subagent":{"thread_spawn":{}}}}}`, true},
		{`{"type":"session_meta","payload":{"originator":"Codex Desktop","source":"vscode"}}`, false},
		{`{"type":"session_meta","payload":{"cwd":"/r"}}`, false},
	} {
		agent, ok := codexHistoryMetaIsAgent([]byte(tc.meta))
		if !ok || agent != tc.agent {
			t.Errorf("%s: agent=%t ok=%t, want %t", tc.meta, agent, ok, tc.agent)
		}
	}
}

// TestReadAssistantHistoryTurnsKeepsOnlyThePersonsExchanges: in the hub
// session the person types, hourly checks and task notifications also
// arrive. Only the person's prompts and the replies to them are read.
func TestReadAssistantHistoryTurnsKeepsOnlyThePersonsExchanges(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "hub.jsonl")
	writeHistoryFile(t, path, at,
		claudeOriginLine(t, dir, "user", "human", "Let's ship the onboarding fix today.", at),
		claudeOriginLine(t, dir, "assistant", "", "Starting on the onboarding fix.", at),
		claudeOriginLine(t, dir, "user", "scheduled", "Hourly PR check: list open PRs and merge the green ones.", at),
		claudeOriginLine(t, dir, "assistant", "", "Merged PR #833 and PR #834.", at),
		claudeOriginLine(t, dir, "user", "task_notification", "<task-notification>side session done</task-notification>", at),
		claudeOriginLine(t, dir, "assistant", "", "The side session finished the shadow compose.", at),
		claudeOriginLine(t, dir, "user", "human", "Good. Keep the release notes short.", at),
		claudeOriginLine(t, dir, "assistant", "", "Noted.", at),
	)
	turns := readAssistantHistoryTurns(assistantHistorySession{Assistant: "claude", Path: path})
	var got []string
	for _, turn := range turns {
		got = append(got, turn.Text)
	}
	joined := strings.Join(got, " | ")
	for _, agent := range []string{"Hourly PR check", "Merged PR", "task-notification", "shadow compose"} {
		if strings.Contains(joined, agent) {
			t.Fatalf("agent turn %q was read: %s", agent, joined)
		}
	}
	if len(turns) != 4 {
		t.Fatalf("turns = %d (%s), want the 4 turns of the person's two exchanges", len(turns), joined)
	}

	// A side session started by another session, with no prompt from the
	// person, reads as nothing.
	side := filepath.Join(dir, "side.jsonl")
	writeHistoryFile(t, side, at,
		claudeOriginLine(t, dir, "user", "sdk", "Background (from the session that started this one): add the quiet-day brief fallback.", at),
		claudeOriginLine(t, dir, "assistant", "", "Added the quiet-day brief fallback.", at),
	)
	session := assistantHistorySession{Assistant: "claude", Path: side, Agent: readAssistantHistorySessionAgent("claude", side)}
	if !session.Agent {
		t.Fatal("a side session started by another session was not marked as agent")
	}
	if turns := readAssistantHistoryTurns(session); len(turns) != 0 {
		t.Fatalf("side session turns read: %+v", turns)
	}
}

// TestScanAssistantHistoryCountsAgentSessionsApart: side sessions and Codex
// exec workers are counted as agent context, never as the person's sessions,
// and a folder only agents worked in is not one of the person's projects.
func TestScanAssistantHistoryCountsAgentSessionsApart(t *testing.T) {
	home := t.TempDir()
	claudeProjects := filepath.Join(home, ".claude", "projects")
	codexSessions := filepath.Join(home, ".codex", "sessions")
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local)
	repo := filepath.Join(home, "src", "app")
	agentsOnly := filepath.Join(home, "src", "bots")
	for _, dir := range []string{repo, agentsOnly} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	at := now.Add(-time.Hour)
	writeHistoryFile(t, filepath.Join(claudeProjects, "-app", "mine.jsonl"), at, claudeOriginLine(t, repo, "user", "human", "fix the tests", at))
	writeHistoryFile(t, filepath.Join(claudeProjects, "-app", "side.jsonl"), at, claudeOriginLine(t, repo, "user", "sdk", "Background (from the session that started this one): fix it", at))
	writeHistoryFile(t, filepath.Join(claudeProjects, "-app", "hourly.jsonl"), at, claudeOriginLine(t, repo, "user", "scheduled", "hourly PR check", at))
	writeHistoryFile(t, filepath.Join(codexSessions, "2026", "11", "12", "rollout-a.jsonl"), at, codexExecMetaLine(t, repo))
	writeHistoryFile(t, filepath.Join(codexSessions, "2026", "11", "12", "rollout-b.jsonl"), at, codexExecMetaLine(t, agentsOnly))

	scan := scanAssistantHistory(assistantHistoryRoots{Home: home, Claude: claudeProjects, Codex: codexSessions}, now)
	if len(scan.Projects) != 1 || scan.Projects[0].Name != "app" {
		t.Fatalf("projects = %+v, want only app", scan.Projects)
	}
	p := scan.Projects[0]
	if p.Sessions14d != 1 || p.SessionsThisWeek != 1 || p.AgentSessions14d != 3 {
		t.Fatalf("counts: mine=%d week=%d agents=%d, want 1 1 3", p.Sessions14d, p.SessionsThisWeek, p.AgentSessions14d)
	}
	if strings.Join(p.Assistants, ",") != "claude" || strings.Join(scan.Assistants, ",") != "claude" {
		t.Fatalf("assistants: project %v scan %v (codex only ran as a worker)", p.Assistants, scan.Assistants)
	}
}

func TestMarkLearnedFromAgentsHidesFromReadersAndKeepsTheFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(agentHomeEnv, filepath.Join(home, "agent-data"))
	knowDir, err := knowledgeDir()
	if err != nil {
		t.Fatal(err)
	}
	memDir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(memDir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	know := buildOKFKnowledgeMarkdown("knowledge-history-a", "Shadow compose for the brief rebuild", "Write a shadow copy.", now, assistantHistorySource, []string{"app"}, nil, []string{"Write a shadow copy."}, "decision")
	know = knowledgeProvenance(know, "", []string{assistantHistorySource}, []string{"app"}, true)
	if !knowledgeProvenanceKnown(know) {
		t.Fatal("fixture provenance not known")
	}
	marked, ok := markLearnedFromAgents(know)
	if !ok || !learnedFromAgents(marked) {
		t.Fatal("know-how not marked")
	}
	if !knowledgeProvenanceKnown(marked) {
		t.Fatal("marking broke the know-how provenance hash")
	}
	if _, again := markLearnedFromAgents(marked); again {
		t.Fatal("marked twice")
	}
	if err := os.WriteFile(filepath.Join(knowDir, "knowledge-history-a.md"), []byte(marked), 0600); err != nil {
		t.Fatal(err)
	}
	kept := buildOKFKnowledgeMarkdown("knowledge-history-b", "Run tests before push", "Run go test.", now, assistantHistorySource, []string{"app"}, nil, []string{"Run go test."}, "how_to")
	if err := os.WriteFile(filepath.Join(knowDir, "knowledge-history-b.md"), []byte(kept), 0600); err != nil {
		t.Fatal(err)
	}
	mem := renderOKFDocument(map[string]any{"type": "Isoform Memory", "title": "You merge PRs every hour", "source": assistantHistorySource}, "## Memory Points\n\n- You merge PRs every hour")
	memMarked, _ := markLearnedFromAgents(mem)
	if err := os.WriteFile(filepath.Join(memDir, "memory-history-a.md"), []byte(memMarked), 0600); err != nil {
		t.Fatal(err)
	}

	entries, err := (&App{}).GetKnowledgeEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "knowledge-history-b" {
		t.Fatalf("know-how readers saw %+v, want only the unmarked entry", entries)
	}
	memories, err := readMemoryEntries(memDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 0 {
		t.Fatalf("About you readers saw %d hidden memories", len(memories))
	}
	for _, path := range []string{filepath.Join(knowDir, "knowledge-history-a.md"), filepath.Join(memDir, "memory-history-a.md")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("hidden file was removed: %v", err)
		}
	}
}

// TestAgentActivityCleanupHidesOnlyAgentLearnedHistory: of two records
// learned from the same hub session, the one cut from an hourly check is
// hidden and the one from the person's own exchange stays. A record whose
// session is gone and the person's own edit are left as they are.
func TestAgentActivityCleanupHidesOnlyAgentLearnedHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(agentHomeEnv, filepath.Join(home, "agent-data"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	repo := filepath.Join(home, "src", "app")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	t1, t2 := base, base.Add(time.Hour)
	var lines []string
	// The person's exchange, ending at t1.
	lines = append(lines,
		claudeOriginLine(t, repo, "user", "human", "Keep release notes short.", t1.Add(-time.Minute)),
		claudeOriginLine(t, repo, "assistant", "", "Noted.", t1),
	)
	// Then 12 turns of hourly checks, ending at t2.
	for i := 0; i < 6; i++ {
		at := t2.Add(-time.Duration(6-i) * time.Minute)
		lines = append(lines,
			claudeOriginLine(t, repo, "user", "scheduled", "Hourly PR check.", at),
			claudeOriginLine(t, repo, "assistant", "", "Merged one PR.", at.Add(time.Second)),
		)
	}
	lines = append(lines, claudeOriginLine(t, repo, "assistant", "", "Done.", t2))
	writeHistoryFile(t, filepath.Join(home, ".claude", "projects", "-app", "hub.jsonl"), t2, lines...)

	knowDir, _ := knowledgeDir()
	memDir, _ := memoryDir()
	if err := os.MkdirAll(memDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeKnow := func(id, title string, at time.Time) string {
		content := buildOKFKnowledgeMarkdown(id, title, title, at, assistantHistorySource, []string{"app"}, nil, []string{title}, "decision")
		doc, _ := parseOKFDocument(content)
		doc.Frontmatter["history_session_at"] = okfTimestamp(at)
		doc.Frontmatter["history_assistant"] = "claude"
		content = renderOKFDocument(doc.Frontmatter, doc.Body)
		path := filepath.Join(knowDir, id+".md")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	writeMem := func(id, title, source string, at time.Time) string {
		content := renderOKFDocument(map[string]any{
			"type": "Isoform Memory", "title": title, "source": source,
			"history_assistant": "claude", "history_session_at": okfTimestamp(at), "history_project": "app",
		}, "## Memory Points\n\n- "+title)
		path := filepath.Join(memDir, id+".md")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	agentKnow := writeKnow("knowledge-history-agent", "Hourly PR check merges green PRs", t2)
	mineKnow := writeKnow("knowledge-history-mine", "Short release notes", t1)
	agentMem := writeMem("memory-history-agent", "You merge PRs every hour", assistantHistorySource, t2)
	goneMem := writeMem("memory-history-gone", "From a deleted session", assistantHistorySource, base.Add(-10*24*time.Hour))
	editedMem := writeMem("memory-history-edited", "Edited by the person", "user_edit", t2)

	app := &App{}
	result, err := app.cleanupAgentLearnedRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.KnowHow != 1 || result.Memories != 1 {
		t.Fatalf("result = %+v, want one know-how and one memory hidden", result)
	}
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("file removed: %v", err)
		}
		return string(data)
	}
	if !learnedFromAgents(read(agentKnow)) || !learnedFromAgents(read(agentMem)) {
		t.Fatal("agent-learned records were not hidden")
	}
	for _, path := range []string{mineKnow, goneMem, editedMem} {
		if learnedFromAgents(read(path)) {
			t.Fatalf("%s was hidden", filepath.Base(path))
		}
	}
}

func TestAgentActivityCleanupRunsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(agentHomeEnv, filepath.Join(home, "agent-data"))
	app := newTranslationTestApp(t, &memoryTranslationFakeRunner{})
	ctx := context.Background()
	app.runAgentActivityCleanup(ctx)
	done, err := app.store.GetUIState(ctx, agentActivityCleanupKey)
	if err != nil || done != "done" {
		t.Fatalf("cleanup marker = %q, %v", done, err)
	}
}
