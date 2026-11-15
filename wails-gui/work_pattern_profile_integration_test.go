package main

import (
	"strings"
	"testing"
)

func qualifiedWorkPatternFixture(
	t *testing.T,
	store *ActivityStore,
) WorkPatternProfileSnapshot {
	t.Helper()
	cutoff := int64(0)
	for session := 0; session < 3; session++ {
		cutoff = saveWorkPatternSession(
			t,
			store,
			"profile-fixture-"+string(rune('a'+session)),
			"done",
			10_000+int64(session)*200_000,
			[]string{
				"Safari",
				"ChatGPT",
				"Terminal",
				"Safari",
				"ChatGPT",
				"Terminal",
			},
		)
	}
	snapshot, _, err := store.EnsureWorkPatternProfileSnapshot(
		cutoff,
		cutoff+1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != WorkPatternProfileStatusQualified {
		t.Fatalf("fixture status=%s", snapshot.Status)
	}
	return snapshot
}

func TestWorkPatternProfileCorrectionRejectsStaleStatement(t *testing.T) {
	store := newTestStore(t)
	snapshot := qualifiedWorkPatternFixture(t, store)
	global := projectionByScope(
		t,
		snapshot,
		WorkPatternScopeGlobal,
		"all",
	)
	_, _, err := store.RecordWorkPatternProfileCorrection(
		RecordWorkPatternProfileCorrectionInput{
			IdempotencyKey:    "stale-work-pattern",
			ProfileSnapshotID: snapshot.ID,
			ScopeKind:         global.ScopeKind,
			ScopeKey:          global.ScopeKey,
			OriginalStatement: "An outdated statement.",
			Correction:        "This applies only to a specific workflow.",
			CreatedAt:         snapshot.SourceCutoffAt + 1,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "stale statement") {
		t.Fatalf("stale correction error=%v", err)
	}
	corrections, listErr := store.ListWorkPatternProfileCorrections(
		snapshot.SourceCutoffAt + 10,
	)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(corrections) != 0 {
		t.Fatalf("stale correction was persisted: %+v", corrections)
	}
}
