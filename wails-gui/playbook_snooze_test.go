//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"testing"
	"time"

	"wails-gui/internal/sop"
)

// Not now on a playbook offer in Tasks › Up next hides it for two weeks and
// leaves it unanswered, so it comes back; Decline is the only way to say no
// for good.
func TestSnoozePlaybookSuggestionHidesForTwoWeeks(t *testing.T) {
	dir := t.TempDir()
	sops, err := sop.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := sop.NewPlanStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{sopStore: sops, playbookPlans: plans}
	if _, err := plans.Update("invoice", func(p *sop.Plan) {
		p.Proposal = &sop.Proposal{Host: "example.com", SiteName: "Example", Time: "16:00", Days: 3, ProposedAt: 1}
	}); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	view, err := app.SnoozePlaybookSuggestion("invoice")
	if err != nil {
		t.Fatal(err)
	}
	p := view.Proposal
	if p == nil || p.Answer != "" || p.AnsweredAt != 0 {
		t.Fatalf("Not now answered the offer: %+v", p)
	}
	low := before.Add(13 * 24 * time.Hour).UnixMilli()
	high := time.Now().Add(15 * 24 * time.Hour).UnixMilli()
	if p.SnoozedUntil < low || p.SnoozedUntil > high {
		t.Fatalf("snoozedUntil = %d, want about two weeks out", p.SnoozedUntil)
	}

	// An answered offer stays answered.
	if _, err := app.DeclinePlaybookSuggestion("invoice"); err != nil {
		t.Fatal(err)
	}
	if _, err := plans.Update("invoice", func(p *sop.Plan) { p.Proposal.SnoozedUntil = 0 }); err != nil {
		t.Fatal(err)
	}
	view, err = app.SnoozePlaybookSuggestion("invoice")
	if err != nil {
		t.Fatal(err)
	}
	if view.Proposal.Answer != sop.AnswerDeclined || view.Proposal.SnoozedUntil != 0 {
		t.Fatalf("snoozed a declined offer: %+v", view.Proposal)
	}

	var none *App
	if _, err := none.SnoozePlaybookSuggestion("invoice"); err == nil {
		t.Fatal("no store must be an error")
	}
}
