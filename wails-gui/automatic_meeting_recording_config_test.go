package main

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/meetingdetect"
)

func TestAutomaticMeetingRecordingDefaultsEnabled(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	app := &App{}
	if !app.GetAutomaticMeetingRecordingEnabled() {
		t.Fatal("automatic meeting recording should default to enabled")
	}
}

func TestSetAutomaticMeetingRecordingEnabledPersistsPreference(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}

	if err := app.SetAutomaticMeetingRecordingEnabled(false); err != nil {
		t.Fatalf("SetAutomaticMeetingRecordingEnabled(false): %v", err)
	}
	if app.GetAutomaticMeetingRecordingEnabled() {
		t.Fatal("runtime preference stayed enabled after disabling")
	}
	if cfg := loadConfig(); !cfg.AutomaticMeetingRecordingDisabled {
		t.Fatal("disabled preference was not persisted")
	}

	if err := app.SetAutomaticMeetingRecordingEnabled(true); err != nil {
		t.Fatalf("SetAutomaticMeetingRecordingEnabled(true): %v", err)
	}
	if !app.GetAutomaticMeetingRecordingEnabled() {
		t.Fatal("runtime preference stayed disabled after enabling")
	}
	if cfg := loadConfig(); cfg.AutomaticMeetingRecordingDisabled {
		t.Fatal("enabled preference was not persisted")
	}
}

func TestAutomaticMeetingRecordingEpochInvalidatesWorkAcrossOffOn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}

	originalEpoch, enabled := app.automaticMeetingRecordingEpoch()
	if !enabled || !app.automaticMeetingRecordingEpochIsCurrent(originalEpoch) {
		t.Fatal("initial automatic meeting recording epoch should be current")
	}

	if err := app.SetAutomaticMeetingRecordingEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if app.automaticMeetingRecordingEpochIsCurrent(originalEpoch) {
		t.Fatal("pre-disable work remained current after opt-out")
	}

	if err := app.SetAutomaticMeetingRecordingEnabled(true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	freshEpoch, enabled := app.automaticMeetingRecordingEpoch()
	if !enabled || freshEpoch == originalEpoch {
		t.Fatalf("re-enable epoch = %d enabled=%v, want a fresh enabled epoch", freshEpoch, enabled)
	}
	if app.automaticMeetingRecordingEpochIsCurrent(originalEpoch) {
		t.Fatal("off/on cycle resurrected work from the original epoch")
	}
	if !app.automaticMeetingRecordingEpochIsCurrent(freshEpoch) {
		t.Fatal("fresh epoch is not current")
	}

	if err := app.SetAutomaticMeetingRecordingEnabled(true); err != nil {
		t.Fatalf("repeat enable: %v", err)
	}
	if got, _ := app.automaticMeetingRecordingEpoch(); got != freshEpoch {
		t.Fatalf("same-state setter advanced epoch: got %d, want %d", got, freshEpoch)
	}
}

func TestConcurrentAutomaticMeetingRecordingSettersKeepDiskAndRuntimeAligned(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}

	var wg sync.WaitGroup
	errCh := make(chan error, 24)
	for i := 0; i < cap(errCh); i++ {
		enabled := i%2 == 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := app.SetAutomaticMeetingRecordingEnabled(enabled); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent setter: %v", err)
	}

	runtimeEnabled := app.GetAutomaticMeetingRecordingEnabled()
	if persistedDisabled := loadConfig().AutomaticMeetingRecordingDisabled; persistedDisabled == runtimeEnabled {
		t.Fatalf("persisted disabled=%v disagrees with runtime enabled=%v", persistedDisabled, runtimeEnabled)
	}
}

