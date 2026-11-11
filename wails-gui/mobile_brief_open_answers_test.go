package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"
)

func createMobileBriefOpenAnswerPost(t *testing.T, app *App, id, topic, question, content string, at time.Time, transitions ...string) string {
	t.Helper()
	transition := "settled"
	if len(transitions) > 0 && transitions[0] != "" {
		transition = transitions[0]
	}
	session, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	created := at.Add(-time.Hour)
	row := storage.OpenAnswerRow{
		ID: id, ChatSessionID: session.LocalID, AssistantMessageID: "promise-" + id,
		Question: question, Topic: topic, Settles: "result", Source: "Web",
		WindowStart: at.Add(-2 * time.Hour).UnixMilli(), WindowEnd: at.Add(2 * time.Hour).UnixMilli(),
		Delivery: "around", ExpiresAt: at.Add(7 * 24 * time.Hour).UnixMilli(),
		State: "open", CreatedAt: created.UnixMilli(),
	}
	if err := app.store.CreateOpenAnswer(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	messageID := "post-" + id
	if err := app.store.CommitOpenAnswerTransition(t.Context(), id, transition, at.UnixMilli(),
		&storage.StreamItemRow{ID: messageID, Kind: "assistant", Content: content, CreatedAt: at.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	return messageID
}

func TestMobileBriefUnseenOpenAnswerParagraph(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       string
		wantText   string
		wantRefs   int
		transition string
	}{
		{name: "unseen", wantText: "You asked: Question 0: Answer 0..", wantRefs: 1},
		{name: "interim", mode: "interim", wantText: "You asked: Question 0: Answer 0..", wantRefs: 1, transition: "interim"},
		{name: "opened", mode: "opened"},
		{name: "muted", mode: "muted"},
		{name: "closed eyes", mode: "closed"},
		{name: "several", mode: "several", wantText: "You asked: Question 0: Answer 0.; Question 1: Answer 1.; Question 2: Answer 2..", wantRefs: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := openAnswerAdmissionApp(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			brief := MobileBriefV1{Narrative: []MobileBriefPara{{Text: "The brief summary."}}, Cards: []MobileBriefCard{}}
			var ids []string
			count := 1
			if tc.mode == "several" {
				count = 4
			}
			for i := 0; i < count; i++ {
				at := now.Add(-time.Duration(i+1) * time.Hour)
				id := "brief-answer-" + string(rune('0'+i))
				ids = append(ids, createMobileBriefOpenAnswerPost(t, app, id, "topic "+id, "Question "+string(rune('0'+i)), "Answer "+string(rune('0'+i))+".\nSecond line.", at, tc.transition))
			}
			switch tc.mode {
			case "opened":
				answerID := "brief-answer-0"
				momentID := "brief-answer-moment"
				if _, err := app.store.CreateEngagementMoment(t.Context(), storage.EngagementMomentRow{
					ID: momentID, Kind: "answer_ready", SubjectID: answerID, DedupeKey: "answer_ready:" + answerID,
					CreatedAt: now.UnixMilli(), Params: `{"message_id":"post-brief-answer-0"}`,
				}); err != nil {
					t.Fatal(err)
				}
				if err := app.store.RecordEngagementReceipt(t.Context(), storage.EngagementReceiptRow{
					MomentID: momentID, Channel: "desktop", State: storage.EngagementReceiptOpened, At: now.UnixMilli(),
				}); err != nil {
					t.Fatal(err)
				}
			case "muted":
				if err := app.store.SetOpenAnswerKindMuted(t.Context(), "topic brief-answer-0", true); err != nil {
					t.Fatal(err)
				}
			case "closed":
				app.closedEyes.enabled.Store(true)
			}
			if tc.mode == "several" {
				brief.Evidence = []MobileBriefRef{}
			}
			app.appendOpenAnswerBriefParagraph(context.Background(), &brief, now.Add(-24*time.Hour), now, "en")
			if tc.wantText == "" && len(brief.Narrative) != 1 {
				t.Fatalf("unexpected appended paragraph: %+v", brief.Narrative)
			}
			if tc.wantText != "" {
				if len(brief.Narrative) != 2 || brief.Narrative[1].Text != tc.wantText || len(brief.Narrative[1].Refs) != tc.wantRefs {
					t.Fatalf("narrative = %+v", brief.Narrative)
				}
				if tc.mode == "several" {
					for i, wantID := range []string{"post-brief-answer-0", "post-brief-answer-1", "post-brief-answer-2"} {
						if brief.Evidence[i].SourceID != wantID || brief.Evidence[i].SourceKind != "chat_message" {
							t.Fatalf("evidence order = %+v", brief.Evidence)
						}
					}
				}
			}
			if len(brief.Cards) != 0 {
				t.Fatalf("answer paragraph created a card: %+v", brief.Cards)
			}
			if tc.mode == "unseen" || tc.mode == "interim" {
				ref, err := app.resolveTodayChatItem(context.Background(), TodayChatContextRef{SourceKind: "chat_message", SourceID: ids[0]}, todayChatSources{}, now)
				if err != nil || !strings.Contains(ref.Record.Text, "Answer 0.") {
					t.Fatalf("chat-message ref = %+v, %v", ref, err)
				}
			}
			if tc.mode == "unseen" {
				base := MobileBriefV1{NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "The brief summary."}}}
				withAnswer := brief
				if spoken := mobileBriefSpokenText(withAnswer, "en"); !strings.Contains(spoken, "You asked:") {
					t.Fatalf("spoken brief omitted the shown open-answer paragraph: %q", spoken)
				}
				if mobileBriefAudioBinding(base, "en") == mobileBriefAudioBinding(withAnswer, "en") {
					t.Fatal("open-answer paragraph did not change the audio binding")
				}
			}
			if tc.mode == "several" && !mobileBriefIsOpenAnswerParagraph(brief, brief.Narrative[1]) {
				t.Fatal("deterministic answer paragraph was not recognized")
			}
		})
	}
}

