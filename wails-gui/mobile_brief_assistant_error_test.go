package main

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"yansu-agent/agent"
	"yansu-agent/agent/native"
)

func TestMobileBriefAssistantFailureAttribution(t *testing.T) {
	for _, tc := range []struct {
		backend string
		status  int
		want    string
	}{
		{"claude-code", 401, "claude-code"},
		{"codex", 403, "codex"},
		{"native", 401, ""},
		{"", 401, ""},
		{"codex", 500, ""},
	} {
		t.Run(tc.backend+"/"+strconv.Itoa(tc.status), func(t *testing.T) {
			cause := &native.HTTPError{Status: tc.status}
			err := mobileBriefAttributeAssistantError(cause, tc.backend)
			var attributed *mobileBriefAssistantError
			if errors.As(err, &attributed) != (tc.want != "") {
				t.Fatalf("attribution = %v", err)
			}
			if attributed != nil && attributed.assistant != tc.want {
				t.Fatalf("assistant = %q, want %q", attributed.assistant, tc.want)
			}
			if !errors.Is(err, cause) {
				t.Fatal("lost the original error")
			}
		})
	}
}

type briefRouteErrorRunner struct {
	backends []string
}

func (*briefRouteErrorRunner) Init(context.Context) error { return nil }
func (*briefRouteErrorRunner) IsReady() bool              { return true }
func (r *briefRouteErrorRunner) ExecuteTask(_ context.Context, _ *agent.TaskInput, emit agent.StreamCallback) (string, error) {
	for _, backend := range r.backends {
		if err := emit(&agent.StreamEvent{Type: "backend_route", Backend: backend}); err != nil {
			return "", err
		}
	}
	return "", &native.HTTPError{Status: 401}
}
func (*briefRouteErrorRunner) CancelRun(string) error                   { return nil }
func (*briefRouteErrorRunner) RespondToPermission(string, string) error { return nil }
func (*briefRouteErrorRunner) CancelPermission(string)                  {}
func (*briefRouteErrorRunner) Shutdown() error                          { return nil }

func TestMobileBriefAssistantFailureUsesLastRoute(t *testing.T) {
	for _, tc := range []struct {
		name     string
		backends []string
		want     string
	}{
		{"assistant", []string{"codex"}, "codex"},
		{"fallback", []string{"codex", "native"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{runner: &briefRouteErrorRunner{backends: tc.backends}}
			_, err := app.mobileBriefModelCall(context.Background(), "test")
			var attributed *mobileBriefAssistantError
			if errors.As(err, &attributed) != (tc.want != "") {
				t.Fatalf("error = %v", err)
			}
			if attributed != nil && attributed.assistant != tc.want {
				t.Fatalf("assistant = %q", attributed.assistant)
			}
		})
	}
}

func TestMobileBriefNativeAuthRequiresSignedOutPrimary(t *testing.T) {
	cause := &native.HTTPError{Status: 401}
	for _, tc := range []struct{ primary, state, want string }{
		{"codex", "signed_out", "codex"}, {"claude-code", "signed_out", "claude-code"},
		{"codex", "signed_in", ""}, {"codex", "", ""}, {"native", "signed_out", ""},
	} {
		err := mobileBriefAttributeNativeAuth(cause, tc.primary, tc.state)
		var attributed *mobileBriefAssistantError
		if errors.As(err, &attributed) != (tc.want != "") {
			t.Fatalf("%+v: %v", tc, err)
		}
		if attributed != nil && attributed.assistant != tc.want {
			t.Fatalf("%+v: %v", tc, attributed.assistant)
		}
		if !errors.Is(err, cause) {
			t.Fatal("lost cause")
		}
	}
}
