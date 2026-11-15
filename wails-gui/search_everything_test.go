package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"wails-gui/storage"
)

func searchTestSession(t *testing.T, a *App, id, title string, hidden bool) {
	t.Helper()
	ctx := t.Context()
	if err := a.store.EnsureProjectStructure(ctx, storage.ProjectRow{ID: "search-project", Name: "Search", Path: filepath.Join(t.TempDir(), "project")}, []storage.WorkspaceRow{{ID: "search-workspace", ProjectID: "search-project", Label: "Default", IsDefault: true}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SaveSession(ctx, storage.SessionRow{LocalID: id, Label: title, Kind: func() string {
		if id == "main" {
			return "main"
		}
		return "side"
	}(), ProjectID: "search-project", WorkspaceID: "search-workspace", Hidden: hidden, CreatedAt: 1000}); err != nil {
		t.Fatal(err)
	}
}
func searchTestItem(t *testing.T, a *App, session, id, kind, text string, at int64) {
	t.Helper()
	if _, err := a.store.AppendStreamItem(t.Context(), session, storage.StreamItemRow{ID: id, Kind: kind, Content: text, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
}
func searchTestResults(t *testing.T, a *App, query, place string, limit int) []SearchResult {
	t.Helper()
	result, err := a.SearchEverything(query, limit)
	if err != nil {
		t.Fatal(err)
	}
	for _, failed := range result.FailedPlaces {
		if failed == place {
			t.Fatalf("%s failed for %q: %+v", place, query, result)
		}
	}
	for _, g := range result.Groups {
		if g.Place == place {
			return g.Results
		}
	}
	return nil
}

func TestSearchChatWordsTitlesGatesAndCursor(t *testing.T) {
	a := newMCPReadTestApp(t)
	searchTestSession(t, a, "main", "Main conversation", false)
	searchTestSession(t, a, "old", "Old side project", false)
	searchTestSession(t, a, "hidden", "Hidden", true)
	searchTestSession(t, a, "cron", "Cron session 12345678", false)
	if err := a.store.SaveCronJob(t.Context(), storage.CronJobRow{ID: "job", Name: "Fixture", ScheduleKind: "interval", ScheduleConfig: "{}", TargetKind: "agent", TargetConfig: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.InsertCronRun(t.Context(), storage.CronRunRow{ID: "run", JobID: "job", SessionTag: "cron", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	searchTestSession(t, a, "cron-title", "Cron session troubleshooting", false)
	if got := searchTestResults(t, a, "Cron session troubleshooting", "chat", 5); len(got) != 1 || got[0].Target.SessionID != "cron-title" {
		t.Fatalf("ordinary cron-titled chat lost: %+v", got)
	}

	searchTestItem(t, a, "main", "message", "user", "The Isoform release is ready", 2000)
	searchTestItem(t, a, "old", "old-message", "assistant", "A vintageword in a side chat", 1000)
	for _, kind := range []string{"thinking", "tool", "result"} {
		searchTestItem(t, a, "main", kind, kind, "Isoform hiddenrow", 3000)
	}
	for _, id := range []string{"hidden", "cron"} {
		searchTestItem(t, a, id, id+"-message", "user", "Isoform hiddenrow", 4000)
	}
	got := searchTestResults(t, a, " isoform ", "chat", 0)
	if len(got) != 1 || got[0].Target.Kind != "chat_item" || got[0].Target.ItemID != "message" || got[0].Target.SessionID != "main" {
		t.Fatalf("messages: %+v", got)
	}
	for _, tc := range []struct{ query, kind string }{{"side project", "chat"}, {"vintageword", "chat_item"}} {
		got = searchTestResults(t, a, tc.query, "chat", 0)
		found := false
		for _, r := range got {
			if r.Target.SessionID != "old" {
				t.Fatalf("wrong side chat for %q: %+v", tc.query, r)
			}
			found = found || r.Target.Kind == tc.kind
		}
		if !found {
			t.Fatalf("side chat destination %s for %q missing: %+v", tc.kind, tc.query, got)
		}
	}
	if got = searchTestResults(t, a, "hiddenrow", "chat", 20); len(got) != 0 {
		t.Fatalf("hidden: %+v", got)
	}
	for i := 0; i < 80; i++ {
		searchTestItem(t, a, "main", fmt.Sprint("after-", i), "assistant", "Later text", 5000+int64(i))
	}
	cursor, err := a.FindStreamItemCursor("main", "message")
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, 5, 50} {
		page, err := a.LoadStreamItemsPage("main", cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range page.Items {
			found = found || r.ID == "message"
		}
		if !found {
			t.Fatalf("cursor %d limit %d omitted target", cursor, limit)
		}
	}
	for _, pair := range [][2]string{{"main", "missing"}, {"hidden", "hidden-message"}, {"cron", "cron-message"}, {"old", "message"}} {
		_, err := a.FindStreamItemCursor(pair[0], pair[1])
		var missing *StreamItemNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("missing %v: %v", pair, err)
		}
	}
	if err := a.store.SaveProject(t.Context(), storage.ProjectRow{ID: "search-project", Name: "Search", Path: "hidden-project", Hidden: true}); err != nil {
		t.Fatal(err)
	}
	if got = searchTestResults(t, a, "isoform", "chat", 20); len(got) != 0 {
		t.Fatalf("hidden project: %+v", got)
	}
}

func TestSearchQueryLiteralUnicodeLimitsAndRedaction(t *testing.T) {
	a := newMCPReadTestApp(t)
	searchTestSession(t, a, "main", "Conversation", false)
	secret := "sk-ant-api03-" + strings.Repeat("A", 80)
	texts := []string{"literal 100% underscore_ 中文 ÉCOLE", "literal 100x underscoreX", "isoform " + secret + strings.Repeat(" tail", 100)}
	for i, text := range texts {
		searchTestItem(t, a, "main", fmt.Sprint(i), "assistant", text, 1000+int64(i))
	}
	for _, q := range []string{"100%", "underscore_", "_ 中文", "中", "école"} {
		got := searchTestResults(t, a, q, "chat", 20)
		if len(got) != 1 || got[0].Target.ItemID != "0" {
			t.Fatalf("literal/unicode %q: %+v", q, got)
		}
	}
	for _, q := range []string{"", "a", "_", strings.Repeat("中", 201), "100% missing"} {
		if got := searchTestResults(t, a, q, "chat", 20); len(got) != 0 {
			t.Fatalf("invalid/nonmatch %q: %+v", q, got)
		}
	}
	got := searchTestResults(t, a, "isoform", "chat", 20)
	if len(got) != 1 || strings.Contains(got[0].Snippet, secret) || !strings.Contains(got[0].Snippet, "[...]") || utf8.RuneCountInString(got[0].Snippet) > 140 {
		t.Fatalf("redaction: %+v", got)
	}
	if got := searchTestResults(t, a, secret, "chat", 20); len(got) != 0 {
		t.Fatal("secret query disclosed a row")
	}
	for i := 0; i < 60; i++ {
		searchTestItem(t, a, "main", fmt.Sprint("limit-", i), "user", "boundedword", int64(5000+i))
	}
	for _, tc := range []struct{ limit, want int }{{0, 5}, {2, 2}, {200, 20}} {
		got := searchTestResults(t, a, "boundedword", "chat", tc.limit)
		if len(got) != tc.want {
			t.Fatalf("limit %d: %d", tc.limit, len(got))
		}
		if got[0].Target.ItemID != "limit-59" {
			t.Fatalf("newest: %+v", got)
		}
	}
}

func TestSearchRankingAndIndependentSources(t *testing.T) {
	a := &App{}
	source := func(place string) searchSource {
		return searchSource{place, func(ctx context.Context, _ []string) ([]searchCandidate, error) {
			return []searchCandidate{
				{result: SearchResult{ID: "body", Title: "Other", At: 900}, body: "isoform"},
				{result: SearchResult{ID: "contains", Title: "Use Isoform", At: 800}},
				{result: SearchResult{ID: "prefix", Title: "Isoform guide", At: 700}},
				{result: SearchResult{ID: "exact", Title: "Isoform", At: 600}},
			}, nil
		}}
	}
	slowDone := make(chan struct{})
	sources := []searchSource{source("chat"), {"activity", func(context.Context, []string) ([]searchCandidate, error) { return nil, errors.New("unavailable") }},
		{"person", func(ctx context.Context, _ []string) ([]searchCandidate, error) {
			defer close(slowDone)
			<-ctx.Done()
			return nil, ctx.Err()
		}}, source("goal")}
	result, err := a.searchEverything(t.Context(), "isoform", 2, sources)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.FailedPlaces, ",") != "activity,person" || len(result.Groups) != 2 {
		t.Fatalf("isolation: %+v", result)
	}
	for _, g := range result.Groups {
		if len(g.Results) != 2 || g.Results[0].ID != "exact" || g.Results[1].ID != "prefix" {
			t.Fatalf("ranking/limit: %+v", g)
		}
	}
	<-slowDone
}

func searchTestMoment(t *testing.T, a *App, id, app, bundle, window, triage string) int64 {
	t.Helper()
	at := seedMCPReadActivity(t, a, id, app, bundle, window, "rawonly OCR typed clipboard", triage)
	var runID string
	// Sessions may join one run. Give each privacy case its own run input;
	// this fixture selects the real persisted timeline run, never raw text.
	err := a.activity.store.db.QueryRow(`SELECT id FROM activity_timeline_groups WHERE EXISTS(SELECT 1 FROM json_each(session_ids) WHERE value=?) LIMIT 1`, id).Scan(&runID)
	if err != nil {
		// Protected sessions deliberately produce no timeline run. A forged
		// old derivative must still be denied through its source session.
		runID = "run-" + id
		_, err = a.activity.store.db.Exec(`INSERT INTO activity_timeline_groups(id,first_event_row_id,last_event_row_id,app_key,app_name,start_ms,end_ms,active_ms,event_count,session_ids,trigger_kinds,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, runID, at, at, bundle, app, at, at+60000, 60000, 1, `["`+id+`"]`, `[]`, "closed", at, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := json.Marshal([]string{runID})
	err = a.activity.store.InsertActivity(ActivityRow{ID: id, Title: "Isoform " + id, Description: "Release summary", StartMs: at, EndMs: at + 60000, RunIDs: string(runs), Status: "settled", CreatedAt: at, UpdatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestSearchActivityPrivacyAndNoRawCapture(t *testing.T) {
	for _, gate := range []string{"allowed", "protected", "sensitive", "sensitive_user", "declared_sensitive", "private", "private_flag", "missing_source", "pause", "closed"} {
		t.Run(gate, func(t *testing.T) {
			a := newMCPReadTestApp(t)
			app, bundle, window, triage := "Editor", "com.example.editor", "Release", "dismissed"
			switch gate {
			case "protected":
				triage = "protected"
			case "sensitive":
				app, bundle = "1Password", "com.1password.1password"
			case "private":
				app, bundle, window = "Google Chrome", "com.google.Chrome", "Release - Incognito"
			}
			at := searchTestMoment(t, a, "moment", app, bundle, window, triage)
			if _, err := a.activity.store.db.Exec(`UPDATE activity_events SET data=json_set(data,'$.typedText','typedonly','$.clipboardPreview','clipboardonly') WHERE session_id='moment'`); err != nil {
				t.Fatal(err)
			}
			switch gate {
			case "declared_sensitive":
				if _, err := a.activity.store.db.Exec(`UPDATE activities SET active_ms_by_app='{"1Password":60000}' WHERE id='moment'`); err != nil {
					t.Fatal(err)
				}
			case "sensitive_user":
				if err := a.store.SetUIState(t.Context(), "sensitive_apps", `{"com.example.editor":"Editor"}`); err != nil {
					t.Fatal(err)
				}
			case "private_flag":
				if _, err := a.activity.store.db.Exec(`UPDATE activity_events SET data=json_set(data,'$.privateWindow',json('true')) WHERE session_id='moment'`); err != nil {
					t.Fatal(err)
				}
			case "missing_source":
				if _, err := a.activity.store.db.Exec(`DELETE FROM activity_sessions WHERE id='moment'`); err != nil {
					t.Fatal(err)
				}
			}

			if gate == "pause" {
				raw, _ := json.Marshal([]observationPause{{Start: at - 1, End: at + 60001}})
				if err := a.store.SetUIState(t.Context(), observationPausesKey, string(raw)); err != nil {
					t.Fatal(err)
				}
			}
			if gate == "closed" {
				a.closedEyes.enabled.Store(true)
			}
			got := searchTestResults(t, a, "isoform", "activity", 20)
			if gate == "allowed" {
				if len(got) != 1 || got[0].Target.ActivityID != "moment" || got[0].Target.SessionID != "moment" || got[0].At != at {
					t.Fatalf("control: %+v", got)
				}
			} else if len(got) != 0 {
				t.Fatalf("%s disclosed: %+v", gate, got)
			}
			for _, q := range []string{"rawonly", "typedonly", "clipboardonly"} {
				if got := searchTestResults(t, a, q, "activity", 20); len(got) != 0 {
					t.Fatalf("raw %s capture searched: %+v", q, got)
				}
			}
		})
	}
}

func TestSearchPeopleAliasesHandlesNotMessages(t *testing.T) {
	a := newMCPReadTestApp(t)
	p, err := a.store.CreatePrincipal(t.Context(), storage.PrincipalRow{ID: "alice", Kind: "contact", DisplayName: "Alice", Aliases: []string{"Ally"}, Relationship: "colleague", CreatedFrom: "manual", Notes: "notsearchable"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.BindAppHandle(t.Context(), p.ID, "email", "stable", "@ali"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.BumpPrincipalSighting(t.Context(), p.ID, "email", "", 1000, "messageonly"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"ally", "@ali", "colleague"} {
		got := searchTestResults(t, a, q, "person", 5)
		if len(got) != 1 || got[0].Target.ID != p.ID {
			t.Fatalf("%q: %+v", q, got)
		}
	}
	for _, q := range []string{"messageonly", "notsearchable"} {
		if got := searchTestResults(t, a, q, "person", 5); len(got) != 0 {
			t.Fatalf("private field searched: %+v", got)
		}
	}
}

const searchNoFoldersChildEnv = "ISOFORM_SEARCH_NO_FOLDERS_TEST"

// A search on missing page folders must not create them. The search runs in a
// child test process: every test in this package shares one HOME variable, so a
// background writer left by an earlier test could create the folder during the
// search and the failure would blame the search.
func TestSearchMissingPageFoldersStayMissing(t *testing.T) {
	if os.Getenv(searchNoFoldersChildEnv) == "1" {
		a := newMCPReadTestApp(t)
		root, err := flavorDataDir()
		if err != nil {
			t.Fatal(err)
		}
		for _, place := range []string{"about_you", "know_how"} {
			_ = searchTestResults(t, a, "isoform", place, 5)
		}
		for _, folder := range []string{"memory", "knowledge"} {
			if _, err := os.Stat(filepath.Join(root, folder)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("search created %s", folder)
			}
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestSearchMissingPageFoldersStayMissing$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), searchNoFoldersChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child search: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestSearchMissingPageFoldersStayMissing") {
		t.Fatalf("child search did not run:\n%s", out)
	}
}

func TestSearchEntriesForgetSuppressionFrontmatterAndNoWrites(t *testing.T) {
	a := newMCPReadTestApp(t)
	root, err := flavorDataDir()
	if err != nil {
		t.Fatal(err)
	}
	// TestSearchMissingPageFoldersStayMissing checks that a search creates no folder.
	for _, folder := range []string{"memory", "knowledge"} {
		if err := os.MkdirAll(filepath.Join(root, folder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	content := "# Preference\n- **Source**: frontmatteronly\n\nIsoform bodyword."
	for _, folder := range []string{"memory", "knowledge"} {
		if err := os.WriteFile(filepath.Join(root, folder, "entry.md"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, place := range []string{"about_you", "know_how"} {
		if got := searchTestResults(t, a, "bodyword", place, 5); len(got) != 1 {
			t.Fatalf("%s: %+v", place, got)
		}
		if got := searchTestResults(t, a, "frontmatteronly", place, 5); len(got) != 0 {
			t.Fatalf("metadata: %+v", got)
		}
	}
	dir := filepath.Join(root, "memory")
	if err := recordMemoryTombstone(dir, "entry", []string{content}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := searchTestResults(t, a, "bodyword", "about_you", 5); len(got) != 0 {
		t.Fatalf("forgotten: %+v", got)
	}
	a.closedEyes.enabled.Store(true)
	if got := searchTestResults(t, a, "bodyword", "know_how", 5); len(got) != 1 {
		t.Fatalf("direct knowledge: %+v", got)
	}
}

type searchFakeTopics struct{ chatTopicFake }

func (*searchFakeTopics) SearchTopicNames(context.Context) ([]SearchTopicName, error) {
	return []SearchTopicName{{ID: "work", Name: "Isoform work"}, {ID: "follow", Name: "Isoform followed", Followed: true}}, nil
}
func TestSearchClosedEyesTopicsAndChats(t *testing.T) {
	a := newMCPReadTestApp(t)
	a.topicSource = &searchFakeTopics{}
	searchTestSession(t, a, "main", "Main", false)
	searchTestItem(t, a, "main", "chat", "user", "Isoform words", 1000)
	if got := searchTestResults(t, a, "isoform", "topic", 5); len(got) != 2 {
		t.Fatalf("topics: %+v", got)
	}
	a.closedEyes.enabled.Store(true)
	if got := searchTestResults(t, a, "isoform", "topic", 5); len(got) != 1 || got[0].Target.ID != "follow" {
		t.Fatalf("closed topics: %+v", got)
	}
	if got := searchTestResults(t, a, "isoform", "chat", 5); len(got) != 1 {
		t.Fatalf("closed chat: %+v", got)
	}
	a.topicSource = nil
	if got := searchTestResults(t, a, "isoform", "topic", 5); len(got) != 0 {
		t.Fatalf("missing A1: %+v", got)
	}
}

func TestSearchGoalPageMembership(t *testing.T) {
	a := newMCPReadTestApp(t)
	store := NewPurposeMapStore(a.activity.store.db)
	visible := mobileTestPurpose(t, store, "visible", PurposeKindGoal, "Isoform release", false, nil)
	hidden := mobileTestPurpose(t, store, "hidden", PurposeKindGoal, "Isoform hidden", false, nil)
	if _, err := a.moveUserPurpose(userPurposeTransition{Key: "forget-search-goal", Origin: "test", OpenLoopID: hidden.OpenLoop.ID, Action: userPurposeForget}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got := searchTestResults(t, a, "isoform", "goal", 20)
	if len(got) != 1 || got[0].Target.ID != visible.OpenLoop.ID {
		t.Fatalf("goals: %+v", got)
	}
}

func TestSearchActivityThreadsAndCorrections(t *testing.T) {
	a := newMCPReadTestApp(t)
	at := searchTestMoment(t, a, "one", "Editor", "com.example.editor", "Release", "dismissed")
	if _, err := a.activity.store.db.Exec(`INSERT INTO activity_threads(id,day_start_ms,title,title_basis,created_at,updated_at) VALUES('thread',0,'Isoform thread','["one"]',?,?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := a.activity.store.db.Exec(`INSERT INTO activity_thread_members(activity_id,thread_id,day_start_ms,source,updated_at) VALUES('one','thread',0,'user',?)`, at); err != nil {
		t.Fatal(err)
	}
	got := searchTestResults(t, a, "thread", "activity", 20)
	if len(got) != 1 || got[0].Target.Kind != "activity_thread" || got[0].Target.ThreadID != "thread" || got[0].Target.StartMs != at {
		t.Fatalf("thread: %+v", got)
	}
	if _, err := a.activity.store.db.Exec(`UPDATE activities SET user_title='Corrected title',user_description='Corrected body' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if got := searchTestResults(t, a, "corrected body", "activity", 20); len(got) != 1 || got[0].Title != "Corrected title" {
		t.Fatalf("corrected: %+v", got)
	}
	if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if got := searchTestResults(t, a, "thread", "activity", 20); len(got) != 0 {
		t.Fatalf("withdrawn title basis: %+v", got)
	}
}

func TestSearchPrivacyEditBeforePublicationDropsStaleGroups(t *testing.T) {
	a := newMCPReadTestApp(t)
	searchTestSession(t, a, "main", "Conversation", false)
	searchTestItem(t, a, "main", "one", "user", "Isoform words", 1000)
	sources := []searchSource{{"chat", a.searchChats}, {"topic", func(ctx context.Context, _ []string) ([]searchCandidate, error) {
		if err := a.store.SoftDeleteSession(ctx, "main"); err != nil {
			return nil, err
		}
		return nil, nil
	}}}
	result, err := a.searchEverything(t.Context(), "isoform", 5, sources)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.FailedPlaces, ",") != "chat" {
		t.Fatalf("stale group not withdrawn: %+v", result)
	}
	for _, g := range result.Groups {
		if g.Place == "chat" {
			t.Fatalf("stale chat returned: %+v", g)
		}
	}
}

func TestSearchBrokenActivityStoreDoesNotDropChat(t *testing.T) {
	a := newMCPReadTestApp(t)
	searchTestSession(t, a, "main", "Conversation", false)
	searchTestItem(t, a, "main", "one", "user", "Isoform words", 1000)
	if _, err := a.store.CreatePrincipal(t.Context(), storage.PrincipalRow{ID: "manual", Kind: "contact", DisplayName: "Isoform contact", CreatedFrom: "manual"}); err != nil {
		t.Fatal(err)
	}
	root, err := flavorDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "knowledge"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "knowledge", "entry.md"), []byte("# Isoform reference\n\nUseful words"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.activity.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, place := range []string{"person", "know_how"} {
		if got := searchTestResults(t, a, "isoform", place, 5); len(got) != 1 {
			t.Fatalf("activity failure dropped %s: %+v", place, got)
		}
	}
	if got := searchTestResults(t, a, "isoform", "chat", 5); len(got) != 1 {
		t.Fatalf("activity failure dropped chat: %+v", got)
	}
}

func TestSearchTopicTagsAndSystemRows(t *testing.T) {
	a := newMCPReadTestApp(t)
	searchTestSession(t, a, "main", "Conversation", false)
	row := storage.StreamItemRow{ID: "tagged", Kind: "user", Content: "Isoform tagged words", TopicID: "work"}
	if _, err := a.store.AppendUserStreamItemAtomic(t.Context(), "main", row); err != nil {
		t.Fatal(err)
	}
	if err := a.store.BindStreamItemTopicRun(t.Context(), "main", "tagged", "search-run"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"assistant", "system"} {
		if _, err := a.store.AppendStreamItem(t.Context(), "main", storage.StreamItemRow{ID: kind, Kind: kind, RunID: "search-run", Content: "Isoform tagged words"}); err != nil {
			t.Fatal(err)
		}
	}
	got := searchTestResults(t, a, "tagged words", "chat", 5)
	if len(got) != 3 {
		t.Fatalf("topic rows: %+v", got)
	}
	for _, r := range got {
		want := "work"
		if r.Target.ItemID == "system" {
			want = ""
		}
		if r.TopicID != want {
			t.Fatalf("topic rule: %+v", r)
		}
	}
}

func TestSearchReadsDoNotMutateStores(t *testing.T) {
	a := newMCPReadTestApp(t)
	searchTestSession(t, a, "main", "Conversation", false)
	searchTestItem(t, a, "main", "chat", "user", "Isoform words", 1000)
	searchTestMoment(t, a, "moment", "Editor", "com.example.editor", "Release", "dismissed")
	mainBefore, activityBefore := a.searchRevisions(t.Context())
	_ = searchTestResults(t, a, "isoform", "activity", 5)
	mainAfter, activityAfter := a.searchRevisions(t.Context())
	if !mainBefore.same(mainAfter) || !activityBefore.same(activityAfter) {
		t.Fatal("search changed a database revision")
	}
}

func TestSearchFileForgetBeforePublication(t *testing.T) {
	a := newMCPReadTestApp(t)
	root, err := flavorDataDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	content := "# Preference\n\nIsoform remembered words"
	if err := os.WriteFile(filepath.Join(dir, "entry.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	sources := []searchSource{{"about_you", func(ctx context.Context, words []string) ([]searchCandidate, error) {
		return a.searchEntries(ctx, words, true)
	}}, {"topic", func(ctx context.Context, _ []string) ([]searchCandidate, error) {
		return nil, recordMemoryTombstone(dir, "entry", []string{content}, time.Now())
	}}}
	result, err := a.searchEverything(t.Context(), "isoform", 5, sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range result.Groups {
		if g.Place == "about_you" && len(g.Results) != 0 {
			t.Fatalf("late Forget leaked: %+v", g)
		}
	}
}

func TestSearchCapturedPeopleUseSessionGates(t *testing.T) {
	a := newMCPReadTestApp(t)
	for _, id := range []string{"visible", "protected"} {
		triage := "dismissed"
		if id == "protected" {
			triage = "protected"
		}
		at := seedMCPReadActivity(t, a, id, "Editor", "com.example.editor", "Release", "", triage)
		p, err := a.store.CreatePrincipal(t.Context(), storage.PrincipalRow{ID: id, Kind: "contact", DisplayName: id, Aliases: []string{"capturedalias"}, CreatedFrom: "auto_from_chat"})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.store.BumpPrincipalSighting(t.Context(), p.ID, "editor", id, at, "unmatched preview"); err != nil {
			t.Fatal(err)
		}
	}
	got := searchTestResults(t, a, "capturedalias", "person", 20)
	if len(got) != 1 || got[0].Target.ID != "visible" {
		t.Fatalf("captured people: %+v", got)
	}
	a.closedEyes.enabled.Store(true)
	if got := searchTestResults(t, a, "capturedalias", "person", 20); len(got) != 0 {
		t.Fatalf("closed captured people: %+v", got)
	}
}

func TestSearchMainChatTitleDoesNotHideMatchingMessages(t *testing.T) {
	a := newMCPReadTestApp(t)
	searchTestSession(t, a, "main", "Isoform", false)
	searchTestItem(t, a, "main", "match", "user", "Isoform release discussion", 2000)
	searchTestItem(t, a, "main", "unrelated", "assistant", "Other words", 3000)
	got := searchTestResults(t, a, "isoform", "chat", 5)
	foundTitle, foundMessage := false, false
	for _, r := range got {
		if r.Target.Kind == "chat" {
			foundTitle = true
		}
		if r.Target.ItemID == "match" {
			foundMessage = true
		}
		if r.Target.ItemID == "unrelated" {
			t.Fatalf("title matched unrelated message: %+v", r)
		}
	}
	if !foundTitle || !foundMessage {
		t.Fatalf("title or message destination missing: %+v", got)
	}
}

func TestSearchEntryBodyPreservesBoldFacts(t *testing.T) {
	for _, content := range []string{
		"# Belief\n\n- **Tip**: searchableboldfact\n",
		"# Belief\n- **Source**: metadataonly\n\n- **Tip**: searchableboldfact\n",
		"---\ntype: Isoform Memory\ntitle: Belief\nsource: metadataonly\n---\n\n- **Tip**: searchableboldfact\n",
	} {
		body := searchEntryBody(content)
		if !strings.Contains(body, "searchableboldfact") || strings.Contains(body, "metadataonly") {
			t.Fatalf("body extraction: %q", body)
		}
	}
}
