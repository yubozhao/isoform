package main

import (
	"testing"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/platform/activitymon"
)

func TestShouldTranscribeBackgroundAudioChunk(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	// A call window or title counts only with its app on the microphone.
	tests := []struct {
		name                string
		app                 activitymon.AppInfo
		holders             micInputSnapshot
		browserMeeting      string
		inMeeting           bool
		meetingName         string
		meetingBundleID     string
		capturedInMeeting   bool
		capturedMeetingName string
		capturedCandidate   bool
		candidateName       string
		want                bool
	}{
		{
			name: "zoom active window",
			app: activitymon.AppInfo{
				BundleID:    "us.zoom.xos",
				AppName:     "Zoom",
				WindowTitle: "Zoom Meeting",
			},
			holders: holding("us.zoom.xos"),
			want:    true,
		},
		{
			name: "zoom meeting window while Claude has the microphone",
			app: activitymon.AppInfo{
				BundleID:    "us.zoom.xos",
				AppName:     "Zoom",
				WindowTitle: "Zoom Meeting",
			},
			holders: holding("com.anthropic.claudefordesktop"),
			want:    false,
		},
		{
			name: "slack huddle",
			app: activitymon.AppInfo{
				BundleID:    "com.tinyspeck.slackmacgap",
				AppName:     "Slack",
				WindowTitle: "Slack - Huddle with team",
			},
			holders: holding("com.tinyspeck.slackmacgap.helper"),
			want:    true,
		},
		{
			name: "google meet in browser",
			app: activitymon.AppInfo{
				BundleID:    "com.google.Chrome",
				AppName:     "Chrome",
				WindowTitle: "Meet - abc-defg-hij",
			},
			holders:        holding("com.google.Chrome.helper"),
			browserMeeting: "Google Meet",
			want:           true,
		},
		{
			name: "a meet title without a call address",
			app: activitymon.AppInfo{
				BundleID:    "com.google.Chrome",
				AppName:     "Chrome",
				WindowTitle: "Meet - abc-defg-hij",
			},
			holders: holding("com.google.Chrome.helper"),
			want:    false,
		},
		{
			name: "chatgpt tab titled microsoft teams taking dictation",
			app: activitymon.AppInfo{
				BundleID:    "com.google.Chrome",
				AppName:     "Chrome",
				WindowTitle: "Microsoft Teams",
			},
			holders: holding("com.google.Chrome.helper"),
			want:    false,
		},
		{
			name: "plain slack channel",
			app: activitymon.AppInfo{
				BundleID:    "com.tinyspeck.slackmacgap",
				AppName:     "Slack",
				WindowTitle: "Slack - #general",
			},
			holders: holding("com.tinyspeck.slackmacgap.helper"),
			want:    false,
		},
		{
			name: "teams active meeting",
			app: activitymon.AppInfo{
				BundleID:    "com.microsoft.teams2",
				AppName:     "Microsoft Teams",
				WindowTitle: "Meeting in Progress",
			},
			holders: holding("com.microsoft.teams2"),
			want:    true,
		},
		{
			name: "allowed meeting continues while notes are frontmost",
			app: activitymon.AppInfo{
				BundleID:    "com.apple.Notes",
				AppName:     "Notes",
				WindowTitle: "Meeting notes",
			},
			holders:         holding("us.zoom.xos"),
			inMeeting:       true,
			meetingName:     "Zoom",
			meetingBundleID: "us.zoom.xos",
			want:            true,
		},
		{
			name: "dictation into Claude after Zoom let go of the microphone",
			app: activitymon.AppInfo{
				BundleID:    "com.anthropic.claudefordesktop",
				AppName:     "Claude",
				WindowTitle: "Claude",
			},
			holders:         holding("com.anthropic.claudefordesktop"),
			inMeeting:       true,
			meetingName:     "Zoom",
			meetingBundleID: "us.zoom.xos",
			want:            false,
		},
		{
			name: "teams meeting continues while notes are frontmost",
			app: activitymon.AppInfo{
				BundleID:    "com.apple.Notes",
				AppName:     "Notes",
				WindowTitle: "Meeting notes",
			},
			holders:         holding("com.microsoft.teams2.helper"),
			inMeeting:       true,
			meetingName:     "Microsoft Teams",
			meetingBundleID: "com.microsoft.teams2",
			want:            true,
		},
		{
			name: "unsupported meeting does not transcribe while notes are frontmost",
			app: activitymon.AppInfo{
				BundleID:    "com.apple.Notes",
				AppName:     "Notes",
				WindowTitle: "Meeting notes",
			},
			holders:     holding("com.hnc.Discord"),
			inMeeting:   true,
			meetingName: "Discord",
			want:        false,
		},
		{
			name: "regular app",
			app: activitymon.AppInfo{
				BundleID:    "com.apple.finder",
				AppName:     "Finder",
				WindowTitle: "Downloads",
			},
			want: false,
		},
		{
			name:                "captured meeting context survives delayed asr",
			app:                 activitymon.AppInfo{BundleID: "com.googlecode.iterm2", AppName: "iTerm2", WindowTitle: "notes"},
			capturedInMeeting:   true,
			capturedMeetingName: "Microsoft Teams",
			want:                true,
		},
		{
			name:              "candidate meeting context is allowed for holistic judgement",
			app:               activitymon.AppInfo{BundleID: "com.googlecode.iterm2", AppName: "iTerm2", WindowTitle: "notes"},
			capturedCandidate: true,
			candidateName:     "Zoom",
			want:              true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{
				activity: &activityMonitorState{
					buf:             activitybuf.NewRingBuffer(16, time.Minute),
					inMeeting:       tt.inMeeting,
					meetingAppName:  tt.meetingName,
					meetingBundleID: tt.meetingBundleID,
				},
			}
			chunk := capturedAudioChunk{
				Source:                   backgroundListeningSourceMicrophone,
				WavData:                  makeWAV(make([]int16, 1000)),
				CapturedAt:               time.Now(),
				CapturedApp:              tt.app,
				MicHolders:               tt.holders,
				BrowserMeeting:           tt.browserMeeting,
				OtherMicUser:             tt.holders.Active,
				CapturedInMeeting:        tt.capturedInMeeting,
				CapturedMeetingAppName:   tt.capturedMeetingName,
				CapturedMeetingCandidate: tt.capturedCandidate,
				CandidateMeetingAppName:  tt.candidateName,
			}
			if !tt.capturedInMeeting && !tt.capturedCandidate {
				chunk = app.annotateBackgroundAudioChunk(chunk)
			}
			if got := app.chunkCapturedInCall(chunk); got != tt.want {
				t.Fatalf("chunkCapturedInCall() = %v, want %v", got, tt.want)
			}
			// Outside or inside a call, microphone speech is listened to.
			if !app.shouldTranscribeBackgroundAudioChunk(chunk) {
				t.Fatal("microphone speech is turned into text all day")
			}
		})
	}
}

