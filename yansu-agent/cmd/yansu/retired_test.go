package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yansu-agent/internal/client"
	"yansu-agent/internal/config"
)

type rejectTransport struct{ t *testing.T }

func (r rejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Error("retired command attempted a network request")
	return nil, errors.New("unexpected network request")
}

func TestRetiredCommands(t *testing.T) {
	state := t.TempDir()
	t.Setenv("HOME", state)
	t.Setenv(config.AgentHomeEnv, state)
	t.Setenv(config.EnvBaseURL, "https://retired.invalid")
	if err := config.SaveToken("fixture-only"); err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport
	http.DefaultTransport = rejectTransport{t}
	t.Cleanup(func() { http.DefaultTransport = transport })
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Error(err)
		}
	})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetErr(nil) })
	for _, args := range [][]string{{"hook", "run"}, {"push"}, {"push", "--knowledge-only"}, {"push", "--knowledge"}, {"push", "--code"}, {"login"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out.Reset()
			rootCmd.SetArgs(args)
			start := time.Now()
			err := rootCmd.Execute()
			elapsed := time.Since(start)
			if err != nil || out.String() != "The Yansu service is retired; nothing was sent.\n" {
				t.Fatalf("got %q, %v", out.String(), err)
			}
			if elapsed >= 50*time.Millisecond {
				t.Fatalf("handler took %v", elapsed)
			}
		})
	}
	for _, args := range [][]string{{"clone", "acme/product/project"}, {"init", "acme/product/project"}, {"hook", "install"}, {"pull"}, {"sync"}} {
		rootCmd.SetArgs(args)
		if err := rootCmd.Execute(); !errors.Is(err, client.ErrRetired) {
			t.Errorf("%v: %v", args, err)
		}
	}
	// Older supervisors keep the daemon running until shutdown. Exercise startup
	// with a saved fixture token and the same transport that rejects every request.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	oldCtx := daemonStartCmd.Context()
	daemonStartCmd.SetContext(ctx)
	t.Cleanup(func() { daemonStartCmd.SetContext(oldCtx) })
	if err := daemonStartCmd.RunE(daemonStartCmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project", ".something", filepath.Join(".git", "hooks", "post-commit")} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Errorf("unexpected project file %s: %v", name, err)
		}
	}
}
