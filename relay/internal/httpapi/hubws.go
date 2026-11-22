package httpapi

import (
	"errors"
	"net/http"
	"time"

	"isoform-relay/internal/store"
	"isoform-relay/internal/wire"
)

// newHubsPerAddress bounds the hub ids one client address (an IPv4 address
// or an IPv6 /64) may create in an hour. Hub ids cost nothing, and each one
// carries its own code budgets on the text and email routes.
const (
	newHubsPerAddress = 20
	newHubsWindow     = time.Hour
)

// hubSocket authenticates the hub before the WebSocket upgrade. A hub id seen
// for the first time is created with the presented credential's digest.
func (h *Handler) hubSocket(w http.ResponseWriter, r *http.Request) {
	if h.authLocked(w, r) {
		return
	}
	hubID := r.Header.Get("X-Yansu-Hub")
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || !wire.ValidUUID(hubID) {
		h.authFailed(w, r, "Hub authentication required")
		return
	}
	ctx, cancel := storeContext(r.Context())
	defer cancel()
	if _, err := h.Store.HubLastSeen(ctx, hubID); errors.Is(err, store.ErrNotFound) {
		if !h.allow(w, r, "new_hubs", addressBlock(clientIP(r)), newHubsPerAddress, newHubsWindow, h.Now()) {
			reply(w, http.StatusTooManyRequests, nil, "Too many new hubs")
			return
		}
	}
	switch err := h.Store.AuthorizeHub(ctx, hubID, wire.Digest(token), h.Now()); {
	case err == nil:
	case errors.Is(err, store.ErrUnauthorized):
		h.authFailed(w, r, "Hub authentication required")
		return
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to authenticate hub")
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// The upgrader has already written the HTTP error response.
		return
	}
	h.Hubs.Serve(conn, hubID)
}
