package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resetForgetQuiet(t *testing.T) {
	t.Helper()
	previous := forgetQuietUntil.Load()
	forgetQuietUntil.Store(0)
	t.Cleanup(func() { forgetQuietUntil.Store(previous) })
}

// A build that began before Forget everything and finishes after it saves
// nothing: its token is from before the job, and the quiet window holds it.
func TestDerivedBuildStartedBeforeForgetEverythingSavesNothing(t *testing.T) {
	resetForgetQuiet(t)
	app, _ := derivedDiskTestApp(t)
	forgetEverythingTestSteps(t, nil)
	token := app.derivedDiskToken()
	if !token.ok {
		t.Skip("derived disk copies are off in this setup")
	}
	if _, err := app.StartForgetEverything(); err != nil {
		t.Fatal(err)
	}
	if status := app.runForgetEverything(context.Background()); status.State != forgetDone {
		t.Fatalf("job = %+v", status)
	}
	app.derivedDiskSave(derivedKnowHow, "", []byte(`[]`), time.Now(), token)
	if files := derivedDiskFiles(t); len(files) != 0 {
		t.Fatalf("a build from before the job saved %v", files)
	}
	if !forgetCutoff().At().After(time.Time{}) {
		t.Fatal("the job must stamp the cutoff")
	}
}

// While the job runs, automatic memory writes wait; the person's own edits don't.
func TestForgetEverythingHoldsAutomaticMemoryWrites(t *testing.T) {
	resetForgetQuiet(t)
	app, dir := newForgetTestApp(t)
	noteForgetQuiet(ForgetEverythingStatus{State: forgetRunning})
	content := "# Learned\n\n- **Source**: activity\n- **Dimension**: user_insight\n\n## Memory Points\n\n- Learned while forgetting.\n"
	if _, err := app.saveMemoryEntry("learned", content, false); !errors.Is(err, errForgetQuiet) {
		t.Fatalf("automatic write while forgetting = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "learned.md")); !os.IsNotExist(err) {
		t.Fatal("nothing must reach disk")
	}
	if _, err := app.saveMemoryEntry("mine", content, true); err != nil {
		t.Fatalf("the person's own edit must still save: %v", err)
	}
	noteForgetQuiet(ForgetEverythingStatus{State: forgetDone, FinishedAt: time.Now().Add(-time.Hour).Format(time.RFC3339Nano)})
	if derivedSavesQuiet() {
		t.Fatal("the window must end after it")
	}
}
