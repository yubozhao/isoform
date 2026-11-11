package main

import (
	"bytes"
	"os"
	"testing"
)

// markOldRuleCopy rewrites a brief's sealed copy as the old edited-source rule
// left it: hidden as a whole, source_withdrawn, with no seal rule.
func markOldRuleCopy(t *testing.T, svc *mobileBriefService, source MobileBriefV1) {
	t.Helper()
	saved, found, err := svc.store.loadFinishedRead(source.BriefID)
	if err != nil || !found {
		t.Fatalf("no sealed copy to mark: %v", err)
	}
	var old MobileBriefV1
	if err := unmarshalStoredMobileBrief(saved.Brief, &old); err != nil {
		t.Fatal(err)
	}
	old.Narrative, old.NarrativeStatus = []MobileBriefPara{}, "unavailable"
	raw, err := marshalStoredMobileBrief(old)
	if err != nil {
		t.Fatal(err)
	}
	saved.Brief, saved.Rule, saved.WithheldReason, saved.WithheldKind = raw, 0, mobileBriefWithheldSource, "memory"
	if err := svc.store.saveFinishedRead(source.BriefID, saved); err != nil {
		t.Fatal(err)
	}
}

// An old-rule copy hidden only because its cited source was edited comes back
// shown when the migration seals it again from the built brief. The result is
// kept, and a second migration does not touch it.
func TestOldRuleCopyWithEditedSourceComesBackShown(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	markOldRuleCopy(t, svc, source)
	writePrivacyMemory(t, "safe-recall", "# Rewritten recall\n\nA later edit.\n")
	if got, withheld, err := svc.finishedReadWithholding(source); err != nil || len(got.Narrative) != 0 || withheld.Reason != mobileBriefWithheldSource {
		t.Fatalf("fixture: the old copy is not hidden before the migration: narrative=%d reason=%q err=%v", len(got.Narrative), withheld.Reason, err)
	}
	if n := svc.migrateOldSealedCopies(); n != 1 {
		t.Fatalf("migrated %d copies, want 1", n)
	}
	got, withheld, err := svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != "" || len(got.Narrative) == 0 {
		t.Fatalf("after the migration an edited source still hides the brief: narrative=%d reason=%q err=%v", len(got.Narrative), withheld.Reason, err)
	}
	if saved, _, _ := svc.store.loadFinishedRead(source.BriefID); saved.Rule != mobileBriefSealRule {
		t.Fatalf("migrated copy has rule %d, want %d", saved.Rule, mobileBriefSealRule)
	}
	if n := svc.migrateOldSealedCopies(); n != 0 {
		t.Fatalf("a second migration touched %d copies; it must run once", n)
	}
}

// The migration reaches only briefs of the last few local days. An old-rule copy
// of a brief five days old is left exactly as it was: the withdrawal log may no
// longer hold what that brief cites, so the gates could not be trusted to bring
// its text back.
func TestOldRuleCopyFiveDaysOldIsLeftUntouched(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	source.LocalDate = svc.now().AddDate(0, 0, -5).Format("2006-01-02")
	if err := svc.store.saveDocument(source, svc.now()); err != nil {
		t.Fatal(err)
	}
	markOldRuleCopy(t, svc, source)
	path, ok := svc.store.finishedReadPath(source.BriefID)
	if !ok {
		t.Fatal("no copy path")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := svc.migrateOldSealedCopies(); n != 0 {
		t.Fatalf("migrated %d copies; a five-day-old copy must be left alone", n)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a five-day-old old-rule copy was rewritten by the migration")
	}
}

// An old-rule copy whose cited source was deleted (a recorded keyed withdrawal)
// comes back without the parts that cite it, and the rest of the brief is kept.
func TestOldRuleCopyWithWithdrawnSourceStaysWithoutThoseParts(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	var safeRef string
	for _, ref := range source.Evidence {
		if ref.SourceID == "safe-recall" {
			safeRef = ref.Ref
		}
	}
	if safeRef == "" {
		t.Fatal("fixture does not cite the safe memory")
	}
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	markOldRuleCopy(t, svc, source)
	// Record the keyed withdrawal without starting the asynchronous amend, which
	// would seal the copy before the migration runs.
	briefPrivacyChanged(briefWithdrawn{Kind: "memory", ID: "safe-recall"})
	if n := svc.migrateOldSealedCopies(); n != 1 {
		t.Fatalf("migrated %d copies, want 1", n)
	}
	got, _, err := svc.finishedReadWithholding(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, para := range got.Narrative {
		for _, ref := range para.Refs {
			if ref == safeRef {
				t.Fatalf("a paragraph citing the withdrawn source survived the migration: %+v", para)
			}
		}
	}
	for _, ref := range got.Evidence {
		if ref.SourceID == "safe-recall" {
			t.Fatalf("evidence of the withdrawn source survived the migration: %+v", ref)
		}
	}
	if saved, _, _ := svc.store.loadFinishedRead(source.BriefID); saved.Rule != mobileBriefSealRule {
		t.Fatalf("migrated copy has rule %d, want %d", saved.Rule, mobileBriefSealRule)
	}
}
