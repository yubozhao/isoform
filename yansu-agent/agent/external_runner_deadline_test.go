package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yansu-agent/agent/authority"
	"yansu-agent/agent/toolserver"
)

func TestControlledBudgetIsOptIn(t *testing.T) {
	for _, budget := range []float64{0, 2.5} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			args := controlledClaudeArgs(&TaskInput{}, ExternalAgentConfig{MaxBudgetUSD: budget}, "/run/mcp.json", nil)
			got := ""
			for i, arg := range args {
				if arg == "--max-budget-usd" {
					got = args[i+1]
				}
			}
			want := ""
			if budget > 0 {
				want = "2.5"
			}
			if got != want {
				t.Fatalf("budget flag = %q, want %q", got, want)
			}
		})
	}
}

func TestControlledContextKeepsCallerDeadline(t *testing.T) {
	for _, timeout := range []time.Duration{30 * time.Minute, 60 * time.Minute} {
		t.Run(timeout.String(), func(t *testing.T) {
			ctx := context.Background()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			stop := errors.New("deadline captured")
			var got time.Time
			var gotDeadline bool
			runner := NewExternalAgentRunner(ExternalAgentConfig{ToolShimCommand: "/bundled/yansu", ToolServerFactory: func(runCtx context.Context, _ *TaskInput, _ string, _ StreamCallback) (*toolserver.Server, error) {
				got, gotDeadline = runCtx.Deadline()
				return nil, stop
			}})
			runner.profileCheckedAt = time.Now()
			runner.binary = "/unused/claude"
			_, err := runner.ExecuteTask(ctx, &TaskInput{Prompt: "capture caller deadline"}, nil)
			if !errors.Is(err, stop) {
				t.Fatal(err)
			}
			want, wantDeadline := ctx.Deadline()
			if gotDeadline != wantDeadline || (wantDeadline && !got.Equal(want)) {
				t.Fatalf("run deadline = %s (%v), caller deadline = %s (%v)", got, gotDeadline, want, wantDeadline)
			}
		})
	}
}