func TestForwardChunkDownstreamSkipsRejectedTranscriptionTargets(t *testing.T) {
	called := false
	capture := &backgroundAudioCapture{
		shouldTranscribe: func(capturedAudioChunk) bool { return false },
		onChunk:          func(capturedAudioChunk) { called = true },
	}

	capture.forwardChunkDownstream(capturedAudioChunk{
		Source:  backgroundListeningSourceMicrophone,
		WavData: makeWAV(make([]int16, 1000)),
	})

	if called {
		t.Fatal("onChunk should not run when the transcription target filter rejects the chunk")
	}
}

func TestForwardChunkDownstreamAnnotatesBeforeFiltering(t *testing.T) {
	called := false
	capture := &backgroundAudioCapture{
		annotateChunk: func(chunk capturedAudioChunk) capturedAudioChunk {
			chunk.CapturedInMeeting = true
			chunk.CapturedMeetingAppName = "Microsoft Teams"
			return chunk
		},
		shouldTranscribe: func(chunk capturedAudioChunk) bool {
			return chunk.CapturedInMeeting && chunk.CapturedMeetingAppName == "Microsoft Teams"
		},
		onChunk: func(capturedAudioChunk) { called = true },
	}

	capture.forwardChunkDownstream(capturedAudioChunk{
		Source:  backgroundListeningSourceSystem,
		WavData: makeWAV(make([]int16, 1000)),
	})

	if !called {
		t.Fatal("onChunk should run when annotateChunk supplies accepted meeting context")
	}
}

