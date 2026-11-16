package main

import (
	"context"
	"testing"
	"time"

	"yansu-agent/agent"
)

// A message sent in the first half minute after launch, before the agent is
// ready, waits for it and is answered; it is not dropped after 15 s. The daily
// trial's runner was ready 32 s after its first message.
func TestATurnSentBeforeTheAgentIsReadyIsAnswered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rig := newUpcomingToolsRig(t)
	app := rig.app
	main, err := app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 1)}
	app.tagCancels = map[string]context.CancelFunc{}
	app.sessionTempDirs = map[string]string{}
	app.streams = map[string]context.CancelFunc{}
	app.runIDByTag = map[string]string{}
	app.pendingTraceIDs = map[string]string{}
	app.mu.Lock()
	app.runner = nil
	app.ready = false
	app.readyCh = make(chan struct{})
	app.mu.Unlock()

	// A clock that fires only once the requested wait has passed.
	elapsed := make(chan time.Duration, 1)
	var asked time.Duration
	prior := agentReadyTurnAfter
	agentReadyTurnAfter = func(d time.Duration) <-chan time.Time {
		asked = d
		fired := make(chan time.Time, 1)
		go func() {
			if gone := <-elapsed; gone >= d {
				fired <- time.Now()
			}
		}()
		return fired
	}
	t.Cleanup(func() { agentReadyTurnAfter = prior })

	if _, err := app.SendMainChat("What did Sam and I agree on at lunch?"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for asked == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if asked < time.Minute {
		t.Fatalf("a turn waits %s for the agent; launch takes longer than that", asked)
	}
	// 32 s pass, then the agent is ready.
	elapsed <- 32 * time.Second
	app.mu.Lock()
	app.runner = runner
	app.setReadyLocked(true)
	app.mu.Unlock()
	select {
	case input := <-runner.inputs:
		if input == nil {
			t.Fatal("no input")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn sent before the agent was ready was never answered")
	}
	waitForTagIdle(t, app, main.LocalID)
}
