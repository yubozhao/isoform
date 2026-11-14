package agent_test

import (
	"strings"
	"testing"

	"yansu-agent/agent"
)

//go:noinline
func labelHelper() string { return agent.CallerLabel(&agent.TaskInput{}) }

//go:noinline
func labelAsker() string { return labelHelper() }

// A registered shared helper is skipped, so the label names the code that
// asked for the call.
func TestCallerLabelSkipsRegisteredHelpers(t *testing.T) {
	before := labelAsker()
	if !strings.HasPrefix(before, "labelHelper<labelAsker") {
		t.Fatalf("unregistered label = %q", before)
	}
	agent.RegisterCallerLabelHelpers("labelHelper")
	if got := labelAsker(); !strings.HasPrefix(got, "labelAsker<") || strings.Contains(got, "labelHelper") {
		t.Fatalf("label with the helper registered = %q", got)
	}
}
