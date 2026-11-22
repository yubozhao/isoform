package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/storage"
)

func TestBriefDisplayProjectionCoversCopyWithoutRewritingEvidence(t *testing.T) {
	mask := "[REDACTED:account_number]"
	brief := MobileBriefV1{
		BriefID: "original-id", NarrativeStatus: "ok",
		Narrative:   []MobileBriefPara{{Text: "The contract " + mask + "sed out.", TopicName: mask}},
		WeeklyRecap: []MobileBriefPara{{Text: mask}},
		Cards:       []MobileBriefCard{{ID: "card-id", Label: mask, Message: mask, Facts: mask, AnswerTo: "binding"}},
		Offers:      []MobileBriefOffer{{Label: mask, Prompt: mask, Text: mask}},
		Sections:    MobileBriefSections{Plan: []MobileBriefItem{{Title: mask, Detail: mask, NextStep: mask, AttentionReason: mask, Context: &MobileBriefItemContext{Preview: mask}, Outcome: &ActivityThreadOutcome{Summary: mask, Question: mask}}}},
		Evidence:    []MobileBriefRef{{SourceID: mask, Label: mask}},
	}
	before, _ := json.Marshal(brief)
	for _, language := range []string{"en", "zh-CN"} {
		got := displayModelCopy(brief, language)
		if strings.Contains(got.Narrative[0].Text, "[REDACTED") || strings.Contains(got.Narrative[0].Text, "sed") {
			t.Fatal(got.Narrative)
		}
		copyOnly := []any{got.Narrative, got.WeeklyRecap, got.Offers, got.Sections, got.Cards[0].Label, got.Cards[0].Message, got.Evidence[0].Label}
		raw, _ := json.Marshal(copyOnly)
		if strings.Contains(string(raw), "[REDACTED") {
			t.Fatalf("copy leaked: %s", raw)
		}
		if got.BriefID != brief.BriefID || got.Evidence[0].SourceID != mask || got.Cards[0].Facts != mask || got.Cards[0].AnswerTo != "binding" {
			t.Fatal("display changed identities/proof")
		}
		if !reflect.DeepEqual(got, displayModelCopy(got, language)) {
			t.Fatal("projection not idempotent")
		}
	}
	after, _ := json.Marshal(brief)
	if string(before) != string(after) {
		t.Fatal("projection mutated stored/cached input")
	}
}

func TestBriefTTSInputNeverContainsMasksOrWordFragments(t *testing.T) {
	for _, language := range []string{"en", "zh-CN"} {
		brief := MobileBriefV1{BriefID: "2026-10-23-evening", Slot: "evening", NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "The contract [REDACTED:account_number]sed out."}}}
		speaker := fakeSpeaker(language, testWAV(1), nil)
		called := false
		speaker.synthesize = func(_ context.Context, _, text string) ([]byte, error) {
			called = true
			if strings.Contains(text, "[REDACTED") || strings.Contains(text, "sed") {
				t.Fatalf("unsafe TTS input: %q", text)
			}
			if text != speechDisplayText(brief.Narrative[0].Text, language) {
				t.Fatalf("screen/speech mismatch: %q", text)
			}
			return testWAV(1), nil
		}
		_, ok, _ := renderMobileBriefAudio(context.Background(), speaker, t.TempDir(), brief)
		if !ok || !called {
			t.Fatal("synthesis was not exercised")
		}
		brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK}
		if mobileBriefAudioForRead(brief) != nil {
			t.Fatal("legacy recording with raw masks remains playable")
		}
	}
}

func TestTopicAndChatDisplayProjection(t *testing.T) {
	mask := "[REDACTED:private_email]"
	raw := TopicView{ID: "topic-id", Name: mask, Latest: &TopicMoment{Text: mask}, Next: &TopicMoment{Text: mask}, Why: &TopicWhy{InterestWhy: mask}, PeopleLine: &TopicPeopleLine{Names: []string{mask}}}
	shown := displayModelCopy(raw, "zh-CN")
	bytes, _ := json.Marshal(shown)
	if strings.Contains(string(bytes), "[REDACTED") || shown.ID != raw.ID || raw.Name != mask {
		t.Fatal(string(bytes))
	}
	items := []StreamItemData{{Kind: "assistant", Content: mask}}
	(&App{}).projectStreamItems(items)
	if strings.Contains(items[0].Content, "[REDACTED") {
		t.Fatal(items)
	}
}

