package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestMobileBriefMergeSharesUpcomingRecapAndPurposeEvidence(t *testing.T) {
	for _, crowdedPurposes := range []bool{false, true} {
		t.Run(fmt.Sprintf("crowded_purposes_%t", crowdedPurposes), func(t *testing.T) {
			in := mobileBriefInput{AsOf: mobileBriefTestDay(11, 0), Location: time.UTC,
				Today: &mobileBriefTodayInput{Context: map[string]MobileBriefItemContext{}}}
			for i := range 3 {
				id := fmt.Sprintf("upcoming-%d", i)
				in.Today.Upcoming = append(in.Today.Upcoming, mobileBriefCandidate{SourceKind: "schedule", SourceID: id, Title: id, At: in.AsOf.Add(time.Hour)})
				in.Today.Context[mobileBriefSourceKey("schedule", id)] = MobileBriefItemContext{JobID: id}
			}
			for i := range mobileBriefItemsPerSection {
				id := fmt.Sprintf("failed-%d", i)
				in.Schedules = append(in.Schedules, mobileBriefCandidate{SourceKind: "schedule_run", SourceID: id, Title: id, Status: "failed"})
				in.Today.Context[mobileBriefSourceKey("schedule_run", id)] = MobileBriefItemContext{JobID: id, Preview: "Retry this run"}
			}
			// Refresh puts one citation per paragraph first, then secondary refs.
			for i := range mobileBriefEvidenceLimit {
				in.RecapEvidence = append(in.RecapEvidence, MobileBriefRef{SourceKind: "summary", SourceID: fmt.Sprintf("recap-%02d", i), Label: "Original recap source"})
			}
			purpose := func(id string) mobileBriefCandidate {
				item := mobileBriefCandidate{SourceKind: "purpose", SourceID: id, Title: id, Detail: "Observed progress", LastTouch: in.AsOf, NextStep: "Review the plan"}
				for j := range 2 {
					item.Evidence = append(item.Evidence, mobileBriefCandidate{SourceKind: "activity_session", SourceID: fmt.Sprintf("%s-source-%d", id, j), Title: "Recorded activity"})
				}
				return item
			}
			for i := range 5 {
				in.WorkingToward = append(in.WorkingToward, purpose(fmt.Sprintf("purpose-%d", i)))
			}
			if crowdedPurposes {
				for i := range mobileBriefItemsPerSection {
					item := purpose(fmt.Sprintf("commitment-%d", i))
					item.Evidence = append(item.Evidence, mobileBriefCandidate{SourceKind: "engagement_moment", SourceID: fmt.Sprintf("moment-%d", i), Title: "Existing reminder"})
					in.Commitments = append(in.Commitments, item)
				}
			}
			// Other rows compete for the same cap, but cannot displace reservations.
			for i := range mobileBriefItemsPerSection {
				in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: "history", SourceID: fmt.Sprintf("moved-%d", i), Title: "Completed work"})
			}
			brief := buildMobileBriefV1(in)
			if len(brief.Evidence) != mobileBriefEvidenceLimit || len(brief.Upcoming) != 3 || len(brief.Sections.Schedules) != 8 || len(brief.Sections.WorkingToward) != 5 {
				t.Fatalf("a merged evidence category was starved: %+v", brief)
			}
			assertTodayRefsResolve(t, brief)
			sources := map[string]bool{}
			for _, ref := range brief.Evidence {
				sources[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)] = true
			}
			for i := range mobileBriefNarrativeMaxParagraphs {
				if !sources[mobileBriefSourceKey("summary", fmt.Sprintf("recap-%02d", i))] {
					t.Fatalf("Purpose evidence displaced recap paragraph %d", i)
				}
			}
			for _, item := range brief.Upcoming {
				if item.Context == nil || item.Context.JobID == "" {
					t.Fatal("upcoming source lost its action context")
				}
			}
			for _, item := range brief.Sections.WorkingToward {
				if len(item.Refs) != 3 || item.LastTouch == "" || item.NextStep == "" {
					t.Fatalf("Purpose metadata or atomic evidence lost: %+v", item)
				}
			}
			if crowdedPurposes && (len(brief.Sections.Commitments) != 5 || !containsString(brief.Limits.TruncatedSections, mobileBriefSectionCommitments) || len(brief.Sections.Moved) != mobileBriefItemsPerSection) {
				t.Fatalf("over-budget commitment was not truncated atomically: %+v", brief.Sections.Commitments)
			}
			for _, item := range brief.Sections.Commitments {
				if len(item.Refs) != 4 {
					t.Fatalf("commitment lost part of its evidence: %+v", item)
				}
			}
		})
	}
}

func TestMobileBriefItemKeepsContextBesidePurposeMetadata(t *testing.T) {
	item := MobileBriefItem{PurposeID: "goal", NextStep: "Review plan", DueAt: "2026-10-17", LastTouch: "2026-10-16", ChatMessageID: "report",
		Context: &MobileBriefItemContext{JobID: "job", Preview: "Current context"}}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MobileBriefItem
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Context == nil || *decoded.Context != *item.Context || decoded.PurposeID != item.PurposeID || decoded.NextStep != item.NextStep || decoded.DueAt != item.DueAt || decoded.LastTouch != item.LastTouch || decoded.ChatMessageID != item.ChatMessageID {
		t.Fatalf("merge lost additive item fields: %s", data)
	}
}
