package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
	"modernc.org/sqlite"
)

func TestTelemetryBusyStoreDoesNotBlockCaptureOrConsent(t *testing.T) {
	telemetry, _ := newRecordingTelemetry(t)
	entered, release := make(chan struct{}), make(chan struct{})
	function := "telemetry_hold_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	// A trigger occupies the store's sole connection exactly as a slow
	// background write would, without depending on SQLite timing or internals.
	if err := sqlite.RegisterScalarFunction(function, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(entered)
		<-release
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.telemetry = telemetry
	t.Cleanup(app.telemetry.Close)
	// Storage opens after Wails starts now; open it here as startup would.
	store, err := openStartupStorage()
	if err != nil {
		t.Fatal(err)
	}
	app.store = store
	t.Cleanup(func() { _ = app.store.Close() })
	db, err := sql.Open("sqlite", filepath.Join(configDirOrFallback(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER telemetry_hold BEFORE INSERT ON ui_state WHEN NEW.key = 'telemetry.test.hold' BEGIN SELECT " + function + "(); END"); err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	var operations sync.WaitGroup
	go func() { writeDone <- app.store.SetUIState(context.Background(), "telemetry.test.hold", "1") }()
	defer func() {
		close(release)
		if err := <-writeDone; err != nil {
			t.Errorf("background write: %v", err)
		}
		operations.Wait()
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("background write did not acquire the connection")
	}
	captured := make(chan struct{})
	operations.Add(1)
	go func() {
		defer operations.Done()
		app.telemetry.Capture("chat.turn_sent", nil)
		close(captured)
	}()
	select {
	case <-captured:
	case <-time.After(500 * time.Millisecond):
		t.Error("capture blocked on the busy store")
	}
	tierRead := make(chan struct{})
	operations.Add(1)
	go func() {
		defer operations.Done()
		_ = currentTelemetryTier()
		close(tierRead)
	}()
	optedOut := make(chan error, 1)
	operations.Add(1)
	go func() {
		defer operations.Done()
		optedOut <- app.SetTelemetryTier(TelemetryTierOff)
	}()
	select {
	case <-tierRead:
	case <-time.After(500 * time.Millisecond):
		t.Error("tier read blocked behind telemetry metadata")
	}
	select {
	case err := <-optedOut:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("opt-out blocked behind telemetry metadata")
	}
}

func TestTelemetryLocaleTracksPersistedLanguage(t *testing.T) {
	telemetry, _ := newRecordingTelemetry(t)
	app := NewApp()
	app.telemetry = telemetry
	// Storage opens after Wails starts now; open it here as startup would.
	store, err := openStartupStorage()
	if err != nil {
		t.Fatal(err)
	}
	app.store = store
	t.Cleanup(func() { _ = store.Close() })
	if err := app.SaveUIState(uiLanguageStateKey, "zh-CN"); err != nil {
		t.Fatal(err)
	}
	app.telemetry.Close()
	_ = app.store.Close()
	app = NewApp()
	recorder := &recordingPostHogClient{}
	app.telemetry = &PostHogTelemetry{client: recorder}
	app.telemetry.enabled.Store(true)
	t.Cleanup(app.telemetry.Close)
	store, err = openStartupStorage()
	if err != nil {
		t.Fatal(err)
	}
	app.store = store
	// startupServices re-reads the UI language once the store is open.
	app.telemetry.setLocale(app.currentUILanguage())
	t.Cleanup(func() { _ = app.store.Close() })
	app.telemetry.Capture("app.open", nil)
	if err := app.SaveUIState(uiLanguageStateKey, "en"); err != nil {
		t.Fatal(err)
	}
	app.telemetry.Capture("chat.turn_sent", nil)
	_ = app.store.Close()
	if err := app.SaveUIState(uiLanguageStateKey, "zh-CN"); err == nil {
		t.Fatal("saving to a closed store unexpectedly succeeded")
	}
	app.telemetry.Capture("iphone.paired", nil)
	app.telemetry.Close()
	events := recorder.messages
	if len(events) != 3 {
		t.Fatalf("received %d events, want 3", len(events))
	}
	for i, want := range []string{"zh-CN", "en", "en"} {
		if got := events[i].(posthog.Capture).Properties["locale"]; got != want {
			t.Errorf("event %d locale = %v, want %s", i, got, want)
		}
	}
}

func TestTelemetryNormalizesTrailingHostSlash(t *testing.T) {
	app, _ := newHTTPTelemetry(t, TelemetryTierDetailed)
	app.telemetry.SetSharingEnabled(false)
	paths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	DefaultDesktopPostHogHost = server.URL + "/"
	_, app.telemetry.config.Endpoint = envPostHogConfig()
	app.telemetry.SetSharingEnabled(true)
	app.telemetry.Capture("app.open", nil)
	app.telemetry.Close()
	select {
	case path := <-paths:
		if path != "/batch/" {
			t.Fatalf("batch path = %q, want /batch/", path)
		}
	default:
		t.Fatal("no batch received")
	}
}
