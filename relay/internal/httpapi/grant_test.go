package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"isoform-relay/internal/wire"
)

// A hub can register a phone it paired through another relay, replace that
// phone's bearer, and never touch another hub's device.
func TestGrantRegistersReplacesAndStaysWithinTheHub(t *testing.T) {
	r := newRelay(t, "development")
	hubID := uuid.NewString()
	hc := r.connectHub(hubID, randomToken(t))
	deviceID := uuid.NewString()
	first := randomToken(t)
	expires := rfc3339(r.now().Add(30 * 24 * time.Hour))
	hc.send(map[string]any{"t": "grant", "deviceId": deviceID, "bearerHash": wire.Digest(first), "scopes": allScopes, "expiresAt": expires})
	hc.barrier()
	r.expectStatus(r.getWith("/v1/presence", first), http.StatusOK, "granted bearer")

	second := randomToken(t)
	hc.send(map[string]any{"t": "grant", "deviceId": deviceID, "bearerHash": wire.Digest(second), "scopes": allScopes, "expiresAt": expires})
	hc.barrier()
	r.expectStatus(r.getWith("/v1/presence", second), http.StatusOK, "replacement bearer")
	r.expectStatus(r.getWith("/v1/presence", first), http.StatusUnauthorized, "replaced bearer")

	other := r.connectHub(uuid.NewString(), randomToken(t))
	stolen := randomToken(t)
	other.send(map[string]any{"t": "grant", "deviceId": deviceID, "bearerHash": wire.Digest(stolen), "scopes": allScopes, "expiresAt": expires})
	if frame := other.recvType("error"); frame["ref"] != "grant" {
		t.Fatalf("another hub's grant answered %v, want a grant error", frame)
	}
	r.expectStatus(r.getWith("/v1/presence", stolen), http.StatusUnauthorized, "another hub's grant")
	r.expectStatus(r.getWith("/v1/presence", second), http.StatusOK, "owner's bearer after another hub's attempt")

	hc.send(map[string]any{"t": "grant", "deviceId": deviceID, "bearerHash": wire.Digest(second), "scopes": allScopes, "expiresAt": rfc3339(r.now().Add(-time.Minute))})
	if frame := hc.recvType("error"); frame["ref"] != "grant" {
		t.Fatalf("an expired grant answered %v, want a grant error", frame)
	}
	hc.send(map[string]any{"t": "revoke", "deviceId": deviceID})
	hc.barrier()
	r.expectStatus(r.getWith("/v1/presence", second), http.StatusUnauthorized, "revoked granted device")
}
