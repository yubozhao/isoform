//go:build darwin

package tools

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"yansu-agent/agent/authority"
)

// A harmless real executable exercises exec and Unix-socket kernel policy
// without starting SSH sessions, apps, or a machine's existing container engine.
func launcherBoundaryProbe(t *testing.T, project string) string {
	t.Helper()
	probe := filepath.Join(project, "launcher-boundary-probe")
	const source = `#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <sys/socket.h>
#include <sys/un.h>
int main(int argc, char **argv) {
    if (argc == 2) {
        int fd = socket(AF_UNIX, SOCK_STREAM, 0);
        struct sockaddr_un endpoint = {0};
        endpoint.sun_family = AF_UNIX;
        snprintf(endpoint.sun_path, sizeof(endpoint.sun_path), "%s", argv[1]);
        if (connect(fd, (struct sockaddr *)&endpoint, sizeof(endpoint)) < 0) { perror("connect"); return 1; }
        char data[128]; ssize_t n = read(fd, data, sizeof(data));
        if (n > 0) write(1, data, n);
        close(fd); return n > 0 ? 0 : 1;
    }
    printf("launcher-probe-ran:%s\n", argv[0]);
    return 0;
}
`
	compile := exec.Command("/usr/bin/clang", "-x", "c", "-", "-o", probe)
	compile.Stdin = strings.NewReader(source)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile launcher boundary probe: %v: %s", err, output)
	}
	return probe
}

