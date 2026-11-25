package main

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"wails-gui/internal/platform/activitymon"
)

// Run the actual handler with its timers owned by the bubble. Visual capture
// is parked, snapshot debounce is closed, and no native monitor is started.
func withCaptureFocusFixture(t *testing.T, test func(*testing.T, *App, func(activitymon.AppInfo, time.Time))) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		a := &App{activity: &activityMonitorState{
			visualStopCh: make(chan struct{}), lastCaptureTime: time.Now().UnixMilli(),
		}}
		a.meetingRecordingState.Store(meetingRecordingOff)
		defer func() {
			a.activity.mu.Lock()
			defer a.activity.mu.Unlock()
			for _, timer := range []*time.Timer{a.activity.idleTimer, a.activity.heartbeatTimer, a.activity.typingTimer} {
				if timer != nil && !timer.Stop() {
					t.Error("a capture fixture timer fired")
				}
			}
		}()
		saved := meetingProbes
		defer func() { browserMeetings.pending.Wait(); meetingProbes = saved }()
		meetingProbes.micHolders = func() micInputSnapshot { return micInputSnapshot{} }
		test(t, a, func(front activitymon.AppInfo, at time.Time) {
			meetingProbes.frontmost = func() activitymon.AppInfo { return front }
			a.sampleCaptureState(at)
		})
	})
}

func publicFocusChunk(a *App, at time.Time, duration time.Duration) capturedAudioChunk {
	return a.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source: backgroundListeningSourceMicrophone, CapturedAt: at, Duration: duration,
		CapturedApp: activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Public docs"},
	})
}

