package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// OpenAIClient is a translating adapter that lets the Anthropic-native
// agent loop talk to an OpenAI Chat Completions endpoint (OpenAI itself
// or any OpenAI-compatible gateway: OpenRouter, vLLM, Groq, Together,
// LiteLLM, ...). It satisfies LLMClient, so the loop never learns which
// wire protocol is underneath.
//
// The adapter does two translations on the fly:
//
//  1. Request: the loop hands us an Anthropic-shaped *Request (Messages,
//     ContentBlocks, ToolDefs, ToolChoice). We rewrite it into the
//     Chat Completions shape. Anthropic-only fields (thinking, container,
//     cache_control, metadata) are simply not read, so they vanish — this
//     is the "feature gating for free" the loop relies on.
//
//  2. Response: Chat Completions streams ChatCompletionChunk SSE frames
//     (delta.content, delta.tool_calls[], finish_reason, usage). We
//     reconstruct the Anthropic StreamEvent vocabulary the loop's SSE
//     handler consumes (message_start, content_block_start/delta/stop,
//     message_delta, message_stop) so runOneTurn assembles identical
//     assistant blocks regardless of provider.
//
// Block-index convention for the reconstructed stream: text always uses
// block index 0 (the loop accumulates text deltas in a single builder and
// does not require a content_block_start for it); each OpenAI tool_call at
// its 0-based index i maps to Anthropic block index i+1.
type OpenAIClient struct {
	http      *http.Client
	baseURL   string
	apiKey    string
	authToken string
	userAgent string
}

const defaultOpenAIBase = "https://api.openai.com/v1"

// NewOpenAIClient builds an adapter. baseURL conventionally already ends
// in the version segment (e.g. https://api.openai.com/v1); we POST to
// baseURL + "/chat/completions". Either apiKey or authToken is sent as the
// bearer credential (apiKey wins when both are set).
//
//isoform:destination assistant
func NewOpenAIClient(baseURL, apiKey, authToken string) *OpenAIClient {
	// Mirror the Anthropic client's transport tuning: fail fast on
	// connect/TLS so an offline machine surfaces an error instead of
	// spinning, but leave the overall timeout at 0 so long SSE responses
	// can stream for minutes.
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
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = defaultOpenAIBase
	}
	return &OpenAIClient{
		http:      &http.Client{Timeout: 0, Transport: transport},
		baseURL:   base,
		apiKey:    apiKey,
		authToken: authToken,
		userAgent: "yansu-native-agent/1.0 (openai-compat)",
	}
}

// ── OpenAI wire types ────────────────────────────────────────────────────

type oaiRequest struct {
	Model           string       `json:"model"`
	Messages        []oaiMessage `json:"messages"`
	Tools           []oaiTool    `json:"tools,omitempty"`
	ToolChoice      any          `json:"tool_choice,omitempty"`
	ReasoningEffort string       `json:"reasoning_effort,omitempty"`
	// NOTE: no max_tokens / max_completion_tokens here. Some OpenAI-compatible
	// gateways reject an output cap outright (opaque HTTP 500), so this path
	// never sends one — the provider's own default applies. Do not reintroduce
	// a max_tokens field or assignment for the OpenAI-compatible wire.
	Temperature   *float64       `json:"temperature,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *oaiStreamOpts `json:"stream_options,omitempty"`
}

type oaiStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content,omitempty"` // string | []oaiContentPart | nil
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiContentPart struct {
	Type     string       `json:"type"` // "text" | "image_url"
	Text     string       `json:"text,omitempty"`
	ImageURL *oaiImageURL `json:"image_url,omitempty"`
}

type oaiImageURL struct {
	URL string `json:"url"`
}

type oaiTool struct {
	Type     string         `json:"type"` // "function"
	Function oaiFunctionDef `json:"function"`
}

type oaiFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type oaiToolCall struct {
	Index    int             `json:"index"`
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type,omitempty"` // "function"
	Function oaiFunctionCall `json:"function"`
}

type oaiFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// Streaming response chunk.
type oaiStreamChunk struct {
	Choices []oaiChoice `json:"choices"`
	Usage   *oaiUsage   `json:"usage,omitempty"`
}

type oaiChoice struct {
	Delta        oaiDelta `json:"delta"`
	FinishReason *string  `json:"finish_reason"`
}

type oaiDelta struct {
	Content   *string       `json:"content"`
	ToolCalls []oaiToolCall `json:"tool_calls,omitempty"`
}

type oaiUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

// ── Stream (LLMClient) ───────────────────────────────────────────────────

// Stream issues a streaming Chat Completions request and translates the
// SSE frames into Anthropic StreamEvents dispatched to handler. It reuses
// the package retry matrix (shouldRetry / backoffDuration) so transient
// 429/5xx behave the same as the Anthropic path.
func (c *OpenAIClient) Stream(ctx context.Context, req *Request, handler StreamHandler) error {
	oReq := c.translateRequest(req)

	retryCounter := retryCounterFromContext(ctx)
	var lastErr error
	for attempt := 0; attempt < defaultAnthropicMaxTries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.doStream(ctx, oReq, handler)
		if err == nil {
			return nil
		}
		if !shouldRetry(err) {
			return err
		}
		lastErr = err
		if retryCounter != nil {
			retryCounter.Add(1)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoffDuration(attempt)):
		}
	}
	return lastErr
}

