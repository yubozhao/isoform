package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestBeliefNamesAgentPractice(t *testing.T) {
	for _, tc := range []struct {
		title string
		agent bool
	}{
		{"Routes progress reports to a central hub session", true},
		{"Accepts partial PRs as long as the remaining gaps are called out", true},
		{"You send some work to a Codex worker on a Mac mini", true},
		{"Plans the Agent of You launch with design first", false},
		{"Prefers short replies in the morning", false},
	} {
		if got := beliefNamesAgentPractice(tc.title); got != tc.agent {
			t.Errorf("%q: %t, want %t", tc.title, got, tc.agent)
		}
	}
}

// TestTopNewBeliefsCapAgentPractice: with the section on, at most one
// agent-practice belief takes a New this week spot; the next beliefs fill
// the rest. With every belief unmarked (section off) the order is unchanged.
func TestTopNewBeliefsCapAgentPractice(t *testing.T) {
	beliefs := []AboutYouBelief{
		{ID: "a1", AgentPractice: true}, {ID: "a2", AgentPractice: true}, {ID: "m1"},
		{ID: "a3", AgentPractice: true}, {ID: "m2"}, {ID: "m3"}, {ID: "m4"}, {ID: "m5"},
	}
	if got := strings.Join(topNewBeliefIDs(beliefs, 5), ","); got != "a1,m1,m2,m3,m4" {
		t.Fatalf("top = %s", got)
	}
	for i := range beliefs {
		beliefs[i].AgentPractice = false
	}
	if got := strings.Join(topNewBeliefIDs(beliefs, 5), ","); got != "a1,a2,m1,a3,m2" {
		t.Fatalf("top with the section off = %s", got)
	}
}

func TestAboutYouAgentSectionDefaultsOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(agentHomeEnv, filepath.Join(home, "agent-data"))
	app := newTranslationTestApp(t, &memoryTranslationFakeRunner{})
	if app.aboutYouAgentSectionEnabled() {
		t.Fatal("the agent section is on before anyone turned it on")
	}
	if err := app.store.SetUIState(context.Background(), aboutYouAgentSectionEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if !app.aboutYouAgentSectionEnabled() {
		t.Fatal("the flag did not turn the section on")
	}
}