func TestAnnotateBackgroundAudioChunkCapturesLiveMeetingContext(t *testing.T) {
	capturedAt := time.Date(2026, 5, 20, 15, 30, 0, 0, time.UTC)
	app := &App{
		activity: &activityMonitorState{
			buf:             activitybuf.NewRingBuffer(16, time.Minute),
			inMeeting:       true,
			meetingAppName:  "Microsoft Teams",
			meetingBundleID: "com.microsoft.teams2",
		},
	}

	chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:      backgroundListeningSourceSystem,
		WavData:     loudPCM(1000),
		CapturedAt:  capturedAt,
		CapturedApp: activitymon.AppInfo{BundleID: "com.googlecode.iterm2", AppName: "iTerm2", WindowTitle: "repo notes"},
		MicHolders:  micInputSnapshot{Active: true, BundleIDs: []string{"com.microsoft.teams2.helper"}},
	})

	if !chunk.CapturedInMeeting {
		t.Fatal("expected chunk to retain meeting context while another app is frontmost")
	}
	if chunk.CapturedMeetingAppName != "Microsoft Teams" {
		t.Fatalf("CapturedMeetingAppName = %q, want Microsoft Teams", chunk.CapturedMeetingAppName)
	}
	if chunk.CapturedMeetingBundleID != "com.microsoft.teams2" {
		t.Fatalf("CapturedMeetingBundleID = %q, want com.microsoft.teams2", chunk.CapturedMeetingBundleID)
	}
	if got := app.activity.lastMeetingAudioMs; got != capturedAt.UnixMilli() {
		t.Fatalf("Teams holding the mic must keep the call alive: lastMeetingAudioMs = %d, want %d", got, capturedAt.UnixMilli())
	}

	// After the call: music on the system audio, Teams no longer on the
	// microphone. That must not keep the call alive.
	app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:      backgroundListeningSourceSystem,
		WavData:     loudPCM(1000),
		CapturedAt:  capturedAt.Add(time.Minute),
		CapturedApp: activitymon.AppInfo{BundleID: "com.spotify.client", AppName: "Spotify", WindowTitle: "Spotify"},
		MicHolders:  micInputSnapshot{},
	})
	if got := app.activity.lastMeetingAudioMs; got != capturedAt.UnixMilli() {
		t.Fatalf("system audio alone refreshed the call: lastMeetingAudioMs = %d", got)
	}
}

