package main

import "testing"

// A launch that misses its saved snapshot names why in the log, so a slow
// first read can be traced to its cause (Today's first reads after launch
// took 6 to 12 s on 2026-11-15 with no line saying the copy was missing).
func TestDerivedDiskLoadNamesWhyItMissed(t *testing.T) {
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	app, _ := derivedDiskTestApp(t)
	var timing derivedDiskLoadTiming
	if _, _, result := app.derivedDiskLoadTimed(derivedAboutYou, "test", &timing); result != "missing" {
		t.Fatalf("no saved copy: result %q, want missing", result)
	}
	readDerivedTestValue(t, app, derivedAboutYou, func() (derivedTestValue, error) {
		return derivedTestValue{Items: []string{"saved"}}, nil
	})
	if _, _, result := app.derivedDiskLoadTimed(derivedAboutYou, "test", &timing); result != "hit" {
		t.Fatalf("saved copy: result %q, want hit", result)
	}
	if _, _, result := app.derivedDiskLoadTimed(derivedTodayDayShape, "test", &timing); result != "missing" {
		t.Fatalf("another view: result %q, want missing", result)
	}
}
