package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/interests"
	"wails-gui/storage"
)

var interestTestNow = time.Date(2026, 10, 15, 8, 0, 0, 0, time.Local)

func interestHoursAgo(h float64) time.Time {
	return interestTestNow.Add(-time.Duration(h * float64(time.Hour)))
}

func itemTexts(items []interests.Item) string {
	var parts []string
	for _, it := range items {
		parts = append(parts, string(it.Source)+":"+it.Text)
	}
	return strings.Join(parts, "\n")
}

func TestInterestItemsApplyEveryExclusion(t *testing.T) {
	protectTopicTestApp(t)
	in := interestInput{
		Now:    interestTestNow,
		From:   interestTestNow.Add(-interests.RefreshWindow),
		Pauses: []observationPause{{Start: interestHoursAgo(30).UnixMilli(), End: interestHoursAgo(29).UnixMilli()}},
		Activities: []ActivityRow{
			{ID: "a1", Title: "Research smart hat products", Description: "Reading about LifeBeam and Ambiq smart hats.",
				ActiveMsByApp: `{"Safari": 600000}`, StartMs: interestHoursAgo(3).UnixMilli(),
				Entities: `[{"type":"topic","name":"smart hat"},{"type":"person","name":"Ana Lopez"}]`},
			{ID: "a2", Title: "Writing in the diary secret", ActiveMsByApp: `{"Diary": 600000}`, StartMs: interestHoursAgo(3).UnixMilli()},
			{ID: "a3", Title: "Reply to the group chat secret", ActiveMsByApp: `{"WeChat": 600000}`, StartMs: interestHoursAgo(3).UnixMilli()},
			{ID: "a4", Title: "paused activity secret", ActiveMsByApp: `{"Safari": 600000}`, StartMs: interestHoursAgo(29.5).UnixMilli()},
			{ID: "a5", Title: "Email ana@example.com about the hat", ActiveMsByApp: `{"Safari": 60000}`, StartMs: interestHoursAgo(2).UnixMilli()},
		},
		Runs: []interestRun{
			{ID: "r1", AppName: "ChatGPT", StartMs: interestHoursAgo(3).UnixMilli(), Type: "research",
				Entities: []string{"Seeed XIAO ESP32-S3 Sense", "ChatGPT", "https://example.com/x", "yetone", "Priya Raman", "Safari", "Plaud"}},
			{ID: "r2", AppName: "ChatGPT", StartMs: interestHoursAgo(2).UnixMilli(), Type: "research", Entities: []string{"Plaud"}},
			{ID: "r3", AppName: "System Settings", StartMs: interestHoursAgo(2).UnixMilli(), Type: "system", Entities: []string{"Touch ID settings secret"}},
			{ID: "r4", AppName: "WeChat", StartMs: interestHoursAgo(2).UnixMilli(), Type: "browsing", Entities: []string{"group chat secret"}},
			{ID: "r5", AppName: "Safari", StartMs: interestHoursAgo(29.5).UnixMilli(), Type: "browsing", Entities: []string{"paused run secret"}},
		},
		Goals: []interestGoal{
			{ID: "g1", Kind: "opportunity", Title: "Research wearable tech for hats", At: interestHoursAgo(4), Seen: 3},
		},
		Memories: []MemoryEntry{
			{ID: "m1", Dimension: "topic_highlight", Apps: []string{"ChatGPT"}, UpdatedAt: interestHoursAgo(1).UnixMilli(),
				Content: "# Hat\n\n## Memory Points\n\n- I am building a wearable AI baseball cap\n- I dictate prompts in Chinese\n"},
			{ID: "m2", Dimension: "topic_highlight", Apps: []string{"ChatGPT", "Zoom"}, Content: "## Memory Points\n\n- I met a client secret\n"},
			{ID: "m3", Dimension: "agentic_memory", Content: "## Memory Points\n\n- assistant instruction secret\n"},
			{ID: "m4", Dimension: "deep_user_model", Apps: []string{"Claude"}, Content: "## Memory Points\n\n- I am a founder building a personal AI app\n"},
		},
		Sessions: []storage.SessionRow{
			{LocalID: "c1", Label: "Isoform", Kind: storage.SessionKindMain, UpdatedAt: interestHoursAgo(1).UnixMilli()},
			{LocalID: "c2", Label: "Silent speech sensors", Kind: storage.SessionKindSide, UpdatedAt: interestHoursAgo(1).UnixMilli()},
			{LocalID: "c3", Label: "Transcript: standup secret", UpdatedAt: interestHoursAgo(1).UnixMilli()},
			{LocalID: "c4", Label: "Automation run secret", AutomationResult: true, UpdatedAt: interestHoursAgo(1).UnixMilli()},
			{LocalID: "c5", Label: "Meeting chat secret", MeetingID: "m", UpdatedAt: interestHoursAgo(1).UnixMilli()},
		},
		Visits: []BrowserVisitRow{
			{URL: "https://www.google.com/search?q=smart+hat+camera", Title: "smart hat camera - Google Search", VisitedAt: interestHoursAgo(2).UnixMilli(), Browser: "Chrome"},
			{URL: "https://www.google.com/search?q=private+secret", Title: "private secret - Google Search - Incognito", VisitedAt: interestHoursAgo(2).UnixMilli(), Browser: "Chrome"},
			{URL: "https://www.google.com/search?q=canary+secret", Title: "canary secret - Google Search", VisitedAt: interestHoursAgo(2).UnixMilli(), Browser: "Chrome Canary"},
		},
		Calendar: []storage.CalendarEventRow{{OrganizerName: "Ben Ito", Attendees: []storage.CalendarAttendeeRow{{Name: "Chen Wei", Email: "chen.wei@example.com"}}}},
		People:   []string{"Bo"},
	}
	items, names := interestItems(in)
	got := itemTexts(items)
	if strings.Contains(got, "secret") {
		t.Fatalf("excluded evidence leaked:\n%s", got)
	}
	for _, want := range []string{
		"goal:Research wearable tech for hats (opportunity, seen 3 times)",
		"about:I am building a wearable AI baseball cap",
		"about:I am a founder building a personal AI app",
		"chat:Silent speech sensors",
		"search:smart hat camera",
		"activity:Research smart hat products. Reading about LifeBeam and Ambiq smart hats. Named: smart hat",
		"named:Plaud (in 2 runs)",
		"named:Seeed XIAO ESP32-S3 Sense (in 1 runs)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, leaked := range []string{"Ana Lopez", "ana@example.com", "yetone", "Priya", "named:ChatGPT", "named:Safari", "example.com/x"} {
		if strings.Contains(got, leaked) && leaked != "yetone" {
			t.Errorf("%q reached the evidence:\n%s", leaked, got)
		}
	}
	// "yetone" is not a known name here, so it stays evidence; the person
	// entity and the calendar names are kept for the checks.
	for _, want := range []string{"Ana Lopez", "Ben Ito", "Chen Wei", "chen.wei@example.com", "Bo"} {
		found := false
		for _, n := range names {
			found = found || n == want
		}
		if !found {
			t.Errorf("names miss %q: %v", want, names)
		}
	}
	// Priority order: goals, About you, chats, searches, activities, named.
	order := []interests.Source{}
	for _, it := range items {
		if len(order) == 0 || order[len(order)-1] != it.Source {
			order = append(order, it.Source)
		}
	}
	want := []interests.Source{interests.SourceGoal, interests.SourceAbout, interests.SourceChat, interests.SourceSearch, interests.SourceActivity, interests.SourceNamed}
	if strings.Join(sourcesOf(order), ",") != strings.Join(sourcesOf(want), ",") {
		t.Errorf("order %v, want %v", order, want)
	}
	// Typed searches and named things are kept by hash only.
	for _, it := range items {
		if (it.Source == interests.SourceSearch || it.Source == interests.SourceNamed) && strings.Contains(it.Ref, "hat") {
			t.Errorf("ref %q carries text", it.Ref)
		}
	}
}

