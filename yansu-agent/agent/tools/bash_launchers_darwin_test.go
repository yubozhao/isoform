//go:build darwin

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"yansu-agent/agent/authority"
)

func TestBashLauncherThrowawayLaunchdJob(t *testing.T) {
	skipUnlessLaunchdRunsSubmittedJobs(t)
	_, project := hardBlockHome(t)
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	label := fmt.Sprintf("dev.isoform.guard-test.%d.%d", os.Getpid(), time.Now().UnixNano())
	marker := filepath.Join(project, "launchd-marker")
	command := "/bin/launchctl submit -l " + label + " -- /bin/sh -c " + strconv.Quote("printf launcher-approved > "+strconv.Quote(marker))
	defer func() {
		_ = exec.Command("/bin/launchctl", "remove", label).Run()
		if err := exec.Command("/bin/launchctl", "list", label).Run(); err == nil {
			t.Errorf("throwaway launchd job remains registered: %s", label)
		}
	}()
	tool := NewBashTool()
	call := ToolCall{WorkDir: project, Input: map[string]any{"command": command}, Authority: &AuthorityRuntime{
		Evaluator: authority.NewPolicy(nil), AllowBashWithoutApproval: true,
	}}
	if _, err := tool.Execute(context.Background(), call); err == nil {
		t.Fatal("unapproved launchd submission ran with risky-command prompts off")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unapproved launcher wrote its scratch marker: %v", err)
	}
	call.Authority.Approved = true
	result, err := tool.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("approved launchd submission: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(marker)
		if readErr == nil && string(data) == "launcher-approved" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("approved throwaway launchd job did not write marker: %s", result.Content)
}

// skipUnlessLaunchdRunsSubmittedJobs submits a plain control job, outside the
// Bash tool, and skips when launchd never starts it. launchctl submit limits
// the job to the Aqua session, so from an ssh login or a background agent
// (such as the merge queue on the Mac mini) launchd never runs it, even where
// launchctl managername reports Aqua.
func skipUnlessLaunchdRunsSubmittedJobs(t *testing.T) {
	t.Helper()
	label := fmt.Sprintf("dev.isoform.guard-probe.%d.%d", os.Getpid(), time.Now().UnixNano())
	marker := filepath.Join(t.TempDir(), "probe-marker")
	defer func() { _ = exec.Command("/bin/launchctl", "remove", label).Run() }()
	if err := exec.Command("/bin/launchctl", "submit", "-l", label, "--", "/bin/sh", "-c", "printf ran > "+strconv.Quote(marker)).Run(); err != nil {
		t.Skipf("launchctl submit is unavailable here: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
	}
	t.Skip("launchd does not run submitted jobs in this session (not an Aqua login)")
}
