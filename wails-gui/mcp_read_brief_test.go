package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/interests"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/recentfiles"
	"wails-gui/storage"
)

func seedMCPBriefSources(t *testing.T, a *App) MobileBriefV1 {
	t.Helper()
	at := seedMCPReadActivity(t, a, "brief-capture", "Editor", "com.example.editor", "Visible project", "", "")
	now := time.Now()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	h, err := handoff.NewStore(filepath.Join(configDirOrFallback(), "handoff"))
	check(err)
	a.handoffStore = h
	_, err = h.Save(handoff.Entry{ID: "work", Title: "Current work", Source: handoff.SourceTriage, SourceID: "brief-capture", AppName: "Editor", Status: handoff.StatusCompleted, CreatedAt: at, UpdatedAt: at + 1, Body: "Never disclose secret work body"})
	check(err)
	purposeStore, err := a.purposeProductStore()
	check(err)
	evidence := purposeTestEvidence("brief", PurposeEvidenceExplicitUser, PurposeStanceSupport, at)
	evidence.SourceKind = "activity_session"
	evidence.SourceID = "brief-capture"
	purpose, _, err := purposeStore.Create(purposeTestCreateInput("brief-purpose", PurposeKindCommitment, "visible purpose", []PurposeEvidenceInput{evidence}, at))
	check(err)
	addLearningCorrection(t, a.activity.store, AgencyLearningCorrectionRecord{ID: "correction", IdempotencyKey: "brief-correction", LearningRevisionID: "lesson", CapabilityKey: "compose.text", Correction: "Use the reviewed template", CreatedAt: at})
	check(a.store.SaveProject(a.ctx, storage.ProjectRow{ID: "project", Name: "project", Path: filepath.Join(os.Getenv("HOME"), "project")}))
	check(a.store.SaveSession(a.ctx, storage.SessionRow{LocalID: "own-chat", ProjectID: "project", Kind: storage.SessionKindMain, Label: "My chat"}))
	check(a.store.SaveChatDeliverable(a.ctx, storage.ChatDeliverableRow{ID: "artifact", SessionID: "own-chat", Title: "Current artifact", Summary: "Never disclose secret artifact body", Files: `["/private/path/secret.pdf"]`}))
	check(a.store.SaveCronJob(a.ctx, storage.CronJobRow{ID: "job", Name: "Current schedule", Enabled: true, SourcePlatform: "chat", SourceSessionID: "own-chat"}))
	check(a.store.InsertCronRun(a.ctx, storage.CronRunRow{ID: "run", JobID: "job", Status: "ok", StartedAt: at, FinishedAt: at + 100, ResultSummary: "Never disclose secret run logs"}))
	check(a.store.SetUIState(a.ctx, permissionUIStateKey(filesPermissionKey), "true"))
	folder := filepath.Join(os.Getenv("HOME"), "Documents")
	_, err = a.activity.store.UpsertFileActivity([]FileActivityRow{{ID: "document", Name: "Current document.md", DirHashes: recentfiles.FolderHash(folder), AppBundleID: "com.example.editor", AppName: "Editor", ActiveAt: at, Action: recentfiles.ActionEdited}}, at)
	check(err)
	start := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local)
	check(a.store.UpsertCalendarEvents(a.ctx, []storage.CalendarEventRow{{ID: "calendar", Source: "eventkit", Provider: "apple", Title: "Current calendar event", StartAt: start.UnixMilli(), EndAt: start.Add(time.Hour).UnixMilli()}}, at))
	check(insertTimelineRun(a.activity.store.db, ActivityTimelineRunRow{ID: "brief-run", FirstEventRowID: 99991, LastEventRowID: 99991, AppKey: "com.example.editor", AppName: "Editor", StartMs: at, EndMs: at + 60000, SessionIDs: []string{"brief-capture"}, EventCount: 1}, at))
	check(a.activity.store.InsertActivity(ActivityRow{ID: "recap", Title: "Current recap", LatestChange: "Finished the project draft", RunIDs: `["brief-run"]`, ActiveMsByApp: `{"Editor":60000}`, StartMs: at, EndMs: at + 60000, CreatedAt: at, UpdatedAt: at}))
	newsURL := "https://example.org/release"
	news := withResearchMeta(`<news_json>[{"title":"Public release","summary":"Current public news","source":"Official source","url":"`+newsURL+`"}]</news_json>`, researchMeta{Via: researchViaClaudeCode, Results: 1, URLs: []string{newsURL}, Personal: researchPersonalNo})
	check(a.store.SaveCronJob(a.ctx, storage.CronJobRow{ID: "news-job", Name: todayNewsJobName, SurfaceKind: todayNewsSurfaceKind, Enabled: true}))
	check(a.store.InsertCronRun(a.ctx, storage.CronRunRow{ID: "news-run", JobID: "news-job", Status: "ok", StartedAt: at, FinishedAt: at + 100, ResultSummary: news}))
	topicDir := filepath.Join(os.Getenv("HOME"), "topics")
	t.Setenv(topicsStateDirEnvVar, topicDir)
	findURL := "https://example.org/find"
	check(a.store.InsertCronRun(a.ctx, storage.CronRunRow{ID: "find-run", JobID: "news-job", Status: "ok", StartedAt: at, FinishedAt: at + 100, ResultSummary: withResearchMeta("find", researchMeta{Via: researchViaClaudeCode, Results: 1, URLs: []string{findURL}, Personal: researchPersonalYes})}))
	_, err = newTopicsStore(topicDir).update(func(st *topicsStateFile) {
		sent := topicSentWithReadSources(topicSent{ID: "interest", Label: "Project tools"}, interests.Interest{ID: "interest", Name: "Project tools", Evidence: []interests.Ref{{Source: interests.SourceActivity, Key: "activity:recap", At: at}}}, false)
		st.Finds = topicFindsState{RunID: "find-run", Sent: []topicSent{sent}, RanOn: now.Format("2006-01-02"), RanAt: now.Format(time.RFC3339), Items: []TopicFind{{ID: "find-one", TopicID: "interest", Title: "Current find", Source: "Official source", WhatsNew: "A new project release", URL: findURL}}}
	})
	check(err)
	_, err = newInterestsStore(topicDir).update(func(st *interestsStateFile) {
		st.Interests = []interests.Interest{{ID: "interest", Name: "Project tools", Evidence: []interests.Ref{{Source: interests.SourceActivity, Key: "activity:recap", At: at}}}}
	})
	check(err)
	brief := mobileBriefStoredBrief(t, now.Format("2006-01-02"), mobileBriefSlotMorning, 7)
	brief.Sections = MobileBriefSections{}
	brief.Evidence = nil
	brief.Limits.TruncatedSections = nil
	sources := [][2]string{{"history", "work"}, {"purpose", purpose.OpenLoop.ID}, {"learning", "lesson"}, {"deliverable", "chat:artifact"}, {"schedule_run", "run"}, {"file", "file:document"}, {"calendar_day", now.Format("2006-01-02")}, {"summary", "recap"}, {"news", "news-run:0"}, {"find", "find-one"}, {"meeting", "brief-capture"}, {"calendar", "calendar"}}
	for i, source := range sources {
		ref := fmt.Sprint("e", i)
		brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: ref, SourceKind: source[0], SourceID: source[1]})
		title := "Never disclose stale copied title"
		if source[0] == "meeting" || source[0] == "calendar" {
			title = "Brief meeting label"
		}
		brief.Sections.Moved = append(brief.Sections.Moved, MobileBriefItem{ID: ref, Title: title, Detail: "Never disclose secret old prose", At: now.Format(time.RFC3339), Refs: []string{ref}})
	}
	brief.Sections.Moved = append(brief.Sections.Moved, MobileBriefItem{ID: "unknown", Title: "Never disclose unknown", Refs: []string{"unknown"}})
	brief.Narrative = []MobileBriefPara{{Text: "Never disclose secret narrative"}}
	return brief
}

