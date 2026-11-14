package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The 2026-11-13 morning brief's words were stored at 01:07:54 and its audio
// at 01:27:58: the power hold ended with the words, so the locked Mac ran the
// voice under App Nap. The recording now keeps its own hold until it is saved.
func TestMobileBriefAudioHoldsPowerWhileTheVoiceSpeaks(t *testing.T) {
	svc, brief, _, _ := newAudioBackfillFixture(t)
	var held, holds, releases atomic.Int32
	svc.power = func(string) (func(), error) {
		held.Add(1)
		holds.Add(1)
		var once sync.Once
		return func() {
			once.Do(func() {
				held.Add(-1)
				releases.Add(1)
			})
		}, nil
	}
	var heldWhileSpeaking atomic.Bool
	heldWhileSpeaking.Store(true)
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.synthesize = func(context.Context, string, string) ([]byte, error) {
		if held.Load() < 1 {
			heldWhileSpeaking.Store(false)
		}
		return testWAV(1), nil
	}
	svc.speaker = speaker
	svc.addAudio(context.Background(), brief, svc.now())

	if !heldWhileSpeaking.Load() {
		t.Fatal("the voice spoke without a power hold")
	}
	if holds.Load() != 1 || releases.Load() != 1 || held.Load() != 0 {
		t.Fatalf("holds=%d releases=%d held=%d, want one hold released after saving", holds.Load(), releases.Load(), held.Load())
	}
	stored, _ := svc.store.load(brief.BriefID)
	if stored.Audio == nil || stored.Audio.Status != mobileBriefAudioStatusOK {
		t.Fatalf("stored audio = %+v, want a saved recording", stored.Audio)
	}
}

// A brief without a voice takes no power hold for its audio step.
func TestMobileBriefAudioTakesNoHoldWithoutAVoice(t *testing.T) {
	svc, brief, _, _ := newAudioBackfillFixture(t)
	var holds atomic.Int32
	svc.power = func(string) (func(), error) {
		holds.Add(1)
		return func() {}, nil
	}
	svc.speaker = mobileBriefSpeaker{}
	svc.addAudio(context.Background(), brief, svc.now())
	if holds.Load() != 0 {
		t.Fatalf("holds=%d, want none without a voice", holds.Load())
	}
}

// While a recording bound to words the brief no longer says is made again,
// the brief says the recording is on its way instead of showing no player,
// and the new recording is bound to the words as they read now.
func TestMobileBriefAudioRemakeSaysItIsPreparing(t *testing.T) {
	svc, brief, _, _ := newAudioBackfillFixture(t)
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mobileBriefAudioMimeM4A, Bytes: 10, DurationSeconds: 3, binding: "en:v2:words-the-brief-no-longer-says"}
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var startOnce sync.Once
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.synthesize = func(ctx context.Context, _, _ string) ([]byte, error) {
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
			return testWAV(1), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	svc.speaker = speaker
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.addAudio(context.Background(), brief, svc.now())
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the stale recording was not made again")
	}
	stored, _ := svc.store.load(brief.BriefID)
	if stored.Audio == nil || stored.Audio.Status != mobileBriefAudioStatusRendering {
		t.Fatalf("stored audio while speaking = %+v, want the rendering mark", stored.Audio)
	}
	if delivered := svc.app.mobileBriefForDelivery(stored); delivered.Audio == nil || delivered.Audio.Status != mobileBriefAudioStatusRendering {
		t.Fatalf("delivered audio while speaking = %+v, want rendering so Today says it is preparing", delivered.Audio)
	}
	close(release)
	<-done
	stored, _ = svc.store.load(brief.BriefID)
	if stored.Audio == nil || stored.Audio.Status != mobileBriefAudioStatusOK || stored.Audio.binding != mobileBriefAudioBinding(stored, "en") {
		t.Fatalf("stored audio after speaking = %+v, want a recording bound to the current words", stored.Audio)
	}
	if delivered := svc.app.mobileBriefForDelivery(stored); delivered.Audio == nil || delivered.Audio.Status != mobileBriefAudioStatusOK {
		t.Fatal("the new recording does not play")
	}
}

// A recording still bound to the brief's words keeps playing: no rendering
// mark replaces it.
func TestMobileBriefAudioBoundRecordingIsNotMarked(t *testing.T) {
	svc, brief, _, _ := newAudioBackfillFixture(t)
	binding := mobileBriefAudioBinding(svc.app.mobileBriefForDelivery(brief), "en")
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Bytes: 10, binding: binding}
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	svc.speaker = fakeSpeaker("en", testWAV(1), nil)
	if svc.markAudioRendering(brief.BriefID, binding, "en", svc.now()) {
		t.Fatal("a playable recording was replaced by the rendering mark")
	}
}

// The recording yields to the app for its first 45 seconds, then runs at
// default priority so it still finishes in time on a loaded computer.
func TestMobileBriefAudioLowPriorityHasABudget(t *testing.T) {
	start := time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)
	ctx := withMobileBriefAudioUtilityBudget(context.Background(), start)
	if !mobileBriefAudioLowPriority(ctx, start.Add(44*time.Second)) {
		t.Fatal("a recording within its budget did not run at low priority")
	}
	if mobileBriefAudioLowPriority(ctx, start.Add(mobileBriefAudioUtilityBudget)) {
		t.Fatal("a recording past its budget still ran at low priority")
	}
	if !mobileBriefAudioLowPriority(context.Background(), start) {
		t.Fatal("brief synthesis without a budget did not run at low priority")
	}
}

// The service sets the budget on the context the voice reads.
func TestMobileBriefAudioCarriesTheBudget(t *testing.T) {
	svc, brief, _, _ := newAudioBackfillFixture(t)
	var budgeted atomic.Bool
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.synthesize = func(ctx context.Context, _, _ string) ([]byte, error) {
		_, ok := ctx.Value(mobileBriefAudioUtilityUntilKey{}).(time.Time)
		budgeted.Store(ok)
		return testWAV(1), nil
	}
	svc.speaker = speaker
	svc.addAudio(context.Background(), brief, svc.now())
	if !budgeted.Load() {
		t.Fatal("the recording ran without its low-priority budget")
	}
}
