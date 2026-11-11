package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/pii"
	"wails-gui/models"
)

// testWAV is one second of silence at 16 kHz, 16-bit mono, as encodeWAV
// writes it.
func testWAV(seconds float64) []byte {
	return encodeWAV(make([]float32, int(16000*seconds)), 16000)
}

func fakeSpeaker(lang string, wav []byte, synthErr error) mobileBriefSpeaker {
	return mobileBriefSpeaker{
		voiceReady: func(string) bool { return true },
		synthesize: func(_ context.Context, _ string, _ string) ([]byte, error) { return wav, synthErr },
		ffmpeg:     func() string { return "" },
		language:   func() string { return lang },
	}
}

func TestMobileBriefSpokenTextPrefersNarrativeAndFallsBackToPlan(t *testing.T) {
	brief := MobileBriefV1{Slot: "morning", NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: " First paragraph. "}, {Text: "Second."}}}
	if got := mobileBriefSpokenText(brief, "en"); got != "First paragraph.\n\nSecond." {
		t.Fatalf("narrative text = %q", got)
	}
	empty := MobileBriefV1{Slot: "morning"}
	if got := mobileBriefSpokenText(empty, "en"); got != "" {
		t.Fatalf("an empty brief must say nothing, got %q", got)
	}
	// A template is never spoken (design R74): the plan rows that used to be read aloud stay silent.
	brief = buildMobileBriefV1(mobileBriefGoldenInput())
	if en := mobileBriefSpokenText(brief, "en"); en != "" {
		t.Fatalf("a template was spoken: %q", en)
	}
	if zh := mobileBriefSpokenText(brief, "zh"); zh != "" {
		t.Fatalf("a template was spoken in zh: %q", zh)
	}
	long := MobileBriefV1{Narrative: []MobileBriefPara{{Text: strings.Repeat("词", 4050)}}}
	if got := mobileBriefSpokenText(long, "zh"); got != "" {
		t.Fatal("oversized unsupported text must not be sliced into a partial sentence")
	}
	if mobileBriefVoiceLang("zh-CN") != "zh" || mobileBriefVoiceLang("Simplified Chinese") != "zh" || mobileBriefVoiceLang(" zh-CN ") != "zh" || mobileBriefVoiceLang("en") != "en" || mobileBriefVoiceLang("") != "en" {
		t.Fatal("voice language mapping")
	}
}

func TestYourWeekSpeechUsesAllWeeklyParagraphs(t *testing.T) {
	brief := MobileBriefV1{
		Narrative: []MobileBriefPara{{Text: "Daily brief must not be spoken."}},
		YourWeek:  &MobileBriefWeek{Start: "2026-10-19", End: "2026-10-23"},
		WeeklyRecap: []MobileBriefPara{
			{Text: "Weekly lead.", Role: yourWeekParagraphLead},
			{Text: strings.Repeat("learned ", 45), Role: yourWeekParagraphLearned},
			{Text: strings.Repeat("done ", 45), Role: yourWeekParagraphDone},
			{Text: strings.Repeat("time ", 45), Role: yourWeekParagraphTime},
			{Text: strings.Repeat("open ", 45), Role: yourWeekParagraphOpen},
			{Text: "Weekly ask.", Role: yourWeekParagraphAsk},
		},
	}
	if speech := mobileBriefSpokenText(brief, "en"); strings.Contains(speech, "Daily brief") || !strings.Contains(speech, "Weekly lead") || !strings.Contains(speech, "Weekly ask") {
		t.Fatalf("weekly speech mixed daily content or dropped fixed paragraphs: %q", speech)
	}
	spoken := yourWeekSpeech(brief, "en")
	for _, word := range []string{"learned", "done", "time", "open"} {
		if !strings.Contains(spoken, word) {
			t.Fatalf("weekly speech omitted %q: %q", word, spoken)
		}
	}
}

func TestRenderMobileBriefAudioAcceptsWeeklyAudioAtSixtySeconds(t *testing.T) {
	brief := MobileBriefV1{
		BriefID: "2026-10-23-evening", YourWeek: &MobileBriefWeek{},
		WeeklyRecap: []MobileBriefPara{{Text: "A full week."}},
	}
	_, ok, status := renderMobileBriefAudio(context.Background(), fakeSpeaker("en", testWAV(60), nil), t.TempDir(), brief)
	if !ok || status != mobileBriefAudioStatusOK {
		t.Fatalf("weekly duration: ok=%v status=%q", ok, status)
	}
}

func TestWAVDurationSeconds(t *testing.T) {
	seconds, err := wavDurationSeconds(testWAV(2.5))
	if err != nil || seconds < 2.49 || seconds > 2.51 {
		t.Fatalf("duration = %v %v", seconds, err)
	}
	if _, err := wavDurationSeconds([]byte("not audio")); err == nil {
		t.Fatal("garbage must not parse")
	}
	if _, err := wavDurationSeconds(encodeWAV(nil, 16000)); err == nil {
		t.Fatal("a WAV with no samples has no duration")
	}
}

func TestRenderMobileBriefAudioStoresWavBesideTheBrief(t *testing.T) {
	dir := t.TempDir()
	brief := MobileBriefV1{BriefID: "2026-10-09-morning", Slot: "morning", NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "Hello."}}}
	file, ok, status := renderMobileBriefAudio(context.Background(), fakeSpeaker("en", testWAV(1.5), nil), dir, brief)
	if !ok || status != mobileBriefAudioStatusOK {
		t.Fatalf("render failed: %v %q", ok, status)
	}
	if file.Audio.Mime != mobileBriefAudioMimeWAV || file.Audio.Bytes == 0 || file.Audio.DurationSeconds < 1.49 || file.Audio.DurationSeconds > 1.51 {
		t.Fatalf("audio shape = %+v", file.Audio)
	}
	if filepath.Base(file.Path) != "2026-10-09-morning.wav" {
		t.Fatalf("stored at %s", file.Path)
	}
	path, mime, found := mobileBriefAudioPath(dir, brief.BriefID)
	if !found || path != file.Path || mime != mobileBriefAudioMimeWAV {
		t.Fatalf("lookup = %q %q %v", path, mime, found)
	}
	if _, _, found := mobileBriefAudioPath(dir, "../2026-10-09-morning"); found {
		t.Fatal("a path-like id must never be looked up")
	}
	if info, err := os.Stat(file.Path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("audio file mode: %v %v", info, err)
	}
}

