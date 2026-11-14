package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

// AppKit acknowledges setup 200 to 300 ms after launch on a busy main thread
// (the app log showed attempt 1 "failing" at 200 ms on most launches, then
// completing 20 to 100 ms later). 500 ms leaves room for that; a genuinely
// stuck main thread still retries.
const menuBarInitAckTimeout = 500 * time.Millisecond

var menuBarInitBackoffs = []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}

type menuBarInitResult struct {
	attempt int
	err     error
}

// runMenuBarInit waits for AppKit to acknowledge setup, not merely dispatch it.
// A late success is valid: all attempts are idempotent on the main queue.
func runMenuBarInit(ctx context.Context, schedule func(int), results <-chan menuBarInitResult, timeout time.Duration, backoffs []time.Duration) error {
	for attempt := 1; attempt <= len(backoffs)+1; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		log.Printf("[menubar] initialization attempt %d scheduled", attempt)
		schedule(attempt)
		timer := time.NewTimer(timeout)
		var failure error
	wait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case result := <-results:
				if result.err == nil {
					timer.Stop()
					log.Printf("[menubar] initialized (attempt %d)", result.attempt)
					return nil
				}
				if result.attempt == attempt {
					failure = result.err
					break wait
				}
			case <-timer.C:
				failure = fmt.Errorf("main-thread setup did not acknowledge within %s", timeout)
				break wait
			}
		}
		timer.Stop()
		log.Printf("[menubar] initialization attempt %d failed: %v", attempt, failure)
		if attempt > len(backoffs) {
			return fmt.Errorf("menu bar initialization failed after %d attempts: %w", attempt, failure)
		}
		log.Printf("[menubar] retrying after %s", backoffs[attempt-1])
		delay := time.NewTimer(backoffs[attempt-1])
	backoff:
		for {
			select {
			case <-ctx.Done():
				delay.Stop()
				return ctx.Err()
			case result := <-results:
				if result.err == nil {
					delay.Stop()
					log.Printf("[menubar] initialized (late attempt %d)", result.attempt)
					return nil
				}
			case <-delay.C:
				break backoff
			}
		}
	}
	return nil
}
