package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"

	"github.com/posthog/posthog-go"
)

func TestMorningBriefWeekQuestionIsWeeklyAndExplicit(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	now := time.Now()
	loc := time.Local
	start, err := time.ParseInLocation("2006-01-02", morningBriefWeekStart(now, loc), loc)
	if err != nil {
		t.Fatal(err)
	}
	brief := MobileBriefV1{Slot: mobileBriefSlotMorning, AsOf: formatMobileBriefTime(start.Add(8 * time.Hour)), Timezone: loc.String(), NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{{Text: "A brief."}}, Sections: MobileBriefSections{WorkingToward: []MobileBriefItem{{Title: "Launch", Refs: []string{"e1"}}}}}
	question := app.morningBriefWeekQuestion(brief, now)
	if question == nil || question.WeekStart != start.Format("2006-01-02") || question.Recommendation != "Launch" {
		t.Fatalf("question = %+v", question)
	}
	if err := app.store.SetUIState(context.Background(), "morning_brief_week_intent:"+question.WeekStart, "Launch"); err != nil {
		t.Fatal(err)
	}
	if app.morningBriefWeekQuestion(brief, now) != nil {
		t.Fatal("already answered question returned")
	}
	intent, err := app.morningBriefWeekIntent(t.Context(), start.AddDate(0, 0, 6), loc)
	if err != nil || intent != "Launch" {
		t.Fatalf("current week intent = %q, %v", intent, err)
	}
	next, err := app.morningBriefWeekIntent(t.Context(), start.AddDate(0, 0, 7), loc)
	if err != nil || next != "" {
		t.Fatalf("previous week's intent carried over: %q, %v", next, err)
	}
}

func TestMorningBriefWeekQuestionIsSpokenAndAudioBound(t *testing.T) {
	brief := MobileBriefV1{BriefID: "2026-10-26-morning", Slot: mobileBriefSlotMorning, AsOf: "2026-10-26T08:00:00Z", Timezone: "UTC", NarrativeStatus: "ok", rankedEdition: true,
		Narrative:    []MobileBriefPara{{Text: "Review the launch plan."}, {Text: "Want to review the plan?"}},
		WeekQuestion: &MorningBriefWeekQuestion{WeekStart: "2026-10-26", Recommendation: "Launch"}}
	spoken, _ := mobileBriefSpeech(brief, "en")
	question := "What is this week about? I'd start with Launch."
	if !strings.Contains(spoken, question) || strings.Index(spoken, question) > strings.Index(spoken, "Want to review") {
		t.Fatalf("weekly question missing or out of order: %q", spoken)
	}
	bound := mobileBriefAudioBinding(brief, "en")
	brief.WeekQuestion = nil
	if bound == mobileBriefAudioBinding(brief, "en") {
		t.Fatal("audio with the question stayed valid after it was answered")
	}
}

