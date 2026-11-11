package main

import (
	"testing"
	"time"

	briefcore "wails-gui/internal/brief"
)

func TestMobileBriefShadowDefaultsOffAndReadsTheSetting(t *testing.T) {
	if defaultMobileBriefSettings().ShadowCompose {
		t.Fatal("shadow compose must default to off in code")
	}
	dir := t.TempDir()
	store := newMobileBriefConfigStore(dir)
	if store.load().ShadowCompose {
		t.Fatal("a missing schedule file must read as shadow off")
	}
	settings := store.load()
	settings.ShadowCompose = true
	if err := store.save(settings); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !store.load().ShadowCompose {
		t.Fatal("shadowCompose true was not read back")
	}
	settings.ShadowCompose = false
	if err := store.save(settings); err != nil {
		t.Fatalf("save: %v", err)
	}
	if store.load().ShadowCompose {
		t.Fatal("shadowCompose false was not read back")
	}
}

func TestMobileBriefShadowItemsDropProtectedAndKeepOrder(t *testing.T) {
	at := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	in := mobileBriefInput{
		Moved: []mobileBriefCandidate{
			{SourceKind: "work", SourceID: "w-protected", Title: "Private project", Protected: true, At: at},
			{SourceKind: "work", SourceID: "w1", Title: "Ship the draft", At: at},
		},
		People: []mobileBriefCandidate{
			{SourceKind: "person", SourceID: "p1", Title: "Sam Lee", At: at},
		},
		Meetings: []mobileBriefCandidate{
			{SourceKind: "calendar", SourceID: "", Title: "No source id", At: at},
		},
	}
	items := mobileBriefShadowItems(in)
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Section]++
	}
	if counts[briefcore.SectionMoved] != 1 {
		t.Fatalf("moved: got %d items, want 1 (the protected record is dropped)", counts[briefcore.SectionMoved])
	}
	if counts[briefcore.SectionPeople] != 1 {
		t.Fatalf("people: got %d items, want 1", counts[briefcore.SectionPeople])
	}
	if counts[briefcore.SectionMeetings] != 0 {
		t.Fatalf("meetings: got %d items, want 0 (no source id)", counts[briefcore.SectionMeetings])
	}
	for _, item := range items {
		if item.Section == briefcore.SectionMoved && item.Title != "Ship the draft" {
			t.Fatalf("moved kept %q", item.Title)
		}
	}
}

func TestMobileBriefModelBudgetCapsEachLocalDay(t *testing.T) {
	var b mobileBriefModelBudget
	day := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	for i := 0; i < mobileBriefShadowDailyCalls-1; i++ {
		b.charge(day)
	}
	if b.low(day) {
		t.Fatal("budget is low before the cap")
	}
	b.charge(day)
	if !b.low(day) {
		t.Fatal("budget is not low at the cap")
	}
	if b.low(day.Add(24 * time.Hour)) {
		t.Fatal("budget did not reset on the next local day")
	}
}

func TestStartBriefShadowIsInertWithoutAnApp(t *testing.T) {
	var s mobileBriefService
	s.startBriefShadow(MobileBriefV1{BriefID: "2026-10-09-morning"})
	if s.shadowCtx != nil {
		t.Fatal("a service with no app started a shadow run")
	}
}