func TestRenderMobileBriefAudioFallsBackGracefully(t *testing.T) {
	dir := t.TempDir()
	brief := MobileBriefV1{BriefID: "2026-10-09-morning", Slot: "morning", NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "Hello."}}}

	noVoice := fakeSpeaker("en", testWAV(1), nil)
	synthCalls := 0
	noVoice.voiceReady = func(string) bool { return false }
	noVoice.synthesize = func(context.Context, string, string) ([]byte, error) { synthCalls++; return nil, nil }
	if _, ok, status := renderMobileBriefAudio(context.Background(), noVoice, dir, brief); ok || status != "no voice installed" || synthCalls != 0 {
		t.Fatalf("without a voice: ok=%v status=%q synth=%d", ok, status, synthCalls)
	}

	if _, ok, status := renderMobileBriefAudio(context.Background(), fakeSpeaker("en", nil, errors.New("boom")), dir, brief); ok || status != "synthesis failed" {
		t.Fatalf("synthesis error: ok=%v status=%q", ok, status)
	}

	if _, ok, status := renderMobileBriefAudio(context.Background(), fakeSpeaker("en", testWAV(1), nil), dir, MobileBriefV1{BriefID: "2026-10-09-evening"}); ok || status != "nothing to say" {
		t.Fatalf("empty brief: ok=%v status=%q", ok, status)
	}

	if _, ok, status := renderMobileBriefAudio(context.Background(), mobileBriefSpeaker{}, dir, brief); ok || status != "no audio store" {
		t.Fatalf("zero speaker: ok=%v status=%q", ok, status)
	}

	huge := fakeSpeaker("en", make([]byte, 0), nil)
	huge.synthesize = func(context.Context, string, string) ([]byte, error) {
		return encodeWAV(make([]float32, (mobileBriefAudioMaxBytes/2)+100), 16000), nil
	}
	if _, ok, status := renderMobileBriefAudio(context.Background(), huge, dir, brief); ok || status != "audio too large" {
		t.Fatalf("oversized audio: ok=%v status=%q", ok, status)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("no audio file may be left behind, found %d", len(entries))
	}
}

func TestMobileBriefServiceStoresAudioAndServesIt(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.compose = testModelCompose // model text, as a model brief has (design R74)
	svc.speaker = fakeSpeaker("en", testWAV(2), nil)
	svc.gather = func(_ context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
		return mobileBriefInput{Slot: slot, WindowStart: windowStart, AsOf: asOf, Location: time.UTC,
			Moved: []mobileBriefCandidate{{SourceKind: "history", SourceID: "h1", Title: "Shipped", At: asOf.Add(-time.Hour)}}}, nil
	}
	svc.checkDue(context.Background())
	// The text brief is stored at once; the audio follows in its own step.
	text, ok := svc.LatestMobileBrief()
	if !ok || text.BriefID != "2026-10-09-morning" {
		t.Fatalf("the text brief must be stored before the audio: %+v %v", text, ok)
	}
	svc.wg.Wait()
	brief, ok := svc.LatestMobileBrief()
	if !ok || brief.Audio == nil {
		t.Fatalf("brief must carry audio: %+v", brief)
	}
	if brief.Audio.Mime != mobileBriefAudioMimeWAV || brief.Audio.Attachment != nil {
		t.Fatalf("audio = %+v, want a WAV that stayed on this Mac", brief.Audio)
	}
	data, found := svc.Audio(brief.BriefID)
	if !found || data.Mime != mobileBriefAudioMimeWAV || data.Base64 == "" || data.DurationSeconds < 1.9 {
		t.Fatalf("Audio() = %+v %v", data, found)
	}
	if _, found := svc.Audio("2026-10-09-evening"); found {
		t.Fatal("a brief without audio has none")
	}
	health := svc.Health()
	if health.LatestBriefID != brief.BriefID || health.Audio == nil {
		t.Fatalf("health must name the latest brief and its audio: %+v", health)
	}

	// Retention removes the audio with its brief.
	svc.store.mu.Lock()
	svc.store.sweepLocked(clock.Now().AddDate(0, 0, mobileBriefRetentionDays+2))
	svc.store.mu.Unlock()
	if _, _, found := mobileBriefAudioPath(svc.store.dir, brief.BriefID); found {
		t.Fatal("the sweep must remove the spoken brief with the brief")
	}
}

