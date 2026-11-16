package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"wails-gui/internal/pii"
)

// ⌘K (computer-find v1.0.0, proof 2): typing "lena" shows Prep for Lena
// first, in Ready; once answered and then forgotten from Your answers, the
// answer never comes back.
func TestFindAnythingShowsReadyFirstAndNeverAForgottenAnswer(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	found, err := app.FindAnything("lena", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Groups) == 0 || found.Groups[0].Group != findGroupReady || found.Groups[0].Results[0].Title != "Prep for Lena" {
		t.Fatalf("lena = %+v, want Prep for Lena first in Ready", found.Groups)
	}
	if found.Groups[0].Results[0].Forget != nil || found.Groups[0].Results[0].Target.Kind != "work" {
		t.Fatalf("ready result = %+v", found.Groups[0].Results[0])
	}
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionNo, Reason: "not_now"}); err != nil {
		t.Fatal(err)
	}
	answer := findTestGroup(t, app, "lena", findGroupAnswers)
	if len(answer) != 1 || answer[0].Forget == nil || answer[0].Forget.Kind != "answer" {
		t.Fatalf("answers = %+v", answer)
	}
	if err := app.ForgetFound(answer[0].Forget.Kind, answer[0].Forget.ID, ""); err != nil {
		t.Fatal(err)
	}
	if again := findTestGroup(t, app, "lena", findGroupAnswers); len(again) != 0 {
		t.Fatalf("a forgotten answer came back: %+v", again)
	}
}

func findTestGroup(t *testing.T, app *App, query, group string) []FindResult {
	t.Helper()
	found, err := app.FindAnything(query, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range found.Groups {
		if g.Group == group {
			return g.Results
		}
	}
	return nil
}

// ⌘K answers under 100 ms with thousands of kept things per group; a group
// that misses the budget is named and the others still show.
func TestFindAnythingAnswersWithinItsBudget(t *testing.T) {
	many := func(prefix string) func(context.Context, []string) ([]searchCandidate, error) {
		return func(ctx context.Context, _ []string) ([]searchCandidate, error) {
			out := make([]searchCandidate, 3000)
			for i := range out {
				out[i] = searchCandidate{result: SearchResult{ID: fmt.Sprint(i), Title: fmt.Sprintf("%s note %d about the halcyon import", prefix, i), At: int64(i)}}
			}
			return out, nil
		}
	}
	stalled := func(ctx context.Context, _ []string) ([]searchCandidate, error) {
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
		return nil, ctx.Err()
	}
	sources := []findSource{{findGroupReady, many("ready")}, {findGroupConversations, stalled}, {findGroupPeople, many("people")}, {findGroupClaims, many("belief")}, {findGroupAnswers, many("answer")}}
	app := &App{}
	// The redactor's one-time setup runs at the app's first page, not here.
	_ = pii.DisplayText("warm up")
	started := time.Now()
	found, err := app.findAnything(context.Background(), "halcyon 299", 5, sources)
	if took := time.Since(started); took >= 100*time.Millisecond {
		t.Fatalf("⌘K took %v, want under 100 ms", took)
	}
	if err != nil || len(found.FailedGroups) != 1 || found.FailedGroups[0] != findGroupConversations {
		t.Fatalf("failed = %v, %v", found.FailedGroups, err)
	}
	if len(found.Groups) != 4 || found.Groups[0].Group != findGroupReady || len(found.Groups[0].Results) != 5 {
		t.Fatalf("groups = %+v", found.Groups)
	}
	if empty, _ := app.findAnything(context.Background(), "h", 5, sources); len(empty.Groups) != 0 {
		t.Fatal("a one-letter query searched")
	}
}

// A ⌘K search is kept on this computer for Activity's Your day, newest first,
// once per query; Forget removes it.
func TestFindSearchesAreKeptLocallyAndForgettable(t *testing.T) {
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	app := &App{}
	for _, q := range []string{"lena", "halcyon", "Lena", "x"} {
		if err := app.NoteFindSearch(q); err != nil {
			t.Fatal(err)
		}
	}
	kept, err := app.ListFindSearches(0, 0)
	if err != nil || len(kept) != 2 || kept[0].Query != "Lena" || kept[1].Query != "halcyon" {
		t.Fatalf("kept = %+v, %v", kept, err)
	}
	path, _ := findSearchesPath()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the searches file is not private: %v", err)
	}
	if err := app.ForgetFindSearch(kept[0].ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := app.ListFindSearches(0, 0); len(left) != 1 || left[0].Query != "halcyon" {
		t.Fatalf("after Forget = %+v", left)
	}
}
