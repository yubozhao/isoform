package agent

import (
	"strings"
	"testing"
)

func TestCallerLabelPrefersExplicitCaller(t *testing.T) {
	if got := CallerLabel(&TaskInput{Caller: "brief.writer", Prompt: "secret text"}); got != "brief.writer" {
		t.Fatalf("CallerLabel = %q", got)
	}
}

func TestCallerLabelNamesCodeNotContent(t *testing.T) {
	got := CallerLabel(&TaskInput{Prompt: "secret text"})
	if strings.Contains(got, "secret") || got == "" {
		t.Fatalf("CallerLabel = %q", got)
	}
}

func TestCallerLabelFunc(t *testing.T) {
	cases := map[string]string{
		"wails-gui.(*App).runInterestsPrompt.func1":           "(*App).runInterestsPrompt",
		"yansu-agent/agent.(*RoutingAgentRunner).ExecuteTask": "",
		"runtime.goexit": "",
		"main.(*ActivityTriager).runActivityTriage": "(*ActivityTriager).runActivityTriage",
	}
	for in, want := range cases {
		if got := callerLabelFunc(in); got != want {
			t.Errorf("callerLabelFunc(%q) = %q, want %q", in, got, want)
		}
	}
}