func TestMobileBriefUnseenOpenAnswerParagraphChinese(t *testing.T) {
	app := openAnswerAdmissionApp(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	createMobileBriefOpenAnswerPost(t, app, "brief-answer-zh", "topic", "结果何时公布？", "明天公布\n后续信息。", now.Add(-time.Hour))
	brief := MobileBriefV1{}
	app.appendOpenAnswerBriefParagraph(context.Background(), &brief, now.Add(-24*time.Hour), now, "zh-CN")
	if len(brief.Narrative) != 1 || brief.Narrative[0].Text != "你问过：结果何时公布？：明天公布。" || len(brief.Narrative[0].Refs) != 1 {
		t.Fatalf("Chinese paragraph = %+v", brief.Narrative)
	}
	raw, err := json.Marshal(brief.Narrative[0])
	if err != nil || !strings.Contains(string(raw), "你问过：") {
		t.Fatalf("paragraph encoding = %s, %v", raw, err)
	}
}

func TestMobileBriefUnseenOpenAnswerParagraphRecheckedAtDelivery(t *testing.T) {
	for _, muted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmuted", true: "muted"}[muted], func(t *testing.T) {
			app := newMobileBriefPurposeTestApp(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			session, err := app.ensureMainChat(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			const summaryMessageID = "brief-summary-message"
			if _, err := app.store.AppendStreamItem(t.Context(), session.LocalID, storage.StreamItemRow{
				ID: summaryMessageID, Kind: "assistant", Content: "The model's summary source.",
			}); err != nil {
				t.Fatal(err)
			}
			summaryRef := MobileBriefRef{
				Ref: "summary-source", SourceKind: "chat_message", SourceID: summaryMessageID,
				Label: "The model's summary source",
			}
			createMobileBriefOpenAnswerPost(t, app, "brief-answer-delivery", "topic delivery", "When is the result?",
				"Tomorrow.", now.Add(-time.Hour))
			brief := MobileBriefV1{
				NarrativeStatus: "ok",
				Narrative:       []MobileBriefPara{{Text: "The brief summary remains.", Refs: []string{summaryRef.Ref}}},
				Evidence:        []MobileBriefRef{summaryRef},
				Audio:           &MobileBriefAudio{Status: mobileBriefAudioStatusOK},
				modelBinding: &mobileBriefModelBinding{
					Version: 2, Synthesis: "stored",
					Sources: []mobileBriefModelSource{{Evidence: summaryRef, Digest: "stable"}},
				},
			}
			app.appendOpenAnswerBriefParagraph(t.Context(), &brief, now.Add(-24*time.Hour), now, "en")
			if len(brief.Narrative) != 2 || brief.Narrative[0].Text != "The brief summary remains." ||
				!strings.HasPrefix(brief.Narrative[1].Text, "You asked: ") {
				t.Fatalf("generated brief = %+v", brief.Narrative)
			}
			if muted {
				if err := app.store.SetOpenAnswerKindMuted(t.Context(), "topic delivery", true); err != nil {
					t.Fatal(err)
				}
			}
			delivered, withheld := app.mobileBriefDelivery(brief)
			if muted {
				if delivered.NarrativeStatus != "ok" || len(delivered.Narrative) != 1 ||
					delivered.Narrative[0].Text != "The brief summary remains." ||
					withheld.Reason != "" || delivered.Audio == nil {
					t.Fatalf("muted answer paragraph altered other narrative: %+v withholding=%+v", delivered.Narrative, withheld)
				}
			} else if len(delivered.Narrative) != 2 ||
				delivered.Narrative[0].Text != "The brief summary remains." ||
				!strings.HasPrefix(delivered.Narrative[1].Text, "You asked: ") ||
				delivered.Audio == nil {
				t.Fatalf("unmuted answer paragraph was withheld: %+v", delivered.Narrative)
			}
		})
	}
}

