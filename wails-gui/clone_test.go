package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepoNameFromURL(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		expect string
	}{
		{name: "https with .git", url: "https://github.com/org/repo.git", expect: "repo"},
		{name: "https without .git", url: "https://github.com/org/repo", expect: "repo"},
		{name: "ssh format", url: "git@github.com:org/repo.git", expect: "repo"},
		{name: "ssh without .git", url: "git@github.com:org/repo", expect: "repo"},
		{name: "nested path", url: "https://gitlab.com/group/subgroup/repo.git", expect: "repo"},
		{name: "bare name", url: "repo", expect: "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repoNameFromURL(tt.url)
			if got != tt.expect {
				t.Fatalf("repoNameFromURL(%q) = %q, want %q", tt.url, got, tt.expect)
			}
		})
	}
}

func TestIsLocalGitHost(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		expect bool
	}{
		{name: "localhost", url: "https://localhost/git/a/b/c.git", expect: true},
		{name: "ipv4 loopback", url: "https://127.0.0.1/git/a/b/c.git", expect: true},
		{name: "ipv6 loopback", url: "https://[::1]/git/a/b/c.git", expect: true},
		{name: "remote host", url: "https://example.invalid/git/a/b/c.git", expect: false},
		{name: "malformed url", url: "localhost/git/a/b/c.git", expect: false},
		{name: "with token userinfo", url: "https://tok@localhost/git/a/b/c.git", expect: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLocalGitHost(tt.url)
			if got != tt.expect {
				t.Fatalf("isLocalGitHost(%q) = %v, want %v", tt.url, got, tt.expect)
			}
		})
	}
}

func TestGitCloneTimeoutFromEnv(t *testing.T) {
	fallback := 3 * time.Minute

	// Unset variable should return fallback.
	t.Setenv("YANSU_GIT_CLONE_TIMEOUT_MS", "")
	if got := gitCloneTimeoutFromEnv(fallback); got != fallback {
		t.Fatalf("gitCloneTimeoutFromEnv(unset) = %s, want %s", got, fallback)
	}

	// Valid override should be used.
	t.Setenv("YANSU_GIT_CLONE_TIMEOUT_MS", "120000")
	if got := gitCloneTimeoutFromEnv(fallback); got != 2*time.Minute {
		t.Fatalf("gitCloneTimeoutFromEnv(valid) = %s, want %s", got, 2*time.Minute)
	}

	// Invalid and non-positive values should fall back.
	t.Setenv("YANSU_GIT_CLONE_TIMEOUT_MS", "bad-value")
	if got := gitCloneTimeoutFromEnv(fallback); got != fallback {
		t.Fatalf("gitCloneTimeoutFromEnv(invalid) = %s, want %s", got, fallback)
	}

	t.Setenv("YANSU_GIT_CLONE_TIMEOUT_MS", "-1")
	if got := gitCloneTimeoutFromEnv(fallback); got != fallback {
		t.Fatalf("gitCloneTimeoutFromEnv(non-positive) = %s, want %s", got, fallback)
	}
}

func TestGitCloneTimeoutFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	fallback := 4 * time.Minute

	// Missing config should return fallback.
	if got := gitCloneTimeoutFromConfig(fallback); got != fallback {
		t.Fatalf("gitCloneTimeoutFromConfig(missing) = %s, want %s", got, fallback)
	}

	if err := saveConfig(yansuConfig{GitCloneTimeoutMS: 7 * 60 * 1000}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	if got := gitCloneTimeoutFromConfig(fallback); got != 7*time.Minute {
		t.Fatalf("gitCloneTimeoutFromConfig(valid) = %s, want %s", got, 7*time.Minute)
	}

	if err := saveConfig(yansuConfig{GitCloneTimeoutMS: -1}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	if got := gitCloneTimeoutFromConfig(fallback); got != fallback {
		t.Fatalf("gitCloneTimeoutFromConfig(non-positive) = %s, want %s", got, fallback)
	}
}

func TestGitCloneTimeoutDefaultDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YANSU_GIT_CLONE_TIMEOUT_MS", "")

	if got := gitCloneTimeout(); got != 0 {
		t.Fatalf("gitCloneTimeout(default) = %s, want no timeout", got)
	}
}

func TestGitCloneTimeoutPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := saveConfig(yansuConfig{GitCloneTimeoutMS: 15 * 60 * 1000}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	// Config should apply when env override is absent.
	t.Setenv("YANSU_GIT_CLONE_TIMEOUT_MS", "")
	if got := gitCloneTimeout(); got != 15*time.Minute {
		t.Fatalf("gitCloneTimeout(config) = %s, want %s", got, 15*time.Minute)
	}

	// Env override should take precedence over config.
	t.Setenv("YANSU_GIT_CLONE_TIMEOUT_MS", "120000")
	if got := gitCloneTimeout(); got != 2*time.Minute {
		t.Fatalf("gitCloneTimeout(env) = %s, want %s", got, 2*time.Minute)
	}
}

func TestCloneReachabilityTarget(t *testing.T) {
	tests := []struct {
		name string
		url  string
		host string
		port string
		ok   bool
	}{
		{
			name: "https default port",
			url:  "https://github.com/org/repo.git",
			host: "github.com",
			port: "443",
			ok:   true,
		},
		{
			name: "http default port",
			url:  "http://example.com/org/repo.git",
			host: "example.com",
			port: "80",
			ok:   true,
		},
		{
			name: "explicit port",
			url:  "https://git.example.com:8443/org/repo.git",
			host: "git.example.com",
			port: "8443",
			ok:   true,
		},
		{
			name: "ssh scheme",
			url:  "ssh://git@github.com/org/repo.git",
			host: "github.com",
			port: "22",
			ok:   true,
		},
		{
			name: "scp-style ssh",
			url:  "git@github.com:org/repo.git",
			host: "github.com",
			port: "22",
			ok:   true,
		},
		{
			name: "local path",
			url:  "/tmp/repo",
			ok:   false,
		},
		{
			name: "empty",
			url:  "",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, ok := cloneReachabilityTarget(tt.url)
			if ok != tt.ok {
				t.Fatalf("cloneReachabilityTarget(%q) ok = %v, want %v", tt.url, ok, tt.ok)
			}
			if host != tt.host {
				t.Fatalf("cloneReachabilityTarget(%q) host = %q, want %q", tt.url, host, tt.host)
			}
			if port != tt.port {
				t.Fatalf("cloneReachabilityTarget(%q) port = %q, want %q", tt.url, port, tt.port)
			}
		})
	}
}

func TestFriendlyCloneErrorTimeoutMessage(t *testing.T) {
	timeout := 8 * time.Minute
	// Simulate git killed by context (signal: killed) after deadline exceeded —
	// this is the case that should produce a timeout message. An "exit status"
	// error means git exited on its own, so we shouldn't mask the real error.
	err := friendlyCloneError(
		errors.New("signal: killed"),
		"",
		context.DeadlineExceeded,
		timeout,
	)
	if err == nil {
		t.Fatal("friendlyCloneError() returned nil")
	}

	msg := err.Error()
	if !strings.Contains(msg, "clone timed out after 8m0s") {
		t.Fatalf("timeout message mismatch: %q", msg)
	}
	if !strings.Contains(msg, "YANSU_GIT_CLONE_TIMEOUT_MS") {
		t.Fatalf("expected timeout override hint, got: %q", msg)
	}
}

func TestCleanupFailedCloneDir(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "partial-clone")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "marker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cleanupFailedCloneDir(targetDir)

	if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
		t.Fatalf("expected cleanupFailedCloneDir to remove %s, stat err=%v", targetDir, err)
	}
}

func TestRetiredProjectBindings(t *testing.T) {
	a := &App{}
	for _, raw := range []string{"https://dashboard.yansu.ai/git/org/project", "ssh://git@git.yansu.ai/project", "git@GIT.YANSU.AI.:org/repo", "yansu.ai:org/repo"} {
		if err := validateCloneURL(raw); err != errNoYansuAccount {
			t.Errorf("retired Git remote: %v", err)
		}
	}
	if _, err := a.CloneProject("org", "product", "project", "", "", "", ""); err != errNoYansuAccount {
		t.Fatal(err)
	}
	if _, err := a.PullKnowledge(""); err != errNoYansuAccount {
		t.Fatal(err)
	}
	if _, err := a.PushKnowledge(""); err != errNoYansuAccount {
		t.Fatal(err)
	}
	if err := a.OpenProjectURL(""); err != errNoYansuAccount {
		t.Fatal(err)
	}
	if _, err := a.FetchProjectContext(""); err != errNoYansuAccount {
		t.Fatal(err)
	}
}
