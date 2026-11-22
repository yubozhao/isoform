package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	watchHeartRateKey = "watch_heart_rate_setting"
	watchPlaceKey     = "watch_place_setting"
)

// WatchSettingsView reads all settings or returns an error, including when the
// recording index is unreadable. Paired is null until all relevant phones have
// reported or one confirms a watch. LastHeardAt is the latest known receipt of
// watch audio from a currently paired phone, independent of its phone heartbeat.
type WatchSettingsView struct {
	Paired      *bool                   `json:"paired"`
	LastHeardAt string                  `json:"lastHeardAt,omitempty"`
	Listening   MobileWatchListening    `json:"listening"`
	Notes       MobileWatchNotesSetting `json:"notes"`
	HeartRate   MobileWatchNotesSetting `json:"heartRate"`
	Place       MobileWatchNotesSetting `json:"place"`
}

func (a *App) readWatchSetting(key string) (MobileWatchNotesSetting, error) {
	if a == nil || a.store == nil {
		return MobileWatchNotesSetting{}, errMobileStoreUnavailable
	}
	// A successful retry also restores an optional field after a failed
	// startup read. Serialize the snapshot with writes of this same key.
	choice := a.contextChoice(key)
	if choice != nil {
		choice.mu.Lock()
		defer choice.mu.Unlock()
	}
	value, err := a.store.GetUIState(context.Background(), key)
	if err != nil {
		return MobileWatchNotesSetting{}, err
	}
	switch value {
	case "on", "off", "":
		if choice != nil {
			choice.publish(value == "off")
		}
		if value == "" {
			return MobileWatchNotesSetting{On: key == watchNotesSettingKey && featureOn(featureWatchNotes)}, nil
		}
		return MobileWatchNotesSetting{On: value == "on", Chosen: true}, nil
	default:
		return MobileWatchNotesSetting{}, errors.New("invalid stored watch setting")
	}
}

// mu serializes settings I/O. Receipt consults only the last published
// decision. Its zero value lets the watch decide until an explicit off.
type watchContextChoice struct {
	mu       sync.Mutex
	off      atomic.Bool
	receipts sync.RWMutex
}

// The caller holds mu. Close admission before draining older receipts.
// Only an explicit off takes the exclusive receipts lock.
func (c *watchContextChoice) publish(off bool) {
	if previous := c.off.Swap(off); off && !previous {
		c.receipts.Lock()
		c.receipts.Unlock()
	}
}

func (a *App) contextChoice(key string) *watchContextChoice {
	switch key {
	case watchHeartRateKey:
		return &a.watchHeartRate
	case watchPlaceKey:
		return &a.watchPlace
	default:
		return nil
	}
}

func (a *App) restoreWatchContextChoices() {
	for _, key := range []string{watchHeartRateKey, watchPlaceKey} {
		if _, err := a.readWatchSetting(key); err != nil {
			log.Print("[recordings] watch context setting unavailable at startup")
		}
	}
}

func (a *App) writeWatchSetting(key string, on bool) error {
	if a == nil || a.store == nil {
		return errMobileStoreUnavailable
	}
	value := "off"
	if on {
		value = "on"
	}
	return a.SaveUIState(key, value)
}

func (a *App) GetWatchSettings() (view WatchSettingsView, err error) {
	if view.Notes, err = a.readWatchSetting(watchNotesSettingKey); err != nil {
		return WatchSettingsView{}, err
	}
	if view.HeartRate, err = a.readWatchSetting(watchHeartRateKey); err != nil {
		return WatchSettingsView{}, err
	}
	if view.Place, err = a.readWatchSetting(watchPlaceKey); err != nil {
		return WatchSettingsView{}, err
	}
	if view.Listening, err = a.watchListening(); err != nil {
		return WatchSettingsView{}, err
	}
	paired := false
	view.Paired = &paired
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return view, nil
	}
	bridge.mu.Lock()
	phones := make(map[string]bool, len(bridge.devices))
	now := time.Now()
	for id, device := range bridge.devices {
		if expires, ok := mobileParseTimestamp(device.ExpiresAt); ok && now.Before(expires) {
			phones[id] = true
		}
	}
	store := bridge.recordingStoreLocked()
	bridge.mu.Unlock()
	unknown := false
	for id := range phones {
		report, err := a.readWatchSetting("watch_paired_" + id)
		if err != nil {
			return WatchSettingsView{}, err
		}
		unknown = unknown || !report.Chosen
		paired = paired || report.On
	}
	if unknown && !paired {
		view.Paired = nil
	}
	if len(phones) > 0 {
		if store == nil {
			return WatchSettingsView{}, errMobileStoreUnavailable
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if err := store.loadLocked(); err != nil {
			return WatchSettingsView{}, err
		}
		for _, r := range store.recordings {
			if r.Source != "watch" || !phones[r.DeviceID] {
				continue
			}
			heard := r.LastPartReceivedAt
			if heard == "" {
				heard = r.ReceivedAt
			}
			if heard > view.LastHeardAt {
				view.LastHeardAt = heard
			}
		}
	}
	return view, nil
}

