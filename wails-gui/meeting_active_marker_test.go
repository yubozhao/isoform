package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMeetingActiveMarkerNamesThisProcessWhileACallIsOn guards the marker the
// app runner reads before relaunching the app: present with this pid and the
// meeting app while a call is on, gone once it ends.
func TestMeetingActiveMarkerNamesThisProcessWhileACallIsOn(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	path, err := meetingActiveMarkerPath()
	if err != nil {
		t.Fatal(err)
	}
	noteMeetingActiveMarker("Google Meet\nx")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, fmt.Sprintf("pid=%d\n", os.Getpid())) || !strings.Contains(body, "app=Google Meet x\n") {
		t.Fatalf("marker = %q", body)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("marker mode: %v %v", info.Mode(), err)
	}
	noteMeetingActiveMarker("")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("marker left after the call: %v", err)
	}
	noteMeetingActiveMarker("") // ending twice is harmless
}
