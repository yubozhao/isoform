package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/storage"
)

// The daily real-app test (docs/rework-2026-11.md, step 4) launches a built
// Isoform on a test data folder seeded with a scripted day. TestTrialSeed
// writes that day through the app's own storage code, so the seed keeps up
// with schema changes, and checks that the app reads it back. It runs only
// when ISOFORM_TRIAL_SEED_DIR names a fresh folder, and never on a real data
// folder:
//
//	ISOFORM_TRIAL_SEED_DIR=/path/to/empty go test -run TestTrialSeed -count=1 .
//
// Everything in it is invented: no person, message or brief comes from
// anyone's data.

const trialSeedDirEnv = "ISOFORM_TRIAL_SEED_DIR"

func TestTrialSeed(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv(trialSeedDirEnv))
	if dir == "" {
		t.Skip(trialSeedDirEnv + " not set")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := trialSeedRefuse(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YANSU_AGENT_HOME", dir)
	t.Setenv("YANSU_MOBILE_STATE_DIR", "")
	now := time.Now()
	ctx := context.Background()

	store, err := storage.New(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &App{store: store, activeTags: map[string]bool{}}

	// Setup is done: the app opens straight on Today, and the menu bar mark shows.
	for key, value := range map[string]string{
		onboardingStateKey:    `{"completed":true}`,
		setupWatchingStateKey: "1",
	} {
		if err := store.SetUIState(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}

	// Chat answers with the computer's own Claude Code (signed in on the test machine).
	if err := saveAIProviderConfig(AIProviderConfig{Provider: "claude-code"}); err != nil {
		t.Fatal(err)
	}

	// You and two people you know.
	for _, p := range []storage.PrincipalRow{
		{Kind: storage.PrincipalKindSelf, DisplayName: "Alex Trial"},
		{Kind: storage.PrincipalKindContact, DisplayName: "Priya Raman", Relationship: "colleague"},
		{Kind: storage.PrincipalKindContact, DisplayName: "Maya Chen", Relationship: "colleague"},
	} {
		if _, err := store.CreatePrincipal(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	// The main chat with one earlier exchange.
	main, err := app.ensureMainChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []storage.StreamItemRow{
		{Kind: "user", Content: "What is on my plate this afternoon?"},
		{Kind: "assistant", Content: "Two things: review the Halcyon launch memo, then the 3 PM check-in with Maya."},
	} {
		row.ID = uuid.NewString()
		if _, err := store.AppendStreamItem(ctx, main.LocalID, row); err != nil {
			t.Fatal(err)
		}
	}

	// The morning brief is due three minutes from now, so the launched app
	// writes, seals and records it on its own schedule and the test then
	// plays it. A brief written here would not carry the sealed sources the
	// brief read requires, and the app would withhold it. The schedule
	// counts as turned on now, so no slot missed before the seed is made up.
	settings := defaultMobileBriefSettings()
	settings.Morning = now.Add(3 * time.Minute).Format("15:04")
	settings.Adaptive = false
	settings.EnabledAt = now.Format(time.RFC3339)
	if err := newMobileBriefConfigStore(filepath.Join(dir, "mobile")).save(settings); err != nil {
		t.Fatal(err)
	}
	wav := trialSeedTone(4)
	sum := sha256.Sum256(wav)

	// A recording from the watch, listed under Activity > From your watch. A
	// phone identity with the relay off makes the app open its phone bridge
	// at launch without reaching any relay.
	mobileDir := filepath.Join(dir, "mobile")
	if _, err := createMobileHubIdentity(mobileDir); err != nil {
		t.Fatal(err)
	}
	recordings := newMobileRecordingStore(mobileDir)
	watchID := uuid.NewString()
	watchStart := now.Add(-90 * time.Minute)
	if err := os.MkdirAll(filepath.Join(recordings.dir, watchID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recordings.dir, watchID, "part-0.wav"), wav, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := mobileTimestamp(watchStart.Add(5 * time.Minute))
	recordings.mu.Lock()
	recordings.recordings = map[string]*mobileRecording{watchID: {
		ID: watchID, DeviceID: uuid.NewString(), Source: "watch", Mime: mobileBriefAudioMimeWAV, Parts: 1,
		Received: []mobileRecordingPart{{Part: 0, UploadID: uuid.NewString(), File: "part-0.wav", Bytes: int64(len(wav)),
			SHA256: hex.EncodeToString(sum[:]), StartedAt: mobileTimestamp(watchStart), DurationSeconds: 4}},
		State: mobileRecordingTranscribed, ReceivedAt: stamp, UpdatedAt: stamp,
	}}
	recordings.loaded = true
	err = recordings.saveLocked()
	recordings.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	// The app reads the day back the way it will at launch.
	if !mobileStateExists(mobileDir) {
		t.Fatal("the phone state does not read as present")
	}
	if got, err := newMobileRecordingStore(mobileDir).all(); err != nil || len(got) != 1 {
		t.Fatalf("the watch recording does not read back: %d %v", len(got), err)
	}
	if raw, err := store.GetUIState(ctx, onboardingStateKey); err != nil || !onboardingStateDone(raw) {
		t.Fatalf("setup does not read as done: %q %v", raw, err)
	}
	t.Logf("trial seed written to %s: main chat %s, watch recording %s", dir, main.LocalID, watchID)
}

// trialSeedRefuse keeps the seed off any folder that holds someone's data.
func trialSeedRefuse(dir string) error {
	if home, err := os.UserHomeDir(); err == nil {
		for _, real := range []string{".isoform-agent", ".yansu-agent"} {
			if dir == filepath.Join(home, real) || strings.HasPrefix(dir, filepath.Join(home, real)+string(os.PathSeparator)) {
				return &trialSeedError{"refusing to seed a real data folder: " + dir}
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) > 0 {
		return &trialSeedError{"refusing to seed a folder that is not empty: " + dir}
	}
	return nil
}

type trialSeedError struct{ msg string }

func (e *trialSeedError) Error() string { return e.msg }

// trialSeedTone is a quiet 440 Hz tone, seconds long, as a mono 16 kHz WAV.
func trialSeedTone(seconds int) []byte {
	const rate = 16000
	samples := make([]float32, rate*seconds)
	for i := range samples {
		samples[i] = float32(0.05 * math.Sin(2*math.Pi*440*float64(i)/rate))
	}
	return encodeWAV(samples, rate)
}
