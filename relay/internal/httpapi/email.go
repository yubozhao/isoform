package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"isoform-relay/internal/email"
	"isoform-relay/internal/wire"
)

// Email (PROTOCOL.md section 19): the hub verifies its own address and has
// results mailed there, with its hub credential. A send names no recipient;
// the relay mails the hub's verified address only. Every route answers 503
// when the relay has no email provider configured.

const (
	// emailHubRequestsPerMinute bounds one hub's calls to the email routes.
	emailHubRequestsPerMinute = 30
	// emailVerificationsPerAddress bounds verification emails started from
	// one client address, because hub ids cost nothing to create.
	emailVerificationsPerAddress = 10
	emailVerificationWindow      = time.Hour
)

type emailStartBody struct {
	Address string `json:"address"`
	Lang    string `json:"lang"`
}

type emailConfirmBody struct {
	Code string `json:"code"`
}

type emailSendBody struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// emailStatusView is the subscription as the hub reads it.
type emailStatusView struct {
	State         string  `json:"state"`
	CodeExpiresAt *string `json:"codeExpiresAt"`
	SentToday     int     `json:"sentToday"`
	DailyCap      int     `json:"dailyCap"`
}

func viewEmailStatus(status email.Status) emailStatusView {
	view := emailStatusView{State: status.State, SentToday: status.SentToday, DailyCap: status.DailyCap}
	if !status.CodeExpiresAt.IsZero() {
		at := wire.FormatTime(status.CodeExpiresAt)
		view.CodeExpiresAt = &at
	}
	return view
}

// emailHub opens an email route: provider configured, hub authenticated.
func (h *Handler) emailHub(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.Email == nil {
		reply(w, http.StatusServiceUnavailable, map[string]string{"code": "not_configured"}, "Email is not configured on this relay")
		return "", false
	}
	return h.authHub(w, r, "email_hub", emailHubRequestsPerMinute)
}

// emailContext bounds an email route, which may wait on the provider.
func emailContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 20*time.Second)
}

// emailSubscription serves GET (status), POST (start verification) and
// DELETE on /v1/hub/email/subscription.
func (h *Handler) emailSubscription(w http.ResponseWriter, r *http.Request) {
	hubID, ok := h.emailHub(w, r)
	if !ok {
		return
	}
	ctx, cancel := emailContext(r.Context())
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		status, err := h.Email.Status(ctx, hubID)
		if err != nil {
			h.emailFailure(w, r, err)
			return
		}
		reply(w, http.StatusOK, viewEmailStatus(status), "")
	case http.MethodDelete:
		if err := h.Email.Unsubscribe(ctx, hubID); err != nil {
			h.emailFailure(w, r, err)
			return
		}
		reply(w, http.StatusOK, viewEmailStatus(email.Status{State: "none", DailyCap: h.Email.Config.DailyCap}), "")
	case http.MethodPost:
		var body emailStartBody
		if !decodeBody(w, r, &body) {
			return
		}
		switch body.Lang {
		case "", "en", "zh-CN":
		default:
			smsRefuse(w, http.StatusBadRequest, "invalid_request", "Unsupported language")
			return
		}
		// A code goes out only for a hub that is connected right now, as a
		// Mac asking from its settings is: a hub id minted for one request
		// cannot have the relay mail a stranger.
		if h.Hubs == nil || !h.Hubs.Online(hubID) {
			smsRefuse(w, http.StatusConflict, "offline", "The hub is not connected")
			return
		}
		if !h.allow(w, r, "email_verify_address", addressBlock(clientIP(r)), emailVerificationsPerAddress, emailVerificationWindow, h.Now()) {
			smsRefuse(w, http.StatusTooManyRequests, "too_many", "Too many verification emails")
			return
		}
		status, err := h.Email.StartVerification(ctx, hubID, body.Address, body.Lang)
		if err != nil {
			h.emailFailure(w, r, err)
			return
		}
		reply(w, http.StatusOK, viewEmailStatus(status), "")
	}
}

// emailConfirm serves POST /v1/hub/email/subscription/confirm.
func (h *Handler) emailConfirm(w http.ResponseWriter, r *http.Request) {
	hubID, ok := h.emailHub(w, r)
	if !ok {
		return
	}
	var body emailConfirmBody
	if !decodeBody(w, r, &body) {
		return
	}
	if !smsCodePattern.MatchString(body.Code) {
		smsRefuse(w, http.StatusBadRequest, "invalid_code", "The code is six digits")
		return
	}
	ctx, cancel := emailContext(r.Context())
	defer cancel()
	status, err := h.Email.Confirm(ctx, hubID, body.Code)
	if err != nil {
		h.emailFailure(w, r, err)
		return
	}
	reply(w, http.StatusOK, viewEmailStatus(status), "")
}

// emailSend serves POST /v1/hub/email/send. The message goes to the
// provider and is dropped; it is never stored or logged.
func (h *Handler) emailSend(w http.ResponseWriter, r *http.Request) {
	hubID, ok := h.emailHub(w, r)
	if !ok {
		return
	}
	var body emailSendBody
	if !decodeBody(w, r, &body) {
		return
	}
	ctx, cancel := emailContext(r.Context())
	defer cancel()
	status, err := h.Email.Send(ctx, hubID, body.Subject, body.Body)
	if err != nil {
		h.emailFailure(w, r, err)
		return
	}
	reply(w, http.StatusOK, viewEmailStatus(status), "")
}

// emailFailure maps a service error to its status and code.
func (h *Handler) emailFailure(w http.ResponseWriter, r *http.Request, err error) {
	var retry *email.RetryError
	if errors.As(err, &retry) {
		setRetryAfter(w, retry.Wait)
	}
	switch {
	case errors.Is(err, email.ErrInvalidAddress):
		smsRefuse(w, http.StatusBadRequest, "invalid_address", err.Error())
	case errors.Is(err, email.ErrInvalidMessage):
		smsRefuse(w, http.StatusBadRequest, "invalid_body", err.Error())
	case errors.Is(err, email.ErrNotActive):
		smsRefuse(w, http.StatusConflict, "not_active", err.Error())
	case errors.Is(err, email.ErrNoPending):
		smsRefuse(w, http.StatusConflict, "no_pending", err.Error())
	case errors.Is(err, email.ErrCodeWrong):
		smsRefuse(w, http.StatusForbidden, "code_wrong", err.Error())
	case errors.Is(err, email.ErrCodeSpent):
		smsRefuse(w, http.StatusGone, "code_spent", err.Error())
	case errors.Is(err, email.ErrQuota):
		h.logLimitError(w, r, err)
		smsRefuse(w, http.StatusTooManyRequests, "quota", err.Error())
	case errors.Is(err, email.ErrTooMany):
		h.logLimitError(w, r, err)
		smsRefuse(w, http.StatusTooManyRequests, "too_many", err.Error())
	case errors.Is(err, email.ErrBusy):
		smsRefuse(w, http.StatusServiceUnavailable, "busy", err.Error())
	case errors.Is(err, email.ErrSendFailed):
		smsRefuse(w, http.StatusBadGateway, "send_failed", err.Error())
	case errors.Is(err, email.ErrUnknownHub):
		reply(w, http.StatusUnauthorized, nil, "Hub authentication required")
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to handle the email request")
	}
}
