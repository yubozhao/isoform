package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"time"

	"isoform-relay/internal/sms"
	"isoform-relay/internal/store"
	"isoform-relay/internal/wire"
)

// Text messages (PROTOCOL.md section 18): the hub manages its subscription and
// sends texts with its hub credential, and the SMS provider posts inbound
// texts to the webhook, which only its signature authenticates. Every route
// answers 503 (the webhook 404) when the relay has no provider configured.

const (
	// smsHubRequestsPerMinute bounds one hub's calls to the text routes.
	smsHubRequestsPerMinute = 30
	// smsVerificationsPerAddress bounds verification texts started from one
	// client address, because hub ids cost nothing to create.
	smsVerificationsPerAddress = 10
	smsVerificationWindow      = time.Hour
	// smsBadWebhooksPerAddress bounds webhook posts with a missing or wrong
	// signature from one address. Signed posts are never counted, so unsigned
	// ones from an address the provider shares (a proxy that does not set
	// X-Forwarded-For) cannot crowd out real texts.
	smsBadWebhooksPerAddress = 60
)

var smsCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

// addressBlock is the unit a per-address limit counts: the address itself
// for IPv4, and its /64 for IPv6, which one subscriber usually holds whole.
func addressBlock(address string) string {
	ip := net.ParseIP(address)
	if ip == nil || ip.To4() != nil {
		return address
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

type smsStartBody struct {
	Number string `json:"number"`
	Lang   string `json:"lang"`
}

type smsConfirmBody struct {
	Code string `json:"code"`
}

type smsSendBody struct {
	Body string `json:"body"`
}

// smsStatusView is the subscription as the hub reads it.
type smsStatusView struct {
	State         string  `json:"state"`
	CodeExpiresAt *string `json:"codeExpiresAt"`
	SentToday     int     `json:"sentToday"`
	SentThisMonth int     `json:"sentThisMonth"`
	DailyCap      int     `json:"dailyCap"`
	MonthlyCap    int     `json:"monthlyCap"`
}

func viewSMSStatus(status sms.Status) smsStatusView {
	view := smsStatusView{State: status.State, SentToday: status.SentToday, SentThisMonth: status.SentThisMonth, DailyCap: status.DailyCap, MonthlyCap: status.MonthlyCap}
	if !status.CodeExpiresAt.IsZero() {
		at := wire.FormatTime(status.CodeExpiresAt)
		view.CodeExpiresAt = &at
	}
	return view
}

// authHub checks the hub credential on an HTTP route without creating the
// hub, then applies the per-hub limit. It writes the error response itself.
func (h *Handler) authHub(w http.ResponseWriter, r *http.Request, limitKey string, perMinute int) (string, bool) {
	if h.authLocked(w, r) {
		return "", false
	}
	hubID := r.Header.Get("X-Yansu-Hub")
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || !wire.ValidUUID(hubID) {
		h.authFailed(w, r, "Hub authentication required")
		return "", false
	}
	ctx, cancel := storeContext(r.Context())
	defer cancel()
	switch err := h.Store.AuthorizeExistingHub(ctx, hubID, wire.Digest(token)); {
	case err == nil:
	case errors.Is(err, store.ErrUnauthorized):
		h.authFailed(w, r, "Hub authentication required")
		return "", false
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to authenticate hub")
		return "", false
	}
	if !h.allow(w, r, limitKey, hubID, perMinute, time.Minute, h.Now()) {
		reply(w, http.StatusTooManyRequests, nil, "Too many requests")
		return "", false
	}
	return hubID, true
}

// smsHub opens a text route: provider configured, hub authenticated.
func (h *Handler) smsHub(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.SMS == nil {
		reply(w, http.StatusServiceUnavailable, map[string]string{"code": "not_configured"}, "Text messages are not configured on this relay")
		return "", false
	}
	return h.authHub(w, r, "sms_hub", smsHubRequestsPerMinute)
}

// smsContext bounds a text route, which may wait on the provider.
func smsContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 20*time.Second)
}

