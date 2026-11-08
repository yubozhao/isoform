//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const stderrHelperEnv = "YANSU_STDERR_TEST_HELPER"

func TestStderrLogCapturesProcessFailures(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"native", "native descriptor 2 diagnostic"},
		{"panic", "panic: stderr capture panic"},
		{"fatal", "fatal error: sync: unlock of unlocked mutex"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			home := stderrTestHome(t)
			cmd := stderrTestCommand(t, home, tc.mode)
			output, err := cmd.CombinedOutput()
			if (tc.mode == "native") != (err == nil) {
				t.Fatalf("unexpected helper result: %v, %s", err, output)
			}
			if bytes.Contains(output, []byte(tc.want)) {
				t.Fatalf("diagnostic escaped to inherited stderr: %s", output)
			}
			path := filepath.Join(home, ".isoform-agent", "yansu-gui.stderr.log")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Go stderr diagnostic", tc.want} {
				if !bytes.Contains(data, []byte(want)) {
					t.Fatalf("missing %q from captured stderr: %s", want, data)
				}
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("stderr log permissions: %v, %v", info, err)
			}
		})
	}
}

func TestStderrLogPreservesInheritedStderrOnFailure(t *testing.T) {
	home := stderrTestHome(t)
	// Make the intended data directory a file, so setup must fail without
	// replacing the only remaining place to report that failure.
	if err := os.WriteFile(filepath.Join(home, ".isoform-agent"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := stderrTestCommand(t, home, "failure").CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v, %s", err, output)
	}
	if !bytes.Contains(output, []byte("inherited descriptor 2 survives")) {
		t.Fatalf("lost inherited stderr: %s", output)
	}
}

func TestStderrLogRotatesBeforeRedirectAndRetainsHistory(t *testing.T) {
	home := stderrTestHome(t)
	dir := filepath.Join(home, ".isoform-agent")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "yansu-gui.stderr.log")
	for i, suffix := range []string{"", ".old", ".old.2"} {
		if err := os.WriteFile(path+suffix, bytes.Repeat([]byte{byte('a' + i)}, 64), 0600); err != nil {
			t.Fatal(err)
		}
	}
	output, err := stderrTestCommand(t, home, "rotation").CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v, %s", err, output)
	}
	for i, suffix := range []string{".old", ".old.2"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil || !bytes.Equal(data, bytes.Repeat([]byte{byte('a' + i)}, 64)) {
			t.Fatalf("wrong retained stderr %s: %q, %v", suffix, data, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "native descriptor 2 diagnostic\n" {
		t.Fatalf("redirect did not target new stderr file: %q, %v", data, err)
	}
}

func TestStderrLogHonorsExplicitAgentHome(t *testing.T) {
	home := stderrTestHome(t)
	dir := filepath.Join(home, "explicit")
	cmd := stderrTestCommand(t, home, "native")
	cmd.Env = append(cmd.Env, agentHomeEnv+"="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v, %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(dir, "yansu-gui.stderr.log"))
	if err != nil || !bytes.Contains(data, []byte("native descriptor 2 diagnostic")) {
		t.Fatalf("explicit agent home not honored: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".isoform-agent")); !os.IsNotExist(err) {
		t.Fatalf("unexpected default flavor directory: %v", err)
	}
}

func TestStderrLogHelperProcess(t *testing.T) {
	mode := os.Getenv(stderrHelperEnv)
	if mode == "" {
		return
	}
	if mode == "rotation" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		if err := redirectStderrLog(filepath.Join(home, configDirName, "yansu-gui.stderr.log"), 64); err != nil {
			t.Fatal(err)
		}
		_, _ = unix.Write(2, []byte("native descriptor 2 diagnostic\n"))
		os.Exit(0)
	}
	err := initStderrLog()
	if mode == "failure" {
		if err == nil {
			t.Fatal("expected stderr setup to fail")
		}
		_, _ = unix.Write(2, []byte("inherited descriptor 2 survives\n"))
		os.Exit(0)
	}
	if err != nil {
		t.Fatal(err)
	}
	// initLogFile replaces the default logger's inherited stderr reference.
	// Do the same before GC so that reference cannot hide an fd 2 finalizer
	// closing the redirected descriptor before the native or fatal write.
	log.SetOutput(io.Discard)
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
	_, _ = fmt.Fprintln(os.Stderr, "Go stderr diagnostic")
	switch mode {
	case "native":
		_, _ = unix.Write(2, []byte("native descriptor 2 diagnostic\n"))
		os.Exit(0)
	case "panic":
		go func() { panic("stderr capture panic") }()
		select {}
	case "fatal":
		var mutex sync.Mutex
		mutex.Unlock()
	default:
		t.Fatalf("unknown stderr helper mode: %q", mode)
	}
}

func stderrTestHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "stderr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

func stderrTestCommand(t *testing.T, home, mode string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStderrLogHelperProcess$")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "HOME", "USERPROFILE", "GOTRACEBACK", agentHomeEnv, appFlavorEnv, stderrHelperEnv:
		default:
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+home, "USERPROFILE="+home, "GOTRACEBACK=single", appFlavorEnv+"=isoform", stderrHelperEnv+"="+mode)
	return cmd
}
