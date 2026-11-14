package main

import (
	"testing"
	"time"
)

// A mail sync's Today refresh is counted by the brief service before the event
// returns, so waiting for the service waits for that refresh too. When the work
// slot was taken inside a goroutine, the wait could return before the refresh
// began, and a read right after it saw the copy sealed before the mail.
func TestMailSyncTodayRefreshIsTrackedBeforeTheEventReturns(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	svc := newMobileBriefService(app)
	t.Cleanup(svc.Stop)
	for i := 0; i < 20; i++ {
		before := mobileBriefTodayRefreshRuns.Load()
		// Each attempt is due: the last refresh is past the gap.
		svc.todayRefreshMu.Lock()
		svc.todayRefreshAt = time.Now().Add(-todayRefreshGap - time.Second)
		svc.todayRefreshMu.Unlock()
		app.emitBrowserEvent("mail:synced")
		svc.todayRefreshWG.Wait()
		if mobileBriefTodayRefreshRuns.Load() == before {
			t.Fatalf("attempt %d: the refresh wait returned before the mail sync's Today refresh ran", i)
		}
		svc.wg.Wait()
	}
}