// smsSubscription serves GET (status), POST (start verification) and DELETE
// on /v1/hub/sms/subscription.
func (h *Handler) smsSubscription(w http.ResponseWriter, r *http.Request) {
	hubID, ok := h.smsHub(w, r)
	if !ok {
		return
	}
	ctx, cancel := smsContext(r.Context())
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		status, err := h.SMS.Status(ctx, hubID)
		if err != nil {
			h.smsFailure(w, r, err)
			return
		}
		reply(w, http.StatusOK, viewSMSStatus(status), "")
	case http.MethodDelete:
		if err := h.SMS.Unsubscribe(ctx, hubID); err != nil {
			h.smsFailure(w, r, err)
			return
		}
		reply(w, http.StatusOK, viewSMSStatus(sms.Status{State: "none", DailyCap: h.SMS.Config.DailyCap, MonthlyCap: h.SMS.Config.MonthlyCap}), "")
	case http.MethodPost:
		var body smsStartBody
		if !decodeBody(w, r, &body) {
			return
		}
		switch body.Lang {
		case "", "en", "zh-CN":
		default:
			smsRefuse(w, http.StatusBadRequest, "invalid_request", "Unsupported language")
			return
		}
		if !h.allow(w, r, "sms_verify_address", addressBlock(clientIP(r)), smsVerificationsPerAddress, smsVerificationWindow, h.Now()) {
			smsRefuse(w, http.StatusTooManyRequests, "too_many", "Too many verification texts")
			return
		}
		status, err := h.SMS.StartVerification(ctx, hubID, body.Number, body.Lang)
		if err != nil {
			h.smsFailure(w, r, err)
			return
		}
		reply(w, http.StatusOK, viewSMSStatus(status), "")
	}
}

// smsEnable serves POST /v1/hub/sms/enable: the Mac turned text messages on.
// It takes no body and answers the status.
func (h *Handler) smsEnable(w http.ResponseWriter, r *http.Request) {
	hubID, ok := h.smsHub(w, r)
	if !ok {
		return
	}
	ctx, cancel := smsContext(r.Context())
	defer cancel()
	status, err := h.SMS.Enable(ctx, hubID)
	if err != nil {
		h.smsFailure(w, r, err)
		return
	}
	reply(w, http.StatusOK, viewSMSStatus(status), "")
}

// smsConfirm serves POST /v1/hub/sms/subscription/confirm.
func (h *Handler) smsConfirm(w http.ResponseWriter, r *http.Request) {
	hubID, ok := h.smsHub(w, r)
	if !ok {
		return
	}
	var body smsConfirmBody
	if !decodeBody(w, r, &body) {
		return
	}
	if !smsCodePattern.MatchString(body.Code) {
		smsRefuse(w, http.StatusBadRequest, "invalid_code", "The code is six digits")
		return
	}
	ctx, cancel := smsContext(r.Context())
	defer cancel()
	status, err := h.SMS.Confirm(ctx, hubID, body.Code)
	if err != nil {
		h.smsFailure(w, r, err)
		return
	}
	reply(w, http.StatusOK, viewSMSStatus(status), "")
}

// smsSend serves POST /v1/hub/sms/send. The body is passed to the provider
// and dropped; it is never stored or logged.
func (h *Handler) smsSend(w http.ResponseWriter, r *http.Request) {
	hubID, ok := h.smsHub(w, r)
	if !ok {
		return
	}
	var body smsSendBody
	if !decodeBody(w, r, &body) {
		return
	}
	ctx, cancel := smsContext(r.Context())
	defer cancel()
	status, err := h.SMS.Send(ctx, hubID, body.Body)
	if err != nil {
		h.smsFailure(w, r, err)
		return
	}
	reply(w, http.StatusOK, viewSMSStatus(status), "")
}

