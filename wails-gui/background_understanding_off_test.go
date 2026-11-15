package main

import "testing"

// The app no longer starts background understanding since the Desk was
// removed, so every reader treats a nil coordinator as "not running".
func TestBackgroundUnderstandingReadersTolerateNoService(t *testing.T) {
	var nilApp *App
	for _, app := range []*App{nilApp, {}} {
		app.cancelBackgroundUnderstandingForPrimaryActivity()
		if service, err := app.backgroundUnderstandingService(); err == nil || service != nil {
			t.Fatalf("service=%v err=%v", service, err)
		}
	}

	app := &App{}
	snapshot, err := app.GetBackgroundUnderstandingSnapshot()
	if err != nil {
		t.Fatalf("snapshot without a service: %v", err)
	}
	if snapshot.Enabled || snapshot.Status != "unavailable" ||
		snapshot.Sources == nil || len(snapshot.Sources) != 0 ||
		snapshot.Runs == nil || len(snapshot.Runs) != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if _, err := app.GetBackgroundUnderstandingRunDetail("run-1"); err == nil {
		t.Fatal("run detail without a service succeeded")
	}
}
