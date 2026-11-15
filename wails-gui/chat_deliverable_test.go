//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDeliverablePathRejectsRelativePathWithoutSessionDir(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "report.txt"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workDir)

	app := &App{sessionTempDirs: make(map[string]string)}
	_, err := app.resolveDeliverablePath("missing-session", "report.txt")
	if err == nil {
		t.Fatal("expected relative path without a session directory to be rejected")
	}
	if !strings.Contains(err.Error(), "cannot resolve relative path without session directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDeliverablePathResolvesRelativePathAgainstSessionDir(t *testing.T) {
	sessionDir := t.TempDir()
	want := filepath.Join(sessionDir, "report.txt")
	if err := os.WriteFile(want, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{sessionTempDirs: map[string]string{"session": sessionDir}}
	got, err := app.resolveDeliverablePath("session", "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolveDeliverablePath() = %q, want %q", got, want)
	}
}

func TestListChatDeliverableOpenWithAppsRejectsUnopenableExtension(t *testing.T) {
	sessionDir := t.TempDir()
	scriptPath := filepath.Join(sessionDir, "script.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{sessionTempDirs: map[string]string{"session": sessionDir}}
	_, err := app.ListChatDeliverableOpenWithApps("session", scriptPath)
	if err == nil {
		t.Fatal("expected unopenable extension to be rejected")
	}
	if !strings.Contains(err.Error(), "file type is not openable from this UI") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetOrCreateSessionTempDirFallsBackWhenStableDirCannotBeCreated(t *testing.T) {
	fakeHome := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(fakeHome, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	fallbackRoot := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("TMPDIR", fallbackRoot)

	app := &App{sessionTempDirs: make(map[string]string)}
	got := app.getOrCreateSessionTempDir("session")
	if got == "" {
		t.Fatal("expected fallback session directory")
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(got)
	})

	if filepath.Dir(got) != fallbackRoot {
		t.Fatalf("fallback directory = %q, want a child of %q", got, fallbackRoot)
	}
	if !strings.HasPrefix(filepath.Base(got), "yansu-generate-") {
		t.Fatalf("fallback directory %q does not use the expected prefix", got)
	}
	if info, err := os.Stat(got); err != nil {
		t.Fatalf("stat fallback directory: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("fallback path %q is not a directory", got)
	}
	if cached := app.getOrCreateSessionTempDir("session"); cached != got {
		t.Fatalf("cached directory = %q, want %q", cached, got)
	}
}

func TestCleanupFallbackSessionDirsOnShutdownPreservesStableDirs(t *testing.T) {
	stableDir := filepath.Join(t.TempDir(), "stable")
	fallbackDir := filepath.Join(t.TempDir(), "fallback")
	for _, dir := range []string{stableDir, fallbackDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	app := &App{
		sessionTempDirs: map[string]string{
			"stable":   stableDir,
			"fallback": fallbackDir,
		},
		sessionFallbackDirs: map[string]struct{}{
			"fallback": {},
		},
	}
	app.cleanupFallbackSessionDirsOnShutdown()

	if _, err := os.Stat(stableDir); err != nil {
		t.Fatalf("stable directory should survive shutdown: %v", err)
	}
	if _, err := os.Stat(fallbackDir); !os.IsNotExist(err) {
		t.Fatalf("fallback directory should be removed, stat error = %v", err)
	}
	if app.sessionTempDirs != nil || app.sessionFallbackDirs != nil {
		t.Fatal("session directory caches should be cleared on shutdown")
	}
}

func TestCleanupSessionTempDirRemovesStableDirWithoutCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	app := &App{}
	stableDir, err := app.chatSessionWorkDir("session")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stableDir, 0o755); err != nil {
		t.Fatal(err)
	}

	app.CleanupSessionTempDir("session")

	if _, err := os.Stat(stableDir); !os.IsNotExist(err) {
		t.Fatalf("stable directory should be removed with its session, stat error = %v", err)
	}
}

func TestCleanupSessionTempDirRemovesCachedFallbackAndStableDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	app := &App{}
	stableDir, err := app.chatSessionWorkDir("session")
	if err != nil {
		t.Fatal(err)
	}
	fallbackDir := filepath.Join(t.TempDir(), "fallback")
	for _, dir := range []string{stableDir, fallbackDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	app.sessionTempDirs = map[string]string{"session": fallbackDir}
	app.sessionFallbackDirs = map[string]struct{}{"session": {}}

	app.CleanupSessionTempDir("session")

	for _, dir := range []string{stableDir, fallbackDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("session directory %q should be removed, stat error = %v", dir, err)
		}
	}
	if _, ok := app.sessionTempDirs["session"]; ok {
		t.Fatal("session work directory should be removed from cache")
	}
	if _, ok := app.sessionFallbackDirs["session"]; ok {
		t.Fatal("fallback marker should be removed from cache")
	}
}
