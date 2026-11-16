package main

import "testing"

// The window saves the language it starts in on every launch. That re-save
// must keep the saved page snapshots, so Today's first reads paint from them;
// it dropped them every launch on 2026-11-15 (first reads 6 to 12 s, live).
// A real change still drops them.
func TestDerivedSnapshotSurvivesTheLaunchLanguageResave(t *testing.T) {
	app, _ := derivedDiskTestApp(t)
	if err := app.SaveUIState(uiLanguageStateKey, "en"); err != nil {
		t.Fatal(err)
	}
	readDerivedTestValue(t, app, derivedKnowHow, func() (derivedTestValue, error) {
		return derivedTestValue{Items: []string{"how-to"}}, nil
	})
	if len(derivedDiskFiles(t)) != 1 {
		t.Fatal("snapshot was not saved")
	}
	if err := app.SaveUIState(uiLanguageStateKey, "en"); err != nil {
		t.Fatal(err)
	}
	if len(derivedDiskFiles(t)) != 1 {
		t.Fatal("re-saving the same language dropped the saved snapshot")
	}
	if err := app.SaveUIState(uiLanguageStateKey, "zh-CN"); err != nil {
		t.Fatal(err)
	}
	if files := derivedDiskFiles(t); len(files) != 0 {
		t.Fatalf("a language change left saved snapshots: %v", files)
	}
}
