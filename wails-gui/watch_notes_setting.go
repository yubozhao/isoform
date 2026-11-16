package main

import (
	"context"
	"net/http"
)

// "Notes for long talks" as the person's own setting (PROTOCOL.md 24.12).
//
// Notes and follow-ups from watch conversations over five minutes began
// behind a build switch (YANSU_FEATURES=watch-notes). The phone's Watch tab
// has a switch for it, so the choice is now stored: once the person has
// chosen, their choice decides. Until they have, the build switch decides, as
// before.
//
// Turning it off stops new notes. Notes already written stay until their
// words go (a deleted hour, a withdrawn transcript), as before.

const watchNotesSettingKey = "watch_notes_setting"

// MobileWatchNotesSetting is the setting as the phone reads it.
type MobileWatchNotesSetting struct {
	On bool `json:"on"`
	// Chosen is true once the person set it themself.
	Chosen bool `json:"chosen"`
}

// watchNotesSetting reads the setting. A store that cannot be read falls back
// to the build switch.
func (a *App) watchNotesSetting() MobileWatchNotesSetting {
	if a != nil && a.store != nil {
		switch value, _ := a.store.GetUIState(context.Background(), watchNotesSettingKey); value {
		case "on":
			return MobileWatchNotesSetting{On: true, Chosen: true}
		case "off":
			return MobileWatchNotesSetting{On: false, Chosen: true}
		}
	}
	return MobileWatchNotesSetting{On: featureOn(featureWatchNotes)}
}

// watchNotesOn says whether notes are written for long watch conversations.
func (a *App) watchNotesOn() bool { return a.watchNotesSetting().On }

// setWatchNotes stores the person's choice.
func (a *App) setWatchNotes(on bool) error {
	if a == nil || a.store == nil {
		return errMobileStoreUnavailable
	}
	value := "off"
	if on {
		value = "on"
	}
	return a.store.SetUIState(context.Background(), watchNotesSettingKey, value)
}

// serveWatchNotes answers GET /settings/watch-notes.
func (bridge *mobileBridge) serveWatchNotes(w http.ResponseWriter) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, bridge.app.watchNotesSetting(), "")
}

// serveWatchNotesSet answers POST /settings/watch-notes with {"on": bool}.
func (bridge *mobileBridge) serveWatchNotesSet(w http.ResponseWriter, r *http.Request) {
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
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	if err := bridge.app.setWatchNotes(*input.On); err != nil {
		mobileJSON(w, http.StatusInternalServerError, nil, "the setting could not be saved")
		return
	}
	mobileJSON(w, http.StatusOK, bridge.app.watchNotesSetting(), "")
}

// "Notes for long talks" (24.12): its routes, and the setting in the status.
func init() {
	mobileRegisterRoute("GET /settings/watch-notes", []string{mobileScopeChatWrite}, func(bridge *mobileBridge, w http.ResponseWriter, _ *http.Request, _ mobileDeviceRecord) {
		bridge.serveWatchNotes(w)
	})
	mobileRegisterRoute("POST /settings/watch-notes", []string{mobileScopeChatWrite}, func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, _ mobileDeviceRecord) {
		bridge.serveWatchNotesSet(w, r)
	})
	mobileRegisterStatus(func(bridge *mobileBridge, status map[string]any) {
		if bridge.app != nil {
			status["watchNotes"] = bridge.app.watchNotesSetting()
		}
	})
}
