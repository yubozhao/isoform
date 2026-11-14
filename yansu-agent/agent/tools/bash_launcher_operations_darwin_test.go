//go:build darwin

package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yansu-agent/agent/authority"
)

// Query the active kernel policy without opening a browser, starting an app or
// sending an AppleEvent to a user's running application.
func TestBashLauncherOperationPolicy(t *testing.T) {
	_, project := hardBlockHome(t)
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	checker := filepath.Join(project, "sandbox-operation-check")
	const source = `#include <stdio.h>
#include <unistd.h>
extern int sandbox_check(pid_t, const char *, int, ...);
int main(void) {
    const char *operations[] = {"job-creation", "lsopen", "appleevent-send"};
    for (int i = 0; i < 3; i++)
        printf("%s=%d\n", operations[i], sandbox_check(getpid(), operations[i], 0));
    return 0;
}
`
	compile := exec.Command("/usr/bin/clang", "-x", "c", "-", "-o", checker, "-lsandbox")
	compile.Stdin = strings.NewReader(source)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile kernel policy probe: %v: %s", err, output)
	}
	profile, err := bashSeatbeltProfile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, command        string
		approved, unattended bool
		job, lsopen, event   int
	}{
		{name: "ordinary", command: "printf safe", job: 1, lsopen: 1, event: 1},
		{name: "read-only launchctl", command: "launchctl list", job: 1, lsopen: 1, event: 1},
		{name: "literal URL", command: "open https://example.com", job: 1, event: 1},
		{name: "URL with another command", command: "open https://example.com; printf safe", job: 1, lsopen: 1, event: 1},
		{name: "approved launchctl", command: "launchctl submit -l test -- /bin/true", approved: true, lsopen: 1, event: 1},
		{name: "approved osascript", command: "osascript ./test.scpt", approved: true, lsopen: 1},
		{name: "approved automator", command: "automator ./test.workflow", approved: true, lsopen: 1},
		{name: "approved open", command: "open -a Terminal", approved: true, job: 1},
		{name: "unattended launchctl", command: "launchctl submit -l test -- /bin/true", approved: true, unattended: true, job: 1, lsopen: 1, event: 1},
		{name: "approved unknown binary", command: "./renamed-tool submit -l test -- /bin/true", approved: true, job: 1, lsopen: 1, event: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := ToolCall{WorkDir: project, Input: map[string]any{"command": test.command}, Authority: &AuthorityRuntime{
				Approved: test.approved, Context: authority.Context{Unattended: test.unattended},
			}}
			output, err := exec.Command("/usr/bin/sandbox-exec", "-p", profile+bashLauncherSeatbeltRules(call), checker).CombinedOutput()
			if err != nil {
				t.Fatalf("kernel operation policy: %v: %s", err, output)
			}
			want := fmt.Sprintf("job-creation=%d\nlsopen=%d\nappleevent-send=%d\n", test.job, test.lsopen, test.event)
			if string(output) != want {
				t.Fatalf("kernel operation policy = %q, want %q", output, want)
			}
		})
	}
}

func TestBashRenamedLaunchctlCannotCreateJob(t *testing.T) {
	_, project := hardBlockHome(t)
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	launcher := filepath.Join(project, "renamed-tool")
	binary, err := os.ReadFile("/bin/launchctl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	// A moved platform binary is killed by code-signing validation on macOS.
	// Ad-hoc sign only this disposable copy so the control proves it can run.
	if output, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", launcher).CombinedOutput(); err != nil {
		t.Fatalf("sign disposable launchctl copy: %v: %s", err, output)
	}
	// Some macOS releases (26.x) ship launchctl as arm64e only and kill any
	// ad-hoc-signed arm64e binary at exec, sandbox or not, so no runnable
	// renamed copy can exist there and this end-to-end case has nothing to
	// prove. The kernel rule itself stays covered on every release by
	// TestBashLauncherOperationPolicy's "approved unknown binary" case.
	if err := exec.Command(launcher, "version").Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
				t.Skipf("this macOS kills an ad-hoc-signed copy of launchctl outside any sandbox: %v", err)
			}
		}
		t.Fatalf("unsandboxed launchctl copy failed: %v", err)
	}
	labelPrefix := fmt.Sprintf("dev.isoform.guard-copy.%d.%d", os.Getpid(), time.Now().UnixNano())
	controlLabel, blockedLabel := labelPrefix+".control", labelPrefix+".blocked"
	defer func() {
		for _, label := range []string{controlLabel, blockedLabel} {
			_ = exec.Command("/bin/launchctl", "remove", label).Run()
			if err := exec.Command("/bin/launchctl", "list", label).Run(); err == nil {
				t.Errorf("throwaway copied-launchctl job remains registered: %s", label)
			}
		}
	}()
	controlMarker := filepath.Join(project, "copy-control-marker")
	controlArgs := []string{"-p", "(version 1)\n(allow default)\n(allow job-creation)", launcher,
		"submit", "-l", controlLabel, "--", "/bin/sh", "-c", "printf control > " + strconv.Quote(controlMarker)}
	if output, err := exec.Command("/usr/bin/sandbox-exec", controlArgs...).CombinedOutput(); err != nil {
		t.Fatalf("copied launchctl positive control: %v: %s", err, output)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(controlMarker); err == nil && string(data) == "control" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if data, err := os.ReadFile(controlMarker); err != nil || string(data) != "control" {
		t.Fatalf("copied launchctl did not write its positive-control marker: %q, %v", data, err)
	}
	_ = exec.Command("/bin/launchctl", "remove", controlLabel).Run()
	blockedMarker := filepath.Join(project, "copy-blocked-marker")
	command := strconv.Quote(launcher) + " submit -l " + blockedLabel + " -- /bin/sh -c " + strconv.Quote("printf blocked > "+strconv.Quote(blockedMarker))
	result, err := NewBashTool().Execute(context.Background(), ToolCall{WorkDir: project, Input: map[string]any{"command": command}, Authority: &AuthorityRuntime{
		Evaluator: authority.NewPolicy(nil), AllowBashWithoutApproval: true,
	}})
	if err != nil {
		t.Fatalf("renamed launcher must reach the OS policy: %v", err)
	}
	if result.Metadata["exit_code"] == 0 {
		t.Fatalf("renamed launchctl escaped job-creation denial: %s", result.Content)
	}
	if _, err := os.Stat(blockedMarker); !os.IsNotExist(err) {
		t.Fatalf("renamed launchctl wrote its denied marker: %v", err)
	}
	if err := exec.Command("/bin/launchctl", "list", blockedLabel).Run(); err == nil {
		t.Fatal("renamed launchctl registered a denied launchd job")
	}
}
