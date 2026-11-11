package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

func TestFirstWeekEvidenceSelection(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	ctx := context.Background()
	// A Thursday: Friday's discovery slot is reserved for "Your week".
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	installed := now.AddDate(0, 0, -2)
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{Report: app.postEngagementReport})
	app.engagement.now = func() time.Time { return now }
	app.engagement.journey = app.checkEngagementJourney
	if err := app.store.SetUIState(ctx, firstWeekInstallKey, installed.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetUIState(ctx, onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	commitment, err := app.createUserPurpose(userPurposeCreate{Key: "first-week-commitment", Origin: "computer", Kind: PurposeKindCommitment, Text: "Review the launch memo"}, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.createUserPurpose(userPurposeCreate{Key: "first-week-goal", Origin: "computer", Kind: PurposeKindGoal, Text: "Prepare the launch"}, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	candidate, ok := app.firstWeekCandidate(ctx, installed, now, 2)
	if !ok || candidate.Params["stage"] != "commitment" || candidate.SubjectID != commitment.OpenLoop.ID {
		t.Fatalf("commitment: %+v %v", candidate, ok)
	}
	person, err := app.store.CreatePrincipal(ctx, storage.PrincipalRow{ID: "first-week-person", Kind: storage.PrincipalKindContact, DisplayName: "Dana"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.BumpPrincipalSighting(ctx, person.ID, "Editor", "", now.Add(-time.Minute).UnixMilli(), "fixture"); err != nil {
		t.Fatal(err)
	}
	candidate, ok = app.firstWeekCandidate(ctx, installed, now, 3)
	if !ok || candidate.Params["stage"] != "person" || candidate.SubjectID != person.ID {
		t.Fatalf("person: %+v %v", candidate, ok)
	}
	notes := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(notes, []byte("Meeting notes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.activity.store.SaveSession(ActivitySessionRow{ID: "first-week-meeting", IsMeeting: true, TranscriptPath: notes, StartTime: now.Add(-time.Hour).UnixMilli(), EndTime: now.Add(-time.Minute).UnixMilli(), AppNames: `["Editor"]`, TriageStatus: "done", CreatedAt: now.Add(-time.Hour).UnixMilli()}, nil, nil); err != nil {
		t.Fatal(err)
	}
	candidate, ok = app.firstWeekCandidate(ctx, installed, now, 4)
	if !ok || candidate.Params["stage"] != "meeting" || candidate.SubjectID != "first-week-meeting" {
		t.Fatalf("meeting: %+v %v", candidate, ok)
	}
	if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, candidate.SubjectID); err != nil {
		t.Fatal(err)
	}
	if app.firstWeekSourceReadable(ctx, candidate, now) {
		t.Fatal("protected meeting passed delivery check")
	}
	app.engagement.Pass(ctx)
	items := mainChatProactiveItems(t, app)
	if len(items) != 1 || items[0].Kind != "first_week" {
		t.Fatalf("day 2: %+v", items)
	}
	app.engagement.Pass(ctx)
	if len(mainChatProactiveItems(t, app)) != 1 {
		t.Fatal("daily duplicate")
	}
	fresh := newEngagementService(app.store, nil, engagementDelivery{Report: app.postEngagementReport})
	fresh.now, fresh.journey = app.engagement.now, app.checkEngagementJourney
	app.engagement = fresh
	if err := fresh.load(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = fresh.Submit(ctx, engagement.Moment{Kind: engagement.KindFirstWeek, SubjectID: person.ID, DedupeKey: "second-source-after-restart", Params: map[string]string{"stage": "person"}})
	if err != nil {
		t.Fatal(err)
	}
	fresh.Pass(ctx)
	if len(mainChatProactiveItems(t, app)) != 1 {
		t.Fatal("restart reset daily cap")
	}
	fresh.completion = app.engagementItemCompletedAt
	fresh.inferCompletedReceipts(ctx, now)
	stats, err := app.store.EngagementStatsBetween(ctx, installed, now)
	if err != nil || stats["first_week"].Acted != 0 {
		t.Fatal("open commitment counted as completed", stats, err)
	}
	now = now.Add(time.Hour)
	if _, err := app.moveUserPurpose(userPurposeTransition{Key: "first-week-resolved", Origin: "computer", OpenLoopID: commitment.OpenLoop.ID, ExpectedEventID: commitment.Current.ID, Action: userPurposeResolve}, now); err != nil {
		t.Fatal(err)
	}
	fresh.inferCompletedReceipts(ctx, now)
	stats, err = app.store.EngagementStatsBetween(ctx, installed, now)
	if err != nil || stats["first_week"].Acted != 1 {
		t.Fatal("resolved commitment not attributed", stats, err)
	}
	// Neither source text nor the reading's presentation is model history.
	raw, _ := json.Marshal(items[0])
	line := mainChatPromptReportLine(storage.StreamItemRow{ToolName: mainChatProactiveTool, ToolInput: string(raw), Content: items[0].Body})
	if strings.Contains(line, "launch memo") || strings.Contains(line, items[0].Body) {
		t.Fatal("display text became model history")
	}
}

func newJourneyRig(t *testing.T) (*App, *engagementTestRig) {
	t.Helper()
	rig := newEngagementTestRig(t)
	app := &App{store: rig.store, engagement: rig.svc}
	rig.svc.deliver.Report = app.postEngagementReport
	rig.svc.journey = app.checkEngagementJourney
	return app, rig
}

func finishJourneyOnboarding(t *testing.T, app *App, now time.Time) {
	t.Helper()
	app.initializeFirstWeek(context.Background(), now)
	if err := app.SaveUIState(onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
}

func TestFirstWeekOnboardingWelcomeRestartAndFirstBrief(t *testing.T) {
	app, rig := newJourneyRig(t)
	ctx := context.Background()
	rig.svc.Pass(ctx)
	if len(mainChatProactiveItems(t, app)) != 0 {
		t.Fatal("welcome during onboarding")
	}
	finishJourneyOnboarding(t, app, rig.clock)
	installed := app.firstWeekInstall(ctx)
	if !installed.Equal(rig.clock) {
		t.Fatal(installed)
	}
	rig.svc.Pass(ctx)
	items := mainChatProactiveItems(t, app)
	if len(items) != 1 || items[0].Kind != "first_week" || !strings.Contains(items[0].Body, "2026-10-13 08:00") {
		t.Fatalf("welcome: %+v", items)
	}
	if len(rig.desktop)+len(rig.pushed) != 0 {
		t.Fatal("welcome rang a doorbell")
	}
	for _, lang := range engagement.Languages {
		if items[0].Localized[lang].Body == "" {
			t.Fatalf("missing %s", lang)
		}
	}
	fresh := newEngagementService(rig.store, nil, engagementDelivery{Report: app.postEngagementReport})
	fresh.now, fresh.journey = rig.svc.now, app.checkEngagementJourney
	app.engagement = fresh
	if err := fresh.load(ctx); err != nil {
		t.Fatal(err)
	}
	fresh.Pass(ctx)
	if len(mainChatProactiveItems(t, app)) != 1 {
		t.Fatal("restart repeated welcome")
	}
	rig.clock = rig.clock.AddDate(0, 0, 1)
	if !app.produceFirstWeekBrief(MobileBriefV1{BriefID: "first-morning", Slot: mobileBriefSlotMorning}) {
		t.Fatal("no first brief explanation")
	}
	fresh.Pass(ctx)
	items = mainChatProactiveItems(t, app)
	if len(items) != 2 || !strings.Contains(items[1].Body, "This is your first brief") {
		t.Fatalf("brief: %+v", items)
	}
	if err := app.SaveUIState(onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	if !app.firstWeekInstall(ctx).Equal(installed) {
		t.Fatal("completion restarted install")
	}
}

func TestFirstWeekUpgradeDoesNotEnroll(t *testing.T) {
	app, rig := newJourneyRig(t)
	ctx := context.Background()
	if err := rig.store.SetUIState(ctx, onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	app.initializeFirstWeek(ctx, rig.clock)
	if err := app.SaveUIState(onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	if !app.firstWeekInstall(ctx).IsZero() {
		t.Fatal("upgrade enrolled")
	}
}

func TestFirstWeekAutomaticBriefBeginsOnDayOne(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(18, 0)}
	svc, gathers := newTestMobileBriefService(t, clock)
	app := svc.app
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{Report: app.postEngagementReport})
	app.engagement.now = clock.Now
	finishJourneyOnboarding(t, app, clock.Now())
	svc.checkDue(context.Background())
	if gathers.Load() != 0 {
		t.Fatal("automatic brief ran on day 0")
	}
	clock.Advance(13 * time.Hour)
	svc.checkDue(context.Background())
	if gathers.Load() != 1 {
		t.Fatal("first brief did not run on day 1")
	}
}

func TestFirstWeekQuietWelcomeAndMutedPlan(t *testing.T) {
	app, rig := newJourneyRig(t)
	rig.clock = time.Date(2026, 10, 12, 23, 0, 0, 0, rig.clock.Location())
	finishJourneyOnboarding(t, app, rig.clock)
	rig.svc.Pass(context.Background())
	if len(mainChatProactiveItems(t, app)) != 0 {
		t.Fatal("welcome broke quiet hours")
	}
	rig.clock = rig.clock.Add(9 * time.Hour)
	rig.svc.Pass(context.Background())
	items := mainChatProactiveItems(t, app)
	if len(items) != 1 {
		t.Fatalf("held welcome: %+v", items)
	}
	if _, err := app.ResolveMainChatProactiveAction(items[0].MomentID, engagement.ActionMuteKind); err != nil {
		t.Fatal(err)
	}
	if app.journeyEnabled(engagement.KindFirstWeek, rig.clock) {
		t.Fatal("not for me failed to mute")
	}
	if app.produceFirstWeekBrief(MobileBriefV1{BriefID: "muted", Slot: mobileBriefSlotMorning}) {
		t.Fatal("muted first-week brief")
	}
}

func TestBriefAudioOmitsWeeklyRecapWhenDailyBriefIsShown(t *testing.T) {
	for _, language := range engagement.Languages {
		t.Run(language, func(t *testing.T) {
			brief := conversationFixtureBrief(t, conversationFixtures(t)[0], language)
			closing, _ := mobileBriefClosing(brief, language)
			if closing.Text == "" {
				t.Fatal("fixture needs concrete offers")
			}
			// The daily view shows the narrative, not the weekly recap.
			brief.NarrativeStatus = "ok" // only the model's ok text is spoken (design R74)
			brief.Narrative = []MobileBriefPara{
				{Text: strings.Repeat("Review the plan. ", 16), Refs: closing.Refs},
				{Text: strings.Repeat("Check the budget. ", 33), Refs: closing.Refs},
				{Text: strings.Repeat("Call the vendor. ", 35), Refs: closing.Refs},
				closing,
			}
			for _, paragraph := range yourWeekParagraphs(yourWeekFixture(mobileBriefVoiceLang(language)), mobileBriefVoiceLang(language)) {
				brief.WeeklyRecap = append(brief.WeeklyRecap, MobileBriefPara{Text: paragraph.Text, Role: paragraph.Role})
			}
			before, _ := json.Marshal(brief)
			spoken := mobileBriefSpokenText(brief, language)
			spokenClosing := mobileBriefParagraphSpeech(brief, closing, language)
			if !strings.HasSuffix(spoken, spokenClosing) || strings.Count(spoken, spokenClosing) != 1 {
				t.Fatalf("closing must occur once after all weekly speech: %q", spoken)
			}
			for _, paragraph := range brief.Narrative {
				if !strings.Contains(spoken, mobileBriefParagraphSpeech(brief, paragraph, language)) {
					t.Fatal("speech lost part of the shown daily reading")
				}
			}
			for _, paragraph := range brief.WeeklyRecap {
				if strings.Contains(spoken, mobileBriefParagraphSpeech(brief, paragraph, language)) {
					t.Fatal("speech included an unshown weekly recap")
				}
			}
			after, _ := json.Marshal(brief)
			if string(before) != string(after) {
				t.Fatal("speech assembly mutated the written brief")
			}
		})
	}
}

func TestYourWeekSpeechDeclinesOversizedFixedReading(t *testing.T) {
	brief := MobileBriefV1{
		Evidence: []MobileBriefRef{
			{Ref: "e1", SourceKind: "history", SourceID: "one"},
			{Ref: "e2", SourceKind: "history", SourceID: "two"},
		},
		Sections: MobileBriefSections{WaitingOnYou: []MobileBriefItem{
			{Title: strings.TrimSpace(strings.Repeat("a ", 70)), Kind: "waiting_on_reply", Refs: []string{"e1"}},
			{Title: strings.TrimSpace(strings.Repeat("b ", 70)), Kind: "waiting_on_reply", Refs: []string{"e2"}},
		}},
	}
	closing, offers := mobileBriefClosing(brief, "en")
	if len(offers) != 2 {
		t.Fatal("fixture needs both bounded long-title offers")
	}
	brief.Narrative = []MobileBriefPara{closing}
	week := yourWeekFixture("en")
	for i := range week.Open {
		week.Open[i].Title = strings.TrimSpace(strings.Repeat("c ", 60))
	}
	for _, paragraph := range yourWeekParagraphs(week, "en") {
		brief.WeeklyRecap = append(brief.WeeklyRecap, MobileBriefPara{Text: paragraph.Text, Role: paragraph.Role})
	}
	fixed := append(append([]MobileBriefPara{}, brief.WeeklyRecap...), closing)
	if mobileBriefNarrativeWords(fixed) <= mobileBriefMaxWords {
		t.Fatal("fixture must exceed the shared budget without any daily body")
	}
	if spoken := mobileBriefSpokenText(brief, "en"); spoken != "" {
		t.Fatalf("oversized fixed reading should keep text without partial audio: %q", spoken)
	}
}
