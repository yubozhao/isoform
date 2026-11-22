package main

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestImportClaimLinesSkipsHeadingsAndEmpty(t *testing.T) {
	got := importClaimLines("# What I know\n\n- Prefers short replies\n* Works in Go\n1. Reviews PRs in the morning\nPreferences:\n---\n> quoted\n- Prefers short replies\n")
	want := []string{"Prefers short replies", "Works in Go", "Reviews PRs in the morning"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q", got)
	}
}

// Guard (design answer T2): a three-line paste becomes three unjudged
// claims, first in New this week.
func TestImportAIMemoryTextBecomesClaimsInNewThisWeek(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	result, err := app.ImportAIMemoryText("ChatGPT", "- Prefers short replies\n- Works in Go\n- Reviews PRs in the morning")
	if err != nil || result.Imported != 3 || len(result.Errors) != 0 {
		t.Fatalf("import = %+v %v", result, err)
	}
	view, err := app.getAboutYouNow()
	if err != nil {
		t.Fatal(err)
	}
	if len(view.NewThisWeek) != 3 {
		t.Fatalf("new this week = %v", view.NewThisWeek)
	}
	titles := []string{}
	for _, id := range view.NewThisWeek {
		idx := slices.IndexFunc(view.Beliefs, func(b AboutYouBelief) bool { return b.ID == id })
		belief := view.Beliefs[idx]
		if belief.Status == "confirmed" || belief.Status == "not_quite" || belief.ImportedFrom != "ChatGPT" || belief.Source != "import:ChatGPT" {
			t.Fatalf("imported claim = %+v", belief)
		}
		titles = append(titles, belief.Title)
	}
	if !reflect.DeepEqual(titles, []string{"Prefers short replies", "Works in Go", "Reviews PRs in the morning"}) {
		t.Fatalf("order = %q", titles)
	}
	// Judged, it leaves New this week.
	if _, err := app.ConfirmMemoryEntry(view.NewThisWeek[0]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { answersLedgers.Delete(app) })
	view, _ = app.getAboutYouNow()
	if len(view.NewThisWeek) != 2 || view.Totals.Confirmed != 1 {
		t.Fatalf("after Yes = %v %+v", view.NewThisWeek, view.Totals)
	}
}

func TestImportedNewBeliefIDsCapsAndGroupsTheRest(t *testing.T) {
	beliefs := []AboutYouBelief{}
	for i := range 23 {
		beliefs = append(beliefs, AboutYouBelief{ID: fmt.Sprintf("imported-x-1-%03d", i), FirstLearnedAt: 1, ImportedFrom: "ChatGPT"})
	}
	shown, more := importedNewBeliefIDs(beliefs)
	if len(shown) != aboutYouImportShown || shown[0] != "imported-x-1-000" {
		t.Fatalf("shown = %v", shown)
	}
	if len(more) != 1 || more[0].Label != "ChatGPT" || len(more[0].IDs) != 3 {
		t.Fatalf("more = %+v", more)
	}
}

// Confirmed says how: Yes is "confirmed", Edit "edited", the person's own
// note "said".
func TestConfirmedHow(t *testing.T) {
	yes := AnswerRecord{Action: workActionYes, At: "2026-11-20T10:00:00Z"}
	edit := AnswerRecord{Action: workActionEdit, At: "2026-11-20T10:00:00Z"}
	for _, tc := range []struct {
		belief   AboutYouBelief
		answer   AnswerRecord
		answered bool
		want     string
	}{
		{AboutYouBelief{Status: "confirmed", ConfirmedAt: 5}, yes, true, confirmedHowConfirmed},
		{AboutYouBelief{Status: "confirmed"}, edit, true, confirmedHowEdited},
		{AboutYouBelief{Status: "confirmed", UpdatedAt: 9}, AnswerRecord{}, false, confirmedHowSaid},
		{AboutYouBelief{Status: "confirmed", ConfirmedAt: 5}, AnswerRecord{}, false, confirmedHowConfirmed},
		{AboutYouBelief{Status: "inferred"}, yes, true, ""},
	} {
		if got, _ := confirmedHow(tc.belief, tc.answer, tc.answered); got != tc.want {
			t.Errorf("%+v %+v = %q, want %q", tc.belief, tc.answer, got, tc.want)
		}
	}
	if !strings.HasPrefix(importedClaimNote("ChatGPT", "Works in Go", time.Now()), "# Works in Go\n") {
		t.Fatal("note title")
	}
}

// Not mine (design answer A6): the claim goes and the ledger keeps not_mine.
func TestNotMineMemoryEntryRemovesAndRecords(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	t.Cleanup(func() { answersLedgers.Delete(app) })
	if _, err := app.ImportAIMemoryText("ChatGPT", "- Likes chestnuts"); err != nil {
		t.Fatal(err)
	}
	view, err := app.getAboutYouNow()
	if err != nil || len(view.NewThisWeek) != 1 {
		t.Fatalf("view = %+v %v", view, err)
	}
	id := view.NewThisWeek[0]
	if err := app.NotMineMemoryEntry(id); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetMemoryEntry(id); err == nil {
		t.Fatal("the claim stayed")
	}
	found := false
	for _, record := range app.answersLedger().entries() {
		if record.ItemID == "belief:"+id && record.Action == workActionNotMine && record.Title == "Likes chestnuts" {
			found = true
		}
	}
	if !found {
		t.Fatal("Not mine not in the ledger")
	}
}
