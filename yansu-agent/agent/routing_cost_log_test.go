package agent

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

// A finished turn logs one cost line that names its caller, so cost tracking
// can join each model call to the code that asked for it (the brief writer
// among them).
func TestRoutingCostLineNamesTheCaller(t *testing.T) {
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	external := &fakeAgentRunner{runID: "external", ready: true, events: []*StreamEvent{
		{Type: "text", Content: "ok"},
		{Type: "done", Usage: &Usage{InputTokens: 1200, OutputTokens: 300, CostUSD: 0.0723}},
	}}
	native := &fakeAgentRunner{ready: true}
	router := NewRoutingAgentRunner(native, external, signedInPolicy)
	if _, err := router.ExecuteTask(context.Background(), &TaskInput{Caller: "brief.writer", Prompt: "private material"}, func(*StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var cost string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, "[routing] cost ") {
			cost = line
		}
	}
	for _, want := range []string{"caller=brief.writer", "cost=$0.0723", "input_tokens=1200", "output_tokens=300"} {
		if !strings.Contains(cost, want) {
			t.Fatalf("cost line %q lacks %q (log %q)", cost, want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "private material") {
		t.Fatal("the prompt reached the log")
	}
}
