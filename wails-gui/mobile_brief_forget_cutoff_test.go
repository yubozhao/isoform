package main

import (
	"context"
	"errors"
	"math"
	"testing"
)

// While Forget everything runs (or just finished), a brief build saves
// nothing and a seal is not written: a build that began before the job can't
// put back what it gathered.
func TestBriefBuildAndSealSaveNothingWhileForgetting(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	before := len(svc.store.briefs())
	forgetQuietUntil.Store(math.MaxInt64)
	t.Cleanup(func() { forgetQuietUntil.Store(0) })

	if _, err := svc.generate(context.Background(), mobileBriefSlotEvening, mobileBriefReasonSchedule); !errors.Is(err, errForgetQuiet) {
		t.Fatalf("a build saved while forgetting: %v", err)
	}
	if got := len(svc.store.briefs()); got != before {
		t.Fatalf("a brief was stored while forgetting: %d briefs, was %d", got, before)
	}
	if _, _, wrote, _ := svc.sealResult(context.Background(), brief); wrote {
		t.Fatal("a seal was written while forgetting")
	}
}

// A privacy change made while a seal is put together is newer than the seal:
// the seal is not written under the epoch it missed.
func TestSealNotWrittenAfterAPrivacyChangeDuringIt(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	mobileBriefBeforeSealSave = func(string) { briefPrivacyChanged() }
	t.Cleanup(func() { mobileBriefBeforeSealSave = nil })
	if _, _, wrote, _ := svc.sealResult(context.Background(), brief); wrote {
		t.Fatal("a seal was written under an epoch a privacy change had passed")
	}
}
