package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAudioCaptureConfigGates(t *testing.T) {
	for _, tc := range []struct {
		name, json                                     string
		directory, known, listen, mic, system, meeting bool
	}{
		{name: "missing", known: true, listen: true, mic: true, meeting: true},
		{name: "existing legacy", json: "{}", known: true, mic: true, meeting: true},
		{name: "enabled", json: `{"background_listening_configured":true,"background_system_audio_outside_calls":true}`, known: true, listen: true, mic: true, system: true, meeting: true},
		{name: "mic paused", json: `{"background_listening_configured":true,"background_microphone_paused":true,"background_system_audio_outside_calls":true}`, known: true, listen: true, system: true, meeting: true},
		{name: "meeting off", json: `{"background_listening_configured":true,"automatic_meeting_recording_disabled":true}`, known: true, listen: true, mic: true},
		{name: "invalid", json: `{"background_listening_configured":true`},
		{name: "invalid field", json: `{"background_listening_configured":true,"background_microphone_paused":17}`},
		{name: "null", json: "null"},
		{name: "read error", directory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(agentHomeEnv, dir)
			path := filepath.Join(dir, configFileName)
			if tc.directory {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if tc.json != "" {
				if err := os.WriteFile(path, []byte(tc.json), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a := &App{}
			a.loadMeetingRecordingPreference()
			cfg := readAudioConfig()
			check := func(name string, want bool, read func() (bool, bool)) {
				t.Helper()
				on, known := read()
				if on != want || known != tc.known {
					t.Fatalf("%s = (%t,%t), want (%t,%t)", name, on, known, want, tc.known)
				}
			}
			check("listen", tc.listen, cfg.listening)
			check("microphone", tc.mic, cfg.microphone)
			check("system outside calls", tc.system, cfg.outsideCalls)
			check("meeting", tc.meeting, a.meetingRecordingPreference)
			check("system permission", false, cfg.systemPermission)
			if !tc.known {
				a.startBackgroundListeningIfReady()
				if a.isBackgroundListeningRunning() || cfg.source() != backgroundListeningSourceNone {
					t.Fatal("recorder start admitted unreadable config")
				}
			}
			for _, sample := range []struct {
				source     backgroundListeningSource
				call, want bool
			}{
				{backgroundListeningSourceMicrophone, false, tc.listen && tc.mic},
				{backgroundListeningSourceSystem, false, tc.listen && tc.system},
				{backgroundListeningSourceSystem, true, tc.listen && tc.meeting},
			} {
				chunk := testCapturedChunk(sample.source, time.Second, 16000)
				chunk.CapturedInMeeting = sample.call
				chunk.CapturedMeetingAppName = "Zoom"
				recorded := false
				capture := backgroundAudioCapture{shouldTranscribe: a.shouldTranscribeBackgroundAudioChunk, onRecordingChunk: func(capturedAudioChunk) { recorded = true }}
				_, admitted := capture.recordCapturedChunk(chunk)
				if admitted != sample.want || recorded != sample.want {
					t.Errorf("source %s call %t: admitted %t recording callback %t, want %t", sample.source, sample.call, admitted, recorded, sample.want)
				}

				model := newFakeBackgroundTranscriber("")
				s := newBackgroundTranscriptionScheduler(model, nil)
				s.allowBatch = a.allowBackgroundAudioBatch
				s.enqueue(chunk) // already queued when the gate is read again
				s.processBatch(s.popDrainBatchLocked())
				if got := len(model.requests) != 0; got != sample.want {
					t.Errorf("source %s call %t: model called %t, want %t", sample.source, sample.call, got, sample.want)
				}
			}
			if !tc.known {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := saveConfig(yansuConfig{BackgroundListeningConfigured: true, BackgroundSystemAudioOutsideCalls: true}); err != nil {
					t.Fatal(err)
				}
				if !a.meetingRecordingOn() || !a.GetBackgroundListening() || !backgroundMicrophoneEnabled() || !a.GetSystemAudioOutsideCallsEnabled() {
					t.Fatal("gates did not recover after repair")
				}
			}
		})
	}
}

func TestZeroValueAppMeetingRecordingRetriesUnknownConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(agentHomeEnv, dir)
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}

	var app App
	if on, known := app.meetingRecordingPreference(); on || known {
		t.Fatalf("unreadable config: on=%t known=%t, want both false", on, known)
	}
	if app.meetingRecordingOn() {
		t.Fatal("recording opened while config was still unreadable")
	}

	if err := saveConfig(yansuConfig{AutomaticMeetingRecordingDisabled: false}); err != nil {
		t.Fatal(err)
	}
	if on, known := app.meetingRecordingPreference(); !on || !known {
		t.Fatalf("repaired config: on=%t known=%t, want both true", on, known)
	}
}

func TestAudioCaptureConfigLogsOncePerOutagePerSource(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	first := audioConfigGate{source: "first"}
	second := audioConfigGate{source: "second"}
	failure := errors.New("unreadable")
	for i := 0; i < 2; i++ {
		for _, gate := range []*audioConfigGate{&first, &second} {
			gate.read(true, failure)
			gate.read(true, failure)
			gate.read(false, nil)
		}
	}
	for _, source := range []string{"first", "second"} {
		if strings.Count(output.String(), "[capture] "+source+" skipped: config unreadable") != 2 {
			t.Fatalf("unexpected outage log: %s", output.String())
		}
	}
}

func TestListeningConfigOutageSuspendsRunningCapture(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(agentHomeEnv, dir)
	recorder := &stoppableFakeRecorder{chunks: make(chan capturedAudioChunk)}
	model := newFakeBackgroundTranscriber("")
	capture := &backgroundAudioCapture{recorder: recorder, server: model}
	if err := capture.start(); err != nil {
		t.Fatal(err)
	}
	defer capture.stop()
	app := &App{backgroundAudio: capture}
	if err := os.Symlink(filepath.Join(dir, "unavailable"), filepath.Join(dir, configFileName)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	app.reconcileBackgroundAudioConfig()
	if capture.isRunning() || app.backgroundAudio != nil || !app.audioConfigRetryPending || len(model.requests) != 0 {
		t.Fatal("outage did not suspend the recorder and schedule a retry")
	}
	if err := saveConfig(yansuConfig{BackgroundListeningConfigured: true, BackgroundListeningDisabled: true}); err != nil {
		t.Fatal(err)
	}
	app.reconcileBackgroundAudioConfig()
	if app.audioConfigRetryPending || app.isBackgroundListeningRunning() {
		t.Fatal("repair retried without respecting the disabled switch")
	}
}