// Adapted from the desk's exact-head actual-handler reproduction. A public
// end snapshot cannot authorize unknown audio from earlier in the chunk.
func TestCaptureFocusUnavailableDelayedEvent(t *testing.T) {
	for _, privateFirst := range []bool{true, false} {
		for _, delivery := range []string{"none", "old public", "repeated and cached"} {
			name := "unknown startup/" + delivery
			if privateFirst {
				name = "private first/" + delivery
			}
			t.Run(name, func(t *testing.T) {
				withCaptureFocusFixture(t, func(t *testing.T, a *App, sample func(activitymon.AppInfo, time.Time)) {
					origin := time.Now().Add(-time.Minute)
					if privateFirst {
						sample(activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Private - Incognito"}, origin)
					}
					sample(activitymon.AppInfo{FocusUnavailable: true}, origin.Add(time.Second))
					deliver := func(offset time.Duration, kind activitymon.EventKind, title string) {
						a.handleActivityEvent(activitymon.ActivityEvent{
							Kind: kind, Timestamp: origin.Add(offset).UnixMilli(),
							AppBundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: title,
						})
					}
					if delivery != "none" {
						deliver(-10*time.Second, activitymon.EventKeyboard, "Public docs")
					}
					if delivery == "repeated and cached" {
						for i := 0; i < 40; i++ {
							deliver(-time.Duration(i)*time.Second, activitymon.EventKeyboard, "Public docs")
						}
						// A newer timestamp does not make cached keyboard text,
						// a titleless focus event, or an app switch a live sample.
						deliver(2*time.Second, activitymon.EventKeyboard, "Public docs")
						deliver(3*time.Second, activitymon.EventFocus, "")
						deliver(4*time.Second, activitymon.EventAppSwitch, "Public docs")
					}
					chunk := publicFocusChunk(a, origin.Add(6*time.Second), 5*time.Second)
					if !chunk.FocusUnavailable || a.shouldTranscribeBackgroundAudioChunk(chunk) || !chunkProtected(chunk) {
						t.Fatalf("unknown audio became readable: unavailable=%v private=%v ASR=%v protected=%v", chunk.FocusUnavailable, chunk.PrivateWindowInFront, a.shouldTranscribeBackgroundAudioChunk(chunk), chunkProtected(chunk))
					}
					if privateFirst && !chunk.PrivateWindowInFront {
						t.Fatal("delayed delivery erased the private interval")
					}
					// New successful sampler evidence recovers normally, while
					// the completed unknown/private overlap remains protected.
					sample(activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Public docs"}, origin.Add(10*time.Second))
					past := publicFocusChunk(a, origin.Add(8*time.Second), time.Second)
					if !past.FocusUnavailable || a.shouldTranscribeBackgroundAudioChunk(past) || !chunkProtected(past) {
						t.Fatal("recovery erased earlier unknown audio")
					}
					overlap := publicFocusChunk(a, origin.Add(12*time.Second), 5*time.Second)
					if !overlap.FocusUnavailable || a.shouldTranscribeBackgroundAudioChunk(overlap) || !chunkProtected(overlap) {
						t.Fatal("a newer safe end observation authorized earlier unknown audio")
					}
					future := publicFocusChunk(a, origin.Add(12*time.Second), time.Second)
					if future.FocusUnavailable || future.PrivateWindowInFront || !a.shouldTranscribeBackgroundAudioChunk(future) || chunkProtected(future) {
						t.Fatal("newer successful public focus did not recover")
					}
				})
			})
		}
	}
}

func TestCaptureFocusConcurrentSampleAndDelayedHandler(t *testing.T) {
	withCaptureFocusFixture(t, func(t *testing.T, a *App, sample func(activitymon.AppInfo, time.Time)) {
		origin := time.Now().Add(-time.Minute)
		sample(activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Private - Incognito"}, origin)
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			sample(activitymon.AppInfo{FocusUnavailable: true}, origin.Add(time.Second))
		}()
		go func() {
			defer workers.Done()
			a.handleActivityEvent(activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, Timestamp: origin.Add(-10 * time.Second).UnixMilli(),
				AppBundleID: "com.google.Chrome", WindowTitle: "Public docs",
			})
		}()
		workers.Wait()
		chunk := publicFocusChunk(a, origin.Add(6*time.Second), 5*time.Second)
		if !chunk.FocusUnavailable || !chunk.PrivateWindowInFront || a.shouldTranscribeBackgroundAudioChunk(chunk) || !chunkProtected(chunk) {
			t.Fatal("concurrent delayed delivery released privacy evidence")
		}
	})
}

func TestCaptureFocusSamplerOrderingAndHistoricalExclusions(t *testing.T) {
	withCaptureFocusFixture(t, func(t *testing.T, a *App, sample func(activitymon.AppInfo, time.Time)) {
		origin := time.Now().Add(-time.Minute)
		public := activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Public docs"}
		private := activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Private - Incognito"}
		sample(private, origin)
		sample(activitymon.AppInfo{FocusUnavailable: true}, origin.Add(time.Second))
		// Reordered and equal-time safe samples cannot release newer unknown.
		sample(public, origin.Add(-time.Second))
		sample(public, origin.Add(time.Second))
		unknown := publicFocusChunk(a, origin.Add(6*time.Second), 5*time.Second)
		if !unknown.FocusUnavailable || !unknown.PrivateWindowInFront || a.shouldTranscribeBackgroundAudioChunk(unknown) || !chunkProtected(unknown) {
			t.Fatal("out-of-order sampler cleared newer privacy evidence")
		}
		sample(public, origin.Add(10*time.Second))
		// Historical failed samples and private events add bounded history;
		// they neither reopen current unknown focus nor erase newer history.
		for i := 0; i < 40; i++ {
			sample(activitymon.AppInfo{FocusUnavailable: true}, origin.Add(-time.Duration(i)*time.Second))
			a.handleActivityEvent(activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, Timestamp: origin.Add(-time.Duration(i) * time.Second).UnixMilli(),
				AppBundleID: "com.google.Chrome", WindowTitle: "Private - Incognito",
			})
		}
		past := publicFocusChunk(a, origin.Add(6*time.Second), time.Second)
		if !past.FocusUnavailable || !past.PrivateWindowInFront || a.shouldTranscribeBackgroundAudioChunk(past) || !chunkProtected(past) {
			t.Fatal("repeated old evidence evicted the newer privacy interval")
		}
		future := publicFocusChunk(a, origin.Add(12*time.Second), time.Second)
		if future.FocusUnavailable || future.PrivateWindowInFront || !a.shouldTranscribeBackgroundAudioChunk(future) || chunkProtected(future) {
			t.Fatal("historical evidence reopened current focus")
		}
		for _, span := range []*frontSpan{&a.captureSpans.private, &a.captureSpans.unavailable} {
			if len(span.done) > frontSpanKeep {
				t.Fatal("history is no longer bounded")
			}
			for _, stretch := range span.done {
				if stretch.untilMs < stretch.sinceMs {
					t.Fatal("out-of-order evidence inverted a historical interval")
				}
			}
		}
	})
}

func TestCaptureFocusNewPrivateEventSurvivesOlderSafeSample(t *testing.T) {
	withCaptureFocusFixture(t, func(t *testing.T, a *App, sample func(activitymon.AppInfo, time.Time)) {
		origin := time.Now().Add(-time.Minute)
		public := activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Public docs"}
		sample(public, origin)
		a.handleActivityEvent(activitymon.ActivityEvent{
			Kind: activitymon.EventKeyboard, Timestamp: origin.Add(2 * time.Second).UnixMilli(),
			AppBundleID: "com.google.Chrome", PrivateWindow: true,
		})
		sample(public, origin.Add(time.Second))
		if chunk := publicFocusChunk(a, origin.Add(4*time.Second), time.Second); !chunk.PrivateWindowInFront || a.shouldTranscribeBackgroundAudioChunk(chunk) {
			t.Fatal("older safe sample cleared the newer private event")
		}
		sample(public, origin.Add(5*time.Second))
		if chunk := publicFocusChunk(a, origin.Add(7*time.Second), time.Second); chunk.PrivateWindowInFront || !a.shouldTranscribeBackgroundAudioChunk(chunk) {
			t.Fatal("newer successful sample did not release the private event")
		}
	})
}