func sourcesOf(list []interests.Source) []string {
	var out []string
	for _, s := range list {
		out = append(out, string(s))
	}
	return out
}

func TestInterestItemsUnreadablePauseLogDropsObservedEvidence(t *testing.T) {
	in := interestInput{
		Now: interestTestNow, From: interestTestNow.Add(-interests.RefreshWindow), PausesUnknown: true,
		Activities: []ActivityRow{{ID: "a1", Title: "Research smart hats", ActiveMsByApp: `{"Safari": 600000}`, StartMs: interestHoursAgo(3).UnixMilli()}},
		Runs:       []interestRun{{AppName: "Safari", StartMs: interestHoursAgo(3).UnixMilli(), Type: "research", Entities: []string{"Plaud"}}},
		Goals:      []interestGoal{{ID: "g1", Kind: "goal", Title: "Build a smart hat", At: interestHoursAgo(3)}},
	}
	items, _ := interestItems(in)
	if got := itemTexts(items); got != "goal:Build a smart hat (goal)" {
		t.Fatalf("with no pause log only the Purpose Map stays, got:\n%s", got)
	}
}

func TestInterestsTimingAndMode(t *testing.T) {
	now := interestTestNow
	stamp := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339Nano) }
	today := now.Format("2006-01-02")
	activities := func(n int, at time.Time) []interests.Item {
		var out []interests.Item
		for i := 0; i < n; i++ {
			out = append(out, interests.Item{Source: interests.SourceActivity, At: at})
		}
		return out
	}
	stored := []interests.Interest{{ID: "x", Name: "smart hats"}}
	cases := []struct {
		name  string
		st    interestsStateFile
		items []interests.Item
		mode  interests.Mode
		ok    bool
	}{
		{"first pass with a month of activity is the review", interestsStateFile{}, activities(20, now.Add(-time.Hour)), interests.ModeConsolidate, true},
		{"first pass with little activity is a refresh", interestsStateFile{}, activities(6, now.Add(-time.Hour)), interests.ModeRefresh, true},
		{"too little activity waits", interestsStateFile{}, activities(3, now.Add(-time.Hour)), "", false},
		{"refresh needs new activity since the last pass",
			interestsStateFile{Interests: stored, RefreshedAt: stamp(21 * time.Hour), ConsolidatedAt: stamp(48 * time.Hour)},
			append(activities(4, now.Add(-time.Hour)), activities(10, now.Add(-30*time.Hour))...), "", false},
		{"refresh with enough new activity",
			interestsStateFile{Interests: stored, RefreshedAt: stamp(21 * time.Hour), ConsolidatedAt: stamp(48 * time.Hour)},
			activities(5, now.Add(-time.Hour)), interests.ModeRefresh, true},
		{"too soon after the last pass",
			interestsStateFile{Interests: stored, RefreshedAt: stamp(5 * time.Hour), ConsolidatedAt: stamp(24 * time.Hour)},
			activities(9, now.Add(-time.Hour)), "", false},
		{"one pass a day is the limit",
			interestsStateFile{Interests: stored, RefreshedAt: stamp(21 * time.Hour), ConsolidatedAt: stamp(48 * time.Hour), RunsOn: today, RunsToday: 1},
			activities(9, now.Add(-time.Hour)), "", false},
		{"a month after the last review",
			interestsStateFile{Interests: stored, RefreshedAt: stamp(time.Hour), ConsolidatedAt: stamp(31 * 24 * time.Hour), RunsOn: today, RunsToday: 3},
			activities(20, now.Add(-48*time.Hour)), interests.ModeConsolidate, true},
		{"a failed refresh backs refreshes off",
			interestsStateFile{Interests: stored, ConsolidatedAt: stamp(24 * time.Hour), FailedAt: stamp(30 * time.Minute), Failures: 1},
			activities(20, now.Add(-time.Minute)), "", false},
		{"a second failed refresh backs off longer",
			interestsStateFile{Interests: stored, ConsolidatedAt: stamp(24 * time.Hour), FailedAt: stamp(90 * time.Minute), Failures: 2},
			activities(20, now.Add(-time.Minute)), "", false},
		{"after the refresh back-off",
			interestsStateFile{Interests: stored, ConsolidatedAt: stamp(24 * time.Hour), FailedAt: stamp(3 * time.Hour), Failures: 2},
			activities(20, now.Add(-time.Minute)), interests.ModeRefresh, true},
		{"a failed review falls through to a refresh",
			interestsStateFile{ConsolidateFailedAt: stamp(time.Hour), ConsolidateFailures: 1},
			activities(20, now.Add(-time.Hour)), interests.ModeRefresh, true},
		{"a failed review waits a day",
			interestsStateFile{Interests: stored, RefreshedAt: stamp(5 * time.Hour), ConsolidateFailedAt: stamp(23 * time.Hour), ConsolidateFailures: 1},
			activities(20, now.Add(-48*time.Hour)), "", false},
		{"and is tried again after it",
			interestsStateFile{Interests: stored, RefreshedAt: stamp(5 * time.Hour), ConsolidateFailedAt: stamp(25 * time.Hour), ConsolidateFailures: 1},
			activities(20, now.Add(-48*time.Hour)), interests.ModeConsolidate, true},
	}
	for _, c := range cases {
		consolidate, refresh := interestsTiming(c.st, now)
		mode, ok := "", false
		if consolidate || refresh {
			var m interests.Mode
			m, ok = interestsMode(c.st, consolidate, refresh, c.items)
			mode = string(m)
		}
		if ok != c.ok || interests.Mode(mode) != c.mode {
			t.Errorf("%s: got %q %v, want %q %v", c.name, mode, ok, c.mode, c.ok)
		}
	}
}

