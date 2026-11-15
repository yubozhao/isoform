//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"testing"
	"time"

	"wails-gui/internal/engagement"
)

func TestCommitmentChatHoldSourceUsesSanitizedMoment(t *testing.T) {
	due := time.Now().Add(time.Hour).Truncate(time.Second)
	moment := engagement.Moment{ID: "m", Kind: engagement.KindCommitmentSlipping, SubjectID: "commitment-1", DueAt: due, Params: map[string]string{"title": "Q4 plan", "due": "today"}}
	source := mainChatHoldSourceForMoment(moment)
	if source == nil || source.Thing.Title != "Q4 plan" || source.Thing.SourceID != "commitment-1" || source.Due != due.Format(time.RFC3339) {
		t.Fatalf("source=%#v", source)
	}
	moment.Params["title"] = "Cancer treatment"
	if mainChatHoldSourceForMoment(moment) != nil {
		t.Fatal("exposed restricted source in hold metadata")
	}
	moment.Params["title"] = "Q4 plan"
	moment.Kind = engagement.KindMeetingPrep
	if mainChatHoldSourceForMoment(moment) != nil {
		t.Fatal("added a commitment hold source to another kind")
	}
}
