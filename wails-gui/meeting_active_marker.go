package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The meeting-active marker tells tools outside the app that a call is on,
// so nothing restarts the app and stops the recording mid-call (the dogfood
// app runner defers a relaunch while it is present and its pid lives). It
// holds the pid, the meeting app's name and the start time; it is written
// when the activity monitor enters a meeting and removed when it leaves one.
// Nothing else reads it, and a stale file from a crash names a dead pid.
const meetingActiveMarkerName = "meeting-active"

var meetingActiveMarkerMu sync.Mutex

func meetingActiveMarkerPath() (string, error) {
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, meetingActiveMarkerName), nil
}

// noteMeetingActiveMarker writes the marker for app, or removes it when app
// is empty.
func noteMeetingActiveMarker(app string) {
	path, err := meetingActiveMarkerPath()
	if err != nil {
		return
	}
	meetingActiveMarkerMu.Lock()
	defer meetingActiveMarkerMu.Unlock()
	if app == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[meeting-marker] remove failed: %v", err)
		}
		return
	}
	name := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, app)
	body := fmt.Sprintf("pid=%d\napp=%s\nsince=%d\n", os.Getpid(), name, time.Now().Unix())
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		log.Printf("[meeting-marker] write failed: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[meeting-marker] write failed: %v", err)
	}
}