func TestRefreshItemsKeepWhatDescribesTheUserNow(t *testing.T) {
	since := interestHoursAgo(30)
	items := []interests.Item{
		{Ref: "goal:1", Source: interests.SourceGoal, At: interestHoursAgo(300)},
		{Ref: "about:1", Source: interests.SourceAbout, At: interestHoursAgo(300)},
		{Ref: "activity:old", Source: interests.SourceActivity, At: interestHoursAgo(40)},
		{Ref: "activity:new", Source: interests.SourceActivity, At: interestHoursAgo(2)},
		{Ref: "named:old", Source: interests.SourceNamed, At: interestHoursAgo(40)},
	}
	var refs []string
	for _, it := range refreshItems(items, since) {
		refs = append(refs, it.Ref)
	}
	if got := strings.Join(refs, ","); got != "goal:1,about:1,activity:new" {
		t.Fatalf("kept %s", got)
	}
}

// fakeInterestsService runs passes on fixtures and a scripted model.
func fakeInterestsService(t *testing.T, answers ...string) (*interestsService, *[]string) {
	t.Helper()
	dir := t.TempDir()
	var prompts []string
	now := interestTestNow
	svc := &interestsService{
		store: newInterestsStore(dir),
		now:   func() time.Time { return now },
		collect: func(from, now time.Time) interestInput {
			in := interestInput{Now: now, From: from, People: []string{"Bo"}}
			for i := 0; i < 20; i++ {
				in.Activities = append(in.Activities, ActivityRow{
					ID: "a" + string(rune('a'+i)), Title: "Research smart hat cameras", ActiveMsByApp: `{"Safari": 300000}`,
					StartMs: now.Add(-time.Duration(i+1) * time.Hour).UnixMilli(),
				})
			}
			in.Goals = []interestGoal{{ID: "g1", Kind: "goal", Title: "Study Magpie plugin system", At: now.Add(-time.Hour)}}
			return in
		},
		allowed: func() bool { return true },
		// The fake service may run the rewrite pass; its fixtures carry no
		// re-derived references, so the trigger stays inert unless a test
		// seeds one.
		rewriteAllowed: func() bool { return true },
		model: func(_ context.Context, prompt, _ string) (string, error) {
			prompts = append(prompts, prompt)
			if len(answers) == 0 {
				return "", errors.New("model unavailable")
			}
			answer := answers[0]
			answers = answers[1:]
			return answer, nil
		},
		lightModel: func() string { return "haiku" },
		mainModel:  func() string { return "sonnet" },
		user: func() interestsUserState {
			return interestsUserState{Removed: []string{"Apple Pay settings"}, Pinned: map[string]bool{}, Hints: []string{"hat", "mAh"}}
		},
		language: func() string { return "English" },
	}
	return svc, &prompts
}