//isoform:destination assistant
func (c *OpenAIClient) doStream(ctx context.Context, oReq *oaiRequest, handler StreamHandler) error {
	body, err := json.Marshal(oReq)
	if err != nil {
		return err
	}
	url := c.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	key := c.apiKey
	if key == "" {
		key = c.authToken
	}
	if key != "" {
		httpReq.Header.Set("authorization", "Bearer "+key)
	}
	httpReq.Header.Set("user-agent", c.userAgent)
	if traceID := TraceIDFromContext(ctx); traceID != "" {
		httpReq.Header.Set("x-yansu-trace-id", traceID)
	}
	suppressAgentloop := SuppressAgentloopFromContext(ctx)
	if suppressAgentloop {
		httpReq.Header.Set("x-yansu-suppress-agentloop", "1")
	}
	// Chat session id for the local gateway's session stay policy. See
	// magpie.go for the header and the context key.
	if sessionID := SessionIDFromContext(ctx); sessionID != "" {
		httpReq.Header.Set(HeaderMagpieSession, sessionID)
	}

	if err := CheckBeforeDispatch(ctx); err != nil {
		return err
	}
	log.Printf("[openai] HTTP POST %s model=%s bodyBytes=%d", url, oReq.Model, len(body))
	start := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		log.Printf("[openai] HTTP request failed after %s: %v", time.Since(start), err)
		return err
	}
	defer resp.Body.Close()
	log.Printf("[openai] HTTP status=%d (TTFB=%s)", resp.StatusCode, time.Since(start))
	observeModelRoute(ctx, resp.Header)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(resp.Body)
		if !suppressAgentloop {
			log.Printf("[openai] HTTP non-2xx body: %s", string(raw))
		}
		if capErr := modelCapacityFor(resp.StatusCode, raw, resp.Header); capErr != nil {
			return capErr
		}
		return &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}

	return c.parseSSE(resp.Body, handler)
}

// ── Response translation ─────────────────────────────────────────────────

