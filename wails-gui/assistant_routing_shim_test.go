package main

import (
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
