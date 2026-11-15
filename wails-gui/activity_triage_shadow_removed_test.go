package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The shadow agent is removed (docs/rework-2026-11.md): a triage run carries
// no shadow observer state.
func TestTriageRunNoLongerExpectsTheShadowObserver(t *testing.T) {
	group := sessionGroup{Sessions: []ActivitySessionRow{{ID: "session-1", AppNames: `["Editor"]`}}}
	raw, err := json.Marshal(newGroupPipelineRun(group, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "shadowObserver") {
		t.Fatalf("triage run still carries shadow observer state: %s", raw)
	}
}
