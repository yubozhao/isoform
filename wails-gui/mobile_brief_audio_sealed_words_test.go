package main

import (
	"context"
	"testing"
)

// 2026-11-14 morning: the recording was bound to the sealed words, which
// differed from a fresh projection of the stored words, so the seal left it
// out and Today showed no player. A recording bound to the words being sealed
// plays with them.
func TestMobileBriefSealKeepsARecordingBoundToTheSealedWords(t *testing.T) {
	svc, brief, _, _ := newAudioBackfillFixture(t)
	sealed := svc.app.mobileBriefForDelivery(brief)
	sealed.Audio = nil
	source := brief
	source.Narrative = append([]MobileBriefPara(nil), brief.Narrative...)
	source.Narrative[0].Text = "Stored words that the sealed copy reads differently."
	source.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mobileBriefAudioMimeM4A, Bytes: 10, DurationSeconds: 3, binding: mobileBriefAudioBinding(sealed, "en")}
	audio := svc.finishedReadAudio(context.Background(), source, sealed)
	if audio == nil || audio.Status != mobileBriefAudioStatusOK {
		t.Fatalf("the seal dropped a recording bound to its own words: %+v", audio)
	}
	// A recording bound to other words still does not play.
	source.Audio.binding = "en:v2:words-nobody-sees"
	if audio := svc.finishedReadAudio(context.Background(), source, sealed); audio != nil {
		t.Fatal("the seal attached a recording bound to words it does not show")
	}
}

// The live Today pass keeps a recording that speaks the current sealed copy,
// even when a fresh projection of the stored words reads differently.
// Dropping it there erased the 2026-11-14 morning recording at 10:21. A
// recording of other words still leaves.
func TestMobileBriefTodayPassKeepsARecordingOfTheSealedCopy(t *testing.T) {
	svc, brief, _, _ := newAudioBackfillFixture(t)
	sealed, _, wrote := svc.seal(context.Background(), brief)
	if !wrote {
		t.Fatal("the fixture brief was not sealed")
	}
	recording := &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mobileBriefAudioMimeM4A, Bytes: 10, DurationSeconds: 3, binding: mobileBriefAudioBinding(sealed, "en")}
	stored := brief
	stored.Narrative = append([]MobileBriefPara(nil), brief.Narrative...)
	stored.Narrative[0].Text = "Stored words that the sealed copy reads differently."
	stored.Audio = recording
	kept, _ := mobileBriefTodayReadableSynthesis(svc.app, stored)
	if kept.Audio == nil || kept.Audio.binding != recording.binding {
		t.Fatalf("the Today pass dropped a recording of the sealed copy: %+v", kept.Audio)
	}
	stored.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Bytes: 10, binding: "en:v2:words-nobody-sees"}
	if dropped, _ := mobileBriefTodayReadableSynthesis(svc.app, stored); dropped.Audio != nil {
		t.Fatal("the Today pass kept a recording of words neither the read nor the sealed copy says")
	}
}

// 2026-11-14 replay: the sealed copy says one word differently from a live
// projection of the stored brief, and the recording was lost. The brief is
// recorded again from the sealed words, and the sealed read (the computer's
// card and the phone's package) carries that recording.
func TestMobileBriefLostRecordingIsMadeAgainFromTheSealedWords(t *testing.T) {
	svc, brief, _, _ := newAudioBackfillFixture(t)
	if _, _, wrote := svc.seal(context.Background(), brief); !wrote {
		t.Fatal("the fixture brief was not sealed")
	}
	saved, found, err := svc.store.loadFinishedRead(brief.BriefID)
	if err != nil || !found {
		t.Fatalf("no sealed copy: %v", err)
	}
	var sealed MobileBriefV1
	if err := unmarshalStoredMobileBrief(saved.Brief, &sealed); err != nil {
		t.Fatal(err)
	}
	sealed.Narrative[0].Text = "You shipped the plans. What do you want to start with?"
	raw, err := marshalStoredMobileBrief(sealed)
	if err != nil {
		t.Fatal(err)
	}
	saved.Brief = raw
	if err := svc.store.saveFinishedReadIfCurrent(brief.BriefID, saved); err != nil {
		t.Fatal(err)
	}
	spoken := ""
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.synthesize = func(_ context.Context, _, text string) ([]byte, error) {
		spoken += text
		return testWAV(1), nil
	}
	svc.speaker = speaker
	svc.publish = func(MobileBriefV1, bool) error { return nil }
	svc.backfillTodayAudio(context.Background())
	if want := mobileBriefSpokenText(sealed, "en"); spoken != want {
		t.Fatalf("the voice read %q, want the sealed words %q", spoken, want)
	}
	// Saving the recording seals the brief again from its sources, which here
	// re-derives the copy's words; the next pass records those, and then the
	// recording and the sealed copy agree.
	spoken = ""
	svc.backfillTodayAudio(context.Background())
	stored, _ := svc.store.load(brief.BriefID)
	if stored.Audio == nil || stored.Audio.Status != mobileBriefAudioStatusOK {
		t.Fatalf("no recording was stored: %+v", stored.Audio)
	}
	read, _, err := svc.finishedReadWithholding(stored)
	if err != nil || read.Audio == nil || read.Audio.Status != mobileBriefAudioStatusOK {
		t.Fatalf("the sealed read has no player: %+v, %v", read.Audio, err)
	}
	spoken = ""
	svc.backfillTodayAudio(context.Background())
	if spoken != "" {
		t.Fatal("a recording that speaks the sealed copy was made again")
	}
}
