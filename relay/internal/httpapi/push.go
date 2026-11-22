package httpapi

import (
	"errors"
	"net/http"

	"isoform-relay/internal/push"
	"isoform-relay/internal/store"
	"isoform-relay/internal/wire"
)

// pushBody registers one APNs token for the device.
type pushBody struct {
	DeviceToken  string `json:"deviceToken"`
	Environment  string `json:"environment"`
	GatewayGrant string `json:"gatewayGrant"`
}

// pushToken registers (PUT) or removes (DELETE) the device's APNs token. Removal
// works even without APNs configuration, so a phone can always clean up.
func (h *Handler) pushToken(w http.ResponseWriter, r *http.Request) {
	device, ok := h.authDevice(w, r, "push")
	if !ok {
		return
	}
	if r.Method == http.MethodDelete {
		if err := h.Store.RemovePush(r.Context(), device.ID); err != nil {
			reply(w, http.StatusInternalServerError, nil, "Unable to remove push registration")
			return
		}
		reply(w, http.StatusOK, map[string]bool{"enabled": false}, "")
		return
	}
	var body pushBody
	if !decodeBody(w, r, &body) {
		return
	}
	token, valid := wire.ValidPushToken(body.DeviceToken)
	if !valid || (body.Environment != "development" && body.Environment != "production") {
		reply(w, http.StatusBadRequest, nil, "Invalid push registration")
		return
	}
	if h.Push.Environment == "" || h.Push.Environment != body.Environment {
		reply(w, http.StatusServiceUnavailable, nil, "Push notifications are not configured for this environment")
		return
	}
	if h.Push.Gateway && body.GatewayGrant == "" {
		reply(w, http.StatusBadRequest, nil, "Push registration needs a gateway grant")
		return
	}
	if !h.Push.Gateway {
		body.GatewayGrant = ""
	}
	registeredAt, err := h.Store.RegisterPushWithGatewayGrant(r.Context(), device.ID, token, body.Environment, body.GatewayGrant, h.Now())
	switch {
	case err == nil:
	case errors.Is(err, push.ErrCapacity):
		reply(w, http.StatusConflict, nil, "Push device limit reached for this hub")
		return
	case errors.Is(err, store.ErrNotFound):
		refuse(w, r, http.StatusUnauthorized, "Device authentication required")
		return
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to register push notifications")
		return
	}
	reply(w, http.StatusOK, map[string]any{"enabled": true, "registeredAt": wire.FormatTime(registeredAt)}, "")
}