func copyLauncherProbe(t *testing.T, probe, target string) {
	t.Helper()
	data, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestBashLauncherDynamicNamesAndResolvedPATHAreDenied(t *testing.T) {
	home, project := hardBlockHome(t)
	bin := filepath.Join(home, "bin")
	t.Setenv("PATH", bin+":/usr/bin:/bin:/usr/sbin:/sbin")
	probe := launcherBoundaryProbe(t, project)
	tool := NewBashTool()
	for _, name := range bashLauncherNames {
		path := filepath.Join(project, "alternate", name)
		copyLauncherProbe(t, probe, path)
		command := "X=" + strconv.Quote(path) + `; "$X"`
		result, err := tool.Execute(context.Background(), ToolCall{WorkDir: project, Input: map[string]any{"command": command},
			Authority: &AuthorityRuntime{AllowBashWithoutApproval: true}})
		if err != nil {
			t.Fatalf("dynamic %s did not reach kernel policy: %v", name, err)
		}
		if strings.Contains(result.Content, "launcher-probe-ran:") || result.Metadata["exit_code"] == 0 {
			t.Fatalf("dynamic %s escaped exec denial: %s", name, result.Content)
		}
	}
	renamed := filepath.Join(project, "renamed-transport")
	copyLauncherProbe(t, probe, renamed)
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(renamed, filepath.Join(bin, "ssh")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(bin, "ssh"), renamed} {
		result, err := tool.Execute(context.Background(), ToolCall{WorkDir: project,
			Input:     map[string]any{"command": "X=" + strconv.Quote(path) + `; "$X"`},
			Authority: &AuthorityRuntime{AllowBashWithoutApproval: true}})
		if err != nil || strings.Contains(result.Content, "launcher-probe-ran:") || result.Metadata["exit_code"] == 0 {
			t.Fatalf("renamed PATH target escaped: %v: %s", err, result.Content)
		}
	}
	call := ToolCall{WorkDir: project, Input: map[string]any{"command": strconv.Quote(renamed)},
		Authority: &AuthorityRuntime{AllowBashWithoutApproval: true}}
	if _, err := tool.Execute(context.Background(), call); err == nil {
		t.Fatal("literal renamed SSH target skipped mandatory approval")
	}
	call.Authority.Approved = true
	result, err := tool.Execute(context.Background(), call)
	if err != nil || !strings.Contains(result.Content, "launcher-probe-ran:") {
		t.Fatalf("approved renamed SSH target refused: %v: %s", err, result.Content)
	}
	// Approval of SSH must not enable a hidden container launcher in the same
	// command, even when both names are known to the profile.
	docker := filepath.Join(project, "alternate", "docker")
	call.Input["command"] = strconv.Quote(renamed) + "; X=" + strconv.Quote(docker) + `; "$X"`
	result, err = tool.Execute(context.Background(), call)
	if err != nil || strings.Contains(result.Content, "launcher-probe-ran:"+docker) || result.Metadata["exit_code"] == 0 {
		t.Fatalf("SSH approval enabled unrelated Docker execution: %v: %s", err, result.Content)
	}
	call.Input["command"] = strconv.Quote(renamed)
	call.Authority.Context = authority.Context{Unattended: true}
	if _, err := tool.Execute(context.Background(), call); err == nil {
		t.Fatal("unattended launcher used an attended approval")
	}
}

func TestBashLauncherContainerSocketRequiresApproval(t *testing.T) {
	shortGuardFixtureRootForSocket(t)
	home, project := hardBlockHome(t)
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	for _, key := range []string{"DOCKER_HOST", "CONTAINER_HOST", "CONTAINERD_ADDRESS", "BUILDKIT_HOST", "PODMAN_HOST"} {
		t.Setenv(key, "")
	}
	probe := launcherBoundaryProbe(t, project)
	socket := filepath.Join(home, ".docker", "run", "docker.sock")
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			connection.Write([]byte("container-socket-contacted"))
			connection.Close()
		}
	}()
	defer func() { listener.Close(); workers.Wait() }()
	if output, err := exec.Command(probe, socket).CombinedOutput(); err != nil || !strings.Contains(string(output), "container-socket-contacted") {
		t.Fatalf("disposable socket positive control: %v: %s", err, output)
	}
	tool := NewBashTool()
	alias := filepath.Join(project, "socket-alias")
	if err := os.Symlink(socket, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{socket, alias} {
		result, err := tool.Execute(context.Background(), ToolCall{WorkDir: project,
			Input:     map[string]any{"command": strconv.Quote(probe) + " " + strconv.Quote(path)},
			Authority: &AuthorityRuntime{AllowBashWithoutApproval: true}})
		if err != nil || strings.Contains(result.Content, "container-socket-contacted") || result.Metadata["exit_code"] == 0 {
			t.Fatalf("socket IPC escaped its kernel denial: %v: %s", err, result.Content)
		}
	}
	docker := filepath.Join(project, "docker")
	copyLauncherProbe(t, probe, docker)
	call := ToolCall{WorkDir: project, Input: map[string]any{"command": strconv.Quote(docker) + " " + strconv.Quote(socket)},
		Authority: &AuthorityRuntime{AllowBashWithoutApproval: true}}
	if _, err := tool.Execute(context.Background(), call); err == nil {
		t.Fatal("unapproved Docker invocation reached IPC")
	}
	call.Authority.Approved = true
	result, err := tool.Execute(context.Background(), call)
	if err != nil || !strings.Contains(result.Content, "container-socket-contacted") {
		t.Fatalf("approved disposable Docker IPC refused: %v: %s", err, result.Content)
	}
	call.Authority.Context.Unattended = true
	if _, err := tool.Execute(context.Background(), call); err == nil {
		t.Fatal("unattended Docker invocation reached IPC")
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected daemon requests: got %d, want control plus approved call", calls.Load())
	}
}

// macOS caps a unix socket path at 104 bytes (sun_path), and the fixture home
// defaults to a directory under the package, so a deep checkout (a worktree, a
// trial folder) puts $HOME/.docker/run/docker.sock past the limit. Only then,
// root the fixture in the user's cache folder instead: still outside /tmp, as
// guardTestHomeDir requires, and short enough for the socket.
func shortGuardFixtureRootForSocket(t *testing.T) {
	t.Helper()
	const sunPathMax = 104
	// iso-guard-<random>/.docker/run/docker.sock below the root.
	const below = len("/iso-guard-0123456789/.docker/run/docker.sock")
	root := os.Getenv("ISOFORM_TEST_FIXTURE_ROOT")
	if root == "" {
		var err error
		if root, err = os.Getwd(); err != nil {
			t.Fatal(err)
		}
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if len(root)+below < sunPathMax {
		return
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(cache); err == nil {
		cache = resolved
	}
	if len(cache)+below >= sunPathMax {
		t.Fatalf("no fixture root short enough for a unix socket: %s", cache)
	}
	t.Setenv("ISOFORM_TEST_FIXTURE_ROOT", cache)
}
