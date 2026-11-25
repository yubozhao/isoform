package main

import (
	"encoding/hex"
	"net/http"
)

// A decline belongs to the week, so another device and a later brief for the
// same week see the same choice. The phone cannot choose the stored week.
func (bridge *mobileBridge) serveWeekDecline(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, briefID string) {
	if !featureOn(featureBriefExtras) {
		mobileJSON(w, http.StatusConflict, nil, mobileBriefChangedMessage)
		return
	}
	if bridge.app == nil || bridge.app.storeForMobile() == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "the computer is not ready yet")
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Version        string `json:"version"`
	}
	if r.Method == http.MethodPost {
		if !mobileDecode(w, r, &input) {
			return
		}
	} else {
		input.Version = r.URL.Query().Get("version")
	}
	version, err := hex.DecodeString(input.Version)
	if !mobileValidBriefID(briefID) || err != nil || len(version) != 32 {
		mobileJSON(w, http.StatusBadRequest, nil, "the brief reference is not valid")
		return
	}
	apply := func(w http.ResponseWriter) {
		brief, ok := bridge.app.todayChatDeliveredBrief(briefID)
		if !ok || brief.ConversationKey != input.Version || brief.YourWeek == nil || brief.YourWeek.Ask == nil || brief.YourWeek.Start == "" || brief.YourWeek.End == "" {
			mobileJSON(w, http.StatusConflict, nil, mobileBriefChangedMessage)
			return
		}
		key, err := todayYourWeekDeclineKey(brief.YourWeek.Start)
		if err != nil {
			mobileJSON(w, http.StatusConflict, nil, mobileBriefChangedMessage)
			return
		}
		if r.Method == http.MethodPost {
			if err := bridge.app.storeForMobile().SetUIState(r.Context(), key, "true"); err != nil {
				mobileJSON(w, http.StatusInternalServerError, nil, "the decline could not be saved")
				return
			}
		}
		declined, err := bridge.app.todayYourWeekDeclined(r.Context(), brief.YourWeek.Start, brief.YourWeek.End)
		if err != nil {
			mobileJSON(w, http.StatusInternalServerError, nil, "the decline could not be read")
			return
		}
		mobileJSON(w, http.StatusOK, map[string]bool{"declined": declined}, "")
	}
	if r.Method == http.MethodPost {
		key, ok := mobileKey(device.DeviceID, input.IdempotencyKey)
		if !ok {
			mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key is required")
			return
		}
		bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, apply)
		return
	}
	apply(w)
}