func TestAnnotateBackgroundAudioChunkStartsNativeMeetingCandidate(t *testing.T) {
	capturedAt := time.Date(2026, 5, 20, 15, 30, 0, 0, time.UTC)
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}

	// Where the processes on the microphone cannot be named (Windows), the
	// meeting app in front on a call window with another mic user starts a
	// candidate on the second chunk in a row.
	frontmostChunk := capturedAudioChunk{
		Source:       backgroundListeningSourceMicrophone,
		WavData:      makeWAV(make([]int16, 1000)),
		CapturedAt:   capturedAt,
		CapturedApp:  activitymon.AppInfo{BundleID: "com.microsoft.teams2", AppName: "Microsoft Teams", WindowTitle: "Weekly sync | Microsoft Teams"},
		OtherMicUser: true,
		MicHolders:   micInputSnapshot{Active: true},
	}
	if first := app.annotateBackgroundAudioChunk(frontmostChunk); first.CapturedMeetingCandidate {
		t.Fatal("one chunk of frontmost evidence must not start a call")
	}
	chunk := app.annotateBackgroundAudioChunk(frontmostChunk)

	if chunk.CapturedInMeeting {
		t.Fatal("candidate fallback must not mark the chunk as a confirmed meeting")
	}
	if !chunk.CapturedMeetingCandidate {
		t.Fatal("expected supported native meeting app with external mic evidence to become a candidate")
	}
	if chunk.CandidateMeetingAppName != "Microsoft Teams" {
		t.Fatalf("CandidateMeetingAppName = %q, want Microsoft Teams", chunk.CandidateMeetingAppName)
	}
	if !app.activity.meetingCandidateActive {
		t.Fatal("expected candidate state to stay active for subsequent system-audio chunks")
	}
	if got := app.activity.lastMeetingCandidateAudioMs; got != capturedAt.UnixMilli() {
		t.Fatalf("lastMeetingCandidateAudioMs = %d, want %d", got, capturedAt.UnixMilli())
	}
}

func TestAnnotateBackgroundAudioChunkContinuesNativeMeetingCandidate(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf:                     activitybuf.NewRingBuffer(16, time.Minute),
			meetingCandidateActive:  true,
			meetingCandidateAppName: "Zoom",
		},
	}

	if music := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:      backgroundListeningSourceSystem,
		WavData:     loudPCM(1000),
		CapturedApp: activitymon.AppInfo{BundleID: "com.googlecode.iterm2", AppName: "iTerm2", WindowTitle: "notes"},
	}); music.CapturedMeetingCandidate {
		t.Fatal("audible system audio alone must not continue a candidate call")
	}

	chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:          backgroundListeningSourceSystem,
		WavData:         loudPCM(1000),
		CapturedApp:     activitymon.AppInfo{BundleID: "com.googlecode.iterm2", AppName: "iTerm2", WindowTitle: "notes"},
		MeetingAppOnMic: "Zoom",
	})

	if !chunk.CapturedMeetingCandidate {
		t.Fatal("expected the call's system audio to continue the candidate while Zoom holds the mic")
	}
	if chunk.CandidateMeetingAppName != "Zoom" {
		t.Fatalf("CandidateMeetingAppName = %q, want Zoom", chunk.CandidateMeetingAppName)
	}
}

func TestAnnotateBackgroundAudioChunkRejectsGenericBrowserAudioCandidate(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}

	chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:      backgroundListeningSourceSystem,
		WavData:     loudPCM(1000),
		CapturedApp: activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Chrome", WindowTitle: "YouTube"},
	})

	if chunk.CapturedMeetingCandidate {
		t.Fatal("generic browser audio should not become a meeting candidate")
	}
}

func TestAnnotateBackgroundAudioChunkDoesNotFallbackDuringUnsupportedMeeting(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf:            activitybuf.NewRingBuffer(16, time.Minute),
			inMeeting:      true,
			meetingAppName: "Discord",
		},
	}

	chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:       backgroundListeningSourceMicrophone,
		WavData:      makeWAV(make([]int16, 1000)),
		CapturedApp:  activitymon.AppInfo{BundleID: "us.zoom.xos", AppName: "Zoom", WindowTitle: "Zoom Workplace"},
		OtherMicUser: true,
	})

	if chunk.CapturedInMeeting || chunk.CapturedMeetingCandidate {
		t.Fatal("unsupported active meetings should not be converted into supported fallback candidates")
	}
}

