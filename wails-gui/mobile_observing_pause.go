package main

import (
	"net/http"
	"sync/atomic"
	"time"
)

// Pausing Isoform from the phone or the watch (PROTOCOL.md 24.10).
//
// One pause covers all of Isoform. The watch has no way to reach the
// computer itself, so the phone asks for it: POST /observing/pause pauses
// observing here for a while, exactly as "Pause for 15 minutes" in the menu
// bar does, and the status route then tells every phone and watch (24.8).
//
// Pausing only ever protects the person, so any paired phone that can write
// may ask for it. Ending a pause is the other direction: it turns observing
// back on. A phone may end only a pause it started; a pause made at the
// computer is ended at the computer.

const (
	mobileObservingPauseDefaultMinutes = 60
	mobileObservingPauseMinMinutes     = 5
	mobileObservingPauseMaxMinutes     = 4 * 60
)

// Seams for tests: the real ones are the menu bar's own controls.
var (
	mobilePauseObserving  = func(a *App, minutes int) error { return a.PauseObservationFor(minutes) }
	mobileResumeObserving = func(a *App) error { return a.ResumeObservation() }
)

// mobilePhonePauseUntil is the end, in Unix milliseconds, of the pause a
// phone last asked for; zero when the current pause was not a phone's.
var mobilePhonePauseUntil atomic.Int64

func (bridge *mobileBridge) observingPauseAnswer() *mobileObservingPause {
	app := bridge.app
	pause := app.observationPauseSnapshot()
	since, _ := app.GetTodayObservationPauseSince()
	return mobileObservingPauseView(pause.Enabled, since, pause.ResumeAt)
}

// serveObservingPause answers POST /observing/pause with {"minutes": n}.
func (bridge *mobileBridge) serveObservingPause(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Minutes int `json:"minutes"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	if input.Minutes == 0 {
		input.Minutes = mobileObservingPauseDefaultMinutes
	}
	if input.Minutes < mobileObservingPauseMinMinutes || input.Minutes > mobileObservingPauseMaxMinutes {
		mobileJSON(w, http.StatusBadRequest, nil, "minutes must be between 5 and 240")
		return
	}
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	if bridge.app.IsClosedEyesModeEnabled() {
		// Already paused: a pause made here is never shortened or taken over by a phone.
		mobileJSON(w, http.StatusOK, map[string]any{"observingPause": bridge.observingPauseAnswer(), "startedHere": false}, "")
		return
	}
	if err := mobilePauseObserving(bridge.app, input.Minutes); err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "the computer could not pause observing")
		return
	}
	mobilePhonePauseUntil.Store(time.Now().Add(time.Duration(input.Minutes) * time.Minute).UnixMilli())
	bridge.notify()
	mobileJSON(w, http.StatusOK, map[string]any{"observingPause": bridge.observingPauseAnswer(), "startedHere": true}, "")
}

// serveObservingResume answers POST /observing/resume. It ends only a pause
// that a phone started and that has not run out.
func (bridge *mobileBridge) serveObservingResume(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if !mobileDecode(w, r, &input) {
		return
	}
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	if !bridge.app.IsClosedEyesModeEnabled() {
		mobilePhonePauseUntil.Store(0)
		mobileJSON(w, http.StatusOK, map[string]any{"observingPause": nil}, "")
		return
	}
	if until := mobilePhonePauseUntil.Load(); until == 0 || time.Now().UnixMilli() >= until {
		mobileJSON(w, http.StatusConflict, map[string]string{"code": "pause_not_from_phone"}, "this pause was made on the computer; resume it there")
		return
	}
	if err := mobileResumeObserving(bridge.app); err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "the computer could not resume observing")
		return
	}
	mobilePhonePauseUntil.Store(0)
	bridge.notify()
	mobileJSON(w, http.StatusOK, map[string]any{"observingPause": nil}, "")
}

// The phone may pause observing here and end a pause it started (24.10).
func init() {
	mobileRegisterRoute("POST /observing/pause", []string{mobileScopeChatWrite}, func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, _ mobileDeviceRecord) {
		bridge.serveObservingPause(w, r)
	})
	mobileRegisterRoute("POST /observing/resume", []string{mobileScopeChatWrite}, func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, _ mobileDeviceRecord) {
		bridge.serveObservingResume(w, r)
	})
	mobileRegisterStatus(func(_ *mobileBridge, status map[string]any) { status["observingPauseControl"] = true })
}
