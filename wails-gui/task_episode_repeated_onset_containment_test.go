package main

import (
	"testing"
)

func TestRepeatedOnsetIsOmittedFromLifecycleObservation(t *testing.T) {
	store := newTestStore(t)
	repeatedAnchor := taskEpisodeOnsetAnchor{
		Ref: "event:lifecycle-repeated", SourceKind: "activity_event",
		SourceID: "lifecycle-repeated-source", ObservedAt: 6_000,
	}
	first := createHistoricalOnsetEpisodeFixture(
		t, store, "lifecycle-repeated-first", repeatedAnchor, 6_100,
	)
	second := createHistoricalOnsetEpisodeFixture(
		t, store, "lifecycle-repeated-second", repeatedAnchor, 6_200,
	)
	independent := createHistoricalOnsetEpisodeFixture(
		t,
		store,
		"lifecycle-independent",
		taskEpisodeOnsetAnchor{
			Ref: "event:lifecycle-independent", SourceKind: "activity_event",
			SourceID: "lifecycle-independent-source", ObservedAt: 6_300,
		},
		6_400,
	)
	input, err := BuildTaskLifecycleInputFromActivity(
		store,
		"activity",
		"lifecycle-containment-window",
		6_500,
		7_000,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Episodes) != 1 ||
		input.Episodes[0].EpisodeID != independent.ID {
		t.Fatalf("lifecycle episodes=%+v", input.Episodes)
	}
	for _, item := range input.Episodes {
		if item.EpisodeID == first.ID || item.EpisodeID == second.ID {
			t.Fatalf("repeated onset entered lifecycle input: %+v", input.Episodes)
		}
	}
}
