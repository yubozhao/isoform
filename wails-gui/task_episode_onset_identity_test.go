package main

import (
	"encoding/json"
	"testing"
)

func createHistoricalOnsetEpisodeFixture(
	t *testing.T,
	store *ActivityStore,
	episodeID string,
	anchor taskEpisodeOnsetAnchor,
	detectedAt int64,
) TaskEpisode {
	t.Helper()
	onset := TaskOnsetEvidence{
		Ref:        anchor.Ref,
		Kind:       "keyboard",
		SourceKind: anchor.SourceKind,
		SourceID:   anchor.SourceID,
		OccurredAt: anchor.ObservedAt,
		Summary:    "The user began composing a work update.",
	}
	payload, err := json.Marshal(onset)
	if err != nil {
		t.Fatal(err)
	}
	input := taskEpisodeFixture(
		episodeID,
		anchor.ObservedAt,
		detectedAt,
		TaskEpisodeStatusActive,
	)
	input.InitialHypothesis.TaskHypothesis = "Compose a customer update"
	input.InitialHypothesis.DesiredResultHypothesis =
		"A customer update is ready"
	input.InitialEvidence = []TaskEpisodeEvidence{{
		ID:          episodeID + "-onset",
		Role:        TaskEpisodeEvidenceOnset,
		SourceKind:  anchor.SourceKind,
		SourceID:    anchor.SourceID,
		Summary:     onset.Summary,
		ObservedAt:  anchor.ObservedAt,
		PayloadJSON: string(payload),
	}}
	input.InitialHypothesis.EvidenceIDs = []string{
		input.InitialEvidence[0].ID,
	}
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create historical onset episode: added=%v err=%v", added, err)
	}
	return episode
}

func TestTaskEpisodeOnsetIdentityUsesOnlyRevisionOnePrimaryEvidence(
	t *testing.T,
) {
	store := newTestStore(t)
	primary := taskEpisodeOnsetAnchor{
		Ref:        "event:primary",
		SourceKind: "activity_event",
		SourceID:   "primary-source",
		ObservedAt: 2_000,
	}
	alternative := taskEpisodeOnsetAnchor{
		Ref:        "event:alternative",
		SourceKind: "activity_event",
		SourceID:   "alternative-source",
		ObservedAt: 2_000,
	}
	primaryPayload, _ := json.Marshal(TaskOnsetEvidence{
		Ref: primary.Ref, Kind: "keyboard", SourceKind: primary.SourceKind,
		SourceID: primary.SourceID, OccurredAt: primary.ObservedAt,
		Summary: "Primary onset",
	})
	alternativePayload, _ := json.Marshal(TaskOnsetEvidence{
		Ref: alternative.Ref, Kind: "focus", SourceKind: alternative.SourceKind,
		SourceID: alternative.SourceID, OccurredAt: alternative.ObservedAt,
		Summary: "Alternative onset",
	})
	input := taskEpisodeFixture(
		"primary-versus-alternative",
		2_000,
		2_100,
		TaskEpisodeStatusActive,
	)
	input.InitialEvidence = []TaskEpisodeEvidence{
		{
			ID: "primary-evidence", Role: TaskEpisodeEvidenceOnset,
			SourceKind: primary.SourceKind, SourceID: primary.SourceID,
			Summary: "Primary onset", ObservedAt: 2_000,
			PayloadJSON: string(primaryPayload),
		},
		{
			ID: "alternative-evidence", Role: TaskEpisodeEvidenceOnset,
			SourceKind: alternative.SourceKind, SourceID: alternative.SourceID,
			Summary: "Alternative onset", ObservedAt: 2_000,
			PayloadJSON: string(alternativePayload),
		},
	}
	input.InitialHypothesis.EvidenceIDs = []string{"primary-evidence"}
	input.InitialHypothesis.Alternatives = []TaskEpisodeHypothesisAlternative{{
		Kind:        "task",
		Statement:   "The user may be reviewing instead of drafting.",
		Confidence:  0.25,
		EvidenceIDs: []string{"alternative-evidence"},
	}}
	if _, added, err := store.CreateTaskEpisode(input); err != nil || !added {
		t.Fatalf("create mixed-evidence episode: added=%v err=%v", added, err)
	}
	if _, found, err := store.FindTaskEpisodeByOnsetAnchor(
		alternative,
		alternative.ObservedAt,
	); err != nil || found {
		t.Fatalf("alternative-only anchor aliased primary episode: found=%v err=%v", found, err)
	}

	second := createHistoricalOnsetEpisodeFixture(
		t,
		store,
		"alternative-primary-episode",
		alternative,
		2_200,
	)
	index, err := store.taskEpisodeOnsetIdentityIndex(2_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.repeatedEpisodeIDs()) != 0 {
		t.Fatalf("alternative evidence created a false cluster: %+v", index.byKey)
	}
	if got, found, err := store.FindTaskEpisodeByOnsetAnchor(
		alternative,
		alternative.ObservedAt,
	); err != nil || !found || got.ID != second.ID {
		t.Fatalf("primary alternative anchor lookup=%+v found=%v err=%v", got, found, err)
	}
}

func TestTaskEpisodeOnsetIdentityKeepsDistinctRefsAndMalformedLegacyRowsIndependent(
	t *testing.T,
) {
	store := newTestStore(t)
	left := taskEpisodeOnsetAnchor{
		Ref: "event:left", SourceKind: "activity_event",
		SourceID: "same-source", ObservedAt: 3_000,
	}
	right := left
	right.Ref = "event:right"
	createHistoricalOnsetEpisodeFixture(
		t, store, "distinct-ref-left", left, 3_100,
	)
	createHistoricalOnsetEpisodeFixture(
		t, store, "distinct-ref-right", right, 3_200,
	)
	index, err := store.taskEpisodeOnsetIdentityIndex(3_000)
	if err != nil || len(index.repeatedEpisodeIDs()) != 0 {
		t.Fatalf("distinct refs clustered: index=%+v err=%v", index, err)
	}

	legacy := taskEpisodeFixture(
		"malformed-legacy-onset",
		3_000,
		3_300,
		TaskEpisodeStatusActive,
	)
	legacy.InitialEvidence = []TaskEpisodeEvidence{{
		ID: "malformed-legacy-evidence", Role: TaskEpisodeEvidenceOnset,
		SourceKind: "activity_event", SourceID: "legacy-source",
		Summary:    "Legacy evidence without a source-bound payload",
		ObservedAt: 3_000, PayloadJSON: `{"legacy":true}`,
	}}
	legacy.InitialHypothesis.EvidenceIDs = []string{"malformed-legacy-evidence"}
	if _, added, err := store.CreateTaskEpisode(legacy); err != nil || !added {
		t.Fatalf("create malformed legacy episode: added=%v err=%v", added, err)
	}
	index, err = store.taskEpisodeOnsetIdentityIndex(3_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, indexed := index.byEpisode[legacy.ID]; indexed {
		t.Fatal("malformed legacy primary evidence became an onset identity")
	}
}