func storeMCPBrief(t *testing.T, brief MobileBriefV1) {
	t.Helper()
	dir, err := mobileBriefStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := newMobileBriefStore(dir).save(brief, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestMCPReadBriefCompletenessUsesCurrentSources(t *testing.T) {
	a := newMCPReadTestApp(t)
	brief := seedMCPBriefSources(t, a)
	storeMCPBrief(t, brief)
	got := callMCPReadTest(t, a, "brief_today", map[string]any{})
	if len(got.Items) != 12 || got.Withheld != 1 || got.Total != 13 || got.Truncated {
		t.Fatalf("brief completeness: %+v", got)
	}
	sources := map[string]bool{}
	for _, item := range got.Items {
		sources[item.Source] = true
		if item.Title != nil && !item.Title.Untrusted {
			t.Fatal("trusted observed title")
		}
	}
	for _, source := range []string{"history", "purpose", "learning", "deliverable", "schedule_run", "file", "calendar_day", "summary", "news", "find", "activity_session", "calendar_event"} {
		if !sources[source] {
			t.Errorf("missing source %s", source)
		}
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "Never disclose") || strings.Contains(string(raw), "/private/path") {
		t.Fatalf("unvalidated brief prose or logs escaped: %s", raw)
	}
	if strings.Count(string(raw), "Brief meeting label") != 1 {
		t.Fatalf("brief meeting label lost: %s", raw)
	}
	if !strings.Contains(string(raw), "Current calendar event") {
		t.Fatalf("stale brief title replaced current calendar title: %s", raw)
	}
	// These read methods must not initialize any producer or run a retry.
	if a.cronScheduler != nil || a.topics != nil {
		t.Fatal("brief read initialized a service")
	}
}

func TestMCPReadBriefRechecksAllDerivedPrivacy(t *testing.T) {
	for _, gate := range []string{"protected", "sensitive", "ignored", "private", "pause"} {
		t.Run(gate, func(t *testing.T) {
			a := newMCPReadTestApp(t)
			brief := seedMCPBriefSources(t, a)
			storeMCPBrief(t, brief)
			switch gate {
			case "protected":
				if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='brief-capture'`); err != nil {
					t.Fatal(err)
				}
			case "sensitive":
				if err := a.store.SetUIState(a.ctx, "sensitive_apps", `{"com.example.editor":"Editor"}`); err != nil {
					t.Fatal(err)
				}
			case "ignored":
				if err := a.store.SetUIState(a.ctx, voiceInputAppsUIStateKey, `{"com.example.editor":"Editor"}`); err != nil {
					t.Fatal(err)
				}
			case "private":
				event, _ := json.Marshal(activitymon.ActivityEvent{Timestamp: time.Now().Add(-time.Hour).UnixMilli(), AppName: "Google Chrome", AppBundleID: "com.google.Chrome", WindowTitle: "Project - Incognito"})
				if _, err := a.activity.store.db.Exec(`UPDATE activity_events SET data=? WHERE session_id='brief-capture'`, string(event)); err != nil {
					t.Fatal(err)
				}
				if _, err := a.activity.store.db.Exec(`UPDATE file_activity SET app_bundle_id='com.google.Chrome',app_name='Google Chrome' WHERE id='document'`); err != nil {
					t.Fatal(err)
				}
			case "pause":
				at := time.Now().Add(-time.Hour).UnixMilli()
				raw, _ := json.Marshal([]observationPause{{Start: at - 10000, End: at + 70000}})
				if err := a.store.SetUIState(a.ctx, observationPausesKey, string(raw)); err != nil {
					t.Fatal(err)
				}
			}
			got := callMCPReadTest(t, a, "brief_today", map[string]any{})
			for _, item := range got.Items {
				switch item.Source {
				case "history", "purpose", "summary", "find", "activity_session", "file":
					t.Errorf("%s leaked %s", gate, item.Source)
				}
			}
			if got.Withheld < 7 {
				t.Fatalf("withheld provenance not counted: %+v", got)
			}
		})
	}
}

func TestMCPReadBriefRetiredPurposeCommandIsWithheld(t *testing.T) {
	a := newMCPReadTestApp(t)
	store, err := a.purposeProductStore()
	if err != nil {
		t.Fatal(err)
	}
	const key = "mcp-retired-purpose"
	at := time.Now().Add(-time.Minute).UnixMilli()
	// The removed product writer could leave valid immutable ledger rows.
	// Its source kind is now unsupported and must fail closed through MCP.
	evidence := purposeTestEvidence(key, PurposeEvidenceExplicitUser, PurposeStanceSupport, at)
	evidence.SourceKind = "purpose_product_user_command"
	evidence.SourceVersion = "purpose-product-command-v1"
	evidence.SourceID = deterministicAgencyID("purpose-product-source", key)
	evidence.IndependenceKey = evidence.SourceID
	input := purposeTestCreateInput(key, PurposeKindGoal, "legacy purpose", []PurposeEvidenceInput{evidence}, at)
	input.Confidence = 1
	created, added, err := store.Create(input)
	if err != nil || !added {
		t.Fatalf("create legacy ledger fixture: added=%v err=%v", added, err)
	}
	detail, err := store.GetDetail(created.OpenLoop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Eligible || len(detail.Evidence) != 1 {
		t.Fatalf("legacy ledger fixture is invalid: %+v", detail)
	}
	storeMCPBriefReferences(t, MobileBriefRef{SourceKind: "purpose", SourceID: created.OpenLoop.ID})
	got := callMCPReadTest(t, a, "brief_today", map[string]any{})
	if len(got.Items) != 0 || got.Withheld != 1 || got.Truncated {
		t.Fatalf("unsupported retired command escaped: %+v", got)
	}
}

func TestMCPReadBriefURLPrivacy(t *testing.T) {
	link := mcpReadBriefURL("https://user:password@example.org:443/release?token=secret#code")
	if link == nil || !link.Untrusted || link.Text != "https://example.org/release" {
		t.Fatalf("URL credentials or query escaped: %+v", link)
	}
	if mcpReadBriefURL("file:///private/document") != nil {
		t.Fatal("non-web URL escaped")
	}
}

func TestMCPReadBriefFileTopicAndNewsGates(t *testing.T) {
	a := newMCPReadTestApp(t)
	brief := seedMCPBriefSources(t, a)
	storeMCPBrief(t, brief)
	folder := filepath.Join(os.Getenv("HOME"), "Documents")
	raw, _ := json.Marshal([]string{folder})
	if err := a.store.SetUIState(a.ctx, filesPrivateFoldersKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	dir, _ := topicsStateDir()
	off := false
	if _, err := newTopicsStore(dir).update(func(st *topicsStateFile) { st.Enabled = &off }); err != nil {
		t.Fatal(err)
	}
	if err := a.store.UpdateCronRun(a.ctx, "news-run", map[string]any{"result_summary": withResearchMeta(`<news_json>[{"title":"private topic","url":"https://example.org/release"}]</news_json>`, researchMeta{URLs: []string{"https://example.org/release"}, Personal: researchPersonalYes})}); err != nil {
		t.Fatal(err)
	}
	got := callMCPReadTest(t, a, "brief_today", map[string]any{})
	for _, item := range got.Items {
		if item.Source == "file" || item.Source == "find" || item.Source == "news" {
			t.Fatalf("supplemental privacy leaked %+v", item)
		}
	}
	if got.Withheld != 4 {
		t.Fatalf("withheld=%d, want4", got.Withheld)
	}
}

func TestMCPReadBriefReportsLimitsAndRejectsTraversal(t *testing.T) {
	a := newMCPReadTestApp(t)
	brief := seedMCPBriefSources(t, a)
	original := brief.Sections.Moved[2]
	brief.Sections = MobileBriefSections{Learned: []MobileBriefItem{}}
	for i := 0; i < 25; i++ {
		item := original
		item.ID = fmt.Sprint(i)
		brief.Sections.Learned = append(brief.Sections.Learned, item)
	}
	storeMCPBrief(t, brief)
	got := callMCPReadTest(t, a, "brief_today", map[string]any{})
	if len(got.Items) != 20 || !got.Truncated || got.Bounded || got.Omitted != 5 || got.Total != 25 {
		t.Fatalf("unreported brief cap: %+v", got)
	}
	p, err := a.mcpReadPrivacy(a.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.briefReferenceItem(MobileBriefRef{SourceKind: "history", SourceID: "../outside"}); ok {
		t.Fatal("history path escaped its store")
	}
}

func TestMCPReadBriefFindsKeepOriginalSearchProvenance(t *testing.T) {
	for _, mode := range []string{"legacy", "replaced_sources", "other_sent_topic"} {
		t.Run(mode, func(t *testing.T) {
			a := newMCPReadTestApp(t)
			storeMCPBrief(t, seedMCPBriefSources(t, a))
			dir, _ := topicsStateDir()
			switch mode {
			case "legacy":
				if _, err := newTopicsStore(dir).update(func(st *topicsStateFile) { st.Finds.Sent[0].ReadSources = nil }); err != nil {
					t.Fatal(err)
				}
			case "replaced_sources":
				// Refreshing the interest to a safe chat cannot launder a search
				// that was informed by a now-protected capture.
				if _, err := newInterestsStore(dir).update(func(st *interestsStateFile) {
					st.Interests[0].Evidence = []interests.Ref{{Source: interests.SourceChat, Key: "chat:own-chat"}}
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='brief-capture'`); err != nil {
					t.Fatal(err)
				}
			case "other_sent_topic":
				if _, err := newTopicsStore(dir).update(func(st *topicsStateFile) {
					st.Finds.Sent = append(st.Finds.Sent, topicSent{ID: "missing-lineage", Label: "Other topic"})
				}); err != nil {
					t.Fatal(err)
				}
			}
			got := callMCPReadTest(t, a, "brief_today", map[string]any{})
			for _, item := range got.Items {
				if item.Source == "find" {
					t.Fatalf("%s bypassed original find provenance: %+v", mode, item)
				}
			}
			if got.Withheld < 2 {
				t.Fatalf("missing find withholding: %+v", got)
			}
		})
	}
}

func TestMCPReadBriefLearningRevalidatesFrozenSources(t *testing.T) {
	for _, mode := range []string{"safe", "no_manifest", "protected"} {
		t.Run(mode, func(t *testing.T) {
			a := newMCPReadTestApp(t)
			at := seedMCPReadActivity(t, a, "learning-source", "Editor", "com.example.editor", "Visible project", "", "")
			input := taskEpisodeFixture("episode-1", at, at+1, TaskEpisodeStatusActive)
			input.InitialEvidence[0].SourceID = "learning-source"
			if mode != "no_manifest" {
				input.FrozenContext.Manifest = []TaskContextManifestEntry{{SourceKind: "activity_session", SourceID: "learning-source", Included: true, PrivacyDecision: TaskContextPrivacyRedact}}
			}
			episode, _, err := a.activity.store.CreateTaskEpisode(input)
			if err != nil {
				t.Fatal(err)
			}
			projection := testShadowTaskLearningProjection()
			projection.Candidates[0].Evidence[0].ContextHash = episode.FrozenContext.SHA256
			if _, _, err := PersistShadowTaskLearningProjection(a.activity.store, projection, at+2); err != nil {
				t.Fatal(err)
			}
			if mode == "protected" {
				if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='learning-source'`); err != nil {
					t.Fatal(err)
				}
			}
			p, err := a.mcpReadPrivacy(a.ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, ok := p.briefReferenceItem(MobileBriefRef{SourceKind: "learning", SourceID: "revision:one"})
			if ok != (mode == "safe") {
				t.Fatalf("learning mode=%s allowed=%v", mode, ok)
			}
		})
	}
}

func TestTopicSentReadSourcesAreFrozenBeforeDispatch(t *testing.T) {
	source := interests.Interest{ID: "topic", Name: "Current topic", Evidence: []interests.Ref{{Source: interests.SourceActivity, Key: "activity:original"}}}
	sent := topicSentWithReadSources(topicSent{ID: "topic", Label: source.Name}, source, false)
	source.Evidence[0].Key = "activity:replacement"
	if sent.ReadSources == nil || sent.ReadSources.Evidence[0].Key != "activity:original" {
		t.Fatalf("source snapshot was not copied: %+v", sent)
	}
	if got := topicSentWithReadSources(topicSent{ID: "topic", Label: "Old topic"}, source, false); got.ReadSources != nil {
		t.Fatal("a concurrently changed topic received unrelated provenance")
	}
	if got := topicSentWithReadSources(topicSent{ID: "topic", Label: "My topic"}, interests.Interest{}, true); got.ReadSources == nil || !got.ReadSources.UserNamed {
		t.Fatal("explicit user name did not retain its origin")
	}
}

func TestMCPReadBriefExplicitPurposeAndURLPrivacy(t *testing.T) {
	a := newMCPReadTestApp(t)
	at := seedMCPReadActivity(t, a, "explicit-purpose-source", "Editor", "com.example.editor", "Visible purpose source", "", "")
	purposeStore, err := a.purposeProductStore()
	if err != nil {
		t.Fatal(err)
	}
	evidence := purposeTestEvidence("explicit", PurposeEvidenceExplicitUser, PurposeStanceSupport, at)
	evidence.SourceKind, evidence.SourceID = "activity_session", "explicit-purpose-source"
	input := purposeTestCreateInput("mcp-explicit-purpose", PurposeKindCommitment, "launch", []PurposeEvidenceInput{evidence}, at)
	input.Title = "Prepare the launch decision"
	created, _, err := purposeStore.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.mcpReadPrivacy(a.ctx)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := p.briefReferenceItem(MobileBriefRef{SourceKind: "purpose", SourceID: created.OpenLoop.ID})
	if !ok || item.Title == nil || item.Title.Text != "Prepare the launch decision" {
		t.Fatalf("explicit Purpose unavailable: %+v, allowed=%v", item, ok)
	}
	link := mcpReadBriefURL("https://user:password@example.org:443/release?token=secret#code")
	if link == nil || !link.Untrusted || link.Text != "https://example.org/release" {
		t.Fatalf("URL credentials or query escaped: %+v", link)
	}
	if mcpReadBriefURL("file:///private/document") != nil {
		t.Fatal("non-web URL escaped")
	}
}

// The MCP read goes through the brief reader: when a sealed copy may be shown,
// it reads the copy the computer and the phone show, so an item the copy left
// out stays out. A copy sealed under another privacy print is not used, and
// the stored brief is checked item by item, as before.
func TestMCPReadBriefReadsTheSealedCopy(t *testing.T) {
	a := newMCPReadTestApp(t)
	brief := seedMCPBriefSources(t, a)
	storeMCPBrief(t, brief)
	stateDir, err := mobileBriefStateDir()
	if err != nil {
		t.Fatal(err)
	}
	sealed := brief
	sealed.Sections.Moved = append([]MobileBriefItem{}, brief.Sections.Moved[1:]...)
	raw, err := marshalStoredMobileBrief(sealed)
	if err != nil {
		t.Fatal(err)
	}
	store := newMobileBriefStore(stateDir)
	save := func(print string) {
		t.Helper()
		read := mobileBriefFinishedRead{Schema: mobileBriefSealedSchema, Rule: mobileBriefSealRule, Epoch: mobileBriefPrivacyEpoch.Load(), Print: print, Day: time.Now().Format("2006-01-02"), Brief: raw}
		if err := store.saveFinishedRead(brief.BriefID, read); err != nil {
			t.Fatal(err)
		}
	}
	save(a.briefPrivacyPrint())
	got := callMCPReadTest(t, a, "brief_today", map[string]any{})
	if len(got.Items) != 11 || got.Withheld != 1 || got.Total != 12 {
		t.Fatalf("sealed read: %+v", got)
	}
	for _, item := range got.Items {
		if item.Source == "history" {
			t.Fatal("an item the sealed copy left out came back")
		}
	}
	save("another-print")
	got = callMCPReadTest(t, a, "brief_today", map[string]any{})
	if len(got.Items) != 12 || got.Withheld != 1 || got.Total != 13 {
		t.Fatalf("a copy under another print must not be read: %+v", got)
	}
}