func TestControlledDeadlineLessRunIsBounded(t *testing.T) {
	stop := errors.New("deadline captured")
	runner := NewExternalAgentRunner(ExternalAgentConfig{ToolShimCommand: "/bundled/yansu", ToolServerFactory: func(runCtx context.Context, _ *TaskInput, _ string, _ StreamCallback) (*toolserver.Server, error) {
		deadline, ok := runCtx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Minute || time.Until(deadline) < 29*time.Minute {
			t.Errorf("deadline-less run has deadline %v (%v), want finite 30-minute bound", deadline, ok)
		}
		return nil, stop
	}})
	runner.profileCheckedAt = time.Now()
	runner.binary = "/unused/claude"
	_, err := runner.ExecuteTask(context.Background(), &TaskInput{Prompt: "memory triage", NoTools: true, Ephemeral: true}, nil)
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

func TestControlledInsufficientBackupBudgetSkipsPreflight(t *testing.T) {
	probe := writeFakeCLI(t, "claude", `echo "unexpected probe" >&2; exit 9`)
	runner := NewExternalAgentRunner(ExternalAgentConfig{Command: probe, ToolShimCommand: "/bundled/yansu", ToolServerFactory: func(context.Context, *TaskInput, string, StreamCallback) (*toolserver.Server, error) {
		t.Fatal("insufficient backup time reached tool-server creation")
		return nil, nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute+4*time.Second)
	defer cancel()
	ctx = WithNativeFallbackBudget(ctx, 30*time.Minute)
	_, err := runner.ExecuteTask(ctx, &TaskInput{Prompt: "preserve native budget", NoTools: true}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("assistant did not reserve native budget before probing: %v", err)
	}
}

func TestControlledDeadlineLessPreflightIsBounded(t *testing.T) {
	probe := writeFakeCLI(t, "claude", `sleep 30`)
	runner := NewExternalAgentRunner(ExternalAgentConfig{Command: probe, ToolShimCommand: "/bundled/yansu", RunTimeout: 100 * time.Millisecond, ToolServerFactory: func(context.Context, *TaskInput, string, StreamCallback) (*toolserver.Server, error) {
		t.Fatal("stalled profile reached tool-server creation")
		return nil, nil
	}})
	_, err := runner.ExecuteTask(context.Background(), &TaskInput{Prompt: "memory triage", NoTools: true, Ephemeral: true}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled profile did not honor run deadline: %v", err)
	}
}

func TestControlledDeadlineLessStalledBeforeInit(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		t.Run(fmt.Sprint(ephemeral), func(t *testing.T) {
			externalFixtureState(t)
			binary := writeFakeCLI(t, "claude", `sleep 30`)
			native := NewNativeAgentRunner(NativeRunnerConfig{DisableEnvFallback: true})
			runner := NewExternalAgentRunner(ExternalAgentConfig{Command: binary, ToolShimCommand: "/bundled/yansu", AttemptTimeout: 100 * time.Millisecond, ToolServerFactory: native.NewToolServer})
			runner.profileCheckedAt = time.Now()
			if err := runner.Init(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, err := runner.ExecuteTask(context.Background(), &TaskInput{Prompt: "memory triage", NoTools: true, Ephemeral: ephemeral}, nil)
			var proof *ExternalAttemptError
			if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &proof) || proof.Actions != 0 || proof.Unfinished {
				t.Fatalf("stalled init lacks timeout and empty-ledger proof: %v", err)
			}
		})
	}
}

func TestControlledSetupPreservesDeadlineCause(t *testing.T) {
	for _, blockConfigWrite := range []bool{false, true} {
		t.Run(fmt.Sprint(blockConfigWrite), func(t *testing.T) {
			state := externalFixtureState(t)
			marker := filepath.Join(state, "unexpected-cli-start")
			binary := writeFakeCLI(t, "claude", `: > '`+marker+`'; exit 9`)
			native := NewNativeAgentRunner(NativeRunnerConfig{DisableEnvFallback: true})
			var server *toolserver.Server
			runner := NewExternalAgentRunner(ExternalAgentConfig{
				Command: binary, ToolShimCommand: "/bundled/yansu",
				ToolServerFactory: func(ctx context.Context, input *TaskInput, runID string, callback StreamCallback) (*toolserver.Server, error) {
					var err error
					server, err = native.NewToolServer(ctx, input, runID, callback)
					return server, err
				},
			})
			runner.profileCheckedAt = time.Now()
			if err := runner.Init(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			_, err := runner.ExecuteTask(ctx, &TaskInput{Prompt: "expire during setup", NoTools: true}, func(event *StreamEvent) error {
				if event.Type == "run_created" {
					if blockConfigWrite {
						// Force an early config-write error as well as the normal
						// canceled cmd.Start path, preserving the action ledger.
						path := filepath.Join(filepath.Dir(server.TicketPath()), "claude-mcp.json")
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(path, "fixture"), nil, 0600); err != nil {
							t.Fatal(err)
						}
					}
					// Match the pre-action timer: Err is Canceled, but the
					// cause is DeadlineExceeded, without racing a short timer.
					cancel(context.DeadlineExceeded)
				}
				return nil
			})
			var proof *ExternalAttemptError
			if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || !errors.As(err, &proof) || proof.Actions != 0 || proof.Unfinished {
				t.Fatalf("setup timeout lost deadline or empty-ledger proof: %v", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expired setup started the CLI: %v", err)
			}
		})
	}
}

func TestControlledDeadlineLessWallClockSurvivesAction(t *testing.T) {
	runner, input := deadlineFixtureRunner(t, true, 30*time.Second, 0)
	runner.cfg.RunTimeout = 5 * time.Second
	_, err := runner.ExecuteTask(context.Background(), input, nil)
	var proof *ExternalAttemptError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &proof) || proof.Actions != 1 || proof.Unfinished {
		t.Fatalf("action disabled full-run deadline or lost ledger: %v", err)
	}
}

func TestControlledPreActionBudget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		caller   time.Duration
		reserve  time.Duration
		explicit time.Duration
		want     time.Duration
	}{
		{"no caller deadline", 0, 0, 0, 2 * time.Minute},
		{"normal chat", 35 * time.Minute, 30 * time.Minute, 0, 2 * time.Minute},
		{"reserve native and teardown", 31*time.Minute + 5*time.Second, 30 * time.Minute, 0, time.Minute},
		{"short caller deadline", time.Minute, 0, 0, time.Minute},
		{"long caller deadline", time.Hour, 0, 0, 2 * time.Minute},
		{"explicit pre-action cap", time.Hour, 0, 5 * time.Minute, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.caller > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.caller)
				defer cancel()
			}
			ctx = WithNativeFallbackBudget(ctx, tc.reserve)
			start := time.Now()
			attempt := newControlledAttemptDeadline(ctx, tc.explicit)
			defer attempt.stop()
			window := attempt.preActionDeadline.Sub(start)
			if attempt.timer == nil || window < tc.want-time.Second || window > tc.want+time.Second {
				t.Fatalf("pre-action window=%v timer=%v, want %v", window, attempt.timer != nil, tc.want)
			}
		})
	}
}

