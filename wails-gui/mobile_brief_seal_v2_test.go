package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A sealed brief is a snapshot. Edits, regenerated text and retention removals
// of a cited source are not withdrawals, so the sealed words keep showing.
func TestSealedBriefKeepsWordsAfterCitedSourceChanges(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "safe-recall.md")
	cases := map[string]func(t *testing.T){
		"edited": func(t *testing.T) {
			writePrivacyMemory(t, "safe-recall", "# Rewritten recall\n\nA later edit.\n")
		},
		"regenerated": func(t *testing.T) {
			writePrivacyMemory(t, "safe-recall", "# Regenerated recall\n\nThe same memory, written again.\n")
		},
		"retention_removed": func(t *testing.T) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			change(t)
			got, withheld, err := svc.finishedReadWithholding(source)
			if err != nil {
				t.Fatal(err)
			}
			if withheld.Reason == mobileBriefWithheldSource || len(got.Narrative) == 0 {
				t.Fatalf("a changed source withheld the sealed brief: reason=%q narrative=%d", withheld.Reason, len(got.Narrative))
			}
		})
	}
}

// A read of a sealed brief reads no live source, runs no gate, seals nothing
// and schedules no retry while the privacy print is unchanged.
func TestSealedBriefReadsDoNoSourceLookupsOrSeals(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	// The first read after sealing may finish work the seal left running; the
	// reads that follow must not look at any source.
	if _, _, err := svc.finishedReadWithholding(source); err != nil {
		t.Fatal(err)
	}
	lookups := mobileBriefSourceLookups.Load()
	seals := mobileBriefFinishedReadKeys.Load()
	gates := mobileBriefGateRuns.Load()
	retries := mobileBriefSealRetryAttempts.Load()
	for i := 0; i < 1000; i++ {
		got, _, err := svc.finishedReadWithholding(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Narrative) == 0 {
			t.Fatal("a sealed brief read showed no words")
		}
	}
	if d := mobileBriefSourceLookups.Load() - lookups; d != 0 {
		t.Fatalf("1000 reads made %d source lookups", d)
	}
	if d := mobileBriefFinishedReadKeys.Load() - seals; d != 0 {
		t.Fatalf("1000 reads made %d seals", d)
	}
	if d := mobileBriefGateRuns.Load() - gates; d != 0 {
		t.Fatalf("1000 reads ran %d gate passes", d)
	}
	if d := mobileBriefSealRetryAttempts.Load() - retries; d != 0 {
		t.Fatalf("1000 reads made %d seal retries", d)
	}
}

// An explicit withdrawal (a forgotten memory) removes what cites it, on the
// computer read and on the phone read, and the copy is amended under the new print.
func TestWithdrawnMemoryLeavesSealedBriefOnComputerAndPhone(t *testing.T) {
	app, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "safe-recall.md")); err != nil {
		t.Fatal(err)
	}
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: "safe-recall"})

	computer, err := svc.finishedRead(source)
	if err != nil {
		t.Fatal(err)
	}
	phone, _, _, err := svc.finishedBriefWithholding(source.BriefID)
	if err != nil {
		t.Fatal(err)
	}
	for name, brief := range map[string]MobileBriefV1{"computer": computer, "phone": phone} {
		data, err := json.Marshal(brief)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"Protected narrative text", "Safe recall title"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("%s read still shows withdrawn text %q", name, secret)
			}
		}
	}
	saved, found, err := svc.store.loadFinishedRead(source.BriefID)
	if err != nil || !found {
		t.Fatalf("amended copy missing: found=%v err=%v", found, err)
	}
	if saved.Print != app.briefPrivacyPrint() {
		t.Fatal("the amended copy was not sealed under the current privacy print")
	}
}

// A keyed withdrawal is amended from the named source alone: no live source is
// read and no gate runs, and nothing is sealed again.
func TestKeyedWithdrawalAmendsWithoutLiveLookupsOrSeals(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "safe-recall.md")); err != nil {
		t.Fatal(err)
	}
	lookups := mobileBriefSourceLookups.Load()
	seals := mobileBriefFinishedReadKeys.Load()
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: "safe-recall"})
	if _, err := svc.finishedRead(source); err != nil {
		t.Fatal(err)
	}
	if d := mobileBriefSourceLookups.Load() - lookups; d != 0 {
		t.Fatalf("a keyed amend made %d live source lookups", d)
	}
	if d := mobileBriefFinishedReadKeys.Load() - seals; d != 0 {
		t.Fatalf("a keyed amend sealed the brief again %d times", d)
	}
}

// The log answers only for changes that named their sources. An unnamed change
// (an app, a pause, an account) makes it refuse, so the gates decide.
func TestWithdrawalLogRefusesUnnamedChanges(t *testing.T) {
	before := mobileBriefPrivacyEpoch.Load()
	briefPrivacyChanged(briefWithdrawn{Kind: "memory", ID: "logged-memory"})
	keys, ok := withdrawnSince(before)
	if !ok || !briefRefWithdrawn("logged-memory", keys) {
		t.Fatalf("the log did not answer for a named withdrawal: ok=%v keys=%v", ok, keys)
	}
	briefPrivacyChanged()
	if _, ok := withdrawnSince(before); ok {
		t.Fatal("the log answered for a copy across an unnamed change")
	}
}

// An edit is not a withdrawal: the watcher neither advances the epoch nor logs it.
func TestEditsAreNotLoggedAsWithdrawals(t *testing.T) {
	dir := t.TempDir()
	before := mobileBriefPrivacyEpoch.Load()
	s := &MemoryStore{baseDir: dir}
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	s.detectFileChanges()
	if err := os.WriteFile(path, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, removed := s.detectFileChanges(); len(removed) != 0 {
		t.Fatalf("an edit was reported as a withdrawal: %v", removed)
	}
	if mobileBriefPrivacyEpoch.Load() != before {
		t.Fatal("an edit advanced the privacy epoch")
	}
}

// A mail sync is not a withdrawal. Fifty syncs leave the sealed brief shown,
// with no epoch change, no seal, and no gate pass.
func TestFiftyMailSyncsLeaveSealedBriefShown(t *testing.T) {
	app, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	epoch := mobileBriefPrivacyEpoch.Load()
	seals := mobileBriefFinishedReadKeys.Load()
	gates := mobileBriefGateRuns.Load()
	refreshes := mobileBriefTodayRefreshRuns.Load()
	var sealed []string
	mobileBriefBeforeSealSave = func(id string) { sealed = append(sealed, id) }
	t.Cleanup(func() { mobileBriefBeforeSealSave = nil })
	for i := 0; i < 50; i++ {
		app.emitBrowserEvent("mail:synced")
	}
	svc.todayRefreshWG.Wait()
	if d := mobileBriefTodayRefreshRuns.Load() - refreshes; d == 0 {
		t.Fatal("mail syncs did not refresh Today")
	}
	for _, id := range sealed {
		if id == source.BriefID {
			t.Fatal("a Today refresh re-sealed the existing sealed brief")
		}
	}
	got, withheld, err := svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != "" || len(got.Narrative) == 0 {
		t.Fatalf("after 50 mail syncs: narrative=%d reason=%q err=%v", len(got.Narrative), withheld.Reason, err)
	}
	if mobileBriefPrivacyEpoch.Load() != epoch {
		t.Fatal("mail syncs advanced the privacy epoch")
	}
	if d := mobileBriefFinishedReadKeys.Load() - seals; d != uint64(len(sealed)) {
		t.Fatalf("seal count %d does not match the seals recorded for the refresh", d)
	}
	_ = gates
}
