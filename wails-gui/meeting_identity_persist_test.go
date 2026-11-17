package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/meetingdetect"
	"wails-gui/internal/platform/activitymon"
)

func withZoomRunning(t *testing.T, pid int) {
	t.Helper()
	saved := meetingProbes.runningApps
	meetingProbes.runningApps = func() []activitymon.RunningApp { return []activitymon.RunningApp{{BundleID: "us.zoom.xos", PID: pid}} }
	t.Cleanup(func() { meetingProbes.runningApps = saved })
}

func zoomCall() meetingdetect.Result {
	return meetingdetect.Result{IsMeeting: true, MeetingName: "Zoom", BundleID: "us.zoom.xos"}
}

// One run of the app: a fresh monitor state on the same data folder, the
// way a relaunch starts.
func freshRun(t *testing.T, store *ActivityStore) *App {
	t.Helper()
	app := mainChatTestApp(t)
	app.activity = &activityMonitorState{store: store, buf: activitybuf.NewRingBuffer(16, time.Hour)}
	restoreCallIdentity(app.activity)
	return app
}

func readCallIdentity(t *testing.T, am *activityMonitorState) (persistedCallIdentity, bool) {
	t.Helper()
	data, err := os.ReadFile(lastCallIdentityPath(am))
	if err != nil {
		return persistedCallIdentity{}, false
	}
	var id persistedCallIdentity
	if err := json.Unmarshal(data, &id); err != nil {
		t.Fatalf("call identity file unreadable: %v", err)
	}
	return id, true
}

// A relaunch in the middle of a call (the app-runner rebuilds, an update,
// a crash) keeps the call under one meeting id, so one notes record.
func TestRelaunchMidCallResumesTheSameMeeting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clean bool
	}{
		{"quit and reopen", true},
		{"crash", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			withZoomRunning(t, 42)
			store := newLifecycleStore(t)

			first := freshRun(t, store)
			first.onMeetingStarted(first.activity, zoomCall())
			callID := first.activity.meetingID
			if callID == "" {
				t.Fatal("the call never started")
			}
			if tc.clean {
				first.extractSession("shutdown_in_meeting")
				rememberCallIdentity(first.activity, time.Now())
			}
			if first.activity.meetingAudioWriter != nil {
				first.activity.meetingAudioWriter.Abort()
			}

			second := freshRun(t, store)
			second.onMeetingStarted(second.activity, zoomCall())
			defer func() {
				if second.activity.meetingAudioWriter != nil {
					second.activity.meetingAudioWriter.Abort()
				}
			}()
			if second.activity.meetingID != callID {
				t.Fatalf("after the relaunch the call became a new meeting %q, want %q", second.activity.meetingID, callID)
			}
		})
	}
}

// The resume rule is the in-process one: a different call hours later in a
// new Zoom process is a new meeting even though the file names the old one.
func TestRelaunchDoesNotJoinADifferentLaterCall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newLifecycleStore(t)
	app := mainChatTestApp(t)
	app.activity = &activityMonitorState{store: store, lastMeetingID: "old-call", lastMeetingApp: "Zoom", lastMeetingPID: 42, lastMeetingEndMs: time.Now().Add(-2 * time.Hour).UnixMilli()}
	rememberCallIdentity(app.activity, time.Now())

	withZoomRunning(t, 77)
	next := freshRun(t, store)
	next.onMeetingStarted(next.activity, zoomCall())
	defer next.activity.meetingAudioWriter.Abort()
	if next.activity.meetingID == "old-call" {
		t.Fatal("a call hours later in a new Zoom process joined the old meeting")
	}
}

// The file holds the identity only: id, app, process and time. Turning
// automatic meeting recording off removes it, as it clears the identity in
// memory.
func TestCallIdentityFileKeepsNoWordsAndClearsOnOptOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withZoomRunning(t, 42)
	app := freshRun(t, newLifecycleStore(t))
	app.onMeetingStarted(app.activity, zoomCall())
	id, ok := readCallIdentity(t, app.activity)
	if !ok || id.ID != app.activity.meetingID || id.App != "Zoom" || id.PID != 42 || id.SeenMs <= 0 {
		t.Fatalf("call identity not kept at call start: %+v ok=%v", id, ok)
	}
	data, _ := os.ReadFile(lastCallIdentityPath(app.activity))
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	for k := range fields {
		switch k {
		case "id", "app", "pid", "paused", "seen_ms":
		default:
			t.Fatalf("call identity file holds %q", k)
		}
	}

	app.abortAutomaticMeetingRecording()
	if _, ok := readCallIdentity(t, app.activity); ok {
		t.Fatal("turning meeting recording off left the call identity on disk")
	}
}

// While a call runs the watcher refreshes "last seen", so a crash late in a
// long call still resumes within the window.
func TestLiveCallIdentityRefreshesWhileTheCallRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withZoomRunning(t, 42)
	app := freshRun(t, newLifecycleStore(t))
	app.onMeetingStarted(app.activity, zoomCall())
	defer app.activity.meetingAudioWriter.Abort()
	start, _ := readCallIdentity(t, app.activity)

	later := time.UnixMilli(start.SeenMs).Add(45 * time.Minute)
	refreshLiveCallIdentity(app.activity, later)
	got, _ := readCallIdentity(t, app.activity)
	if got.SeenMs != later.UnixMilli() || got.ID != start.ID {
		t.Fatalf("live call identity not refreshed: %+v", got)
	}
}