func TestControlledDefaultPreActionTimerIsArmed(t *testing.T) {
	attempt := newControlledAttemptDeadline(context.Background(), 0)
	defer attempt.stop()
	if attempt.timer == nil {
		t.Fatal("deadline-less default attempt has no pre-action timer")
	}
}

func TestControlledActionAdmissionTimer(t *testing.T) {
	t.Run("expired admission cannot start", func(t *testing.T) {
		attempt := newControlledAttemptDeadline(context.Background(), time.Minute)
		defer attempt.stop()
		attempt.timer.Stop()
		attempt.preActionDeadline = time.Now().Add(-time.Second)
		started := false
		err := attempt.admitAction(func() error { started = true; return nil })
		if !errors.Is(err, context.DeadlineExceeded) || started || attempt.acting {
			t.Fatalf("late admission: started=%v acting=%v err=%v", started, attempt.acting, err)
		}
	})
	t.Run("failed ledger leaves timer armed", func(t *testing.T) {
		attempt := newControlledAttemptDeadline(context.Background(), 100*time.Millisecond)
		defer attempt.stop()
		ledgerErr := errors.New("cannot persist action start")
		if err := attempt.admitAction(func() error { return ledgerErr }); !errors.Is(err, ledgerErr) || attempt.acting {
			t.Fatalf("failed ledger disarmed timer: acting=%v err=%v", attempt.acting, err)
		}
		select {
		case <-attempt.ctx.Done():
			if !errors.Is(context.Cause(attempt.ctx), context.DeadlineExceeded) {
				t.Fatal(context.Cause(attempt.ctx))
			}
		case <-time.After(5 * time.Second):
			t.Fatal("failed ledger left an unbounded attempt")
		}
	})
	t.Run("admitted action retains caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		attempt := newControlledAttemptDeadline(ctx, time.Minute)
		defer attempt.stop()
		if err := attempt.admitAction(func() error { return nil }); err != nil || !attempt.acting {
			t.Fatalf("admission: acting=%v err=%v", attempt.acting, err)
		}
		want, _ := ctx.Deadline()
		got, ok := attempt.ctx.Deadline()
		if !ok || !got.Equal(want) || attempt.timer.Stop() {
			t.Fatalf("admission changed deadline or left timer armed: got=%v want=%v", got, want)
		}
	})
}

func TestControlledFixtureIgnoresLongTMPDIR(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("temporary-directory-", 6))
	if err := os.MkdirAll(long, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	runner, input := deadlineFixtureRunner(t, false, 0, 0)
	factory := runner.cfg.ToolServerFactory
	runner.cfg.ToolServerFactory = func(ctx context.Context, input *TaskInput, id string, cb StreamCallback) (*toolserver.Server, error) {
		server, err := factory(ctx, input, id, cb)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(server.TicketPath())
		if err != nil {
			t.Fatal(err)
		}
		var ticket struct {
			SocketPath string `json:"socket_path"`
		}
		if err := json.Unmarshal(data, &ticket); err != nil {
			t.Fatal(err)
		}
		if len(ticket.SocketPath) >= 103 {
			t.Fatalf("fixture socket is %d bytes, must fit macOS: %s", len(ticket.SocketPath), ticket.SocketPath)
		}
		t.Logf("fixture socket path: %d bytes", len(ticket.SocketPath))
		return server, nil
	}
	if _, err := runner.ExecuteTask(context.Background(), input, nil); err != nil {
		t.Fatalf("fixture socket must fit macOS even with a long TMPDIR: %v", err)
	}
}

// This measures local transport startup, not an assistant model or its network.
func TestControlledFixtureStartupTiming(t *testing.T) {
	runner, input := deadlineFixtureRunner(t, true, 0, 0)
	for i := 0; i < 10; i++ {
		start := time.Now()
		var firstAction time.Duration
		_, err := runner.ExecuteTask(context.Background(), input, func(event *StreamEvent) error {
			if event.Type == "action_started" && firstAction == 0 {
				firstAction = time.Since(start)
			}
			return nil
		})
		if err != nil || firstAction == 0 {
			t.Fatalf("fixture startup: action=%v err=%v", firstAction, err)
		}
		t.Logf("sample %d: first admitted action %v; complete %v", i+1, firstAction, time.Since(start))
	}
}

