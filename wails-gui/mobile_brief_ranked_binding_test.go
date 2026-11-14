package main

import (
	"testing"
)

func TestMorningBriefCandidateSafetyKeepsGeneratedDates(t *testing.T) {
	item := morningBriefCandidate{ID: mobileBriefItemKey("candidate", "history", "item"), Title: "Prepare the plan", LastTouch: "2026-10-29T16:00:00Z", Due: "2026-10-30T09:00:00Z"}
	if !morningBriefCandidateSafe(item) {
		t.Fatal("generated dates were treated as secret codes")
	}
	item.Due = "not a date"
	if morningBriefCandidateSafe(item) {
		t.Fatal("invalid timestamp was admitted")
	}
}

func TestMorningBriefRankedDeliveryRejectsUnboundProse(t *testing.T) {
	app, _, brief := keptTextFixture(t, "safe-recall")
	brief.rankedEdition = true
	if delivered, withheld := app.mobileBriefDelivery(brief); len(delivered.Narrative) != 0 || withheld.Reason == "" {
		t.Fatal("a legacy source binding certified ranked prose")
	}
	brief.modelBinding = nil
	if delivered, withheld := app.mobileBriefDelivery(brief); len(delivered.Narrative) != 0 || delivered.Audio != nil || withheld.Reason == "" {
		t.Fatalf("unbound ranked prose was delivered: %+v, %+v", delivered.Narrative, withheld)
	}
}
