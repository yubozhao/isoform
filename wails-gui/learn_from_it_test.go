package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func historyConsentHome(t *testing.T) assistantHistoryRoots {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(agentHomeEnv, filepath.Join(home, "isoform"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	roots, err := assistantHistoryRootsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "src", "app")
	now := time.Now()
	writeHistoryFile(t, filepath.Join(roots.Claude, "app", "c.jsonl"), now, claudeOriginLine(t, repo, "user", "human", "Claude text for this test", now))
	writeHistoryFile(t, filepath.Join(roots.Codex, "today", "rollout-test.jsonl"), now, codexMetaLine(t, repo), `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"codex-private-text"}]}}`)
	return roots
}

func saveHistoryChoices(t *testing.T, ids ...string) {
	t.Helper()
	s := learnFromItSettings{Migrated: true, Choices: map[string]learnFromItChoice{}}
	for _, id := range ids {
		s.Choices[id] = learnFromItChoice{true, 1, "person"}
	}
	if err := saveLearnFromIt(s); err != nil {
		t.Fatal(err)
	}
}

// Record directory enumeration, transcript opens (including pass 2 and
// cleanup's second read) and even discovery of the Codex thread database.
func observedHistoryOpens(t *testing.T) *[]string {
	t.Helper()
	var paths []string
	open, dirs, walk, index := historyOpenSession, historyReadDir, historyWalkDir, historyThreadIndexPath
	historyOpenSession = func(path string) (*os.File, error) { paths = append(paths, path); return open(path) }
	historyReadDir = func(path string) ([]os.DirEntry, error) { paths = append(paths, path); return dirs(path) }
	historyWalkDir = func(path string, f fs.WalkDirFunc) error { paths = append(paths, path); return walk(path, f) }
	historyThreadIndexPath = func(root string) string {
		paths = append(paths, filepath.Join(filepath.Dir(root), "thread-index"))
		return index(root)
	}
	t.Cleanup(func() {
		historyOpenSession, historyReadDir, historyWalkDir, historyThreadIndexPath = open, dirs, walk, index
	})
	return &paths
}

func writeConsentHistoryRecord(t *testing.T, dir, id, source string, agents []string, project, legacyAssistant string) {
	t.Helper()
	fields := map[string]any{"source": source, "title": id, "history_project": project, "history_session_at": okfTimestamp(time.Now()), "history_assistant": legacyAssistant}
	if len(agents) > 0 {
		fields["historyAgents"] = agents
	}
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(renderOKFDocument(fields, "A retained record.")), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLearnFromItMigration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cache      []string
		record     string
		off, saved bool
		want       []string
	}{
		{name: "fresh"},
		{name: "both", cache: []string{"claude"}, record: "codex", want: []string{"claude-code", "codex"}},
		{name: "only Codex", record: "codex", want: []string{"codex"}},
		{name: "old global off", cache: []string{"claude", "codex"}, off: true},
		{name: "saved off wins", cache: []string{"claude", "codex"}, saved: true},
		{name: "unattributed", record: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roots := historyConsentHome(t)
			paths := observedHistoryOpens(t)
			app := &App{}
			if tc.cache != nil {
				st := assistantHistoryState{cache: assistantHistoryCache{Version: 1, Projects: []HistoryProject{{Name: "app", Assistants: tc.cache}}}}
				st.saveLocked()
			}
			if tc.record != "" {
				dir, _ := knowledgeDir()
				writeConsentHistoryRecord(t, dir, "history", assistantHistorySource, nil, "app", tc.record)
			}
			if tc.off {
				if err := updateConfig(func(c *yansuConfig) { c.AssistantHistoryOff = true }); err != nil {
					t.Fatal(err)
				}
			}
			if tc.saved {
				if err := saveLearnFromIt(learnFromItSettings{Choices: map[string]learnFromItChoice{"codex": {false, 1, "person"}}}); err != nil {
					t.Fatal(err)
				}
			}
			views, err := app.GetLearnFromIt()
			if err != nil {
				t.Fatal(err)
			}
			settings, err := readLearnFromIt()
			if err != nil || !settings.Migrated {
				t.Fatalf("settings %+v, %v", settings, err)
			}
			for id := range historySources {
				c := settings.Choices[id]
				want := slices.Contains(tc.want, id)
				if v := views[id]; v.On != want || v.AgentID != id || !v.Available || c.On != want {
					t.Fatalf("%s: view %+v, choice %+v", id, v, c)
				}
				if want && (c.ChangedAt <= 0 || c.ChangedBy != "migration") {
					t.Fatal(c)
				}
			}
			if tc.saved && settings.Choices["codex"] != (learnFromItChoice{false, 1, "person"}) {
				t.Fatal("explicit choice changed")
			}
			path, _ := learnFromItPath()
			before, _ := os.ReadFile(path)
			dir, _ := memoryDir()
			writeConsentHistoryRecord(t, dir, "late", assistantHistorySource, []string{"claude", "codex"}, "app", "")
			if _, err := (&App{}).GetLearnFromIt(); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatal("migration ran twice")
			}
			if len(tc.want) == 0 {
				if _, err := app.scanAssistantHistory(t.Context(), roots, time.Now(), ""); err != nil {
					t.Fatal(err)
				}
			}
			if len(*paths) != 0 {
				t.Fatalf("opened %v", *paths)
			}
		})
	}
}

