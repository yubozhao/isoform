package main

import "time"

// mobileObservingPause says that observing is paused on the computer. Since
// is when the pause began and Until when it ends by itself; each is left out
// when the computer does not know it (a pause with no end; a start it cannot
// vouch for).
type mobileObservingPause struct {
	Since string `json:"since,omitempty"`
	Until string `json:"until,omitempty"`
}

// mobileObservingPauseView is the status field for a pause; nil when
// observing is not paused.
func mobileObservingPauseView(paused bool, sinceMs int64, until time.Time) *mobileObservingPause {
	if !paused {
		return nil
	}
	view := &mobileObservingPause{}
	if sinceMs > 0 {
		view.Since = mobileTimestamp(time.UnixMilli(sinceMs))
	}
	if !until.IsZero() {
		view.Until = mobileTimestamp(until)
	}
	return view
}

// The status says when observing is paused on this computer (24.8), so the
// phone can tell the watch to stop recording too.
func init() {
	mobileRegisterStatus(func(bridge *mobileBridge, status map[string]any) {
		app := bridge.app
		if app == nil {
			return
		}
		pause := app.observationPauseSnapshot()
		since, _ := app.GetTodayObservationPauseSince()
		if view := mobileObservingPauseView(pause.Enabled, since, pause.ResumeAt); view != nil {
			status["observingPause"] = view
		}
	})
}
