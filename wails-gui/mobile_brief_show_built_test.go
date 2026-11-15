package main

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// builtBriefFixture is a stored brief with written words and a recording, and
// its build's own copy (.built), as the writer leaves it.
func builtBriefFixture(t *testing.T) (*App, *mobileBriefService, MobileBriefV1) {
	t.Helper()
	app, svc, brief := newStoredMobileBriefPrivacyFixture(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	refs := map[string]string{}
	for _, ref := range brief.Evidence {
		refs[ref.SourceID] = ref.Ref
	}
	brief.Narrative = []MobileBriefPara{
		{Text: "The first written paragraph.", Refs: []string{refs["safe-recall"]}},
		{Text: "The second written paragraph.", Refs: []string{refs["protected-knowledge"]}},
	}
	brief.NarrativeStatus = "ok"
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mobileBriefAudioMimeWAV, Bytes: 4, SHA256: "built-recording"}
	if err := svc.store.saveBuilt(brief); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	svc.wg.Wait()
	return app, svc, brief
}

func stripMobileBriefWords(brief MobileBriefV1) MobileBriefV1 {
	brief.Narrative, brief.NarrativeStatus, brief.Audio, brief.Offers, brief.NarrativeParts = nil, "unavailable", nil, nil, nil
	return brief
}

// assertBuiltBriefShows checks every surface that reads the brief: Today's
// current brief, the shared read, and the phone's sealed read.
func assertBuiltBriefShows(t *testing.T, app *App, svc *mobileBriefService, built MobileBriefV1, step string) {
	t.Helper()
	check := func(surface string, got MobileBriefV1) {
		t.Helper()
		if got.BriefID != built.BriefID || got.NarrativeStatus != "ok" || !mobileBriefSameNarrativeText(got, built) {
			t.Fatalf("%s: %s shows brief=%q status=%q paragraphs=%d, want the built words", step, surface, got.BriefID, got.NarrativeStatus, len(got.Narrative))
		}
		if got.Audio == nil || got.Audio.SHA256 != built.Audio.SHA256 {
			t.Fatalf("%s: %s lost the built recording", step, surface)
		}
		evidence := map[string]bool{}
		for _, ref := range got.Evidence {
			evidence[ref.Ref] = true
		}
		for _, para := range got.Narrative {
			for _, ref := range para.Refs {
				if !evidence[ref] {
					t.Fatalf("%s: %s cites %s with no evidence", step, surface, ref)
				}
			}
		}
	}
	stored, _ := svc.store.load(built.BriefID)
	read, _, err := svc.finishedReadWithholding(stored)
	if err != nil {
		t.Fatalf("%s: read failed: %v", step, err)
	}
	check("the shared read", read)
	current, ok := svc.CurrentMobileBrief()
	if !ok {
		t.Fatalf("%s: Today has no current brief", step)
	}
	check("Today", current)
	phone, ok := app.mobileBriefSealedRead(stored)
	if !ok {
		t.Fatalf("%s: the phone could not read the brief", step)
	}
	check("the phone", phone)
}

// Bo, 2026-11-15: once built, the latest brief shows with its words and its
// recording; nothing after the build withholds it. A seeded random run of what
// happens after a build: live refreshes, a pass that saves the brief without
// its words, a blank seal, an unreadable sealed copy, Closed Eyes, a privacy
// change, and an explicit withdrawal of a source the words do not cite.
func TestBuiltBriefAlwaysShowsWithItsWordsAndRecording(t *testing.T) {
	app, svc, built := builtBriefFixture(t)
	assertBuiltBriefShows(t, app, svc, built, "after the build")
	steps := []struct {
		name string
		run  func()
	}{
		{"a live Today refresh", func() { svc.refreshToday(false) }},
		{"a live-only refresh", func() { svc.refreshToday(true) }},
		{"a pass saves the brief without its words", func() {
			stored, _ := svc.store.load(built.BriefID)
			_ = svc.store.save(stripMobileBriefWords(stored), svc.now())
			svc.wg.Wait()
		}},
		{"a pass seals the brief without its words", func() {
			stored, _ := svc.store.load(built.BriefID)
			svc.seal(t.Context(), stripMobileBriefWords(stored))
		}},
		{"the sealed copy cannot be read", func() {
			if path, ok := svc.store.finishedReadPath(built.BriefID); ok {
				_ = os.WriteFile(path, []byte("{not json"), 0o600)
			}
		}},
		{"Closed Eyes turns on", func() { _ = app.SetClosedEyesModeEnabled(true) }},
		{"Closed Eyes turns off", func() { _ = app.SetClosedEyesModeEnabled(false) }},
		{"a privacy change", func() { briefPrivacyChanged() }},
		{"an uncited source is withdrawn", func() { briefPrivacyChanged(briefWithdrawn{Kind: "memory", ID: "protected-recall"}) }},
	}
	random := rand.New(rand.NewSource(20261115))
	for i := range 30 {
		step := steps[random.Intn(len(steps))]
		step.run()
		svc.todayRefreshWG.Wait()
		svc.wg.Wait()
		assertBuiltBriefShows(t, app, svc, built, step.name)
		if i == 0 {
			t.Logf("first step: %s", step.name)
		}
	}
	_ = app.SetClosedEyesModeEnabled(false)
}