func TestLearnFromItScanAndExcerpt(t *testing.T) {
	roots := historyConsentHome(t)
	saveHistoryChoices(t, "claude-code")
	paths := observedHistoryOpens(t)
	app := &App{}
	scan, err := app.scanAssistantHistory(t.Context(), roots, time.Now(), "")
	if err != nil || !slices.Equal(scan.Assistants, []string{"claude"}) || len(scan.Projects) != 1 {
		t.Fatalf("%+v, %v", scan, err)
	}
	project := scan.Projects[0]
	// A stale in-memory pass 1 can still contain the now-off session.
	off := assistantHistorySession{Assistant: "codex", Path: filepath.Join(roots.Codex, "today", "rollout-test.jsonl")}
	sessions := append([]assistantHistorySession{off}, scan.Sessions[project.ID]...)
	old := assistantHistoryModelCall
	t.Cleanup(func() { assistantHistoryModelCall = old })
	var prompt string
	assistantHistoryModelCall = func(_ context.Context, _ *App, p string) (string, error) { prompt = p; return "{}", nil }
	if _, called, err := app.runAssistantHistoryProject(t.Context(), project, sessions, "English"); err != nil || !called || !strings.Contains(prompt, "Claude text") || strings.Contains(prompt, "codex-private-text") || strings.Contains(prompt, off.Path) {
		t.Fatalf("prompt %q, called %t, error %v", prompt, called, err)
	}
	if _, called, err := app.runAssistantHistoryProject(t.Context(), project, []assistantHistorySession{off}, "English"); called || err != nil {
		t.Fatalf("off-only project: called %t, error %v", called, err)
	}
	for _, path := range *paths {
		if !strings.HasPrefix(path, roots.Claude+string(os.PathSeparator)) && path != roots.Claude {
			t.Errorf("opened %s", path)
		}
	}
}

