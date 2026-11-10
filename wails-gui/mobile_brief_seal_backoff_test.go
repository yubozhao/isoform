package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type sealFailureLogCounter struct{ count atomic.Int64 }

func (c *sealFailureLogCounter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("cannot be sealed (reason=source_missing)")) {
		c.count.Add(1)
	}
	return len(p), nil
}

func TestBriefSealAllowedTransitions(t *testing.T) {
	states := []briefSealState{"", briefSealPending, briefSealSealing, briefSealRetryWait, briefSealSealed, briefSealWithheld, briefSealFailed}
	allowed := map[briefSealState][]briefSealState{
		"":                 {briefSealPending},
		briefSealPending:   {briefSealSealing, briefSealFailed},
		briefSealSealing:   {briefSealRetryWait, briefSealSealed, briefSealWithheld, briefSealFailed},
		briefSealRetryWait: {briefSealSealing, briefSealPending, briefSealFailed, briefSealRetryWait},
		briefSealSealed:    {briefSealPending}, briefSealWithheld: {briefSealPending}, briefSealFailed: {briefSealPending},
	}
	for _, from := range states {
		for _, to := range states {
			want := false
			for _, target := range allowed[from] {
				if target == to {
					want = true
				}
			}
			got, ok := transitionBriefSeal(from, to)
			if ok != want || (ok && got != to) || (!ok && got != from) {
				t.Errorf("%s -> %s: got %s, %v; allowed %v", from, to, got, ok, want)
			}
		}
	}
}