func TestAutomaticMeetingRecordingDisabledRejectsAndDoesNotAnnotateAudio(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}
	app.meetingRecordingState.Store(meetingRecordingOff)

	chunks := []capturedAudioChunk{
		{
			Source:      backgroundListeningSourceSystem,
			WavData:     loudPCM(1000),
			CapturedApp: activitymon.AppInfo{BundleID: "com.microsoft.teams2", AppName: "Microsoft Teams", WindowTitle: "Meeting in Progress"},
		},
		{
			Source:                 backgroundListeningSourceSystem,
			WavData:                loudPCM(1000),
			CapturedInMeeting:      true,
			CapturedMeetingAppName: "Microsoft Teams",
		},
		{
			Source:                   backgroundListeningSourceMicrophone,
			WavData:                  loudPCM(1000),
			CapturedMeetingCandidate: true,
			CandidateMeetingAppName:  "Zoom",
		},
	}

	for i, chunk := range chunks {
		if app.chunkCapturedInCall(chunk) {
			t.Fatalf("disabled preference accepted chunk %d as call audio", i)
		}
		// With meeting recording off, listening goes on outside calls: the
		// microphone yes, the computer's own sound no.
		if got, want := app.shouldTranscribeBackgroundAudioChunk(chunk), chunk.Source == backgroundListeningSourceMicrophone; got != want {
			t.Fatalf("chunk %d (%s) listened = %v, want %v", i, chunk.Source, got, want)
		}
	}

	annotated := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:       backgroundListeningSourceMicrophone,
		WavData:      loudPCM(1000),
		CapturedApp:  activitymon.AppInfo{BundleID: "com.microsoft.teams2", AppName: "Microsoft Teams", WindowTitle: "Meeting in Progress"},
		OtherMicUser: true,
	})
	if annotated.CapturedInMeeting || annotated.CapturedMeetingCandidate {
		t.Fatal("disabled preference still annotated audio as a meeting")
	}
	if app.activity.meetingCandidateActive {
		t.Fatal("disabled preference created meeting candidate state")
	}
}

func TestAnnotateBackgroundAudioChunkIgnoresDictationWhileMeetingAppIdles(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}

	// Dictation into Claude while Zoom sits idle: another process holds the
	// microphone, but not the meeting app.
	chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:       backgroundListeningSourceMicrophone,
		WavData:      loudPCM(1000),
		CapturedApp:  activitymon.AppInfo{BundleID: "com.anthropic.claudefordesktop", AppName: "Claude", WindowTitle: "Claude"},
		OtherMicUser: true,
	})

	if chunk.CapturedInMeeting || chunk.CapturedMeetingCandidate {
		t.Fatal("dictation into another app must not become a meeting candidate")
	}
	if app.activity.meetingCandidateActive {
		t.Fatal("dictation created candidate state")
	}
	if app.chunkCapturedInCall(chunk) {
		t.Fatal("dictation outside a call is not call audio")
	}
}

func TestAnnotateBackgroundAudioChunkStartsCandidateWhenMeetingAppIsInCall(t *testing.T) {
	capturedAt := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}

	chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:                  backgroundListeningSourceMicrophone,
		WavData:                 makeWAV(make([]int16, 1000)),
		CapturedAt:              capturedAt,
		CapturedApp:             activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes", WindowTitle: "Notes"},
		OtherMicUser:            true,
		MeetingAppOnMic:         "Zoom",
		MeetingAppOnMicBundleID: "us.zoom.xos",
		MeetingCallOnMic:        "Zoom",
	})

	if !chunk.CapturedMeetingCandidate || chunk.CandidateMeetingAppName != "Zoom" || chunk.CandidateMeetingReason != "meeting_app_in_call" {
		t.Fatalf("a meeting app holding the mic is a call: %+v", chunk)
	}
	if !app.activity.meetingCandidateActive || app.activity.meetingCandidateAppName != "Zoom" {
		t.Fatal("expected candidate state for the call")
	}
}

