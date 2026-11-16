package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClampInterfaceZoomSnapsToTheRange(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 90}, {80, 90}, {90, 90}, {94, 90}, {95, 100}, {100, 100},
		{112, 110}, {150, 150}, {160, 150}, {1000, 150},
	} {
		if got := clampInterfaceZoom(tc.in); got != tc.want {
			t.Errorf("clampInterfaceZoom(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// The zoom is saved per user in the data folder and read back at launch.
func TestInterfaceZoomIsSavedPerUserAndReadBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YANSU_AGENT_HOME", home)
	interfaceZoomMu.Lock()
	interfaceZoom = interfaceZoomDefault
	interfaceZoomMu.Unlock()
	t.Cleanup(func() {
		interfaceZoomMu.Lock()
		interfaceZoom = interfaceZoomDefault
		interfaceZoomMu.Unlock()
	})

	a := &App{}
	if got := a.GetInterfaceZoom(); got != 100 {
		t.Fatalf("default zoom = %d, want 100", got)
	}
	if got := a.SetInterfaceZoom(130); got != 130 {
		t.Fatalf("SetInterfaceZoom(130) = %d", got)
	}
	if got := a.SetInterfaceZoom(400); got != 150 {
		t.Fatalf("SetInterfaceZoom(400) = %d, want the 150 cap", got)
	}
	b, err := os.ReadFile(filepath.Join(home, interfaceZoomFile))
	if err != nil || strings.TrimSpace(string(b)) != "150" {
		t.Fatalf("saved zoom = %q, %v; want 150", b, err)
	}

	interfaceZoomMu.Lock()
	interfaceZoom = interfaceZoomDefault
	interfaceZoomMu.Unlock()
	loadInterfaceZoomFromDisk()
	if got := a.GetInterfaceZoom(); got != 150 {
		t.Fatalf("zoom after relaunch = %d, want 150", got)
	}

	if err := os.WriteFile(filepath.Join(home, interfaceZoomFile), []byte("not a number"), 0o644); err != nil {
		t.Fatal(err)
	}
	interfaceZoomMu.Lock()
	interfaceZoom = interfaceZoomDefault
	interfaceZoomMu.Unlock()
	loadInterfaceZoomFromDisk()
	if got := a.GetInterfaceZoom(); got != 100 {
		t.Fatalf("unreadable saved zoom gave %d, want 100", got)
	}
}
