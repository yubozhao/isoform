package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"isoform-relay/internal/notify"
	"isoform-relay/internal/store"
	"isoform-relay/internal/wire"
)

// briefView is one stored brief as the device receives it.
type briefView struct {
	Version uint64          `json:"version"`
	AsOf    string          `json:"asOf"`
	Env     json.RawMessage `json:"env"`
}

func (h *Handler) presence(w http.ResponseWriter, r *http.Request) {
	device, ok := h.authDevice(w, r, "presence:read")
	if !ok {
		return
	}
	seen, err := h.Store.HubLastSeen(r.Context(), device.HubID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		reply(w, http.StatusInternalServerError, nil, "Unable to read presence")
		return
	}
	var lastSeen any
	if !seen.IsZero() {
		lastSeen = wire.FormatTime(seen)
	}
	cause, reason, at, err := h.Store.HubGoodbye(r.Context(), device.HubID)
	if err != nil {
		reply(w, http.StatusInternalServerError, nil, "Unable to read presence")
		return
	}
	view := map[string]any{"hubOnline": h.Hubs.Online(device.HubID), "lastSeenAt": lastSeen}
	if cause != "" && !view["hubOnline"].(bool) {
		view["goodbyeCause"] = cause
		view["goodbyeAt"] = wire.FormatTime(at)
		if reason != "" {
			view["goodbyeReason"] = reason
		}
	}
	reply(w, http.StatusOK, view, "")
}

func (h *Handler) latestBrief(w http.ResponseWriter, r *http.Request) {
	device, ok := h.authDevice(w, r, "brief:read")
	if !ok {
		return
	}
	brief, err := h.Store.LatestBrief(r.Context(), device.ID, h.Now())
	switch {
	case err == nil:
		reply(w, http.StatusOK, briefView{Version: brief.Version, AsOf: brief.AsOf, Env: brief.Body}, "")
	case errors.Is(err, store.ErrNotFound):
		reply(w, http.StatusNotFound, nil, "No brief is available")
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to read brief")
	}
}

func (h *Handler) listBriefs(w http.ResponseWriter, r *http.Request) {
	device, ok := h.authDevice(w, r, "brief:read")
	if !ok {
		return
	}
	limit := uint64(store.KeptBriefs)
	if values, present := r.URL.Query()["limit"]; present {
		parsed, valid := parseUint(values, store.KeptBriefs)
		if !valid || parsed < 1 {
			reply(w, http.StatusBadRequest, nil, "limit must be between 1 and 7")
			return
		}
		limit = parsed
	}
	briefs, err := h.Store.ListBriefs(r.Context(), device.ID, int(limit), h.Now())
	if err != nil {
		reply(w, http.StatusInternalServerError, nil, "Unable to list briefs")
		return
	}
	views := make([]briefView, 0, len(briefs))
	for _, brief := range briefs {
		views = append(views, briefView{Version: brief.Version, AsOf: brief.AsOf, Env: brief.Body})
	}
	reply(w, http.StatusOK, map[string]any{"briefs": views}, "")
}

// postRequest queues a device request for its hub, delivering it at once when
// the hub is connected. Scope for the request's path is enforced by the hub,
// since the path is inside the ciphertext the relay never reads.
func (h *Handler) postRequest(w http.ResponseWriter, r *http.Request) {
	device, ok := h.authDevice(w, r, "")
	if !ok {
		return
	}
	var env wire.Envelope
	if !decodeBody(w, r, &env) {
		return
	}
	if env.Validate() != nil || env.Dir != "d2h" || env.Kind != "req" {
		reply(w, http.StatusBadRequest, nil, "Invalid request envelope")
		return
	}
	if env.Hub != device.HubID || env.Dev != device.ID {
		refuse(w, r, http.StatusForbidden, "Envelope does not belong to this device")
		return
	}
	body, err := env.Canonical()
	if err != nil {
		reply(w, http.StatusBadRequest, nil, "Invalid request envelope")
		return
	}
	err = h.Store.AddRequest(r.Context(), device, env, body, h.Now())
	switch {
	case err == nil:
	case errors.Is(err, store.ErrQueueFull):
		setRetryAfter(w, time.Minute)
		h.logLimitError(w, r, err)
		reply(w, http.StatusTooManyRequests, nil, "Request queue is full")
		return
	case errors.Is(err, store.ErrConflict):
		reply(w, http.StatusConflict, nil, "Sequence number already used for different content")
		return
	default:
		// The cause, never the request: on 2026-11-20 one phone request ended 500
		// and the log could not say whether the phone had gone or the store failed.
		log.Print(queueFailureLine(err))
		reply(w, http.StatusInternalServerError, nil, "Unable to queue request")
		return
	}
	h.Hubs.Kick(device.HubID)
	reply(w, http.StatusAccepted, map[string]bool{"accepted": true}, "")
}