func TestMorningBriefWeekAnswerPersistsOnce(t *testing.T) {
	telemetry, telemetryClient := newRecordingTelemetry(t)
	now := time.Now()
	monday, err := time.ParseInLocation("2006-01-02", morningBriefWeekStart(now, time.Local), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	asOf := monday.Add(8 * time.Hour)
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: now})
	svc.app.mobileBriefSvc = svc
	svc.app.telemetry = telemetry
	work, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.app.handoffStore = work
	entry := handoff.Entry{ID: "week-item", Title: "Launch plan", Source: handoff.SourceMobile, Status: handoff.StatusNeedsReview, CreatedAt: asOf.Add(-time.Hour).UnixMilli(), UpdatedAt: asOf.Add(-time.Minute).UnixMilli()}
	if _, err := work.Save(entry); err != nil {
		t.Fatal(err)
	}
	input := mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: asOf, WindowStart: asOf.Add(-24 * time.Hour), Location: time.Local, Timezone: time.Local.String()}
	collectMobileBriefWork(svc.app, &input)
	brief := buildMobileBriefV1(input)
	if len(brief.Evidence) == 0 {
		t.Fatal("fixture has no readable evidence")
	}
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{{Text: "Review the launch plan.", Refs: []string{brief.Evidence[0].Ref}}}
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	if err := svc.app.SaveMorningBriefWeekIntent(brief.BriefID, "release isoform 1.0"); err != nil {
		t.Fatalf("save typed answer: %v", err)
	}
	view, err := svc.app.GetTodayBriefByID(brief.BriefID)
	if err != nil || view.WeekAnswer == nil || view.WeekAnswer.Answer != "release isoform 1.0" {
		t.Fatalf("saved answer missing from Today: %+v, %v", view.WeekAnswer, err)
	}
	if err := svc.app.ChangeMorningBriefWeekIntent(brief.BriefID, "release isoform 1.0", "Writing"); err != nil {
		t.Fatalf("change typed answer: %v", err)
	}
	if err := svc.app.ChangeMorningBriefWeekIntent(brief.BriefID, "release isoform 1.0", "Stale"); err == nil {
		t.Fatal("stale typed answer overwrote the newer answer")
	}
	if err := svc.app.store.SetUIState(t.Context(), "morning_brief_week_intent:"+morningBriefWeekStart(now, time.Local), ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.app.PostMorningBriefWeekAnswerFromChip(brief.BriefID, "", "Launch"); err != nil {
		t.Fatal(err)
	}
	main, err := svc.app.store.LoadMainSession(t.Context())
	if err != nil || main == nil {
		t.Fatalf("main session: %v", err)
	}
	rows, err := svc.app.store.LoadStreamItems(t.Context(), main.LocalID)
	if err != nil || len(rows) != 1 || rows[0].Kind != "user" || rows[0].Content != "Launch" {
		t.Fatalf("posted answer: %+v %v", rows, err)
	}
	if !strings.Contains(rows[0].AutomationContext, `"weekAnswerBriefId":"`+brief.BriefID+`"`) {
		t.Fatalf("week answer lost its question provenance: %q", rows[0].AutomationContext)
	}
	if err := svc.app.PostMorningBriefWeekAnswer(brief.BriefID, "", "Stale"); err == nil {
		t.Fatal("stale answer was posted")
	}
	rows, err = svc.app.store.LoadStreamItems(t.Context(), main.LocalID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("stale answer left a chat row: %+v %v", rows, err)
	}
	if got, err := svc.app.morningBriefWeekIntent(t.Context(), now, time.Local); err != nil || got != "Launch" {
		t.Fatalf("saved answer = %q, %v", got, err)
	}
	if err := svc.app.SaveMorningBriefWeekIntent(brief.BriefID, "Other"); err == nil {
		t.Fatal("stale question overwrote the answer")
	}
	readable, ok := svc.GetMobileBrief(brief.BriefID)
	if !ok || svc.app.todayWeekAnswer(readable, now) == nil || svc.app.todayWeekAnswer(readable, now).Answer != "Launch" {
		t.Fatal("desktop could not reopen the saved answer")
	}
	view, err = svc.app.GetTodayBriefByID(brief.BriefID)
	if err != nil || view.WeekAnswer == nil || view.WeekAnswer.Answer != "Launch" {
		t.Fatalf("desktop view lost the saved answer: %+v, %v", view.WeekAnswer, err)
	}
	if err := svc.app.PostMorningBriefWeekAnswer(brief.BriefID, "Launch", "Writing"); err != nil {
		t.Fatalf("change answer: %v", err)
	}
	if err := svc.app.ChangeMorningBriefWeekIntent(brief.BriefID, "Launch", "Stale"); err == nil {
		t.Fatal("stale revision overwrote the answer")
	}
	if got, err := svc.app.morningBriefWeekIntent(t.Context(), now, time.Local); err != nil || got != "Writing" {
		t.Fatalf("changed answer = %q, %v", got, err)
	}
	telemetryClient.mu.Lock()
	defer telemetryClient.mu.Unlock()
	var turns []posthog.Capture
	for _, message := range telemetryClient.messages {
		if event, ok := message.(posthog.Capture); ok && event.Event == "chat.turn_sent" {
			turns = append(turns, event)
		}
	}
	if len(turns) != 2 {
		t.Fatalf("turn count=%d", len(turns))
	}
	source, ok := turns[0].Properties["source"].(map[string]any)
	if !ok || source["surface"] != "today" || source["action"] != "T12" {
		t.Fatalf("chip source=%#v", turns[0].Properties["source"])
	}
	if _, ok := turns[1].Properties["source"]; ok {
		t.Fatalf("typed answer has source: %#v", turns[1].Properties["source"])
	}
}

func TestMorningBriefWeekIntentBindingWithdrawsChangedAnswer(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	now := time.Now()
	brief := MobileBriefV1{AsOf: formatMobileBriefTime(now), Timezone: time.Local.String()}
	key := "morning_brief_week_intent:" + morningBriefWeekStart(now, time.Local)
	if err := app.store.SetUIState(t.Context(), key, "Launch"); err != nil {
		t.Fatal(err)
	}
	binding := &mobileBriefModelBinding{WeekIntentDigest: mobileBriefModelDigest("Launch")}
	if !app.morningBriefWeekIntentBindingReadable(t.Context(), brief, binding) {
		t.Fatal("matching intent did not validate")
	}
	if err := app.store.SetUIState(t.Context(), key, "Writing"); err != nil {
		t.Fatal(err)
	}
	if app.morningBriefWeekIntentBindingReadable(t.Context(), brief, binding) {
		t.Fatal("changed intent retained its binding")
	}
}

