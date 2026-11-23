package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"wails-gui/internal/destinations"
)

// yourAgentTimeout bounds every call to the agents server.
const yourAgentTimeout = 3 * time.Second

// errYourAgentOffline means the server could not be reached in time, or it
// answered with a server error. The page then shows the offline line.
var errYourAgentOffline = errors.New("the isoform agents server is unreachable")

// yourAgentAPIError is a documented error answer: {"error": code, "message"}.
// Suggestion is set for a 409 taken answer.
type yourAgentAPIError struct {
	Status     int
	Code       string
	Message    string
	Suggestion string
	// RetryAt is the RFC 3339 time a 429 names for the next try, when sent.
	RetryAt string
}

func (e *yourAgentAPIError) Error() string {
	return fmt.Sprintf("agents api %d %s: %s", e.Status, e.Code, e.Message)
}

// yourAgentClient talks to the contract in docs/agents-api.md. The base URL
// comes from ISOFORM_AGENTS_API for tests and staging.
type yourAgentClient struct {
	base string
	http *http.Client
}

//isoform:destination agent_of_you
func newYourAgentClient(override string) *yourAgentClient {
	base := strings.TrimRight(strings.TrimSpace(override), "/")
	if base == "" {
		base = yourAgentDefaultAPI
	}
	return &yourAgentClient{base: base, http: &http.Client{Timeout: yourAgentTimeout}}
}

type yourAgentCheckRequest struct {
	Name       string   `json:"name"`
	Candidates []string `json:"candidates"`
	Hub        string   `json:"hub,omitempty"`
	Token      string   `json:"token,omitempty"`
}

type yourAgentReserveRequest struct {
	Hub        string   `json:"hub"`
	Token      string   `json:"token"`
	Name       string   `json:"name"`
	Email      string   `json:"email"`
	Lang       string   `json:"lang"`
	Candidates []string `json:"candidates"`
	// Client is "mac" when the Mac sends the request; the server records it.
	Client string `json:"client"`
}

// yourAgentTokenRequest is the body of the calls that need only the hub and
// the token: release, resend and cancel.
type yourAgentTokenRequest struct {
	Hub   string `json:"hub"`
	Token string `json:"token"`
}

// yourAgentCodeRequest is the body of POST /verify.
type yourAgentCodeRequest struct {
	Hub   string `json:"hub"`
	Token string `json:"token"`
	Code  string `json:"code"`
}

// yourAgentChooseRequest is the body of POST /choose.
type yourAgentChooseRequest struct {
	Hub   string `json:"hub"`
	Token string `json:"token"`
	Keep  bool   `json:"keep"`
}

// check is POST /check.
func (c *yourAgentClient) check(ctx context.Context, request yourAgentCheckRequest) (YourAgentCheckResult, error) {
	status, body, err := c.do(ctx, http.MethodPost, "/check", request, "")
	if err != nil {
		return YourAgentCheckResult{}, err
	}
	if status != http.StatusOK {
		return YourAgentCheckResult{}, yourAgentDecodeError(status, body)
	}
	var answer YourAgentCheckResult
	if err := json.Unmarshal(body, &answer); err != nil {
		return YourAgentCheckResult{}, fmt.Errorf("agents api check: %w", err)
	}
	return answer, nil
}

// reserve is POST /reserve. A 200 returns the reservation; 409, 422, 403 and
// 429 come back as *yourAgentAPIError.
func (c *yourAgentClient) reserve(ctx context.Context, request yourAgentReserveRequest) (YourAgentReservation, error) {
	return c.reservationCall(ctx, "/reserve", request, "reserve")
}

// verify is POST /verify: the 6-digit code from the email. A 200 returns the
// verified reservation or a choice; 400, 410, 409, 429 and 403 are *yourAgentAPIError.
func (c *yourAgentClient) verify(ctx context.Context, hub, token, code string) (YourAgentReservation, error) {
	return c.reservationCall(ctx, "/verify", yourAgentCodeRequest{Hub: hub, Token: token, Code: code}, "verify")
}

