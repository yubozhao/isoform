package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// startAudioService starts the fixture service with a counting voice and
// waits for the start-up recording, so later syntheses are remakes.
func startAudioService(t *testing.T) (*mobileBriefService, MobileBriefV1, *atomic.Int32) {
	t.Helper()
	svc, brief, _, _ := newAudioBackfillFixture(t)
	var syntheses atomic.Int32
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.synthesize = func(context.Context, string, string) ([]byte, error) {
		syntheses.Add(1)
		return testWAV(1), nil
	}
	svc.speaker = speaker
	svc.publish = func(MobileBriefV1, bool) error { return nil }
	svc.Start(context.Background())
	t.Cleanup(svc.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, _ := svc.store.load(brief.BriefID)
		if stored.Audio != nil && stored.Audio.Status == mobileBriefAudioStatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the start-up recording was not made")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return svc, brief, &syntheses
}

// staleRecording binds the stored recording to words the sealed copy does not
// say, the state a remake would act on.
func staleRecording(t *testing.T, svc *mobileBriefService, briefID string) {
	t.Helper()
	svc.patchMu.Lock()
	defer svc.patchMu.Unlock()
	stored, _ := svc.store.load(briefID)
	stored.Audio.binding = "en:v2:words-the-sealed-copy-does-not-say"
	if err := svc.store.saveDocument(stored, svc.now()); err != nil {
		t.Fatal(err)
	}
}

// Nov 13: every memory:updated made today's recording again, six times in one
// evening, and the brief had no player while each ran. Under seal v2 memory
// events never change the sealed words, so they never make the voice read the
// brief again, even when something else left the recording stale.
func TestMobileBriefMemoryEventsNeverRemakeAudio(t *testing.T) {
	svc, brief, syntheses := startAudioService(t)
	before := syntheses.Load()
	staleRecording(t, svc, brief.BriefID)
	for i := 0; i < 6; i++ {
		svc.app.emitBrowserEvent("memory:updated")
		waitForAudioWork(svc)
	}
	if got := syntheses.Load() - before; got != 0 {
		t.Fatalf("memory events made the recording %d more times, want none", got)
	}
}

// An explicit privacy withdrawal can change the sealed words, so it asks for
// a remake; the remake runs only when the recording no longer speaks them.
func TestMobileBriefPrivacyWithdrawalRemakesOnlyStaleAudio(t *testing.T) {
	svc, brief, syntheses := startAudioService(t)
	previous := mobileBriefLive.Swap(svc)
	t.Cleanup(func() { mobileBriefLive.Store(previous) })

	before := syntheses.Load()
	briefPrivacyChanged()
	waitForAudioWork(svc)
	if got := syntheses.Load() - before; got != 0 {
		t.Fatalf("a withdrawal that left the sealed words as they were made the recording %d more times", got)
	}

	staleRecording(t, svc, brief.BriefID)
	briefPrivacyChanged()
	for deadline := time.Now().Add(10 * time.Second); syntheses.Load()-before < 1 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	waitForAudioWork(svc)
	if got := syntheses.Load() - before; got != 1 {
		t.Fatalf("a recording that no longer speaks the sealed words was made %d times after a withdrawal, want once", got)
	}
	stored, _ := svc.store.load(brief.BriefID)
	shown, _, err := svc.finishedReadWithholding(stored)
	if err != nil || mobileBriefAudioNeedsRemake(stored.Audio, shown, "en") {
		t.Fatalf("the remade recording is not bound to the sealed words (err=%v)", err)
	}
}

// waitForAudioWork waits for a queued remake to take the audio lock and
// finish.
func waitForAudioWork(svc *mobileBriefService) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		queued := svc.audioRemakeQueued
		svc.mu.Unlock()
		if !queued && svc.audioMu.TryLock() {
			svc.audioMu.Unlock()
			time.Sleep(50 * time.Millisecond)
			svc.mu.Lock()
			queued = svc.audioRemakeQueued
			svc.mu.Unlock()
			if !queued && svc.audioMu.TryLock() {
				svc.audioMu.Unlock()
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTTSThreadsFollowTheComputer(t *testing.T) {
	for cpus, want := range map[int]int{4: 2, 6: 2, 8: 4, 10: 4, 16: 4} {
		if got := ttsThreadsFor(cpus); got != want {
			t.Fatalf("ttsThreadsFor(%d) = %d, want %d", cpus, got, want)
		}
	}
}
