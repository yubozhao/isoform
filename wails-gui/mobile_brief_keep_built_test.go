package main

import (
	"math/rand"
	"testing"
)

// assertEveryReaderKeepsBuilt checks the stored copies and every reader that
// does not go through the shared read: the stored brief (.json), the sealed
// copy (.read), the delivery projection the feed, publish, Today chat, your
// week and holds read, the podcast feed and its audio gate, and the stored
// delivery to the phone. The recording stays the build's, so play-ahead has
// nothing to make again (no re-voice loop).
func assertEveryReaderKeepsBuilt(t *testing.T, app *App, svc *mobileBriefService, built MobileBriefV1, step string) {
	t.Helper()
	check := func(surface string, got MobileBriefV1, audio bool) {
		t.Helper()
		if got.BriefID != built.BriefID || !mobileBriefShowsWrittenWords(got) || !mobileBriefSameNarrativeText(got, built) {
			t.Fatalf("%s: %s has status=%q paragraphs=%d, want the built words", step, surface, got.NarrativeStatus, len(got.Narrative))
		}
		if audio && (got.Audio == nil || got.Audio.SHA256 != built.Audio.SHA256) {
			t.Fatalf("%s: %s lost the built recording", step, surface)
		}
	}
	stored, ok := svc.store.load(built.BriefID)
	if !ok {
		t.Fatalf("%s: the stored brief is gone", step)
	}
	check("the stored brief", stored, true)
	if stored.Audio.binding != built.Audio.binding {
		t.Fatalf("%s: the stored recording's binding changed, so play-ahead would make it again", step)
	}
	if saved, found, err := svc.store.loadFinishedRead(built.BriefID); err == nil && found && saved.Schema == mobileBriefSealedSchema {
		var sealed MobileBriefV1
		if unmarshalStoredMobileBrief(saved.Brief, &sealed) == nil {
			check("the sealed copy", sealed, false)
		}
	}
	check("the delivery projection", app.mobileBriefForDelivery(stored), true)
	found := false
	for _, brief := range svc.feedBriefs() {
		if brief.BriefID == built.BriefID {
			check("the podcast feed", brief, true)
			found = true
		}
	}
	if !found {
		t.Fatalf("%s: the podcast feed has no brief", step)
	}
	if svc.feed != nil && svc.feed.audioAllowed != nil && !svc.feed.audioAllowed(built.BriefID) {
		t.Fatalf("%s: the podcast feed withholds the recording", step)
	}
	sent := false
	svc.deliverStoredBrief(stored, func(brief MobileBriefV1) {
		check("the stored delivery", brief, true)
		sent = true
	})
	if !sent {
		t.Fatalf("%s: the stored delivery sent nothing", step)
	}
}

// boundBuiltBriefFixture is builtBriefFixture with a recording bound to its
// words (v3), as the voice makes it, so every reader may play it.
func boundBuiltBriefFixture(t *testing.T) (*App, *mobileBriefService, MobileBriefV1) {
	t.Helper()
	app, svc, built := builtBriefFixture(t)
	built.Audio.binding = mobileBriefAudioBinding(built, "en")
	if err := svc.store.saveBuilt(built); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.save(built, svc.now()); err != nil {
		t.Fatal(err)
	}
	svc.wg.Wait()
	return app, svc, built
}

// The store rule (Bo, 2026-11-15, ranked goals): no refresh, gate, projection
// or seal stores a built brief with fewer words or less audio than its
// build. A seeded random run of what can happen after a build, including the
// passes that dropped words on 11-15 and gates that drop only some
// paragraphs; after every step the stored copies and every reader keep the
// build's words and recording.
func TestEveryReaderKeepsTheBuiltWordsAndRecording(t *testing.T) {
	app, svc, built := boundBuiltBriefFixture(t)
	assertEveryReaderKeepsBuilt(t, app, svc, built, "after the build")
	partial := func(brief MobileBriefV1) MobileBriefV1 {
		brief.Narrative = append([]MobileBriefPara(nil), brief.Narrative[:1]...)
		brief.Audio = nil
		return brief
	}
	steps := []struct {
		name string
		run  func()
	}{
		{"a live Today refresh", func() { svc.refreshToday(false) }},
		{"a live-only refresh", func() { svc.refreshToday(true) }},
		{"a pass saves the brief without its words", func() {
			stored, _ := svc.store.load(built.BriefID)
			_ = svc.store.save(stripMobileBriefWords(stored), svc.now())
		}},
		{"a gate drops one paragraph and the recording on save", func() {
			stored, _ := svc.store.load(built.BriefID)
			_ = svc.store.save(partial(stored), svc.now())
		}},
		{"a pass seals the brief without its words", func() {
			stored, _ := svc.store.load(built.BriefID)
			svc.seal(t.Context(), stripMobileBriefWords(stored))
		}},
		{"a pass seals one paragraph", func() {
			stored, _ := svc.store.load(built.BriefID)
			svc.seal(t.Context(), partial(stored))
		}},
		{"Today's copy is saved without its words", func() {
			stored, _ := svc.store.load(built.BriefID)
			_ = svc.store.saveCurrentToday(stripMobileBriefWords(stored))
		}},
		{"Closed Eyes turns on", func() { _ = app.SetClosedEyesModeEnabled(true) }},
		{"Closed Eyes turns off", func() { _ = app.SetClosedEyesModeEnabled(false) }},
		{"an unkeyed privacy change", func() { briefPrivacyChanged() }},
		{"an uncited source is withdrawn", func() { briefPrivacyChanged(briefWithdrawn{Kind: "memory", ID: "protected-recall"}) }},
	}
	random := rand.New(rand.NewSource(20261115))
	for range 40 {
		step := steps[random.Intn(len(steps))]
		step.run()
		svc.todayRefreshWG.Wait()
		svc.wg.Wait()
		if today, ok := svc.store.currentToday(); ok && today.BriefID == built.BriefID && today.AsOf == built.AsOf && !mobileBriefSameNarrativeText(today, built) {
			t.Fatalf("%s: Today's copy lost the built words", step.name)
		}
		assertEveryReaderKeepsBuilt(t, app, svc, built, step.name)
	}
	_ = app.SetClosedEyesModeEnabled(false)
}

