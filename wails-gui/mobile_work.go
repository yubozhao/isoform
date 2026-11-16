package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
)

// serveWorkList answers GET /work (PROTOCOL.md section 26): Your work as the
// computer shows it, from the same snapshot, scoped to what this phone may
// read. A phone without chat:read gets no topic proposals, as on /today.
func (bridge *mobileBridge) serveWorkList(w http.ResponseWriter, device mobileDeviceRecord) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	list, err := bridge.app.GetWorkList()
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	bridge.noteWorkReader(device.DeviceID)
	mobileJSON(w, http.StatusOK, mobileWorkListForDevice(list, device), "")
}

// mobileWorkReadersKey keeps the phones that read Your work: the redesigned
// phone, which is pushed only for the brief, meeting prep and finished work
// (engagement.PhonePushKind). An older phone never reads it and keeps every
// push.
const mobileWorkReadersKey = "mobile.work-readers"

var mobileWorkReadersMu sync.Mutex

// workReaders lists the devices that read Your work.
func (bridge *mobileBridge) workReaders() map[string]bool {
	out := map[string]bool{}
	if bridge == nil || bridge.app == nil || bridge.app.store == nil {
		return out
	}
	raw, err := bridge.app.store.GetUIState(context.Background(), mobileWorkReadersKey)
	if err != nil || raw == "" {
		return out
	}
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) != nil {
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// noteWorkReader records a device's first read of Your work.
func (bridge *mobileBridge) noteWorkReader(deviceID string) {
	if deviceID == "" || bridge.app == nil || bridge.app.store == nil {
		return
	}
	mobileWorkReadersMu.Lock()
	defer mobileWorkReadersMu.Unlock()
	readers := bridge.workReaders()
	if readers[deviceID] {
		return
	}
	ids := []string{deviceID}
	for id := range readers {
		ids = append(ids, id)
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return
	}
	_ = bridge.app.store.SetUIState(context.Background(), mobileWorkReadersKey, string(raw))
}

// mobileWorkListForDevice drops what the device's scopes don't cover and
// recounts the list.
func mobileWorkListForDevice(list WorkList, device mobileDeviceRecord) WorkList {
	if mobileHasScope(device.Scopes, mobileScopeChatRead) {
		return list
	}
	out := list
	out.Items, out.ToAnswer, out.BackFromAgents = []WorkItem{}, 0, 0
	for _, item := range list.Items {
		if item.Source == workSourceTopic {
			continue
		}
		item.Order = len(out.Items)
		out.Items = append(out.Items, item)
		switch item.State {
		case workStateOpen:
			out.ToAnswer++
		case workStateBack:
			out.ToAnswer++
			out.BackFromAgents++
		}
	}
	out.Version = workListVersion(out)
	return out
}

// serveWorkAnswer answers POST /work/{id}/answer (section 26.1) through the
// same function as the computer's AnswerWork. The receipt replays the first
// outcome of a retried key; a refused answer stores none.
func (bridge *mobileBridge) serveWorkAnswer(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, id string) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Action         string `json:"action"`
		Reason         string `json:"reason,omitempty"`
		Before         string `json:"before,omitempty"`
		After          string `json:"after,omitempty"`
		Surface        string `json:"surface,omitempty"`
		AnsweredAt     string `json:"answeredAt,omitempty"`
		QueuedAt       string `json:"queuedAt,omitempty"`
		Seq            uint64 `json:"seq,omitempty"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	surface := workSurfacePhone
	switch input.Surface {
	case "", workSurfacePhone:
	case workSurfaceNotification:
		surface = workSurfaceNotification
	default:
		mobileJSON(w, http.StatusBadRequest, nil, "surface must be phone or notification")
		return
	}
	if id == "" || len(id) > 512 {
		mobileJSON(w, http.StatusNotFound, nil, errWorkItemGone.Error())
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		item, err := bridge.app.answerWork(r.Context(), id,
			WorkAnswerInput{Action: input.Action, Reason: input.Reason, Before: input.Before, After: input.After},
			workAnswerMeta{Surface: surface, DeviceID: device.DeviceID, Seq: input.Seq})
		switch {
		case err == nil:
			mobileJSON(w, http.StatusOK, map[string]any{"item": item}, "")
		case errors.Is(err, errWorkItemGone):
			mobileJSON(w, http.StatusNotFound, nil, err.Error())
		case errors.Is(err, errWorkItemChanged):
			mobileJSON(w, http.StatusConflict, map[string]any{"code": "item_changed", "item": item}, err.Error())
		case errors.Is(err, errWorkUndoClosed):
			mobileJSON(w, http.StatusConflict, map[string]any{"code": "undo_closed", "item": item}, err.Error())
		case errors.Is(err, errWorkAnswerInvalid):
			mobileJSON(w, http.StatusBadRequest, nil, err.Error())
		default:
			mobileJSON(w, http.StatusServiceUnavailable, nil, "the answer could not be saved; try again")
		}
	})
}

// serveAnswers answers GET /answers?limit=&before= (section 26.2): the
// answers ledger for Your data on the phone, newest first.
func (bridge *mobileBridge) serveAnswers(w http.ResponseWriter, r *http.Request) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > answersPageMax {
			mobileJSON(w, http.StatusBadRequest, nil, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	page, err := bridge.app.GetAnswers(limit, r.URL.Query().Get("before"))
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, page, "")
}