func TestLearnFromItCleanupSourceOpens(t *testing.T) {
	for _, mode := range []string{"only Claude", "old global off", "unreadable", "Codex fallback"} {
		t.Run(mode, func(t *testing.T) {
			roots := historyConsentHome(t)
			dir, _ := memoryDir()
			writeConsentHistoryRecord(t, dir, "old-claude", assistantHistorySource, nil, "app", "claude")
			// An unplaceable Codex record would otherwise consult its index.
			writeConsentHistoryRecord(t, dir, "old-codex", assistantHistorySource, nil, "archived", "codex")
			switch mode {
			case "only Claude":
				saveHistoryChoices(t, "claude-code")
			case "Codex fallback":
				saveHistoryChoices(t, "codex")
			case "unreadable":
				saveHistoryChoices(t)
				path, _ := learnFromItPath()
				if err := os.WriteFile(path, []byte(`{"migrated":true,"choices":{"codex":{"on":true}}}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "old global off":
				if err := updateConfig(func(c *yansuConfig) { c.AssistantHistoryOff = true }); err != nil {
					t.Fatal(err)
				}
			}
			paths := observedHistoryOpens(t)
			app := &App{}
			_, err := app.cleanupAgentLearnedRecords(t.Context())
			if (err != nil) != (mode == "unreadable") {
				t.Fatal(err)
			}
			switch mode {
			case "only Claude":
				if !slices.Contains(*paths, filepath.Join(roots.Claude, "app", "c.jsonl")) {
					t.Fatalf("no Claude read: %v", *paths)
				}
				for _, path := range *paths {
					if strings.HasPrefix(path, filepath.Dir(roots.Codex)) {
						t.Fatalf("opened %s", path)
					}
				}
			case "Codex fallback":
				if !slices.Contains(*paths, filepath.Join(filepath.Dir(roots.Codex), "thread-index")) {
					t.Fatalf("index guard unexercised: %v", *paths)
				}
			default:
				if len(*paths) != 0 {
					t.Fatalf("opened %v", *paths)
				}
			}
		})
	}
}

func TestLearnFromItUnreadableAndClosedEyes(t *testing.T) {
	roots := historyConsentHome(t)
	app := &App{store: newEngagementTestRig(t).store}
	if err := app.store.SetUIState(t.Context(), onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	saveHistoryChoices(t)
	path, _ := learnFromItPath()
	paths := observedHistoryOpens(t)
	for _, raw := range []string{"{", "null", `{"migrated":true,"choices":{"codex":null}}`, `{"choices":{"codex":{"changedAt":1,"changedBy":"person"}}}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if on, err := learnFromItOn("codex"); on || err == nil {
			t.Fatalf("bad consent %q: %t %v", raw, on, err)
		}
		if app.startAssistantHistoryScan(true) {
			t.Fatal("unreadable started scan")
		}
		if _, err := app.scanAssistantHistory(t.Context(), roots, time.Now(), ""); err == nil {
			t.Fatal("unreadable source accepted")
		}
	}
	saveHistoryChoices(t)
	app.closedEyes.enabled.Store(true)
	if err := app.SetLearnFromIt("codex", true); err != nil {
		t.Fatal(err)
	}
	if on, err := learnFromItOn("codex"); err != nil || !on {
		t.Fatal(on, err)
	}
	if app.assistantHistory.running {
		t.Fatal("paused scan started")
	}
	if _, err := app.scanAssistantHistory(t.Context(), roots, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if len(*paths) != 0 {
		t.Fatalf("opened %v", *paths)
	}
	app.closedEyes.enabled.Store(false)
	scan, err := app.scanAssistantHistory(t.Context(), roots, time.Now(), "")
	if err != nil || !slices.Equal(scan.Assistants, []string{"codex"}) {
		t.Fatal(scan.Assistants, err)
	}
}

func TestLearnFromItSelectiveForget(t *testing.T) {
	app, dir := newForgetTestApp(t)
	saveHistoryChoices(t, "claude-code", "codex")
	know, _ := knowledgeDir()
	cache := assistantHistoryCache{Version: 1, Assistants: []string{"claude", "codex"}, Projects: []HistoryProject{{ID: "c", Name: "solo", Assistants: []string{"claude"}}, {ID: "m", Name: "shared", Assistants: []string{"claude", "codex"}}}}
	st := assistantHistoryState{cache: cache}
	st.saveLocked()
	fixtures := []struct {
		id      string
		agents  []string
		project string
		keep    bool
	}{
		{"only-claude", []string{"claude"}, "shared", true}, {"only-codex", []string{"codex"}, "shared", false}, {"mixed", []string{"claude", "codex"}, "shared", false}, {"legacy-solo", nil, "solo", true}, {"legacy-mixed", nil, "shared", false}, {"legacy-unknown", nil, "gone", false},
	}
	for _, f := range fixtures {
		for _, folder := range []string{dir, know} {
			writeConsentHistoryRecord(t, folder, f.id, assistantHistorySource, f.agents, f.project, "")
		}
	}
	if err := app.SetLearnFromIt("codex", false); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		for _, folder := range []string{dir, know} {
			_, err := os.Stat(filepath.Join(folder, f.id+".md"))
			if (err == nil) != f.keep || (err != nil && !os.IsNotExist(err)) {
				t.Errorf("%s: %v (keep %t)", f.id, err, f.keep)
			}
		}
	}
	var rebuilt assistantHistoryState
	rebuilt.loadLocked()
	if !slices.Equal(rebuilt.cache.Assistants, []string{"claude"}) || len(rebuilt.cache.Projects) != 1 || rebuilt.cache.Projects[0].ID != "c" {
		t.Fatalf("cache %+v", rebuilt.cache)
	}
	if on, err := app.GetReadAssistantHistory(); !on || err != nil {
		t.Fatal(on, err)
	}
}

func TestLearnFromItCompatibility(t *testing.T) {
	historyConsentHome(t)
	app := &App{}
	for _, on := range []bool{true, false} {
		if err := app.SetReadAssistantHistory(on); err != nil {
			t.Fatal(err)
		}
		got, err := app.GetReadAssistantHistory()
		if err != nil || got != on {
			t.Fatal(got, err)
		}
		settings, err := readLearnFromIt()
		if err != nil {
			t.Fatal(err)
		}
		agents, err := app.GetYourAgents()
		if err != nil {
			t.Fatal(err)
		}
		for id := range historySources {
			c := settings.Choices[id]
			if c.On != on || c.ChangedBy != "person" || c.ChangedAt <= 0 {
				t.Fatal(c)
			}
			for _, row := range agents.Agents {
				if row.ID == id && row.LearnFromIt != on {
					t.Fatalf("agent %+v", row)
				}
			}
		}
	}
}

func TestLearnFromItHistoryReaderInventory(t *testing.T) {
	// The manual memory importer reads Markdown in projects/*/memory.
	// Session roots, transcript scanners, and the index resolver belong
	// solely to the consent-gated history pipeline.
	rootPattern := regexp.MustCompile(`[.]claude[/"]\s*(,\s*")?projects|[.]codex[/"]\s*(,\s*")?sessions|filepath.Join\((claude|codex)(Dir|Home)?, "(projects|sessions)"\)`)
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" || d.Name() == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(path)
		if rootPattern.Match(data) && name != "../wails-gui/assistant_history_scan.go" && name != "../wails-gui/ai_memory_scanner.go" {
			t.Errorf("review history root access in %s", name)
		}
		for call, permittedFiles := range map[string][]string{
			"scanClaudeHistorySessions(": {"../wails-gui/assistant_history_scan.go"},
			"scanCodexHistorySessions(":  {"../wails-gui/assistant_history_scan.go"},
			"readCodexThreadSpans(":      {"../wails-gui/agent_activity_learned.go", "../wails-gui/agent_activity_codex_threads.go"},
		} {
			if !strings.Contains(string(data), call) {
				continue
			}
			if !slices.Contains(permittedFiles, name) {
				t.Errorf("history source bypass %s in %s", call, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLearnFromItNewProvenance(t *testing.T) {
	historyConsentHome(t)
	app := &App{}
	sessions := []assistantHistorySession{{Assistant: "claude", At: time.Now()}, {Assistant: "codex", At: time.Now()}}
	var reply assistantHistoryReply
	if err := json.Unmarshal([]byte(`{"beliefs":[{"text":"Runs tests before every release","session":1,"sessions":[1,2]}],"knowHow":[{"title":"Release notes","takeaway":"Check the release notes.","kind":"decision","session":2}]}`), &reply); err != nil {
		t.Fatal(err)
	}
	u := buildAssistantHistoryUpdate(reply, sessions, "", time.Time{})
	p := HistoryProject{ID: "project", Name: "app"}
	if n, err := app.storeAssistantHistoryBeliefs(p, u.Beliefs, time.Now()); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := app.storeAssistantHistoryKnowHow(p, u.KnowHow, time.Now()); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	mem, _ := memoryDir()
	know, _ := knowledgeDir()
	for _, tc := range []struct {
		folder, id string
		names      []string
	}{{mem, assistantHistoryRecordID("memory-history-", p.ID, beliefInSecondPerson(u.Beliefs[0].Text)), []string{"claude", "codex"}}, {know, assistantHistoryRecordID("knowledge-history-", p.ID, u.KnowHow[0].Title), []string{"codex"}}} {
		data, err := os.ReadFile(filepath.Join(tc.folder, tc.id+".md"))
		if err != nil {
			t.Fatal(err)
		}
		doc, _ := parseOKFDocument(string(data))
		if got := okfStringSlice(doc.Frontmatter, "historyAgents"); !slices.Equal(got, tc.names) {
			t.Fatalf("%s: historyAgents = %v", tc.id, got)
		}
	}
}

func TestLearnFromItEnablingScansOnlyNewSource(t *testing.T) {
	roots := historyConsentHome(t)
	app := &App{store: newEngagementTestRig(t).store}
	if err := app.store.SetUIState(t.Context(), onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	saveHistoryChoices(t, "claude-code")
	paths := observedHistoryOpens(t)
	old := assistantHistoryModelCall
	t.Cleanup(func() { assistantHistoryModelCall = old })
	assistantHistoryModelCall = func(context.Context, *App, string) (string, error) { return "{}", nil }
	if err := app.SetLearnFromIt("codex", true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		app.assistantHistory.mu.Lock()
		running := app.assistantHistory.running
		app.assistantHistory.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if !slices.Contains(*paths, roots.Codex) {
		t.Fatalf("Codex scan did not start: %v", *paths)
	}
	for _, path := range *paths {
		if strings.HasPrefix(path, roots.Claude) {
			t.Fatalf("reopened Claude at %s", path)
		}
	}
}

func TestLearnFromItInterruptedForgetRetriesBeforeReading(t *testing.T) {
	roots := historyConsentHome(t)
	s := learnFromItSettings{Migrated: true, Choices: map[string]learnFromItChoice{"claude-code": {true, 1, "person"}, "codex": {false, 2, "person"}}, Forget: "codex", ForgetSources: []string{"erased-parent"}}
	if err := saveLearnFromIt(s); err != nil {
		t.Fatal(err)
	}
	dir, _ := knowledgeDir()
	writeConsentHistoryRecord(t, dir, "withdrawn", assistantHistorySource, []string{"codex"}, "app", "")
	child := renderOKFDocument(map[string]any{"title": "derived", "source": "observed", "source_ids": []string{"erased-parent"}}, "Derived know-how.")
	if err := os.WriteFile(filepath.Join(dir, "derived.md"), []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	paths := observedHistoryOpens(t)
	app := &App{}
	if _, err := app.scanAssistantHistory(t.Context(), roots, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if len(*paths) != 0 {
		t.Fatalf("opened during unfinished Forget: %v", *paths)
	}
	if !app.prepareHistorySchedule() {
		t.Fatal("did not recover withdrawal")
	}
	if _, err := os.Stat(filepath.Join(dir, "withdrawn.md")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "derived.md")); !os.IsNotExist(err) {
		t.Fatal("retained descendant of previously erased source", err)
	}
	got, err := readLearnFromIt()
	if err != nil || got.Forget != "" || !got.Choices["claude-code"].On {
		t.Fatal(got, err)
	}
}
