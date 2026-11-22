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

// Monday 2026-11-16, 7:22 to 8:23 PM: the hub's direct-path link connected to
// its pinned relay instead of its own sidecar, and on each registration it
// re-sent its direct-path grant there. The grant replaced the bearer of the
// phone paired on that relay, which was refused until it paired again. A
// device paired on a relay keeps its bearer: a grant can never replace it.
func TestAGrantNeverReplacesTheBearerOfADevicePairedHere(t *testing.T) {
	r := newRelay(t, "development")
	hubID := uuid.NewString()
	credential := randomToken(t)
	pinned := r.connectHub(hubID, credential)
	phone := r.pairDevice(pinned, allScopes)
	r.expectStatus(r.getWith("/v1/presence", phone.bearer), http.StatusOK, "paired bearer")

	// The misrouted direct link: the same hub, a second connection, its grant.
	direct := r.connectHub(hubID, credential)
	directBearer := randomToken(t)
	grant := map[string]any{"t": "grant", "deviceId": phone.id, "bearerHash": wire.Digest(directBearer), "scopes": allScopes, "expiresAt": rfc3339(r.now().Add(30 * 24 * time.Hour))}
	for attempt := 0; attempt < 2; attempt++ { // re-sent at every registration
		direct.send(grant)
		if frame := direct.recvType("error"); frame["ref"] != "grant" {
			t.Fatalf("a grant over a paired device answered %v, want a grant error", frame)
		}
	}
	r.expectStatus(r.getWith("/v1/presence", phone.bearer), http.StatusOK, "the paired bearer after the wrong grant")
	r.expectStatus(r.getWith("/v1/presence", directBearer), http.StatusUnauthorized, "the direct-path bearer on the pinned relay")

	// The same bearer changes nothing and is not an error.
	direct.send(map[string]any{"t": "grant", "deviceId": phone.id, "bearerHash": wire.Digest(phone.bearer), "scopes": []string{"presence:read"}, "expiresAt": rfc3339(r.now().Add(24 * time.Hour))})
	direct.barrier()
	r.expectStatus(r.getWith("/v1/brief/latest", phone.bearer), http.StatusNotFound, "the paired scopes after a same-bearer grant")
}
