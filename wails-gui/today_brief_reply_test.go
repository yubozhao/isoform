package main

import (
	"testing"
)

// Bo, 2026-11-15 evening: a paragraph's reply on Today answered "Couldn't
// open the chat". The reply checks the paragraph's text against the brief it
// reads itself; every paragraph Today shows must pass that check, whatever
// the text holds (times, day words, names).
func TestEveryParagraphTodayShowsCanBeRepliedTo(t *testing.T) {
	rig, svc, brief := cardRig(t)
	stored, ok := svc.store.load(brief.BriefID)
	if !ok || len(stored.Narrative) == 0 {
		t.Fatal("missing brief narrative")
	}
	refs := stored.Narrative[0].Refs
	stored.Narrative = []MobileBriefPara{
		{Text: "You talked with Coral about the release at 3:00 PM today.", Refs: refs},
		{Text: "The board deck is due tomorrow at 10:00 AM, and the memo is still blocked.", Refs: refs},
		{Text: "Maya is waiting on the launch memo before the check-in.", Refs: refs},
	}
	if err := svc.store.save(stored, svc.now()); err != nil {
		t.Fatal(err)
	}
	view, err := rig.app.GetLatestBrief()
	if err != nil || view.Brief == nil || len(view.Brief.Narrative) == 0 {
		t.Fatalf("Today shows no brief: %v", err)
	}
	for i, para := range view.Brief.Narrative {
		if _, err := rig.app.PrepareTodayBriefParagraph(view.Brief.BriefID, i, para.Text); err != nil {
			t.Errorf("paragraph %d as Today shows it (%q) cannot be replied to: %v", i, para.Text, err)
		}
	}
}

// The reply on 2026-11-15 failed within its eight-second limit while the
// app gathered every memory and the activity store first, which a paragraph
// reply never needed. It must find the paragraph without that gathering.
func TestParagraphReplyDoesNotGatherCardSources(t *testing.T) {
	rig, _, brief := cardRig(t)
	view, err := rig.app.GetLatestBrief()
	if err != nil || view.Brief == nil || len(view.Brief.Narrative) == 0 {
		t.Fatalf("Today shows no brief: %v", err)
	}
	gathered := 0
	rig.app.todayChatCollectionObserver = func() { gathered++ }
	if _, err := rig.app.PrepareTodayBriefParagraph(brief.BriefID, 0, view.Brief.Narrative[0].Text); err != nil {
		t.Fatal(err)
	}
	if gathered != 0 {
		t.Fatalf("a paragraph reply gathered the card sources %d time(s)", gathered)
	}
}
