package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An edit to a cited memory is not a withdrawal: the reseal keeps the words and
// the original provenance.
func TestMobileBriefSealedSourceProvenance(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	saved, found, err := svc.store.loadFinishedRead(source.BriefID)
	if err != nil || !found || saved.SealedAt.IsZero() {
		t.Fatalf("missing seal time: found=%v err=%v", found, err)
	}
	key := mobileBriefSourceKey("memory", "safe-recall")
	if len(saved.Fingerprints[key]) != 67 {
		t.Fatalf("missing memory fingerprint: %q", saved.Fingerprints[key])
	}
	originalTime := saved.SealedAt
	originalPrint := saved.Fingerprints[key]

	// The check does not depend on the watcher or the privacy epoch.
	writePrivacyMemory(t, "safe-recall", "# Corrected offline\n")
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	later := originalTime.Add(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "safe-recall.md"), later, later); err != nil {
		t.Fatal(err)
	}
	// An edit is not a withdrawal: the reseal keeps the words and the original provenance.
	if got, withheld, wrote := svc.seal(t.Context(), source); !wrote || withheld.Reason == mobileBriefWithheldSource || len(got.Narrative) == 0 {
		t.Fatalf("reseal withheld an unchanged brief after an edit: wrote=%v reason=%q", wrote, withheld.Reason)
	}
	saved, _, err = svc.store.loadFinishedRead(source.BriefID)
	if err != nil || !saved.SealedAt.Equal(originalTime) || saved.Fingerprints[key] != originalPrint {
		t.Fatal("reseal replaced the original provenance")
	}
}

func TestMobileBriefEditedSourceDoesNotBypassAnotherWithdrawal(t *testing.T) {
	app, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	saved, _, err := svc.store.loadFinishedRead(source.BriefID)
	if err != nil {
		t.Fatal(err)
	}
	writePrivacyMemory(t, "safe-recall", "# Corrected recall\n")
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	later := saved.SealedAt.Add(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "safe-recall.md"), later, later); err != nil {
		t.Fatal(err)
	}
	withdrawWithoutAmendment(t, app)
	read, _, err := svc.finishedReadWithholding(source)
	if err != nil {
		t.Fatal(err)
	}
	// The edited memory is not withdrawn, so its sealed words may still show (an
	// edit does not withhold). The withdrawn items must not show.
	for _, forbidden := range []string{"Protected recall title", "Protected knowledge title", "Protected meeting title"} {
		if strings.Contains(string(mustJSON(t, read)), forbidden) {
			t.Errorf("an edit to one source bypassed another withdrawal: %s", forbidden)
		}
	}
}

func TestMobileBriefLaterEvidenceDoesNotChangeOriginalCitations(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	before, withheld, err := svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != "" || len(before.Narrative) == 0 {
		t.Fatalf("missing initial text: %q %v", withheld.Reason, err)
	}
	writePrivacyMemory(t, "later-note", "# Later note\n")
	source.Evidence = append(source.Evidence, MobileBriefRef{Ref: "later", SourceKind: "memory", SourceID: "later-note", Label: "Later note"})
	if err := svc.store.saveDocument(source, svc.now()); err != nil {
		t.Fatal(err)
	}
	if _, withheld, wrote := svc.seal(t.Context(), source); !wrote || withheld.Reason != "" {
		t.Fatalf("later evidence withdrew the original text: %q", withheld.Reason)
	}
	after, withheld, err := svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != "" || len(after.Narrative) == 0 || after.Narrative[0].Text != before.Narrative[0].Text {
		t.Fatalf("later evidence hid the original text: %q %v", withheld.Reason, err)
	}
}
