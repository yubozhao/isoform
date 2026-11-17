package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func forgetEverythingTestSteps(t *testing.T, steps []forgetEverythingStep) {
	t.Helper()
	previous := forgetEverythingSteps
	forgetEverythingSteps = steps
	// The job's quiet window is process-wide: a job a test runs must not hold
	// back the saves of the tests after it.
	previousQuiet := forgetQuietUntil.Load()
	t.Cleanup(func() {
		forgetQuietUntil.Store(previousQuiet)
		forgetEverythingSteps = previous
		forgetEverythingRun.mu.Lock()
		if forgetEverythingRun.timer != nil {
			forgetEverythingRun.timer.Stop()
		}
		forgetEverythingRun.mu.Unlock()
		if path, err := forgetEverythingPath(); err == nil {
			_ = os.Remove(path)
		}
	})
}

// A step that fails leaves the job pending; the next run does only what is
// left, and a step already done is never run again (a crash between steps
// resumes where it stopped).
func TestForgetEverythingResumesAndRetriesOnlyWhatIsLeft(t *testing.T) {
	runs := map[string]int{}
	failMail := true
	forgetEverythingTestSteps(t, []forgetEverythingStep{
		{"activity", func(*App, context.Context) error { runs["activity"]++; return nil }},
		{"mail", func(*App, context.Context) error {
			runs["mail"]++
			if failMail {
				return errors.New("Mail is busy")
			}
			return nil
		}},
		{"memory", func(*App, context.Context) error { runs["memory"]++; return nil }},
	})
	app := &App{}
	if _, err := app.StartForgetEverything(); err != nil {
		t.Fatal(err)
	}
	status := app.runForgetEverything(context.Background())
	if status.State != forgetPending {
		t.Fatalf("state = %s, want pending after a failed step", status.State)
	}
	if got := loadForgetEverything(); got.State != forgetPending || got.Steps[1].State != "failed" || got.Steps[1].Error == "" {
		t.Fatalf("saved = %+v", got)
	}
	// A restart: the job carries on from what was saved.
	failMail = false
	status = app.runForgetEverything(context.Background())
	if status.State != forgetDone || status.FinishedAt == "" {
		t.Fatalf("state = %+v, want done", status)
	}
	if runs["activity"] != 1 || runs["memory"] != 1 || runs["mail"] != 2 {
		t.Fatalf("runs = %v, want the done steps run once and the failed one again", runs)
	}
	// A finished job doesn't run again by itself.
	if again := app.runForgetEverything(context.Background()); again.State != forgetDone || runs["activity"] != 1 {
		t.Fatalf("a done job ran again: %+v %v", again, runs)
	}
}

func TestForgetEverythingKeepsGoingWhenAStepPanics(t *testing.T) {
	forgetEverythingTestSteps(t, []forgetEverythingStep{
		{"broken", func(*App, context.Context) error { panic("store closed") }},
		{"memory", func(*App, context.Context) error { return nil }},
	})
	app := &App{}
	if _, err := app.StartForgetEverything(); err != nil {
		t.Fatal(err)
	}
	status := app.runForgetEverything(context.Background())
	if status.State != forgetPending || status.Steps[0].State != "failed" || status.Steps[1].State != "done" {
		t.Fatalf("status = %+v", status)
	}
}

// A per-item step is done only when its list comes back empty.
func TestForgetEachIsDoneOnlyWhenNothingIsLeft(t *testing.T) {
	kept := map[string]bool{"a": true, "b": true, "stuck": true}
	list := func() ([]string, error) {
		var ids []string
		for id := range kept {
			ids = append(ids, id)
		}
		return ids, nil
	}
	forget := func(id string) error {
		if id == "stuck" {
			return errors.New("locked")
		}
		delete(kept, id)
		return nil
	}
	if err := forgetEach(list, forget); err == nil {
		t.Fatal("an item left must fail the step")
	}
	delete(kept, "stuck")
	if err := forgetEach(list, forget); err != nil {
		t.Fatal(err)
	}
}

// The memory step goes through the real forget path, entry by entry, and is
// done only when no entry is left.
func TestForgetEverythingMemoryStepForgetsEveryEntry(t *testing.T) {
	app, dir := newForgetTestApp(t)
	for _, id := range []string{"likes-tea", "works-late"} {
		content := "# " + id + "\n\n- **Source**: activity\n- **Dimension**: user_insight\n\n## Memory Points\n\n- " + id + ".\n"
		if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.memoryStore.Sync("test"); err != nil {
		t.Fatal(err)
	}
	if err := app.forgetAllMemory(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := app.GetMemoryEntries()
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries after the memory step = %d %v", len(entries), err)
	}
}

// The answers step is Delete your answers: nothing is left in the ledger.
func TestForgetEverythingAnswersStepEmptiesTheLedger(t *testing.T) {
	judgmentsTestDir(t)
	app, _ := workAnswersTestApp(t)
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionYes}); err != nil {
		t.Fatal(err)
	}
	if err := app.DeleteAnswers(); err != nil {
		t.Fatal(err)
	}
	if page, _ := app.GetAnswers(0, ""); len(page.Answers) != 0 {
		t.Fatalf("answers after the step = %+v", page.Answers)
	}
}
