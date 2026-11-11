package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
)

// Under Closed Eyes a template brief follows the template rules: every card
// (and with it every paragraph) is hidden on its own, never the text withdrawn
// whole as if the model had written it. A Today read while it lasts leaves the
// stored brief byte-identical, and the read after Closed Eyes shows the text
// again. Found by the Today regressions self-review (#129, 2026-10-21).
func TestClosedEyesNeverWithdrawsATemplateBriefAsModelText(t *testing.T) {
	rig := newHandoffTestRig(t)
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.app, rig.app.mobileBriefSvc = rig.app, svc
	var candidates []mobileBriefCandidate
	for i, id := range []string{"plan-a", "plan-b", "plan-c"} {
		at := clock.Now().Add(-time.Duration(i+1) * time.Hour)
		if _, err := rig.app.handoffStore.Save(handoff.Entry{ID: id, Title: "Draft " + id, Status: handoff.StatusNeedsReview, UpdatedAt: at.UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, mobileBriefCandidate{SourceKind: "history", SourceID: id, Title: "Draft " + id, Kind: "one_time_work", Status: "needs_review", At: at})
	}
	svc.gather = func(_ context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
		return mobileBriefInput{Slot: slot, WindowStart: windowStart, AsOf: asOf, Location: time.UTC, OpenLoops: candidates}, nil
	}
	brief, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if brief.NarrativeStatus == "ok" || len(brief.Narrative) == 0 {
		t.Fatalf("fixture is not a template brief with text: %s %+v", brief.NarrativeStatus, brief.Narrative)
	}
	briefPath := filepath.Join(svc.store.dir, brief.BriefID+".json")
	before, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatal(err)
	}
	rig.app.closedEyes.enabled.Store(true)
	clock.Set(mobileBriefTestDay(11, 0))
	for read := 1; read <= 2; read++ {
		view, err := rig.app.GetLatestBrief()
		if err != nil || view.Brief == nil {
			t.Fatalf("read %d: view = %+v, %v", read, view, err)
		}
		// What Closed Eyes hides is unchanged: every observed card, so none of
		// the template's text shows while it is on.
		if len(view.Brief.Narrative) != 0 || view.Brief.NarrativeStatus == "ok" {
			t.Fatalf("read %d: Closed Eyes shows the template's text: %+v", read, view.Brief.Narrative)
		}
		after, err := os.ReadFile(briefPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("read %d: a Today read under Closed Eyes saved a withdrawn projection over the stored brief", read)
		}
	}
	rig.app.closedEyes.enabled.Store(false)
	view, err := rig.app.GetLatestBrief()
	if err != nil || view.Brief == nil || len(view.Brief.Narrative) != len(brief.Narrative) {
		t.Fatalf("the template's text did not come back after Closed Eyes: %+v, %v", view.Brief, err)
	}
	for i, para := range brief.Narrative {
		if view.Brief.Narrative[i].Text != para.Text {
			t.Fatalf("paragraph %d changed across Closed Eyes: %q, want %q", i, view.Brief.Narrative[i].Text, para.Text)
		}
	}
}

// Delivery under Closed Eyes reports the passing reason for a template brief
// the same as for the model's text, so a Today read keeps the stored brief for
// the read after it.
func TestClosedEyesReportsThePassingReasonForATemplateBrief(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	seedMobileBriefReadableHistory(t, app, "keep")
	now := time.Now()
	brief := buildMobileBriefV1(mobileBriefInput{AsOf: now, WindowStart: now.Add(-time.Hour), Location: time.Local, Slot: mobileBriefSlotMorning,
		Moved: []mobileBriefCandidate{{SourceKind: "history", SourceID: "keep", Title: "Kept work"}},
	})
	refs := map[string]string{}
	for _, ref := range brief.Evidence {
		refs[ref.SourceID] = ref.Ref
	}
	brief.Narrative = []MobileBriefPara{{Text: "Kept work moved.", Refs: []string{refs["keep"]}}}
	app.closedEyes.enabled.Store(true)
	t.Cleanup(func() { app.closedEyes.enabled.Store(false) })
	for _, status := range []string{"unavailable", "ok"} {
		brief.NarrativeStatus = status
		delivered, withheld := app.mobileBriefDelivery(brief)
		if len(delivered.Narrative) != 0 {
			t.Fatalf("%s: Closed Eyes shows the brief's text: %+v", status, delivered.Narrative)
		}
		if withheld.Reason != mobileBriefWithheldClosedEyes || !withheld.Transient() {
			t.Fatalf("%s: withholding = %+v, want the passing Closed Eyes reason", status, withheld)
		}
	}
}