// The Python process is a deterministic CLI stream fixture. Its Write request
// uses the real authenticated MCP socket, native gate, file tool and ledger.
func deadlineFixtureRunner(t *testing.T, write bool, delay time.Duration, attemptTimeout time.Duration) (*ExternalAgentRunner, *TaskInput) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("deadline process fixture requires Python 3")
	}
	state := externalFixtureState(t)
	work := realTempDir(t)
	output := filepath.Join(work, "written.txt")
	init := validControlledInit()
	init["tools"] = []string{}
	if write {
		init["tools"] = []string{"mcp__isoform__Write"}
	}
	initJSON, _ := json.Marshal(init)
	outputJSON, _ := json.Marshal(output)
	writeJSON, _ := json.Marshal(write)
	script := fmt.Sprintf(`import json, socket, sys, time
print(%q, flush=True)
if json.loads(%q):
    config = json.load(open(sys.argv[sys.argv.index("--mcp-config") + 1]))
    args = config["mcpServers"]["isoform"]["args"]
    ticket = json.load(open(args[args.index("--ticket-file") + 1]))
    conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    conn.connect(ticket["socket_path"])
    stream = conn.makefile("rwb", buffering=0)
    def send(message):
        stream.write((json.dumps(message) + "\n").encode())
    def receive():
        return json.loads(stream.readline())
    send({"token":open(ticket["token_file"]).read().strip()})
    assert receive()["ok"]
    send({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"deadline-fixture","version":"1"}}})
    assert "result" in receive()
    send({"jsonrpc":"2.0","method":"notifications/initialized"})
    send({"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"Write","arguments":{"path":json.loads(%q),"content":"written"}}})
    result=receive()
    assert "result" in result and not result["result"].get("isError",False), result
    stream.close()
    conn.close()
time.sleep(%g)
print(json.dumps({"type":"result","result":"completed","is_error":False}), flush=True)
`, string(initJSON), string(writeJSON), string(outputJSON), delay.Seconds())
	scriptPath := filepath.Join(state, "fixture.py")
	if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	binary := writeFakeCLI(t, "claude", `exec `+quote(python)+` `+quote(scriptPath)+` "$@"`)
	native := NewNativeAgentRunner(NativeRunnerConfig{DisableEnvFallback: true})
	runner := NewExternalAgentRunner(ExternalAgentConfig{Command: binary, ToolShimCommand: "/bundled/yansu", ToolServerFactory: native.NewToolServer, AttemptTimeout: attemptTimeout})
	runner.profileCheckedAt = time.Now()
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	input := &TaskInput{WorkDir: work, Prompt: "deadline fixture", NoTools: !write, AutoApproveTools: true}
	if write {
		input.AllowedTools = []string{"Write"}
		input.AuthorityGrants = []authority.Grant{{ID: "deadline-write", Capability: "tool.write", ResourcePrefix: work}}
	}
	return runner, input
}

func TestControlledFirstActionKeepsCallerTime(t *testing.T) {
	runner, input := deadlineFixtureRunner(t, true, 6*time.Second, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var actions int
	_, err := runner.ExecuteTask(ctx, input, func(event *StreamEvent) error {
		if event.ActionsStarted > actions {
			actions = event.ActionsStarted
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run stopped after %d actions despite caller time remaining: %v", actions, err)
	}
	if actions != 1 {
		t.Fatalf("actions=%d, want real Write action", actions)
	}
	if content, err := os.ReadFile(filepath.Join(input.WorkDir, "written.txt")); err != nil || string(content) != "written" {
		t.Fatalf("write=%q err=%v", content, err)
	}
}

func TestControlledNoActionLeavesBackupTime(t *testing.T) {
	runner, input := deadlineFixtureRunner(t, false, 30*time.Second, 100*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := runner.ExecuteTask(ctx, input, nil)
	var proof *ExternalAttemptError
	if !errors.As(err, &proof) || !errors.Is(err, context.DeadlineExceeded) || proof.ActionCount() != 0 || proof.HasUnfinishedActions() {
		t.Fatalf("timeout lacks safe fallback proof: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("assistant exhausted the caller deadline before backup could start")
	}
}

func TestControlledProfileCancellationIsNotCached(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "probe-started")
			binary := writeFakeCLI(t, "claude", `if [ ! -f '`+marker+`' ]; then
    : > '`+marker+`'
    sleep 10
fi
case "$1" in
--version) echo '2.1.296 (Claude Code)' ;;
--help) echo '`+strings.Join(controlledClaudeFlags, " ")+`' ;;
esac`)
			runner := NewExternalAgentRunner(ExternalAgentConfig{Command: binary})
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
			}
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- runner.checkControlProfile(ctx) }()
			until := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(until) {
					t.Fatal("probe did not start")
				}
				time.Sleep(time.Millisecond)
			}
			if !deadline {
				cancel()
			}
			if err := <-result; err == nil {
				t.Fatal("cancelled probe succeeded")
			}
			if err := runner.checkControlProfile(context.Background()); err != nil {
				t.Fatalf("healthy next run inherited cancelled probe: %v", err)
			}
		})
	}
}

