package main

import (
	"context"
	"testing"
	"time"

	"yansu-agent/agent"
)

// silentRunner starts a run and never sends anything until it is stopped.
type silentRunner struct{ started chan struct{} }

func (r *silentRunner) Init(context.Context) error { return nil }
func (r *silentRunner) IsReady() bool              { return true }
func (r *silentRunner) ExecuteTask(ctx context.Context, _ *agent.TaskInput, _ agent.StreamCallback) (string, error) {
	close(r.started)
	<-ctx.Done()
	return "silent-run", ctx.Err()
}
func (r *silentRunner) CancelRun(string) error                   { return nil }
func (r *silentRunner) RespondToPermission(string, string) error { return nil }
func (r *silentRunner) CancelPermission(string)                  {}
func (r *silentRunner) Shutdown() error                          { return nil }

// A Chat turn that shows nothing stops after the wait and says it couldn't
// answer, without retrying (computer-chat v1.1.0 rules 3 and 4).
func TestAChatTurnWithNoAnswerStopsAndSaysSo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rig := newUpcomingToolsRig(t)
	app := rig.app
	main, err := app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := &silentRunner{started: make(chan struct{})}
	app.runner = runner
	app.tagCancels = map[string]context.CancelFunc{}
	app.sessionTempDirs = map[string]string{}
	app.streams = map[string]context.CancelFunc{}
	app.runIDByTag = map[string]string{}
	app.pendingTraceIDs = map[string]string{}
	prior := chatNoAnswerWait
	chatNoAnswerWait = 100 * time.Millisecond
	t.Cleanup(func() { chatNoAnswerWait = prior })
	failed := make(chan AgentEvent, 4)
	stop := app.onAgentEvent(func(evt AgentEvent) {
		if evt.Tag == main.LocalID && evt.Type == "error" {
			failed <- evt
		}
	})
	defer stop()

	if _, err := app.SendMainChat("What did Sam and I agree on?"); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	select {
	case evt := <-failed:
		if evt.Code != chatCodeNoReply {
			t.Fatalf("error = %+v, want the no-answer code", evt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a turn with no answer never said so")
	}
	waitForTagIdle(t, app, main.LocalID)
	select {
	case evt := <-failed:
		t.Fatalf("a second error after the stop: %+v", evt)
	default:
	}
}
