//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/storage"
)

func TestDeliverablesHubBrowserRPCMethodsAreAllowlisted(t *testing.T) {
	methods := []string{
		"ListDeliverables",
		"RecordChatDeliverables",
		"DeliverableFileInfo",
		"ListDeliverableFileOpenWithApps",
	}
	for _, method := range methods {
		if !browserRPCAllowlist[method] {
			t.Errorf("%s must be browser RPC allowlisted", method)
		}
	}
	// Opening a file launches an app on the Mac; only the app window may.
	for _, method := range []string{
		"OpenDeliverableFile",
		"RevealDeliverableFile",
		"OpenDeliverableFileWithApp",
	} {
		if browserRPCAllowlist[method] {
			t.Errorf("%s acts outside the window and must stay off the browser allowlist", method)
		}
	}
}

func TestEmitAgentEventRecordsFinishedChatDeliverableWithoutRenderer(t *testing.T) {
	prev := agentSegmentWriteDelay
	agentSegmentWriteDelay = time.Hour
	t.Cleanup(func() { agentSegmentWriteDelay = prev })

	st, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	workDir := t.TempDir()
	report := filepath.Join(workDir, "report.md")
	if err := os.WriteFile(report, []byte("# Report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveProject(ctx, storage.ProjectRow{ID: "p1", Name: "P", Path: workDir}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSession(ctx, storage.SessionRow{LocalID: "main", ProjectID: "p1", Label: "Launch plan", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	app := &App{store: st}
	t.Cleanup(func() { app.flushAgentSegmentWrites(true) })

	app.emitAgentEvent(AgentEvent{Type: "run_created", Tag: "main", RunID: "r1"})
	app.emitAgentEvent(AgentEvent{
		Type:    "assistant",
		Tag:     "main",
		Content: fmt.Sprintf("Report ready.\n<deliverables>\n%s | Final report\n</deliverables>", report),
	})
	app.emitAgentEvent(AgentEvent{Type: "done", Tag: "main"})

	entries, err := app.ListDeliverables()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("deliverables = %+v, want one finished chat", entries)
	}
	got := entries[0]
	if got.Source != deliverableSourceChat || got.Status != "completed" || got.Title != "Launch plan" || got.Summary != "Report ready." {
		t.Fatalf("finished chat = %+v", got)
	}
	if len(got.Files) != 1 || got.Files[0].Path != report || got.Files[0].Description != "Final report" {
		t.Fatalf("finished chat files = %+v", got.Files)
	}
}