// smsInbound serves the provider webhook POST /v1/sms/inbound. A verified
// text is acted on once and answered with the provider's empty reply; the
// relay's own answers (STOP, HELP) go out through the send API. The signature
// is checked first (an HMAC over a body of at most 64 KiB); only failed
// signatures count against the sender's address.
func (h *Handler) smsInbound(w http.ResponseWriter, r *http.Request) {
	if h.SMS == nil {
		reply(w, http.StatusNotFound, nil, "Route not found")
		return
	}
	ctx, cancel := smsContext(r.Context())
	defer cancel()
	switch err := h.SMS.HandleInbound(ctx, r); {
	case err == nil:
	case errors.Is(err, sms.ErrBadSignature):
		if !h.allow(w, r, "sms_bad_webhooks", addressBlock(clientIP(r)), smsBadWebhooksPerAddress, time.Minute, h.Now()) {
			reply(w, http.StatusTooManyRequests, nil, "Too many requests")
			return
		}
		reply(w, http.StatusForbidden, nil, "Invalid webhook signature")
		return
	case errors.Is(err, sms.ErrBadWebhook):
		reply(w, http.StatusBadRequest, nil, "Invalid webhook")
		return
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to handle the text")
		return
	}
	contentType, ack := h.SMS.Provider.WebhookAck()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(ack)
}

// smsRefuse writes a refusal with a machine-readable code the hub maps to
// its own copy.
func smsRefuse(w http.ResponseWriter, status int, code, message string) {
	reply(w, status, map[string]string{"code": code}, message)
}

// smsFailure maps a service error to its status and code.
func (h *Handler) smsFailure(w http.ResponseWriter, r *http.Request, err error) {
	var retry *sms.RetryError
	if errors.As(err, &retry) {
		setRetryAfter(w, retry.Wait)
	}
	switch {
	case errors.Is(err, sms.ErrInvalidNumber):
		smsRefuse(w, http.StatusBadRequest, "invalid_number", err.Error())
	case errors.Is(err, sms.ErrCountry):
		smsRefuse(w, http.StatusBadRequest, "unsupported_country", err.Error())
	case errors.Is(err, sms.ErrInvalidBody):
		smsRefuse(w, http.StatusBadRequest, "invalid_body", err.Error())
	case errors.Is(err, sms.ErrOptedOut):
		smsRefuse(w, http.StatusConflict, "opted_out", err.Error())
	case errors.Is(err, sms.ErrNotActive):
		smsRefuse(w, http.StatusConflict, "not_active", err.Error())
	case errors.Is(err, sms.ErrNoPending):
		smsRefuse(w, http.StatusConflict, "no_pending", err.Error())
	case errors.Is(err, sms.ErrCodeWrong):
		smsRefuse(w, http.StatusForbidden, "code_wrong", err.Error())
	case errors.Is(err, sms.ErrCodeSpent):
		smsRefuse(w, http.StatusGone, "code_spent", err.Error())
	case errors.Is(err, sms.ErrQuota):
		h.logLimitError(w, r, err)
		smsRefuse(w, http.StatusTooManyRequests, "quota", err.Error())
	case errors.Is(err, sms.ErrTooMany):
		h.logLimitError(w, r, err)
		smsRefuse(w, http.StatusTooManyRequests, "too_many", err.Error())
	case errors.Is(err, sms.ErrBusy):
		smsRefuse(w, http.StatusServiceUnavailable, "busy", err.Error())
	case errors.Is(err, sms.ErrSendFailed):
		smsRefuse(w, http.StatusBadGateway, "send_failed", err.Error())
	case errors.Is(err, sms.ErrNotEntitled):
		smsRefuse(w, http.StatusForbidden, "not_entitled", err.Error())
	case errors.Is(err, sms.ErrUnknownHub):
		reply(w, http.StatusUnauthorized, nil, "Hub authentication required")
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to handle the text message request")
	}
}
