package main

import (
	"errors"
	"testing"

	"wails-gui/models"
)

type fakeSpeechPlanner struct {
	plan      models.Plan
	planErr   error
	downloads []models.Download
	started   []models.Plan
	startErr  error
	gotSkip   []string
}

func (f *fakeSpeechPlanner) BuildPlanSkipping(_ map[string]string, _ map[string]string, skip []string) (models.Plan, error) {
	f.gotSkip = skip
	return f.plan, f.planErr
}
func (f *fakeSpeechPlanner) Start(p models.Plan) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, p)
	return nil
}
func (f *fakeSpeechPlanner) Downloads() []models.Download { return f.downloads }

func speechWaitingState(value string) (map[string]string, func(string) (string, error), func(string, string) error) {
	state := map[string]string{setupSpeechWaitingKey: value}
	return state,
		func(k string) (string, error) { return state[k], nil },
		func(k, v string) error { state[k] = v; return nil }
}

const waitingSpeech = `{"choices":{"live":"sensevoice"},"skip":["privacy","memory"]}`

func TestWaitingSpeechModelsStartOnceThereIsRoom(t *testing.T) {
	item := models.PlanItem{ID: "sensevoice", Bytes: 100}
	f := &fakeSpeechPlanner{plan: models.Plan{Items: []models.PlanItem{item}, EnoughSpace: false}}
	state, load, save := speechWaitingState(waitingSpeech)

	if startWaitingSpeechModels(f, load, save) {
		t.Fatal("settled with no room")
	}
	if len(f.started) != 0 || state[setupSpeechWaitingKey] == "" {
		t.Fatalf("short of disk: started %d, waiting %q; want nothing started and the request kept", len(f.started), state[setupSpeechWaitingKey])
	}
	if len(f.gotSkip) != 2 || f.gotSkip[0] != "privacy" || f.gotSkip[1] != "memory" {
		t.Fatalf("skip = %v, want the request's [privacy memory]", f.gotSkip)
	}

	f.plan.EnoughSpace = true
	if !startWaitingSpeechModels(f, load, save) {
		t.Fatal("not settled once there was room")
	}
	if len(f.started) != 1 || state[setupSpeechWaitingKey] != "" {
		t.Fatalf("with room: started %d, waiting %q; want one start and the request cleared", len(f.started), state[setupSpeechWaitingKey])
	}
}

func TestWaitingSpeechModelsNeverStartTwice(t *testing.T) {
	f := &fakeSpeechPlanner{
		plan:      models.Plan{Items: []models.PlanItem{{ID: "sensevoice"}}, EnoughSpace: true},
		downloads: []models.Download{{ID: "sensevoice", State: "downloading"}},
	}
	state, load, save := speechWaitingState(waitingSpeech)
	if !startWaitingSpeechModels(f, load, save) || len(f.started) != 0 || state[setupSpeechWaitingKey] != "" {
		t.Fatalf("already downloading: started %d, waiting %q", len(f.started), state[setupSpeechWaitingKey])
	}
}

func TestWaitingSpeechModelsKeepTheRequestWhenStartFails(t *testing.T) {
	f := &fakeSpeechPlanner{plan: models.Plan{Items: []models.PlanItem{{ID: "sensevoice"}}, EnoughSpace: true}, startErr: models.ErrNoSpace}
	state, load, save := speechWaitingState(waitingSpeech)
	if startWaitingSpeechModels(f, load, save) || state[setupSpeechWaitingKey] == "" {
		t.Fatal("a failed start must keep waiting")
	}
	f.planErr, f.startErr = errors.New("catalog"), nil
	if startWaitingSpeechModels(f, load, save) || state[setupSpeechWaitingKey] == "" {
		t.Fatal("a failed plan must keep waiting")
	}
}

func TestWaitingSpeechModelsDoNothingWhenNothingWaits(t *testing.T) {
	f := &fakeSpeechPlanner{plan: models.Plan{Items: []models.PlanItem{{ID: "sensevoice"}}, EnoughSpace: true}}
	_, load, save := speechWaitingState("")
	if startWaitingSpeechModels(f, load, save) || len(f.started) != 0 {
		t.Fatal("started with nothing waiting")
	}
}
