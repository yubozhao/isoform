package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"yansu-agent/agent/native"
)

// mobileBriefWriterErrorClass names why the brief writer's model call failed, as a
// class only. The log gets this and nothing else: never the prompt, the reply, the
// provider's message or a token (design R74, log review).
func mobileBriefWriterErrorClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errMobileBriefSourcesChanged) {
		return "sources_changed"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var assistant *mobileBriefAssistantError
	if errors.As(err, &assistant) {
		return "signed_out"
	}
	var httpErr *native.HTTPError
	if errors.As(err, &httpErr) && httpErr != nil {
		switch httpErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "auth_rejected"
		case http.StatusTooManyRequests:
			return "rate_limited"
		}
		return fmt.Sprintf("http_%d", httpErr.Status)
	}
	// Validation failures are fixed strings with no user content, so the
	// reason is safe to log and tells which check refused the draft.
	var code codeError
	if errors.As(err, &code) {
		return "check: " + string(code)
	}
	return "other"
}
