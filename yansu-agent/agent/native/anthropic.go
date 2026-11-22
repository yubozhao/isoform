package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultAnthropicBase     = "https://api.anthropic.com"
	defaultAnthropicVersion  = "2023-06-01"
	defaultAnthropicMaxTries = 3
)

// LLMClient is the model-transport abstraction the agent loop depends
// on. It speaks the Anthropic-shaped Request/StreamEvent vocabulary so
// the loop stays provider-agnostic. *Client (Anthropic Messages, native)
// and *OpenAIClient (OpenAI Chat Completions, translated on the fly)
// both satisfy it, which is what lets BYOA swap the upstream wire
// protocol without touching loop.go.
type LLMClient interface {
	Stream(ctx context.Context, req *Request, handler StreamHandler) error
}

type beforeDispatchContextKey struct{}

// DispatchDeniedError is a terminal read-authority refusal, never a transient
// network failure. Wrapping retains errors.Is without exposing request text.
type DispatchDeniedError struct{ Cause error }

func (e *DispatchDeniedError) Error() string { return e.Cause.Error() }
func (e *DispatchDeniedError) Unwrap() error { return e.Cause }

// ContextWithBeforeDispatch carries the caller's live read gate through native
// turns and compaction. The HTTP transports invoke it again for every retry.
func ContextWithBeforeDispatch(ctx context.Context, check func(context.Context) error) context.Context {
	if check == nil {
		return ctx
	}
	return context.WithValue(ctx, beforeDispatchContextKey{}, check)
}

func CheckBeforeDispatch(ctx context.Context) error {
	if check, ok := ctx.Value(beforeDispatchContextKey{}).(func(context.Context) error); ok {
		if err := check(ctx); err != nil {
			return &DispatchDeniedError{Cause: err}
		}
	}
	return nil
}

// Client is a thin Anthropic Messages API client. It wraps a vanilla
// http.Client with Anthropic-specific headers and SSE parsing; we
// deliberately do not depend on the official anthropic-sdk-go so our
// own types stay stable.
type Client struct {
	http       *http.Client
	baseURL    string
	apiKey     string
	authToken  string // optional: used when sub2api injects a bearer
	version    string
	betaHeader string
	// userAgent identifies the calling app.
	userAgent string
}

// ClientOption tweaks the Client at construction time.
type ClientOption func(*Client)

// WithHTTPClient overrides the HTTP client (useful in tests).
func WithHTTPClient(c *http.Client) ClientOption {
	return func(cl *Client) { cl.http = c }
}

// WithBaseURL sets the base URL (e.g. sub2api's gateway).
func WithBaseURL(u string) ClientOption {
	return func(cl *Client) {
		cl.baseURL = strings.TrimRight(u, "/")
	}
}

// WithAPIKey sets the x-api-key header.
func WithAPIKey(k string) ClientOption {
	return func(cl *Client) { cl.apiKey = k }
}

// WithAuthToken sets the Authorization: Bearer <token> header. Used by
// sub2api where the gateway accepts OAuth-style tokens.
func WithAuthToken(t string) ClientOption {
	return func(cl *Client) { cl.authToken = t }
}

// WithBeta sets the anthropic-beta header (comma-separated).
func WithBeta(b string) ClientOption {
	return func(cl *Client) { cl.betaHeader = b }
}

// WithUserAgent overrides the User-Agent.
func WithUserAgent(u string) ClientOption {
	return func(cl *Client) { cl.userAgent = u }
}

