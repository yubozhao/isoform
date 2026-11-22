package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"isoform-relay/internal/notify"
	"isoform-relay/internal/store"
	"isoform-relay/internal/wire"
)

// claimBody is the device's claim on an invitation.
type claimBody struct {
	Hub        string `json:"hub"`
	ClaimToken string `json:"claimToken"`
	DeviceID   string `json:"deviceId"`
	DevicePub  string `json:"devicePub"`
	CT         string `json:"ct"`
}

// pairingRoute serves GET /v1/pairings/{id} and POST /v1/pairings/{id}/claim.
func (h *Handler) pairingRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/pairings/"), "/")
	if len(parts) == 2 && parts[1] == "claim" {
		if r.Method != http.MethodPost {
			reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
			return
		}
		h.claimPairing(w, r, parts[0])
		return
	}
	if len(parts) != 1 {
		reply(w, http.StatusNotFound, nil, "Route not found")
		return
	}
	if r.Method != http.MethodGet {
		reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
		return
	}
	h.pollPairing(w, r, parts[0])
}

// claimPairing stores a device's claim. Every attempt counts toward the limits,
// including malformed or rejected ones.
func (h *Handler) claimPairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	if !wire.ValidUUID(pairingID) {
		reply(w, http.StatusNotFound, nil, "Pairing not found")
		return
	}
	now := h.Now()
	if !h.allow(w, r, "claim_pairing", pairingID, claimAttempts, claimWindow, now) {
		reply(w, http.StatusTooManyRequests, nil, "Too many claim attempts")
		return
	}
	if !h.allow(w, r, "claim_address", clientIP(r), claimAttempts, claimWindow, now) {
		reply(w, http.StatusTooManyRequests, nil, "Too many claim attempts")
		return
	}
	var body claimBody
	if !decodeBody(w, r, &body) {
		return
	}
	if !wire.ValidUUID(body.Hub) || !wire.ValidToken(body.ClaimToken) || !wire.ValidUUID(body.DeviceID) ||
		!wire.ValidToken(body.DevicePub) || !wire.ValidCiphertext(body.CT, wire.MaxPairingPlaintext) {
		reply(w, http.StatusBadRequest, nil, "Invalid claim")
		return
	}
	claim := store.Claim{Hub: body.Hub, TokenHash: wire.Digest(body.ClaimToken), DeviceID: body.DeviceID, DevicePub: body.DevicePub, CT: body.CT}
	hubID, err := h.Store.ClaimPairing(r.Context(), pairingID, claim, now)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		reply(w, http.StatusNotFound, nil, "Pairing not found")
		return
	case errors.Is(err, store.ErrForbidden):
		reply(w, http.StatusForbidden, nil, "Claim token rejected")
		return
	case errors.Is(err, store.ErrConflict):
		reply(w, http.StatusConflict, nil, "Pairing is no longer open")
		return
	case errors.Is(err, store.ErrExpired):
		reply(w, http.StatusGone, nil, "Pairing has expired")
		return
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to store claim")
		return
	}
	h.Hubs.Kick(hubID)
	h.Notify.Notify(notify.PairingKey(pairingID))
	reply(w, http.StatusAccepted, map[string]string{"state": "claimed"}, "")
}

// pollPairing long-polls the pairing's state for the holder of the claim token.
// It returns when the state leaves claimed, or after longPollMax.
func (h *Handler) pollPairing(w http.ResponseWriter, r *http.Request, pairingID string) {
	if !wire.ValidUUID(pairingID) {
		reply(w, http.StatusNotFound, nil, "Pairing not found")
		return
	}
	claimToken := r.Header.Get("X-Yansu-Claim")
	if !wire.ValidToken(claimToken) {
		reply(w, http.StatusUnauthorized, nil, "Claim token required")
		return
	}
	digest := wire.Digest(claimToken)
	deadline := time.Now().Add(longPollMax)
	sawClaimed := false
	for {
		changed, cancel := h.Notify.Watch(notify.PairingKey(pairingID))
		status, err := h.Store.PollPairing(r.Context(), pairingID, digest, h.Now())
		if errors.Is(err, store.ErrNotFound) && sawClaimed {
			// The janitor removed an expired pairing while this poll waited.
			status, err = store.PairingStatus{State: "expired"}, nil
		}
		if err != nil {
			cancel()
			writePollError(w, err)
			return
		}
		if status.State != "claimed" {
			cancel()
			h.finishPoll(w, pairingID, status)
			return
		}
		sawClaimed = true
		remaining := time.Until(deadline)
		if remaining <= 0 {
			cancel()
			reply(w, http.StatusOK, map[string]string{"state": "claimed"}, "")
			return
		}
		timer := time.NewTimer(min(remaining, time.Second))
		select {
		case <-changed:
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
			cancel()
			return
		}
		timer.Stop()
		cancel()
	}
}

// finishPoll answers a settled pairing. The approval is served once: after the
// response is written it is deleted, so later polls report approved without it.
func (h *Handler) finishPoll(w http.ResponseWriter, pairingID string, status store.PairingStatus) {
	if status.State != "approved" || status.ApprovalCT == "" {
		reply(w, http.StatusOK, map[string]string{"state": status.State}, "")
		return
	}
	reply(w, http.StatusOK, map[string]any{"state": "approved", "approval": map[string]string{"ct": status.ApprovalCT}}, "")
	ctx, cancel := storeContext(context.Background())
	defer cancel()
	_ = h.Store.ConsumeApproval(ctx, pairingID)
}

func writePollError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		reply(w, http.StatusNotFound, nil, "Pairing not found")
	case errors.Is(err, store.ErrForbidden):
		reply(w, http.StatusForbidden, nil, "Claim token rejected")
	case errors.Is(err, store.ErrConflict):
		reply(w, http.StatusConflict, nil, "Pairing has not been claimed")
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to read pairing")
	}
}
