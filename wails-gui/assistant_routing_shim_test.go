package main

import (
	"os"
	"path/filepath"
	"testing"
)

// On a new data folder the CLI is installed by a launch task that can finish
// after the assistant router is built. The router then held an empty shim path
// and reported Claude Code and Codex unavailable ("Isoform tool shim is
// unavailable") until the app restarted, so the first launch's brief and chats
// went to the backup model (daily trial, 2026-11-15).
func TestRouterKeepsTheCLIPathBeforeItIsInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YANSU_AGENT_HOME", home)
	if got := installedYansuCLIPath(); got != "" {
		t.Fatalf("installed path before install = %q", got)
	}
	got := assistantToolShimPath()
	if !filepath.IsAbs(got) || got != filepath.Join(home, yansuCLIInstallRoot, bundledYansuCLIExecutableName()) {
		t.Fatalf("router shim path before install = %q", got)
	}
}

// With a bundle to install from, the router installs the CLI before handing
// out its path: a path the CLI was not at yet failed the first run's
// tool-server check and paused Claude Code for ten minutes (daily trial,
// 2026-11-15, 18:47:13 check, 18:47:16 install).
func TestRouterInstallsTheCLIBeforeHandingOutItsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YANSU_AGENT_HOME", home)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(filepath.Dir(exe), yansuCLIBundleDirName, "bin", bundledYansuCLIExecutableName())
	if _, err := os.Stat(bundle); err == nil {
		t.Skip("a bundle already sits beside the test binary")
	}
	if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(filepath.Dir(exe), yansuCLIBundleDirName)) })
	if err := os.WriteFile(bundle, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := assistantToolShimPath()
	if got != filepath.Join(home, yansuCLIInstallRoot, bundledYansuCLIExecutableName()) {
		t.Fatalf("router shim path = %q", got)
	}
	if installedYansuCLIPath() != got {
		t.Fatalf("the CLI is not installed at the path the router handed out: %q", got)
	}
}
