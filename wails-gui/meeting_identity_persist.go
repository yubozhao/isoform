package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"
)

// A call keeps one meeting id, and so one notes record, across a relaunch
// of the app. The id of the call in progress (or of the one that just
// ended) lives in activityMonitorState, which a relaunch starts empty:
// without this file the call the watcher saw again after the relaunch got
// a new id and a second notes record. The file holds only the id, the call
// app's name and process id, and when the call was last seen: no title, no
// words. onMeetingStarted's resume rule (same process, or back within
// meetingResumeWindowMs) decides from it exactly as it does within one run.

const lastCallIdentityFile = "last-call.json"

// lastCallRefreshMs is how often the watcher refreshes the file while a
// call runs, so a crash mid-call leaves a "last seen" at most this old.
const lastCallRefreshMs int64 = 60_000

type persistedCallIdentity struct {
	ID     string `json:"id"`
	App    string `json:"app"`
	PID    int    `json:"pid,omitempty"`
	Paused bool   `json:"paused,omitempty"`
	SeenMs int64  `json:"seen_ms"`
}

func lastCallIdentityPath(am *activityMonitorState) string {
	if am == nil || am.store == nil || am.store.AudioDir() == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(am.store.AudioDir()), lastCallIdentityFile)
}

// callIdentityToPersistLocked is the identity a relaunch should resume: the
// call in progress, seen now, or else the last ended call. Caller holds am.mu.
func callIdentityToPersistLocked(am *activityMonitorState, nowMs int64) (persistedCallIdentity, bool) {
	if am.inMeeting && am.meetingID != "" {
		return persistedCallIdentity{ID: am.meetingID, App: am.meetingAppName, PID: am.lastMeetingPID, SeenMs: nowMs}, true
	}
	if am.lastMeetingID != "" && am.lastMeetingEndMs > 0 {
		return persistedCallIdentity{ID: am.lastMeetingID, App: am.lastMeetingApp, PID: am.lastMeetingPID, Paused: am.lastMeetingPaused, SeenMs: am.lastMeetingEndMs}, true
	}
	return persistedCallIdentity{}, false
}

// rememberCallIdentity writes the identity a relaunch should resume, or
// removes the file when there is none.
func rememberCallIdentity(am *activityMonitorState, now time.Time) {
	path := lastCallIdentityPath(am)
	if path == "" {
		return
	}
	am.mu.Lock()
	id, ok := callIdentityToPersistLocked(am, now.UnixMilli())
	if ok {
		am.lastCallIdentityWrittenMs = now.UnixMilli()
	}
	am.mu.Unlock()
	if !ok {
		forgetCallIdentity(am)
		return
	}
	data, err := json.Marshal(id)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("[meeting-watcher] could not keep the call identity: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		log.Printf("[meeting-watcher] could not keep the call identity: %v", err)
	}
}

// refreshLiveCallIdentity is the watcher's periodic write while a call runs.
func refreshLiveCallIdentity(am *activityMonitorState, now time.Time) {
	am.mu.Lock()
	due := am.inMeeting && am.meetingID != "" && now.UnixMilli()-am.lastCallIdentityWrittenMs >= lastCallRefreshMs
	am.mu.Unlock()
	if due {
		rememberCallIdentity(am, now)
	}
}

func forgetCallIdentity(am *activityMonitorState) {
	if path := lastCallIdentityPath(am); path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[meeting-watcher] could not clear the call identity: %v", err)
		}
	}
}

// restoreCallIdentity loads the identity a previous run left, at monitor
// start, unless this run already has one. The resume rule in
// onMeetingStarted then joins the call seen after a relaunch to it.
func restoreCallIdentity(am *activityMonitorState) {
	path := lastCallIdentityPath(am)
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var id persistedCallIdentity
	if json.Unmarshal(data, &id) != nil || id.ID == "" || id.App == "" || id.SeenMs <= 0 {
		return
	}
	am.mu.Lock()
	defer am.mu.Unlock()
	if am.inMeeting || am.lastMeetingID != "" {
		return
	}
	am.lastMeetingID = id.ID
	am.lastMeetingApp = id.App
	am.lastMeetingPID = id.PID
	am.lastMeetingPaused = id.Paused
	am.lastMeetingEndMs = id.SeenMs
	log.Printf("[meeting-watcher] call identity from the previous run kept: app=%s last_seen=%s ago", id.App, time.Since(time.UnixMilli(id.SeenMs)).Round(time.Second))
}
