package main

import (
	"os"
	"testing"
	"time"
)

// The slot's real build replaces a copy made ahead under the same brief ID. It
// gets its own seal, with its own time and cited sources; a re-seal of either
// copy keeps that copy's seal (seal v2).
func TestRealBuildDoesNotInheritTheMadeAheadSeal(t *testing.T) {
	_, svc, real := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	// The night before, no seal exists yet for the morning's ID.
	if path, ok := svc.store.finishedReadPath(real.BriefID); ok {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	// The made-ahead copy saw less: only the safe note.
	now := svc.now()
	ahead := buildMobileBriefV1(mobileBriefInput{
		AsOf: now, WindowStart: now.Add(-time.Hour), Slot: mobileBriefSlotMorning, Location: time.UTC,
		Learned: []mobileBriefCandidate{{SourceKind: "memory", SourceID: "safe-recall", Title: "Safe recall title", At: now.Add(-time.Minute)}},
	})
	if ahead.BriefID != real.BriefID {
		t.Fatalf("the made-ahead copy has ID %q, the real build %q", ahead.BriefID, real.BriefID)
	}
	ahead.NarrativeStatus = "ok"
	ahead.Narrative = real.Narrative
	ahead.Generation = &MobileBriefGeneration{Reason: mobileBriefGenerationMadeAhead}
	if err := svc.store.save(ahead, svc.now()); err != nil {
		t.Fatal(err)
	}
	if _, _, wrote := svc.seal(t.Context(), ahead); !wrote {
		t.Fatal("could not seal the made-ahead copy")
	}
	aheadSeal := loadSealForTest(t, svc, ahead.BriefID)
	if hasSealRef(aheadSeal, "protected-recall") {
		t.Fatalf("the made-ahead seal should cite only its own sources: %+v evidence=%+v", aheadSeal.SourceRefs, ahead.Evidence)
	}
	// A re-seal of the same made-ahead copy keeps its boundary.
	time.Sleep(5 * time.Millisecond)
	if _, _, wrote := svc.seal(t.Context(), ahead); !wrote {
		t.Fatal("could not re-seal the made-ahead copy")
	}
	if got := loadSealForTest(t, svc, ahead.BriefID); !got.SealedAt.Equal(aheadSeal.SealedAt) {
		t.Fatalf("a re-seal of the same copy moved its seal time from %s to %s", aheadSeal.SealedAt, got.SealedAt)
	}

	time.Sleep(5 * time.Millisecond)
	if err := svc.store.save(real, svc.now()); err != nil {
		t.Fatal(err)
	}
	if _, _, wrote := svc.seal(t.Context(), real); !wrote {
		t.Fatal("could not seal the real build")
	}
	realSeal := loadSealForTest(t, svc, real.BriefID)
	if !realSeal.SealedAt.After(aheadSeal.SealedAt) {
		t.Fatalf("the real build kept the made-ahead seal time %s", realSeal.SealedAt)
	}
	if !hasSealRef(realSeal, "protected-recall") || len(realSeal.Fingerprints) <= len(aheadSeal.Fingerprints) {
		t.Fatalf("the real build's seal kept the made-ahead copy's sources: refs=%d fingerprints=%d", len(realSeal.SourceRefs), len(realSeal.Fingerprints))
	}

	// A re-seal of the real build keeps the real build's seal.
	time.Sleep(5 * time.Millisecond)
	if _, _, wrote := svc.seal(t.Context(), real); !wrote {
		t.Fatal("could not re-seal the real build")
	}
	if got := loadSealForTest(t, svc, real.BriefID); !got.SealedAt.Equal(realSeal.SealedAt) {
		t.Fatalf("a re-seal of the real build moved its seal time from %s to %s", realSeal.SealedAt, got.SealedAt)
	}
}

func loadSealForTest(t *testing.T, svc *mobileBriefService, id string) mobileBriefFinishedRead {
	t.Helper()
	read, found, err := svc.store.loadFinishedRead(id)
	if err != nil || !found {
		t.Fatalf("no seal saved: found=%t err=%v", found, err)
	}
	return read
}

func hasSealRef(read mobileBriefFinishedRead, id string) bool {
	for _, ref := range read.SourceRefs {
		if ref.ID == id {
			return true
		}
	}
	return false
}
