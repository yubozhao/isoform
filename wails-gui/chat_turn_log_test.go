package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yansu-agent/agent"
)

func TestChatTurnLogLine(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	end := start.Add(1500 * time.Millisecond)
	secret := "please email Dana the Q4 numbers"
	for _, tc := range []struct {
		name   string
		task   bool
		origin promptOrigin
		events []agent.StreamEvent
		ctxErr error
		failed bool
		runErr error
		want   string
	}{
		{"answered chat", false, promptOriginMac, []agent.StreamEvent{{Type: "run_created", Backend: "claude-code"}, {Type: "assistant", Content: secret}}, nil, false, nil,
			"[chat-turn] kind=chat caller=mac executor=claude_code outcome=answered duration_ms=1500 tag=t1"},
		{"no answer text", false, promptOriginPhone, []agent.StreamEvent{{Type: "run_created", Backend: "codex"}, {Type: "assistant"}}, nil, false, nil,
			"[chat-turn] kind=chat caller=phone executor=codex outcome=no_answer duration_ms=1500 tag=t1"},
		{"failed task keeps the status, not the words", true, promptOriginMac, []agent.StreamEvent{{Type: "run_created", Backend: "native"}, {Type: "error", Error: secret, Status: 401}}, nil, true, nil,
			"[chat-turn] kind=task caller=mac executor=isoform outcome=failed duration_ms=1500 tag=t1 status=401"},
		{"stopped before a runner", false, promptOriginPanelAsk, nil, context.Canceled, false, nil,
			"[chat-turn] kind=chat caller=panel_ask executor=none outcome=stopped duration_ms=1500 tag=t1"},
		{"out of time", false, promptOriginMac, []agent.StreamEvent{{Type: "run_created", Backend: "claude-code"}}, nil, true, errors.Join(errors.New(secret), context.DeadlineExceeded),
			"[chat-turn] kind=chat caller=mac executor=claude_code outcome=timed_out duration_ms=1500 tag=t1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := newChatTurnLog(tc.task, tc.origin, start)
			for i := range tc.events {
				turn.note(&tc.events[i])
			}
			got := turn.line(tc.ctxErr, tc.failed, tc.runErr, "t1", end)
			if got != tc.want {
				t.Fatalf("line\n got %q\nwant %q", got, tc.want)
			}
			if strings.Contains(got, "Dana") {
				t.Fatalf("the log line carried chat words: %q", got)
			}
		})
	}
}
