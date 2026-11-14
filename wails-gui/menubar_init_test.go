package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMenuBarInitStateMachine(t *testing.T) {
	t.Run("success stops after acknowledgement", func(t *testing.T) {
		results := make(chan menuBarInitResult, 3)
		var attempts []int
		err := runMenuBarInit(context.Background(), func(attempt int) {
			attempts = append(attempts, attempt)
			results <- menuBarInitResult{attempt: attempt}
		}, results, time.Second, []time.Duration{time.Millisecond, time.Millisecond})
		if err != nil || !reflect.DeepEqual(attempts, []int{1}) {
			t.Fatalf("attempts=%v error=%v", attempts, err)
		}
	})
	t.Run("failure retries and recovers", func(t *testing.T) {
		results := make(chan menuBarInitResult, 3)
		var attempts []int
		start := time.Now()
		err := runMenuBarInit(context.Background(), func(attempt int) {
			attempts = append(attempts, attempt)
			var failure error
			if attempt < 3 {
				failure = errors.New("button unavailable")
			}
			results <- menuBarInitResult{attempt: attempt, err: failure}
		}, results, time.Second, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond})
		if err != nil || !reflect.DeepEqual(attempts, []int{1, 2, 3}) || time.Since(start) < 30*time.Millisecond {
			t.Fatalf("attempts=%v elapsed=%v error=%v", attempts, time.Since(start), err)
		}
	})
	t.Run("unresponsive main queue is bounded", func(t *testing.T) {
		results := make(chan menuBarInitResult, 3)
		var attempts []int
		err := runMenuBarInit(context.Background(), func(attempt int) { attempts = append(attempts, attempt) }, results, time.Millisecond, []time.Duration{time.Millisecond, 2 * time.Millisecond})
		if err == nil || !reflect.DeepEqual(attempts, []int{1, 2, 3}) {
			t.Fatalf("attempts=%v error=%v", attempts, err)
		}
	})
	t.Run("late success cancels retries", func(t *testing.T) {
		results := make(chan menuBarInitResult, 3)
		var attempts []int
		err := runMenuBarInit(context.Background(), func(attempt int) {
			attempts = append(attempts, attempt)
			if attempt == 2 {
				results <- menuBarInitResult{attempt: 1}
			}
		}, results, time.Millisecond, []time.Duration{time.Millisecond, time.Millisecond})
		if err != nil || !reflect.DeepEqual(attempts, []int{1, 2}) {
			t.Fatalf("attempts=%v error=%v", attempts, err)
		}
	})
	t.Run("shutdown cancels a queued attempt", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var attempts []int
		err := runMenuBarInit(ctx, func(attempt int) { attempts = append(attempts, attempt); cancel() }, make(chan menuBarInitResult), time.Second, []time.Duration{time.Second, time.Second})
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(attempts, []int{1}) {
			t.Fatalf("attempts=%v error=%v", attempts, err)
		}
	})
}

// A main thread that acknowledges 300 ms after launch, as most launches in
// the app log did, initializes on the first attempt with no retry and no
// "attempt 1 failed" line.
func TestMenuBarInitAcceptsSlowFirstAcknowledgement(t *testing.T) {
	results := make(chan menuBarInitResult, 3)
	var attempts []int
	err := runMenuBarInit(context.Background(), func(attempt int) {
		attempts = append(attempts, attempt)
		go func() {
			time.Sleep(300 * time.Millisecond)
			results <- menuBarInitResult{attempt: attempt}
		}()
	}, results, menuBarInitAckTimeout, menuBarInitBackoffs)
	if err != nil {
		t.Fatalf("runMenuBarInit: %v", err)
	}
	if !reflect.DeepEqual(attempts, []int{1}) {
		t.Fatalf("attempts = %v, want [1]", attempts)
	}
}

// An acknowledgement that lands just after the timeout, during the backoff,
// is a success: the log must not say the attempt failed (every launch on
// 11/13 logged "attempt 1 failed" and then "initialized (late attempt 1)").
func TestMenuBarInitLateAcknowledgementLogsNoFailure(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })

	results := make(chan menuBarInitResult, 3)
	err := runMenuBarInit(context.Background(), func(attempt int) {
		go func() {
			time.Sleep(30 * time.Millisecond)
			results <- menuBarInitResult{attempt: attempt}
		}()
	}, results, 10*time.Millisecond, []time.Duration{200 * time.Millisecond, 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("runMenuBarInit: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "failed") || strings.Contains(out, "retrying") {
		t.Fatalf("a late acknowledgement logged a failure:\n%s", out)
	}
	if !strings.Contains(out, "initialized (late attempt 1)") {
		t.Fatalf("late success not logged:\n%s", out)
	}
}
