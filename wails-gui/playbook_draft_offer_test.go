//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"errors"
	"testing"

	"wails-gui/internal/sop"
)

// A playbook drafted on request becomes an Up next offer in Tasks, and Do it
// schedules it (how-you-do-things v1.0.0 W1).
func TestDraftedPlaybookBecomesUpNextOffer(t *testing.T) {
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
	saved, err := sops.Save(sop.Entry{
		ID:     "weekly-report",
		Title:  "Send the weekly report",
		Body:   "1. Open the report\n2. Send it to Maple Lane",
		Status: sop.StatusDraft,
		Source: sop.SourceManual,
		Sites:  []string{"https://www.example.com/reports", "https://example.com/send"},
	})
	if err != nil {
		t.Fatal(err)
	}

	app.offerDraftedPlaybook(sopAuthorSpec{Source: sop.SourceManual}, saved)
	plan, ok, err := plans.Get(saved.ID)
	if err != nil || !ok || plan.Proposal == nil {
		t.Fatalf("no offer: plan=%+v ok=%v err=%v", plan, ok, err)
	}
	p := plan.Proposal
	if p.Host != "example.com" || p.Time != draftedPlaybookTime || p.ProposedAt == 0 || p.Answer != "" {
		t.Fatalf("offer = %+v", p)
	}

	// Do it gets past the site check a routine offer's host gets; this bare
	// app has no scheduler, so it stops at scheduling instead.
	if _, err := app.AcceptPlaybookSuggestion(saved.ID); errors.Is(err, errPlaybookSitesChanged) {
		t.Fatalf("Do it held a drafted playbook to one site: %v", err)
	}
	if _, err := plans.Update(saved.ID, func(p *sop.Plan) { p.Proposal.Host = "" }); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AcceptPlaybookSuggestion(saved.ID); errors.Is(err, errPlaybookSitesChanged) {
		t.Fatalf("Do it held a no-site offer to a site: %v", err)
	}
	if _, err := plans.Update(saved.ID, func(p *sop.Plan) { p.Proposal.Host = "example.org" }); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AcceptPlaybookSuggestion(saved.ID); !errors.Is(err, errPlaybookSitesChanged) {
		t.Fatalf("a routine offer's site check went away: %v", err)
	}
}

// Triage drafts and confirmed playbooks keep their own path: no offer here.
func TestDraftedPlaybookOfferSkipsTriageAndConfirmed(t *testing.T) {
	dir := t.TempDir()
	plans, err := sop.NewPlanStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{playbookPlans: plans}
	app.offerDraftedPlaybook(sopAuthorSpec{Source: sop.SourceTriage}, sop.Entry{ID: "a", Status: sop.StatusDraft})
	app.offerDraftedPlaybook(sopAuthorSpec{Source: sop.SourceManual}, sop.Entry{ID: "b", Status: sop.StatusConfirmed})
	for _, id := range []string{"a", "b"} {
		if plan, ok, _ := plans.Get(id); ok && plan.Proposal != nil {
			t.Fatalf("%s got an offer: %+v", id, plan.Proposal)
		}
	}
}

func TestDraftedPlaybookHost(t *testing.T) {
	for _, tc := range []struct {
		sites []string
		want  string
	}{
		{nil, ""},
		{[]string{"https://www.example.com/a"}, "example.com"},
		{[]string{"https://example.com", "https://example.org"}, ""},
	} {
		if got := draftedPlaybookHost(tc.sites); got != tc.want {
			t.Errorf("draftedPlaybookHost(%v) = %q, want %q", tc.sites, got, tc.want)
		}
	}
}