// parseSSE reads the Chat Completions SSE stream and reconstructs the
// Anthropic StreamEvent sequence. It maintains the per-tool-call assembly
// state needed to emit content_block_start once per tool and matching
// content_block_stop frames at the end so the loop parses tool inputs.
func (c *OpenAIClient) parseSSE(r io.Reader, handler StreamHandler) error {
	// message_start lets the loop initialise its accounting; usage is
	// filled later via message_delta when the final usage chunk arrives.
	if err := handler(&StreamEvent{Type: "message_start", Message: &MessageHeader{Role: "assistant"}}); err != nil {
		return err
	}

	startedTools := map[int]bool{} // OpenAI tool index -> content_block_start emitted
	var toolOrder []int            // OpenAI tool indices in first-seen order
	finishReason := ""

	br := bufio.NewReaderSize(r, 64*1024)
	var dataBuf bytes.Buffer

	flushTools := func() error {
		// Close every tool block we opened so the loop parses the
		// accumulated argument JSON. Anthropic block index = i+1.
		for _, i := range toolOrder {
			if err := handler(&StreamEvent{Type: "content_block_stop", Index: i + 1}); err != nil {
				return err
			}
		}
		return nil
	}

	dispatch := func(payload []byte) error {
		trimmed := bytes.TrimSpace(payload)
		if len(trimmed) == 0 {
			return nil
		}
		if bytes.Equal(trimmed, []byte("[DONE]")) {
			return nil
		}
		var chunk oaiStreamChunk
		if err := json.Unmarshal(trimmed, &chunk); err != nil {
			// A malformed frame should not kill an otherwise healthy
			// stream; log and skip it.
			log.Printf("[openai] skipping unparseable SSE frame: %v", err)
			return nil
		}

		// Usage-only chunk (stream_options.include_usage) arrives with
		// empty choices after the final content chunk.
		if chunk.Usage != nil {
			u := &Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
			if chunk.Usage.PromptTokensDetails != nil {
				u.CacheReadInputTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			if err := handler(&StreamEvent{Type: "message_delta", Usage: u}); err != nil {
				return err
			}
		}

		for ci := range chunk.Choices {
			choice := chunk.Choices[ci]

			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				if err := handler(&StreamEvent{
					Type:  "content_block_delta",
					Index: 0,
					Delta: &StreamDelta{Type: "text_delta", Text: *choice.Delta.Content},
				}); err != nil {
					return err
				}
			}

			for ti := range choice.Delta.ToolCalls {
				tc := choice.Delta.ToolCalls[ti]
				idx := tc.Index
				if !startedTools[idx] {
					startedTools[idx] = true
					toolOrder = append(toolOrder, idx)
					if err := handler(&StreamEvent{
						Type:  "content_block_start",
						Index: idx + 1,
						ContentBlock: &ContentBlock{
							Type: "tool_use",
							ID:   tc.ID,
							Name: tc.Function.Name,
						},
					}); err != nil {
						return err
					}
				}
				if tc.Function.Arguments != "" {
					if err := handler(&StreamEvent{
						Type:  "content_block_delta",
						Index: idx + 1,
						Delta: &StreamDelta{Type: "input_json_delta", PartialJSON: tc.Function.Arguments},
					}); err != nil {
						return err
					}
				}
			}

			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
		}
		return nil
	}

	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			t := bytes.TrimRight(line, "\r\n")
			switch {
			case len(t) == 0:
				if dataBuf.Len() > 0 {
					if derr := dispatch(dataBuf.Bytes()); derr != nil {
						return derr
					}
					dataBuf.Reset()
				}
			case bytes.HasPrefix(t, []byte(":")):
				// comment / keep-alive line
			case bytes.HasPrefix(t, []byte("data:")):
				payload := bytes.TrimSpace(t[len("data:"):])
				if dataBuf.Len() > 0 {
					dataBuf.WriteByte('\n')
				}
				dataBuf.Write(payload)
			}
		}
		if err != nil {
			if err == io.EOF {
				if dataBuf.Len() > 0 {
					if derr := dispatch(dataBuf.Bytes()); derr != nil {
						return derr
					}
				}
				break
			}
			return err
		}
	}

	// Close tool blocks, then signal end-of-turn with the mapped stop
	// reason and message_stop. content_block_stop must precede the
	// message_delta so the loop has appended every tool_use block first.
	if err := flushTools(); err != nil {
		return err
	}
	if err := handler(&StreamEvent{
		Type:  "message_delta",
		Delta: &StreamDelta{StopReason: mapOpenAIFinishReason(finishReason, len(toolOrder) > 0)},
	}); err != nil {
		return err
	}
	return handler(&StreamEvent{Type: "message_stop"})
}

func mapOpenAIFinishReason(reason string, hadToolCalls bool) string {
	switch reason {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "stop", "content_filter":
		return "end_turn"
	default:
		// Some gateways omit finish_reason on the last content chunk and
		// only send it on a trailing empty chunk we might not see. Infer
		// from whether tool calls were produced.
		if hadToolCalls {
			return "tool_use"
		}
		return "end_turn"
	}
}

// ── Request translation ──────────────────────────────────────────────────