// A brief written without the model is Isoform's template: each paragraph
// names only the cards it cites. Opening Today used to withdraw all of it, on
// disk too, as soon as any one card could no longer be read. Now only the
// paragraphs naming that card leave; the rest render and stay stored.
func TestTodayWithholdsTemplateBriefWhenACitedCardIsWithdrawn(t *testing.T) {
	rig := newHandoffTestRig(t)
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.app, rig.app.mobileBriefSvc = rig.app, svc
	var candidates []mobileBriefCandidate
	for i, id := range []string{"plan-a", "plan-b", "plan-c", "plan-d"} {
		at := clock.Now().Add(-time.Duration(i+1) * time.Hour)
		status := []handoff.Status{handoff.StatusNeedsReview, handoff.StatusBlocked}[i%2]
		if _, err := rig.app.handoffStore.Save(handoff.Entry{ID: id, Title: "Draft " + id, Status: status, UpdatedAt: at.UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, mobileBriefCandidate{SourceKind: "history", SourceID: id, Title: "Draft " + id, Kind: "one_time_work", Status: string(status), At: at})
	}
	svc.gather = func(_ context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
		return mobileBriefInput{Slot: slot, WindowStart: windowStart, AsOf: asOf, Location: time.UTC, OpenLoops: candidates}, nil
	}
	brief, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if brief.NarrativeStatus == "ok" || len(brief.Narrative) < 2 {
		t.Fatalf("fixture is not a template with several paragraphs: %s %+v", brief.NarrativeStatus, brief.Narrative)
	}
	// Withdraw a card that one paragraph names and another does not.
	sources := map[string]string{}
	for _, ref := range brief.Evidence {
		sources[ref.Ref] = ref.SourceID
	}
	withdrawn := ""
	for _, para := range brief.Narrative {
		for _, ref := range para.Refs {
			if withdrawn == "" && slices.ContainsFunc(brief.Narrative, func(other MobileBriefPara) bool { return !slices.Contains(other.Refs, ref) }) {
				withdrawn = sources[ref]
			}
		}
	}
	var kept []string
	for _, para := range brief.Narrative {
		if !slices.ContainsFunc(para.Refs, func(ref string) bool { return sources[ref] == withdrawn }) {
			kept = append(kept, para.Text)
		}
	}
	if withdrawn == "" || len(kept) == 0 {
		t.Fatalf("no paragraph is independent of another card: %+v", brief.Narrative)
	}
	if err := rig.app.handoffStore.Delete(withdrawn); err != nil {
		t.Fatal(err)
	}

	// The brief sealed before the withdrawal is amended from the withdrawal alone:
	// the written template cites the withdrawn card, so it is withheld as a whole,
	// and the card's evidence is gone. Reads repeat the same answer.
	for read := 1; read <= 2; read++ {
		view, err := rig.app.GetLatestBrief()
		if err != nil || view.Brief == nil {
			t.Fatalf("read %d: view = %+v, %v", read, view, err)
		}
		if len(view.Brief.Narrative) != 0 || len(view.Brief.Offers) != 0 || view.Brief.Audio != nil {
			t.Fatalf("read %d: the written template survived withdrawal: %+v", read, view.Brief.Narrative)
		}
		for _, ref := range view.Brief.Evidence {
			if ref.SourceID == withdrawn {
				t.Fatalf("read %d: evidence of the withdrawn card is still shown", read)
			}
		}
	}

	// A brief built later reads the live store, which no longer holds the card.
	var live []mobileBriefCandidate
	for _, candidate := range candidates {
		if candidate.SourceID != withdrawn {
			live = append(live, candidate)
		}
	}
	candidates = live
	clock.Set(mobileBriefTestDay(11, 0))
	view, err := latestBriefAfterRefresh(t, rig.app)
	if err != nil || view.Brief == nil {
		t.Fatalf("day 11 view = %+v, %v", view, err)
	}
	for _, para := range view.Brief.Narrative {
		if strings.Contains(para.Text, withdrawn) {
			t.Fatalf("the day 11 brief names the withdrawn card: %q", para.Text)
		}
	}
	assertTodayRefsResolve(t, *view.Brief)
}

// The model's own text may draw on more than it cites, so a withdrawn card
// still withdraws all of it, as before.
func TestTodayStillWithdrawsTheModelsBriefWhenACardIsWithdrawn(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	seedMobileBriefReadableHistory(t, app, "keep", "withdraw")
	now := time.Now()
	brief := buildMobileBriefV1(mobileBriefInput{AsOf: now, WindowStart: now.Add(-time.Hour), Location: time.Local, Slot: mobileBriefSlotMorning,
		Moved: []mobileBriefCandidate{{SourceKind: "history", SourceID: "keep", Title: "Kept work"}, {SourceKind: "history", SourceID: "withdraw", Title: "Withdrawn work"}},
	})
	refs := map[string]string{}
	for _, ref := range brief.Evidence {
		refs[ref.SourceID] = ref.Ref
	}
	brief.Narrative = []MobileBriefPara{{Text: "Kept work moved.", Refs: []string{refs["keep"]}}, {Text: "Withdrawn work moved.", Refs: []string{refs["withdraw"]}}}
	if err := app.handoffStore.Delete("withdraw"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"ok", "unavailable"} {
		brief.NarrativeStatus = status
		got := app.mobileBriefForDelivery(brief)
		want := map[string]int{"ok": 0, "unavailable": 1}[status]
		if len(got.Narrative) != want {
			t.Fatalf("%s narrative after a withdrawn card: %+v", status, got.Narrative)
		}
		if want == 1 && got.Narrative[0].Text != "Kept work moved." {
			t.Fatalf("template kept the wrong paragraph: %+v", got.Narrative)
		}
	}
}

// The Oct 19 brief: no model text ("unavailable"), and its stored template
// and offers were withdrawn and saved by an earlier Today open, while its
// cards still show. Today builds the template of the cards it delivers now,
// so the text is back without naming a card that cannot be read, and the
// brief's chat accepts the brief Today shows.
func TestTodayShowsTheTemplateForAStoredBriefWithoutText(t *testing.T) {
	rig := newHandoffTestRig(t)
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.app, rig.app.mobileBriefSvc = rig.app, svc
	var candidates []mobileBriefCandidate
	for i, id := range []string{"plan-a", "plan-b", "plan-c"} {
		at := clock.Now().Add(-time.Duration(i+1) * time.Hour)
		if _, err := rig.app.handoffStore.Save(handoff.Entry{ID: id, Title: "Draft " + id, Status: handoff.StatusNeedsReview, UpdatedAt: at.UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, mobileBriefCandidate{SourceKind: "history", SourceID: id, Title: "Draft " + id, Kind: "one_time_work", Status: "needs_review", At: at})
	}
	svc.gather = func(_ context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
		return mobileBriefInput{Slot: slot, WindowStart: windowStart, AsOf: asOf, Location: time.UTC, OpenLoops: candidates}, nil
	}
	brief, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSchedule)
	if err != nil {
		t.Fatal(err)
	}
	brief.Narrative, brief.NarrativeStatus, brief.Offers, brief.Audio = []MobileBriefPara{}, "unavailable", nil, nil
	if err := svc.store.save(brief, clock.Now()); err != nil {
		t.Fatal(err)
	}
	clock.Set(mobileBriefTestDay(11, 20))

	view, err := rig.app.GetLatestBrief()
	if err != nil || view.Brief == nil {
		t.Fatalf("view = %+v, %v", view, err)
	}
	text := ""
	for _, para := range view.Brief.Narrative {
		text += para.Text + "\n"
	}
	named := ""
	for _, id := range []string{"plan-a", "plan-b", "plan-c"} {
		if strings.Contains(text, id) {
			named = id
		}
	}
	if named == "" || view.Brief.NarrativeStatus != "unavailable" {
		t.Fatalf("Today shows no template text for the cards it shows: %q (%s)", text, view.Brief.NarrativeStatus)
	}
	if len(view.Brief.Offers) == 0 || view.Brief.ConversationKey != mobileBriefConversationKey(*view.Brief) {
		t.Fatalf("offers or conversation key do not match the text: %+v %q", view.Brief.Offers, view.Brief.ConversationKey)
	}
	assertTodayRefsResolve(t, *view.Brief)
	ref := TodayChatContextRef{SourceKind: "brief", SourceID: view.Brief.ConversationKey, BriefID: view.Brief.BriefID}
	if _, err := rig.app.PrepareTodayChatDraft(ref); err != nil {
		t.Fatalf("the brief's chat refuses the brief Today shows: %v", err)
	}
	for _, card := range view.Brief.Cards {
		if card.Mode != mobileBriefCardSend {
			continue
		}
		draft, err := rig.app.PrepareTodayChatDraft(TodayChatContextRef{SourceKind: todayChatBriefCardKind, SourceID: card.ID, BriefID: view.Brief.BriefID})
		if err != nil || draft.Message != card.Message {
			t.Fatalf("the brief's chat refuses a card Today shows: %+v, %v", card, err)
		}
	}

	// A withdrawn source withholds the entire written template.
	if err := rig.app.handoffStore.Delete(named); err != nil {
		t.Fatal(err)
	}
	view, err = rig.app.GetLatestBrief()
	if err != nil || view.Brief == nil || len(view.Brief.Narrative) != 0 || len(view.Brief.Offers) != 0 {
		t.Fatalf("withdrawn template = %+v, %v", view, err)
	}
}
