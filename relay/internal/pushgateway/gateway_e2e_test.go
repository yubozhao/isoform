package pushgateway

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"isoform-relay/internal/apnspush"
	"isoform-relay/internal/config"
	"isoform-relay/internal/httpapi"
	"isoform-relay/internal/push"
	"isoform-relay/internal/pushgatewayauth"
)

type recordingSender struct {
	claim        push.Claim
	outcome      push.Outcome
	err          error
	grantCalls   int
	grantToken   string
	grantEnv     string
	grantHubID   string
	grantHubPub  string
	grant        string
	grantExpires time.Time
}

func (s *recordingSender) Send(_ context.Context, claim push.Claim) (push.Outcome, error) {
	s.claim = claim
	return s.outcome, s.err
}

func (s *recordingSender) SendGrant(_ context.Context, token, environment, hubID, hubPub, grant string, expires time.Time) (push.Outcome, error) {
	s.grantCalls++
	s.grantToken, s.grantEnv, s.grantHubID, s.grantHubPub = token, environment, hubID, hubPub
	s.grant, s.grantExpires = grant, expires
	return s.outcome, s.err
}

func TestGatewayKeyEndpointReturnsPublicKey(t *testing.T) {
	gatewayKey := testPrivateKey(t)
	server := httptest.NewServer(New("development", &recordingSender{}, gatewayKey))
	defer server.Close()
	response, err := http.Get(server.URL + "/v1/key")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		PublicKey string `json:"publicKey"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/key = %d, %v", response.StatusCode, err)
	}
	if result.PublicKey != pushgatewayauth.PublicKeyString(gatewayKey) {
		t.Fatalf("public key = %q", result.PublicKey)
	}
}

func TestSignedGatewayClientRoundTripsThroughHandler(t *testing.T) {
	gatewayKey := testPrivateKey(t)
	hubKey := testPrivateKey(t)
	hubPub := base64.RawURLEncoding.EncodeToString(hubKey.PublicKey().Bytes())
	macKey, _, err := pushgatewayauth.DeriveHubKeyFromPrivate(hubKey, gatewayKey.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	claim := validClaim()
	grantKey, err := pushgatewayauth.DeriveGrantKey(gatewayKey.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	claim.GatewayGrant, err = pushgatewayauth.NewGrant(grantKey, claim.Environment, claim.Token, claim.HubID, hubPub, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{outcome: push.Accepted}
	server := httptest.NewServer(New("development", sender, gatewayKey))
	defer server.Close()
	client := apnspush.NewGatewayClient(apnspush.GatewayConfig{
		URL: server.URL, Environment: "development", HubID: claim.HubID, HubPub: hubPub,
		HubKey: base64.RawURLEncoding.EncodeToString(macKey),
	})
	outcome, err := client.Send(context.Background(), claim)
	if err != nil || outcome != push.Accepted {
		t.Fatalf("Send = %q, %v", outcome, err)
	}
	if sender.claim.Token != claim.Token || sender.claim.HubID != claim.HubID ||
		sender.claim.GatewayGrant != claim.GatewayGrant || sender.claim.Environment != claim.Environment {
		t.Fatalf("handler received claim %+v", sender.claim)
	}
}

func TestPushRequiresSignedRequestAndValidGrant(t *testing.T) {
	gatewayKey := testPrivateKey(t)
	hubKey := testPrivateKey(t)
	hubPub := base64.RawURLEncoding.EncodeToString(hubKey.PublicKey().Bytes())
	sender := &recordingSender{outcome: push.Accepted}
	h := New("development", sender, gatewayKey)
	claim := validClaim()
	grantKey, err := pushgatewayauth.DeriveGrantKey(gatewayKey.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	claim.GatewayGrant, err = pushgatewayauth.NewGrant(grantKey, claim.Environment, claim.Token, claim.HubID, hubPub, time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(gatewayRequestForTest(claim))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		mutate     func(*http.Request, []byte) []byte
		wantStatus int
	}{
		{name: "unsigned", wantStatus: http.StatusUnauthorized},
		{name: "wrong signature", mutate: func(req *http.Request, body []byte) []byte {
			signGatewayRequest(t, req, hubKey, gatewayKey, body, time.Now(), "wrong")
			return body
		}, wantStatus: http.StatusUnauthorized},
		{name: "stale timestamp", mutate: func(req *http.Request, body []byte) []byte {
			signGatewayRequest(t, req, hubKey, gatewayKey, body, time.Now().Add(-10*time.Minute), "")
			return body
		}, wantStatus: http.StatusUnauthorized},
		{name: "body tampered", mutate: func(req *http.Request, body []byte) []byte {
			signGatewayRequest(t, req, hubKey, gatewayKey, body, time.Now(), "")
			return append(body, ' ')
		}, wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := append([]byte(nil), payload...)
			req := httptest.NewRequest(http.MethodPost, "/v1/push", strings.NewReader(string(body)))
			req.RemoteAddr = "127.0.0.1:4567"
			if test.mutate != nil {
				body = test.mutate(req, body)
			}
			if test.name == "unsigned" {
				req = httptest.NewRequest(http.MethodPost, "/v1/push", strings.NewReader(string(body)))
			} else {
				headers := req.Header.Clone()
				req = httptest.NewRequest(http.MethodPost, "/v1/push", strings.NewReader(string(body)))
				req.Header = headers
			}
			req.RemoteAddr = "127.0.0.1:4567"
			recorder := httptest.NewRecorder()
			h.ServeHTTP(recorder, req)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestPushGrantBindingReplayAndStrictDecode(t *testing.T) {
	gatewayKey := testPrivateKey(t)
	hubKey := testPrivateKey(t)
	hubPub := base64.RawURLEncoding.EncodeToString(hubKey.PublicKey().Bytes())
	sender := &recordingSender{outcome: push.Accepted}
	h := New("development", sender, gatewayKey)
	now := time.Now().UTC().Truncate(time.Second)
	h.now = func() time.Time { return now }
	claim := validClaim()
	claim.Environment = "development"
	grantKey, err := pushgatewayauth.DeriveGrantKey(gatewayKey.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	makeGrant := func(environment, token, hubID, public string, expiry time.Time) string {
		t.Helper()
		value, grantErr := pushgatewayauth.NewGrant(grantKey, environment, token, hubID, public, expiry)
		if grantErr != nil {
			t.Fatal(grantErr)
		}
		return value
	}
	cases := []struct {
		name       string
		grant      string
		token      string
		env        string
		public     string
		wantStatus int
	}{
		{"other hub key", makeGrant("development", claim.Token, claim.HubID, base64.RawURLEncoding.EncodeToString(testPrivateKey(t).PublicKey().Bytes()), now.Add(time.Hour)), claim.Token, "development", hubPub, http.StatusForbidden},
		{"other token", makeGrant("development", strings.Repeat("cd", 32), claim.HubID, hubPub, now.Add(time.Hour)), claim.Token, "development", hubPub, http.StatusForbidden},
		{"other environment", makeGrant("production", claim.Token, claim.HubID, hubPub, now.Add(time.Hour)), claim.Token, "development", hubPub, http.StatusForbidden},
		{"expired", makeGrant("development", claim.Token, claim.HubID, hubPub, now), claim.Token, "development", hubPub, http.StatusForbidden},
		{"malformed", "bad", claim.Token, "development", hubPub, http.StatusForbidden},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			payload, marshalErr := json.Marshal(map[string]any{
				"token": test.token, "environment": test.env, "kind": claim.Kind,
				"hub": claim.HubID, "device": claim.DeviceID, "eventId": claim.EventID,
				"notificationId": claim.NotificationID, "notice": map[string]string{},
				"registeredAt": claim.RegisteredAt.Format(time.RFC3339),
				"expiresAt":    claim.ExpiresAt.Format(time.RFC3339), "grant": test.grant,
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			response := signedPost(h, hubKey, gatewayKey, payload, now)
			if response.Code != test.wantStatus || sender.claim.Token != "" {
				t.Fatalf("response = %d %q, sender claim = %+v", response.Code, response.Body.String(), sender.claim)
			}
		})
	}

	validGrant := makeGrant("development", claim.Token, claim.HubID, hubPub, now.Add(time.Hour))
	claim.GatewayGrant = validGrant
	otherHubClaim := claim
	otherHubClaim.HubID = uuid.NewString()
	otherHubPayload, err := json.Marshal(gatewayRequestForTest(otherHubClaim))
	if err != nil {
		t.Fatal(err)
	}
	otherHubResponse := signedPost(h, hubKey, gatewayKey, otherHubPayload, now)
	if otherHubResponse.Code != http.StatusForbidden || sender.claim.Token != "" {
		t.Fatalf("grant for one hub used by another = %d %q, sender claim %+v",
			otherHubResponse.Code, otherHubResponse.Body.String(), sender.claim)
	}
	validPayload, err := json.Marshal(gatewayRequestForTest(claim))
	if err != nil {
		t.Fatal(err)
	}
	response := signedPost(h, hubKey, gatewayKey, validPayload, now)
	if response.Code != http.StatusOK || sender.claim.GatewayGrant != validGrant || sender.claim.Token != claim.Token {
		t.Fatalf("valid push = %d %q, claim %+v", response.Code, response.Body.String(), sender.claim)
	}
	replay := signedPost(h, hubKey, gatewayKey, validPayload, now)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed push = %d, want 401", replay.Code)
	}

	unknown := append(append([]byte(nil), validPayload[:len(validPayload)-1]...), []byte(`,"unexpected":true}`)...)
	strict := signedPost(h, hubKey, gatewayKey, unknown, now)
	if strict.Code != http.StatusBadRequest {
		t.Fatalf("strict decode = %d, want 400", strict.Code)
	}
}

func gatewayRequestForTest(claim push.Claim) map[string]any {
	return map[string]any{
		"token": claim.Token, "environment": claim.Environment, "kind": claim.Kind,
		"hub": claim.HubID, "device": claim.DeviceID, "eventId": claim.EventID,
		"notificationId": claim.NotificationID, "notice": map[string]string{},
		"registeredAt": claim.RegisteredAt.Format(time.RFC3339), "expiresAt": claim.ExpiresAt.Format(time.RFC3339),
		"grant": claim.GatewayGrant,
	}
}

func TestRegisterSendsGrantWithoutReturningIt(t *testing.T) {
	gatewayKey := testPrivateKey(t)
	hubKey := testPrivateKey(t)
	hubPub := base64.RawURLEncoding.EncodeToString(hubKey.PublicKey().Bytes())
	sender := &recordingSender{outcome: push.Accepted}
	h := New("development", sender, gatewayKey)
	h.now = func() time.Time { return time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC) }
	input := map[string]string{
		"token": strings.Repeat("ab", 32), "environment": "development",
		"hubId": uuid.NewString(), "hubPub": hubPub,
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/register", strings.NewReader(string(body)))
	request.RemoteAddr = "127.0.0.1:4567"
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || sender.grantCalls != 1 {
		t.Fatalf("register = %d %q, calls=%d", response.Code, response.Body.String(), sender.grantCalls)
	}
	if strings.Contains(response.Body.String(), sender.grant) || sender.grantToken != input["token"] ||
		sender.grantEnv != input["environment"] || sender.grantHubID != input["hubId"] || sender.grantHubPub != hubPub {
		t.Fatal("grant response leaked or sender arguments differ")
	}
	grantKey, err := pushgatewayauth.DeriveGrantKey(gatewayKey.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !pushgatewayauth.VerifyGrant(grantKey, sender.grant, input["environment"], input["token"], input["hubId"], hubPub, h.now()) ||
		!sender.grantExpires.Equal(h.now().Add(180*24*time.Hour).Truncate(time.Second)) {
		t.Fatal("sent grant does not bind the registration or expected expiry")
	}
	for i, test := range []struct {
		name  string
		hubID string
	}{
		{name: "missing"},
		{name: "invalid", hubID: "not-a-uuid"},
	} {
		t.Run(test.name+" hub ID", func(t *testing.T) {
			invalidInput := map[string]string{
				"token": input["token"], "environment": input["environment"], "hubPub": hubPub,
			}
			if test.hubID != "" {
				invalidInput["hubId"] = test.hubID
			}
			invalidBody, marshalErr := json.Marshal(invalidInput)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			invalidRequest := httptest.NewRequest(http.MethodPost, "/v1/register", strings.NewReader(string(invalidBody)))
			invalidRequest.RemoteAddr = "127.0.0.1:" + strconv.Itoa(4569+i)
			invalidResponse := httptest.NewRecorder()
			h.ServeHTTP(invalidResponse, invalidRequest)
			if invalidResponse.Code != http.StatusBadRequest || sender.grantCalls != 1 {
				t.Fatalf("invalid hub ID register = %d %q, calls=%d",
					invalidResponse.Code, invalidResponse.Body.String(), sender.grantCalls)
			}
		})
	}
	input["hubPub"] = "invalid"
	body, _ = json.Marshal(input)
	request = httptest.NewRequest(http.MethodPost, "/v1/register", strings.NewReader(string(body)))
	request.RemoteAddr = "127.0.0.1:4568"
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("bad hub key = %d, want 400", response.Code)
	}
}

func TestRegisterRateLimitsIPAndToken(t *testing.T) {
	gatewayKey := testPrivateKey(t)
	hubKey := testPrivateKey(t)
	hubPub := base64.RawURLEncoding.EncodeToString(hubKey.PublicKey().Bytes())
	h := New("development", &recordingSender{outcome: push.Accepted}, gatewayKey)
	body, _ := json.Marshal(map[string]string{
		"token": strings.Repeat("ef", 32), "environment": "development",
		"hubId": uuid.NewString(), "hubPub": hubPub,
	})
	for i := range 6 {
		request := httptest.NewRequest(http.MethodPost, "/v1/register", strings.NewReader(string(body)))
		request.RemoteAddr = "127.0.0.1:" + strconv.Itoa(5000+i)
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, request)
		want := http.StatusOK
		if i == 5 {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want {
			t.Fatalf("token registration %d status = %d, want %d", i+1, recorder.Code, want)
		}
	}
}

func TestRegisterRateLimitsUseTrustedProxyClientAddresses(t *testing.T) {
	for _, test := range []struct {
		name       string
		trusted    bool
		wantStatus int
	}{
		{name: "trusted clients have separate buckets", trusted: true, wantStatus: http.StatusOK},
		{name: "untrusted forwarded header is ignored", wantStatus: http.StatusTooManyRequests},
	} {
		t.Run(test.name, func(t *testing.T) {
			trusted, noTrusted, err := config.ParseTrustedProxies("192.0.2.10")
			if err != nil || noTrusted {
				t.Fatalf("parse proxy: %v %t %v", trusted, noTrusted, err)
			}
			if test.trusted {
				httpapi.SetTrustedProxies(trusted)
			} else {
				httpapi.DisableForwardedClientIP()
			}
			httpapi.SetClientIPHeader(config.DefaultClientIPHeader)
			t.Cleanup(func() {
				defaults, _, _ := config.ParseTrustedProxies("")
				httpapi.SetTrustedProxies(defaults)
				httpapi.SetClientIPHeader(config.DefaultClientIPHeader)
			})

			gatewayKey := testPrivateKey(t)
			hubKey := testPrivateKey(t)
			hubPub := base64.RawURLEncoding.EncodeToString(hubKey.PublicKey().Bytes())
			handler := New("development", &recordingSender{outcome: push.Accepted}, gatewayKey)
			register := func(client string, tokenIndex int) int {
				t.Helper()
				body, marshalErr := json.Marshal(map[string]string{
					"token": fmt.Sprintf("%064x", tokenIndex+1), "environment": "development",
					"hubId": uuid.NewString(), "hubPub": hubPub,
				})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				request := httptest.NewRequest(http.MethodPost, "/v1/register", strings.NewReader(string(body)))
				request.RemoteAddr = net.JoinHostPort("192.0.2.10", "8792")
				request.Header.Set("X-Forwarded-For", client)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response.Code
			}
			for i := range 10 {
				if status := register("203.0.113.1", i); status != http.StatusOK {
					t.Fatalf("registration %d status = %d, want 200", i+1, status)
				}
			}
			if status := register("203.0.113.2", 10); status != test.wantStatus {
				t.Fatalf("second client status = %d, want %d", status, test.wantStatus)
			}
		})
	}
}

func validClaim() push.Claim {
	now := time.Now().UTC().Truncate(time.Second)
	return push.Claim{
		HubID: uuid.NewString(), DeviceID: uuid.NewString(), EventID: uuid.NewString(),
		NotificationID: uuid.NewString(), Kind: push.KindBrief, Token: strings.Repeat("ab", 32),
		Environment: "development", RegisteredAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	}
}

func testPrivateKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func signedPost(h *Handler, hubKey, gatewayKey *ecdh.PrivateKey, body []byte, now time.Time) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/push", strings.NewReader(string(body)))
	req.RemoteAddr = "127.0.0.1:4567"
	signGatewayRequest(nil, req, hubKey, gatewayKey, body, now, "")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	return recorder
}

func signGatewayRequest(t *testing.T, request *http.Request, hubKey, gatewayKey *ecdh.PrivateKey, body []byte, now time.Time, override string) {
	if t != nil {
		t.Helper()
	}
	key, hubPub, err := pushgatewayauth.DeriveHubKeyFromPrivate(hubKey, gatewayKey.PublicKey().Bytes())
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(fmt.Errorf("derive test hub key: %w", err))
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	signature := pushgatewayauth.RequestSignature(key, timestamp, body)
	if override != "" {
		signature = override
	}
	request.Header.Set("Isoform-Hub-Key", base64.RawURLEncoding.EncodeToString(hubPub))
	request.Header.Set("Isoform-Timestamp", timestamp)
	request.Header.Set("Isoform-Signature", signature)
}

func TestGatewayLimitLogs(t *testing.T) {
	gatewayKey, hubKey := testPrivateKey(t), testPrivateKey(t)
	hubPub := pushgatewayauth.PublicKeyString(hubKey)
	claim := validClaim()
	key, err := pushgatewayauth.DeriveGrantKey(gatewayKey.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	claim.GatewayGrant, err = pushgatewayauth.NewGrant(key, claim.Environment, claim.Token, claim.HubID, hubPub, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	pushBody, err := json.Marshal(gatewayRequestForTest(claim))
	if err != nil {
		t.Fatal(err)
	}
	registerBody, err := json.Marshal(registerRequest{Token: claim.Token, Environment: claim.Environment, HubID: claim.HubID, HubPub: hubPub})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(claim.Token))
	tokenKey := hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name, path, subject string
		cap                 int
	}{
		{"push_address", "/v1/push", "127.0.0.1", 120},
		{"push_token", "/v1/push", tokenKey, 30},
		{"push_hub", "/v1/push", hex.EncodeToString(hubKey.PublicKey().Bytes()), 600},
		{"register_address", "/v1/register", "127.0.0.1", 10},
		{"register_token", "/v1/register", tokenKey, 5},
		{"provider_cooldown", "/v1/push", "", 0},
		{"provider_cooldown", "/v1/register", "", 0},
	} {
		t.Run(tc.name+tc.path, func(t *testing.T) {
			h := New("development", &recordingSender{err: &apnspush.ProviderCooldownError{}}, gatewayKey)
			for range tc.cap {
				h.limits.Allow(tc.name+":"+tc.subject, tc.cap, time.Hour, h.now())
			}
			body := pushBody
			if tc.path == "/v1/register" {
				body = registerBody
			}
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(string(body)))
			req.RemoteAddr = "127.0.0.1:1234"
			signGatewayRequest(t, req, hubKey, gatewayKey, body, h.now(), "")
			rec := httptest.NewRecorder()
			previous := log.Writer()
			var logs strings.Builder
			log.SetOutput(&logs)
			defer log.SetOutput(previous)
			h.ServeHTTP(rec, req)
			want := "status=429 limit=" + tc.name
			if tc.cap > 0 {
				want += fmt.Sprintf(" count=%d cap=%d", tc.cap, tc.cap)
			}
			want += " route=" + tc.path + "\n"
			if rec.Code != 429 || !strings.HasSuffix(logs.String(), want) || strings.Count(logs.String(), "\n") != 1 {
				t.Fatalf("status %d, log %q, want 429 with %q", rec.Code, logs.String(), want)
			}
		})
	}
}