type observedProfileContext struct {
	context.Context
	once     sync.Once
	observed chan struct{}
}

func (c *observedProfileContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.observed) })
	return err
}

func TestControlledProfileWaitHonorsCancellation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "probe-started")
	binary := writeFakeCLI(t, "claude", `: > '`+marker+`'; sleep 30`)
	runner := NewExternalAgentRunner(ExternalAgentConfig{Command: binary})
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	probeResult := make(chan error, 1)
	go func() { probeResult <- runner.checkControlProfile(probeCtx) }()
	t.Cleanup(func() { cancelProbe(); <-probeResult })
	until := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("first profile probe did not start")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedProfileContext{Context: ctx, observed: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- runner.checkControlProfile(observed) }()
	<-observed.observed
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled probe waiter: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled waiter remained blocked behind the ten-second profile probe")
	}
}

func TestControlledRejectsRelativeShim(t *testing.T) {
	for _, command := range []string{"yansu", "./bin/yansu", "bin/yansu"} {
		runner := NewExternalAgentRunner(ExternalAgentConfig{ToolShimCommand: command, ToolServerFactory: func(context.Context, *TaskInput, string, StreamCallback) (*toolserver.Server, error) {
			panic("not called")
		}})
		if ok, reason := runner.RoutingEligibility(&TaskInput{}); ok {
			t.Errorf("relative shim %q remained eligible (%s)", command, reason)
		}
	}
}

func TestControlledProfileCanceledCallerCannotUseCache(t *testing.T) {
	runner := NewExternalAgentRunner(ExternalAgentConfig{})
	runner.profileCheckedAt = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.checkControlProfile(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller used healthy cached profile: %v", err)
	}
	if err := runner.checkControlProfile(context.Background()); err != nil {
		t.Fatalf("healthy cached profile was replaced: %v", err)
	}
}

func TestControlledRequiresNativeForTokenBudget(t *testing.T) {
	runner := NewExternalAgentRunner(ExternalAgentConfig{ToolShimCommand: "/bundled/yansu", ToolServerFactory: func(context.Context, *TaskInput, string, StreamCallback) (*toolserver.Server, error) {
		panic("not called")
	}})
	for _, budget := range []int{0, 1000} {
		got, reason := runner.RoutingEligibility(&TaskInput{MaxTotalTokens: budget})
		if got != (budget == 0) {
			t.Errorf("token budget=%d eligible=%v reason=%q", budget, got, reason)
		}
	}
}

func TestControlledProfileBudgetFlagIsOptional(t *testing.T) {
	var flags []string
	for _, flag := range controlledClaudeFlags {
		if flag != "--max-budget-usd" {
			flags = append(flags, flag)
		}
	}
	binary := writeFakeCLI(t, "claude", `case "$1" in
--version) echo '2.1.296 (Claude Code)' ;;
--help) echo '`+strings.Join(flags, " ")+`' ;;
esac`)
	for _, budget := range []float64{0, 2.5} {
		err := ExternalControlProfile(context.Background(), ExternalAgentConfig{Command: binary, MaxBudgetUSD: budget})
		if (err != nil) != (budget > 0) {
			t.Errorf("budget=%g profile error=%v", budget, err)
		}
	}
}

