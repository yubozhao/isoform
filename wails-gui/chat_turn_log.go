package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"yansu-agent/agent"
)

// chatTurnLog is the one content-free log line a chat turn or Task run
// leaves when it settles: who asked, which assistant ran it, how long it
// took and how it ended. It carries ids and codes only, never words, so
// failures can be counted from yansu-gui.log without reading any chat.
type chatTurnLog struct {
	mu       sync.Mutex
	started  time.Time
	kind     string // "chat" or "task"
	caller   string // the prompt origin: mac, phone, panel_ask, phone_ask, resume
	executor string // claude_code, codex, isoform; empty until the run starts
	answered bool   // the assistant streamed any answer text
	status   int    // the provider's HTTP status on an error event
}

func newChatTurnLog(isTask bool, origin promptOrigin, now time.Time) *chatTurnLog {
	kind := "chat"
	if isTask {
		kind = "task"
	}
	caller := string(origin)
	if caller == "" {
		caller = "mac"
	}
	return &chatTurnLog{started: now, kind: kind, caller: caller}
}

// note folds one runner event into the line.
func (l *chatTurnLog) note(event *agent.StreamEvent) {
	if l == nil || event == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch event.Type {
	case "run_created":
		switch event.Backend {
		case "claude-code":
			l.executor = "claude_code"
		case "codex":
			l.executor = "codex"
		case "native":
			l.executor = "isoform"
		}
	case "assistant":
		if event.Content != "" {
			l.answered = true
		}
	case "error":
		if event.Status != 0 {
			l.status = event.Status
		}
	}
}

// line is the settled turn: outcome is answered, no_answer, failed, stopped
// or timed_out. ctxErr is the turn context's error; failed says an error
// was shown; runErr is the runner's own error, used only for its kind.
func (l *chatTurnLog) line(ctxErr error, failed bool, runErr error, tag string, now time.Time) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	outcome := "answered"
	switch {
	case errors.Is(ctxErr, context.Canceled):
		outcome = "stopped"
	case errors.Is(ctxErr, context.DeadlineExceeded), errors.Is(runErr, context.DeadlineExceeded):
		outcome = "timed_out"
	case failed || runErr != nil:
		outcome = "failed"
	case !l.answered:
		outcome = "no_answer"
	}
	executor := l.executor
	if executor == "" {
		executor = "none"
	}
	line := fmt.Sprintf("[chat-turn] kind=%s caller=%s executor=%s outcome=%s duration_ms=%d tag=%s",
		l.kind, l.caller, executor, outcome, now.Sub(l.started).Milliseconds(), tag)
	if l.status != 0 {
		line += fmt.Sprintf(" status=%d", l.status)
	}
	return line
}
