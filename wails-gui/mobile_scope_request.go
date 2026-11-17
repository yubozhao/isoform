package main

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// A paired phone asks for a scope it did not hold at pairing, and the person
// allows it on the computer (PROTOCOL.md 10.1). Only chat:correct can be added
// this way; every other scope still needs a new pairing. The relay is not
// involved: the computer enforces chat:correct itself.

// mobileUpgradableScopes are the scopes a paired phone may ask for later.
var mobileUpgradableScopes = []string{mobileScopeChatCorrect}

// mobileScopeDeclineHold is how long a Not now stands before the phone may ask
// again.
const mobileScopeDeclineHold = 24 * time.Hour

const (
	mobileScopeRequestWaiting  = "waiting"
	mobileScopeRequestDeclined = "declined"
	mobileScopeRequestGranted  = "granted"
)

// MobileScopeRequest is a phone's open or recently declined ask.
type MobileScopeRequest struct {
	Scopes []string `json:"scopes"`
	State  string   `json:"state"`
	At     string   `json:"at"`
}

type mobileScopeRequestState struct {
	scopes []string
	state  string
	at     time.Time
}

var (
	errMobileScopeRequestGone   = errors.New("that phone request is no longer waiting")
	errMobileScopeNotUpgradable = errors.New("only chat:correct can be added to a paired phone")
)

// scopeRequestViewLocked is the device's ask as the phone and Settings read it.
// A decline shows until its hold ends. Callers hold bridge.mu.
func (bridge *mobileBridge) scopeRequestViewLocked(deviceID string, now time.Time) *MobileScopeRequest {
	request, ok := bridge.scopeRequests[deviceID]
	if !ok {
		return nil
	}
	if request.state == mobileScopeRequestDeclined && now.Sub(request.at) >= mobileScopeDeclineHold {
		delete(bridge.scopeRequests, deviceID)
		return nil
	}
	return &MobileScopeRequest{Scopes: append([]string{}, request.scopes...), State: request.state, At: mobileTimestamp(request.at)}
}

// serveScopeRequest answers POST /devices/{id}/scope-request.
func (bridge *mobileBridge) serveScopeRequest(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, deviceID string) {
	if deviceID != device.DeviceID {
		mobileJSON(w, http.StatusForbidden, nil, "a paired phone may only ask for itself")
		return
	}
	var input struct {
		IdempotencyKey string   `json:"idempotencyKey"`
		Add            []string `json:"add"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	if _, valid := mobileKey(device.DeviceID, input.IdempotencyKey); !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if len(input.Add) == 0 || len(input.Add) > len(mobileUpgradableScopes) {
		mobileJSON(w, http.StatusBadRequest, nil, errMobileScopeNotUpgradable.Error())
		return
	}
	for _, scope := range input.Add {
		if !mobileHasScope(mobileUpgradableScopes, scope) {
			mobileJSON(w, http.StatusBadRequest, nil, errMobileScopeNotUpgradable.Error())
			return
		}
	}
	now := time.Now()
	bridge.mu.Lock()
	record := bridge.devices[device.DeviceID]
	if record == nil {
		bridge.mu.Unlock()
		mobileJSON(w, http.StatusNotFound, nil, errMobileDeviceNotFound.Error())
		return
	}
	missing := []string{}
	for _, scope := range input.Add {
		if !mobileHasScope(record.Scopes, scope) && !mobileHasScope(missing, scope) {
			missing = append(missing, scope)
		}
	}
	if len(missing) == 0 {
		scopes := append([]string{}, record.Scopes...)
		bridge.mu.Unlock()
		mobileJSON(w, http.StatusOK, map[string]any{"state": mobileScopeRequestGranted, "scopes": scopes}, "")
		return
	}
	if view := bridge.scopeRequestViewLocked(device.DeviceID, now); view != nil && view.State == mobileScopeRequestDeclined {
		bridge.mu.Unlock()
		mobileJSON(w, http.StatusConflict, map[string]any{"code": mobileScopeRequestDeclined}, "you said not now on the computer")
		return
	}
	if bridge.scopeRequests == nil {
		bridge.scopeRequests = map[string]mobileScopeRequestState{}
	}
	bridge.scopeRequests[device.DeviceID] = mobileScopeRequestState{scopes: missing, state: mobileScopeRequestWaiting, at: now}
	bridge.mu.Unlock()
	bridge.notify()
	mobileJSON(w, http.StatusAccepted, map[string]any{"state": mobileScopeRequestWaiting}, "")
}

// confirmScopeRequest is the person's Allow or Not now on the computer. Allow
// adds only the asked scopes that can be added, and is written to the audit
// log like every phone request.
func (bridge *mobileBridge) confirmScopeRequest(deviceID string, approve bool) error {
	now := time.Now()
	bridge.mu.Lock()
	record := bridge.devices[deviceID]
	request, ok := bridge.scopeRequests[deviceID]
	if record == nil || !ok || request.state != mobileScopeRequestWaiting {
		bridge.mu.Unlock()
		return errMobileScopeRequestGone
	}
	status := http.StatusForbidden
	if approve {
		before := append([]string{}, record.Scopes...)
		for _, scope := range request.scopes {
			if mobileHasScope(mobileUpgradableScopes, scope) && !mobileHasScope(record.Scopes, scope) {
				record.Scopes = append(record.Scopes, scope)
			}
		}
		normalized, err := mobileNormalizeScopes(record.Scopes)
		if err == nil {
			record.Scopes = normalized
			err = bridge.persistDevicesLocked()
		}
		if err != nil {
			record.Scopes = before
			bridge.mu.Unlock()
			return err
		}
		delete(bridge.scopeRequests, deviceID)
		status = http.StatusOK
	} else {
		bridge.scopeRequests[deviceID] = mobileScopeRequestState{scopes: request.scopes, state: mobileScopeRequestDeclined, at: now}
	}
	bridge.mu.Unlock()
	audit := mobileAuditEntry{TS: mobileTimestamp(now), DeviceID: deviceID, Method: "CONFIRM", Path: "/devices/" + deviceID + "/scope-request/" + strings.Join(request.scopes, ","), Status: status}
	if err := appendMobileAudit(bridge.stateDir, audit); err != nil {
		log.Printf("[mobile-bridge] could not write the audit log: %v", err)
	}
	bridge.notify()
	return nil
}

// ConfirmMobileScopeRequest allows or declines a paired phone's ask for a
// scope it did not hold at pairing.
func (a *App) ConfirmMobileScopeRequest(deviceID string, approve bool) (MobileConnectionStatus, error) {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return MobileConnectionStatus{}, errMobileScopeRequestGone
	}
	if err := bridge.confirmScopeRequest(strings.TrimSpace(deviceID), approve); err != nil {
		return MobileConnectionStatus{}, err
	}
	return bridge.connectionStatus(), nil
}