func TestSealRetryRecoversAfterBackoffWithoutAnotherRead(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	path, _ := svc.store.finishedReadPath(brief.BriefID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	oldDelays, oldBackoff := sealRetryDelays, sealRetryBackoff
	sealRetryDelays, sealRetryBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}, 150*time.Millisecond
	t.Cleanup(func() {
		svc.Stop()
		sealRetryDelays, sealRetryBackoff = oldDelays, oldBackoff
		briefPolicyPending.Store(false)
	})
	briefPolicyPending.Store(true)
	before := mobileBriefSealRetryAttempts.Load()
	svc.scheduleSealRetry(brief)
	svc.wg.Wait()
	if got := mobileBriefSealRetryAttempts.Load() - before; got != 4 {
		t.Fatalf("got %d attempts, want 4", got)
	}
	read, _, err := svc.finishedReadWithholding(brief)
	if err != nil || len(read.Narrative) != 0 || len(read.Cards) != 0 {
		t.Fatalf("unsealed brief was shown: %v", err)
	}
	// The source becomes available during the pause. No read or event schedules the next attempt.
	briefPolicyPending.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	sealed := false
	for time.Now().Before(deadline) {
		if _, found := readSealedBrief(svc.store, brief.BriefID, svc.app.briefPrivacyPrint(), svc.readDay()); found {
			sealed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sealed {
		t.Fatal("the timer did not seal the brief")
	}
	read, _, err = svc.finishedReadWithholding(brief)
	if err != nil || len(read.Cards) == 0 {
		t.Fatalf("brief did not become visible after retry: cards=%d err=%v", len(read.Cards), err)
	}
}

func TestSealRetryResumesAfterRestart(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	path, _ := svc.store.finishedReadPath(brief.BriefID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	oldDelays, oldBackoff := sealRetryDelays, sealRetryBackoff
	sealRetryDelays, sealRetryBackoff = []time.Duration{time.Millisecond}, 200*time.Millisecond
	var restarted *mobileBriefService
	t.Cleanup(func() {
		if restarted != nil {
			restarted.Stop()
		}
		svc.Stop()
		sealRetryDelays, sealRetryBackoff = oldDelays, oldBackoff
		briefPolicyPending.Store(false)
	})
	briefPolicyPending.Store(true)
	svc.scheduleSealRetry(brief)
	svc.wg.Wait()
	if _, err := os.Stat(svc.sealStatePath(brief.BriefID)); err != nil {
		t.Fatalf("retry state was not saved: %v", err)
	}
	svc.Stop()
	briefPolicyPending.Store(false)
	restarted = &mobileBriefService{app: svc.app, store: svc.store, now: svc.now, location: svc.location}
	restarted.resumeSealRetries()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, found := readSealedBrief(restarted.store, brief.BriefID, restarted.app.briefPrivacyPrint(), restarted.readDay()); found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("restart did not resume the retry")
}

func TestSealMissingSourceFailsOnceAndReopensOnSourceEvent(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	if err := os.Remove(filepath.Join(svc.store.dir, brief.BriefID+".json")); err != nil {
		t.Fatal(err)
	}
	oldDelays := sealRetryDelays
	sealRetryDelays = []time.Duration{time.Millisecond}
	var output sealFailureLogCounter
	previousLog := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { svc.Stop(); log.SetOutput(previousLog); sealRetryDelays = oldDelays })
	svc.scheduleSealRetry(brief)
	svc.wg.Wait()
	key := briefSealJobKey{svc, brief.BriefID}
	briefSealJobs.Lock()
	job := briefSealJobs.jobs[key]
	failed := job != nil && job.record.State == briefSealFailed && job.record.Reason == "source_missing"
	briefSealJobs.Unlock()
	if !failed {
		t.Fatal("missing source did not reach terminal failure")
	}
	before := mobileBriefSealRetryAttempts.Load()
	svc.scheduleSealRetry(brief)
	svc.wg.Wait()
	if mobileBriefSealRetryAttempts.Load() != before {
		t.Fatal("terminal failure retried without a source event")
	}
	if n := output.count.Load(); n != 1 {
		t.Fatalf("terminal failure was logged %d times, want once", n)
	}
	if err := svc.store.saveDocument(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	sealedPath, _ := svc.store.finishedReadPath(brief.BriefID)
	if err := os.Remove(sealedPath); err != nil {
		t.Fatal(err)
	}
	svc.sealStored(brief.BriefID)
	if _, found := readSealedBrief(svc.store, brief.BriefID, svc.app.briefPrivacyPrint(), svc.readDay()); !found {
		t.Fatal("source event did not reopen the failure")
	}
}

func TestSealRetryUsesChangedSourceAndPrivacyEpoch(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	path, _ := svc.store.finishedReadPath(brief.BriefID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	oldDelays, oldBackoff := sealRetryDelays, sealRetryBackoff
	sealRetryDelays, sealRetryBackoff = []time.Duration{time.Millisecond}, 200*time.Millisecond
	t.Cleanup(func() {
		svc.Stop()
		sealRetryDelays, sealRetryBackoff = oldDelays, oldBackoff
		briefPolicyPending.Store(false)
	})
	briefPolicyPending.Store(true)
	svc.scheduleSealRetry(brief)
	svc.wg.Wait()
	changed := brief
	changed.Slot += "-changed"
	if err := svc.store.saveDocument(changed, svc.now()); err != nil {
		t.Fatal(err)
	}
	before := mobileBriefSealRetryAttempts.Load()
	svc.scheduleSealRetry(changed)
	svc.wg.Wait()
	if mobileBriefSealRetryAttempts.Load() <= before {
		t.Fatal("changed source did not wake the retry")
	}
	briefPrivacyChanged()
	epoch := mobileBriefPrivacyEpoch.Load()
	briefPolicyPending.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		read, found, err := svc.store.loadFinishedRead(changed.BriefID)
		if err == nil && found && read.Source == mobileBriefSourceDigest(changed) && read.Epoch == epoch {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("retry did not seal the changed source under the current privacy epoch")
}

func TestSealStateFollowsBriefRetention(t *testing.T) {
	store := newMobileBriefStore(t.TempDir())
	if err := os.MkdirAll(store.dir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	oldID := now.AddDate(0, 0, -mobileBriefRetentionDays-1).Format("2006-01-02") + "-morning"
	newID := now.Format("2006-01-02") + "-morning"
	for _, id := range []string{oldID, newID} {
		if err := os.WriteFile(filepath.Join(store.dir, id+mobileBriefSealStateExt), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store.sweepLocked(now)
	if _, err := os.Stat(filepath.Join(store.dir, oldID+mobileBriefSealStateExt)); !os.IsNotExist(err) {
		t.Fatal("expired seal state was retained")
	}
	if _, err := os.Stat(filepath.Join(store.dir, newID+mobileBriefSealStateExt)); err != nil {
		t.Fatalf("current seal state was removed: %v", err)
	}
}

func TestSealUnreadableCopySchedulesRecovery(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	path, _ := svc.store.finishedReadPath(brief.BriefID)
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	oldDelays := sealRetryDelays
	sealRetryDelays = []time.Duration{time.Millisecond}
	t.Cleanup(func() { svc.Stop(); sealRetryDelays = oldDelays })
	if _, _, _, err := svc.sealedReadOrPending(brief); err == nil {
		t.Fatal("unreadable seal did not report an error")
	}
	svc.wg.Wait()
	if _, found := readSealedBrief(svc.store, brief.BriefID, svc.app.briefPrivacyPrint(), svc.readDay()); !found {
		t.Fatal("unreadable seal was not retried")
	}
}

func TestSealReplacementDuringReadUsesOnePrivacySnapshot(t *testing.T) {
	app, svc, brief := newStoredMobileBriefPrivacyFixture(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	mobileBriefAfterSealedRead = func(id string) {
		mobileBriefAfterSealedRead = nil
		if id != brief.BriefID {
			t.Fatalf("unexpected brief %s", id)
		}
		if err := app.AddSensitiveApp("com.example.test", "TestApp"); err != nil {
			t.Fatal(err)
		}
		svc.wg.Wait()
	}
	t.Cleanup(func() { svc.Stop(); mobileBriefAfterSealedRead = nil })
	read, _, err := svc.finishedReadWithholding(brief)
	if err != nil {
		t.Fatal(err)
	}
	assertMobileBriefDeliveryFiltered(t, read, false)
}

func TestSealRetryPersistsInitialWaitDeadline(t *testing.T) {
	_, svc, brief := cardRig(t)
	svc.todayRefreshWG.Wait()
	svc.wg.Wait()
	oldDelays := sealRetryDelays
	sealRetryDelays = []time.Duration{time.Millisecond, time.Second}
	briefPolicyPending.Store(true)
	t.Cleanup(func() { svc.Stop(); sealRetryDelays = oldDelays; briefPolicyPending.Store(false) })
	svc.scheduleSealRetry(brief)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(svc.sealStatePath(brief.BriefID))
		var record briefSealRecord
		if err == nil && json.Unmarshal(data, &record) == nil && record.State == briefSealRetryWait {
			if record.Digest != mobileBriefSourceDigest(brief) || record.Reason != mobileBriefWithheldNotReady || !record.NextAttempt.After(time.Now()) {
				t.Fatalf("incomplete retry wait: %+v", record)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("initial retry wait was not recorded")
}