func TestInterestsServiceRunsChecksAndKeepsTheList(t *testing.T) {
	first := `{"interests":[
		{"name":"wearable AI camera hats","why":"You are prototyping a smart cap.","kind":"project","confidence":0.9,"evidence":["A1","A2","A99"],"queries":["smart hat camera","Bo smart hat"]},
		{"name":"Magpie plugin system open source","why":"You study its plugins.","kind":"research","confidence":0.8,"evidence":["G1"],"queries":["open source AI agent plugin architecture"]},
		{"name":"Isoform memory feature","kind":"theme","evidence":["A1"]},
		{"name":"Apple Pay settings on Mac","kind":"routine","evidence":["A3"]},
		{"name":"invented interest","kind":"theme","evidence":["Z1"]}
	]}`
	second := "```json\n" + `{"interests":[
		{"name":"wearable AI camera hat","why":"Still building it.","kind":"project","confidence":0.85,"evidence":["A1"],"queries":["ESP32-S3 wearable camera"]},
		{"name":"on-device speech recognition","why":"You fix recording quality.","kind":"research","confidence":0.6,"evidence":["A2"],"queries":["on-device speaker diarization"]}
	]}` + "\n```"
	svc, prompts := fakeInterestsService(t, first, second)

	if !svc.maybeRun(context.Background(), "test") {
		t.Fatal("the first pass did not run")
	}
	st := svc.store.load()
	if len(st.Interests) != 2 || st.ConsolidatedAt == "" || st.RunsToday != 1 {
		t.Fatalf("after the first pass: %+v", st)
	}
	if !strings.Contains((*prompts)[0], "monthly review") && !strings.Contains((*prompts)[0], "Return 5 to 10 interests") {
		t.Fatalf("unexpected prompt")
	}
	hat := st.Interests[0]
	if hat.Name != "wearable AI camera hats" || strings.Join(hat.Queries, "|") != "smart hat camera" || len(hat.Evidence) != 2 {
		t.Fatalf("hat %+v", hat)
	}
	raw, _ := json.Marshal(st)
	for _, leaked := range []string{"Research smart hat cameras", "Study Magpie"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("the stored list keeps evidence text %q", leaked)
		}
	}

	// Too soon for another pass.
	if svc.maybeRun(context.Background(), "test") {
		t.Fatal("a second pass ran right away")
	}
	// The next day new activity came in: a light refresh keeps the id and
	// the name of the hat interest and adds a new one.
	later := interestTestNow.Add(21 * time.Hour)
	svc.now = func() time.Time { return later }
	if !svc.maybeRun(context.Background(), "test") {
		t.Fatal("the refresh did not run")
	}
	if !strings.Contains((*prompts)[1], "- wearable AI camera hats - You are prototyping a smart cap.") {
		t.Fatalf("the refresh was not anchored on the stored list")
	}
	st = svc.store.load()
	byID := map[string]interests.Interest{}
	for _, it := range st.Interests {
		byID[it.ID] = it
	}
	if got := byID[hat.ID]; got.Name != "wearable AI camera hats" || got.Why != "Still building it." {
		t.Fatalf("the hat interest lost its id or name: %+v", st.Interests)
	}
	// A new day: the refresh is the day's first pass.
	if len(st.Interests) != 3 || st.RunsToday != 1 {
		t.Fatalf("after the refresh: %d interests, %d runs", len(st.Interests), st.RunsToday)
	}

	// The model fails: the list stays and the next pass backs off.
	svc.now = func() time.Time { return later.Add(21 * time.Hour) }
	if svc.maybeRun(context.Background(), "test") {
		t.Fatal("a failed pass reported success")
	}
	st = svc.store.load()
	if len(st.Interests) != 3 || st.Failures != 1 {
		t.Fatalf("after a failure: %+v", st)
	}
	if err := svc.forget(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(svc.store.path), interestsStateFileName)); !os.IsNotExist(err) {
		t.Fatalf("forget left the file: %v", err)
	}
}

