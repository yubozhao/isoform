package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/interests"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

func writeInterestReplayMemory(t *testing.T, id string, proof memoryReadProvenance) string {
	t.Helper()
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".md")
	content := renderOKFDocument(map[string]any{
		"type": "Isoform Memory", "title": "Camera project", "source": "dream-synthesis",
		"dimension": "user_insight", "read_provenance": memoryReadProvenanceJSON(proof),
	}, "## Memory Points\n\n- Building a camera project.")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func interestReplayService(t *testing.T, app *App, answer string) (*interestsService, *string) {
	t.Helper()
	svc := newInterestsService(app)
	svc.now = func() time.Time { return interestTestNow }
	svc.allowed = func() bool { return true }
	svc.enabled = func() bool { return true }
	svc.changed = nil
	svc.user = func() interestsUserState { return interestsUserState{} }
	svc.language = func() string { return "English" }
	svc.lightModel = func() string { return "test" }
	svc.mainModel = svc.lightModel
	// Fresh allowed evidence is independent of the stored interest. The
	// regression exercises real interest JSON, memory files and source rows.
	svc.collect = func(from, now time.Time) interestInput {
		in := interestInput{Now: now, From: from}
		for i := 0; i < interestsConsolidateMin; i++ {
			in.Activities = append(in.Activities, ActivityRow{
				ID: fmt.Sprintf("fresh-%d", i), Title: "Research compact camera modules",
				ActiveMsByApp: `{"Safari":300000}`, StartMs: now.Add(-time.Duration(i+1) * time.Minute).UnixMilli(),
			})
		}
		return in
	}
	var prompt string
	svc.model = func(_ context.Context, value, _ string) (string, error) {
		prompt = value
		return answer, nil
	}
	return svc, &prompt
}

