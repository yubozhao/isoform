package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestForgetDumpsAndLogsKeepsOnlyTheCurrentCrashLog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, err := flavorDataDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"ax-dumps/20261115-1200-safari.json", "logs/crash-old-1.txt", fmt.Sprintf("logs/crash-now-%d.txt.pending", os.Getpid())} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("text"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&App{}).forgetDumpsAndLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "ax-dumps")); !os.IsNotExist(err) {
		t.Fatal("the probe dumps must go")
	}
	left, _ := os.ReadDir(filepath.Join(root, "logs"))
	if len(left) != 1 {
		t.Fatalf("logs left = %v, want only this process's crash log", left)
	}
}
