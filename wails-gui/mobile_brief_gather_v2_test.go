package main

import (
	"testing"
)

func TestMorningBriefScoringMetadataDoesNotChangeLegacySourceDigest(t *testing.T) {
	base := mobileBriefCandidate{SourceKind: "purpose", SourceID: "goal", Title: "Launch"}
	withScoring := base
	withScoring.confidence, withScoring.confidenceKnown, withScoring.rankedSourceDigest = 0.9, true, "private-stamp"
	withScoring.rankedOriginDigest = "private-origin"
	withScoring.rankedAttendees = []string{"Dana"}
	withScoring.rankedInvitePending = true
	if mobileBriefModelDigest(base) != mobileBriefModelDigest(withScoring) {
		t.Fatal("scoring metadata changed the legacy source digest")
	}
}