// NewClient constructs a Client with sensible defaults. Options
// override them.
//
// The HTTP client deliberately leaves the overall timeout at 0 so that long
// SSE responses can stream for minutes without being cut off. We still need
// the connect / TLS phases to fail fast though, otherwise an offline machine
// sits on macOS's ~75s TCP-level timeout before the user gets any error —
// for that whole window the UI just spins.
//
// The transport below caps dial + TLS at 10s each. We deliberately do NOT
// set ResponseHeaderTimeout: once TCP+TLS succeed we are talking to the
// proxy, and any further wait is server-side processing latency (model
// warmup, queue depth, upstream Anthropic load) — not a connectivity
// problem. A 10s cap there was killing real requests whose proxy TTFB
// spiked past 10s, surfacing as spurious "Connection problem" cards.
//
//isoform:destination assistant
func NewClient(opts ...ClientOption) *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	c := &Client{
		http:      &http.Client{Timeout: 0, Transport: transport},
		baseURL:   defaultAnthropicBase,
		version:   defaultAnthropicVersion,
		userAgent: "yansu-native-agent/1.0",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Request describes a single POST /v1/messages call. Fields mirror the
// Anthropic Messages API. Optional ones are omitempty.
type Request struct {
	Model        string          `json:"model"`
	Messages     []Message       `json:"messages"`
	System       any             `json:"system,omitempty"`
	Tools        []ToolDef       `json:"tools,omitempty"`
	ToolChoice   *ToolChoice     `json:"tool_choice,omitempty"`
	MaxTokens    int             `json:"max_tokens"`
	Temperature  *float64        `json:"temperature,omitempty"`
	Thinking     *ThinkingConfig `json:"thinking,omitempty"`
	OutputConfig *OutputConfig   `json:"output_config,omitempty"`
	Container    string          `json:"container,omitempty"`
	Stream       bool            `json:"stream"`
	Metadata     map[string]any  `json:"metadata,omitempty"`
}

// OutputConfig carries the requested reasoning effort to compatible gateways.
type OutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// Message is a single turn in the conversation. Role is "user" or
// "assistant". Content is either a string or a list of ContentBlock.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// ContentBlock is one block of a multi-block message (text, tool_use,
// tool_result, thinking, image, ...).
type ContentBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	Source       *ImageSource  `json:"source,omitempty"`
	ID           string        `json:"id,omitempty"`
	Name         string        `json:"name,omitempty"`
	Input        any           `json:"input,omitempty"`
	ToolUseID    string        `json:"tool_use_id,omitempty"`
	Content      any           `json:"content,omitempty"`
	IsError      bool          `json:"is_error,omitempty"`
	Caller       *ToolCaller   `json:"caller,omitempty"`
	Thinking     string        `json:"thinking,omitempty"`
	Signature    string        `json:"signature,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// ToolCaller identifies whether a tool_use came from normal direct model
// tool use or from the code execution container.
type ToolCaller struct {
	Type   string `json:"type,omitempty"`
	ToolID string `json:"tool_id,omitempty"`
}

// ImageSource is the base64 image source used in image content blocks.
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// CacheControl enables prompt caching per block.
type CacheControl struct {
	Type string `json:"type"`
}

// ToolDef describes one tool in the request.
type ToolDef struct {
	Type           string         `json:"type,omitempty"`
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	InputSchema    map[string]any `json:"input_schema,omitempty"`
	AllowedCallers []string       `json:"allowed_callers,omitempty"`
	CacheControl   *CacheControl  `json:"cache_control,omitempty"`
}

// ToolChoice constrains how the model uses tools.
type ToolChoice struct {
	Type string `json:"type"`           // "auto" | "any" | "tool"
	Name string `json:"name,omitempty"` // required when Type == "tool"
}

// ThinkingConfig enables extended thinking. Claude 4.7+ models
// (Fable/Mythos 5, Opus 4.7/4.8, Sonnet 5) accept only "adaptive" and
// no budget; older models use "enabled" + BudgetTokens.
type ThinkingConfig struct {
	Type         string `json:"type"` // "enabled" | "adaptive" | "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// StreamEvent is one entry from the SSE stream.
type StreamEvent struct {
	Type         string         `json:"type"`
	Message      *MessageHeader `json:"message,omitempty"`       // message_start
	Index        int            `json:"index"`                   // block index
	ContentBlock *ContentBlock  `json:"content_block,omitempty"` // content_block_start
	Delta        *StreamDelta   `json:"delta,omitempty"`         // content_block_delta, message_delta
	Usage        *Usage         `json:"usage,omitempty"`         // message_delta, message_start
	Container    *ContainerInfo `json:"container,omitempty"`
	Error        *StreamError   `json:"error,omitempty"` // error
}

// MessageHeader is the initial payload of message_start.
type MessageHeader struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []ContentBlock `json:"content"`
	StopReason   string         `json:"stop_reason,omitempty"`
	StopSequence string         `json:"stop_sequence,omitempty"`
	Usage        *Usage         `json:"usage,omitempty"`
	Container    *ContainerInfo `json:"container,omitempty"`
}

// ContainerInfo describes an Anthropic code execution container.
type ContainerInfo struct {
	ID        string `json:"id,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// StreamDelta holds the incremental payload attached to a stream event.
type StreamDelta struct {
	Type         string `json:"type"` // text_delta | thinking_delta | input_json_delta | signature_delta
	Text         string `json:"text,omitempty"`
	Thinking     string `json:"thinking,omitempty"`
	PartialJSON  string `json:"partial_json,omitempty"`
	Signature    string `json:"signature,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
}

// StreamError is the inline error payload.
type StreamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Usage is the token accounting reported by the API.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// StreamHandler is called for each parsed SSE event. Return a non-nil
// error to stop the stream immediately.
type StreamHandler func(ev *StreamEvent) error

// HTTPError captures a non-2xx response.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	// Defensive nil receiver: a typed-nil *HTTPError stored in an error
	// interface (the classic Go typed-nil trap) would otherwise panic
	// when callers do `err.Error()` without first unwrapping. Returning
	// a sentinel string keeps the classifier / log-formatter call sites
	// safe and is observable enough that the bug shows up in logs.
	if e == nil {
		return "anthropic: <nil HTTPError>"
	}
	return fmt.Sprintf("anthropic: HTTP %d: %s", e.Status, e.Body)
}

// IsToolPairingError reports whether the error is a 400 whose body
// indicates that tool_use ↔ tool_result pairing is broken in the
// request payload. Used by the loop's recovery path: a single retry
// after RepairOrphans almost always resolves it (the broken state was
// in the assembled message slice, not in any model behavior).
//
// Anthropic returns one of two body shapes for these failures:
//
//	"unexpected `messages.N.content.M: tool_use_id` found in `tool_result`
//	 blocks: <id>. Each `tool_result` block must have a corresponding
//	 `tool_use` block in the previous message."
//
// or the converse "tool_use ids ... were not found in `tool_result`
// blocks".
//
// In addition, sub2api-style relays (and any proxy that internally
// converts Anthropic → OpenAI before forwarding to the upstream — e.g.
// Kimi K2 routed through sub2api) surface the same class of pairing
// failure with the OpenAI field name leaking through, wrapped in an
// Anthropic-style envelope:
//
//	{"error":{"type":"invalid_request_error",
//	          "message":"tool_call_id ... is not found"},
//	 "type":"error"}
//
// The underlying condition is identical — a tool result references an
// id with no matching tool call earlier in history — so the same
// RepairOrphans pass + one retry recovers it. We pattern-match on
// phrases stable across all known wordings.
func IsToolPairingError(err error) bool {
	if err == nil {
		return false
	}
	// errors.As + nil check tolerates fmt.Errorf %w wrappers a future
	// caller might add to a Stream error, and prevents a typed-nil
	// HTTPError stored in an error interface from sneaking through.
	var he *HTTPError
	if !errors.As(err, &he) || he == nil || he.Status != 400 {
		return false
	}
	body := he.Body
	// Anthropic-native phrasings reference tool_use_id (singular) or
	// tool_use ids (plural — note the space, not an underscore). The
	// two are not substrings of each other, so both must be checked.
	// OpenAI-style phrasings leaked through a converting proxy
	// reference tool_call_id; matching this also covers the plural
	// "tool_call_ids" by substring, so no separate plural check is
	// needed. Both families are recoverable by the same RepairOrphans
	// pass on the Anthropic-side message slice, because the proxy
	// derives its OpenAI tool_call_id values directly from our
	// tool_use_id values during conversion.
	hasIDKeyword := strings.Contains(body, "tool_use_id") ||
		strings.Contains(body, "tool_use ids") ||
		strings.Contains(body, "tool_call_id")
	if !hasIDKeyword {
		return false
	}
	// Pairing-specific evidence. Do NOT include "tool_use" or "tool_call"
	// here — they are substrings of the ID keywords above and would make
	// the check tautological, falsely classifying messages like
	// "invalid tool_call_id" (a format-validation error) as a pairing
	// error and triggering an unnecessary RepairOrphans retry.
	return strings.Contains(body, "tool_result") ||
		strings.Contains(body, "not found") ||
		strings.Contains(body, "not_found")
}

// Stream issues a streaming Messages request and dispatches events to
// handler. It applies the retry matrix from §4.4.5 of the refactor
// doc for transient failures (429/5xx).
func (c *Client) Stream(ctx context.Context, req *Request, handler StreamHandler) error {
	req.Stream = true

	// retryCounter (Phase 0.6) is nil for callers that didn't attach
	// one via ContextWithRetryCounter (e.g. compact / nudge paths or
	// older code paths that pre-date the telemetry contract); the
	// increment branch is silent in that case so Stream's behaviour
	// is unchanged for unattached calls.
	retryCounter := retryCounterFromContext(ctx)

	var lastErr error
	for attempt := 0; attempt < defaultAnthropicMaxTries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.doStream(ctx, req, handler)
		if err == nil {
			return nil
		}
		if !shouldRetry(err) {
			return err
		}
		lastErr = err
		// Increment the per-request retry counter so the agent loop's
		// step.llm.* telemetry can report how many recoveries this turn
		// burned through before either succeeding or surfacing an
		// error. We increment AFTER shouldRetry so failed attempts
		// without retry (4xx unrelated to 429) don't inflate the count.
		if retryCounter != nil {
			retryCounter.Add(1)
		}
		backoff := backoffDuration(attempt)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return lastErr
}

func backoffDuration(attempt int) time.Duration {
	// 1s, 2s, 4s (capped); tests can override via a shorter wrapper.
	d := time.Second << attempt
	const max = 8 * time.Second
	if d > max {
		d = max
	}
	return d
}

// retryableStatus reports whether an HTTP status is transient enough to
// retry with backoff.
func retryableStatus(status int) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	var denied *DispatchDeniedError
	if errors.As(err, &denied) {
		return false
	}
	// ModelCapacityError is only produced for statuses that retryableStatus
	// accepts, but check the status explicitly so the retry decision follows
	// the same rule either way.
	var capErr *ModelCapacityError
	if errors.As(err, &capErr) && capErr != nil {
		return retryableStatus(capErr.Status)
	}
	he, ok := err.(*HTTPError)
	if !ok {
		// Network / I/O error. Most are worth retrying (transient DNS
		// blips, momentary packet loss). Skip retries for unambiguously
		// terminal conditions — DNS resolution failure or no route —
		// so an offline machine surfaces the error quickly instead of
		// burning ~30s on backoff before reporting it to the UI.
		msg := err.Error()
		if strings.Contains(msg, "no such host") ||
			strings.Contains(msg, "network is unreachable") ||
			strings.Contains(msg, "no route to host") {
			return false
		}
		return true
	}
	return retryableStatus(he.Status)
}

//isoform:destination assistant
func (c *Client) doStream(ctx context.Context, req *Request, handler StreamHandler) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	url := c.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", c.version)
	if c.betaHeader != "" {
		httpReq.Header.Set("anthropic-beta", c.betaHeader)
	}
	if c.apiKey != "" {
		httpReq.Header.Set("x-api-key", c.apiKey)
	}
	if c.authToken != "" {
		httpReq.Header.Set("authorization", "Bearer "+c.authToken)
	}
	httpReq.Header.Set("user-agent", c.userAgent)
	if traceID := TraceIDFromContext(ctx); traceID != "" {
		httpReq.Header.Set("x-yansu-trace-id", traceID)
	}
	suppressAgentloop := SuppressAgentloopFromContext(ctx)
	if suppressAgentloop {
		// Signals to the backend's telemetry layer that this run opted out
		// of agentloop.* events. Necessary because the backend's trace_id
		// middleware mints a fresh UUID when no x-yansu-trace-id is sent,
		// which makes a plain "no trace_id → drop" rule unreachable.
		httpReq.Header.Set("x-yansu-suppress-agentloop", "1")
	}
	// Run id + step index let the backend conversation capture (plan §12)
	// place each captured request/response at the right step of a trace.
	// Both are best-effort: absent when the loop didn't stamp them.
	if runID := RunIDFromContext(ctx); runID != "" {
		httpReq.Header.Set("x-yansu-run-id", runID)
	}
	if stepIndex := StepIndexFromContext(ctx); stepIndex >= 0 {
		httpReq.Header.Set("x-yansu-step-index", fmt.Sprintf("%d", stepIndex))
	}
	// Chat session id for the local gateway's session stay policy. See
	// magpie.go for the header and the context key.
	if sessionID := SessionIDFromContext(ctx); sessionID != "" {
		httpReq.Header.Set(HeaderMagpieSession, sessionID)
	}

	if err := CheckBeforeDispatch(ctx); err != nil {
		return err
	}
	log.Printf("[native] HTTP POST %s model=%s bodyBytes=%d hasXAPIKey=%v hasAuth=%v", url, req.Model, len(body), c.apiKey != "", c.authToken != "")
	start := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		log.Printf("[native] HTTP request failed after %s: %v", time.Since(start), err)
		return err
	}
	defer resp.Body.Close()
	log.Printf("[native] HTTP status=%d (TTFB=%s) ct=%s", resp.StatusCode, time.Since(start), resp.Header.Get("content-type"))
	observeModelRoute(ctx, resp.Header)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(resp.Body)
		if !suppressAgentloop {
			log.Printf("[native] HTTP non-2xx body: %s", string(raw))
		}
		if capErr := modelCapacityFor(resp.StatusCode, raw, resp.Header); capErr != nil {
			return capErr
		}
		return &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}

	return parseSSE(resp.Body, handler, verboseSSE() && !suppressAgentloop)
}

// verboseSSE returns true when the YANSU_SSE_DEBUG env var is set to a
// non-empty value; used by ParseSSE to log each raw SSE line.
func verboseSSE() bool {
	return os.Getenv("YANSU_SSE_DEBUG") != ""
}

// ParseSSE reads an SSE stream from r, decodes each `data: { ... }`
// payload into a StreamEvent, and invokes handler. It exits when the
// stream returns EOF or handler returns an error.
func ParseSSE(r io.Reader, handler StreamHandler) error {
	return parseSSE(r, handler, verboseSSE())
}

func parseSSE(r io.Reader, handler StreamHandler, verbose bool) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var dataBuf bytes.Buffer
	lineCount := 0
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			lineCount++
			trimmed := bytes.TrimRight(line, "\r\n")
			if verbose {
				log.Printf("[native-sse] %d: %s", lineCount, string(trimmed))
			}
			switch {
			case len(trimmed) == 0:
				if dataBuf.Len() > 0 {
					if err := dispatchSSEEvent(dataBuf.Bytes(), handler); err != nil {
						return err
					}
					dataBuf.Reset()
				}
			case bytes.HasPrefix(trimmed, []byte(":")):
				// comment line, skip
			case bytes.HasPrefix(trimmed, []byte("data:")):
				payload := bytes.TrimSpace(trimmed[len("data:"):])
				if dataBuf.Len() > 0 {
					dataBuf.WriteByte('\n')
				}
				dataBuf.Write(payload)
			case bytes.HasPrefix(trimmed, []byte("event:")):
				// The Anthropic stream includes `event:` lines echoing the
				// type; we still rely on the embedded `type` in data.
			}
		}
		if err != nil {
			if err == io.EOF {
				// Flush any residual buffer.
				if dataBuf.Len() > 0 {
					return dispatchSSEEvent(dataBuf.Bytes(), handler)
				}
				log.Printf("[native] SSE stream closed cleanly after %d lines", lineCount)
				return nil
			}
			log.Printf("[native] SSE read error after %d lines: %v", lineCount, err)
			return err
		}
	}
}

func dispatchSSEEvent(payload []byte, handler StreamHandler) error {
	if len(payload) == 0 {
		return nil
	}
	var ev StreamEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Errorf("parse sse event: %w", err)
	}
	return handler(&ev)
}