func (c *OpenAIClient) translateRequest(req *Request) *oaiRequest {
	o := &oaiRequest{
		Model:         req.Model,
		Messages:      translateMessages(req),
		Temperature:   req.Temperature,
		Stream:        true,
		StreamOptions: &oaiStreamOpts{IncludeUsage: true},
	}
	for i := range req.Tools {
		t := req.Tools[i]
		// Skip server-side / built-in Anthropic tools that carry no JSON
		// schema — they have no OpenAI function equivalent.
		params := t.InputSchema
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		o.Tools = append(o.Tools, oaiTool{
			Type: "function",
			Function: oaiFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	if req.OutputConfig != nil {
		o.ReasoningEffort = req.OutputConfig.Effort
	}
	// GPT-5.6 defaults to reasoning effort "medium", but Chat Completions
	// rejects function tools unless its effective reasoning effort is "none".
	// Keep this compatibility field scoped to GPT-5.6 requests that actually
	// carry translated tools so unrelated models and tool-free requests retain
	// exactly the same wire shape as before.
	if len(o.Tools) > 0 && isGPT56Model(o.Model) {
		o.ReasoningEffort = "none"
	}
	o.ToolChoice = translateToolChoice(req.ToolChoice)
	return o
}

func isGPT56Model(model string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	return model == "gpt-5.6" || strings.HasPrefix(model, "gpt-5.6-")
}

func translateToolChoice(tc *ToolChoice) any {
	if tc == nil {
		return nil
	}
	switch tc.Type {
	case "any":
		return "required"
	case "auto":
		return "auto"
	case "tool":
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}
	default:
		return nil
	}
}

func translateMessages(req *Request) []oaiMessage {
	out := make([]oaiMessage, 0, len(req.Messages)+1)

	if sys := blocksToPlainText(req.System); sys != "" {
		out = append(out, oaiMessage{Role: "system", Content: sys})
	}

	for i := range req.Messages {
		m := req.Messages[i]
		blocks, isBlocks := normalizeContentBlocks(m.Content)
		if !isBlocks {
			out = append(out, oaiMessage{Role: m.Role, Content: anyToString(m.Content)})
			continue
		}

		if m.Role == "assistant" {
			msg := oaiMessage{Role: "assistant"}
			var text strings.Builder
			for _, b := range blocks {
				switch b.Type {
				case "text":
					text.WriteString(b.Text)
				case "tool_use":
					args, err := json.Marshal(b.Input)
					if err != nil || b.Input == nil {
						args = []byte("{}")
					}
					msg.ToolCalls = append(msg.ToolCalls, oaiToolCall{
						ID:       b.ID,
						Type:     "function",
						Function: oaiFunctionCall{Name: b.Name, Arguments: string(args)},
					})
				}
				// thinking / signature / unknown blocks are dropped: they
				// have no Chat Completions representation.
			}
			if text.Len() > 0 {
				msg.Content = text.String()
			}
			out = append(out, msg)
			continue
		}

		// user (or any non-assistant) role. tool_result blocks become
		// standalone {role:"tool"} messages, emitted first so they
		// immediately follow the assistant tool_calls message as OpenAI
		// requires. Remaining text/image blocks form one user message.
		var parts []oaiContentPart
		hasImage := false
		var plain strings.Builder
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				out = append(out, oaiMessage{
					Role:       "tool",
					ToolCallID: b.ToolUseID,
					Content:    toolResultToText(b.Content),
				})
			case "text":
				plain.WriteString(b.Text)
				parts = append(parts, oaiContentPart{Type: "text", Text: b.Text})
			case "image":
				if b.Source != nil {
					hasImage = true
					parts = append(parts, oaiContentPart{
						Type:     "image_url",
						ImageURL: &oaiImageURL{URL: "data:" + b.Source.MediaType + ";base64," + b.Source.Data},
					})
				}
			}
		}
		switch {
		case hasImage && len(parts) > 0:
			out = append(out, oaiMessage{Role: "user", Content: parts})
		case plain.Len() > 0:
			out = append(out, oaiMessage{Role: "user", Content: plain.String()})
		}
	}
	return out
}

// normalizeContentBlocks coerces a Message.Content (which is `any`: a
// string for plain turns, []ContentBlock for assembled turns, or
// []interface{} after a JSON history round-trip) into []ContentBlock.
// The bool is false when the content is a plain string.
func normalizeContentBlocks(c any) ([]ContentBlock, bool) {
	switch v := c.(type) {
	case nil:
		return nil, false
	case string:
		return nil, false
	case []ContentBlock:
		return v, true
	default:
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, false
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return nil, false
		}
		var blocks []ContentBlock
		if json.Unmarshal(raw, &blocks) == nil {
			return blocks, true
		}
		return nil, false
	}
}

// blocksToPlainText flattens a system field (string or []ContentBlock)
// into a single string. Chat Completions has no structured system blocks.
func blocksToPlainText(c any) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		blocks, ok := normalizeContentBlocks(c)
		if !ok {
			return anyToString(c)
		}
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
}

// toolResultToText renders a tool_result block's Content (string, or a
// list of text content blocks, or arbitrary JSON) into the plain string
// OpenAI's tool message expects.
func toolResultToText(c any) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		if blocks, ok := normalizeContentBlocks(c); ok {
			var b strings.Builder
			for _, blk := range blocks {
				if blk.Type == "text" {
					b.WriteString(blk.Text)
				}
			}
			if b.Len() > 0 {
				return b.String()
			}
		}
		return anyToString(c)
	}
}

func anyToString(c any) string {
	if c == nil {
		return ""
	}
	if s, ok := c.(string); ok {
		return s
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Sprintf("%v", c)
	}
	return string(raw)
}
