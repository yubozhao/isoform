package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/models"
)

func TestDeferredModelReadyNeedsPowerAndIdle(t *testing.T) {
	plugged := activitymon.PowerState{Known: true, Percent: 80}
	battery := activitymon.PowerState{Known: true, OnBattery: true, Percent: 80}
	for _, c := range []struct {
		name  string
		power activitymon.PowerState
		idle  time.Duration
		want  bool
	}{
		{"plugged and idle", plugged, deferredModelIdle, true},
		{"plugged and busy", plugged, time.Minute, false},
		{"battery and idle", battery, time.Hour, false},
		{"unknown power", activitymon.PowerState{Percent: -1}, time.Hour, false},
	} {
		if got := deferredModelReady(c.power, c.idle); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func TestReleaseDeferredModelsWaitsForReady(t *testing.T) {
	data := []byte("a real fixture model\n")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	manager := deferredModelFixture(t, server.URL, data)
	plan, err := manager.BuildPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Defer(plan); err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	go releaseDeferredModels(manager, time.Millisecond, ready.Load)
	time.Sleep(30 * time.Millisecond)
	if requests.Load() != 0 || !manager.HasDeferred() {
		t.Fatal("released before power and idle")
	}
	ready.Store(true)
	deadline := time.After(5 * time.Second)
	for {
		if jobs := manager.Downloads(); len(jobs) == 1 && jobs[0].State == "done" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("not released: %+v", manager.Downloads())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func deferredModelFixture(t *testing.T, url string, data []byte) *models.Manager {
	t.Helper()
	n := int64(len(data))
	sum := sha256Hex(data)
	entry := models.Entry{ID: "fixture", Job: "live", DisplayName: "Fixture", Licence: models.Licence{Name: "MIT"}, Recommended: true, SizeBytes: &n,
		Files:   []models.File{{Path: "fixture/model.bin", SHA256: &sum, Bytes: &n, URLs: []string{url}}},
		Sources: []models.Source{{Kind: "mirror", URL: "https://model-assets.yansu.app/"}}}
	dir := t.TempDir()
	manager, err := models.New(filepath.Join(dir, "models"), dir, []models.Entry{entry}, func(string) (int64, error) { return 1000, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	manager.SetNetworkState(models.NetworkState{Known: true})
	return manager
}

// The popover's "Downloading models · {percent}%" line (setup v2.2.0 §2):
// shown while confirmed models download, never for those waiting on Later.
func TestMenuBarModelsLine(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	manager := deferredModelFixture(t, server.URL, []byte("a real fixture model\n"))
	app := &App{}
	app.setupModelsOnce.Do(func() { app.setupModels = manager })
	if got := app.menuBarModels(menuBarWords("en")); got != "" {
		t.Fatalf("nothing confirmed: %q", got)
	}
	plan, err := manager.BuildPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Defer(plan); err != nil {
		t.Fatal(err)
	}
	if got := app.menuBarModels(menuBarWords("en")); got != "" {
		t.Fatalf("deferred job shown: %q", got)
	}
	manager.ReleaseDeferred()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("release did not start the download")
	}
	if got := app.menuBarModels(menuBarWords("en")); got != "Downloading models · 0%" {
		t.Fatalf("en: %q", got)
	}
	if got := app.menuBarModels(menuBarWords("zh-CN")); got != "正在下载模型 · 0%" {
		t.Fatalf("zh-CN: %q", got)
	}
}