// An explicit keyed withdrawal is the one change that amends a built brief,
// in the stored copy too: the paragraph citing the withdrawn source is left
// out, and the rest stays.
func TestStoredBuiltBriefAmendsOnlyForAKeyedWithdrawal(t *testing.T) {
	_, svc, built := builtBriefFixture(t)
	briefPrivacyChanged(briefWithdrawn{Kind: "memory", ID: "safe-recall"})
	svc.wg.Wait()
	stored, _ := svc.store.load(built.BriefID)
	stored.Narrative = nil
	stored.NarrativeStatus = "unavailable"
	if err := svc.store.save(stored, svc.now()); err != nil {
		t.Fatal(err)
	}
	svc.wg.Wait()
	stored, _ = svc.store.load(built.BriefID)
	if len(stored.Narrative) != 1 || stored.Narrative[0].Text != "The second written paragraph." {
		t.Fatalf("stored after a keyed withdrawal: paragraphs=%d, want only the uncited paragraph", len(stored.Narrative))
	}
}

// A corrected paragraph keeps its corrected words when a later save drops a
// different paragraph: the build only fills in what is missing.
func TestKeepBuiltKeepsACorrectedParagraph(t *testing.T) {
	built := MobileBriefV1{BriefID: "2026-11-15-morning", AsOf: "2026-11-15T06:51:21Z", NarrativeStatus: "ok",
		Evidence:  []MobileBriefRef{{Ref: "e1", SourceKind: "memory", SourceID: "a"}, {Ref: "e2", SourceKind: "calendar", SourceID: "b"}},
		Narrative: []MobileBriefPara{{Text: "Old belief.", Refs: []string{"e1"}}, {Text: "A meeting.", Refs: []string{"e2"}}},
	}
	doc := built
	doc.Narrative = []MobileBriefPara{{Text: "Corrected belief.", Refs: []string{"e1"}}}
	kept, changed := mobileBriefKeepBuilt(doc, built, nil)
	if !changed || len(kept.Narrative) != 2 || kept.Narrative[0].Text != "Corrected belief." || kept.Narrative[1].Text != "A meeting." {
		t.Fatalf("changed=%v narrative=%v", changed, kept.Narrative)
	}
}

// 2026-11-14 morning: the sealed copy rendered three of four paragraphs for
// the moment it was sealed and lost the recording, while the stored words
// stayed the build's. The read plays the build's recording again; a stored
// rewrite still never plays it (TestMobileBriefAudioNeverSpeaksTextTheBriefNoLongerShows).
func TestSealedRenderingKeepsTheBuildRecording(t *testing.T) {
	_, svc, built := boundBuiltBriefFixture(t)
	stored, _ := svc.store.load(built.BriefID)
	if _, _, err := svc.finishedReadWithholding(stored); err != nil {
		t.Fatal(err)
	}
	saved, found, err := svc.store.loadFinishedRead(built.BriefID)
	if err != nil || !found {
		t.Fatalf("no sealed copy: %v", err)
	}
	var rendered MobileBriefV1
	if err := unmarshalStoredMobileBrief(saved.Brief, &rendered); err != nil {
		t.Fatal(err)
	}
	rendered.Narrative[0].Text = "The first written paragraph, as of this morning."
	rendered.Audio = nil
	if saved.Brief, err = marshalStoredMobileBrief(rendered); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.saveFinishedRead(built.BriefID, saved); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := svc.store.loadFinishedRead(built.BriefID); again.Brief == nil {
		t.Fatal("the rendered seal was not saved")
	} else {
		var check MobileBriefV1
		_ = unmarshalStoredMobileBrief(again.Brief, &check)
		if check.Audio != nil || check.Narrative[0].Text != rendered.Narrative[0].Text {
			t.Fatal("the test did not store a rendered seal without its recording")
		}
	}
	read, _, err := svc.finishedReadWithholding(stored)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Narrative) != len(built.Narrative) || read.Audio == nil || read.Audio.SHA256 != built.Audio.SHA256 {
		t.Fatalf("the sealed rendering lost the build's recording: paragraphs=%d audio=%v", len(read.Narrative), read.Audio != nil)
	}
	if got, ok := svc.Audio(built.BriefID); ok && got.Status != "" && got.Status != mobileBriefAudioStatusOK {
		t.Fatalf("the play button has no recording: %+v", got.Status)
	}
}
