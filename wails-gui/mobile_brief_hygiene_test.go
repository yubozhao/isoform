package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBriefHygieneShortBriefIsQuietAndNeverPublished(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.compose = composeMobileBriefNarrative
	var published atomic.Int32
	svc.publish = func(MobileBriefV1, bool) error { published.Add(1); return nil }
	brief, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSchedule)
	svc.wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	// A short template opening publishes: the word floor is for model text (design R74).
	if published.Load() != 1 || brief.NarrativeStatus == "quiet" || len(brief.Narrative) == 0 {
		t.Fatalf("a template opening was not published: published=%d status=%s words=%d", published.Load(), brief.NarrativeStatus, mobileBriefNarrativeWords(brief.Narrative))
	}
}

func TestBriefHygieneRequested99WordBriefIsPublished(t *testing.T) {
	for _, reason := range []string{mobileBriefReasonPhone, mobileBriefReasonSettings} {
		t.Run(reason, func(t *testing.T) {
			svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: mobileBriefTestDay(9, 0)})
			svc.speaker = fakeSpeaker("en", testWAV(1), nil)
			gather := svc.gather
			svc.gather = func(ctx context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
				input, err := gather(ctx, slot, windowStart, asOf)
				if err != nil {
					return input, err
				}
				input.WaitingOnYou = append(input.WaitingOnYou, mobileBriefCandidate{
					SourceKind: "history", SourceID: "h-" + slot, Title: "Dana",
					EvidenceLabel: "Shipped the plan", Kind: "waiting_on_reply", Status: "waiting", At: asOf.Add(-time.Hour),
				})
				return input, nil
			}
			svc.compose = func(_ context.Context, _ *App, brief MobileBriefV1, _ []MobileBriefCard) ([]MobileBriefPara, string, []mobileBriefCardWords) {
				refs := []string{brief.Evidence[0].Ref}
				closing := MobileBriefPara{Text: "Want me to review Shipped the plan?", Refs: refs}
				body := MobileBriefPara{Text: strings.TrimSpace(strings.Repeat("plan ", 99-mobileBriefNarrativeWords([]MobileBriefPara{closing}))), Refs: refs}
				brief.Narrative = []MobileBriefPara{body, closing}
				return brief.Narrative, "ok", []mobileBriefCardWords{{ID: "answer-1", Label: "Leave it", Message: "No, leave it.", AnswerTo: mobileBriefClosingDigest(brief)}}
			}
			var published atomic.Int32
			svc.publish = func(brief MobileBriefV1, _ bool) error {
				if mobileBriefNarrativeWords(brief.Narrative) != 99 || len(mobileBriefClosingAnswers(brief, brief.Cards)) != 1 || len(mobileBriefParagraphChipCards(brief.Cards)) != 0 {
					// The closing asks about the only item, so it gets no chip (today-review v1.1.4 #4).
					t.Errorf("request lost its text, answer or questions: %+v", brief)
				}
				published.Add(1)
				return nil
			}
			result, err := svc.RequestMobileBrief(reason)
			wantStatus := "started"
			if reason == mobileBriefReasonPhone {
				wantStatus = "making"
			}
			if err != nil || result.Status != wantStatus {
				t.Fatalf("request: %+v, %v", result, err)
			}
			svc.wg.Wait()
			brief, found := svc.LatestMobileBrief()
			if !found || brief.Slot != mobileBriefSlotOnRequest || brief.NarrativeStatus != "ok" || mobileBriefNarrativeWords(brief.Narrative) != 99 || brief.Audio == nil || published.Load() == 0 {
				t.Fatalf("requested brief was suppressed: found=%v published=%d brief=%+v", found, published.Load(), brief)
			}
		})
	}
}

func TestBriefHygienePreservesRealBracketedCopy(t *testing.T) {
	for _, text := range []string{"Review [RFC] retry policy", "Reply to Dana <dana@example.com>", "The API returns [1, 2, 3]", "Review [MAX_RETRIES]", "Reply to Dana <person@example.com>"} {
		t.Run(text, func(t *testing.T) {
			if !mobileBriefCopySafe(text) {
				t.Fatalf("real copy rejected: %q", text)
			}
			brief := buildMobileBriefV1(mobileBriefInput{Slot: "morning", AsOf: mobileBriefTestDay(7, 0), Location: time.UTC,
				Moved: []mobileBriefCandidate{{SourceKind: "history", SourceID: "h1", Title: text}}})
			brief.Narrative = []MobileBriefPara{{Text: text}}
			shown := mobileBriefHygiene(brief)
			if len(shown.Sections.Moved) != 1 || shown.Sections.Moved[0].Title != text || len(shown.Narrative) != 1 || shown.Narrative[0].Text != text {
				t.Fatalf("assembly/display dropped real work: %+v", shown)
			}
		})
	}
}

