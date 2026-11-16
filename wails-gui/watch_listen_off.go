package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// "Listen on your watch", the off from the phone (design's pass-questions
// v1.0.0; PROTOCOL.md 24.14). The phone can only turn listening off, never
// on: watchOS cannot be relied on to start listening from the phone, and
// starting to listen is the wearer's own act on the watch.
//
// Off takes effect on the computer at once: whatever the watch recorded after
// that moment is dropped here, exactly as audio from a pause is (24.6), even
// if the watch has not yet heard that it should stop. The watch stops at its
// next sync. Listening is on again when the wearer starts it on the watch; the
// phone then says so, with the time the watch began.
//
// Kept as stretches of time in UI state. A stretch with no end is the off that
// holds now. Stretches older than the recordings the computer keeps are
// dropped when the setting is written.

const (
	watchListenOffKey = "watch_listen_off"
	// watchListenOffKeep is how long a closed stretch is remembered: longer
	// than a watch recording can wait on the watch or the phone.
	watchListenOffKeep = 30 * 24 * time.Hour
)

// MobileWatchListening is the setting as the phone reads it.
type MobileWatchListening struct {
	// Off is true while the phone's off holds.
	Off bool `json:"off"`
	// Since is when it was turned off; absent while listening is allowed.
	Since string `json:"since,omitempty"`
}

// watchListenOffSpans reads the stretches when listening was turned off. An
// open stretch has End 0, as an open pause does.
func (a *App) watchListenOffSpans() ([]observationPause, error) {
	if a == nil || a.store == nil {
		return nil, nil
	}
	raw, err := a.store.GetUIState(context.Background(), watchListenOffKey)
	if err != nil || raw == "" {
		return nil, err
	}
	var spans []observationPause
	if err := json.Unmarshal([]byte(raw), &spans); err != nil {
		return nil, err
	}
	return spans, nil
}

func (a *App) writeWatchListenOffSpans(spans []observationPause, now time.Time) error {
	cutoff := now.Add(-watchListenOffKeep).UnixMilli()
	kept := spans[:0:0]
	for _, span := range spans {
		if span.End == 0 || span.End > cutoff {
			kept = append(kept, span)
		}
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	return a.store.SetUIState(context.Background(), watchListenOffKey, string(raw))
}

// watchListening reports the setting now.
func (a *App) watchListening() MobileWatchListening {
	spans, err := a.watchListenOffSpans()
	if err != nil {
		return MobileWatchListening{}
	}
	for _, span := range spans {
		if span.End == 0 {
			return MobileWatchListening{Off: true, Since: mobileTimestamp(time.UnixMilli(span.Start))}
		}
	}
	return MobileWatchListening{}
}

// setWatchListeningOff turns listening off at now. Already off changes nothing.
func (a *App) setWatchListeningOff(now time.Time) error {
	if a == nil || a.store == nil {
		return errMobileStoreUnavailable
	}
	spans, err := a.watchListenOffSpans()
	if err != nil {
		return err
	}
	for _, span := range spans {
		if span.End == 0 {
			return nil
		}
	}
	return a.writeWatchListenOffSpans(append(spans, observationPause{Start: now.UnixMilli()}), now)
}

// endWatchListeningOff records that the wearer started listening on the watch
// at startedAt. The off ends then, never earlier than it began and never
// later than now, so audio from before the wearer's own start stays dropped.
func (a *App) endWatchListeningOff(startedAt, now time.Time) error {
	if a == nil || a.store == nil {
		return errMobileStoreUnavailable
	}
	spans, err := a.watchListenOffSpans()
	if err != nil {
		return err
	}
	changed := false
	for i := range spans {
		if spans[i].End != 0 {
			continue
		}
		end := startedAt.UnixMilli()
		if end > now.UnixMilli() {
			end = now.UnixMilli()
		}
		if end <= spans[i].Start {
			end = spans[i].Start + 1
		}
		spans[i].End = end
		changed = true
	}
	if !changed {
		return nil
	}
	return a.writeWatchListenOffSpans(spans, now)
}

// mobileRecordingDropSpans is everything a recording's audio must not hold:
// the pauses, and for a watch recording the stretches when listening on the
// watch was turned off. An error means nothing may be read yet.
func (a *App) mobileRecordingDropSpans(source string) ([]observationPause, error) {
	pauses, err := mobileRecordingPauses(a)
	if err != nil || source == "phone" {
		return pauses, err
	}
	off, err := a.watchListenOffSpans()
	if err != nil {
		return nil, err
	}
	return append(append([]observationPause{}, pauses...), off...), nil
}

// serveWatchListening answers GET /watch/listening.
func (bridge *mobileBridge) serveWatchListening(w http.ResponseWriter) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, bridge.app.watchListening(), "")
}

// serveWatchListeningSet answers POST /watch/listening. {"off": true} turns
// listening off now. {"off": false, "startedAt": "..."} says the wearer
// started listening on the watch at that time; without a time it is refused,
// because the phone itself can never turn listening on.
func (bridge *mobileBridge) serveWatchListeningSet(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Off       *bool  `json:"off"`
		StartedAt string `json:"startedAt"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	if input.Off == nil {
		mobileJSON(w, http.StatusBadRequest, nil, "off is required")
		return
	}
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	now := time.Now()
	var err error
	if *input.Off {
		err = bridge.app.setWatchListeningOff(now)
	} else {
		startedAt, parseErr := time.Parse(time.RFC3339, input.StartedAt)
		if parseErr != nil {
			mobileJSON(w, http.StatusBadRequest, nil, "startedAt is required: listening starts on the watch")
			return
		}
		err = bridge.app.endWatchListeningOff(startedAt, now)
	}
	if err != nil {
		mobileJSON(w, http.StatusInternalServerError, nil, "the setting could not be saved")
		return
	}
	mobileJSON(w, http.StatusOK, bridge.app.watchListening(), "")
}

// "Listen on your watch" (24.14): its routes, and the setting in the status.
func init() {
	mobileRegisterRoute("GET /watch/listening", []string{mobileScopeChatWrite}, func(bridge *mobileBridge, w http.ResponseWriter, _ *http.Request, _ mobileDeviceRecord) {
		bridge.serveWatchListening(w)
	})
	mobileRegisterRoute("POST /watch/listening", []string{mobileScopeChatWrite}, func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, _ mobileDeviceRecord) {
		bridge.serveWatchListeningSet(w, r)
	})
	mobileRegisterStatus(func(bridge *mobileBridge, status map[string]any) {
		if bridge.app != nil {
			status["watchListening"] = bridge.app.watchListening()
		}
	})
}