func TestInterestsRewritePassRunsOnceAfterUpgrade(t *testing.T) {
	answer := `{"interests":[{"name":"silent speech EMG wearable","why":"You follow silent speech sensor research.","kind":"research","confidence":0.9,"evidence":["A1"],"queries":["silent speech EMG wearable"]}]}`
	svc, prompts := fakeInterestsService(t, answer)
	// Stored before the upgrade: an unverified why on a re-derived reference.
	legacy := testInterest("legacy", "silent speech EMG wearable", "You follow silent speech interfaces.", "silent speech EMG wearable")
	legacy.Evidence = []interests.Ref{{Key: "search:" + interestHash("silent speech emg wearable"), Source: interests.SourceSearch, At: interestHoursAgo(3).UnixMilli()}}
	if _, err := svc.store.update(func(st *interestsStateFile) {
		st.Interests = []interests.Interest{legacy}
		st.RefreshedAt = interestTestNow.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		st.ConsolidatedAt = interestTestNow.UTC().Format(time.RFC3339Nano)
	}); err != nil {
		t.Fatal(err)
	}
	// No pass is due by the clock (the last refresh ran an hour ago), but the
	// upgrade pass runs once for the unverified why.
	if !svc.maybeRun(context.Background(), "test") {
		t.Fatal("the rewrite pass did not run")
	}
	if len(*prompts) != 1 || svc.calls != 1 {
		t.Fatalf("the rewrite pass made %d calls (%d today), want 1", len(*prompts), svc.calls)
	}
	if strings.Contains((*prompts)[0], "You follow silent speech interfaces.") {
		t.Fatal("the unverified why was replayed to the model")
	}
	stored := svc.store.load().Interests
	if len(stored) != 1 || stored[0].Why != "You follow silent speech sensor research." || !stored[0].WhyVerified {
		t.Fatalf("the why was not rewritten from readable evidence: %+v", stored)
	}
	// Only once: the next tick does not run, and neither does a later start
	// with nothing left unverified.
	if svc.maybeRun(context.Background(), "test") {
		t.Fatal("the rewrite pass ran twice")
	}
	svc.rewriteChecked = false
	if svc.maybeRun(context.Background(), "test") {
		t.Fatal("a later start reran the rewrite with nothing unverified")
	}
	if len(*prompts) != 1 {
		t.Fatalf("calls %d, want 1", len(*prompts))
	}
}