func TestMobileBriefUnseenAnswerParagraphStoreUnavailableIsTransient(t *testing.T) {
	app, svc, brief := newStoredMobileBriefPrivacyFixture(t)
	now := svc.now()
	createMobileBriefOpenAnswerPost(t, app, "brief-answer-not-ready", "topic not ready", "When is the result?",
		"Tomorrow.", now.Add(-time.Hour))
	app.appendOpenAnswerBriefParagraph(t.Context(), &brief, now.Add(-24*time.Hour), now, "en")
	if !containsOpenAnswerBriefParagraph(brief) {
		t.Fatalf("fixture has no answer paragraph: %+v", brief.Narrative)
	}
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}

	store := app.store
	app.store = nil
	defer func() { app.store = store }()

	delivered, withheld := app.mobileBriefDelivery(brief)
	if !withheld.Transient() {
		t.Fatalf("store-unavailable withholding = %+v, want transient", withheld)
	}
	if containsOpenAnswerBriefParagraph(delivered) {
		t.Fatalf("answer paragraph was delivered while the store was unavailable: %+v", delivered.Narrative)
	}
	if _, ok := svc.LatestMobileBrief(); !ok {
		t.Fatal("stored brief disappeared during a transient not-ready read")
	}
	persisted, ok := svc.store.load(brief.BriefID)
	if !ok || !containsOpenAnswerBriefParagraph(persisted) {
		t.Fatalf("transient read replaced the stored answer paragraph: %+v", persisted.Narrative)
	}
}

func containsOpenAnswerBriefParagraph(brief MobileBriefV1) bool {
	for _, paragraph := range brief.Narrative {
		if mobileBriefIsOpenAnswerParagraph(brief, paragraph) {
			return true
		}
	}
	return false
}

func TestMobileBriefUnseenOpenAnswerParagraphSurvivesQuietPublication(t *testing.T) {
	app := openAnswerAdmissionApp(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	createMobileBriefOpenAnswerPost(t, app, "brief-answer-quiet", "topic", "When is the result?", "Tomorrow.", now.Add(-time.Hour))
	brief := mobileBriefForPublication(MobileBriefV1{Slot: mobileBriefSlotMorning, NarrativeStatus: "ok"})
	if brief.NarrativeStatus != "quiet" {
		t.Fatalf("fixture status = %q", brief.NarrativeStatus)
	}
	app.appendOpenAnswerBriefParagraph(context.Background(), &brief, now.Add(-24*time.Hour), now, "en")
	if brief.NarrativeStatus != "ok" || len(brief.Narrative) != 1 || !strings.HasPrefix(brief.Narrative[0].Text, "You asked: ") {
		t.Fatalf("brief after answer paragraph = %+v", brief)
	}
}

func TestMobileBriefGenerationAppendsUnseenOpenAnswerParagraph(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(9, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.publish = func(MobileBriefV1, bool) error { return nil }
	createMobileBriefOpenAnswerPost(t, svc.app, "brief-answer-generated", "topic", "When is the result?",
		"Tomorrow.", clock.Now().Add(-time.Hour))
	brief, err := svc.generateWithMetadata(t.Context(), mobileBriefSlotMorning, mobileBriefReasonPhone, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.wg.Wait()
	if len(brief.Narrative) < 2 {
		t.Fatalf("generated brief narrative = status %q, %+v", brief.NarrativeStatus, brief.Narrative)
	}
	last := brief.Narrative[len(brief.Narrative)-1]
	if last.Text != "You asked: When is the result?: Tomorrow.." || len(last.Refs) != 1 ||
		len(brief.Evidence) == 0 || brief.Evidence[len(brief.Evidence)-1].SourceKind != "chat_message" ||
		brief.Evidence[len(brief.Evidence)-1].SourceID != "post-brief-answer-generated" {
		t.Fatalf("generated answer paragraph = %+v evidence=%+v", last, brief.Evidence)
	}
}