func TestControlledCancelRunPreservesCancellationWithPartialJSON(t *testing.T) {
	state := externalFixtureState(t)
	marker := filepath.Join(state, "partial-written")
	init := validControlledInit()
	init["tools"] = []string{}
	initJSON, _ := json.Marshal(init)
	binary := writeFakeCLI(t, "claude", `printf '%s\n' '`+string(initJSON)+`'
printf '%s' '{"type":"result"'
: > '`+marker+`'
sleep 10`)
	native := NewNativeAgentRunner(NativeRunnerConfig{DisableEnvFallback: true})
	runner := NewExternalAgentRunner(ExternalAgentConfig{Command: binary, ToolShimCommand: "/bundled/yansu", ToolServerFactory: native.NewToolServer})
	runner.profileCheckedAt = time.Now()
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	runIDs := make(chan string, 1)
	result := make(chan error, 1)
	go func() {
		_, err := runner.ExecuteTask(context.Background(), &TaskInput{Prompt: "cancel partial stream", NoTools: true}, func(event *StreamEvent) error {
			if event.Type == "run_created" {
				runIDs <- event.RunID
			}
			return nil
		})
		result <- err
	}()
	runID := <-runIDs
	t.Cleanup(func() { _ = runner.CancelRun(runID) })
	until := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("CLI did not emit partial JSON")
		}
		time.Sleep(time.Millisecond)
	}
	if err := runner.CancelRun(runID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CancelRun lost cancellation and permits fallback: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled CLI did not stop")
	}
}

func TestControlledAnswerTextExtendsPreActionWindow(t *testing.T) {
	t.Run("progress keeps a streaming answer alive past the first window", func(t *testing.T) {
		attempt := newControlledAttemptDeadline(context.Background(), 150*time.Millisecond)
		defer attempt.stop()
		for end := time.Now().Add(450 * time.Millisecond); time.Now().Before(end); {
			attempt.noteProgress()
			time.Sleep(30 * time.Millisecond)
		}
		if err := attempt.ctx.Err(); err != nil {
			t.Fatalf("streaming answer cut at the first window: %v", context.Cause(attempt.ctx))
		}
		// Silence after the text still ends the attempt one window later.
		select {
		case <-attempt.ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("a silent assistant was never stopped")
		}
		if !errors.Is(context.Cause(attempt.ctx), context.DeadlineExceeded) {
			t.Fatalf("silence ended with %v, want the pre-action deadline", context.Cause(attempt.ctx))
		}
	})
	t.Run("never passes the native fallback reserve", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute+externalFallbackTeardownAllowance+200*time.Millisecond)
		defer cancel()
		attempt := newControlledAttemptDeadline(WithNativeFallbackBudget(ctx, 30*time.Minute), 2*time.Minute)
		defer attempt.stop()
		end := attempt.windowEnd
		attempt.noteProgress()
		if attempt.preActionDeadline.After(end) {
			t.Fatalf("progress moved the window to %v, past the reserve at %v", attempt.preActionDeadline, end)
		}
	})
	t.Run("an admitted action owns the run deadline", func(t *testing.T) {
		attempt := newControlledAttemptDeadline(context.Background(), time.Minute)
		defer attempt.stop()
		if err := attempt.admitAction(func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		before := attempt.preActionDeadline
		attempt.noteProgress()
		if !attempt.preActionDeadline.Equal(before) {
			t.Fatal("progress moved the window after an action was admitted")
		}
	})
}

func TestControlledLongTextAnswerFinishes(t *testing.T) {
	externalFixtureState(t)
	initJSON, _ := json.Marshal(func() map[string]any { init := validControlledInit(); init["tools"] = []string{}; return init }())
	delta := func(text string) string {
		line, _ := json.Marshal(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": text}}})
		return "echo '" + string(line) + "'; sleep 0.1"
	}
	script := "echo '" + string(initJSON) + "'\n"
	for i := 0; i < 8; i++ {
		script += delta("part ") + "\n"
	}
	script += `echo '{"type":"result","result":"completed","is_error":false}'`
	binary := writeFakeCLI(t, "claude", script)
	native := NewNativeAgentRunner(NativeRunnerConfig{DisableEnvFallback: true})
	runner := NewExternalAgentRunner(ExternalAgentConfig{Command: binary, ToolShimCommand: "/bundled/yansu", ToolServerFactory: native.NewToolServer, AttemptTimeout: 300 * time.Millisecond})
	runner.profileCheckedAt = time.Now()
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	_, err := runner.ExecuteTask(context.Background(), &TaskInput{WorkDir: realTempDir(t), Prompt: "write a long answer", NoTools: true}, func(event *StreamEvent) error {
		if event.Type == "assistant" {
			text.WriteString(event.Content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("an answer streaming for longer than one window was cut off: %v", err)
	}
	if got := strings.Count(text.String(), "part "); got != 8 {
		t.Fatalf("answer kept %d of 8 parts", got)
	}
}
