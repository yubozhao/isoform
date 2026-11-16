package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
	"wails-gui/internal/platform/activitymon"

	"wails-gui/storage"
)

func newTimedPauseFixture(t *testing.T) *App {
	t.Helper()
	return newTimedPauseFixtureAt(t, filepath.Join(t.TempDir(), "pause.db"))
}

func newTimedPauseFixtureAt(t *testing.T, path string) *App {
	t.Helper()
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	store, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: store, audioScreenLocked: func() bool { return true }}
	t.Cleanup(func() { app.stopObservationResumeTimer(); store.Close() })
	return app
}

func pauseDeadline(t *testing.T, app *App) string {
	t.Helper()
	value, err := app.store.GetUIState(context.Background(), closedEyesResumeAtKey)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestTimedPauseResumesOnDeadlineAndEmitsExistingEvents(t *testing.T) {
	app := newTimedPauseFixture(t)
	broadcaster := NewEventBroadcaster()
	app.eventBroadcaster = broadcaster
	events := broadcaster.AddClient(t.Name())
	defer broadcaster.RemoveClient(t.Name())
	expect := func(want bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case raw := <-events:
				var event BrowserEvent
				if err := json.Unmarshal(raw, &event); err != nil {
					t.Fatal(err)
				}
				// The pause also posts its Chat row and refreshes Today; only the mode events are ordered here.
				if event.Event != closedEyesModeChangedEvent {
					continue
				}
				if len(event.Args) != 1 || event.Args[0] != want {
					t.Fatalf("event = %+v, want paused %v", event, want)
				}
				return
			case <-deadline:
				t.Fatalf("no pause event %v", want)
			}
		}
	}
	// Exercise the real timer, waiting on its completion event, without a sleep
	// or a narrow wall-clock assertion. Public minutes validation is separate.
	if _, err := app.setClosedEyesModeTimed(true, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	expect(true)
	expect(false)
	if app.IsClosedEyesModeEnabled() || pauseDeadline(t, app) != "" {
		t.Fatal("expiry did not clear pause and persistence")
	}
	pauses, err := app.observationPauses()
	if err != nil || len(pauses) != 1 || pauses[0].End == 0 {
		t.Fatalf("pause ledger = %+v, %v", pauses, err)
	}
}

func TestTimedPauseSurvivesStoreReopenWithExactDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pause.db")
	app := newTimedPauseFixtureAt(t, path)
	if err := app.PauseObservationFor(60); err != nil {
		t.Fatal(err)
	}
	deadline := pauseDeadline(t, app)
	started, err := app.GetTodayObservationPauseSince()
	if err != nil || started <= 0 {
		t.Fatalf("pause start: %d %v", started, err)
	}
	app.stopObservationResumeTimer()
	if err := app.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	restarted := &App{store: reopened, audioScreenLocked: func() bool { return true }}
	restarted.restoreObservationResumeTimer()
	t.Cleanup(restarted.stopObservationResumeTimer)
	if got, err := restarted.GetTodayObservationPauseSince(); err != nil || got != started {
		t.Fatalf("restored pause start = %d, %v; want %d", got, err, started)
	}
	at, ok := restarted.observationResumeAt()
	if !ok || !restarted.IsClosedEyesModeEnabled() || at.UTC().Format(time.RFC3339Nano) != deadline || pauseDeadline(t, restarted) != deadline {
		t.Fatalf("restored = paused %v, deadline %v, persisted %s", restarted.IsClosedEyesModeEnabled(), at, pauseDeadline(t, restarted))
	}
	// Startup resume intent must be present before a callback can own the lock.
	restarted.closedEyes.mu.Lock()
	if !restarted.closedEyes.resumeActivity || !restarted.closedEyes.resumeListening {
		t.Error("restored monitors would never resume")
	}
	// Keep this unit fixture from invoking native services on manual resume.
	restarted.closedEyes.resumeActivity, restarted.closedEyes.resumeListening = false, false
	restarted.closedEyes.mu.Unlock()
	if err := restarted.ResumeObservation(); err != nil {
		t.Fatal(err)
	}
	if pauseDeadline(t, restarted) != "" || restarted.IsClosedEyesModeEnabled() {
		t.Fatal("resume did not clear restored pause")
	}
}