func TestDisablingAutomaticMeetingRecordingClearsRuntimeState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	meetingWriter, err := newMeetingAudioWriter(t.TempDir())
	if err != nil {
		t.Fatalf("new meeting writer: %v", err)
	}
	candidateWriter, err := newMeetingAudioWriter(t.TempDir())
	if err != nil {
		t.Fatalf("new candidate writer: %v", err)
	}

	am := &activityMonitorState{
		inMeeting:                      true,
		meetingAppName:                 "Microsoft Teams",
		meetingBundleID:                "com.microsoft.teams2",
		meetingStartMs:                 100,
		meetingRecordingEpoch:          7,
		meetingID:                      "meeting-id",
		lastMeetingID:                  "previous-id",
		lastMeetingApp:                 "Microsoft Teams",
		lastMeetingPID:                 42,
		lastMeetingEndMs:               90,
		lastMeetingEventMs:             95,
		lastMeetingAudioMs:             99,
		meetingAudioWriter:             meetingWriter,
		meetingCandidateActive:         true,
		meetingCandidateAppName:        "Microsoft Teams",
		meetingCandidateStartMs:        80,
		meetingCandidateRecordingEpoch: 7,
		firstSpeechTime:                75,
		lastMeetingCandidateAudioMs:    98,
		meetingCandidateAudioWriter:    candidateWriter,
	}
	app := &App{activity: am}

	if err := app.SetAutomaticMeetingRecordingEnabled(false); err != nil {
		t.Fatalf("SetAutomaticMeetingRecordingEnabled(false): %v", err)
	}

	am.mu.Lock()
	defer am.mu.Unlock()
	if am.inMeeting || am.meetingCandidateActive {
		t.Fatal("meeting classification state remained active after disabling")
	}
	if am.meetingAppName != "" || am.meetingBundleID != "" || am.meetingID != "" {
		t.Fatal("current meeting identity was not cleared")
	}
	if am.meetingRecordingEpoch != 0 || am.meetingCandidateRecordingEpoch != 0 || am.firstSpeechTime != 0 {
		t.Fatal("meeting generation or speech timing state was not cleared")
	}
	if am.lastMeetingID != "" || am.lastMeetingApp != "" || am.lastMeetingPID != 0 || am.lastMeetingEndMs != 0 {
		t.Fatal("meeting resume identity was not cleared")
	}
	if am.meetingAudioWriter != nil || am.meetingCandidateAudioWriter != nil {
		t.Fatal("meeting audio writers remained attached")
	}

	meetingWriter.mu.Lock()
	meetingClosed := meetingWriter.closed
	meetingWriter.mu.Unlock()
	candidateWriter.mu.Lock()
	candidateClosed := candidateWriter.closed
	candidateWriter.mu.Unlock()
	if !meetingClosed || !candidateClosed {
		t.Fatal("meeting audio writers were not aborted")
	}
}

func TestAutomaticMeetingRecordingDisabledBlocksLateMeetingStartsAndSpeech(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}
	app.meetingRecordingState.Store(meetingRecordingOff)

	app.onMeetingStarted(app.activity, meetingdetect.Result{
		IsMeeting:   true,
		MeetingName: "Microsoft Teams",
		BundleID:    "com.microsoft.teams2",
	})
	if app.activity.inMeeting {
		t.Fatal("late meeting start bypassed the disabled preference")
	}

	app.injectSpeechEvent("queued meeting speech", backgroundListeningSourceSystem, speechCaptureContext{
		InMeeting:      true,
		MeetingAppName: "Microsoft Teams",
	})
	if got := app.activity.buf.Len(); got != 0 {
		t.Fatalf("queued meeting speech reached the activity buffer after opt-out: len=%d", got)
	}
}

func TestAutomaticMeetingRecordingDropsDelayedSpeechAcrossOffOn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Listening is on, as it is wherever speech reaches the activity.
	if err := updateConfig(func(cfg *yansuConfig) {
		cfg.BackgroundListeningConfigured = true
		cfg.BackgroundListeningDisabled = false
	}); err != nil {
		t.Fatal(err)
	}
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}
	staleEpoch, _ := app.automaticMeetingRecordingEpoch()
	if err := app.SetAutomaticMeetingRecordingEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := app.SetAutomaticMeetingRecordingEnabled(true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}

	app.injectSpeechEvent("old queued meeting speech", backgroundListeningSourceSystem, speechCaptureContext{
		MeetingRecordingEpoch: staleEpoch,
		InMeeting:             true,
		MeetingAppName:        "Microsoft Teams",
	})
	if got := app.activity.buf.Len(); got != 0 {
		t.Fatalf("stale speech crossed off/on boundary: len=%d", got)
	}

	freshEpoch, _ := app.automaticMeetingRecordingEpoch()
	app.injectSpeechEvent("new meeting speech", backgroundListeningSourceSystem, speechCaptureContext{
		MeetingRecordingEpoch: freshEpoch,
		InMeeting:             true,
		MeetingAppName:        "Microsoft Teams",
	})
	if app.activity.idleTimer != nil {
		defer app.activity.idleTimer.Stop()
	}
	if got := app.activity.buf.Len(); got != 1 {
		t.Fatalf("fresh speech was not admitted: len=%d", got)
	}
}

// The detection poll and audio hot paths read the in-memory preference; only the
// window's binding writes a perf line for it.
func TestMeetingRecordingOnReadsMemoryWithoutPerfLines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)
	app := &App{}

	if err := app.SetAutomaticMeetingRecordingEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	logged.Reset()
	for range 3 {
		if app.meetingRecordingOn() {
			t.Fatal("internal read saw enabled after opt-out")
		}
	}
	if strings.Contains(logged.String(), "binding=GetAutomaticMeetingRecordingEnabled") {
		t.Fatalf("internal reads wrote perf lines: %q", logged.String())
	}
	if app.GetAutomaticMeetingRecordingEnabled() {
		t.Fatal("window binding saw enabled after opt-out")
	}
	if !strings.Contains(logged.String(), "binding=GetAutomaticMeetingRecordingEnabled") {
		t.Fatal("window binding stopped writing its perf line")
	}
}
