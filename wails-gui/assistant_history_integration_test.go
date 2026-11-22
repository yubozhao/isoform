package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"yansu-agent/agent"
)

type historyRealCLIRunner struct {
	home, workDir string
	calls         int
}

func (r *historyRealCLIRunner) Init(context.Context) error { return nil }
func (r *historyRealCLIRunner) IsReady() bool              { return true }
func (r *historyRealCLIRunner) ExecuteTask(ctx context.Context, input *agent.TaskInput, callback agent.StreamCallback) (string, error) {
	r.calls++
	args := []string{"-p", "--output-format", "text", "--disallowed-tools", "Bash,Read,Write,Edit,Glob,Grep,WebFetch,WebSearch,Task", "--strict-mcp-config", "--setting-sources", "project"}
	// Like the portrait integration, use the CLI's signed-in default model.
	// A specific model can be exercised explicitly without changing the fixture.
	if model := os.Getenv("YANSU_HISTORY_INTEGRATION_MODEL"); model != "" {
		args = append(args, "--model", model)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = r.workDir
	cmd.Env = append(os.Environ(), "HOME="+r.home)
	cmd.Stdin = strings.NewReader(input.Prompt)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return "history-integration", callback(&agent.StreamEvent{Type: "assistant", Content: string(output)})
}
func (r *historyRealCLIRunner) CancelRun(string) error                   { return nil }
func (r *historyRealCLIRunner) RespondToPermission(string, string) error { return nil }
func (r *historyRealCLIRunner) CancelPermission(string)                  {}
func (r *historyRealCLIRunner) Shutdown() error                          { return nil }

// TestAssistantHistoryPassTwoRealModel uses only synthetic sessions and the
// signed-in Claude CLI. It is opt-in and never reads the user's history.
func TestAssistantHistoryPassTwoRealModel(t *testing.T) {
	if os.Getenv("YANSU_CLAUDE_INTEGRATION") != "1" {
		t.Skip("set YANSU_CLAUDE_INTEGRATION=1 to run Claude Code")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not installed")
	}
	if testRealHomeDir == "" {
		t.Fatal("real home unavailable for CLI sign-in")
	}
	roots, _, now := historyFixture(t)
	scan := fixtureHistoryScan(t, roots, now)
	if len(scan.Projects) != 1 {
		t.Fatalf("expected one synthetic project, got %d", len(scan.Projects))
	}
	// The fixture instructions reach Claude only through pass 2's redacted
	// prompt, not through the CLI's automatic project-instruction loading.
	runner := &historyRealCLIRunner{home: testRealHomeDir, workDir: t.TempDir()}
	app := &App{runner: runner}
	project := scan.Projects[0]
	update, called, err := app.runAssistantHistoryProject(context.Background(), project, scan.Sessions[project.ID], "English")
	if err != nil || !called {
		t.Fatalf("pass 2: called=%v err=%v", called, err)
	}
	if runner.calls != 1 {
		t.Fatal("expected one model call and response")
	}
	t.Logf("synthetic project: status=%t openProblem=%t beliefs=%d knowHow=%d", update.Status != "", update.OpenProblem != "", len(update.Beliefs), len(update.KnowHow))
	if update.Status == "" && update.OpenProblem == "" && len(update.Beliefs) == 0 && len(update.KnowHow) == 0 {
		t.Fatal("model returned no usable observations")
	}
}
