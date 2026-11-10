package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

// Adapted from the desk's PR #216 exact-head probe. Exercise the production
// publication lifecycle, rather than restoring a helper before a direct read.
func TestLaunchPublishesObservationPolicyBeforeFirstScreen(t *testing.T) {
	for _, policy := range []string{"future", "expired", "empty", "malformed", "read_failed", "expires_before_paint"} {
		t.Run(policy, func(t *testing.T) {
			rig, svc, brief := cardRigWithStore(t, func(rig *handoffTestRig) {
				root, err := flavorDataDir()
				if err != nil {
					t.Fatal(err)
				}
				work, err := handoff.NewStore(filepath.Join(root, "handoff"))
				if err != nil {
					t.Fatal(err)
				}
				rig.app.handoffStore = work
			})
			svc.wg.Wait()
			app := rig.app
			app.launch = newLaunchStartup()
			app.storageStartup = newStorageStartupState()
			app.audioScreenLocked = func() bool { return true }
			previous := todayBriefPermissionStatus
			todayBriefPermissionStatus = func(*App) PlatformPermissions { return PlatformPermissions{} }
			t.Cleanup(func() { todayBriefPermissionStatus = previous })
			// Keep all source gates real, including the canonical handoff store
			// that mandatory publication opens for the first screen.
			// A real, locally rendered recording makes the withheld-audio
			// assertion meaningful; no provider, model or native capture runs.
			// Only the model's ok text is read aloud (design R74): a recording needs model
			// words. A template opening is never spoken, so it would publish with no audio.
			brief.Narrative, brief.NarrativeStatus, _ = testModelCompose(context.Background(), nil, brief, nil)
			if err := svc.store.save(brief, svc.now()); err != nil {
				t.Fatal(err)
			}
			svc.speaker = fakeSpeaker("en", testWAV(1), nil)
			svc.addAudio(context.Background(), brief, svc.now())
			svc.store = newMobileBriefStore(filepath.Dir(svc.store.dir))
			assertView := func(withheld bool) {
				t.Helper()
				got, ok := svc.storedLatestForLaunch()
				if !ok || got.BriefID != brief.BriefID {
					t.Fatal("publication lost canonical brief identity")
				}
				if withheld {
					if len(got.Sections.OpenLoops) != 0 || len(got.Narrative) != 0 || len(got.Cards) != 0 || got.Audio != nil {
						t.Fatal("startup exposed paused brief text, cards or audio")
					}
					if audio, err := app.GetMobileBriefAudio(brief.BriefID); err == nil || audio.Base64 != "" {
						t.Fatal("audio endpoint bypassed the startup pause")
					}
				} else {
					if len(got.Sections.OpenLoops) == 0 || len(got.Narrative) == 0 || len(got.Cards) == 0 || got.Audio == nil {
						t.Fatal("permitted startup did not recover stored text, cards and audio")
					}
					if audio, err := app.GetMobileBriefAudio(brief.BriefID); err != nil || audio.Base64 == "" {
						t.Fatalf("permitted recording unavailable: %v", err)
					}
				}
				if app.isActivityMonitorRunning() || app.isBackgroundListeningRunning() {
					t.Fatal("capture started before optional service startup")
				}
			}
			assertView(false)
			wall := time.Now().UTC()
			clock := &fakeMobileBriefClock{now: wall}
			app.closedEyes.now = clock.Now
			raw := wall.Add(time.Hour).Format(time.RFC3339Nano)
			switch policy {
			case "expired":
				raw = wall.Add(-time.Hour).Format(time.RFC3339Nano)
			case "empty":
				raw = ""
			case "malformed":
				raw = "not-a-deadline"
			}
			if err := app.store.SetUIState(context.Background(), closedEyesResumeAtKey, raw); err != nil {
				t.Fatal(err)
			}
			store := app.store
			if policy == "read_failed" {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			app.store = nil
			ctx, cancel := context.WithCancel(context.Background())
			opened, releaseOpen := make(chan struct{}), make(chan struct{})
			published, environment := make(chan struct{}), make(chan struct{})
			releaseEnvironment := make(chan struct{})
			app.startStorageStartup(ctx, func() (*storage.Store, error) {
				close(opened)
				select {
				case <-releaseOpen:
					return store, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}, func() {
				close(published)
				if app.launch.wait(ctx, time.Hour) {
					close(environment)
					select {
					case <-releaseEnvironment:
					case <-ctx.Done():
					}
				}
			})
			t.Cleanup(func() {
				cancel()
				<-app.storageStartup.done
				app.storageStartup.stop()
				app.stopObservationResumeTimer()
				app.launch.stop()
			})
			<-opened
			if app.GetStorageStartupStatus().Code != "checking" || !app.IsClosedEyesModeEnabled() {
				t.Fatal("unknown policy was published as readable")
			}
			assertView(true)
			start := time.Now()
			close(releaseOpen)
			select {
			case <-published:
			case <-time.After(time.Second):
				t.Fatal("mandatory publication waited for paint or environment discovery")
			}
			if app.GetStorageStartupStatus().Code != "" {
				t.Fatal("storage readiness was not published")
			}
			if app.closedEyes.policyPending.Load() {
				t.Fatal("storage ready with observation policy still unknown")
			}
			withheld := policy != "expired" && policy != "empty"
			if app.IsClosedEyesModeEnabled() != withheld {
				t.Fatal("storage ready before persisted policy was restored")
			}
			at, timed := app.observationResumeAt()
			if policy == "future" || policy == "expires_before_paint" {
				if !timed || at.Format(time.RFC3339Nano) != raw || pauseDeadline(t, app) != raw {
					t.Fatal("startup lost the persisted timed pause deadline")
				}
			} else if timed {
				t.Fatal("startup invented a timed pause")
			}
			assertView(withheld)
			if canonical, ok := svc.store.load(brief.BriefID); !ok || len(canonical.Sections.OpenLoops) == 0 || canonical.Audio == nil {
				t.Fatal("withholding destroyed the canonical stored evidence")
			}
			view, err := app.GetLatestBrief()
			if err != nil || view.Brief == nil || (withheld && (!view.Empty || view.Brief.Audio != nil || len(view.Brief.Cards) > 0)) {
				t.Fatalf("desktop first screen bypassed restored policy: %v", err)
			}
			if time.Since(start) >= time.Second {
				t.Fatal("persisted first screen exceeded its one-second budget")
			}
			t.Logf("policy=%s publication_and_first_screen_ms=%d", policy, time.Since(start).Milliseconds())
			app.closedEyes.mu.Lock()
			armed := app.closedEyes.resumeTimer != nil || app.closedEyes.resumeActivity || app.closedEyes.resumeListening
			app.closedEyes.mu.Unlock()
			if armed {
				t.Fatal("timer or capture resume intent armed before service initialization")
			}
			if policy == "expires_before_paint" {
				clock.Advance(2 * time.Hour)
				app.checkObservationDeadline()
				assertView(true)
			}
			select {
			case <-environment:
				t.Fatal("environment discovery ran before paint")
			default:
			}
			app.launch.firstScreenPainted()
			<-environment
			assertView(withheld)
			// Recovery remains possible while optional environment work is
			// parked, without capture starting before those services exist.
			if policy == "read_failed" {
				home, err := os.UserHomeDir()
				if err != nil {
					t.Fatal(err)
				}
				reopened, err := storage.New(filepath.Join(home, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { reopened.Close() })
				app.store = reopened
			}
			if withheld {
				if err := app.ResumeObservation(); err != nil {
					t.Fatal(err)
				}
				assertView(false)
			}
			if pauseDeadline(t, app) != "" {
				t.Fatal("permitted recovery did not durably clear the pause")
			}
			close(releaseEnvironment)
		})
	}
}

func TestLaunchPolicyDoesNotReplaceManualPauseWithTimer(t *testing.T) {
	app := newTimedPauseFixture(t)
	if err := app.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetUIState(context.Background(), closedEyesResumeAtKey, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	app.restoreObservationPolicy()
	if !app.IsClosedEyesModeEnabled() {
		t.Fatal("restoration ended an explicit manual pause")
	}
	if _, timed := app.observationResumeAt(); timed || pauseDeadline(t, app) != "" {
		t.Fatal("restoration gave a manual pause an auto-resume deadline")
	}
}