func TestBriefHygieneWithholdsMaskedQuestionSubjects(t *testing.T) {
	for _, mask := range []string{"[REDACTED:code]", "[REDACTED:account_number]", "[masked]", "[...]"} {
		for _, field := range []string{"label", "message", "long-label"} {
			t.Run(mask+"/"+field, func(t *testing.T) {
				brief := MobileBriefV1{BriefID: "2026-10-09-on_request-090000", Slot: mobileBriefSlotOnRequest, LocalDate: "2026-10-09", AsOf: formatMobileBriefTime(mobileBriefTestDay(9, 0)), NarrativeStatus: "ok",
					Evidence:  []MobileBriefRef{{Ref: "e1", SourceKind: "activity", SourceID: "review"}},
					Sections:  MobileBriefSections{WorkingToward: []MobileBriefItem{{ID: "goal-1", Title: "Atlas plan", Kind: "goal", Refs: []string{"e1"}}}},
					Narrative: []MobileBriefPara{{Text: "The Atlas plan is ready.", Refs: []string{"e1"}}}}
				questions := cardsByKind(mobileBriefPatternCards(brief, "English"), mobileBriefCardQuestion)
				if len(questions) == 0 {
					t.Fatal("fixture needs a valid retained question")
				}
				card := questions[0]
				card.Writer = mobileBriefCardWriterModel
				if field == "label" {
					card.Label = "Ask about " + mask
				} else if field == "long-label" {
					card.Label = strings.Repeat("Review ", 6) + mask
				} else {
					card.Message = "Ask about " + mask
				}
				brief.Cards = []MobileBriefCard{card}
				if got := mobileBriefApplyCardWords(brief, brief.Cards, nil, "English"); len(got) != 0 {
					t.Fatalf("generated chip hid its mask during shortening: %+v", got)
				}
				assembled := mobileBriefForPublication(brief)
				if len(assembled.Cards) != 0 || len(assembled.Narrative) != 1 {
					t.Fatalf("assembly leaked a masked question or lost prose: %+v", assembled)
				}
				// Use the real retained-document preparation boundary, including
				// card reconciliation, rather than only testing the predicate.
				store := newMobileBriefStore(t.TempDir())
				if err := store.save(brief, mobileBriefTestDay(9, 0)); err != nil {
					t.Fatal(err)
				}
				retained, found := store.latest()
				if !found {
					t.Fatal("retained fixture not saved")
				}
				shown := (&App{}).mobileBriefWithCardsFrom(t.Context(), retained, &todayChatSources{})
				for _, got := range shown.Cards {
					if got.ID == card.ID || mobileBriefPrivacyMask.MatchString(got.Label) || mobileBriefPrivacyMask.MatchString(got.Message) {
						t.Fatalf("retained masked question survived: %+v", got)
					}
				}
				if len(brief.Cards) != 1 || (field == "label" && brief.Cards[0].Label != card.Label) {
					t.Fatal("hygiene mutated its input")
				}
			})
		}
	}
}

func TestBriefHygieneQuietStateContent(t *testing.T) {
	now := mobileBriefTestDay(0, 11)
	brief := buildMobileBriefV1(mobileBriefInput{Slot: "on_request", AsOf: now, WindowStart: now.Add(-6 * time.Hour), Location: time.UTC})
	brief.NarrativeStatus = "quiet"
	view := todayBriefView(now, time.UTC, brief, true, MobileBriefHealth{Enabled: true, NextRunAt: formatMobileBriefTime(mobileBriefTestDay(7, 0)), NextSlot: "morning"}, false, nil)
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if string(fields["quiet"]) != "true" || !view.Empty || view.NextAt != formatMobileBriefTime(mobileBriefTestDay(7, 0)) {
		t.Fatalf("quiet view lacks state/next slot: %s", raw)
	}
}

func TestBriefHygienePlaceholderQuestionDropped(t *testing.T) {
	for _, text := range []string{
		"Want me to open the follow-up field in Activity for Name?",
		"Want me to draft a reply to {{person}}?",
		"Want me to draft a reply to [CONTACT_NAME]?",
		"Want me to draft a reply to <name>?",
		"Want me to draft a reply about [project]?",
		"Want me to draft a reply to %s?",
		"Want me to draft a reply to __name__?",
		"Want me to draft a reply to $person?",
		"Want me to draft a reply to ?",
	} {
		raw, _ := json.Marshal(map[string]any{"paragraphs": []map[string]any{
			{"text": "The Meridian proposal is ready for review.", "refs": []string{"e1"}},
			{"text": text, "refs": []string{"e1"}},
		}})
		paragraphs, err := parseMobileBriefNarrative(string(raw), []MobileBriefRef{{Ref: "e1"}})
		if err != nil || len(paragraphs) != 1 || strings.Contains(paragraphs[0].Text, "?") {
			t.Errorf("placeholder survived: %q => %+v, %v", text, paragraphs, err)
		}
	}
}

