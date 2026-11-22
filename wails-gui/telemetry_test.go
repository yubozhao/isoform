package main

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/posthog/posthog-go"
)

// recordingPostHogClient keeps every message instead of sending it.
type recordingPostHogClient struct {
	posthog.Client
	mu       sync.Mutex
	messages []posthog.Message
}

func (c *recordingPostHogClient) Enqueue(message posthog.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, message)
	return nil
}

func (c *recordingPostHogClient) Close() error { return nil }

func (c *recordingPostHogClient) CloseWithContext(context.Context) error { return nil }

func (c *recordingPostHogClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.messages)
}

func newRecordingTelemetry(t *testing.T) (*PostHogTelemetry, *recordingPostHogClient) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "telemetry-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	telemetryTierMu.Lock()
	previous := telemetryTier
	telemetryTier = TelemetryTierDetailed
	telemetryTierMu.Unlock()
	t.Cleanup(func() {
		telemetryTierMu.Lock()
		telemetryTier = previous
		telemetryTierMu.Unlock()
	})
	client := &recordingPostHogClient{}
	telemetry := &PostHogTelemetry{client: client}
	telemetry.enabled.Store(true)
	return telemetry, client
}

// With "Share diagnostic data" off, no event and no identify leaves the Mac.
func TestTelemetrySendsNothingWhenDiagnosticDataIsOff(t *testing.T) {
	telemetry, client := newRecordingTelemetry(t)
	app := &App{telemetry: telemetry}
	if err := app.SetTelemetryTier(TelemetryTierOff); err != nil {
		t.Fatal(err)
	}

	telemetry.Capture("chat.turn_sent", map[string]any{"points_count": 3})
	telemetry.Capture("app.open", nil)

	if got := client.count(); got != 0 {
		t.Fatalf("diagnostic data off sent %d messages, want 0", got)
	}

	// The stored choice holds after a restart.
	telemetryTierMu.Lock()
	telemetryTier = ""
	telemetryTierMu.Unlock()
	store, err := openStartupStorage()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app.restoreSettingsDefaults(context.Background(), store)
	telemetry.Capture("iphone.paired", nil)
	if got := client.count(); got != 0 {
		t.Fatalf("diagnostic data off after reload sent %d messages, want 0", got)
	}
}

func TestTelemetrySendsWhenDiagnosticDataIsOn(t *testing.T) {
	telemetry, client := newRecordingTelemetry(t)
	app := &App{telemetry: telemetry}
	if err := app.SetTelemetryTier(TelemetryTierDetailed); err != nil {
		t.Fatal(err)
	}

	telemetry.Capture("chat.turn_sent", map[string]any{"points_count": 3})

	if got := client.count(); got != 1 {
		t.Fatalf("diagnostic data on sent %d messages, want one anonymous capture", got)
	}
}