func TestCaptureFocusEqualTimestampFavorsPrivateInEitherOrder(t *testing.T) {
	for _, privateFirst := range []bool{true, false} {
		name := "public first"
		if privateFirst {
			name = "private first"
		}
		t.Run(name, func(t *testing.T) {
			withCaptureFocusFixture(t, func(t *testing.T, a *App, sample func(activitymon.AppInfo, time.Time)) {
				origin := time.Now().Add(-time.Minute)
				public := activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Public docs"}
				private := activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Private - Incognito"}
				first, second := public, private
				if privateFirst {
					first, second = private, public
				}
				sample(first, origin)
				sample(second, origin)
				if chunk := publicFocusChunk(a, origin.Add(2*time.Second), time.Second); !chunk.PrivateWindowInFront || a.shouldTranscribeBackgroundAudioChunk(chunk) {
					t.Fatal("equal-time safe focus overrode private evidence")
				}
				sample(public, origin.Add(3*time.Second))
				if chunk := publicFocusChunk(a, origin.Add(5*time.Second), time.Second); chunk.PrivateWindowInFront || !a.shouldTranscribeBackgroundAudioChunk(chunk) {
					t.Fatal("strictly newer public focus did not recover")
				}
			})
		})
	}
}

func TestCaptureFocusLateEvidencePreservesEarlierPublicRecovery(t *testing.T) {
	withCaptureFocusFixture(t, func(t *testing.T, a *App, sample func(activitymon.AppInfo, time.Time)) {
		origin := time.Now().Add(-time.Minute)
		public := activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Public docs"}
		sample(activitymon.AppInfo{FocusUnavailable: true}, origin)
		sample(public, origin.Add(2*time.Second))
		for i := 3; i <= 42; i++ {
			sample(public, origin.Add(time.Duration(i)*time.Second))
		}
		// Late evidence predates the first recovery. It must not extend the
		// old exclusion across the already observed public interval [2s,42s], even after repeated public samples.
		sample(activitymon.AppInfo{FocusUnavailable: true}, origin.Add(time.Second))
		a.handleActivityEvent(activitymon.ActivityEvent{
			Kind: activitymon.EventKeyboard, Timestamp: origin.Add(time.Second).UnixMilli(),
			AppBundleID: "com.google.Chrome", WindowTitle: "Private - Incognito",
		})
		past := publicFocusChunk(a, origin.Add(time.Second), time.Second)
		if !past.FocusUnavailable || !past.PrivateWindowInFront || a.shouldTranscribeBackgroundAudioChunk(past) || !chunkProtected(past) {
			t.Fatal("late evidence lost the original historical exclusion")
		}
		recovered := publicFocusChunk(a, origin.Add(15*time.Second), time.Second)
		if recovered.FocusUnavailable || recovered.PrivateWindowInFront || !a.shouldTranscribeBackgroundAudioChunk(recovered) || chunkProtected(recovered) {
			t.Fatal("late evidence erased previously observed public recovery")
		}
	})
}

func TestCaptureFocusNewerEventCannotReleaseLateUnavailableSample(t *testing.T) {
	withCaptureFocusFixture(t, func(t *testing.T, a *App, sample func(activitymon.AppInfo, time.Time)) {
		origin := time.Now().Add(-time.Minute)
		a.handleActivityEvent(activitymon.ActivityEvent{
			Kind: activitymon.EventKeyboard, Timestamp: origin.Add(2 * time.Second).UnixMilli(),
			AppBundleID: "com.google.Chrome", WindowTitle: "Private - Incognito",
		})
		sample(activitymon.AppInfo{FocusUnavailable: true}, origin.Add(time.Second))
		chunk := publicFocusChunk(a, origin.Add(4*time.Second), time.Second)
		if !chunk.FocusUnavailable || !chunk.PrivateWindowInFront || a.shouldTranscribeBackgroundAudioChunk(chunk) || !chunkProtected(chunk) {
			t.Fatal("a newer historical event acted as a successful focus sample")
		}
		sample(activitymon.AppInfo{BundleID: "com.google.Chrome", WindowTitle: "Public docs"}, origin.Add(5*time.Second))
		if chunk := publicFocusChunk(a, origin.Add(7*time.Second), time.Second); chunk.FocusUnavailable || chunk.PrivateWindowInFront || !a.shouldTranscribeBackgroundAudioChunk(chunk) || chunkProtected(chunk) {
			t.Fatal("a newer live focus sample did not recover")
		}
	})
}
