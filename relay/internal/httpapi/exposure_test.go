package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// forwardedAs is a header a trusted local proxy would send for one client.
func forwardedAs(address string) http.Header {
	return http.Header{"X-Forwarded-For": {address}}
}

func TestFailedAuthenticationsLockTheAddress(t *testing.T) {
	r := newRelay(t, "")
	dev := r.pairDevice(r.connectHub(uuid.NewString(), randomToken(t)), allScopes)
	scanner, bystander := "203.0.113.50", "203.0.113.51"
	withBearer := func(address, token string) http.Header {
		h := bearer(token)
		h.Set("X-Forwarded-For", address)
		return h
	}
	for i := 0; i < authFailuresPerAddress; i++ {
		r.expectStatus(r.call(http.MethodGet, "/v1/presence", withBearer(scanner, randomToken(t)), nil), http.StatusUnauthorized, "guess within the window")
	}
	var locked response
	expectLimitLog(t, "auth_failures", "/v1/presence", authFailuresPerAddress, authFailuresPerAddress, func() int {
		locked = r.call(http.MethodGet, "/v1/presence", withBearer(scanner, randomToken(t)), nil)
		return locked.Status
	})
	r.expectStatus(locked, http.StatusTooManyRequests, "guess past the limit")
	if locked.Header.Get("Retry-After") == "" {
		t.Fatal("authentication lock omitted Retry-After")
	}
	r.expectStatus(r.call(http.MethodGet, "/v1/presence", withBearer(scanner, dev.bearer), nil), http.StatusTooManyRequests, "the right credential from a locked address")
	r.expectStatus(r.call(http.MethodGet, "/v1/presence", withBearer(bystander, dev.bearer), nil), http.StatusOK, "another address is unaffected")
	r.expectStatus(r.call(http.MethodGet, "/v1/presence", withBearer(bystander, randomToken(t)), nil), http.StatusUnauthorized, "another address still gets a look-up")
	hubHeader := forwardedAs(scanner)
	if _, resp, err := r.dialHub(uuid.NewString(), randomToken(t), hubHeader); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("hub socket from a locked address: err=%v resp=%v, want 429", err, resp)
	}
	r.expectStatus(r.call(http.MethodPut, "/v1/hub/attachments/"+uuid.NewString(), withBearer(scanner, randomToken(t)), []byte("x")), http.StatusTooManyRequests, "hub route from a locked address")
	r.advance(authFailureWindow)
	r.expectStatus(r.call(http.MethodGet, "/v1/presence", withBearer(scanner, dev.bearer), nil), http.StatusOK, "after the window resets")
}

func TestHubCredentialGuessesCountAsFailures(t *testing.T) {
	r := newRelay(t, "")
	address := "203.0.113.60"
	hubID := uuid.NewString()
	r.connectHub(hubID, randomToken(t))
	for i := 0; i < authFailuresPerAddress; i++ {
		_, resp, err := r.dialHub(hubID, randomToken(t), forwardedAs(address))
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("guess %d: err=%v resp=%v, want 401", i, err, resp)
		}
	}
	if _, resp, err := r.dialHub(hubID, randomToken(t), forwardedAs(address)); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("guess past the limit: err=%v resp=%v, want 429", err, resp)
	}
}

func TestClientIPHeaderIsConfigurable(t *testing.T) {
	previous := clientIPHeader
	t.Cleanup(func() { SetClientIPHeader(previous) })
	SetClientIPHeader("Fly-Client-IP")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.1")
	if got := clientIP(req); got != "127.0.0.1" {
		t.Fatalf("without the configured header the proxy's client = %q, want the peer", got)
	}
	req.Header.Set("Fly-Client-IP", "203.0.113.9")
	if got := clientIP(req); got != "203.0.113.9" {
		t.Fatalf("Fly-Client-IP ignored: %q", got)
	}
	req.RemoteAddr = "198.51.100.1:5000"
	if got := clientIP(req); got != "198.51.100.1" {
		t.Fatalf("an untrusted peer's header was believed: %q", got)
	}
	SetClientIPHeader("")
	if clientIPHeader != "Fly-Client-IP" {
		t.Fatal("an empty name replaced the header")
	}
}

func TestNoTrustedProxiesIgnoresLoopbackForwardedAddress(t *testing.T) {
	previous := append([]*net.IPNet(nil), trustedProxies...)
	t.Cleanup(func() { trustedProxies = previous })
	DisableForwardedClientIP()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := clientIP(req); got != "127.0.0.1" {
		t.Fatalf("trusted forwarded address in no-proxy mode: got %q", got)
	}
}

func TestOpenRequestsPerAddressAreCapped(t *testing.T) {
	r := newRelay(t, "")
	hc := r.connectHub(uuid.NewString(), randomToken(t))
	devices := []device{r.pairDevice(hc, allScopes), r.pairDevice(hc, allScopes), r.pairDevice(hc, allScopes)}
	address := "198.51.100.7"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls sync.WaitGroup
	for i := 0; i < inflightPerAddress; i++ {
		polls.Add(1)
		go func(dev device) {
			defer polls.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.srv.URL+"/v1/responses?wait=20", nil)
			if err != nil {
				return
			}
			req.Header.Set("Authorization", "Bearer "+dev.bearer)
			req.Header.Set("X-Forwarded-For", address)
			resp, err := r.srv.Client().Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}(devices[i%len(devices)])
	}
	deadline := time.Now().Add(10 * time.Second)
	for r.h.openRequests(address) < inflightPerAddress {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d long-polls are open", r.h.openRequests(address), inflightPerAddress)
		}
		time.Sleep(10 * time.Millisecond)
	}
	header := bearer(devices[0].bearer)
	header.Set("X-Forwarded-For", address)
	var refused response
	expectLimitLog(t, "inflight_address", "/v1/presence", inflightPerAddress, inflightPerAddress, func() int {
		refused = r.call(http.MethodGet, "/v1/presence", header, nil)
		return refused.Status
	})
	r.expectStatus(refused, http.StatusTooManyRequests, "request past the open cap")
	if refused.Header.Get("Retry-After") == "" {
		t.Fatal("open request cap omitted Retry-After")
	}
	header.Set("X-Forwarded-For", "198.51.100.8")
	r.expectStatus(r.call(http.MethodGet, "/v1/presence", header, nil), http.StatusOK, "another address is unaffected")
	cancel()
	polls.Wait()
	deadline = time.Now().Add(10 * time.Second)
	for r.h.openRequests(address) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d requests still counted open after their clients left", r.h.openRequests(address))
		}
		time.Sleep(10 * time.Millisecond)
	}
	header.Set("X-Forwarded-For", address)
	r.expectStatus(r.call(http.MethodGet, "/v1/presence", header, nil), http.StatusOK, "after the polls close")
}
