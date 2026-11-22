package httpapi

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"isoform-relay/internal/sms"
	"isoform-relay/internal/store"
)

// Capture the actual response log, including any accidental second status line.
func expectLimitLog(t *testing.T, name, route string, count, cap int, request func() int) {
	t.Helper()
	logs := &syncBuffer{}
	before := log.Writer()
	log.SetOutput(logs)
	defer log.SetOutput(before)
	if status := request(); status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", status)
	}
	want := fmt.Sprintf("status=429 limit=%s count=%d cap=%d route=%s", name, count, cap, route)
	matches := 0
	for _, line := range strings.Split(string(logs.Bytes()), "\n") {
		if strings.Contains(line, "status=429") || strings.Contains(line, "(429)") {
			if !strings.HasSuffix(line, want) {
				t.Fatalf("unexpected 429 log: %s", line)
			}
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("got %d matching lines, want one: %s", matches, logs.Bytes())
	}
}

func TestRateLimitLogs(t *testing.T) {
	r := newSMSRelay(t)
	emailRelay := newEmailRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.enabledHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"presence:read", uploadScope})
	emailRelay.connectHub(hubID, credential)
	hubAuth := hubHeader(hubID, credential)
	id := uuid.NewString()
	const ip = "192.0.2.1"
	const pieces = "/v1/uploads/{id}/pieces"
	piecePath := "/v1/uploads/" + id + "/pieces?total=128&offset=0"
	pieceAuth := bearer(dev.bearer)
	pieceAuth.Set("Content-Type", octetStream)
	for _, tc := range []struct {
		name, subject       string
		cap                 int
		method, path, route string
		header              http.Header
		body                string
		h                   *Handler
	}{
		{"device_rate", dev.id, deviceRequestsPerMinute, "GET", "/v1/presence", "/v1/presence", bearer(dev.bearer), "", r.h},
		{"upload_rate", dev.id, uploadRequestsPerMinute, "PUT", piecePath, pieces, pieceAuth, "x", r.h},
		{"upload_pieces", dev.id, uploadPiecesPerDevice, "PUT", piecePath, pieces, pieceAuth, "x", r.h},
		{"uploads", dev.id, uploadsPerDevice, "PUT", piecePath, pieces, pieceAuth, "x", r.h},
		{"attachment_hub", hubID, attachmentUploadsPerHub, "PUT", "/v1/hub/attachments/" + id, "/v1/hub/attachments/{id}", hubAuth, "", r.h},
		{"attachment_address", ip, attachmentUploadsPerAddress, "PUT", "/v1/hub/attachments/" + id, "/v1/hub/attachments/{id}", hubAuth, "", r.h},
		{"claim_pairing", id, claimAttempts, "POST", "/v1/pairings/" + id + "/claim", "/v1/pairings/{id}/claim", nil, "", r.h},
		{"claim_address", ip, claimAttempts, "POST", "/v1/pairings/" + id + "/claim", "/v1/pairings/{id}/claim", nil, "", r.h},
		{"new_hubs", ip, newHubsPerAddress, "GET", "/v1/hub/ws", "/v1/hub/ws", hubHeader(uuid.NewString(), credential), "", r.h},
		{"sms_hub", hubID, smsHubRequestsPerMinute, "GET", "/v1/hub/sms/subscription", "/v1/hub/sms/subscription", hubAuth, "", r.h},
		{"sms_verify_address", ip, smsVerificationsPerAddress, "POST", "/v1/hub/sms/subscription", "/v1/hub/sms/subscription", hubAuth, `{"number":"+14155550123"}`, r.h},
		{"sms_bad_webhooks", ip, smsBadWebhooksPerAddress, "POST", "/v1/sms/inbound", "/v1/sms/inbound", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, "Body=test", r.h},
		{"email_hub", hubID, emailHubRequestsPerMinute, "GET", "/v1/hub/email/subscription", "/v1/hub/email/subscription", hubAuth, "", emailRelay.h},
		{"email_verify_address", ip, emailVerificationsPerAddress, "POST", "/v1/hub/email/subscription", "/v1/hub/email/subscription", hubAuth, `{"address":"test@example.com"}`, emailRelay.h},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Move beyond the preceding case's seeded window.
			r.advance(2 * time.Hour)
			emailRelay.advance(2 * time.Hour)
			for range tc.cap {
				tc.h.limits.Allow(tc.name+":"+tc.subject, tc.cap, time.Hour, tc.h.Now())
			}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.RemoteAddr = ip + ":443"
			for k, v := range tc.header {
				req.Header[k] = v
			}
			if req.Header.Get("Content-Type") == "" {
				req.Header.Set("Content-Type", "application/json")
			}
			expectLimitLog(t, tc.name, tc.route, tc.cap, tc.cap, func() int {
				w := httptest.NewRecorder()
				tc.h.ServeHTTP(w, req)
				return w.Code
			})
		})
	}
}

