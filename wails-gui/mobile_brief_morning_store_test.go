package main

import (
	"testing"
	"time"
)

// 2026-11-13: the made-ahead copy from 01:07 made the adaptive 07:00 build
// look delivered, so every build after it was thrown away. A made-ahead copy
// never counts as the slot's brief.
func TestMadeAheadCopyDoesNotBlockTheRealBuild(t *testing.T) {
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: mobileBriefTestDay(6, 50)})
	now := mobileBriefTestDay(1, 7)
	ahead := buildMobileBriefV1(mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: now, WindowStart: now.Add(-time.Hour), Location: time.UTC})
	ahead.Generation = &MobileBriefGeneration{Reason: mobileBriefGenerationMadeAhead, ScheduledAt: formatMobileBriefTime(mobileBriefTestDay(7, 0)), GeneratedAt: formatMobileBriefTime(now)}
	if err := svc.store.saveDocument(ahead, now); err != nil {
		t.Fatal(err)
	}
	if svc.finalCopyExists(ahead.BriefID) {
		t.Fatal("a made-ahead copy blocked the slot's real build")
	}
	final := ahead
	final.Generation = nil
	if err := svc.store.saveDocument(final, now); err != nil {
		t.Fatal(err)
	}
	if !svc.finalCopyExists(final.BriefID) {
		t.Fatal("a real copy did not count")
	}
}

// The old writer's fallback failed four times on "closing requires two
// grounded answers". A closing without answers now goes; the brief stands.
func TestRankedDraftWithoutGroundedAnswersKeepsItsBody(t *testing.T) {
	brief := MobileBriefV1{BriefID: "2026-11-13-morning", Slot: mobileBriefSlotMorning, Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: "purpose", SourceID: "p1", Label: "Calf raises"}}}
	lead := morningBriefCandidate{ID: mobileBriefItemKey("candidate", "purpose", "p1"), SourceKind: "purpose", SourceID: "p1", Title: "Calf raises", Refs: []string{"e1"}}
	draft := morningBriefDraft{Paragraphs: []MobileBriefPara{
		{Text: "Your calf raises have slipped since November 5; add them to today's workout.", Refs: []string{"e1"}},
		{Text: "Want me to plan them?", Refs: []string{"e1"}},
	}}
	got, err := prepareRankedMorningDraft(brief, draft, morningBriefSelection{Lead: &lead}, "English")
	if err != nil {
		t.Fatalf("a missing closing answer failed the brief: %v", err)
	}
	if len(got.Paragraphs) != 1 || got.Paragraphs[0].Text != draft.Paragraphs[0].Text {
		t.Fatalf("the body was not kept without its closing: %+v", got.Paragraphs)
	}
}