func TestTimedPauseManualControlsAndReplacementFenceOldCallback(t *testing.T) {
	for _, control := range []string{"setter", "menu", "hotkey", "resume", "replacement", "shutdown"} {
		t.Run(control, func(t *testing.T) {
			app := newTimedPauseFixture(t)
			if err := app.PauseObservationFor(60); err != nil {
				t.Fatal(err)
			}
			app.closedEyes.mu.Lock()
			oldGeneration := app.closedEyes.generation
			app.closedEyes.mu.Unlock()
			switch control {
			case "setter":
				if err := app.SetClosedEyesModeEnabled(true); err != nil {
					t.Fatal(err)
				}
			case "menu":
				app.ToggleClosedEyesMode()
				app.ToggleClosedEyesMode()
			case "hotkey":
				app.toggleClosedEyesModeFromHotkey()
				app.toggleClosedEyesModeFromHotkey()
			case "resume":
				if err := app.ResumeObservation(); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				if err := app.PauseObservationFor(120); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				app.stopObservationResumeTimer()
			}
			wantPaused := control != "resume"
			wantDeadline := pauseDeadline(t, app)
			// Force the callback that already woke before cancellation to run
			// after the newer control has committed. It must have no effect.
			if _, err := app.setObservationPause(false, time.Time{}, oldGeneration, false); err != nil {
				t.Fatal(err)
			}
			if app.IsClosedEyesModeEnabled() != wantPaused || pauseDeadline(t, app) != wantDeadline {
				t.Fatal("detached callback overwrote a newer decision")
			}
			at, timed := app.observationResumeAt()
			if control != "replacement" && control != "shutdown" && (timed || wantDeadline != "") {
				t.Fatalf("manual control kept timer %v", at)
			}
			if control == "replacement" && !timed {
				t.Fatal("replacement lost timer")
			}
			if control == "shutdown" && wantDeadline == "" {
				t.Fatal("shutdown erased deadline")
			}
		})
	}
}

func TestTimedPauseValidationAndPersistenceFailure(t *testing.T) {
	app := newTimedPauseFixture(t)
	for _, minutes := range []int{-1, 0, 1441, int(^uint(0) >> 1)} {
		if err := app.PauseObservationFor(minutes); err == nil {
			t.Fatalf("accepted %d minutes", minutes)
		}
	}
	if app.IsClosedEyesModeEnabled() {
		t.Fatal("invalid duration paused observation")
	}
	if err := app.PauseObservationFor(1440); err != nil {
		t.Fatal(err)
	}
	app.store.Close()
	if err := app.ResumeObservation(); err == nil {
		t.Fatal("reported successful resume after persistence failed")
	}
	if !app.IsClosedEyesModeEnabled() {
		t.Fatal("failed durable clear must remain paused")
	}
	if err := (&App{}).PauseObservationFor(60); err == nil {
		t.Fatal("timed pause without persistence succeeded")
	}
}

func TestTimedPauseRestoreExpiredAndCorruptDeadline(t *testing.T) {
	for _, value := range []string{time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), "broken"} {
		t.Run(value, func(t *testing.T) {
			app := newTimedPauseFixture(t)
			if err := app.store.SetUIState(context.Background(), closedEyesResumeAtKey, value); err != nil {
				t.Fatal(err)
			}
			app.restoreObservationResumeTimer()
			if value == "broken" {
				if !app.IsClosedEyesModeEnabled() {
					t.Fatal("corrupt persisted pause resumed observation")
				}
				app.closedEyes.mu.Lock()
				if !app.closedEyes.resumeActivity || !app.closedEyes.resumeListening {
					t.Error("conservative startup pause lost resume intent")
				}
				app.closedEyes.resumeActivity, app.closedEyes.resumeListening = false, false
				app.closedEyes.mu.Unlock()
				if err := app.ResumeObservation(); err != nil {
					t.Fatal(err)
				}
				if pauseDeadline(t, app) != "" {
					t.Fatal("manual resume did not clear corrupt deadline")
				}
			} else if app.IsClosedEyesModeEnabled() || pauseDeadline(t, app) != "" {
				t.Fatal("expired deadline did not clear")
			}
		})
	}
}

func TestTimedPausePersistenceFailureDoesNotEmitResumeOrBlockManualPrivacy(t *testing.T) {
	app := newTimedPauseFixture(t)
	if err := app.PauseObservationFor(60); err != nil {
		t.Fatal(err)
	}
	broadcaster := NewEventBroadcaster()
	events := broadcaster.AddClient(t.Name())
	defer broadcaster.RemoveClient(t.Name())
	app.eventBroadcaster = broadcaster
	app.store.Close()
	if err := app.ResumeObservation(); err == nil {
		t.Fatal("missing persistence error")
	}
	select {
	case event := <-events:
		t.Fatalf("failed resume emitted an event: %s", event)
	default:
	}
	// Manual privacy remains available even when no state can be saved.
	app.closedEyes.enabled.Store(false)
	if err := app.SetClosedEyesModeEnabled(true); err == nil {
		t.Fatal("durable clear failure was hidden")
	}
	if !app.IsClosedEyesModeEnabled() {
		t.Fatal("failed storage prevented a privacy pause")
	}
	if _, timed := app.observationResumeAt(); timed {
		t.Fatal("failed manual clear retained live callback")
	}
}