func TestBriefHygieneSlotNaming(t *testing.T) {
	for _, tc := range []struct {
		slot string
		hour int
		want string
	}{
		{"morning", 7, "Morning brief"}, {"evening", 18, "Evening brief"},
		{"on_request", 0, "Morning brief"}, {"on_request", 9, "Morning brief"}, {"on_request", 20, "Evening brief"},
	} {
		got := mobileBriefFeedItemTitle(mobileBriefFeedItem{Slot: tc.slot, AsOf: mobileBriefTestDay(tc.hour, 11)})
		if got != tc.want {
			t.Errorf("%s at %d: %q, want %q", tc.slot, tc.hour, got, tc.want)
		}
	}
}

func TestBriefHygienePublicationBoundary(t *testing.T) {
	for _, slot := range []string{mobileBriefSlotMorning, mobileBriefSlotEvening} {
		for _, words := range []int{0, 99, 100, 101} {
			brief := MobileBriefV1{Slot: slot, NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: strings.Repeat("work ", words)}}}
			got := mobileBriefForPublication(brief)
			if (got.NarrativeStatus == "quiet") != (words < 100) {
				t.Errorf("%s: %d words: %s", slot, words, got.NarrativeStatus)
			}
		}
	}
	if got := mobileBriefForPublication(MobileBriefV1{Slot: mobileBriefSlotEvening, NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: strings.Repeat("工作", 100)}}}); got.NarrativeStatus == "quiet" {
		t.Fatal("Chinese reading units were treated as whitespace words")
	}
}

func TestBriefHygieneKeepsItemCardWhenParagraphIsWithheld(t *testing.T) {
	paragraph := 1
	brief := MobileBriefV1{
		NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{
			{Text: "The Meridian proposal is ready.", Refs: []string{"e1"}},
			{Text: "Want me to draft a reply to Name?", Refs: []string{"e1"}},
		},
		Cards: []MobileBriefCard{
			{
				ID: "item:meridian", Kind: mobileBriefCardItem, Mode: mobileBriefCardSend,
				Paragraph: &paragraph, Label: "Open Meridian proposal", Message: "Review the Meridian proposal?",
				Refs: []string{"e1"}, ItemKey: "meridian", Facts: "proposal-facts",
			},
			{
				ID: "question:meridian", Kind: mobileBriefCardQuestion, Mode: mobileBriefCardSend,
				Paragraph: &paragraph, Label: "Review Meridian proposal", Message: "Review the Meridian proposal?",
				Refs: []string{"e1"},
			},
		},
	}

	got := mobileBriefHygiene(brief)
	if len(got.Narrative) != 1 || got.Narrative[0].Text != brief.Narrative[0].Text {
		t.Fatalf("narrative = %+v, want only the safe paragraph", got.Narrative)
	}
	if len(got.Cards) != 1 {
		t.Fatalf("cards = %+v, want the linked item only", got.Cards)
	}
	card := got.Cards[0]
	if card.ID != "item:meridian" || card.Paragraph != nil || card.Label != "" ||
		card.Message != "Review the Meridian proposal?" || card.ItemKey != "meridian" || card.Facts != "proposal-facts" {
		t.Fatalf("item card was not preserved without its paragraph: %+v", card)
	}
	if brief.Cards[0].Paragraph == nil || *brief.Cards[0].Paragraph != paragraph || brief.Cards[0].Label == "" {
		t.Fatal("hygiene mutated the source item card")
	}
}

func TestBriefHygienePromptWordFloorIsOnlyScheduled(t *testing.T) {
	for _, slot := range []string{mobileBriefSlotMorning, mobileBriefSlotEvening, mobileBriefSlotOnRequest} {
		prompt := mobileBriefTestPrompt(MobileBriefV1{Slot: slot}, "English")
		if strings.Contains(prompt, "under 100 words stays quiet") != (slot != mobileBriefSlotOnRequest) {
			t.Errorf("word floor instruction for %s", slot)
		}
	}
}

