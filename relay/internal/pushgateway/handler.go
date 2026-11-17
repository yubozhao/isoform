package pushgateway

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"isoform-relay/internal/apnspush"
	"isoform-relay/internal/httpapi"
	"isoform-relay/internal/limit"
	"isoform-relay/internal/push"
	"isoform-relay/internal/pushgatewayauth"
	"isoform-relay/internal/wire"
)

const maxRequestBytes = 8 * 1024
const replayLimit = 100000

type Sender interface {
	Send(context.Context, push.Claim) (push.Outcome, error)
	SendGrant(context.Context, string, string, string, string, string, time.Time) (push.Outcome, error)
}

type Handler struct {
	Environment string
	Sender      Sender
	privateKey  *ecdh.PrivateKey
	grantKey    []byte
	now         func() time.Time
	limits      *limit.Limiter
	replayMu    sync.Mutex
	replay      map[string]replayEntry
}

type replayEntry struct {
	accepted time.Time
	expires  time.Time
}

func New(environment string, sender Sender, privateKey *ecdh.PrivateKey) *Handler {
	h := &Handler{
		Environment: environment, Sender: sender, privateKey: privateKey,
		now: time.Now, limits: limit.New(), replay: make(map[string]replayEntry),
	}
	if privateKey != nil {
		h.grantKey, _ = pushgatewayauth.DeriveGrantKey(privateKey.Bytes())
	}
	return h
}

type notice struct {
	ID       string `json:"id"`
	CT       string `json:"ct"`
	Moment   string `json:"moment"`
	Category string `json:"category"`
	Level    string `json:"level"`
	Collapse string `json:"collapse"`
}

type request struct {
	Token          string `json:"token"`
	Environment    string `json:"environment"`
	Kind           string `json:"kind"`
	Hub            string `json:"hub"`
	Device         string `json:"device"`
	EventID        string `json:"eventId"`
	NotificationID string `json:"notificationId"`
	Notice         notice `json:"notice"`
	RegisteredAt   string `json:"registeredAt"`
	ExpiresAt      string `json:"expiresAt"`
	Grant          string `json:"grant"`
}

type registerRequest struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	HubID       string `json:"hubId"`
	HubPub      string `json:"hubPub"`
}