func TestInterestsRewritePassNeverRunsWhilePaused(t *testing.T) {
	svc, prompts := fakeInterestsService(t, `{"interests":[{"name":"silent speech EMG wearable","why":"Fresh why.","kind":"research","confidence":0.9,"evidence":["A1"],"queries":["x"]}]}`)
	legacy := testInterest("legacy", "silent speech EMG wearable", "You follow silent speech interfaces.", "silent speech EMG wearable")
	legacy.Evidence = []interests.Ref{{Key: "search:" + interestHash("silent speech emg wearable"), Source: interests.SourceSearch, At: interestHoursAgo(3).UnixMilli()}}
	if _, err := svc.store.update(func(st *interestsStateFile) {
		st.Interests = []interests.Interest{legacy}
		st.RefreshedAt = interestTestNow.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		st.ConsolidatedAt = interestTestNow.UTC().Format(time.RFC3339Nano)
	}); err != nil {
		t.Fatal(err)
	}
	// Observing is paused (Closed Eyes): the rewrite pass never runs.
	svc.rewriteAllowed = func() bool { return false }
	if svc.maybeRun(context.Background(), "test") || len(*prompts) != 0 {
		t.Fatal("the rewrite pass ran while observation was paused")
	}
}

func TestInterestsServiceStopsWhenNotAllowed(t *testing.T) {
	svc, prompts := fakeInterestsService(t, `{"interests":[]}`)
	svc.allowed = func() bool { return false }
	if svc.maybeRun(context.Background(), "test") || len(*prompts) != 0 {
		t.Fatal("a pass ran while not allowed")
	}
	// An answer with nothing usable keeps the stored list and counts as a
	// failure.
	svc.allowed = func() bool { return true }
	if svc.maybeRun(context.Background(), "test") {
		t.Fatal("an empty answer was stored")
	}
	// The first pass is the review (20 summaries): its failure is counted
	// apart, so refreshes go on.
	if st := svc.store.load(); st.ConsolidateFailures != 1 || st.Failures != 0 || st.ConsolidatedAt != "" {
		t.Fatalf("after an empty answer: %+v", st)
	}
}

