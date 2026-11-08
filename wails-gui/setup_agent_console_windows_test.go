//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/internal/platform/cmdutil"
)

func TestWindowsSetupAgentProbeDoesNotAllocateConsole(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "probe.go")
	program := `package main
import("fmt";"os";"syscall")
func main(){
    window,_,_:=syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
    if window!=0 { os.Exit(3) }
    if len(os.Args)>1 && os.Args[1]=="--version" { fmt.Println("fixture 1"); return }
    fmt.Println("Logged in using ChatGPT")
}`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := cmdutil.CommandContext(ctx, "go", "build", "-o", filepath.Join(dir, "codex.exe"), source).CombinedOutput(); err != nil {
		t.Fatalf("build console probe: %v: %s", err, out)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	row := probeSetupAgent(ctx, "codex")
	if !row.Installed || !row.SignedIn {
		t.Fatalf("probe allocated a console or failed: %+v", row)
	}
	if err := os.Rename(filepath.Join(dir, "codex.exe"), filepath.Join(dir, "probe.exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "codex.cmd"), []byte("@echo off\r\n\"%~dp0probe.exe\" %*\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	row = probeSetupAgent(ctx, "codex")
	if !row.Installed || !row.SignedIn {
		t.Fatalf("npm .cmd shim was not probed silently: %+v", row)
	}
}

func TestWindowsDownloadedGatewaySmoke(t *testing.T) {
	bin := os.Getenv("ISOFORM_TEST_GATEWAY_BIN")
	if bin == "" {
		t.Skip("set ISOFORM_TEST_GATEWAY_BIN to a verified Magpie CLI fixture")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	s := testSidecar(t, bin)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Start(ctx); err != nil {
		log, _ := os.ReadFile(s.logPath)
		t.Fatalf("gateway start: %v\n%s", err, log)
	}
	if !s.Healthy() || s.Status().Version != "0.1.3315" {
		t.Fatalf("gateway not ready: %+v", s.Status())
	}
}

func TestWindowsGatewayDownloadSmoke(t *testing.T) {
	if os.Getenv("ISOFORM_TEST_GATEWAY_DOWNLOAD") != "1" {
		t.Skip("set ISOFORM_TEST_GATEWAY_DOWNLOAD=1 to verify the pinned release download")
	}
	entry, err := magpieDownloadEntry("windows/amd64")
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	if err := downloadMagpieTool(ToolDownloadPlan{Entry: entry, Folder: folder}, nil); err != nil {
		t.Fatal(err)
	}
	s := testSidecar(t, filepath.Join(folder, "magpie-cli.exe"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.Healthy() {
		t.Fatal("downloaded gateway did not become healthy")
	}
}
