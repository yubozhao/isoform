package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"wails-gui/models"
)

func (a *App) setupModelManager() (*models.Manager, error) {
	a.setupModelsOnce.Do(func() {
		dir := configDirOrFallback()
		folder := filepath.Join(dir, "models")
		a.setupModels, a.setupModelsErr = models.New(folder, dir, models.Catalog(), func(path string) (int64, error) {
			// Stat the nearest existing parent; planning does not create folders.
			for {
				if _, err := os.Stat(path); err == nil {
					break
				}
				parent := filepath.Dir(path)
				if parent == path {
					return 0, fmt.Errorf("no disk for %s", path)
				}
				path = parent
			}
			n, err := freeDiskBytes(path)
			return int64(n), err
		}, func(progress []models.Download) { a.emitBrowserEvent("models:downloads", progress) })
		if a.setupModelsErr == nil {
			a.setupModels.SetDownloadComplete(a.onModelDownloadComplete)
		}
		if a.setupModelsErr == nil && !a.setupModels.HasSavedChoices() && loadConfig().AfterCallSpeechModelOff {
			a.setupModelsErr = a.setupModels.SetAfterCallEnabled(false)
		}
	})
	return a.setupModels, a.setupModelsErr
}

func (a *App) modelDownloadCompleted(ctx context.Context, entry models.Entry, initialize func() error, retry func(context.Context)) {
	switch entry.Job {
	case "privacy":
		am := a.activity
		if am == nil || ctx.Err() != nil || a.shouldDiscardPIIOutput(am) {
			return
		}
		if ready, _ := am.piiDetectorState(); ready {
			go a.releasePrivacyHolds(am)
			return
		}
		am.piiDetectorMu.Lock()
		am.piiInitRetryAfter = time.Time{}
		am.piiArrivalPending = am.piiInitInFlight
		am.piiDetectorMu.Unlock()
		a.startPIIDetectorAsync(am)
		return
	case "live", "afterCall", "speakerPack": // Silero VAD ships with the speaker pack.
	default:
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err := initialize(); err != nil {
		log.Printf("[speech-model-arrival] %s: %v", entry.ID, err)
		return
	}
	// The manager joins initialization at shutdown. Recovery belongs to the
	// app and may still be decoding a long call when the manager closes.
	if appCtx := a.ctx; appCtx != nil {
		go retry(appCtx)
	}
}

func (a *App) retryWaitingSpeech(ctx context.Context) {
	if ctx.Err() != nil || a.IsClosedEyesModeEnabled() || a.speechTranscriber() == nil {
		return
	}
	a.recoverUntranscribedMeetingsWithContext(ctx)
	if ctx.Err() != nil {
		return
	}
	bridge := a.mobileBridgeForUse()
	if bridge == nil || !bridge.trackBackground() {
		return
	}
	defer bridge.background.Done()
	bridge.mu.Lock()
	store := bridge.recordingStoreLocked()
	bridge.mu.Unlock()
	if store != nil {
		workerCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(bridge.ctx, cancel)
		defer stop()
		a.retryMobileRecordings(workerCtx, store, false)
	}
}

func (a *App) GetModelChoices() (models.Choices, error) {
	m, err := a.setupModelManager()
	if err != nil {
		return models.Choices{}, err
	}
	return m.GetChoices()
}
func (a *App) SetModelChoice(job, id string) error {
	m, err := a.setupModelManager()
	if err != nil {
		return err
	}
	return m.SetChoice(job, id)
}
func (a *App) PlanModelDownload(choices map[string]string, sourceOverrides map[string]string) (models.Plan, error) {
	m, err := a.setupModelManager()
	if err != nil {
		return models.Plan{}, err
	}
	return m.BuildPlan(choices, sourceOverrides)
}
func (a *App) StartModelDownloads(plan models.Plan) error {
	m, err := a.setupModelManager()
	if err != nil {
		return err
	}
	m.MonitorNetwork()
	return m.Start(plan)
}
func (a *App) GetModelDownloads() ([]models.Download, error) {
	m, err := a.setupModelManager()
	if err != nil {
		return nil, err
	}
	return m.Downloads(), nil
}
func (a *App) RetryModelDownload(id string) error {
	m, err := a.setupModelManager()
	if err != nil {
		return err
	}
	m.MonitorNetwork()
	return m.Retry(id)
}
func (a *App) RemoveModel(id string) error {
	m, err := a.setupModelManager()
	if err != nil {
		return err
	}
	return m.Remove(id)
}
func (a *App) GetModelEvaluation() string { return models.Evaluation() }

// ResumeModelDownload rechecks the current path and shared disk reservation;
// it never overrides a metered connection or grants a new model download.
func (a *App) ResumeModelDownload(id string) error {
	m, err := a.setupModelManager()
	if err != nil {
		return err
	}
	m.MonitorNetwork()
	return m.ResumeDownload(id)
}

func (a *App) GetModelNetworkState() (models.NetworkState, error) {
	m, err := a.setupModelManager()
	if err != nil {
		return models.NetworkState{}, err
	}
	return m.NetworkSnapshot(), nil
}