func TestBriefHygieneReadGuardDropsPlaceholderAndItsQuestions(t *testing.T) {
	index := 1
	brief := MobileBriefV1{Narrative: []MobileBriefPara{
		{Text: "The Meridian proposal is ready."},
		{Text: "Want me to draft a reply for Name?"},
	}, Cards: []MobileBriefCard{{ID: "q", Kind: mobileBriefCardQuestion, Paragraph: &index, Message: "Draft a reply"}}, Audio: &MobileBriefAudio{Bytes: 123}}
	got := mobileBriefHygiene(brief)
	if len(got.Narrative) != 1 || len(got.Cards) != 0 || got.Audio != nil {
		t.Fatalf("unsafe cached copy survived: %+v", got)
	}
	if len(brief.Narrative) != 2 || brief.Cards[0].Paragraph == nil {
		t.Fatal("read guard mutated storage")
	}
}

// The configured slot times no longer name a brief asked for: before noon
// it is the morning brief, from noon the evening brief (today-review v1.1.16 D14).
func TestBriefHygieneConfiguredSlotClock(t *testing.T) {
	settings := defaultMobileBriefSettings()
	settings.Morning, settings.Evening = "09:30", "20:15"
	for _, tc := range []struct {
		hour, minute int
		want         string
	}{{2, 4, "morning"}, {9, 29, "morning"}, {9, 30, "morning"}, {12, 0, "evening"}, {20, 14, "evening"}, {20, 15, "evening"}} {
		brief := MobileBriefV1{Slot: "on_request", AsOf: formatMobileBriefTime(mobileBriefTestDay(tc.hour, tc.minute)), Timezone: "UTC"}
		if got := mobileBriefDisplaySlot(brief, settings); got != tc.want {
			t.Errorf("%d:%d: %s", tc.hour, tc.minute, got)
		}
	}
}

func TestBriefHygieneQuietScreenCountIsRealAndBounded(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	start, end := mobileBriefTestDay(0, 0), mobileBriefTestDay(7, 0)
	insertTestSession(t, app.activity.store, "quiet-readable", "done", start.UnixMilli())
	insertTestSession(t, app.activity.store, "quiet-protected", "protected", start.UnixMilli())
	if _, err := app.activity.store.db.Exec(`DELETE FROM activity_snapshots`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id string
		at time.Time
	}{{"quiet-readable", start.Add(-time.Millisecond)}, {"quiet-readable", start}, {"quiet-readable", end.Add(-time.Millisecond)}, {"quiet-readable", end}, {"quiet-protected", start.Add(time.Hour)}} {
		if _, err := app.activity.store.db.Exec(`INSERT INTO activity_snapshots (session_id, timestamp, file_path) VALUES (?, ?, '')`, tc.id, tc.at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	count := app.mobileBriefQuietScreens(start, end)
	if count == nil || *count != 2 {
		t.Fatalf("screens=%v, want 2", count)
	}
	app.closedEyes.enabled.Store(true)
	if got := app.mobileBriefQuietScreens(start, end); got != nil {
		t.Fatalf("paused screen count=%v", got)
	}
}

func TestBriefHygieneQuietSlotsStayOutOfPhoneHistory(t *testing.T) {
	now := mobileBriefTestDay(18, 0)
	store := newMobileBriefStore(t.TempDir())
	posted := mobileBriefStoredBrief(t, "2026-10-09", "morning", 7)
	// A posted brief has written words.
	posted.NarrativeStatus, posted.Narrative = "ok", []MobileBriefPara{{Text: madeAheadWords()}}
	quiet := mobileBriefStoredBrief(t, "2026-10-09", "evening", 18)
	quiet.NarrativeStatus = "quiet"
	for _, brief := range []MobileBriefV1{posted, quiet} {
		if err := store.save(brief, now); err != nil {
			t.Fatal(err)
		}
	}
	svc := &mobileBriefService{store: store}
	if rows := svc.ListMobileBriefs(10); len(rows) != 1 || rows[0].BriefID != posted.BriefID {
		t.Fatalf("phone history=%+v", rows)
	}
	latest, ok := latestPostedMobileBrief(svc)
	if !ok || latest.BriefID != posted.BriefID {
		t.Fatalf("phone latest=%s, found=%v", latest.BriefID, ok)
	}
	if start := svc.windowStart(now.Add(time.Hour)); !start.Equal(mobileBriefAsOfTime(posted)) {
		t.Fatalf("quiet slot advanced the coverage start to %s", start)
	}
}

func TestBriefHygienePreservesPrivacyMasks(t *testing.T) {
	for _, mask := range []string{"[REDACTED:code]", "[REDACTED:account_number]", "[masked]", "[...]"} {
		text := "The order " + mask + " is ready for review."
		if !mobileBriefCopySafe(text) || mobileBriefSafeLines(text) != text {
			t.Errorf("privacy mask mistaken for a template: %q", text)
		}
		if mobileBriefCopySafe(text + " Reply to Name?") {
			t.Errorf("mask bypassed the placeholder guard: %q", text)
		}
	}
}