// The only change after the build is the person's own: withdrawing a source
// the words cite leaves out the paragraph that cites it, and the rest of the
// brief shows. The recording, which speaks the withdrawn paragraph, is not
// played.
func TestBuiltBriefExplicitWithdrawalLeavesOutOnlyItsParagraph(t *testing.T) {
	_, svc, built := builtBriefFixture(t)
	stored, _ := svc.store.load(built.BriefID)
	svc.seal(t.Context(), stripMobileBriefWords(stored))
	briefPrivacyChanged(briefWithdrawn{Kind: "memory", ID: "safe-recall"})
	svc.wg.Wait()
	stored, _ = svc.store.load(built.BriefID)
	read, _, err := svc.finishedReadWithholding(stored)
	if err != nil {
		t.Fatal(err)
	}
	if read.NarrativeStatus != "ok" || len(read.Narrative) != 1 || read.Narrative[0].Text != "The second written paragraph." {
		t.Fatalf("withdrawal: status=%q paragraphs=%d, want only the uncited paragraph", read.NarrativeStatus, len(read.Narrative))
	}
	if read.Audio != nil {
		t.Fatal("the recording of a withdrawn paragraph is still offered")
	}
}

// The 2026-11-15 repair: a brief whose stored copy and sealed copy lost their
// words to the live refresh shows its build's words and recording again on
// the next read, without a rebuild, and the play button has a recording.
func TestStrippedBriefIsShownFromItsBuild(t *testing.T) {
	_, svc, built := builtBriefFixture(t)
	stripped := stripMobileBriefWords(built)
	if err := svc.store.save(stripped, svc.now()); err != nil {
		t.Fatal(err)
	}
	svc.wg.Wait()
	svc.seal(t.Context(), stripped)
	audio := filepath.Join(svc.store.dir, built.BriefID+".wav")
	if err := os.WriteFile(audio, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, ok := svc.CurrentMobileBrief()
	if !ok || current.BriefID != built.BriefID || !mobileBriefSameNarrativeText(current, built) || current.Audio == nil {
		t.Fatalf("the stripped brief is not shown from its build: ok=%v paragraphs=%d audio=%v", ok, len(current.Narrative), current.Audio != nil)
	}
	if got := svc.store.mobileBriefStoredAudio(stripped); got == nil || got.SHA256 != built.Audio.SHA256 {
		t.Fatal("the play button has no recording for the stripped brief")
	}
}

// The play button plays the stripped brief's recording: Audio() serves the
// build's recording, bound to the words the build wrote (the daily check's
// stripped-brief scenario).
func TestStrippedBriefRecordingPlays(t *testing.T) {
	_, svc, built := builtBriefFixture(t)
	wav := trialSeedTone(1)
	sum := sha256.Sum256(wav)
	built.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mobileBriefAudioMimeWAV, Bytes: len(wav), SHA256: hex.EncodeToString(sum[:]), DurationSeconds: 1}
	built.Audio.binding = mobileBriefAudioBinding(built, "en")
	if err := svc.store.saveBuilt(built); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.store.dir, built.BriefID+mobileBriefAudioExtWAV), wav, 0o600); err != nil {
		t.Fatal(err)
	}
	stripped := stripMobileBriefWords(built)
	if err := svc.store.save(stripped, svc.now()); err != nil {
		t.Fatal(err)
	}
	svc.wg.Wait()
	health := svc.app.GetMobileBriefHealth()
	if health.Audio == nil || health.CurrentBriefID != built.BriefID {
		t.Fatalf("Today offers no recording: current=%q audio=%v", health.CurrentBriefID, health.Audio != nil)
	}
	data, ok := svc.Audio(built.BriefID)
	if !ok || data.Base64 == "" {
		t.Fatal("the stripped brief's recording does not play")
	}
}
