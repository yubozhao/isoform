//go:build offfeatures

// Feature being deleted (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBundledCuaSkillContentWithSocket(t *testing.T) {
	binary := "/tmp/CuaDriver.app/Contents/MacOS/cua-driver"
	socket := "/tmp/yansu-cua-daemon.sock"
	upstreamDir := "/tmp/CuaDriver.app/Contents/Resources/Skills/cua-driver"
	body := bundledCuaSkillContent(binary, socket, upstreamDir)

	if !strings.Contains(body, bundledCuaSkillManaged) {
		t.Fatal("content missing managed sentinel")
	}
	if !strings.Contains(body, "name: cua\n") {
		t.Fatalf("content missing frontmatter name: %s", body)
	}
	if !strings.Contains(body, bundledCuaSkillDescription) {
		t.Fatalf("content missing frontmatter description: %s", body)
	}
	if !strings.Contains(body, "--socket "+socket) && !strings.Contains(body, "--socket '"+socket+"'") {
		t.Fatalf("content missing --socket flag: %s", body)
	}
	if !strings.Contains(body, binary) {
		t.Fatalf("content missing binary path: %s", body)
	}
	if !strings.Contains(body, "yansu policy:") {
		t.Fatal("content missing policy proxy section")
	}
	// The seed must teach the LLM about the new AX-empty fallback chain:
	// branch on ax_status, use visual_index for v* refs, fall back to
	// pixel clicks through background pid/window delivery. Without
	// these the LLM defaults to the old "give up and ask the user to
	// bring the app forward" behaviour for CEF/Electron/Chinese apps.
	if !strings.Contains(body, "ax_status") {
		t.Fatal("content missing ax_status branching section")
	}
	if !strings.Contains(body, "visual_index") {
		t.Fatal("content missing visual_index fallback section")
	}
	if !strings.Contains(body, "process-level background activation") {
		t.Fatal("content missing background activation explanation")
	}
	for _, sub := range []string{"SKILL.md", "WEB_APPS.md", "RECORDING.md", "TESTS.md"} {
		if !strings.Contains(body, filepath.Join(upstreamDir, sub)) {
			t.Fatalf("content missing upstream pointer for %s: %s", sub, body)
		}
	}
}

func TestBundledCuaSkillContentWithoutSocket(t *testing.T) {
	binary := "/tmp/CuaDriver.app/Contents/MacOS/cua-driver"
	upstreamDir := "/tmp/CuaDriver.app/Contents/Resources/Skills/cua-driver"
	body := bundledCuaSkillContent(binary, "", upstreamDir)

	if !strings.Contains(body, "daemon socket unknown") {
		t.Fatalf("content missing socket-unknown fallback: %s", body)
	}
	// The literal --socket flag is still mentioned in the docs/failure-mode
	// sections; what we want to ensure is that no concrete socket path was
	// rendered into the invocation prefix or the "Daemon socket:" bullet.
	if strings.Contains(body, "Daemon socket: `/") {
		t.Fatalf("content should omit Daemon socket bullet when socket path is empty: %s", body)
	}
}