func TestAnnotateBackgroundAudioChunkKeepsQuietCallAudio(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	app := &App{
		activity: &activityMonitorState{
			buf:                     activitybuf.NewRingBuffer(16, time.Minute),
			meetingCandidateActive:  true,
			meetingCandidateAppName: "Zoom",
		},
	}

	// Nobody on the other side is talking: the system audio is silent, but
	// Zoom still holds the microphone, so the chunk stays in the recording.
	chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:          backgroundListeningSourceSystem,
		WavData:         makeWAV(make([]int16, 1000)),
		CapturedApp:     activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes", WindowTitle: "Notes"},
		MeetingAppOnMic: "Zoom",
	})

	if !chunk.CapturedMeetingCandidate {
		t.Fatal("quiet chunks of a running call must continue the candidate")
	}
	if !app.shouldTranscribeBackgroundAudioChunk(chunk) {
		t.Fatal("quiet chunks of a running call must reach the recording")
	}
}

func TestAnnotateBackgroundAudioChunkIgnoresMeetingAppHoldWithoutCallEvidence(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}
	// Zoom has the microphone for its audio test: it holds it, but there is
	// no call evidence (MeetingCallOnMic stays empty).
	for i := 0; i < 3; i++ {
		chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
			Source:                  backgroundListeningSourceMicrophone,
			WavData:                 loudPCM(1000),
			CapturedApp:             activitymon.AppInfo{BundleID: "us.zoom.xos", AppName: "zoom.us", WindowTitle: "Settings"},
			OtherMicUser:            true,
			MicHolders:              micInputSnapshot{Active: true, BundleIDs: []string{"us.zoom.xos"}},
			MeetingAppOnMic:         "Zoom",
			MeetingAppOnMicBundleID: "us.zoom.xos",
		})
		if chunk.CapturedMeetingCandidate || app.chunkCapturedInCall(chunk) {
			t.Fatalf("an audio test is not a call: %+v", chunk.CandidateMeetingReason)
		}
	}
}

func TestAnnotateBackgroundAudioChunkFrontmostIdleLauncherIsNoCall(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}
	idle := capturedAudioChunk{
		Source:       backgroundListeningSourceMicrophone,
		WavData:      loudPCM(1000),
		CapturedApp:  activitymon.AppInfo{BundleID: "us.zoom.xos", AppName: "zoom.us", WindowTitle: "Zoom Workplace"},
		OtherMicUser: true,
		MicHolders:   micInputSnapshot{Active: true},
	}
	named := idle
	named.CapturedApp.WindowTitle = "Weekly sync"
	named.MicHolders = micInputSnapshot{Active: true, BundleIDs: []string{"com.anthropic.claudefordesktop"}}
	for i := 0; i < 3; i++ {
		if chunk := app.annotateBackgroundAudioChunk(idle); chunk.CapturedMeetingCandidate {
			t.Fatal("Zoom in front on its idle launcher is not a call")
		}
		if chunk := app.annotateBackgroundAudioChunk(named); chunk.CapturedMeetingCandidate {
			t.Fatal("dictation into Claude while Zoom is in front is not a call")
		}
	}
}

func TestListeningOutsideCallsKeepsTheComputerSoundBehindItsSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{activity: &activityMonitorState{buf: activitybuf.NewRingBuffer(16, time.Minute)}}
	mic := capturedAudioChunk{Source: backgroundListeningSourceMicrophone, WavData: loudPCM(1000), CapturedApp: activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes"}}
	sys := mic
	sys.Source = backgroundListeningSourceSystem
	if !app.shouldTranscribeBackgroundAudioChunk(mic) || app.shouldTranscribeBackgroundAudioChunk(sys) {
		t.Fatal("outside calls: the microphone yes, the computer's sound no")
	}
	ensureBackgroundListeningDefaultForFreshInstall()
	if err := app.SetSystemAudioOutsideCallsEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !app.shouldTranscribeBackgroundAudioChunk(sys) {
		t.Fatal("with its switch on, the computer's sound is heard outside calls")
	}
	if !(&App{}).GetSystemAudioOutsideCallsEnabled() {
		t.Fatal("the switch is saved")
	}
	app.closedEyes.enabled.Store(true)
	if app.shouldTranscribeBackgroundAudioChunk(mic) {
		t.Fatal("paused observing hears nothing")
	}
}
