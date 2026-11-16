package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/handoff"

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

// trialSeedBriefWords opens the seeded brief; the daily check looks for it on
// Today (frontend/e2e/daily-real-app.spec.ts).
const trialSeedBriefWords = "Daily test brief: the Halcyon review is the first thing today."

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

	// An earlier brief today, built with its words and recording, then saved
	// again without them, as the live refresh did on 2026-11-15 (its words
	// cited five calendar events). Today must still show its words and play
	// it: once built, the latest brief always shows (mobile_brief_show_built.go).
	briefs := newMobileBriefStore(mobileDir)
	if err := os.MkdirAll(briefs.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Yesterday's evening brief: a brief from today would stand in for the
	// slot seeded three minutes after launch, and the scheduled brief (the
	// brief-audio check) would never be made.
	y := now.AddDate(0, 0, -1)
	builtAt := time.Date(y.Year(), y.Month(), y.Day(), 18, 0, 0, 0, time.Local)
	in := mobileBriefInput{AsOf: builtAt, WindowStart: builtAt.Add(-2 * time.Hour), Slot: mobileBriefSlotEvening, Location: time.Local, Timezone: mobileBriefTimezoneName(time.Local)}
	for i := range 5 {
		in.Meetings = append(in.Meetings, mobileBriefCandidate{SourceKind: mobileBriefCalendarSource, SourceID: "trial-event-" + strconv.Itoa(i), Title: "Halcyon review", At: builtAt.Add(-time.Duration(i+1) * time.Minute)})
	}
	written := buildMobileBriefV1(in)
	var calendar []string
	for _, ref := range written.Evidence {
		if ref.SourceKind == mobileBriefCalendarSource {
			calendar = append(calendar, ref.Ref)
		}
	}
	written.Narrative = []MobileBriefPara{
		{Text: trialSeedBriefWords, Refs: calendar},
		{Text: "Maya is waiting on the launch memo before the check-in.", Refs: calendar[:1]},
	}
	written.NarrativeStatus = "ok"
	briefWav := trialSeedTone(3)
	briefSum := sha256.Sum256(briefWav)
	written.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mobileBriefAudioMimeWAV, Bytes: len(briefWav), SHA256: hex.EncodeToString(briefSum[:]), DurationSeconds: 3}
	written.Audio.binding = mobileBriefAudioBinding(written, "en")
	if err := briefs.saveBuilt(written); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(briefs.dir, written.BriefID+mobileBriefAudioExtWAV), briefWav, 0o600); err != nil {
		t.Fatal(err)
	}
	stripped := written
	stripped.Narrative, stripped.NarrativeStatus, stripped.Audio = nil, "unavailable", nil
	if err := briefs.save(stripped, builtAt); err != nil {
		t.Fatal(err)
	}

	// Work the day's scheduled brief can write about: two tasks Isoform
	// finished today and one waiting for review. Without material the writer
	// writes a quiet brief with no words and no recording, and the brief-audio
	// check has nothing to play.
	work, err := handoff.NewStore(filepath.Join(dir, "handoff"))
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range []handoff.Entry{
		{ID: "trial-done-memo", Title: "Drafted the Halcyon launch memo", Source: handoff.SourceDesktop, Status: handoff.StatusCompleted},
		{ID: "trial-done-notes", Title: "Summarized the design review notes", Source: handoff.SourceDesktop, Status: handoff.StatusCompleted},
		{ID: "trial-review-plan", Title: "Launch plan for Priya", Source: handoff.SourceMobile, Status: handoff.StatusNeedsReview},
	} {
		at := now.Add(-time.Duration(3-i) * time.Hour).UnixMilli()
		entry.CreatedAt, entry.UpdatedAt = at-int64(time.Hour/time.Millisecond), at
		if _, err := work.Save(entry); err != nil {
			t.Fatal(err)
		}
	}

	// One meeting still ahead today, for the brief's next part.
	meetingAt := now.Add(2 * time.Hour)
	if err := store.UpsertCalendarEvents(ctx, []storage.CalendarEventRow{{
		ID: "trial-meeting", UID: "trial-meeting", CalendarID: "trial", CalendarTitle: "Work", Source: "eventkit",
		Title: "Design review with Maya", StartAt: meetingAt.UnixMilli(), EndAt: meetingAt.Add(45 * time.Minute).UnixMilli(),
		TimeZone: now.Location().String(),
	}}, now.UnixMilli()); err != nil {
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
	if got, ok := briefs.loadBuilt(written.BriefID); !ok || len(got.Narrative) != 2 || got.Audio == nil {
		t.Fatal("the seeded brief's build does not read back")
	}
	t.Logf("trial seed written to %s: main chat %s, watch recording %s, stripped brief %s", dir, main.LocalID, watchID, written.BriefID)
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