func TestAFailedFirstReviewStillNamesInterests(t *testing.T) {
	answer := `{"interests":[{"name":"wearable AI camera hats","kind":"project","confidence":0.9,"evidence":["A1"],"queries":["smart hat camera"]}]}`
	svc, prompts := fakeInterestsService(t, "not json", answer)
	var models []string
	model := svc.model
	svc.model = func(ctx context.Context, prompt, name string) (string, error) {
		models = append(models, name)
		return model(ctx, prompt, name)
	}
	if svc.maybeRun(context.Background(), "test") {
		t.Fatal("a failed review stored a list")
	}
	// An hour later the review waits for its day, and a refresh names the
	// interests on the light model.
	later := interestTestNow.Add(61 * time.Minute)
	svc.now = func() time.Time { return later }
	if !svc.maybeRun(context.Background(), "test") {
		t.Fatal("no refresh ran after the failed review")
	}
	if strings.Join(models, ",") != "sonnet,haiku" || len(*prompts) != 2 {
		t.Fatalf("models %v", models)
	}
	st := svc.store.load()
	if len(st.Interests) != 1 || st.ConsolidatedAt != "" || st.ConsolidateFailures != 1 {
		t.Fatalf("after the refresh: %+v", st)
	}
}

func TestAPassThatCannotBeStoredStillCountsItsCalls(t *testing.T) {
	answer := `{"interests":[{"name":"wearable AI camera hats","kind":"project","confidence":0.9,"evidence":["A1"],"queries":["smart hat camera"]}]}`
	var answers []string
	for i := 0; i < 20; i++ {
		answers = append(answers, answer)
	}
	svc, prompts := fakeInterestsService(t, answers...)
	// Every write fails: the folder is unknown.
	svc.store = newInterestsStore("")
	now := interestTestNow
	// Ticks from 08:00 to 23:30, one local day.
	for tick := 0; tick < 32; tick++ {
		at := now.Add(time.Duration(tick) * 30 * time.Minute)
		svc.now = func() time.Time { return at }
		if svc.maybeRun(context.Background(), "test") {
			t.Fatal("a pass reported success with no store")
		}
	}
	// The in-memory back-off (1h, 2h, 4h, 8h) bounds the calls well below
	// one per tick, and never above the daily cap.
	if n := len(*prompts); n == 0 || n > interestsCallsPerDay {
		t.Fatalf("%d model calls in a day of ticks", n)
	}
}

