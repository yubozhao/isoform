//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"wails-gui/internal/destinations"
)

func TestQuittingEndsTheResearchProcessGroup(t *testing.T) {
	t.Cleanup(func() {
		researchProcs.mu.Lock()
		researchProcs.stopping = false
		researchProcs.mu.Unlock()
	})
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & sleep 30")
	setResearchProcessGroup(cmd)
	if err := startResearchProcess(cmd); err != nil {
		t.Fatal(err)
	}
	defer forgetResearchProcess(cmd)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopResearchProcesses()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the research process outlived quitting")
	}
	if err := syscall.Kill(-cmd.Process.Pid, 0); err == nil {
		t.Fatal("a process the research CLI started is still running")
	}
	if err := startResearchProcess(exec.Command("/bin/true")); !errors.Is(err, errResearchStopping) {
		t.Fatalf("a research run started while quitting: %v", err)
	}
}

func TestAChildHoldingStdoutDoesNotHoldTheSearch(t *testing.T) {
	calls := observeDestinationCalls(t)
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\n" +
		`echo '{"type":"result","subtype":"success","result":"done"}'` + "\n" +
		"sleep 30 &\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := researchClaudeBinary
	researchClaudeBinary = script
	t.Cleanup(func() { researchClaudeBinary = previous })
	started := time.Now()
	final, _, err := runClaudeResearch(context.Background(), "PROMPT")
	if err != nil || final != "done" {
		t.Fatalf("final %q err %v", final, err)
	}
	expectDestination(t, calls, destinations.AssistantSearch, "claude_code")
	if took := time.Since(started); took > 10*time.Second {
		t.Fatalf("the search waited %s on a child that kept stdout open", took)
	}
}

func TestResearchProcessNonzeroExitOverridesSuccessOutput(t *testing.T) {
	previous := researchClaudeBinary
	t.Cleanup(func() { researchClaudeBinary = previous })
	for _, tc := range []struct {
		name   string
		result string
		stderr string
	}{
		{"success output", `{"type":"result","subtype":"success","result":"<news_json>[]</news_json>"}`, "The user denied permission to execute the search."},
		{"long stderr", `{"type":"result","subtype":"success","result":"<news_json>[]</news_json>"}`, strings.Repeat("Progress output.\n", 100) + "The user denied permission to execute the search."},
		{"generic stream error", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Search failed"}`, strings.Repeat("Progress output.\n", 100) + "Blocked: this run cannot search."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "claude")
			body := "#!/bin/sh\nprintf '%s\\n' '" + tc.result + "'\nprintf '%s\\n' '" + tc.stderr + "' >&2\nexit 77\n"
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			researchClaudeBinary = script
			final, meta, err := runClaudeResearch(context.Background(), "PROMPT")
			if err == nil || final != "" || !strings.Contains(err.Error(), "exit status 77") || !meta.Denied || meta.FailureReason == "" || !strings.Contains(err.Error(), meta.FailureReason) {
				t.Fatalf("nonzero denial lost: final %q, meta %+v, err %v", final, meta, err)
			}
		})
	}
}

func TestResearchProcessRecoveredSearchIgnoresEarlierStderrDenial(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"s1","name":"WebSearch","input":{"query":"news"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"s1","content":"Found https://example.com/story"}]}}`,
		`{"type":"result","subtype":"success","result":"done"}`,
	}, "\n")
	body := "#!/bin/sh\nprintf '%s\\n' '" + stream + "'\nprintf '%s\\n' 'Blocked: an earlier attempt was denied.' >&2\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := researchClaudeBinary
	researchClaudeBinary = script
	t.Cleanup(func() { researchClaudeBinary = previous })
	final, meta, err := runClaudeResearch(context.Background(), "PROMPT")
	if err != nil || final != "done" || !meta.searched() {
		t.Fatalf("recovered search failed: final %q, meta %+v, err %v", final, meta, err)
	}
}

func TestResearchProcessCompleteSuccessSurvivesCleanupExit(t *testing.T) {
	const answer = `<news_json>[{"title":"A real story","url":"https://example.com/story"}]</news_json>`
	previous := researchClaudeBinary
	t.Cleanup(func() { researchClaudeBinary = previous })
	for _, tc := range []struct {
		name       string
		searched   bool
		denied     bool
		streamFail bool
		wantOK     bool
	}{
		{name: "complete successful answer", searched: true, wantOK: true},
		{name: "no successful search"},
		{name: "real denial", searched: true, denied: true},
		{name: "terminal stream failure", searched: true, streamFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stream []string
			if tc.searched {
				stream = append(stream,
					`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"s1","name":"WebSearch","input":{"query":"news"}}]}}`,
					`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"s1","content":"Found https://example.com/story"}]}}`)
			}
			result, err := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": answer})
			if err != nil {
				t.Fatal(err)
			}
			stream = append(stream, string(result))
			if tc.streamFail {
				stream = append(stream, `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"The session failed"}`)
			}
			stderr := "Cleanup could not remove a temporary file."
			if tc.denied {
				stderr = "Blocked: this run cannot search."
			}
			script := filepath.Join(t.TempDir(), "claude")
			body := "#!/bin/sh\nprintf '%s\\n' '" + strings.Join(stream, "\n") + "'\nprintf '%s\\n' '" + stderr + "' >&2\nexit 1\n"
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			researchClaudeBinary = script
			final, meta, err := runClaudeResearch(context.Background(), "PROMPT")
			if !tc.wantOK {
				if err == nil || final != "" {
					t.Fatalf("failed run was accepted: final %q, meta %+v, err %v", final, meta, err)
				}
				return
			}
			if err != nil || final != answer || !meta.hasURL("https://example.com/story") {
				t.Fatalf("cleanup exit discarded a complete search: final %q, meta %+v, err %v", final, meta, err)
			}
		})
	}
}
