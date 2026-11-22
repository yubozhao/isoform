package main

import "time"

// watch_phone_quiet.go tells Settings › Your watch when the phone has gone
// quiet (computer-conformance answers W3, W5) and when listening off is still
// waiting to reach the watch (W7). The computer can't see the watch: it knows
// only when it last heard from a paired phone (the bridge's LastSeenAt) and
// when listening was turned off.

const (
	// W5: 3 hours while the watch was listening when the phone went quiet,
	// 12 hours otherwise.
	watchPhoneQuietListening = 3 * time.Hour
	watchPhoneQuietIdle      = 12 * time.Hour
	// The watch counts as listening when the phone went quiet if watch audio
	// arrived within this window before the phone's last contact. The
	// setting alone can be on while the watch is off the wrist. Two hours,
	// because the watch sends its talk by the hour: missing a watch still
	// listening delays the warning to 12 hours, which is worse than early.
	watchListeningRecentAudio = 2 * time.Hour
)

// WatchPhoneQuietView is what Your watch needs for its two lines.
type WatchPhoneQuietView struct {
	// Quiet is true past W5's threshold since the phone was last in touch.
	Quiet bool `json:"quiet"`
	// Since is when a paired phone was last in touch; empty if never.
	Since string `json:"since,omitempty"`
	// Listening is whether the watch was listening when the phone went quiet:
	// watch audio arrived within two hours before the phone's last contact,
	// or, with no audio ever known, no listening-off stretch covered it.
	Listening bool `json:"listening"`
	// OffPending is true while listening is off and no phone has been in
	// touch since it was turned off: the watch can't have heard it yet. The
	// computer has no watch acknowledgement, so the phone's next contact is
	// the condition (W7's line names that time).
	OffPending bool `json:"offPending"`
}

// watchPhoneQuietAt decides both lines from the last contact, when watch
// audio last arrived, the off stretches and now. lastSeen and lastHeard are
// zero when unknown.
func watchPhoneQuietAt(lastSeen, lastHeard time.Time, spans []observationPause, now time.Time) WatchPhoneQuietView {
	var view WatchPhoneQuietView
	var openOff int64
	for _, span := range spans {
		if span.End == 0 {
			openOff = span.Start
		}
	}
	if lastSeen.IsZero() {
		view.OffPending = openOff != 0
		return view
	}
	view.Since = mobileTimestamp(lastSeen)
	seenMs := lastSeen.UnixMilli()
	if !lastHeard.IsZero() {
		view.Listening = !lastHeard.Before(lastSeen.Add(-watchListeningRecentAudio))
	} else {
		view.Listening = true
		for _, span := range spans {
			if seenMs >= span.Start && (span.End == 0 || seenMs < span.End) {
				view.Listening = false
				break
			}
		}
	}
	threshold := watchPhoneQuietIdle
	if view.Listening {
		threshold = watchPhoneQuietListening
	}
	view.Quiet = now.Sub(lastSeen) >= threshold
	view.OffPending = openOff != 0 && seenMs < openOff
	return view
}

// GetWatchPhoneQuiet reads the paired phones' last contact and the listening
// off stretches. A failed read is an error, never a guessed quiet or not.
func (a *App) GetWatchPhoneQuiet() (WatchPhoneQuietView, error) {
	if a == nil || a.store == nil {
		return WatchPhoneQuietView{}, errMobileStoreUnavailable
	}
	spans, err := a.watchListenOffSpans()
	if err != nil {
		return WatchPhoneQuietView{}, err
	}
	now := time.Now()
	var lastSeen time.Time
	if bridge := a.mobileBridgeForUse(); bridge != nil {
		bridge.mu.Lock()
		for _, device := range bridge.devices {
			expires, ok := mobileParseTimestamp(device.ExpiresAt)
			if !ok || !now.Before(expires) {
				continue
			}
			if seen, ok := mobileParseTimestamp(device.LastSeenAt); ok && seen.After(lastSeen) {
				lastSeen = seen
			}
		}
		bridge.mu.Unlock()
	}
	// When watch audio last arrived from a paired phone, as Your watch's
	// state line reads it.
	settings, err := a.GetWatchSettings()
	if err != nil {
		return WatchPhoneQuietView{}, err
	}
	var lastHeard time.Time
	if heard, ok := mobileParseTimestamp(settings.LastHeardAt); ok {
		lastHeard = heard
	}
	return watchPhoneQuietAt(lastSeen, lastHeard, spans, now), nil
}
