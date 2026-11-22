package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const noticedTestDay = "2026-10-19"

func noticedTestAt(hour, minute int) time.Time {
	day, _ := time.ParseInLocation("2006-01-02", noticedTestDay, time.Local)
	return day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

// saveNoticedMoment stores a moment with one run in app, ending at `at`.
func saveNoticedMoment(t *testing.T, app *App, id, title, appName, entities string, at time.Time) {
	t.Helper()
	start, end := at.Add(-20*time.Minute).UnixMilli(), at.UnixMilli()
	if err := app.activity.store.SaveSession(ActivitySessionRow{ID: id, AppNames: `["` + appName + `"]`, StartTime: start, EndTime: end, TriageStatus: "done"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Distinct event rows per run (the helper derives them from the start).
	run := testRun("run-"+id, appName, start, end, end-start, 1)
	run.SessionIDs = []string{id}
	insertComposableTimelineRun(t, app.activity.store, run, ActivityTimelineGroupSummary{})
	apps, _ := json.Marshal(map[string]int64{appName: end - start})
	runs, _ := json.Marshal([]string{run.ID})
	if entities == "" {
		entities = "[]"
	}
	if err := app.activity.store.InsertActivity(ActivityRow{ID: id, Title: title, Status: activityStatusSettled, Entities: entities,
		RunIDs: string(runs), ActiveMsByApp: string(apps), StartMs: start, EndMs: end, CreatedAt: end, UpdatedAt: end}); err != nil {
		t.Fatal(err)
	}
	if err := app.activity.store.UpsertActivityRunLink(run.ID, id, 1, end); err != nil {
		t.Fatal(err)
	}
}

// saveNoticedWithSummary writes the day's summary row first, as a summary
// generation does, then the suggestions made with it.
func saveNoticedWithSummary(t *testing.T, app *App, raws []noticedRaw) int {
	t.Helper()
	gen, err := app.activity.store.saveSummaryWithSources(noticedTestDay, "daily", "The day.", "test-model", false, "en", nil)
	if err != nil {
		t.Fatal(err)
	}
	app.saveNoticed(noticedTestDay, "en", gen, raws)
	return gen
}

func noticedRoutineRaw(moment string, steps ...string) noticedRaw {
	return noticedRaw{Kind: noticedKindRoutine, MomentID: moment, Text: "Weekly expense report", Why: "You did it step by step",
		Routine: &NoticedRoutineDraft{Name: "Weekly expense report", When: "weekly fri 16:00", Steps: steps, Will: []string{"Fill the expense sheet"}, Wont: []string{"Submit anything"}}}
}

func TestNoticedBlockIsCutFromTheDigest(t *testing.T) {
	digest := "## Today\n\n- Shipped the billing fix."
	text := digest + "\n\n<isoform_noticed>[{\"kind\":\"follow_up\",\"moment_id\":\"m1\",\"text\":\"Follow up with Priya\",\"why\":\"They asked\"}]</isoform_noticed>\n"
	clean, raws := splitNoticedBlock(text)
	if clean != digest || len(raws) != 1 || raws[0].MomentID != "m1" || raws[0].Text != "Follow up with Priya" {
		t.Fatalf("clean=%q raws=%+v", clean, raws)
	}
	clean, raws = splitNoticedBlock(digest + "\n<isoform_noticed>not json</isoform_noticed>")
	if clean != digest || raws != nil {
		t.Fatalf("an invalid block must be dropped without suggestions: %q %+v", clean, raws)
	}
	if clean, raws := splitNoticedBlock(digest); clean != digest || raws != nil {
		t.Fatalf("no block: %q %+v", clean, raws)
	}
}

func TestNoticedRoutineWhenBecomesACronExpression(t *testing.T) {
	for when, want := range map[string]string{
		"": "", "daily 09:30": "30 9 * * *", "weekdays 08:00": "0 8 * * 1-5", "weekly fri 16:00": "0 16 * * 5", "Weekly  MON 07:05": "5 7 * * 1",
	} {
		if got, ok := noticedCronExpr(when); !ok || got != want {
			t.Errorf("noticedCronExpr(%q) = %q %v, want %q", when, got, ok, want)
		}
	}
	for _, bad := range []string{"tomorrow", "daily 25:00", "weekly someday 09:00", "hourly 09:00"} {
		if _, ok := noticedCronExpr(bad); ok {
			t.Errorf("noticedCronExpr(%q) accepted", bad)
		}
	}
}

func TestNoticedRequestListsTheDaysMomentsBeforeTheClosingLine(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	saveNoticedMoment(t, app, "m-lamp", "Comparing Halcyon Arc desk lamps for Priya", "Safari", `[{"type":"person","name":"Priya"}]`, noticedTestAt(10, 0))
	saveNoticedMoment(t, app, "m-private", "Private Browsing", "Safari", "", noticedTestAt(11, 0))
	prompt := app.withNoticedRequest("Context...\n\nWrite the summary now:", noticedTestDay)
	if !strings.HasSuffix(prompt, "\n\nWrite the summary now:") {
		t.Fatalf("the closing line must stay last: %q", prompt[len(prompt)-60:])
	}
	if !strings.Contains(prompt, "id=m-lamp") || !strings.Contains(prompt, "people: Priya") || !strings.Contains(prompt, "<isoform_noticed>") ||
		!strings.Contains(prompt, "never a suggestion on its own") {
		t.Fatalf("prompt = %s", prompt)
	}
	if strings.Contains(prompt, "m-private") {
		t.Fatal("a private moment was listed to the model")
	}
	empty := newMobileBriefPurposeTestApp(t)
	if got := empty.withNoticedRequest("Context\n\nWrite the summary now:", noticedTestDay); got != "Context\n\nWrite the summary now:" {
		t.Fatalf("a day without moments changes nothing: %q", got)
	}
}

func TestNoticedKeepsOnlyAnchoredSuggestionsAndAtMostThree(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	saveNoticedMoment(t, app, "m-lamp", "Comparing Halcyon Arc desk lamps for Priya", "Safari", `[{"type":"person","name":"Priya"}]`, noticedTestAt(10, 0))
	saveNoticedMoment(t, app, "m-claude", "Asking Claude about the isoform repo", "Claude", `[{"type":"repo","name":"isoform"}]`, noticedTestAt(11, 0))
	saveNoticedMoment(t, app, "m-sheet", "Filling the expense sheet", "Numbers", "", noticedTestAt(12, 0))
	saveNoticedMoment(t, app, "m-pr", "Reviewing PR 80 in the browser", "Safari", `[{"type":"pr","name":"#80"}]`, noticedTestAt(13, 0))
	saveNoticedMoment(t, app, "m-private", "Private Browsing", "Safari", `[{"type":"person","name":"Dana"}]`, noticedTestAt(14, 0))

	saveNoticedWithSummary(t, app, []noticedRaw{
		{Kind: noticedKindFollowUp, MomentID: "m-claude", Text: "Follow up on the repo question", Why: "typed into Claude"},
		{Kind: noticedKindFollowUp, MomentID: "m-unknown", Text: "Follow up on nothing"},
		{Kind: noticedKindFollowUp, MomentID: "m-private", Text: "Follow up with Dana"},
		noticedRoutineRaw("m-sheet", "Open the sheet"),
		{Kind: noticedKindFollowUp, MomentID: "m-lamp", Text: "Follow up with Priya about the Halcyon Arc lamp", Why: "They are waiting"},
		noticedRoutineRaw("m-sheet", "Open the expense sheet", "Add this week's receipts"),
		{Kind: noticedKindFollowUp, MomentID: "m-pr", Text: "Follow up on PR 80 review comments"},
		{Kind: noticedKindFollowUp, MomentID: "m-lamp", Text: "Order the linen Halcyon Arc shade"},
	})
	got, err := app.GetDayNoticed(noticedTestDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != noticedMaxItems {
		t.Fatalf("got %d suggestions, want %d: %+v", len(got), noticedMaxItems, got)
	}
	want := []string{"Follow up with Priya about the Halcyon Arc lamp", "Weekly expense report", "Follow up on PR 80 review comments"}
	for i, item := range got {
		if item.Text != want[i] {
			t.Fatalf("suggestion %d = %q, want %q (all: %+v)", i, item.Text, want[i], got)
		}
	}
	if got[0].MomentTitle != "Comparing Halcyon Arc desk lamps for Priya" || got[0].MomentStartMs == 0 || got[1].Routine == nil || got[1].Routine.When != "weekly fri 16:00" {
		t.Fatalf("display fields = %+v", got)
	}
}

func TestNoticedReadsOnlyNamedMomentsAndRechecksChanges(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	saveNoticedMoment(t, app, "chosen", "Plan a trip with Priya", "Safari", `[{"type":"person","name":"Priya"}]`, noticedTestAt(10, 0))
	saveNoticedMoment(t, app, "unrelated", "Review a document with Dana", "Safari", `[{"type":"person","name":"Dana"}]`, noticedTestAt(11, 0))
	items := []NoticedSuggestion{{ID: "suggestion", Kind: noticedKindFollowUp, MomentID: "chosen", Text: "Follow up with Priya"}}
	moments, _, err := app.noticedCandidateMoments(noticedTestDay, items)
	if err != nil || len(moments) != 1 || moments[0].ID != "chosen" {
		t.Fatalf("candidate moments = %+v, %v", moments, err)
	}
	if otherDay, _, err := app.noticedCandidateMoments("2026-10-20", items); err != nil || len(otherDay) != 0 {
		t.Fatalf("moment outside day = %+v, %v", otherDay, err)
	}
	if got, err := app.noticedCheck(noticedTestDay, items); err != nil || len(got) != 1 {
		t.Fatalf("initial suggestions = %+v, %v", got, err)
	}
	if err := app.activity.store.DeleteActivity("chosen"); err != nil {
		t.Fatal(err)
	}
	if got, err := app.noticedCheck(noticedTestDay, items); err != nil || len(got) != 0 {
		t.Fatalf("deleted moment still visible = %+v, %v", got, err)
	}
}

func seedNoticedDismissal(t *testing.T, app *App, item NoticedSuggestion) {
	t.Helper()
	if err := app.activity.store.setNoticedDismissed(noticedDismissKeys(item), true); err != nil {
		t.Fatal(err)
	}
	app.touchDerivedViews(derivedDayNoticed)
}

func TestNoticedNeverRepeatsNotNowOrAnOpenFollowUpAndDropsADeletedMoment(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	saveNoticedMoment(t, app, "m-lamp", "Comparing Halcyon Arc desk lamps for Priya", "Safari", `[{"type":"person","name":"Priya"}]`, noticedTestAt(10, 0))
	saveNoticedMoment(t, app, "m-dinner", "Booking dinner with Dana", "Safari", `[{"type":"person","name":"Dana"}]`, noticedTestAt(18, 0))
	saveNoticedWithSummary(t, app, []noticedRaw{
		{Kind: noticedKindFollowUp, MomentID: "m-lamp", Text: "Follow up with Priya about the Halcyon Arc lamp"},
		{Kind: noticedKindFollowUp, MomentID: "m-dinner", Text: "Confirm the table with Dana"},
	})
	got, _ := app.GetDayNoticed(noticedTestDay)
	if len(got) != 2 {
		t.Fatalf("setup: %+v", got)
	}

	// Not now hides it, and the same words on another moment too.
	seedNoticedDismissal(t, app, got[1])
	got, _ = app.GetDayNoticed(noticedTestDay)
	if len(got) != 1 || got[0].MomentID != "m-lamp" {
		t.Fatalf("after Not now: %+v", got)
	}
	saveNoticedWithSummary(t, app, []noticedRaw{
		{Kind: noticedKindFollowUp, MomentID: "m-lamp", Text: "Follow up with Priya about the Halcyon Arc lamp"},
		{Kind: noticedKindFollowUp, MomentID: "m-lamp", Text: "Confirm the table with Dana!"},
	})
	got, _ = app.GetDayNoticed(noticedTestDay)
	if len(got) != 1 || got[0].Text != "Follow up with Priya about the Halcyon Arc lamp" {
		t.Fatalf("Not now must hold for the same words: %+v", got)
	}

	// An open follow-up on the moment hides its follow-up suggestion.
	added, err := app.addActivityFollowUp("m-lamp", "Follow up with Priya", "k", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ = app.GetDayNoticed(noticedTestDay); len(got) != 0 {
		t.Fatalf("a suggestion repeated an open follow-up: %+v", got)
	}
	if err := app.undoActivityFollowUp(added.OpenLoopID, added.EventID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ = app.GetDayNoticed(noticedTestDay); len(got) != 1 {
		t.Fatalf("Undo of the follow-up brings the suggestion back: %+v", got)
	}

	// Deleting the moment removes the suggestions that cite it.
	if err := app.activity.store.DeleteActivity("m-lamp"); err != nil {
		t.Fatal(err)
	}
	stored, _, err := app.activity.store.noticedStored(noticedTestDay, "en")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range stored {
		if item.MomentID == "m-lamp" {
			t.Fatalf("a suggestion outlived its moment: %+v", stored)
		}
	}
}

func TestNoticedKnowHowFile(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	draft := NoticedRoutineDraft{Name: "Friday expenses", Steps: []string{"Open the expense sheet"}, Wont: []string{"Submit anything"}}
	id, err := app.saveNoticedKnowHow(draft, noticedTestDay, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := knowledgeDir()
	raw, err := os.ReadFile(filepath.Join(dir, id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	entry := parseKnowledgeFile(id+".md", string(raw))
	if entry.Title != "Friday expenses" || !strings.Contains(entry.Content, "1. Open the expense sheet") || !strings.Contains(entry.Content, "Submit anything") {
		t.Fatalf("know-how entry = %+v", entry)
	}
}

func TestNoticedGoesWithTheSummaryItWasMadeWith(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	saveNoticedMoment(t, app, "m-lamp", "Comparing Halcyon Arc desk lamps for Priya", "Safari", `[{"type":"person","name":"Priya"}]`, noticedTestAt(10, 0))
	saveNoticedWithSummary(t, app, []noticedRaw{{Kind: noticedKindFollowUp, MomentID: "m-lamp", Text: "Follow up with Priya about the Halcyon Arc lamp"}})
	if got, _ := app.GetDayNoticed(noticedTestDay); len(got) != 1 {
		t.Fatalf("setup: %+v", got)
	}
	// The summary is written again without suggestions saved for it yet.
	if _, err := app.activity.store.saveSummaryWithSources(noticedTestDay, "daily", "The day, again.", "test-model", false, "en", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := app.GetDayNoticed(noticedTestDay); len(got) != 0 {
		t.Fatalf("suggestions outlived the summary they were made with: %+v", got)
	}
	// Without any summary row (Forget, retention) nothing is shown.
	if _, err := app.activity.store.db.Exec(`DELETE FROM activity_summaries WHERE date = ?`, noticedTestDay); err != nil {
		t.Fatal(err)
	}
	if got, _ := app.GetDayNoticed(noticedTestDay); len(got) != 0 {
		t.Fatalf("suggestions without their summary: %+v", got)
	}
}

func TestActivityNoticedMigrationIsIdempotent(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	ok, err := app.activity.store.activityNoticedPresent()
	if err != nil || !ok {
		t.Fatalf("fresh store = %v %v", ok, err)
	}
	tx, err := app.activity.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := migrateActivityNoticed(tx); err != nil {
		t.Fatalf("replay = %v", err)
	}
}
