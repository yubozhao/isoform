package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const agentServerDefaultURL = "https://api.isoform.ai"
const agentServerTimeout = 5 * time.Second
const agentServerResponseLimit = 8 << 20

var errAgentServerOffline = errors.New("isoform server unavailable")

// agentServerError carries the server's stable refusal code without exposing
// request credentials or server-provided text to logs.
type agentServerError struct {
	Status         int
	Code           string
	Kind           string
	Current        json.RawMessage
	CurrentVersion *int64
}

func (e *agentServerError) Error() string {
	return fmt.Sprintf("isoform server: %s (%s, %d)", e.Kind, e.Code, e.Status)
}

type agentServerClient struct {
	base string
	http *http.Client
}

func newAgentServerClient(base string) (*agentServerClient, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = agentServerDefaultURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("invalid isoform server URL")
	}
	loopback := u.Hostname() == "localhost" || (net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("isoform server URL must use HTTPS")
	}
	return &agentServerClient{base: base, http: &http.Client{
		Timeout:       agentServerTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *agentServerClient) do(ctx context.Context, method, path string, payload any, hub, token string) (json.RawMessage, error) {
	body, err := c.request(ctx, method, path, payload, hub, token, agentServerResponseLimit)
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, errors.New("invalid isoform server response")
	}
	return json.RawMessage(body), nil
}

func (c *agentServerClient) request(ctx context.Context, method, path string, payload any, hub, token string, limit int64) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, errors.New("invalid isoform server request")
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if hub != "" {
		req.Header.Set("X-Isoform-Hub", hub)
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errAgentServerOffline
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errAgentServerOffline
	}
	if resp.StatusCode >= 500 {
		return nil, errAgentServerOffline
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error          string          `json:"error"`
			Current        json.RawMessage `json:"current"`
			CurrentVersion *int64          `json:"currentVersion"`
		}
		_ = json.Unmarshal(body, &failure)
		kind := "refused"
		switch failure.Error {
		case "not_approved":
			kind = "not_approved"
		case "paused":
			kind = "paused"
		}
		return nil, &agentServerError{Status: resp.StatusCode, Code: failure.Error, Kind: kind, Current: failure.Current, CurrentVersion: failure.CurrentVersion}
	}
	return body, nil
}
