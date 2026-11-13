package main

import (
	"testing"
	"time"
)

func TestClaimDerivedRunOncePerCadenceAcrossRestarts(t *testing.T) {
	app := newTranslationTestApp(t, &memoryTranslationFakeRunner{ready: true})
	now := time.Now()
	if !app.claimDerivedRun("purpose_understanding", cadenceDaily, now) {
		t.Fatal("a job that never ran is not due")
	}
	if app.claimDerivedRun("purpose_understanding", cadenceDaily, now.Add(time.Hour)) {
		t.Fatal("a daily job ran twice within the day")
	}
	restarted := &App{store: app.store}
	if restarted.claimDerivedRun("purpose_understanding", cadenceDaily, now.Add(2*time.Hour)) {
		t.Fatal("restarting the app ran a daily job again")
	}
	if !restarted.claimDerivedRun("purpose_understanding", cadenceDaily, now.Add(25*time.Hour)) {
		t.Fatal("a daily job did not run the next day")
	}
	if !app.claimDerivedRun("self_dream", cadenceDaily, now) {
		t.Fatal("jobs share one cadence record")
	}
}
