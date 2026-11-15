package main

import (
	"testing"
	"time"
)

// The shadow agent is being removed (docs/rework-2026-11.md): a triage run no
// longer expects or waits on the shadow task observer.
func TestTriageRunNoLongerExpectsTheShadowObserver(t *testing.T) {
	group := sessionGroup{Sessions: []ActivitySessionRow{{ID: "session-1", AppNames: `["Editor"]`}}}
	run := newGroupPipelineRun(group, time.Now())
	if run.ShadowObserverExpected || run.ShadowObserverDispatchStatus != "" ||
		run.ShadowObserverAdmission != "" {
		t.Fatalf("triage run still expects the shadow observer: %+v", run)
	}
}