// responses returns the hub's envelopes after a cursor. With wait it holds the
// request open until something arrives or the wait ends (capped at 25 seconds).
func (h *Handler) responses(w http.ResponseWriter, r *http.Request) {
	device, ok := h.authDevice(w, r, "brief:read")
	if !ok {
		return
	}
	query := r.URL.Query()
	var after uint64
	if values, present := query["after"]; present {
		parsed, valid := parseUint(values, wire.MaxSeq)
		if !valid {
			reply(w, http.StatusBadRequest, nil, "after must be a sequence number")
			return
		}
		after = parsed
	}
	var wait time.Duration
	if values, present := query["wait"]; present {
		parsed, valid := parseUint(values, 1<<31)
		if !valid {
			reply(w, http.StatusBadRequest, nil, "wait must be a number of seconds")
			return
		}
		wait = min(time.Duration(parsed)*time.Second, longPollMax)
	}
	began := time.Now()
	deadline := began.Add(wait)
	for {
		changed, cancel := h.Notify.Watch(notify.DeviceKey(device.ID))
		envelopes, err := h.Store.ResponsesSince(r.Context(), device.ID, after, h.Now(), responsePageSize)
		if errors.Is(err, store.ErrUnauthorized) {
			cancel()
			refuse(w, r, http.StatusUnauthorized, "Device authentication required")
			return
		}
		if err != nil {
			cancel()
			reply(w, http.StatusInternalServerError, nil, "Unable to read responses")
			return
		}
		remaining := time.Until(deadline)
		if len(envelopes) > 0 || remaining <= 0 {
			cancel()
			if len(envelopes) > 0 {
				log.Print(answerPollLine(time.Since(began), len(envelopes), false))
			}
			reply(w, http.StatusOK, map[string]any{"envelopes": envelopes}, "")
			return
		}
		timer := time.NewTimer(min(remaining, time.Second))
		select {
		case <-changed:
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
			cancel()
			log.Print(answerPollLine(time.Since(began), 0, true))
			return
		}
		timer.Stop()
		cancel()
	}
}

func (h *Handler) ackResponses(w http.ResponseWriter, r *http.Request) {
	device, ok := h.authDevice(w, r, "brief:read")
	if !ok {
		return
	}
	var body struct {
		UpTo *uint64 `json:"upTo"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.UpTo == nil || *body.UpTo > wire.MaxSeq {
		reply(w, http.StatusBadRequest, nil, "upTo must be a sequence number")
		return
	}
	if err := h.Store.AckResponses(r.Context(), device.ID, *body.UpTo); err != nil {
		reply(w, http.StatusInternalServerError, nil, "Unable to acknowledge responses")
		return
	}
	reply(w, http.StatusOK, map[string]uint64{"upTo": *body.UpTo}, "")
}

// unpair removes the calling device and everything stored for it. The hub is
// told when it is connected and reconciles from its device list otherwise.
func (h *Handler) unpair(w http.ResponseWriter, r *http.Request) {
	device, ok := h.authDevice(w, r, "")
	if !ok {
		return
	}
	err := h.Store.RemoveDevice(r.Context(), device.ID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		refuse(w, r, http.StatusUnauthorized, "Device authentication required")
		return
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to unpair device")
		return
	}
	h.Hubs.Unpaired(device.HubID, device.ID)
	h.Notify.Notify(notify.DeviceKey(device.ID))
	reply(w, http.StatusOK, map[string]bool{"unpaired": true}, "")
}