// A timed request cannot take ownership of an existing manual privacy pause.
func TestTimedPauseNeverEndsManualPause(t *testing.T) {
	app := newTimedPauseFixture(t)
	clock := &fakeMobileBriefClock{now: time.Now().UTC()}
	app.closedEyes.now = clock.Now
	if err := app.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := app.PauseObservationFor(60); err == nil || err.Error() != "observation_manual_pause_active" {
		t.Fatalf("timed request over manual pause: %v", err)
	}
	if _, timed := app.observationResumeAt(); timed || pauseDeadline(t, app) != "" {
		t.Fatal("manual pause acquired a timer")
	}
	clock.Advance(2 * time.Hour)
	app.checkObservationDeadline()
	if !app.IsClosedEyesModeEnabled() {
		t.Fatal("a timer ended a pause it did not start")
	}
	if err := app.ResumeObservation(); err != nil {
		t.Fatal(err)
	}
	if app.IsClosedEyesModeEnabled() {
		t.Fatal("manual resume did not end manual pause")
	}
}

func TestTimedPauseUsesWallDeadlineAfterSleep(t *testing.T) {
	for _, signal := range []string{"wake", "ticker"} {
		t.Run(signal, func(t *testing.T) {
			app := newTimedPauseFixture(t)
			clock := &fakeMobileBriefClock{now: time.Now().UTC()}
			app.closedEyes.now = clock.Now
			if err := app.PauseObservationFor(60); err != nil {
				t.Fatal(err)
			}
			app.checkObservationDeadline()
			if !app.IsClosedEyesModeEnabled() {
				t.Fatal("resumed before wall deadline")
			}
			broadcaster := NewEventBroadcaster()
			events := broadcaster.AddClient(t.Name())
			defer broadcaster.RemoveClient(t.Name())
			app.eventBroadcaster = broadcaster
			// Sleep advances the wall clock but does not advance the real
			// duration timer by an hour. No native app or sleep is required.
			clock.Advance(2 * time.Hour)
			if signal == "wake" {
				signals := newContextSignalService(app)
				signals.onSystemState(activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now()})
				if app.IsClosedEyesModeEnabled() {
					t.Fatal("wake did not reconcile expired wall deadline")
				}
			}
			// Other events (for example the system:state wake broadcast) may
			// arrive first; wait for the closed-eyes change itself.
			deadline := time.After(5 * time.Second)
		waitResume:
			for {
				select {
				case raw := <-events:
					var event BrowserEvent
					if err := json.Unmarshal(raw, &event); err != nil {
						t.Fatal(err)
					}
					if event.Event != closedEyesModeChangedEvent {
						continue
					}
					if len(event.Args) != 1 || event.Args[0] != false {
						t.Fatalf("resume event: %+v", event)
					}
					break waitResume
				case <-deadline:
					t.Fatal("short deadline check did not recover a missed wake")
				}
			}
			if app.IsClosedEyesModeEnabled() || pauseDeadline(t, app) != "" {
				t.Fatal("expired wall pause remained active")
			}
		})
	}
}

func TestTimedPauseRetriesFailedDurableClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pause.db")
	app := newTimedPauseFixtureAt(t, path)
	clock := &fakeMobileBriefClock{now: time.Now().UTC()}
	app.closedEyes.now = clock.Now
	if err := app.PauseObservationFor(60); err != nil {
		t.Fatal(err)
	}
	app.closedEyes.mu.Lock()
	app.store.Close()
	app.closedEyes.mu.Unlock()
	clock.Advance(2 * time.Hour)
	app.checkObservationDeadline()
	if !app.IsClosedEyesModeEnabled() {
		t.Fatal("failed clear resumed observation")
	}
	reopened, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.stopObservationResumeTimer(); reopened.Close() })
	broadcaster := NewEventBroadcaster()
	events := broadcaster.AddClient(t.Name())
	defer broadcaster.RemoveClient(t.Name())
	app.closedEyes.mu.Lock()
	app.store = reopened
	app.eventBroadcaster = broadcaster
	app.closedEyes.mu.Unlock()
	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("failed durable clear was never retried")
	}
	if app.IsClosedEyesModeEnabled() || pauseDeadline(t, app) != "" {
		t.Fatal("retry did not clear the expired pause")
	}
}

func TestGetClosedEyesResumeAtReadsTheTimedDeadlineOnly(t *testing.T) {
	app := newTimedPauseFixtureAt(t, filepath.Join(t.TempDir(), "resume-at.db"))
	t.Cleanup(app.stopObservationResumeTimer)
	if got := app.GetClosedEyesResumeAt(); got != "" {
		t.Fatalf("open eyes resume at = %q, want empty", got)
	}
	if err := app.PauseObservationFor(15); err != nil {
		t.Fatal(err)
	}
	got := app.GetClosedEyesResumeAt()
	at, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("timed resume at = %q: %v", got, err)
	}
	if until := time.Until(at); until < 13*time.Minute || until > 16*time.Minute {
		t.Fatalf("timed resume at is %v away, want about 15 minutes", until)
	}
	if err := app.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	if got := app.GetClosedEyesResumeAt(); got != "" {
		t.Fatalf("closed until opened resume at = %q, want empty", got)
	}
	if err := app.ResumeObservation(); err != nil {
		t.Fatal(err)
	}
	if got := app.GetClosedEyesResumeAt(); got != "" {
		t.Fatalf("resumed resume at = %q, want empty", got)
	}
}
