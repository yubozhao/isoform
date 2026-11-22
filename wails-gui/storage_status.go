package main

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/platform/shellenv"
	"wails-gui/storage"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// GetStorageStartupStatus is available before background services start.
func (a *App) GetStorageStartupStatus() StorageStartupStatus {
	if a.storageStartup == nil {
		return StorageStartupStatus{}
	}
	return a.storageStartup.snapshot()
}

func (a *App) beginStorageStartup(ctx context.Context) {
	a.startStorageStartup(ctx, openStartupStorage, func() {
		// The menu bar's whole view, built once after the first screen.
		a.preloadMenuBarView(ctx)
		if !a.launch.wait(ctx, 2*time.Second) {
			return
		}
		recordLaunchStage("shell_env_start")
		shellenv.ResolveStartupPATH()
		syncLocalClaudeCodeEnvFromLoginShell()
		recordLaunchStage("shell_env_ready")
		if ctx.Err() != nil || a.launch.isStopped() {
			return
		}
		if a.launch != nil {
			a.launch.envReady.Store(true)
		}
		a.emitBrowserEvent("ai:config-changed")
		a.launch.startWorker(ctx)
		a.startupServices(ctx)
	})
}

// Keep policy restoration in the publication lifecycle, before readiness and
// independently of paint, environment discovery or optional service startup.
func (a *App) startStorageStartup(ctx context.Context, open func() (*storage.Store, error), startServices func()) {
	a.storageStartup.start(func() (*storage.Store, error) {
		a.closedEyes.policyPending.Store(true)
		store, err := open()
		if err != nil {
			return nil, err
		}
		a.restoreSettingsDefaults(ctx, store)
		return store, nil
	}, func(store *storage.Store) {
		a.store = store
		telemetryTierSaveMu.Lock()
		a.telemetry.SetSharingEnabled(telemetrySharingOn())
		telemetryTierSaveMu.Unlock()
		a.telemetry.Capture("app.open", nil)
		recordLaunchStage("telemetry_ready")
		a.restoreWatchContextChoices()
		recordLaunchStage("observation_policy_start")
		briefPolicyPending.Store(true)
		a.restoreObservationPolicy()
		briefObservationPolicyLoaded()
		recordLaunchStage("observation_policy_ready")
		// These policies and local ledgers are required to authorize persisted
		// first-screen records. Models, capture and source refresh are not.
		a.loadUserSensitiveApps()
		a.loadUserVoiceInputApps()
		recordLaunchStage("activity_read_open_start")
		a.prepareActivityReads()
		recordLaunchStage("activity_read_open_done")
		if root, err := flavorDataDir(); err == nil {
			if hs, err := handoff.NewStore(filepath.Join(root, "handoff")); err == nil {
				a.handoffStore = hs
			} else {
				log.Printf("[handoff] store init failed: %v", err)
			}
		}
		// Restore before the main UI mounts and saves its initial window size.
		a.restoreWindowPosition(ctx)
	}, startServices)
}

func openStartupStorage() (*storage.Store, error) {
	recordLaunchStage("storage_open_start")
	defer recordLaunchStage("storage_open_done")
	home, err := flavorDataDir()
	if err != nil {
		log.Printf("[persistence] data directory failed: %v", err)
		return nil, err
	}
	store, err := storage.New(filepath.Join(home, "sessions.db"))
	if err != nil {
		log.Printf("[persistence] startup blocked: %v", err)
		return nil, err
	}
	return store, nil
}

func repairStartupStorage(store *storage.Store) {
	// A prior crash may have left a People dossier stuck in research.
	if n, err := store.ResetStaleResearch(context.Background()); err != nil {
		log.Printf("[people] reset stale research failed: %v", err)
	} else if n > 0 {
		log.Printf("[people] reset %d stale researching dossier(s) from a prior run", n)
	}
	// Runs cut off by a quit or crash would otherwise read "running" forever.
	if n, err := store.EndInterruptedAgentRunRoutes(context.Background()); err != nil {
		log.Printf("[routing] end interrupted runs failed: %v", err)
	} else if n > 0 {
		log.Printf("[routing] ended %d run(s) left running by a prior launch", n)
	}
}

func (a *App) requestQuit() {
	if a.storageStartup != nil {
		a.storageStartup.requestQuit()
	}
	runtime.Quit(a.ctx)
}

func (a *App) beforeWindowClose(ctx context.Context, platform string) bool {
	if a.GetStorageStartupStatus().Code != "" {
		return false
	}
	a.captureAndSaveWindowState(ctx)
	// macOS installs its native close-to-menu-bar hook after storage opens.
	// On Windows, Wails uses the same callback for X and runtime.Quit; the
	// explicit quit action distinguishes leaving the app from hiding it.
	if platform == "windows" && a.storageStartup != nil && a.storageStartup.keepRunningOnClose() {
		runtime.WindowHide(ctx)
		return true
	}
	return false
}