type response struct {
	Outcome push.Outcome `json:"outcome"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/health" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	case r.URL.Path == "/v1/key" && r.Method == http.MethodGet:
		if h.privateKey == nil {
			http.Error(w, "push unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			PublicKey string `json:"publicKey"`
		}{PublicKey: pushgatewayauth.PublicKeyString(h.privateKey)})
	case r.URL.Path == "/v1/push" && r.Method == http.MethodPost:
		h.push(w, r)
	case r.URL.Path == "/v1/register" && r.Method == http.MethodPost:
		h.register(w, r)
	case r.URL.Path == "/v1/health" || r.URL.Path == "/v1/key" ||
		r.URL.Path == "/v1/push" || r.URL.Path == "/v1/register":
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) push(w http.ResponseWriter, r *http.Request) {
	peer := httpapi.ClientIP(r)
	if allowed, retry := h.limits.Allow("ip:"+peer, 120, time.Minute, h.now()); !allowed {
		setRetryAfter(w, retry)
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	body, ok := readRequestBody(w, r)
	if !ok {
		return
	}
	hubPublic := r.Header.Get("Isoform-Hub-Key")
	timestamp := r.Header.Get("Isoform-Timestamp")
	signature := r.Header.Get("Isoform-Signature")
	hubKey, hubRaw, err := pushgatewayauth.DecodeHubPublicKey(hubPublic)
	if err != nil || h.privateKey == nil || h.grantKey == nil ||
		!pushgatewayauth.VerifyRequestSignatureForHub(h.privateKey, hubKey, hubRaw, timestamp, signature, body, h.now()) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.acceptSignature(signature, h.now()) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var input request
	if err := wire.DecodeStrict(strings.NewReader(string(body)), &input); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	token, ok := wire.ValidPushToken(input.Token)
	if !ok || !wire.ValidUUID(input.Hub) || !wire.ValidUUID(input.Device) ||
		!wire.ValidUUID(input.EventID) || !wire.ValidUUID(input.NotificationID) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if input.Environment != h.Environment {
		http.Error(w, "environment mismatch", http.StatusBadRequest)
		return
	}
	switch input.Kind {
	case push.KindBrief, push.KindNeedsYou:
		if input.Notice != (notice{}) {
			http.Error(w, "invalid notice", http.StatusBadRequest)
			return
		}
	case push.KindMoment:
	case push.KindWithdraw:
		if input.Notice.ID != "" || input.Notice.Moment != "" || !push.ValidWithdrawIDs(push.WithdrawIDs(input.Notice.CT)) {
			http.Error(w, "invalid notice", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	registered, err := wire.ParseTime(input.RegisteredAt)
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	expires, err := wire.ParseTime(input.ExpiresAt)
	if err != nil || !expires.After(h.now()) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	var pushNotice push.Notice
	if input.Kind == push.KindWithdraw {
		pushNotice = push.Notice{CT: input.Notice.CT}
	}
	if input.Kind == push.KindMoment {
		pushNotice = push.Notice{
			ID: input.Notice.ID, CT: input.Notice.CT, Moment: input.Notice.Moment,
			Category: input.Notice.Category, Level: input.Notice.Level, Collapse: input.Notice.Collapse,
		}
		if _, err := pushNotice.Prepare(); err != nil {
			http.Error(w, "invalid notice", http.StatusBadRequest)
			return
		}
	}
	if !pushgatewayauth.VerifyGrant(h.grantKey, input.Grant, input.Environment, token, input.Hub, hubPublic, h.now()) {
		http.Error(w, "push grant invalid", http.StatusForbidden)
		return
	}
	hubKeyID := hex.EncodeToString(hubRaw)
	if allowed, retry := h.limits.Allow("hub:"+hubKeyID, 600, time.Hour, h.now()); !allowed {
		setRetryAfter(w, retry)
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	digest := sha256.Sum256([]byte(token))
	tokenKey := hex.EncodeToString(digest[:])
	if allowed, retry := h.limits.Allow("token:"+tokenKey, 30, time.Hour, h.now()); !allowed {
		setRetryAfter(w, retry)
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	claim := push.Claim{
		HubID: input.Hub, DeviceID: input.Device, EventID: input.EventID,
		NotificationID: input.NotificationID, Kind: input.Kind, Notice: pushNotice,
		Token: token, Environment: input.Environment, RegisteredAt: registered, ExpiresAt: expires,
		GatewayGrant: input.Grant,
	}
	if h.Sender == nil || h.Environment == "" {
		http.Error(w, "push unavailable", http.StatusServiceUnavailable)
		return
	}
	outcome, sendErr := h.Sender.Send(r.Context(), claim)
	h.writeOutcome(w, outcome, sendErr)
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	peer := httpapi.ClientIP(r)
	if allowed, retry := h.limits.Allow("register-ip:"+peer, 10, time.Minute, h.now()); !allowed {
		setRetryAfter(w, retry)
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	body, ok := readRequestBody(w, r)
	if !ok {
		return
	}
	var input registerRequest
	if err := wire.DecodeStrict(strings.NewReader(string(body)), &input); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	token, valid := wire.ValidPushToken(input.Token)
	hubPublicKey, _, keyErr := pushgatewayauth.DecodeHubPublicKey(input.HubPub)
	if !valid || input.Environment != h.Environment || !wire.ValidUUID(input.HubID) || keyErr != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if h.privateKey == nil || h.grantKey == nil || h.Sender == nil || h.Environment == "" {
		http.Error(w, "push unavailable", http.StatusServiceUnavailable)
		return
	}
	shared, err := h.privateKey.ECDH(hubPublicKey)
	if err != nil || len(shared) != 32 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	tokenDigest := sha256.Sum256([]byte(token))
	tokenKey := hex.EncodeToString(tokenDigest[:])
	if allowed, retry := h.limits.Allow("register-token:"+tokenKey, 5, time.Hour, h.now()); !allowed {
		setRetryAfter(w, retry)
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	expires := h.now().Add(180 * 24 * time.Hour).UTC().Truncate(time.Second)
	grant, err := pushgatewayauth.NewGrant(h.grantKey, input.Environment, token, input.HubID, input.HubPub, expires)
	if err != nil {
		http.Error(w, "push unavailable", http.StatusServiceUnavailable)
		return
	}
	outcome, sendErr := h.Sender.SendGrant(r.Context(), token, input.Environment, input.HubID, input.HubPub, grant, expires)
	h.writeOutcome(w, outcome, sendErr)
}

func (h *Handler) writeOutcome(w http.ResponseWriter, outcome push.Outcome, sendErr error) {
	if apnspush.IsConfigurationError(sendErr) {
		http.Error(w, "provider configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	if delay, ok := apnspush.ProviderCooldown(sendErr); ok {
		setRetryAfter(w, delay)
		http.Error(w, "provider cooldown", http.StatusTooManyRequests)
		return
	}
	switch outcome {
	case push.Accepted, push.Retry, push.Permanent, push.TokenInvalid:
	default:
		outcome = push.Retry
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response{Outcome: outcome})
}

func readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err == nil {
		return body, true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
	} else {
		http.Error(w, "invalid request", http.StatusBadRequest)
	}
	return nil, false
}

func (h *Handler) acceptSignature(signature string, now time.Time) bool {
	h.replayMu.Lock()
	defer h.replayMu.Unlock()
	for key, entry := range h.replay {
		if !now.Before(entry.expires) {
			delete(h.replay, key)
		}
	}
	if _, exists := h.replay[signature]; exists {
		return false
	}
	if len(h.replay) >= replayLimit {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range h.replay {
			if oldestKey == "" || entry.accepted.Before(oldest) {
				oldestKey, oldest = key, entry.accepted
			}
		}
		if oldestKey != "" {
			delete(h.replay, oldestKey)
		}
	}
	h.replay[signature] = replayEntry{accepted: now, expires: now.Add(10 * time.Minute)}
	return true
}

func setRetryAfter(w http.ResponseWriter, duration time.Duration) {
	seconds := int(duration.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}
