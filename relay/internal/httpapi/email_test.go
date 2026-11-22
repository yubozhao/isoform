package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"isoform-relay/internal/email"
	"isoform-relay/internal/wire"
)

// emailRelay is a relay with the fake email provider wired as main.go wires
// the real one.
type emailRelay struct {
	*relay
	fake *email.Fake
}

func newEmailRelay(t *testing.T) *emailRelay {
	t.Helper()
	r := newRelay(t, "")
	vault, err := email.NewVault(bytes.Repeat([]byte{5}, email.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	fake := email.NewFake()
	svc := email.NewService(r.store, fake, vault, email.Config{Provider: "fake", DailyCap: email.DefaultDailyCap, HourlyCap: email.DefaultHourlyCap, CodesPerHour: email.DefaultCodesPerHour, CodesPerDay: email.DefaultCodesPerDay})
	svc.Now = r.now
	r.h.Email = svc
	return &emailRelay{relay: r, fake: fake}
}

func (r *emailRelay) hubCall(method, path, hubID, credential string, value any) response {
	r.t.Helper()
	var body []byte
	if value != nil {
		var err error
		if body, err = json.Marshal(value); err != nil {
			r.t.Fatal(err)
		}
	}
	return r.call(method, path, hubHeader(hubID, credential), body)
}

type emailStatusData struct {
	State     string `json:"state"`
	SentToday int    `json:"sentToday"`
	DailyCap  int    `json:"dailyCap"`
	Code      string `json:"code"`
}

func emailData(t *testing.T, resp response) emailStatusData {
	t.Helper()
	var data emailStatusData
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			t.Fatal(err)
		}
	}
	return data
}

var emailCode = regexp.MustCompile(`[0-9]{6}`)

func TestEmailRoutesNeedAProvider(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	r.connectHub(hubID, credential)
	resp := r.call(http.MethodGet, "/v1/hub/email/subscription", hubHeader(hubID, credential), nil)
	r.expectStatus(resp, http.StatusServiceUnavailable, "status without a provider")
}

func TestEmailHubRoutesAreAuthenticated(t *testing.T) {
	r := newEmailRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	r.connectHub(hubID, credential)
	for _, p := range []struct{ method, path string }{
		{http.MethodGet, "/v1/hub/email/subscription"},
		{http.MethodPost, "/v1/hub/email/subscription"},
		{http.MethodDelete, "/v1/hub/email/subscription"},
		{http.MethodPost, "/v1/hub/email/subscription/confirm"},
		{http.MethodPost, "/v1/hub/email/send"},
	} {
		resp := r.call(p.method, p.path, hubHeader(hubID, randomToken(t)), []byte(`{"address":"owner@example.com","code":"123456","subject":"Isoform: x","body":"y"}`))
		r.expectStatus(resp, http.StatusUnauthorized, "wrong credential "+p.method+" "+p.path)
	}
	if len(r.fake.Sent()) != 0 {
		t.Fatal("an unauthenticated request sent an email")
	}
}