func TestWaitingUploadsAndQueueLog(t *testing.T) {
	r := newRelay(t, "")
	hub := r.connectHub(uuid.NewString(), randomToken(t))
	dev := r.pairDevice(hub, []string{"presence:read", uploadScope})
	for range store.KeptUploads {
		r.expectStatus(r.putUpload(dev.bearer, uuid.NewString(), []byte("x")), 201, "fill waiting uploads")
	}
	expectLimitLog(t, "waiting_uploads", "/v1/uploads/{id}/pieces", store.KeptUploads, store.KeptUploads, func() int {
		resp, _ := r.putPiece(dev.bearer, uuid.NewString(), 0, 2, []byte("x"))
		return resp.Status
	})
	hub.conn.Close()
	r.waitHubOnline(dev, false)
	send := func(seq int) int {
		return r.postWith("/v1/requests", dev.bearer, requestEnvelope(t, dev, uint64(seq))).Status
	}
	for i := range store.MaxQueued {
		if status := send(i + 1); status != 202 {
			t.Fatalf("queue fill: %d", status)
		}
	}
	expectLimitLog(t, "request_queue", "/v1/requests", store.MaxQueued, store.MaxQueued, func() int { return send(store.MaxQueued + 1) })
}

func seedCodeWindow(t *testing.T, r *relay, key string, cap int) {
	t.Helper()
	for range cap {
		ok, _, err := r.store.TakeSMSLimit(context.Background(), key, cap, time.Hour, r.now())
		if err != nil || !ok {
			t.Fatalf("seed verification limit: %v", err)
		}
	}
}

func TestSMSVerificationLimitLogs(t *testing.T) {
	for _, name := range []string{"sms_codes_hub", "sms_codes_number", "sms_wrong_codes", "sms_confirms"} {
		t.Run(name, func(t *testing.T) {
			r := newSMSRelay(t)
			hub, cred := uuid.NewString(), randomToken(t)
			r.enabledHub(hub, cred)
			path := "/v1/hub/sms/subscription"
			body := map[string]string{"number": smsNumber}
			cap := 3
			switch name {
			case "sms_codes_hub":
				seedCodeWindow(t, r.relay, "code:hub:"+hub, cap)
			case "sms_codes_number":
				cap = 5
				seedCodeWindow(t, r.relay, "code:number:"+r.texts.Vault.HashNumber(smsNumber), cap)
			case "sms_wrong_codes":
				r.expectStatus(r.hubCall("POST", path, hub, cred, body), 200, "start verification")
				cap = sms.WrongCodesPerNumber
				seedCodeWindow(t, r.relay, store.WrongCodeKey(r.texts.Vault.HashNumber(smsNumber)), cap)
				path += "/confirm"
				body = map[string]string{"code": "000000"}
			case "sms_confirms":
				cap = 10
				path += "/confirm"
				body = map[string]string{"code": "000000"}
				for range cap {
					r.expectStatus(r.hubCall("POST", path, hub, cred, body), 409, "confirm without pending code")
				}
			}
			expectLimitLog(t, name, path, cap, cap, func() int { return r.hubCall("POST", path, hub, cred, body).Status })
		})
	}
}

func TestEmailVerificationLimitLogs(t *testing.T) {
	for _, name := range []string{"email_codes_hub", "email_codes_address"} {
		t.Run(name, func(t *testing.T) {
			r := newEmailRelay(t)
			hub, cred := uuid.NewString(), randomToken(t)
			r.connectHub(hub, cred)
			const path = "/v1/hub/email/subscription"
			const addr = "test@example.com"
			cap, key := 3, "email:codes:hub:"+hub
			if name == "email_codes_address" {
				cap, key = 5, "email:codes:address:"+r.h.Email.Vault.HashAddress(addr)
			}
			seedCodeWindow(t, r.relay, key, cap)
			expectLimitLog(t, name, path, cap, cap, func() int { return r.hubCall("POST", path, hub, cred, map[string]string{"address": addr}).Status })
		})
	}
}

func TestInflightLogRedactsUnmatchedRoute(t *testing.T) {
	r := newRelay(t, "")
	req := httptest.NewRequest(http.MethodGet, "/v1/private-path?token=query-secret", nil)
	req.Header.Set("Authorization", "Bearer header-secret")
	for range inflightPerAddress {
		r.h.acquire(clientIP(req))
	}
	expectLimitLog(t, "inflight_address", "unknown", inflightPerAddress, inflightPerAddress, func() int {
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		return w.Code
	})
}
