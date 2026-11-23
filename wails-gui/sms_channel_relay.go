package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"wails-gui/internal/destinations"
)

// smsResponseLimit bounds a relay answer on the text routes.
const smsResponseLimit = 64 << 10

// smsHTTPClient calls the relay's text routes. Like the attachment client it
// follows the WebSocket's proxy settings and never follows a redirect, so the
// hub credential goes to the configured relay only.
//
//isoform:destination text_relay
var smsHTTPClient = &http.Client{
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// smsCall makes one call to a text route with the hub credential (PROTOCOL.md
// 18.2) and decodes the status object. A refusal becomes an *smsError with
// the relay's code; an unreachable relay is smsErrOffline. Neither the number
// nor a text body is ever logged here.
//
//isoform:destination text_relay
func (c *mobileRelayClient) smsCall(ctx context.Context, method, path string, body any) (smsRelayStatus, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return smsRelayStatus{}, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, reader)
	if err != nil {
		return smsRelayStatus{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	req.Header.Set("X-Yansu-Hub", c.hubID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if path == "/v1/hub/sms/send" {
		req = destinations.CountRequest(req, destinations.TextRelay)
	}
	resp, err := smsHTTPClient.Do(req)
	if err != nil {
		return smsRelayStatus{}, &smsError{Code: smsErrOffline}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, smsResponseLimit))
	if err != nil {
		return smsRelayStatus{}, &smsError{Code: smsErrOffline}
	}
	var envelope struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return smsRelayStatus{}, &smsError{Code: smsErrUnknown}
	}
	if resp.StatusCode >= 400 || !envelope.OK {
		return smsRelayStatus{}, smsRefusal(resp, envelope.Data)
	}
	var status smsRelayStatus
	if err := json.Unmarshal(envelope.Data, &status); err != nil || status.State == "" {
		return smsRelayStatus{}, &smsError{Code: smsErrUnknown}
	}
	return status, nil
}

// smsRefusal reads the relay's refusal code and Retry-After. A refusal
// without a code is named by its status.
func smsRefusal(resp *http.Response, data json.RawMessage) *smsError {
	var refusal struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(data, &refusal)
	code := refusal.Code
	if code == "" {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			code = "unauthorized"
		case http.StatusNotFound:
			code = "not_configured"
		case http.StatusTooManyRequests:
			code = "too_many"
		default:
			code = smsErrUnknown
		}
	}
	var wait time.Duration
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		wait = time.Duration(seconds) * time.Second
	}
	return &smsError{Code: code, RetryAfter: wait}
}