func TestEmailVerifyAndSend(t *testing.T) {
	r := newEmailRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	r.connectHub(hubID, credential)

	resp := r.hubCall(http.MethodPost, "/v1/hub/email/send", hubID, credential, map[string]string{"subject": "Isoform: x", "body": "y"})
	r.expectStatus(resp, http.StatusConflict, "send before verifying")
	if data := emailData(t, resp); data.Code != "not_active" {
		t.Fatalf("code %q", data.Code)
	}
	resp = r.hubCall(http.MethodPost, "/v1/hub/email/subscription", hubID, credential, map[string]string{"address": "nope", "lang": "en"})
	r.expectStatus(resp, http.StatusBadRequest, "bad address")

	resp = r.hubCall(http.MethodPost, "/v1/hub/email/subscription", hubID, credential, map[string]string{"address": "owner@example.com", "lang": "en"})
	r.expectStatus(resp, http.StatusOK, "start")
	if data := emailData(t, resp); data.State != "pending_verification" {
		t.Fatalf("state after start %q", data.State)
	}
	code := emailCode.FindString(r.fake.Sent()[0].Subject)
	resp = r.hubCall(http.MethodPost, "/v1/hub/email/subscription/confirm", hubID, credential, map[string]string{"code": code})
	r.expectStatus(resp, http.StatusOK, "confirm")
	if data := emailData(t, resp); data.State != "active" {
		t.Fatalf("state after confirm %q", data.State)
	}

	// The body names a recipient field the route ignores: the relay mails the
	// verified address only.
	resp = r.hubCall(http.MethodPost, "/v1/hub/email/send", hubID, credential, map[string]string{"subject": "Isoform: PostHog morning check", "body": "Downloads 1,240", "to": "someone@else.example"})
	if resp.Status == http.StatusBadRequest {
		// decodeBody refuses unknown fields; send it again without one.
		resp = r.hubCall(http.MethodPost, "/v1/hub/email/send", hubID, credential, map[string]string{"subject": "Isoform: PostHog morning check", "body": "Downloads 1,240"})
	}
	r.expectStatus(resp, http.StatusOK, "send")
	sent := r.fake.Sent()
	if last := sent[len(sent)-1]; last.To != "owner@example.com" || last.Text != "Downloads 1,240" {
		t.Fatalf("sent %+v", last)
	}
	if data := emailData(t, resp); data.SentToday != 1 {
		t.Fatalf("count %d", data.SentToday)
	}

	r.h.Email.Config.DailyCap = 1
	expectLimitLog(t, "email_daily", "/v1/hub/email/send", 1, 1, func() int {
		return r.hubCall("POST", "/v1/hub/email/send", hubID, credential, map[string]string{"subject": "Isoform: test", "body": "test"}).Status
	})

	resp = r.hubCall(http.MethodDelete, "/v1/hub/email/subscription", hubID, credential, nil)
	r.expectStatus(resp, http.StatusOK, "stop")
	resp = r.hubCall(http.MethodPost, "/v1/hub/email/send", hubID, credential, map[string]string{"subject": "Isoform: x", "body": "y"})
	r.expectStatus(resp, http.StatusConflict, "send after stop")
}

func TestEmailCodesNeedAConnectedHub(t *testing.T) {
	r := newEmailRelay(t)
	hubID, credential := uuid.NewString(), randomToken(t)
	if err := r.store.AuthorizeHub(context.Background(), hubID, wire.Digest(credential), r.now()); err != nil {
		t.Fatal(err)
	}
	resp := r.hubCall(http.MethodPost, "/v1/hub/email/subscription", hubID, credential, map[string]string{"address": "someone@example.com", "lang": "en"})
	r.expectStatus(resp, http.StatusConflict, "code for a hub that is not connected")
	if data := emailData(t, resp); data.Code != "offline" {
		t.Fatalf("code %q", data.Code)
	}
	if len(r.fake.Sent()) != 0 {
		t.Fatal("a hub minted for one request had the relay mail someone")
	}
}

func TestNewHubsPerAddressAreLimited(t *testing.T) {
	r := newRelay(t, "")
	for i := 0; i < newHubsPerAddress; i++ {
		r.connectHub(uuid.NewString(), randomToken(t))
	}
	if _, resp, err := r.dialHub(uuid.NewString(), randomToken(t), nil); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("hub %d from one address: err=%v status=%d, want 429", newHubsPerAddress+1, err, status)
	}
}

func TestForwardedAddressOnlyFromATrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.RemoteAddr = "10.0.0.5:5000"
	if got := clientIP(req); got != "10.0.0.5" {
		t.Fatalf("a private peer that is no proxy named %q", got)
	}
	req.RemoteAddr = "127.0.0.1:5000"
	if got := clientIP(req); got != "203.0.113.9" {
		t.Fatalf("the local proxy's client = %q", got)
	}
}