func TestInterestItemsRefuseEveryNameTheEvidenceHolds(t *testing.T) {
	in := interestInput{
		Now:  interestTestNow,
		From: interestTestNow.Add(-interests.RefreshWindow),
		Activities: []ActivityRow{
			// Left out (a messaging app), but the people it names still count.
			{ID: "a1", Title: "Chat with Tomasz", ActiveMsByApp: `{"WeChat": 600000}`, StartMs: interestHoursAgo(3).UnixMilli(),
				Entities: `[{"type":"person","name":"Tomasz Nowak"}]`},
			{ID: "a2", Title: "Read @yetone on X about plugins", ActiveMsByApp: `{"Safari": 600000}`, StartMs: interestHoursAgo(2).UnixMilli(),
				Entities: `[{"type":"person","name":"yetone (@yetone)"}]`},
			// A handle alone may name a product: it refuses nothing.
			{ID: "a3", Title: "Set up CI from @github docs", ActiveMsByApp: `{"Safari": 600000}`, StartMs: interestHoursAgo(1).UnixMilli()},
		},
		Runs: []interestRun{{AppName: "Safari", StartMs: interestHoursAgo(2).UnixMilli(), Type: "research",
			Entities: []string{"Tomasz Nowak", "yetone", "Magpie plugins"}}},
	}
	items, names := interestItems(in)
	got := itemTexts(items)
	for _, leaked := range []string{"named:Tomasz", "named:yetone"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("%q reached the evidence:\n%s", leaked, got)
		}
	}
	if !strings.Contains(got, "named:Magpie plugins") {
		t.Fatalf("a product was dropped:\n%s", got)
	}
	joined := strings.Join(names, "|")
	for _, want := range []string{"Tomasz Nowak", "yetone"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("names miss %q: %v", want, names)
		}
	}
	// The checks refuse a query with that name.
	ans := interests.Answer{Interests: []interests.Candidate{
		{Name: "research grant writing", Evidence: []string{"A1"}, Queries: []string{"Tomasz Nowak grant", "yetone magpie"}},
		{Name: "GitHub Actions pipelines", Evidence: []string{"A2"}, Queries: []string{"GitHub Actions caching"}},
	}}
	_, ids := interests.Prompt(interests.PromptInput{Items: items})
	accepted, _ := interests.Accept(ans, ids, items, interests.Rules{Names: names})
	if len(accepted) != 2 || strings.Join(accepted[0].Queries, "|") != "research grant writing" {
		t.Fatalf("accepted %+v", accepted)
	}
	if strings.Join(accepted[1].Queries, "|") != "GitHub Actions caching" {
		t.Fatalf("a handle refused a product: %+v", accepted[1])
	}
}

func TestInterestsPassNeedsThePeopleList(t *testing.T) {
	svc, prompts := fakeInterestsService(t, `{"interests":[]}`)
	collect := svc.collect
	svc.collect = func(from, now time.Time) interestInput {
		in := collect(from, now)
		in.PeopleUnknown = true
		return in
	}
	if svc.maybeRun(context.Background(), "test") || len(*prompts) != 0 {
		t.Fatal("a pass ran without the people list")
	}
}

func TestAFailedReviewDoesNotBlockRefreshesInMemory(t *testing.T) {
	answer := `{"interests":[{"name":"wearable AI camera hats","kind":"project","confidence":0.9,"evidence":["A1"],"queries":["smart hat camera"]}]}`
	svc, _ := fakeInterestsService(t, "not json", answer)
	// The stored state cannot record the failure: only memory backs off.
	svc.store = newInterestsStore("")
	if svc.maybeRun(context.Background(), "test") {
		t.Fatal("a failed review stored a list")
	}
	if !svc.mayCall(interestTestNow, interests.ModeRefresh) || svc.mayCall(interestTestNow.Add(time.Hour), interests.ModeConsolidate) {
		t.Fatal("a failed review must back off reviews only")
	}
}

func TestPinnedInterestsOutliveTheWindowAndReadsAreChecked(t *testing.T) {
	svc, _ := fakeInterestsService(t)
	old := interests.Ref{Key: "activity:old", Source: interests.SourceActivity, At: interestTestNow.Add(-20 * 24 * time.Hour).UnixMilli()}
	if _, err := svc.store.update(func(st *interestsStateFile) {
		st.Interests = []interests.Interest{
			{ID: "stale", Name: "Brazil hydro power", Evidence: []interests.Ref{old}, Queries: []string{"Brazil hydro power"}},
			{ID: "pinned", Name: "electric car impact", Evidence: []interests.Ref{old}, Queries: []string{"electric car impact"}},
			{ID: "named", Name: "Nowak lab news", Evidence: []interests.Ref{{Key: "a", Source: interests.SourceActivity, At: interestHoursAgo(1).UnixMilli()}}, Queries: []string{"Nowak lab"}},
		}
	}); err != nil {
		t.Fatal(err)
	}
	svc.choices = func() interestsUserState { return interestsUserState{Pinned: map[string]bool{"pinned": true}} }
	svc.rememberNames([]string{"Ana Nowak"}, interestTestNow)
	var ids []string
	for _, it := range svc.list() {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "pinned" {
		t.Fatalf("list = %v, want the pinned one only", ids)
	}
	if svc.safeToSend("Nowak lab news") || !svc.safeToSend("electric car impact") {
		t.Fatal("the send-time check disagrees")
	}
}
