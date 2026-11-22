package main

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"wails-gui/internal/agentinbox"
)

// TestAgentFilesAreQuarantined: each file an agent posts carries the
// quarantine flag, so macOS asks before running it from Finder.
func TestAgentFilesAreQuarantined(t *testing.T) {
	app := newAgentJoinTestApp(t)
	names := app.keepAgentFiles(agentinbox.Item{ID: "h1", SourceKind: "brief_item", SourceID: "b1"}, []agentinbox.File{{Name: "run.terminal", Text: "x"}})
	if len(names) != 1 {
		t.Fatalf("kept %v", names)
	}
	dir, _ := app.agentFilesDir("h1")
	buf := make([]byte, 128)
	n, err := unix.Getxattr(filepath.Join(dir, "run.terminal"), quarantineXattr, buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "0081;") {
		t.Fatalf("quarantine: %q %v", buf[:max(n, 0)], err)
	}
}
