package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Only a change in what the person said to keep out of their briefs counts as
// a privacy action; other memory events leave the sealed briefs alone.
func TestMobileBriefExclusionsChangedOnlyWhenTheStatedSetChanges(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(8, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	app := svc.app
	if app.mobileBriefExclusionsChangedForTest() {
		t.Fatal("the first read of the stated exclusions read as a change")
	}
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.md"), []byte("# Note\n\nThe train leaves at nine.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checkMemoryFolderNow(app)
	if app.mobileBriefExclusionsChangedForTest() {
		t.Fatal("an unrelated memory note read as a privacy change")
	}
	if err := os.WriteFile(filepath.Join(dir, "brief-preference.md"), []byte("# Brief preference\n\nKeep PT out of my briefs.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checkMemoryFolderNow(app)
	if !app.mobileBriefExclusionsChangedForTest() {
		t.Fatal("a new stated exclusion did not read as a change")
	}
	if app.mobileBriefExclusionsChangedForTest() {
		t.Fatal("the same stated exclusions read as a change twice")
	}
}

// mobileBriefExclusionsChangedForTest reads the stated exclusions and reports
// whether the read noticed a change.
func (a *App) mobileBriefExclusionsChangedForTest() bool {
	before := mobileBriefPrivacyEpoch.Load()
	a.mobileBriefExclusions()
	return mobileBriefPrivacyEpoch.Load() != before
}