func TestInterestsReplayRechecksStoredMemorySources(t *testing.T) {
	for _, restriction := range []string{"app", "session", "retained-session", "paused", "closed-eyes", "missing-memory", "read-failure", "correction"} {
		t.Run(restriction, func(t *testing.T) {
			app := newPrivacyReadTestApp(t)
			previous := pii.GetUserSensitiveApps()
			t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
			pii.SetUserSensitiveApps(nil)
			insertTestSession(t, app.activity.store, "replay-source", "done", 100_000)
			if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET app_names='["Diary"]' WHERE id='replay-source'`); err != nil {
				t.Fatal(err)
			}
			proof := memoryReadProvenance{Version: 2, Kinds: []string{"activity"}, Sessions: []string{"replay-source"}}
			if restriction == "app" {
				proof.Sessions, proof.Apps = nil, []string{"Diary"}
			}
			privatePath := writeInterestReplayMemory(t, "private-interest", proof)
			writeInterestReplayMemory(t, "safe-interest", memoryReadSource("manual"))
			private := testInterest("private", "woven camera accessories", "Replay-private-needle", "woven camera mount")
			private.Evidence = []interests.Ref{{Key: "about:private-interest#0", Source: interests.SourceAbout, At: interestTestNow.Add(-40 * 24 * time.Hour).UnixMilli()}}
			safe := testInterest("safe", "optical camera modules", "Replay-safe-needle", "optical camera module")
			safe.Evidence = []interests.Ref{{Key: "about:safe-interest#0", Source: interests.SourceAbout, At: interestTestNow.UnixMilli()}}
			if !app.interestsReadFilter()(private) {
				t.Fatal("readable stored interest was held before the restriction")
			}
			switch restriction {
			case "app", "session":
				pii.AddUserSensitiveApp("com.example.diary", "Diary")
			case "retained-session":
				if err := app.activity.store.deleteRetainedSession("replay-source"); err != nil {
					t.Fatal(err)
				}
				if !app.interestsReadFilter()(private) {
					t.Fatal("retention alone lost the surviving source receipt")
				}
				pii.AddUserSensitiveApp("com.example.diary", "Diary")
			case "paused":
				app.saveObservationPauses([]observationPause{{Start: 100_000, End: 160_000}})
			case "closed-eyes":
				app.closedEyes.enabled.Store(true)
			case "missing-memory":
				if err := os.Remove(privatePath); err != nil {
					t.Fatal(err)
				}
			case "read-failure":
				if err := app.activity.store.Close(); err != nil {
					t.Fatal(err)
				}
			case "correction":
				if _, _, err := PersistShadowTaskLearningProjection(app.activity.store, testShadowTaskLearningProjection(), 1000); err != nil {
					t.Fatal(err)
				}
				if _, _, err := app.activity.store.AddShadowTaskLearningMaterialization(ShadowTaskLearningMaterialization{RevisionID: "revision:one", TargetKind: ShadowTaskLearningTargetMemory, TargetID: "private-interest", Status: "materialized", CreatedAt: 1100}); err != nil {
					t.Fatal(err)
				}
				addLearningCorrection(t, app.activity.store, AgencyLearningCorrectionRecord{IdempotencyKey: "interest-replay-correction", CapabilityKey: "coding.review", LearningRevisionID: "revision:one", OriginalStatement: "old inference", Correction: "Do not use", CreatedAt: 1200})
			}
			svc, prompt := interestReplayService(t, app, `{"interests":[{"name":"compact camera modules","kind":"research","confidence":0.9,"evidence":["A1"],"queries":["compact camera modules"]}]}`)
			if _, err := svc.store.update(func(st *interestsStateFile) { st.Interests = []interests.Interest{private, safe} }); err != nil {
				t.Fatal(err)
			}
			if !svc.maybeRun(context.Background(), "privacy-test") {
				t.Fatal("the pass did not run")
			}
			for _, held := range []string{private.Name, private.Why} {
				if strings.Contains(*prompt, held) {
					t.Fatalf("held stored interest entered the model prompt: %q", held)
				}
			}
			if restriction != "closed-eyes" && restriction != "read-failure" && !strings.Contains(*prompt, safe.Why) {
				t.Fatal("the readable stored interest was not replayed")
			}
			found := false
			for _, stored := range svc.store.load().Interests {
				if stored.ID == private.ID {
					found = true
					if stored.Name != private.Name || stored.Why != private.Why || !reflect.DeepEqual(stored.Evidence, private.Evidence) {
						t.Fatalf("holding the read altered local evidence: %+v", stored)
					}
				}
			}
			if !found {
				t.Fatal("the held stored interest was discarded")
			}
		})
	}
}

func TestInterestsReplayHoldsAmbiguousAndMissingEvidence(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	writeInterestReplayMemory(t, "readable", memoryReadSource("manual"))
	for _, refs := range [][]interests.Ref{
		nil,
		{{Key: "search:hashed-query", Source: interests.SourceSearch}},
		{{Key: "named:hashed-name", Source: interests.SourceNamed}},
		{{Key: "activity:deleted", Source: interests.SourceActivity}},
		{{Key: "about:readable#0", Source: interests.SourceChat}},
		{{Key: "about:readable#bad", Source: interests.SourceAbout}},
		{{Key: "about:readable#0", Source: interests.SourceAbout}, {Key: "about:deleted#0", Source: interests.SourceAbout}},
	} {
		if app.interestsReadFilter()(interests.Interest{Evidence: refs}) {
			t.Fatalf("unresolved evidence authorized replay: %+v", refs)
		}
	}
}

func TestInterestsReadFilterReauthorizesSearchAndNamedHashes(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	pii.SetUserSensitiveApps(nil)
	now := time.Now()
	// A typed search the stored browser visits still derive.
	searchText := strings.ToLower(topicText("silent speech emg wearable"))
	if _, err := app.activity.store.InsertBrowserVisits([]BrowserVisitRow{{
		URL:       "https://www.google.com/search?q=silent+speech+emg+wearable",
		Title:     "silent speech emg wearable - Google Search",
		VisitedAt: now.Add(-time.Hour).UnixMilli(),
		Browser:   "Chrome",
	}}); err != nil {
		t.Fatal(err)
	}
	// A thing a run summary named, from a session that may still be read.
	at := now.Add(-2 * time.Hour).UnixMilli()
	insertTestSession(t, app.activity.store, "interest-named-source", "done", at)
	run := testRun("interest-named-run", "Notes", at, at+60_000, 60_000, 1)
	run.SessionIDs = []string{"interest-named-source"}
	insertComposableTimelineRun(t, app.activity.store, run, ActivityTimelineGroupSummary{
		Title: "Wearable sensor boards", ActivityType: "research",
		Entities: []string{"Seeed XIAO ESP32-S3 Sense"},
	})
	namedText := strings.ToLower(strings.Join(strings.Fields(topicText("Seeed XIAO ESP32-S3 Sense")), " "))
	searchRef := interests.Ref{Key: "search:" + interestHash(searchText), Source: interests.SourceSearch}
	namedRef := interests.Ref{Key: "named:" + interestHash(namedText), Source: interests.SourceNamed}
	// A hash the same collection still derives from readable sources is not
	// held: the interest keeps taking part in passes.
	if !app.interestsReadFilter()(interests.Interest{Evidence: []interests.Ref{searchRef, namedRef}}) {
		t.Fatal("a reference still derivable from readable sources was held")
	}
	// A hash the sources no longer produce stays refused, alone or paired.
	for _, refs := range [][]interests.Ref{
		{{Key: "search:" + interestHash("a query never typed"), Source: interests.SourceSearch}},
		{{Key: "named:" + interestHash("a thing never named"), Source: interests.SourceNamed}},
		{searchRef, {Key: "named:" + interestHash("a thing never named"), Source: interests.SourceNamed}},
	} {
		if app.interestsReadFilter()(interests.Interest{Evidence: refs}) {
			t.Fatalf("a reference no longer derivable authorized replay: %+v", refs)
		}
	}
	// Once the visit falls inside a pause, the search hash is refused again.
	app.saveObservationPauses([]observationPause{{Start: now.Add(-time.Hour).UnixMilli() - 1000, End: now.Add(-time.Hour).UnixMilli() + 1000}})
	if app.interestsReadFilter()(interests.Interest{Evidence: []interests.Ref{searchRef}}) {
		t.Fatal("a paused-away search reference still authorized replay")
	}
	if !app.interestsReadFilter()(interests.Interest{Evidence: []interests.Ref{namedRef}}) {
		t.Fatal("an unrelated pause held a still-readable named reference")
	}
}

func TestInterestWhyFromReDerivedEvidenceStaysHiddenUntilRewritten(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	t.Setenv(topicsStateDirEnvVar, t.TempDir())
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	pii.SetUserSensitiveApps(nil)
	now := time.Now()
	searchText := strings.ToLower(topicText("silent speech emg wearable"))
	if _, err := app.activity.store.InsertBrowserVisits([]BrowserVisitRow{{
		URL:       "https://www.google.com/search?q=silent+speech+emg+wearable",
		Title:     "silent speech emg wearable - Google Search",
		VisitedAt: now.Add(-time.Hour).UnixMilli(),
		Browser:   "Chrome",
	}}); err != nil {
		t.Fatal(err)
	}
	searchRef := interests.Ref{Key: "search:" + interestHash(searchText), Source: interests.SourceSearch, At: now.Add(-time.Hour).UnixMilli()}
	// A pre-fix stored interest: its why predates verification, and its only
	// evidence is a search reference the read filter can only re-derive.
	legacy := testInterest("rederived", "silent speech EMG wearable", "You follow silent speech interfaces.", "silent speech EMG wearable")
	legacy.Evidence = []interests.Ref{searchRef}
	legacy.FirstAt = now.Add(-72 * time.Hour).UnixMilli()

	svc := newInterestsService(app)
	svc.allowed = func() bool { return true }
	svc.changed = nil
	svc.user = func() interestsUserState { return interestsUserState{} }
	svc.language = func() string { return "English" }
	svc.lightModel = func() string { return "test" }
	svc.mainModel = svc.lightModel
	// Fresh activity makes a pass due; the real collection still runs, so the
	// search evidence keeps coming from the stored browser visits.
	base := svc.collect
	svc.collect = func(from, at time.Time) interestInput {
		in := base(from, at)
		for i := 0; i < interestsConsolidateMin; i++ {
			in.Activities = append(in.Activities, ActivityRow{
				ID: fmt.Sprintf("fresh-%d", i), Title: "Research compact camera modules",
				ActiveMsByApp: `{"Safari":300000}`, StartMs: at.Add(-time.Duration(i+1) * time.Minute).UnixMilli(),
			})
		}
		return in
	}
	var prompt string
	svc.model = func(_ context.Context, value, _ string) (string, error) {
		prompt = value
		return `{"interests":[{"name":"silent speech EMG wearable","why":"You follow silent speech sensor research.","kind":"research","confidence":0.9,"evidence":["S1"],"queries":["silent speech EMG wearable"]}]}`, nil
	}
	if _, err := svc.store.update(func(st *interestsStateFile) {
		st.Interests = []interests.Interest{legacy}
		st.ConsolidatedAt = now.UTC().Format(time.RFC3339Nano)
	}); err != nil {
		t.Fatal(err)
	}

	// Before the pass the stored why is unverified: not listed, not shown.
	for _, it := range svc.list() {
		if it.Why != "" {
			t.Fatalf("an unverified why was listed before the rewrite: %+v", it)
		}
	}
	if view := app.topicsSvc().view(); !view.Interests || len(view.Topics) != 1 || view.Topics[0].Why != "" {
		t.Fatalf("the re-derived topic showed its old why: %+v", view.Topics)
	}

	if !svc.maybeRun(context.Background(), "rewrite-test") {
		t.Fatal("the pass did not run")
	}
	if strings.Contains(prompt, "You follow silent speech interfaces.") {
		t.Fatal("the unverified why was replayed to the model")
	}
	stored := svc.store.load().Interests
	if len(stored) != 1 || stored[0].Why != "You follow silent speech sensor research." || !stored[0].WhyVerified {
		t.Fatalf("the pass did not rewrite the why from current evidence: %+v", stored)
	}
	// Once rewritten from currently readable evidence, the why shows.
	if view := app.topicsSvc().view(); len(view.Topics) != 1 || view.Topics[0].Why != "You follow silent speech sensor research." {
		t.Fatalf("the rewritten why did not show: %+v", view.Topics)
	}
}

func TestInterestsRewriteAllowedFollowsClosedEyes(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	svc := newInterestsService(app)
	if !svc.rewriteAllowed() {
		t.Fatal("the rewrite pass is blocked while observing is on")
	}
	app.closedEyes.enabled.Store(true)
	if svc.rewriteAllowed() {
		t.Fatal("the rewrite pass may run while Closed Eyes is on")
	}
}

func TestInterestsReplayDoesNotMergeHeldIdentityOrSendRemovedLabels(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	pii.SetUserSensitiveApps(map[string]string{"com.example.diary": "Diary"})
	writeInterestReplayMemory(t, "private-interest", memoryReadProvenance{Version: 2, Apps: []string{"Diary"}})
	private := testInterest(interests.IDFor("woven camera accessories"), "woven camera accessories", "Private-local-explanation", "woven camera mount")
	private.Evidence = []interests.Ref{{Key: "about:private-interest#0", Source: interests.SourceAbout, At: interestTestNow.Add(-40 * 24 * time.Hour).UnixMilli()}}
	for _, mode := range []interests.Mode{interests.ModeRefresh, interests.ModeConsolidate} {
		t.Run(string(mode), func(t *testing.T) {
			svc, prompt := interestReplayService(t, app, `{"interests":[{"name":"woven camera accessories","why":"Fresh-public-explanation","kind":"research","confidence":0.9,"evidence":["A1"],"queries":["woven camera accessories"]}]}`)
			svc.store = newInterestsStore(t.TempDir())
			svc.user = func() interestsUserState {
				return interestsUserState{Removed: []string{"Private-removed-needle", "Missing-source-needle"}, RemovedNames: map[string][]string{private.ID: {"Private-removed-needle"}, "gone": {"Missing-source-needle"}}}
			}
			if _, err := svc.store.update(func(st *interestsStateFile) {
				st.Interests = []interests.Interest{private}
				if mode == interests.ModeRefresh {
					st.ConsolidatedAt = interestTestNow.Format(time.RFC3339Nano)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if !svc.maybeRun(context.Background(), "privacy-test") {
				t.Fatal("the pass did not run")
			}
			if strings.Contains(*prompt, private.Name) || strings.Contains(*prompt, "Private-") || strings.Contains(*prompt, "Missing-source-needle") {
				t.Fatal("a held or unresolved label entered the replay/removed list")
			}
			stored := svc.store.load().Interests
			if len(stored) != 2 {
				t.Fatalf("fresh result merged into held identity: %+v", stored)
			}
			for _, item := range stored {
				if item.ID == private.ID {
					if item.Why != private.Why || !reflect.DeepEqual(item.Evidence, private.Evidence) {
						t.Fatal("fresh result replaced held content or pruned its dependencies")
					}
				} else if item.Why != "Fresh-public-explanation" || len(item.Evidence) != 1 || item.Evidence[0].Source != interests.SourceActivity {
					t.Fatalf("fresh result inherited held content: %+v", item)
				}
			}
		})
	}
}

func TestInterestsPassFoldsStoredNearDuplicates(t *testing.T) {
	answer := `{"interests":[{"name":"silent speech EMG wearable","kind":"research","confidence":0.9,"evidence":["A1"],"queries":["silent speech EMG wearable"]}]}`
	svc, _ := fakeInterestsService(t, answer)
	older := testInterest("twin-old", "silent speech EMG wearable", "You follow silent speech interfaces.", "silent speech EMG wearable")
	older.FirstAt = interestTestNow.Add(-72 * time.Hour).UnixMilli()
	newer := testInterest("twin-new", "silent speech EMG wearables", "", "silent speech EMG wearables")
	newer.FirstAt = interestTestNow.Add(-24 * time.Hour).UnixMilli()
	if _, err := svc.store.update(func(st *interestsStateFile) {
		st.Interests = []interests.Interest{older, newer}
		st.RefreshedAt = interestTestNow.Add(-21 * time.Hour).UTC().Format(time.RFC3339Nano)
		st.ConsolidatedAt = interestTestNow.UTC().Format(time.RFC3339Nano)
	}); err != nil {
		t.Fatal(err)
	}
	if !svc.maybeRun(context.Background(), "test") {
		t.Fatal("the pass did not run")
	}
	stored := svc.store.load().Interests
	if len(stored) != 1 {
		t.Fatalf("the stored near-duplicates did not fold: %+v", stored)
	}
	if stored[0].ID != "twin-old" || stored[0].Why != "You follow silent speech interfaces." {
		t.Fatalf("the fold kept the wrong row: %+v", stored[0])
	}
}

func TestInterestsReadFilterChecksActivityAndSemanticSources(t *testing.T) {
	for _, kind := range []string{"activity", "semantic"} {
		t.Run(kind, func(t *testing.T) {
			app := newPrivacyReadTestApp(t)
			previous := pii.GetUserSensitiveApps()
			t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
			pii.SetUserSensitiveApps(nil)
			store := app.activity.store
			insertTestSession(t, store, "interest-observation", "done", 100_000)
			if _, err := store.db.Exec(`UPDATE activity_sessions SET app_names='["Diary"]' WHERE id='interest-observation'`); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "activity":
				run := testRun("interest-run", "Notes", 100_000, 160_000, 60_000, 1)
				run.SessionIDs = []string{"interest-observation"}
				insertComposableTimelineRun(t, store, run, ActivityTimelineGroupSummary{Title: "Camera project"})
				if err := store.InsertActivity(ActivityRow{ID: "derived-interest", Title: "Camera project", ActiveMsByApp: `{"Notes":60000}`, RunIDs: `["interest-run"]`, StartMs: 100_000, EndMs: 160_000}); err != nil {
					t.Fatal(err)
				}
			case "semantic":
				if err := store.InsertSemanticEvent(SemanticEventRow{ID: "derived-interest", Title: "Camera project", Apps: `["Notes"]`, SourceRefs: `{"session_ids":["interest-observation"]}`, StartMs: 100_000, EndMs: 160_000}); err != nil {
					t.Fatal(err)
				}
			}
			interest := interests.Interest{Evidence: []interests.Ref{{Key: kind + ":derived-interest", Source: interests.SourceActivity, At: 100_000}}}
			if !app.interestsReadFilter()(interest) {
				t.Fatal("readable activity evidence was held")
			}
			pii.AddUserSensitiveApp("com.example.diary", "Diary")
			if app.interestsReadFilter()(interest) {
				t.Fatal("derived interest ignored the original session's sensitive app")
			}
			pii.SetUserSensitiveApps(nil)
			app.saveObservationPauses([]observationPause{{Start: 100_000, End: 160_000}})
			if app.interestsReadFilter()(interest) {
				t.Fatal("derived interest ignored the original observation pause")
			}
			app.saveObservationPauses(nil)
			if _, err := store.db.Exec(`DELETE FROM activity_sessions WHERE id='interest-observation'`); err != nil {
				t.Fatal(err)
			}
			if app.interestsReadFilter()(interest) {
				t.Fatal("missing source session authorized derived interest replay")
			}
		})
	}
}

func TestInterestsReadFilterChecksChatSourceEligibility(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	ctx := context.Background()
	if err := app.store.SaveProject(ctx, storage.ProjectRow{ID: "interest-project", Name: "Camera project"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"safe", "hidden", "automation", "meeting", "main", "transcript", "missing"} {
		t.Run(kind, func(t *testing.T) {
			row := storage.SessionRow{LocalID: kind, ProjectID: "interest-project", Label: "Camera modules"}
			switch kind {
			case "hidden":
				row.Hidden = true
			case "automation":
				row.AutomationResult = true
			case "meeting":
				row.MeetingID = "meeting"
			case "main":
				row.Kind = storage.SessionKindMain
			case "transcript":
				row.Label = "Transcript: private discussion"
			}
			if kind != "missing" {
				if err := app.store.SaveSession(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			interest := interests.Interest{Evidence: []interests.Ref{{Key: "chat:" + kind, Source: interests.SourceChat}}}
			if got := app.interestsReadFilter()(interest); got != (kind == "safe") {
				t.Fatalf("chat source %q allowed=%v", kind, got)
			}
		})
	}
}

func TestInterestsReadFilterChecksPurposeEvidenceClosure(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	pii.SetUserSensitiveApps(nil)
	at := time.Now().Add(-time.Minute).UnixMilli()
	insertTestSession(t, app.activity.store, "interest-purpose-source", "done", at)
	if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET app_names='["Diary"]' WHERE id='interest-purpose-source'`); err != nil {
		t.Fatal(err)
	}
	evidence := purposeTestEvidence("interest-purpose-source", PurposeEvidenceObservedUser, PurposeStanceSupport, at)
	evidence.SourceKind, evidence.SourceID = "activity_session", "interest-purpose-source"
	detail := createTodayContextPurpose(t, app, "camera-project", "Camera project", evidence)
	interest := interests.Interest{Evidence: []interests.Ref{{Key: "goal:" + detail.OpenLoop.ID, Source: interests.SourceGoal, At: detail.Current.OccurredAt}}}
	if !app.interestsReadFilter()(interest) {
		t.Fatal("readable Purpose evidence was held")
	}
	pii.AddUserSensitiveApp("com.example.diary", "Diary")
	if app.interestsReadFilter()(interest) {
		t.Fatal("Purpose-derived interest ignored its source privacy change")
	}
	pii.SetUserSensitiveApps(nil)
	interest.Evidence[0].At--
	if app.interestsReadFilter()(interest) {
		t.Fatal("a different Purpose revision authorized old derived text")
	}
}

func TestInterestsReplayHeldRowsDoNotPinStorageSlots(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	svc, _ := interestReplayService(t, app, `{"interests":[{"name":"compact camera modules","why":"Fresh useful interest","kind":"research","confidence":0.9,"evidence":["A1"],"queries":["compact camera modules"]}]}`)
	if _, err := svc.store.update(func(st *interestsStateFile) {
		for i := 0; i < interests.MaxStored; i++ {
			st.Interests = append(st.Interests, interests.Interest{
				ID: fmt.Sprintf("held-%d", i), Name: fmt.Sprintf("Old camera topic %d", i), Confidence: 0.1,
				Evidence: []interests.Ref{{Key: "about:missing#0", Source: interests.SourceAbout, At: interestTestNow.Add(-40 * 24 * time.Hour).UnixMilli()}},
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !svc.maybeRun(context.Background(), "privacy-test") {
		t.Fatal("the pass did not run")
	}
	stored := svc.store.load().Interests
	if len(stored) != interests.MaxStored {
		t.Fatalf("storage cap changed: %d", len(stored))
	}
	foundFresh := false
	for _, interest := range stored {
		foundFresh = foundFresh || interest.Why == "Fresh useful interest"
		if len(interest.Evidence) != 1 {
			t.Fatal("retained interest lost its source dependency")
		}
	}
	if !foundFresh {
		t.Fatal("held rows consumed every storage slot")
	}
}