func TestBriefAudioBindingRejectsOldMaskRecordingAndAcceptsCleanBackfill(t *testing.T) {
	brief := MobileBriefV1{BriefID: "2026-10-23-evening", AsOf: "2026-10-23T20:00:00Z", NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "The contract [REDACTED:account_number]sed out."}}}
	// This is the previous binding's exact shape, which hashed the raw text.
	old := mobileBriefModelDigest(struct {
		ID, AsOf, Status  string
		Narrative, Weekly [][]string
		Template, Closing string
	}{brief.BriefID, brief.AsOf, brief.NarrativeStatus, [][]string{{brief.Narrative[0].Text}}, [][]string{}, "", ""})
	brief.Audio = &MobileBriefAudio{binding: "en:" + old}
	if mobileBriefAudioForRead(brief) != nil {
		t.Fatal("old recording remains playable")
	}
	brief.Audio.binding = mobileBriefAudioBinding(brief, "en")
	clean := displayModelCopy(brief, "en")
	if mobileBriefAudioForRead(brief) == nil || mobileBriefAudioForRead(clean) == nil || mobileBriefAudioBinding(clean, "en") != brief.Audio.binding {
		t.Fatal("clean backfill cannot bind stored masks and displayed text")
	}
}

type displayGuardSpeechRecorder struct {
	text  string
	texts []string
}

func (r *displayGuardSpeechRecorder) Synthesize(text string) ([]byte, error) {
	r.text = text
	r.texts = append(r.texts, text)
	return testWAV(1), nil
}
func (*displayGuardSpeechRecorder) Close() {}

func TestSpeechBindingSanitizesBeforeCallingEngine(t *testing.T) {
	recorder := &displayGuardSpeechRecorder{}
	app, _ := setupVoiceFixture(t)
	app.ensureTtsState().newEngine = func(string) (speechSynthesizer, error) { return recorder, nil }
	_, err := app.SynthesizeSpeech("The contract [REDACTED:account_number]sed out.")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(recorder.text, "[REDACTED") || strings.Contains(recorder.text, "sed") || !strings.Contains(recorder.text, "an account number") {
		t.Fatalf("engine received %q", recorder.text)
	}
}

func TestTopicSettingsAndStructuredSummariesUseDisplayCopy(t *testing.T) {
	mask := "[REDACTED:account_number]"
	topic := UserTopic{ID: "topic-id", Label: mask, Original: mask, Why: mask}
	raw, _ := json.Marshal(displayModelCopy(topic, "zh-CN"))
	if strings.Contains(string(raw), "[REDACTED") {
		t.Fatal(string(raw))
	}
	report, _ := json.Marshal(mainChatReport{Summary: mask})
	shown := displayModelToolInput(mainChatReportTool, string(report), "zh-CN")
	if strings.Contains(shown, "[REDACTED") || !strings.Contains(shown, "一个账号") {
		t.Fatal(shown)
	}
	untouched := `{"summary":"Keep these bytes", "unknownFutureField":true}`
	if displayModelToolInput(mainChatReportTool, untouched, "en") != untouched {
		t.Fatal("changed an unrelated payload")
	}
}

func TestLegacyBriefAudioIsWithheldBeforeHygieneDropsMaskedLines(t *testing.T) {
	brief := MobileBriefV1{Narrative: []MobileBriefPara{{Text: "The review is ready."}}, WeeklyRecap: []MobileBriefPara{{Text: "Reply to Name about [REDACTED:account_number]sed?"}}, Audio: &MobileBriefAudio{Status: mobileBriefAudioStatusOK}}
	shown := mobileBriefDisplay(brief, "en")
	if shown.Audio != nil {
		t.Fatal("hygiene hid the mask before the legacy recording was checked")
	}
	if shown.Narrative[0].Text != brief.Narrative[0].Text {
		t.Fatal("unrelated narrative changed")
	}
	if !strings.Contains(brief.WeeklyRecap[0].Text, "[REDACTED") {
		t.Fatal("display mutated storage")
	}
}

func TestPhoneChatDisplayUsesPreparedLocaleAndKeepsIdentity(t *testing.T) {
	rows := []storage.StreamItemRow{{ID: "message", Kind: "assistant", Content: "请核对[REDACTED:account_number]sed。", SortOrder: 1}}
	page := mobileChatPageWithView(rows, false, false, 0, nil, "zh-CN")
	if len(page.Items) != 1 || page.Items[0].ID != "message" || strings.Contains(page.Items[0].Text, "[REDACTED") || strings.Contains(page.Items[0].Text, "sed") || !strings.Contains(page.Items[0].Text, "一个账号") {
		t.Fatalf("phone copy: %+v", page.Items)
	}
	if rows[0].Content != "请核对[REDACTED:account_number]sed。" {
		t.Fatal("phone projection changed stored row")
	}
}