// resend is POST /resend: a new code, and the old one stops working. A 200 returns
// the pending reservation; 404, 409 and 429 are *yourAgentAPIError.
func (c *yourAgentClient) resend(ctx context.Context, hub, token string) (YourAgentReservation, error) {
	return c.reservationCall(ctx, "/resend", yourAgentTokenRequest{Hub: hub, Token: token}, "resend")
}

// choose is POST /choose, after a choice answer. A 200 returns the verified
// reservation; 410 is *yourAgentAPIError.
func (c *yourAgentClient) choose(ctx context.Context, hub, token string, keep bool) (YourAgentReservation, error) {
	return c.reservationCall(ctx, "/choose", yourAgentChooseRequest{Hub: hub, Token: token, Keep: keep}, "choose")
}

// cancel is POST /cancel. The answer holds what is left: nil when the hold went,
// or the verified original when the email had one.
func (c *yourAgentClient) cancel(ctx context.Context, hub, token string) (*YourAgentReservation, error) {
	status, body, err := c.do(ctx, http.MethodPost, "/cancel", yourAgentTokenRequest{Hub: hub, Token: token}, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, yourAgentDecodeError(status, body)
	}
	var answer struct {
		Reservation *YourAgentReservation `json:"reservation"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("agents api cancel: %w", err)
	}
	return normalizeYourAgentReservation(answer.Reservation), nil
}

// reservationCall posts payload to path and reads back a reservation. A 200
// returns it; any other status comes back as *yourAgentAPIError.
func (c *yourAgentClient) reservationCall(ctx context.Context, path string, payload any, step string) (YourAgentReservation, error) {
	status, body, err := c.do(ctx, http.MethodPost, path, payload, "")
	if err != nil {
		return YourAgentReservation{}, err
	}
	if status != http.StatusOK {
		return YourAgentReservation{}, yourAgentDecodeError(status, body)
	}
	var reservation YourAgentReservation
	if err := json.Unmarshal(body, &reservation); err != nil {
		return YourAgentReservation{}, fmt.Errorf("agents api %s: %w", step, err)
	}
	normalizeYourAgentReservation(&reservation)
	return reservation, nil
}

// reservation is GET /reservation. A 404 means this hub holds no name, so it
// returns nil with no error.
func (c *yourAgentClient) reservation(ctx context.Context, hub, token string) (*YourAgentReservation, error) {
	path := "/reservation?hub=" + url.QueryEscape(hub)
	status, body, err := c.do(ctx, http.MethodGet, path, nil, token)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, yourAgentDecodeError(status, body)
	}
	var reservation YourAgentReservation
	if err := json.Unmarshal(body, &reservation); err != nil {
		return nil, fmt.Errorf("agents api reservation: %w", err)
	}
	return normalizeYourAgentReservation(&reservation), nil
}

// release is POST /release.
func (c *yourAgentClient) release(ctx context.Context, hub, token string) error {
	status, body, err := c.do(ctx, http.MethodPost, "/release", yourAgentTokenRequest{Hub: hub, Token: token}, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return yourAgentDecodeError(status, body)
	}
	return nil
}

// do sends one request with the 3 second timeout. Network failures and 5xx
// answers are errYourAgentOffline; every other status is returned to the caller.
//
//isoform:destination agent_of_you
func (c *yourAgentClient) do(ctx context.Context, method, path string, payload any, bearer string) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, yourAgentTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req = destinations.CountRequest(req, destinations.AgentOfYou)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errYourAgentOffline, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errYourAgentOffline, err)
	}
	if resp.StatusCode >= 500 {
		return resp.StatusCode, body, errYourAgentOffline
	}
	return resp.StatusCode, body, nil
}

// yourAgentDecodeError turns a documented error body into *yourAgentAPIError.
// A body that is not the documented shape still reports its status.
func yourAgentDecodeError(status int, body []byte) error {
	var envelope struct {
		Error      string `json:"error"`
		Message    string `json:"message"`
		Suggestion string `json:"suggestion"`
		RetryAt    string `json:"retryAt"`
	}
	_ = json.Unmarshal(body, &envelope)
	return &yourAgentAPIError{
		Status:     status,
		Code:       envelope.Error,
		Message:    envelope.Message,
		Suggestion: envelope.Suggestion,
		RetryAt:    envelope.RetryAt,
	}
}