func (a *App) setWatchSettingView(key string, on bool) (WatchSettingsView, error) {
	if err := a.writeWatchSetting(key, on); err != nil {
		return WatchSettingsView{}, err
	}
	return a.GetWatchSettings()
}

func (a *App) SetWatchNotes(on bool) (WatchSettingsView, error) {
	return a.setWatchSettingView(watchNotesSettingKey, on)
}

func (a *App) SetWatchHeartRate(on bool) (WatchSettingsView, error) {
	return a.setWatchSettingView(watchHeartRateKey, on)
}

func (a *App) SetWatchPlace(on bool) (WatchSettingsView, error) {
	return a.setWatchSettingView(watchPlaceKey, on)
}

func (a *App) SetWatchListeningOff() (WatchSettingsView, error) {
	if err := a.setWatchListeningOff(time.Now()); err != nil {
		return WatchSettingsView{}, err
	}
	return a.GetWatchSettings()
}

func init() {
	for _, s := range []struct{ key, path, status string }{
		{watchNotesSettingKey, "/settings/watch-notes", "watchNotes"},
		{watchHeartRateKey, "/settings/watch-heart-rate", "watchHeartRate"},
		{watchPlaceKey, "/settings/watch-place", "watchPlace"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			mobileRegisterRoute(method+" "+s.path, []string{mobileScopeChatWrite}, func(b *mobileBridge, w http.ResponseWriter, r *http.Request, _ mobileDeviceRecord) {
				if method == http.MethodPost {
					var input struct {
						On *bool `json:"on"`
					}
					if !mobileDecode(w, r, &input) {
						return
					}
					if input.On == nil {
						mobileJSON(w, http.StatusBadRequest, nil, "on is required")
						return
					}
					if err := b.app.writeWatchSetting(s.key, *input.On); err != nil {
						mobileJSON(w, http.StatusServiceUnavailable, nil, "the setting could not be saved")
						return
					}
				}
				setting, err := b.app.readWatchSetting(s.key)
				if err != nil {
					mobileJSON(w, http.StatusServiceUnavailable, nil, "the setting could not be read")
					return
				}
				mobileJSON(w, http.StatusOK, setting, "")
			})
		}
		mobileRegisterStatus(func(b *mobileBridge, status map[string]any) {
			if setting, err := b.app.readWatchSetting(s.key); err == nil {
				status[s.status] = setting
			}
		})
	}
	mobileRegisterStatus(func(_ *mobileBridge, status map[string]any) { status["watchPairing"] = true })
	mobileRegisterRoute("POST /watch/pairing", []string{mobileScopeChatWrite}, func(b *mobileBridge, w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
		var input struct {
			Paired *bool `json:"paired"`
		}
		if !mobileDecode(w, r, &input) {
			return
		}
		if input.Paired == nil {
			mobileJSON(w, http.StatusBadRequest, nil, "paired is required")
			return
		}
		// An authorized request can still be in flight when its phone unpairs.
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.devices[device.DeviceID] == nil {
			mobileJSON(w, http.StatusForbidden, nil, "the phone is no longer paired")
			return
		}
		if err := b.app.writeWatchSetting("watch_paired_"+device.DeviceID, *input.Paired); err != nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "pairing state could not be saved")
			return
		}
		mobileJSON(w, http.StatusOK, input, "")
	})
}

// Called before the bridge starts or under bridge.mu, also held by pairing reports.
// Startup retries cleanup after an interrupted unpair and covers expired phones.
func (b *mobileBridge) pruneWatchPairingReportsLocked() {
	if b.app == nil || b.app.store == nil {
		return
	}
	ids := make([]string, 0, len(b.devices))
	for id := range b.devices {
		ids = append(ids, id)
	}
	if err := b.app.store.PruneWatchPairingReports(context.Background(), ids); err != nil {
		log.Print("[mobile-bridge] watch pairing reports could not be pruned")
	}
}