func TestBriefPromptHasOneCompactPrivacyInstruction(t *testing.T) {
	brief := briefPicksFixture()
	cards := mobileBriefPatternCards(brief, "English")
	prompt := buildMobileBriefNarrativePrompt(brief, mobileBriefPicks(brief), mobileBriefProfile{}, cards, "English")
	if strings.Count(prompt, pii.ModelOutputInstructions) != 1 {
		t.Fatal("privacy instruction was duplicated or lost")
	}
	if len(pii.ModelOutputInstructions) > 260 {
		t.Fatal("privacy instruction exceeds its compact copy budget")
	}
	if !strings.Contains(prompt, "an account number; a private detail") || !strings.Contains(prompt, "requested language") {
		t.Fatal("prompt lost natural localized wording")
	}
}

func TestDisplayLocaleReadIsBoundedAndDoesNotCacheFailure(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "locale.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &App{store: store}
	if err := store.SetUIState(t.Context(), uiLanguageStateKey, "zh-CN"); err != nil {
		t.Fatal(err)
	}
	// UI-state reads run on the read pool, so a held writer no longer slows
	// them: the locale reads at once.
	{
		revision, err := store.ReadRevision(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		release, err := store.LockReadRevision(t.Context(), revision)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan string, 1)
		go func() { done <- app.modelDisplayLanguage() }()
		select {
		case language := <-done:
			if language != "zh-CN" {
				t.Fatalf("locale with the writer held: %q", language)
			}
		case <-time.After(300 * time.Millisecond):
			release()
			<-done
			t.Fatal("display locale read waited for the writer")
		}
		release()
	}
	// A read that can't answer in time (the rollback switch puts it back on
	// the held writer) falls back and is not cached.
	prevOnWriter := storage.UIStateOnWriter
	storage.UIStateOnWriter = true
	defer func() { storage.UIStateOnWriter = prevOnWriter }()
	revision, err := store.ReadRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.LockReadRevision(t.Context(), revision)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan string, 1)
	go func() { done <- app.modelDisplayLanguage() }()
	select {
	case language := <-done:
		if language != "en" {
			t.Fatalf("unavailable locale fallback: %q", language)
		}
	case <-time.After(300 * time.Millisecond):
		release()
		<-done
		t.Fatal("display locale read exceeded its budget")
	}
	release()
	storage.UIStateOnWriter = prevOnWriter
	if language := app.modelDisplayLanguage(); language != "zh-CN" {
		t.Fatalf("failed locale read was cached: %q", language)
	}
}

func TestSpeechSplitToSynthesisNeverNormalizesRawMasksIntoSpeech(t *testing.T) {
	for _, tc := range []struct{ language, text, want string }{
		{"en", "The contract [REDACTED:account_number]sed out.", "an account number"},
		{"zh-CN", "请核对[REDACTED:account_number]sed后再继续。", "一个账号"},
		{"en", strings.Repeat("context ", 80) + "[REDACTED:account_number]sed out.", "an account number"},
	} {
		t.Run(tc.language+tc.want, func(t *testing.T) {
			app, _ := setupVoiceFixture(t)
			store, err := storage.New(filepath.Join(t.TempDir(), "speech.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.SetUIState(t.Context(), uiLanguageStateKey, tc.language); err != nil {
				t.Fatal(err)
			}
			recorder := &displayGuardSpeechRecorder{}
			app.store = store
			app.ensureTtsState().newEngine = func(string) (speechSynthesizer, error) { return recorder, nil }
			chunks := app.SplitSpeechText(tc.text)
			if len(chunks) == 0 {
				t.Fatal("no speech chunks")
			}
			for _, chunk := range chunks {
				if _, err := app.SynthesizeSpeech(chunk); err != nil {
					t.Fatal(err)
				}
			}
			spoken := strings.Join(recorder.texts, " ")
			if !strings.Contains(spoken, tc.want) || strings.Contains(strings.ToUpper(spoken), "REDACTED") || strings.Contains(strings.ToLower(spoken), "r e d a c t e d") || strings.Contains(spoken, "sed") {
				t.Fatalf("unsafe split-to-speech input: %q", spoken)
			}
		})
	}
}
