package main

import (
	"encoding/json"
	"log"
	"time"

	"wails-gui/models"
)

// Setup step 1, short of disk (computer conformance answers v1.1.0 S9): with
// Microphone allowed and no room for speech recognition, the download starts
// by itself once there is room. Setup re-plans every minute while it is open;
// after that this file checks at each launch and then hourly. The frontend
// saves the request it planned under setupSpeechWaitingKey and clears it once
// the download starts.

const (
	// setupSpeechWaitingKey holds a speechWaitingRequest while speech
	// recognition waits for disk; empty when nothing waits.
	setupSpeechWaitingKey = "isoform.setup.speech-waiting"
	// speechWaitingPoll is how often a waiting download is retried after launch.
	speechWaitingPoll = time.Hour
)

// speechWaitingRequest is the plan request setup made: the same choices and
// skipped jobs, so the retry downloads exactly what Microphone said yes to.
type speechWaitingRequest struct {
	Choices map[string]string `json:"choices"`
	Skip    []string          `json:"skip"`
}

// speechModelPlanner is the part of the model manager the retry needs.
type speechModelPlanner interface {
	BuildPlanSkipping(choices map[string]string, overrides map[string]string, skipJobs []string) (models.Plan, error)
	Start(p models.Plan) error
	Downloads() []models.Download
}

// startWaitingSpeechModels starts a waiting speech download when it fits, and
// forgets the request once it started, is already running or has nothing left
// to fetch. It reports whether the request was settled.
func startWaitingSpeechModels(m speechModelPlanner, load func(string) (string, error), save func(string, string) error) bool {
	raw, err := load(setupSpeechWaitingKey)
	if err != nil || raw == "" {
		return false
	}
	var req speechWaitingRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		_ = save(setupSpeechWaitingKey, "")
		return true
	}
	plan, err := m.BuildPlanSkipping(req.Choices, nil, req.Skip)
	if err != nil {
		log.Printf("[setup] speech recognition retry: plan failed: %v", err)
		return false
	}
	settled := len(plan.Items) == 0
	if !settled {
		active := map[string]bool{}
		for _, d := range m.Downloads() {
			if !d.Deferred && d.State != "failed" && d.State != "done" {
				active[d.ID] = true
			}
		}
		settled = true
		for _, item := range plan.Items {
			if !active[item.ID] {
				settled = false
				break
			}
		}
	}
	if !settled {
		if !plan.EnoughSpace {
			return false
		}
		if err := m.Start(plan); err != nil {
			log.Printf("[setup] speech recognition retry: start failed: %v", err)
			return false
		}
		settled = true
	}
	if err := save(setupSpeechWaitingKey, ""); err != nil {
		log.Printf("[setup] speech recognition retry: clear failed: %v", err)
	}
	return settled
}

// retryWaitingSpeechModels checks once now (each launch), then every `every`
// until the manager stops. It keeps ticking with nothing waiting, because
// setup can save a request after launch; each tick is one ui_state read.
func retryWaitingSpeechModels(manager *models.Manager, every time.Duration, load func(string) (string, error), save func(string, string) error) {
	startWaitingSpeechModels(manager, load, save)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-manager.Done():
			return
		case <-ticker.C:
			startWaitingSpeechModels(manager, load, save)
		}
	}
}

func (a *App) watchWaitingSpeechModels(manager *models.Manager) {
	go retryWaitingSpeechModels(manager, speechWaitingPoll, a.LoadUIState, a.SaveUIState)
}