func TestMorningBriefRecommendationMatchesCurrentSourceAcrossSections(t *testing.T) {
	prior := MobileBriefV1{Evidence: []MobileBriefRef{{Ref: "old", SourceKind: "purpose", SourceID: "project"}},
		Sections: MobileBriefSections{OpenLoops: []MobileBriefItem{{Key: "old-row", Title: "Old title", Refs: []string{"old"}}}}}
	current := MobileBriefV1{Evidence: []MobileBriefRef{{Ref: "new", SourceKind: "purpose", SourceID: "project"}},
		Sections: MobileBriefSections{WorkingToward: []MobileBriefItem{{Key: "new-row", Title: "Current project", Refs: []string{"new"}}}}}
	if got := morningBriefCurrentOpenTitle(prior, current, "old-row", nil); got != "Current project" {
		t.Fatalf("recommendation = %q", got)
	}
	current.Sections.WorkingToward[0].Status = "resolved"
	if got := morningBriefCurrentOpenTitle(prior, current, "old-row", nil); got != "" {
		t.Fatalf("resolved item was recommended: %q", got)
	}
}

func TestMorningBriefWeekQuestionSkipsExplicitlyExcludedRecommendation(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	if _, err := app.SaveMemoryEntry("", "# Brief preference\n\nKeep PT out of my morning brief."); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	start, err := time.ParseInLocation("2006-01-02", morningBriefWeekStart(now, time.Local), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	brief := MobileBriefV1{Slot: mobileBriefSlotMorning, AsOf: formatMobileBriefTime(start.Add(8 * time.Hour)), Timezone: time.Local.String(), NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{{Text: "A brief."}}, Evidence: []MobileBriefRef{{Ref: "e1", Label: "PT routine"}}, Sections: MobileBriefSections{WorkingToward: []MobileBriefItem{
			{Title: "Exercises", Refs: []string{"e1"}},
			{Kind: "commitment", Title: "Make sure I do some sort of calf race or exercise for AFO like every day", Refs: []string{"e3"}},
			{Title: "Launch", Refs: []string{"e2"}},
		}}}
	question := app.morningBriefWeekQuestion(brief, now)
	if question == nil || question.Recommendation != "Launch" {
		t.Fatalf("question = %+v", question)
	}
	brief.Sections.WorkingToward = brief.Sections.WorkingToward[:2]
	question = app.morningBriefWeekQuestion(brief, now)
	if question == nil || question.Recommendation != "" {
		t.Fatalf("question without an eligible recommendation = %+v", question)
	}
}

func TestLegacyMorningWeekAnswerProjection(t *testing.T) {
	now := time.Now()
	monday, err := time.ParseInLocation("2006-01-02", morningBriefWeekStart(now, time.Local), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: now})
	// The week is the app's local week; the test helper's UTC zone put "this
	// week" a week ahead every Sunday evening (UTC is already Monday).
	svc.location = time.Local
	svc.app.mobileBriefSvc = svc
	work, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.app.handoffStore = work
	asOf := monday.Add(time.Minute)
	_, err = work.Save(handoff.Entry{ID: "week-legacy", Title: "Launch plan", Source: handoff.SourceMobile, Status: handoff.StatusNeedsReview, CreatedAt: asOf.Add(-time.Hour).UnixMilli(), UpdatedAt: asOf.UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	input := mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: asOf, WindowStart: asOf.Add(-24 * time.Hour), Location: time.Local, Timezone: time.Local.String()}
	collectMobileBriefWork(svc.app, &input)
	brief := buildMobileBriefV1(input)
	if len(brief.Evidence) == 0 {
		t.Fatal("missing fixture evidence")
	}
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{{Text: "Review the launch plan.", Refs: []string{brief.Evidence[0].Ref}}}
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	main, err := svc.app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	key := "morning_brief_week_intent:" + morningBriefWeekStart(now, time.Local)
	if _, err := svc.app.store.PostUIStateAnswer(t.Context(), key, "", "release isoform 1.0", main.LocalID, "legacy-week"); err != nil {
		t.Fatal(err)
	}
	rows := []StreamItemData{{ID: "legacy-week", Kind: "user", Content: "release isoform 1.0"}, {ID: "other", Kind: "user", Content: "Unrelated"}}
	svc.app.projectLegacyWeekAnswer(t.Context(), rows)
	if rows[0].WeekAnswerBriefID != brief.BriefID || rows[0].AutomationContext != "" || rows[1].WeekAnswerBriefID != "" {
		t.Fatalf("legacy projection: %+v", rows)
	}
	if _, err := svc.app.store.PostUIStateAnswer(t.Context(), key, "release isoform 1.0", "release isoform 1.0", main.LocalID, "ambiguous"); err != nil {
		t.Fatal(err)
	}
	rows[0].WeekAnswerBriefID = ""
	svc.app.projectLegacyWeekAnswer(t.Context(), rows)
	if rows[0].WeekAnswerBriefID != "" {
		t.Fatal("ambiguous legacy answer was attributed")
	}
}

// Hub ruling, 2026-11-16 (ranked goal 1, the latest brief shows, words and
// audio): answering the Monday week question never takes the play button
// away. The recording asked the question and keeps playing, question as
// recorded, while it is made again without it; it is not marked as being
// made, so Today keeps the player.
func TestMorningBriefWeekAnswerKeepsThePlayButton(t *testing.T) {
	now := time.Now()
	monday, err := time.ParseInLocation("2006-01-02", morningBriefWeekStart(now, time.Local), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	asOf := monday.Add(8 * time.Hour)
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: now})
	svc.app.mobileBriefSvc = svc
	svc.speaker.voiceReady = func(string) bool { return true }
	work, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.app.handoffStore = work
	entry := handoff.Entry{ID: "week-item", Title: "Launch plan", Source: handoff.SourceMobile, Status: handoff.StatusNeedsReview, CreatedAt: asOf.Add(-time.Hour).UnixMilli(), UpdatedAt: asOf.Add(-time.Minute).UnixMilli()}
	if _, err := work.Save(entry); err != nil {
		t.Fatal(err)
	}
	input := mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: asOf, WindowStart: asOf.Add(-24 * time.Hour), Location: time.Local, Timezone: time.Local.String()}
	collectMobileBriefWork(svc.app, &input)
	brief := buildMobileBriefV1(input)
	if len(brief.Evidence) == 0 {
		t.Fatal("fixture has no readable evidence")
	}
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{{Text: "Review the launch plan.", Refs: []string{brief.Evidence[0].Ref}}}
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	// The voice reads the brief as the read shows it: with the question.
	shown, _, err := svc.finishedReadWithholding(brief)
	if err != nil || shown.WeekQuestion == nil {
		t.Fatalf("the Monday read asks no week question: %v", err)
	}
	question := *shown.WeekQuestion
	brief, _ = svc.store.load(brief.BriefID)
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mobileBriefAudioMimeWAV, Bytes: 4, DurationSeconds: 12, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("wave"))),
		binding: mobileBriefAudioBinding(shown, "en"), question: &question}
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.store.dir, brief.BriefID+".wav"), []byte("wave"), 0600); err != nil {
		t.Fatal(err)
	}
	plays := func(stage string) {
		t.Helper()
		stored, _ := svc.store.load(brief.BriefID)
		read, _, err := svc.finishedReadWithholding(stored)
		if err != nil || read.Audio == nil || read.Audio.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("wave"))) {
			t.Fatalf("%s: the read has no recording to play", stage)
		}
		if _, ok := svc.Audio(brief.BriefID); !ok {
			t.Fatalf("%s: the play button has no recording", stage)
		}
	}
	plays("before the answer")
	if err := svc.app.SaveMorningBriefWeekIntent(brief.BriefID, "release isoform 1.0"); err != nil {
		t.Fatal(err)
	}
	svc.wg.Wait()
	stored, _ := svc.store.load(brief.BriefID)
	if read, _, _ := svc.finishedReadWithholding(stored); read.WeekQuestion != nil {
		t.Fatal("the answered question is still asked")
	}
	plays("after the answer")
	sealed, ok := svc.savedSealedCopy(stored)
	if !ok {
		t.Fatal("no sealed copy")
	}
	if svc.markAudioRendering(brief.BriefID, mobileBriefAudioBinding(sealed, "en"), "en", now) {
		t.Fatal("the new recording took the old one's place before it was ready")
	}
	plays("while it is made again")
	// An excluded recommendation is not played from the old recording.
	recorded := *stored.Audio
	if question.Recommendation != "" && mobileBriefAudioAcrossWeekAnswer(sealed, &recorded, []string{question.Recommendation}) != nil {
		t.Fatal("a recording asking about an excluded recommendation still plays")
	}
}