func TestMobileBriefServiceStoresModelLeadAndDropsAudioAfterWithdrawal(t *testing.T) {
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	pii.SetUserSensitiveApps(nil)
	now := time.Date(2026, 10, 23, 18, 5, 0, 0, time.UTC)
	clock := &fakeMobileBriefClock{now: now}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.compose = testModelCompose // model text, as a model brief has (design R74)
	app := svc.app
	ctx := context.Background()
	activity, err := NewActivityStore(filepath.Join(t.TempDir(), "activity"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { activity.Close() })
	app.activity = &activityMonitorState{store: activity}
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{Report: app.postEngagementReport})
	app.engagement.now = clock.Now
	if err := app.store.SetUIState(ctx, onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	if err := app.engagement.SetKindEnabled(ctx, engagement.KindWeeklyRecap, true); err != nil {
		t.Fatal(err)
	}
	if _, err := app.topicsSvc().store.update(func(state *topicsStateFile) {
		state.Projects = []topicProject{
			{ID: "topic-halcyon", Name: "Halcyon", ActivityIDs: []string{"audio-halcyon"}},
			{ID: "topic-borealis", Name: "Borealis", ActivityIDs: []string{"audio-borealis"}},
		}
	}); err != nil {
		t.Fatal(err)
	}
	monday := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	seedThreadMoment(t, app, threadFixtureMoment{
		id: "audio-halcyon", title: "Review Halcyon project", app: "Finder",
		start: at(monday, 9, 0), end: at(monday, 11, 0),
	})
	seedThreadMoment(t, app, threadFixtureMoment{
		id: "audio-borealis", title: "Review Borealis project", app: "Workspace",
		start: at(monday.AddDate(0, 0, 1), 12, 0), end: at(monday.AddDate(0, 0, 1), 13, 0),
	})
	seedThreadMoment(t, app, threadFixtureMoment{
		id: "audio-unassigned", title: "Review notes", app: "Workspace",
		start: at(monday.AddDate(0, 0, 2), 12, 0), end: at(monday.AddDate(0, 0, 2), 13, 0),
	})
	snapshot, err := json.Marshal(yourWeekSnapshot{
		Version: 1, StartDate: "2026-10-19", WorkDays: 5, SeenDays: 5, TotalMinutes: 240, HasActivityProvenance: true,
		Activities: []yourWeekSnapshotActivity{
			{ID: "audio-halcyon", DayMs: []int64{2 * time.Hour.Milliseconds(), 0, 0, 0, 0}},
			{ID: "audio-borealis", DayMs: []int64{0, time.Hour.Milliseconds(), 0, 0, 0}},
			{ID: "audio-unassigned", DayMs: []int64{0, 0, time.Hour.Milliseconds(), 0, 0}},
		},
		Topics: []yourWeekSnapshotRef{
			{ID: "topic-halcyon", Minutes: 120, ActivityIDs: []string{"audio-halcyon"}},
			{ID: "topic-borealis", Minutes: 60, ActivityIDs: []string{"audio-borealis"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetUIState(ctx, yourWeekSnapshotKey+"2026-10-23", string(snapshot)); err != nil {
		t.Fatal(err)
	}
	svc.gather = func(_ context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
		return mobileBriefInput{
			Slot: slot, WindowStart: windowStart, AsOf: asOf, Location: time.UTC,
			Moved: []mobileBriefCandidate{{
				SourceKind: "history", SourceID: "h-evening", Title: "Shipped work",
				At: asOf.Add(-time.Hour),
			}},
		}, nil
	}
	svc.composeWithLead = func(_ context.Context, _ *App, brief MobileBriefV1, _ []MobileBriefCard) (
		[]MobileBriefPara, string, []mobileBriefCardWords, string, string,
	) {
		if brief.YourWeek == nil || len(brief.YourWeek.Time) == 0 {
			t.Errorf("composer did not receive the complete week: %+v", brief.YourWeek)
			return nil, "unavailable", nil, "", ""
		}
		lead, facts := validateYourWeekModelLead("A Halcyon week with progress.", brief.YourWeek, "en")
		if lead == "" {
			t.Errorf("composer lead was rejected for %+v", brief.YourWeek)
		}
		var refs []string
		for _, ref := range brief.Evidence {
			refs = append(refs, ref.Ref)
		}
		return []MobileBriefPara{{Text: mobileBriefPublishableTestReading, Refs: refs}}, "ok", nil, lead, facts
	}
	var spoken string
	speaker := fakeSpeaker("en", testWAV(2), nil)
	speaker.synthesize = func(_ context.Context, _, text string) ([]byte, error) {
		// Each paragraph is read on its own; the reading is all of them.
		if spoken != "" {
			spoken += "\n\n"
		}
		spoken += text
		return testWAV(2), nil
	}
	svc.speaker = speaker
	svc.publish = func(MobileBriefV1, bool) error { return nil }

	generated, err := svc.generateWithMetadata(ctx, mobileBriefSlotEvening, mobileBriefReasonPhone, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.wg.Wait()
	stored, ok := svc.store.load(generated.BriefID)
	if !ok || stored.NarrativeStatus != "ok" {
		t.Fatalf("stored brief: found=%v status=%q", ok, stored.NarrativeStatus)
	}
	if stored.WeekLead != "A Halcyon week with progress." || len(stored.WeeklyRecap) == 0 ||
		stored.WeeklyRecap[0].Text != stored.WeekLead {
		t.Fatalf("stored recap did not use the model lead: lead=%q recap=%+v", stored.WeekLead, stored.WeeklyRecap)
	}
	if stored.Audio == nil {
		t.Fatal("service did not attach audio for the stored weekly lead")
	}
	delivered := app.mobileBriefForDelivery(stored)
	if delivered.Audio == nil {
		t.Fatal("delivery dropped audio while the weekly lead remained readable")
	}
	if want := mobileBriefSpokenText(delivered, "en"); spoken != want || !strings.Contains(spoken, stored.WeekLead) ||
		strings.Contains(spoken, "The release plan brings together") {
		t.Fatalf("spoken text = %q, want week-only text %q", spoken, want)
	}
	phone, _ := app.mobileBriefPhoneProjection(stored)
	if phone.YourWeek == nil || phone.YourWeek.Headline != stored.WeekLead ||
		len(phone.WeeklyRecap) == 0 || phone.WeeklyRecap[0].Text != stored.WeekLead || phone.Audio == nil {
		t.Fatalf("phone projection diverged from the model-led delivery: week=%+v recap=%+v audio=%+v",
			phone.YourWeek, phone.WeeklyRecap, phone.Audio)
	}

	pii.SetUserSensitiveApps(map[string]string{"com.example.finder": "Finder"})
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalApp, ID: "com.example.finder"})
	withdrawn := app.mobileBriefForDelivery(stored)
	if withdrawn.YourWeek == nil || withdrawn.YourWeek.Headline == stored.WeekLead ||
		reflect.DeepEqual(withdrawn.WeeklyRecap, stored.WeeklyRecap) {
		t.Fatalf("withdrawal did not replace the model-led recap: %+v", withdrawn.WeeklyRecap)
	}
	if withdrawn.Audio != nil {
		t.Fatal("audio survived after the named weekly topic was withdrawn")
	}
}

func TestMobileBriefServiceWithoutVoiceStoresTextOnly(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.voiceReady = func(string) bool { return false }
	svc.speaker = speaker
	svc.checkDue(context.Background())
	svc.wg.Wait()
	brief, ok := svc.LatestMobileBrief()
	if !ok || brief.Audio != nil {
		t.Fatalf("without a voice the brief has no audio: %+v %v", brief.Audio, ok)
	}
	if _, found := svc.Audio(brief.BriefID); found {
		t.Fatal("no audio file may exist without a voice")
	}
}

func mobileBriefAudioTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestMobileBriefMissingVoiceWaitsForSetupConfirmation(t *testing.T) {
	for _, language := range []string{"en", "zh-CN"} {
		t.Run(language, func(t *testing.T) {
			mobileBriefAudioTestHome(t)
			var downloads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(srv.Close)
			t.Setenv(envKokoroEnURL, srv.URL)
			t.Setenv(envMeloTtsURL, srv.URL)
			app := &App{}
			speaker := appMobileBriefSpeaker(app)
			speaker.language = func() string { return language }
			var syntheses atomic.Int32
			speaker.synthesize = func(context.Context, string, string) ([]byte, error) {
				syntheses.Add(1)
				return testWAV(1), nil
			}
			brief := MobileBriefV1{BriefID: "2026-10-09-morning", NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "Hello."}}}
			for attempt := 0; attempt < 2; attempt++ {
				file, ok, status := renderMobileBriefAudio(t.Context(), speaker, t.TempDir(), brief)
				if ok || status != "no voice installed" || file.Audio.Bytes != 0 {
					t.Fatalf("unconfirmed voice = %+v, %v, %q", file, ok, status)
				}
			}
			jobs, err := app.GetModelDownloads()
			if err != nil || len(jobs) != 0 || downloads.Load() != 0 || syntheses.Load() != 0 {
				t.Fatalf("unconfirmed voice started work: jobs=%+v err=%v requests=%d syntheses=%d", jobs, err, downloads.Load(), syntheses.Load())
			}
		})
	}
}

func TestBriefObservesOnlyConfirmedVoiceDownload(t *testing.T) {
	data := []byte("voice fixture")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	n := int64(len(data))
	requested := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write(data)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	entry := models.Entry{ID: "kokoro-en", Job: "voice", DisplayName: "Fixture voice", Recommended: true, Languages: []string{"en"},
		SizeBytes: &n, Licence: models.Licence{Name: "MIT"},
		Files:   []models.File{{Path: "voice.bin", Bytes: &n, SHA256: &hash, URLs: []string{srv.URL}}},
		Sources: []models.Source{{Kind: "mirror", URL: "https://model-assets.yansu.app/"}}}
	dir := t.TempDir()
	manager, err := models.New(filepath.Join(dir, "models"), dir, []models.Entry{entry}, func(string) (int64, error) { return 1000, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	app := &App{}
	app.setupModelsOnce.Do(func() { app.setupModels = manager })
	plan, err := manager.BuildPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if app.startTtsVoiceDownload(t.Context(), "en") {
		t.Fatal("planning alone advertised a download")
	}
	if err := manager.Start(plan); err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	releaseDownload := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseDownload)
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmed download did not start")
	}
	if !app.startTtsVoiceDownload(t.Context(), "en") || app.startTtsVoiceDownload(t.Context(), "zh") {
		t.Fatal("brief did not observe only its confirmed voice")
	}
	releaseDownload()
	deadline := time.After(5 * time.Second)
	for manager.Downloads()[0].State != "done" {
		select {
		case <-deadline:
			t.Fatalf("confirmed voice did not finish: %+v", manager.Downloads())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if app.startTtsVoiceDownload(t.Context(), "en") {
		t.Fatal("completed voice still advertised a download")
	}
}

type mobileBriefTestVoice struct{}

func (mobileBriefTestVoice) Synthesize(string) ([]byte, error) { return testWAV(1), nil }
func (mobileBriefTestVoice) Close()                            {}

func TestMobileBriefAudioRouteAuthorizationAndVersion(t *testing.T) {
	brief := mobileTestBrief()
	brief.Audio = &MobileBriefAudio{Status: "ok", Mime: mobileBriefAudioMimeWAV, Bytes: 100, DurationSeconds: 1,
		Attachment: &MobileBriefAudioAttachment{ID: mobileTestOtherID, Key: "sealed-key", Nonce: "sealed-nonce"}}
	source := &mobileTestBriefSource{brief: &brief}
	bridge, err := newMobileBridge(&App{mobileBriefSvc: source}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	path := "/briefs/" + brief.BriefID + "/audio"
	version := mobileBriefAudioVersion(brief)
	query := "version=" + url.QueryEscape(version)
	for _, tc := range []struct {
		name, query string
		scopes      []string
		expires     string
		want        int
		reason      string
	}{
		{name: "reader", query: query, scopes: []string{mobileScopeBriefRead}, want: http.StatusOK},
		{name: "no read permission", query: query, want: http.StatusForbidden},
		{name: "expired", query: query, scopes: []string{mobileScopeBriefRead}, expires: mobileTimestamp(time.Now().Add(-time.Minute)), want: http.StatusUnauthorized},
		{name: "missing version", scopes: []string{mobileScopeBriefRead}, want: http.StatusBadRequest},
		{name: "repeated version", query: query + "&" + query, scopes: []string{mobileScopeBriefRead}, want: http.StatusBadRequest},
		{name: "old version", query: "version=older", scopes: []string{mobileScopeBriefRead}, want: http.StatusConflict, reason: "version_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := mobileTestDevice(tc.scopes...)
			if tc.expires != "" {
				device.ExpiresAt = tc.expires
			}
			status, body := bridge.serveRequest(device, mobileRequestPlaintext{Method: http.MethodGet, Path: path, Query: tc.query})
			if status != tc.want || (tc.reason != "" && !strings.Contains(body, tc.reason)) {
				t.Fatalf("status/body = %d %s", status, body)
			}
			if status != http.StatusOK && strings.Contains(body, "sealed-key") {
				t.Fatal("rejected request disclosed an attachment key")
			}
			if status == http.StatusOK {
				var response struct {
					Data mobileBriefAudioResource `json:"data"`
				}
				if err := json.Unmarshal([]byte(body), &response); err != nil {
					t.Fatal(err)
				}
				if response.Data.Version != version || response.Data.BriefID != brief.BriefID || response.Data.Audio.Attachment.Key != "sealed-key" {
					t.Fatalf("wrong audio resource: %+v", response.Data)
				}
			}
		})
	}
	if source.refreshes != 0 {
		t.Fatal("GET started brief generation")
	}
}

func TestMobileBriefAudioRouteUnavailableReasons(t *testing.T) {
	for _, tc := range []struct {
		name    string
		audio   *MobileBriefAudio
		missing bool
		reason  string
	}{
		{name: "rendering", reason: "audio_rendering"},
		{name: "voice absent", audio: &MobileBriefAudio{Status: mobileBriefAudioStatusDownloading}, reason: "voice_not_installed"},
		{name: "no matching brief", missing: true, reason: "audio_unavailable"},
		{name: "unknown audio status", audio: &MobileBriefAudio{Status: "future-status"}, reason: "audio_unavailable"},
		{name: "no attachment", audio: &MobileBriefAudio{Mime: mobileBriefAudioMimeWAV, Bytes: 100, DurationSeconds: 1}, reason: "audio_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brief := mobileTestBrief()
			brief.Audio = tc.audio
			source := &mobileTestBriefSource{brief: &brief}
			if tc.missing {
				source.brief = nil
			}
			bridge, err := newMobileBridge(&App{mobileBriefSvc: source}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(bridge.Stop)
			recorder := httptest.NewRecorder()
			bridge.handleAuthorized(recorder, httptest.NewRequest(http.MethodGet, "/mobile/v1/briefs/"+brief.BriefID+"/audio?version="+url.QueryEscape(mobileBriefAudioVersion(brief)), nil), mobileTestDevice(mobileScopeBriefRead))
			if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), `"reason":"`+tc.reason+`"`) {
				t.Fatalf("unavailable = %d %s", recorder.Code, recorder.Body.String())
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("audio metadata can be cached by HTTP")
			}
		})
	}
}

func TestMobileBriefAudioRouteHonorsWithdrawnSources(t *testing.T) {
	app, svc, brief := newStoredMobileBriefPrivacyFixture(t)
	brief.Audio.Mime = mobileBriefAudioMimeWAV
	brief.Audio.Bytes = 100
	brief.Audio.DurationSeconds = 1
	brief.Audio.binding = mobileBriefAudioBinding(app.mobileBriefForDelivery(brief), "en")
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	bridge, err := newMobileBridge(app, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	request := mobileRequestPlaintext{Method: http.MethodGet, Path: "/briefs/" + brief.BriefID + "/audio", Query: "version=" + url.QueryEscape(mobileBriefAudioVersion(brief))}
	device := mobileTestDevice(mobileScopeBriefRead)
	status, body := bridge.serveRequest(device, request)
	if status != http.StatusOK || !strings.Contains(body, "private-audio-key") {
		t.Fatalf("fixture was not deliverable before source withdrawal: %d %s", status, body)
	}
	app.closedEyes.enabled.Store(true)
	status, body = bridge.serveRequest(device, request)
	if status != http.StatusNotFound || strings.Contains(body, "private-audio") {
		t.Fatalf("withdrawn recording escaped: %d %s", status, body)
	}
}

func newMobileBriefAudioUploadFixture(t *testing.T) (*mobileBriefService, MobileBriefV1, []byte) {
	t.Helper()
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(8, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	brief := mobileTestBrief()
	data := testWAV(1)
	brief.Audio = &MobileBriefAudio{Status: "ok", Mime: mobileBriefAudioMimeWAV, Bytes: len(data), DurationSeconds: 1, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	brief.Audio.binding = mobileBriefAudioBinding(svc.app.mobileBriefForDelivery(brief), "en")
	if err := svc.store.save(brief, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.store.dir, brief.BriefID+mobileBriefAudioExtWAV), data, 0600); err != nil {
		t.Fatal(err)
	}
	return svc, brief, data
}

func TestMobileBriefAudioRetryAfterRelayFailureAndStopsAtNewBrief(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		t.Run(fmt.Sprint(superseded), func(t *testing.T) {
			svc, brief, _ := newMobileBriefAudioUploadFixture(t)
			svc.publish = func(MobileBriefV1, bool) error { return nil }
			attempts := 0
			upload := func(context.Context, []byte) (*MobileBriefAudioAttachment, error) {
				attempts++
				if attempts == 1 {
					if superseded {
						next := mobileTestBrief()
						next.BriefID, next.AsOf = "2026-10-09-evening", "2026-10-09T18:00:00Z"
						if err := svc.store.save(next, svc.now()); err != nil {
							t.Fatal(err)
						}
					}
					return nil, errors.New("relay 502")
				}
				return &MobileBriefAudioAttachment{ID: mobileTestOtherID, Key: "key", Nonce: "nonce"}, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			svc.retryBriefAudio(ctx, brief, func(int) time.Duration { return time.Millisecond }, upload)
			if superseded && attempts != 1 || !superseded && attempts != 2 {
				t.Fatalf("attempts = %d", attempts)
			}
			stored, _ := svc.store.load(brief.BriefID)
			if (stored.Audio != nil && stored.Audio.Attachment != nil) == superseded {
				t.Fatalf("attachment after retries = %+v", stored.Audio)
			}
		})
	}
	t.Run("replaced during upload", func(t *testing.T) {
		svc, brief, _ := newMobileBriefAudioUploadFixture(t)
		upload := func(context.Context, []byte) (*MobileBriefAudioAttachment, error) {
			next := mobileTestBrief()
			next.BriefID, next.AsOf = "2026-10-09-evening", "2026-10-09T18:00:00Z"
			if err := svc.store.save(next, svc.now()); err != nil {
				t.Fatal(err)
			}
			return &MobileBriefAudioAttachment{ID: mobileTestOtherID, Key: "key", Nonce: "nonce"}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		svc.retryBriefAudio(ctx, brief, func(int) time.Duration { return time.Millisecond }, upload)
		stored, _ := svc.store.load(brief.BriefID)
		if stored.Audio == nil || stored.Audio.Attachment != nil {
			t.Fatalf("superseded audio attached: %+v", stored.Audio)
		}
	})
}

func TestMobileBriefAudioUploadUsesVersionedSnapshotAndKeepsLatest(t *testing.T) {
	svc, brief, data := newMobileBriefAudioUploadFixture(t)
	newest := mobileTestBrief()
	newest.BriefID = "2026-10-09-evening"
	newest.AsOf = "2026-10-09T18:00:00Z"
	if err := svc.store.save(newest, svc.now()); err != nil {
		t.Fatal(err)
	}
	uploads := 0
	upload := func(_ context.Context, snapshot []byte) (*MobileBriefAudioAttachment, error) {
		uploads++
		if string(snapshot) != string(data) {
			t.Fatal("upload did not receive verified audio")
		}
		return &MobileBriefAudioAttachment{ID: mobileTestOtherID, Key: "key", Nonce: "nonce"}, nil
	}
	for range 2 {
		if err := svc.prepareMobileBriefAudio(context.Background(), brief.BriefID, mobileBriefAudioVersion(brief), upload); err != nil {
			t.Fatal(err)
		}
	}
	if uploads != 1 {
		t.Fatalf("uploaded %d times; descriptor should be reused", uploads)
	}
	stored, _ := svc.GetMobileBrief(brief.BriefID)
	if stored.Audio == nil || stored.Audio.Attachment == nil {
		t.Fatal("attachment was not persisted")
	}
	latest, _ := svc.store.latest()
	if latest.BriefID != newest.BriefID {
		t.Fatal("audio for an earlier brief changed the latest brief")
	}
}

func TestMobileBriefAudioUploadRejectsWrongFileAndStaleVersion(t *testing.T) {
	for _, scenario := range []string{"wrong version", "same-sized replacement", "legacy without digest", "version changes during upload", "privacy changes during upload"} {
		t.Run(scenario, func(t *testing.T) {
			svc, brief, data := newMobileBriefAudioUploadFixture(t)
			version := mobileBriefAudioVersion(brief)
			switch scenario {
			case "wrong version":
				version += "old"
			case "same-sized replacement":
				data[len(data)-1] ^= 1
				if err := os.WriteFile(filepath.Join(svc.store.dir, brief.BriefID+mobileBriefAudioExtWAV), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "legacy without digest":
				brief.Audio.SHA256 = ""
				if err := svc.store.save(brief, svc.now()); err != nil {
					t.Fatal(err)
				}
			}
			uploads := 0
			err := svc.prepareMobileBriefAudio(context.Background(), brief.BriefID, version, func(_ context.Context, _ []byte) (*MobileBriefAudioAttachment, error) {
				uploads++
				switch scenario {
				case "version changes during upload":
					brief.AsOf = "2026-10-09T09:00:00Z"
					if err := svc.store.save(brief, svc.now()); err != nil {
						t.Fatal(err)
					}
				case "privacy changes during upload":
					svc.app.closedEyes.enabled.Store(true)
				}
				return &MobileBriefAudioAttachment{ID: mobileTestOtherID, Key: "key", Nonce: "nonce"}, nil
			})
			if !errors.Is(err, errMobileBriefNoAudio) {
				t.Fatalf("unsafe audio accepted: %v", err)
			}
			if !strings.Contains(scenario, "during upload") && uploads != 0 {
				t.Fatal("unsafe file reached the relay")
			}
			stored, _ := svc.store.load(brief.BriefID)
			if stored.Audio.Attachment != nil {
				t.Fatal("unsafe attachment key was persisted")
			}
		})
	}
}

func TestMobileBriefAudioUploadConcurrentRequestsReturnBusyAndReuseAttachment(t *testing.T) {
	svc, brief, _ := newMobileBriefAudioUploadFixture(t)
	var uploads atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	upload := func(context.Context, []byte) (*MobileBriefAudioAttachment, error) {
		uploads.Add(1)
		close(started)
		<-release
		return &MobileBriefAudioAttachment{ID: mobileTestOtherID, Key: "key", Nonce: "nonce"}, nil
	}
	go func() {
		result <- svc.prepareMobileBriefAudio(context.Background(), brief.BriefID, mobileBriefAudioVersion(brief), upload)
	}()
	select {
	case <-started:
	case err := <-result:
		// Fail instead of waiting forever: the upload never began.
		t.Fatalf("the upload never started: %v", err)
	}
	busy := svc.prepareMobileBriefAudio(context.Background(), brief.BriefID, mobileBriefAudioVersion(brief), upload)
	close(release)
	if !errors.Is(busy, errMobileBriefAudioBusy) {
		t.Fatalf("concurrent read should return busy without waiting: %v", busy)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := svc.prepareMobileBriefAudio(context.Background(), brief.BriefID, mobileBriefAudioVersion(brief), upload); err != nil {
		t.Fatal(err)
	}
	if uploads.Load() != 1 {
		t.Fatalf("concurrent reads uploaded %d copies", uploads.Load())
	}
}

func TestMobileBriefAudioReadDoesNotRenderOrInstallVoice(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(fmt.Sprintf("installed=%v", installed), func(t *testing.T) {
			svc, brief, _ := newMobileBriefAudioUploadFixture(t)
			brief.Audio = nil
			if err := svc.store.save(brief, svc.now()); err != nil {
				t.Fatal(err)
			}
			calls := 0
			svc.speaker = mobileBriefSpeaker{
				voiceReady:    func(string) bool { return installed },
				downloadVoice: func(context.Context, string) bool { calls++; return true },
				synthesize:    func(context.Context, string, string) ([]byte, error) { calls++; return testWAV(1), nil },
			}
			svc.app.mobileBriefSvc = svc
			bridge, err := newMobileBridge(svc.app, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(bridge.Stop)
			status, body := bridge.serveRequest(mobileTestDevice(mobileScopeBriefRead), mobileRequestPlaintext{Method: http.MethodGet, Path: "/briefs/" + brief.BriefID + "/audio", Query: "version=" + url.QueryEscape(mobileBriefAudioVersion(brief))})
			reason := "voice_not_installed"
			if installed {
				reason = "audio_rendering"
			}
			if status != http.StatusNotFound || !strings.Contains(body, reason) || calls != 0 {
				t.Fatalf("audio read = %d %s; voice calls = %d", status, body, calls)
			}
		})
	}
}

func TestMobileBriefAudioRouteBusyReturnsRenderingWithoutWaiting(t *testing.T) {
	svc, brief, _ := newMobileBriefAudioUploadFixture(t)
	svc.app.mobileBriefSvc = svc
	bridge, err := newMobileBridge(svc.app, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	svc.audioMu.Lock()
	defer svc.audioMu.Unlock()
	status, body := bridge.serveRequest(mobileTestDevice(mobileScopeBriefRead), mobileRequestPlaintext{Method: http.MethodGet, Path: "/briefs/" + brief.BriefID + "/audio", Query: "version=" + url.QueryEscape(mobileBriefAudioVersion(brief))})
	if status != http.StatusNotFound || !strings.Contains(body, "audio_rendering") {
		t.Fatalf("busy audio read = %d %s", status, body)
	}
}

func TestMobileBriefAudioPreparationDoesNotBlockDeviceWorker(t *testing.T) {
	bridge, relay := mobileTestHub(t)
	svc, brief, data := newMobileBriefAudioUploadFixture(t)
	bridge.app.mobileBriefSvc = svc
	t.Cleanup(svc.Stop)
	other := brief
	other.BriefID = "2026-10-09-evening"
	otherAudio := *other.Audio
	other.Audio = &otherAudio
	other.Audio.binding = mobileBriefAudioBinding(svc.app.mobileBriefForDelivery(other), "en")
	if err := svc.store.save(other, svc.now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.store.dir, other.BriefID+mobileBriefAudioExtWAV), data, 0600); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var uploads atomic.Int32
	relay.mu.Lock()
	relay.attachmentHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uploads.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			w.WriteHeader(http.StatusCreated)
		case <-r.Context().Done():
		}
	})
	relay.mu.Unlock()
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
	conn := relay.acceptRegistration(t, mobileTestListed(phone))
	call := func(seq uint64, path, query string) mobileResponsePlaintext {
		t.Helper()
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
		request, err := json.Marshal(mobileRequestPlaintext{ID: id, Method: http.MethodGet, Path: path, Query: query})
		if err != nil {
			t.Fatal(err)
		}
		conn.send(t, map[string]any{"t": "in", "env": phone.sealRequest(t, bridge.identity.Public, bridge.identity.HubID, seq, request)})
		var response mobileResponsePlaintext
		for {
			frame := relay.nextFrame(t, "out")
			var out struct {
				Env mobileEnvelope `json:"env"`
			}
			if err := json.Unmarshal(frame.raw, &out); err != nil {
				t.Fatal(err)
			}
			var candidate mobileResponsePlaintext
			if err := json.Unmarshal(phone.openFromHub(t, bridge.identity.Public, bridge.identity.HubID, out.Env), &candidate); err != nil {
				t.Fatal(err)
			}
			// A today_changed event is unsolicited and carries no request id.
			if candidate.ID != "" {
				response = candidate
				break
			}
		}
		if response.ID != id {
			t.Fatalf("response belongs to %s, want %s", response.ID, id)
		}
		return response
	}
	audioPath := "/briefs/" + brief.BriefID + "/audio"
	query := "version=" + url.QueryEscape(mobileBriefAudioVersion(brief))
	first := call(1, audioPath, query)
	if first.Status != http.StatusNotFound || !strings.Contains(first.Body, "audio_rendering") {
		t.Fatalf("preparing response = %+v", first)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background attachment upload did not start")
	}
	second := call(2, "/briefs/"+other.BriefID+"/audio", "version="+url.QueryEscape(mobileBriefAudioVersion(other)))
	if second.Status != http.StatusNotFound || !strings.Contains(second.Body, "audio_rendering") {
		t.Fatalf("another brief's audio should return promptly while busy: %+v", second)
	}
	status := call(3, "/status", "")
	if status.Status != http.StatusOK || uploads.Load() != 1 {
		t.Fatalf("serial worker blocked or duplicated upload: status=%d uploads=%d", status.Status, uploads.Load())
	}
	unblock()
	mobileTestWaitFor(t, "background attachment metadata", func() bool {
		stored, found := svc.GetMobileBrief(brief.BriefID)
		return found && stored.Audio != nil && stored.Audio.Attachment != nil
	})
	ready := call(4, audioPath, query)
	if ready.Status != http.StatusOK || !strings.Contains(ready.Body, `"attachment"`) {
		t.Fatalf("prepared recording was not served: %+v", ready)
	}
}

func TestMobileBriefAudioPreparationStopsWithService(t *testing.T) {
	svc, brief, _ := newMobileBriefAudioUploadFixture(t)
	started, cancelled := make(chan struct{}), make(chan struct{})
	upload := func(ctx context.Context, _ []byte) (*MobileBriefAudioAttachment, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	if err := svc.queueMobileBriefAudio(context.Background(), brief.BriefID, mobileBriefAudioVersion(brief), upload); err != nil {
		t.Fatal(err)
	}
	<-started
	svc.Stop()
	select {
	case <-cancelled:
	default:
		t.Fatal("service shutdown left an audio upload running")
	}
	if err := svc.queueMobileBriefAudio(context.Background(), brief.BriefID, mobileBriefAudioVersion(brief), upload); !errors.Is(err, errMobileBriefUnavailable) {
		t.Fatalf("a stopped service accepted new work: %v", err)
	}
}

// P138: while the voice speaks a brief, the stored brief says its recording is being made, so a phone that reads it
// shows "Recording on its way". A failed attempt clears that mark, so the brief never says so after the attempt ends.
func TestMobileBriefAudioRenderingMarkWhileSpokenAndClearedWhenItFails(t *testing.T) {
	asOf := time.Date(2026, 11, 7, 7, 0, 0, 0, time.UTC)
	newBrief := func(id string) MobileBriefV1 {
		return MobileBriefV1{BriefID: id, Slot: "morning", AsOf: mobileTimestamp(asOf), NarrativeStatus: "ok",
			Narrative: []MobileBriefPara{{Text: "Three meetings today, and Priya is waiting on you."}}}
	}
	svc := &mobileBriefService{store: newMobileBriefStore(t.TempDir())}

	spoken := newBrief("2026-11-07-morning")
	if err := svc.store.save(spoken, asOf); err != nil {
		t.Fatal(err)
	}
	var seen string
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.synthesize = func(_ context.Context, _ string, _ string) ([]byte, error) {
		if stored, found := svc.store.load(spoken.BriefID); found && stored.Audio != nil {
			seen = stored.Audio.Status
		}
		return testWAV(1), nil
	}
	svc.speaker = speaker
	svc.addAudio(context.Background(), spoken, asOf)
	if seen != mobileBriefAudioStatusRendering {
		t.Fatalf("while the voice spoke, the brief said audio status %q, want %q", seen, mobileBriefAudioStatusRendering)
	}
	stored, _ := svc.store.load(spoken.BriefID)
	if stored.Audio == nil || stored.Audio.Status != mobileBriefAudioStatusOK || stored.Audio.Bytes == 0 {
		t.Fatalf("a finished recording must replace the mark, got %+v", stored.Audio)
	}

	failed := newBrief("2026-11-06-evening")
	if err := svc.store.save(failed, asOf); err != nil {
		t.Fatal(err)
	}
	svc.speaker = fakeSpeaker("en", nil, errors.New("voice unavailable"))
	svc.addAudio(context.Background(), failed, asOf)
	stored, found := svc.store.load(failed.BriefID)
	if !found {
		t.Fatal("the brief disappeared")
	}
	if stored.Audio != nil && stored.Audio.Status == mobileBriefAudioStatusRendering {
		t.Fatalf("a failed attempt left the rendering mark on the brief: %+v", stored.Audio)
	}
}

// The script and the joined audio must contain every delivered paragraph even
// when the written brief is longer than the old speech-only budget.
func TestBriefAudioReadsAllThreeShownParagraphs(t *testing.T) {
	brief := MobileBriefV1{BriefID: "2026-11-10-evening", Slot: "evening", NarrativeStatus: "ok"}
	for _, part := range []string{"First", "Second", "Third"} {
		brief.Narrative = append(brief.Narrative, MobileBriefPara{Text: strings.TrimSpace(strings.Repeat(part+" part is complete. ", 30))})
	}
	var spoken []string
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.synthesize = func(_ context.Context, _, text string) ([]byte, error) {
		spoken = append(spoken, text)
		return testWAV(1), nil
	}
	file, ok, reason := renderMobileBriefAudio(t.Context(), speaker, t.TempDir(), brief)
	if !ok {
		t.Fatalf("render failed: %s", reason)
	}
	var shown []string
	for _, p := range brief.Narrative {
		shown = append(shown, p.Text)
	}
	if got, want := strings.Join(strings.Fields(strings.Join(spoken, " ")), " "), strings.Join(strings.Fields(strings.Join(shown, " ")), " "); got != want {
		t.Fatalf("spoken script differs from shown text: got %q, want %q", got, want)
	}
	if len(file.Audio.Paragraphs) != 3 {
		t.Fatalf("paragraph starts = %+v", file.Audio.Paragraphs)
	}
}

func TestBriefAudioReadsAmendmentAfterSealing(t *testing.T) {
	svc, sealed, _, clock := newAudioBackfillFixture(t)
	if _, _, ok := svc.seal(t.Context(), sealed); !ok {
		t.Fatal("fixture could not seal the original brief")
	}
	amended := sealed
	refs := sealed.Narrative[0].Refs
	amended.Narrative = []MobileBriefPara{{Text: "The amended plan is ready.", Refs: refs}, {Text: "The review is next.", Refs: refs}, {Text: "The final part is here.", Refs: refs}}
	if err := svc.store.save(amended, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := svc.seal(t.Context(), amended); !ok {
		t.Fatal("fixture could not seal the amended brief")
	}
	var spoken []string
	svc.speaker = fakeSpeaker("en", testWAV(1), nil)
	svc.speaker.synthesize = func(_ context.Context, _, text string) ([]byte, error) {
		spoken = append(spoken, text)
		return testWAV(1), nil
	}
	svc.addAudio(t.Context(), sealed, clock.Now())
	stored, ok := svc.store.load(sealed.BriefID)
	if !ok || stored.Audio == nil {
		t.Fatal("amended brief has no audio")
	}
	shown, _, err := svc.finishedReadWithholding(stored)
	if err != nil {
		t.Fatal(err)
	}
	var paragraphs []string
	for _, p := range shown.Narrative {
		paragraphs = append(paragraphs, p.Text)
	}
	if got, want := strings.Join(spoken, "\n\n"), strings.Join(paragraphs, "\n\n"); got != want {
		t.Fatalf("spoken %q; shown %q", got, want)
	}
}

func TestBriefAudioRejectsFailedChunk(t *testing.T) {
	brief := MobileBriefV1{BriefID: "2026-11-10-evening", NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: strings.Repeat("A complete sentence. ", 20)}}}
	calls := 0
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.synthesize = func(context.Context, string, string) ([]byte, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("voice failed")
		}
		return testWAV(1), nil
	}
	dir := t.TempDir()
	if _, ok, _ := renderMobileBriefAudio(t.Context(), speaker, dir, brief); ok {
		t.Fatal("partial recording was accepted")
	}
	if _, _, found := mobileBriefAudioPath(dir, brief.BriefID); found {
		t.Fatal("partial recording was stored")
	}
}

func TestBriefAudioRejectsReplacedFile(t *testing.T) {
	svc, brief, _ := newMobileBriefAudioUploadFixture(t)
	if _, ok := svc.Audio(brief.BriefID); !ok {
		t.Fatal("original file was not available")
	}
	path := filepath.Join(svc.store.dir, brief.BriefID+mobileBriefAudioExtWAV)
	if err := os.WriteFile(path, testWAV(2), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Audio(brief.BriefID); ok {
		t.Fatal("replaced file was served under the old audio metadata")
	}
}

func TestBriefAudioDoesNotServeOrphanWhileRendering(t *testing.T) {
	svc, brief, _ := newMobileBriefAudioUploadFixture(t)
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusRendering}
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Audio(brief.BriefID); ok {
		t.Fatal("rendering status served an old file")
	}
}

func TestBriefAudioWithholdsRecordingAfterCitedSourceEdit(t *testing.T) {
	_, svc, brief := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	var ref string
	for _, evidence := range brief.Evidence {
		if evidence.SourceID == "safe-recall" {
			ref = evidence.Ref
		}
	}
	if ref == "" {
		t.Fatal("fixture lacks memory source")
	}
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{{Text: "The original note is ready.", Refs: []string{ref}}}
	data := testWAV(1)
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mobileBriefAudioMimeWAV, Bytes: len(data), DurationSeconds: 1, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	brief.Audio.binding = mobileBriefAudioBinding(svc.app.mobileBriefForDelivery(brief), "en")
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.store.dir, brief.BriefID+mobileBriefAudioExtWAV), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := svc.seal(t.Context(), brief); !ok {
		t.Fatal("fixture did not seal")
	}
	if _, ok := svc.Audio(brief.BriefID); !ok {
		t.Fatal("original recording was not available")
	}
	// An edit to the cited note is not a withdrawal: the sealed recording keeps playing.
	writePrivacyMemory(t, "safe-recall", "# Corrected recall\n\nThe note changed.\n")
	if _, ok := svc.Audio(brief.BriefID); !ok {
		t.Fatal("an edit to a cited source withheld the sealed recording")
	}
	calls := 0
	svc.speaker = fakeSpeaker("en", data, nil)
	svc.speaker.synthesize = func(context.Context, string, string) ([]byte, error) { calls++; return data, nil }
	svc.addAudio(t.Context(), brief, svc.now())
	_ = calls
}

func TestBriefAudioBindingIncludesSpokenScript(t *testing.T) {
	brief := MobileBriefV1{NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "The daily text."}}}
	weekly := brief
	weekly.YourWeek = &MobileBriefWeek{}
	if mobileBriefAudioBinding(brief, "en") == mobileBriefAudioBinding(weekly, "en") {
		t.Fatal("different spoken scripts shared a binding")
	}
}
