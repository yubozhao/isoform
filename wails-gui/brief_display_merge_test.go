package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBriefDeliveryMergeKeepsClocksMasksAndPrivacy(t *testing.T) {
	for _, tc := range []struct{ language, clock, mask string }{
		{"en", "9:00 AM", "an account number"},
		// The paragraph is English, so its mask reads in English (R46).
		{"zh-CN", "9:00", "an account number"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			app, svc, stored := newStoredMobileBriefPrivacyFixture(t)
			if err := app.store.SetUIState(t.Context(), uiLanguageStateKey, tc.language); err != nil {
				t.Fatal(err)
			}
			stored.Narrative[0].Text = "Review at 9am with [REDACTED:account_number]sed."
			paragraph := 0
			stored.Cards = []MobileBriefCard{{ID: "masked-subject", Kind: mobileBriefCardQuestion, Paragraph: &paragraph,
				Label: "Ask about [REDACTED:account_number]", Message: "Ask about [REDACTED:account_number]", Refs: stored.Narrative[0].Refs}}
			if err := svc.store.save(stored, svc.now()); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(stored)
			if got := app.mobileBriefForDelivery(stored); len(got.Cards) != 0 {
				t.Fatal("display copy admitted a prepared question about a hidden subject")
			}
			shown, ok := svc.GetMobileBrief(stored.BriefID)
			if !ok || len(shown.Narrative) != 1 {
				t.Fatalf("readable paragraph lost: %+v", shown)
			}
			text := shown.Narrative[0].Text
			if !strings.Contains(text, tc.clock) || !strings.Contains(text, tc.mask) || strings.Contains(text, "[REDACTED") || strings.Contains(text, "sed") {
				t.Fatalf("clock and mask projections diverged: %q", text)
			}
			if shown.Audio != nil || shown.BriefID != stored.BriefID || shown.Narrative[0].Refs[0] != stored.Narrative[0].Refs[0] {
				t.Fatal("legacy audio survived or source identity changed")
			}
			for _, card := range shown.Cards {
				if card.ID == "masked-subject" {
					t.Fatal("masked prepared question survived the phone read")
				}
			}
			app.closedEyes.enabled.Store(true)
			withdrawn, ok := svc.GetMobileBrief(stored.BriefID)
			if !ok || len(withdrawn.Narrative) != 0 || len(withdrawn.Cards) != 0 || withdrawn.Audio != nil {
				t.Fatalf("display normalization bypassed withdrawal: %+v", withdrawn)
			}
			after, ok := svc.store.load(stored.BriefID)
			raw, _ := json.Marshal(after)
			if !ok || string(raw) != string(before) {
				t.Fatal("delivery projections rewrote the stored brief")
			}
		})
	}
}

func TestBriefDisplayLocaleInheritsRequestCancellation(t *testing.T) {
	app, store := newLinkTestApp(t)
	if err := store.SetUIState(t.Context(), uiLanguageStateKey, "zh-CN"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	if got := app.modelDisplayLanguageContext(ctx); got != "en" {
		t.Fatalf("canceled locale read did not fall back: %q", got)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("canceled request received a fresh locale wait budget")
	}
	if got := app.modelDisplayLanguageContext(t.Context()); got != "zh-CN" {
		t.Fatalf("cancellation fallback was cached: %q", got)
	}
}

func TestBriefCardsMergeLocaleHonorsCanceledRequest(t *testing.T) {
	app, store := newLinkTestApp(t)
	release := blockStateStoreReads(t, store)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan MobileBriefV1, 1)
	go func() {
		done <- app.mobileBriefWithCardsFrom(ctx, MobileBriefV1{NarrativeStatus: "quiet"}, &todayChatSources{})
	}()
	select {
	case brief := <-done:
		if brief.NarrativeStatus != "quiet" {
			t.Fatal("canceled locale changed the quiet brief")
		}
	case <-time.After(100 * time.Millisecond):
		release()
		<-done
		t.Fatal("card display locale ignored the canceled request or waited on the reserved connection")
	}
}

func TestLegacyBriefAudioAdmissionSurvivesCardHygiene(t *testing.T) {
	for _, tc := range []struct {
		subject  string
		playable bool
	}{
		{"Name", true}, {"[masked]", true}, {"[...]", true}, {"[REDACTED:account_number]", false},
	} {
		t.Run(tc.subject, func(t *testing.T) {
			brief := MobileBriefV1{
				NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "The Atlas review is ready."}},
				Cards: []MobileBriefCard{{Kind: mobileBriefCardQuestion, Label: "Ask " + tc.subject, Message: "Ask about " + tc.subject}},
				Audio: &MobileBriefAudio{Status: mobileBriefAudioStatusOK},
			}
			shown := mobileBriefDisplay(brief, "en")
			if len(shown.Cards) != 0 || (shown.Audio != nil) != tc.playable || shown.Narrative[0].Text != brief.Narrative[0].Text {
				t.Fatal("question hygiene changed the existing legacy-audio admission rule")
			}
		})
	}
}
