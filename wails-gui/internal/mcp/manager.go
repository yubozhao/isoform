package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// HealthStatus is the manager's view of a server's reachability. It is
// derived from the most recent initialise/list/call attempt and from
// the subprocess exit state — not from a periodic ping.
type HealthStatus int

const (
	// HealthUnknown is the zero value, used while a server is in the
	// middle of being spawned and before initialise has returned.
	HealthUnknown HealthStatus = iota
	// HealthHealthy indicates initialise succeeded and the subprocess
	// is still alive.
	HealthHealthy
	// HealthUnhealthy indicates either the subprocess exited or a
	// recent operation against it returned an error. The manager will
	// not auto-restart in P0; the caller must explicitly Restart.
	HealthUnhealthy
)

func (h HealthStatus) String() string {
	switch h {
	case HealthHealthy:
		return "healthy"
	case HealthUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

const (
	// defaultInitTimeout caps the MCP `initialize` handshake. The
	// previous 5s was tuned for already-installed binaries, but in
	// practice the most common entry points are `npx -y <pkg>`,
	// `bunx <pkg>`, `uvx <pkg>`, and `pipx run <pkg>` — every one of
	// which fetches/extracts the package on a cold cache before the
	// MCP server even starts speaking JSON-RPC on stdin. Three such
	// servers spawning in parallel (sharing the npm cache lock)
	// regularly need >5s for the first launch on this machine; 30s
	// covers cold cache without giving a genuinely-broken server an
	// abusive idle window. Once the package is cached subsequent
	// launches return well under a second, so we're not paying this
	// cost on the steady-state path.
	defaultInitTimeout       = 30 * time.Second
	defaultLogLineCapacity   = 200
	defaultLogLineMaxBytes   = 4 * 1024
	gracefulShutdownInterval = 2 * time.Second
)

// envPassthroughKeys is the denylist-free subset of the parent process
// environment we expose to MCP child processes. Anything not on this
// list (api keys, OAuth tokens, third-party credentials) MUST be set
// explicitly via Spec.Env so we can audit which servers see what.
var envPassthroughKeys = []string{"PATH", "HOME", "TZ", "LANG", "LC_ALL", "USER", "SHELL", "TMPDIR"}

// ServerInfo is a manager-side snapshot of a managed server's runtime
// state, suitable for surfacing through Wails RPC.
type ServerInfo struct {
	ID        string
	Health    HealthStatus
	LastError string
	Tools     []mcp.Tool
	StartedAt time.Time
}

// ExitReason classifies why a managed subprocess stopped running.
// Callers (notably the App-level bridge) consume this in exit
// callbacks to distinguish "user pulled the plug" from "server
// crashed" so they can suppress duplicate cleanup.
type ExitReason int

const (
	// ExitUnexpected means the subprocess died without a Shutdown
	// being initiated by the manager — i.e. it crashed or exited
	// on its own. The bridge should unregister tools so the model
	// stops seeing them.
	ExitUnexpected ExitReason = iota
	// ExitIntentional means Shutdown / ShutdownAll initiated the
	// teardown. Bridge cleanup is the caller's responsibility and
	// the manager skips its own callback firing.
	ExitIntentional
)

// ExitCallback is invoked once per server exit (regardless of reason)
// AFTER the subprocess has been reaped. It runs on a manager-internal
// goroutine; callbacks must not block on Manager methods or they will
// self-deadlock.
type ExitCallback func(serverID string, reason ExitReason, exitErr error)

// Manager owns the lifecycle of MCP server subprocesses. It is the
// single place the rest of the GUI talks to when it wants to spawn,
// kill, query, or invoke a tool against a managed server.
type Manager struct {
	mu sync.Mutex
	// clients is keyed by Spec.ID. The map is replaced wholesale by
	// ShutdownAll so cancellation of in-flight goroutines doesn't race
	// with new spawns.
	clients map[string]*managedClient

	initTimeout    time.Duration
	logCapacity    int
	logLineMaxByte int

	exitCallback ExitCallback

	// commandFactory lets tests substitute exec.Command with a
	// helper-process fake. Production code leaves this nil. The factory
	// must NOT bind the subprocess's lifetime to a caller context — the
	// manager owns the subprocess for as long as it is registered, and
	// drives shutdown explicitly via Process.Signal/Kill.
	commandFactory func(command string, args []string) *exec.Cmd
}

// Option mutates a Manager during construction.
type Option func(*Manager)

// WithInitTimeout overrides the timeout applied to the client.Initialize
// handshake. Servers that fail to negotiate within the timeout are
// marked unhealthy and their subprocess is reaped.
func WithInitTimeout(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.initTimeout = d
		}
	}
}

// WithLogCapacity overrides the per-server stderr ring-buffer line cap.
func WithLogCapacity(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.logCapacity = n
		}
	}
}

// WithCommandFactory overrides exec.Command for tests. The factory is
// responsible only for constructing the *exec.Cmd; the manager still
// attaches stdin/stdout/stderr pipes and starts it. The factory must
// not bind the cmd to any external ctx.
func WithCommandFactory(f func(command string, args []string) *exec.Cmd) Option {
	return func(m *Manager) { m.commandFactory = f }
}

// WithExitCallback installs a callback fired exactly once per server
// after its subprocess has been reaped. The callback runs on a
// manager-internal goroutine and MUST NOT call back into Manager
// methods that take m.mu (Health/Tools/Info/Shutdown/etc.) or it will
// self-deadlock. Bridge unregister is the canonical use.
func WithExitCallback(cb ExitCallback) Option {
	return func(m *Manager) { m.exitCallback = cb }
}

// New returns a Manager with no managed servers.
func New(opts ...Option) *Manager {
	m := &Manager{
		clients:        map[string]*managedClient{},
		initTimeout:    defaultInitTimeout,
		logCapacity:    defaultLogLineCapacity,
		logLineMaxByte: defaultLogLineMaxBytes,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// managedClient is the per-server bookkeeping. It is created in Spawn,
// retired in Shutdown, and never mutated concurrently — every read or
// write happens under Manager.mu (or via the atomic intentional flag).
type managedClient struct {
	spec      Spec
	cmd       *exec.Cmd
	client    *mcpclient.Client
	transport *transport.Stdio

	startedAt time.Time
	health    HealthStatus
	lastError error
	tools     []mcp.Tool

	logRing *lineRing

	// stderrCloser is a handle to the stderr pipe so Shutdown can
	// release it after the capture goroutine exits. The capture
	// goroutine itself signals completion via stderrDone.
	stderrCloser io.Closer
	stderrDone   chan struct{}

	// done is closed exactly once after cmd.Wait() returns. Multiple
	// consumers (watchExit, shutdownClient, future health checks) can
	// safely <-done without contending — channel close broadcasts to
	// every waiter, unlike the previous single-buffered cmdExitCh which
	// could only be drained by whichever goroutine read it first.
	done chan struct{}

	// exitErr is the error returned by cmd.Wait(), populated under
	// Manager.mu just before done is closed. Read only after done is
	// observed closed.
	exitErr error

	// intentional is set to true by shutdownClient before signalling
	// the process so watchExit knows the exit was caller-driven and
	// can skip the unhealthy-flip + exit callback. Atomic so we don't
	// have to take m.mu on the shutdown path before signalling.
	intentional atomic.Bool
}

// Spawn launches a new MCP server subprocess and runs the initialize
// handshake. On success the server is registered under spec.ID and
// becomes available to Tools / CallTool. Spawning a spec with an ID
// that already exists returns an error — callers should Shutdown first.
func (m *Manager) Spawn(ctx context.Context, spec Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	if _, exists := m.clients[spec.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("mcp server %q already managed", spec.ID)
	}
	// Reserve the slot so two concurrent Spawns of the same id can't
	// both reach the spawn step.
	m.clients[spec.ID] = &managedClient{
		spec:    spec,
		health:  HealthUnknown,
		logRing: newLineRing(m.logCapacity),
	}
	m.mu.Unlock()

	if err := m.startManagedClient(ctx, spec); err != nil {
		// Roll back the reservation so Spawn can be retried after the
		// caller addresses the failure.
		m.mu.Lock()
		delete(m.clients, spec.ID)
		m.mu.Unlock()
		return err
	}
	return nil
}

func (m *Manager) startManagedClient(ctx context.Context, spec Spec) error {
	switch spec.transport() {
	case TransportStdio:
		return m.startStdioManagedClient(ctx, spec)
	case TransportStreamableHTTP:
		return m.startStreamableHTTPManagedClient(ctx, spec)
	default:
		return fmt.Errorf("mcp %q: unsupported transport %q", spec.ID, spec.Transport)
	}
}

//isoform:destination mcp_servers
func (m *Manager) startStdioManagedClient(ctx context.Context, spec Spec) error {
	cmd := m.buildCommand(spec)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp %q: stdin pipe: %w", spec.ID, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("mcp %q: stdout pipe: %w", spec.ID, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("mcp %q: stderr pipe: %w", spec.ID, err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stderr.Close()
		return fmt.Errorf("mcp %q: spawn: %w", spec.ID, err)
	}

	// Capture stderr into the ring buffer for the lifetime of the
	// process. The pipe returns EOF once the child closes its stderr
	// (typically at exit), at which point the goroutine returns.
	stderrDone := make(chan struct{})
	logRing := m.lockedLogRing(spec.ID)
	go func() {
		defer close(stderrDone)
		captureLines(stderr, logRing, m.logLineMaxByte)
	}()

	// Wait for the process to exit in a separate goroutine. The
	// goroutine writes exitErr under m.mu, then closes done so any
	// number of consumers (watchExit, shutdownClient, ...) can wait on
	// done without contending — channel close is a broadcast.
	done := make(chan struct{})
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		if mc, ok := m.clients[spec.ID]; ok && mc.cmd == cmd {
			mc.exitErr = err
		}
		m.mu.Unlock()
		close(done)
	}()

	stdio := transport.NewIO(stdout, stdin, io.NopCloser(emptyReader{}))
	c := mcpclient.NewClient(stdio)

	// transport.NewIO already has the pipes; Start kicks off the JSON-RPC
	// reader goroutine. It does not spawn anything because cmd is empty
	// inside this transport.
	if err := c.Start(ctx); err != nil {
		_ = cmd.Process.Kill()
		<-done
		return fmt.Errorf("mcp %q: client start: %w", spec.ID, err)
	}

	initCtx, cancel := context.WithTimeout(ctx, m.initTimeout)
	defer cancel()
	initReq := mcp.InitializeRequest{}
	initReq.Params.ClientInfo = mcp.Implementation{Name: "yansu-desktop", Version: "0"}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(initCtx, initReq); err != nil {
		_ = c.Close()
		_ = cmd.Process.Kill()
		<-done
		return fmt.Errorf("mcp %q: initialize: %w", spec.ID, err)
	}

	// Initial tools/list — failures here are non-fatal in case the
	// server only implements resources/prompts. The bridge in commit 4
	// will re-run this on demand.
	var tools []mcp.Tool
	listRes, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err == nil && listRes != nil {
		tools = listRes.Tools
	}

	m.mu.Lock()
	mc := m.clients[spec.ID]
	mc.cmd = cmd
	mc.client = c
	mc.transport = stdio
	mc.startedAt = time.Now()
	mc.health = HealthHealthy
	mc.lastError = nil
	mc.tools = tools
	mc.stderrCloser = stderr
	mc.stderrDone = stderrDone
	mc.done = done
	m.mu.Unlock()

	// Watch for unexpected process exit and flip health. We don't
	// auto-restart; the user has to explicitly Restart in P0.
	go m.watchExit(spec.ID, mc)
	return nil
}

//isoform:destination mcp_servers
func (m *Manager) startStreamableHTTPManagedClient(ctx context.Context, spec Spec) error {
	var opts []transport.StreamableHTTPCOption
	if len(spec.Headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(spec.Headers))
	}
	c, err := mcpclient.NewStreamableHttpClient(spec.URL, opts...)
	if err != nil {
		return fmt.Errorf("mcp %q: streamable http client: %w", spec.ID, err)
	}
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return fmt.Errorf("mcp %q: client start: %w", spec.ID, err)
	}

	initCtx, cancel := context.WithTimeout(ctx, m.initTimeout)
	defer cancel()
	initReq := mcp.InitializeRequest{}
	initReq.Params.ClientInfo = mcp.Implementation{Name: "yansu-desktop", Version: "0"}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(initCtx, initReq); err != nil {
		_ = c.Close()
		return fmt.Errorf("mcp %q: initialize: %w", spec.ID, err)
	}

	var tools []mcp.Tool
	listRes, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err == nil && listRes != nil {
		tools = listRes.Tools
	}

	m.mu.Lock()
	mc := m.clients[spec.ID]
	mc.client = c
	mc.startedAt = time.Now()
	mc.health = HealthHealthy
	mc.lastError = nil
	mc.tools = tools
	m.mu.Unlock()
	return nil
}

func (m *Manager) lockedLogRing(id string) *lineRing {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mc, ok := m.clients[id]; ok && mc.logRing != nil {
		return mc.logRing
	}
	return newLineRing(m.logCapacity)
}

func (m *Manager) buildCommand(spec Spec) *exec.Cmd {
	if m.commandFactory != nil {
		cmd := m.commandFactory(spec.Command, spec.Args)
		if cmd.Env == nil {
			cmd.Env = append(filteredParentEnv(), spec.envSlice()...)
		}
		if spec.WorkDir != "" && cmd.Dir == "" {
			cmd.Dir = spec.WorkDir
		}
		return cmd
	}
	// Use exec.Command (not CommandContext): the subprocess lifetime
	// is owned by the manager, not by any caller's ctx. Caller ctx
	// only governs the initialise handshake timeout.
	cmd := exec.Command(spec.Command, spec.Args...)
	if spec.WorkDir != "" {
		cmd.Dir = spec.WorkDir
	}
	cmd.Env = append(filteredParentEnv(), spec.envSlice()...)
	return cmd
}

func filteredParentEnv() []string {
	out := make([]string, 0, len(envPassthroughKeys))
	for _, key := range envPassthroughKeys {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	return out
}

// watchExit waits for the managed subprocess to exit and decides what
// to do about it. It receives a *managedClient so it observes the same
// done/exitErr/intentional fields the rest of the manager wrote — and
// so a stale id lookup after Shutdown's delete cannot mask a callback.
//
// Only an *unexpected* exit (intentional flag false at exit time) flips
// health to unhealthy, logs, and fires the configured exit callback.
// Intentional exits (Shutdown / ShutdownAll / Restart's tear-down step)
// are silent — the caller already knows the process is gone and is
// responsible for any registry cleanup before issuing the kill.
func (m *Manager) watchExit(id string, mc *managedClient) {
	<-mc.done
	if mc.intentional.Load() {
		return
	}

	m.mu.Lock()
	exitErr := mc.exitErr
	// Only flip if the entry hasn't already been replaced (e.g. Restart
	// raced and a new managedClient is now under the same id).
	if cur, ok := m.clients[id]; ok && cur == mc {
		cur.health = HealthUnhealthy
		if exitErr != nil {
			cur.lastError = fmt.Errorf("subprocess exited: %w", exitErr)
		} else {
			cur.lastError = errors.New("subprocess exited cleanly")
		}
		log.Printf("[mcp] server %q exited unexpectedly: %v", id, cur.lastError)
	}
	cb := m.exitCallback
	m.mu.Unlock()

	if cb != nil {
		// Fire outside the lock: the bridge may call back into manager
		// methods that themselves take m.mu (e.g. to look up tools for
		// re-registration on a sibling server).
		cb(id, ExitUnexpected, exitErr)
	}
}

// Shutdown gracefully terminates a single managed server. It sends
// SIGTERM, waits up to gracefulShutdownInterval, then SIGKILLs if the
// process is still alive. The server is removed from the registry on
// success so a subsequent Spawn(spec) re-creates it from scratch.
func (m *Manager) Shutdown(ctx context.Context, id string) error {
	m.mu.Lock()
	mc, ok := m.clients[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("mcp server %q not managed", id)
	}
	delete(m.clients, id)
	m.mu.Unlock()
	return shutdownClient(ctx, mc)
}

// ShutdownAll terminates every managed server. It is safe to call
// during application shutdown; the per-server timeout still applies.
func (m *Manager) ShutdownAll(ctx context.Context) {
	m.mu.Lock()
	clients := m.clients
	m.clients = map[string]*managedClient{}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for id, mc := range clients {
		wg.Add(1)
		go func(id string, mc *managedClient) {
			defer wg.Done()
			if err := shutdownClient(ctx, mc); err != nil {
				log.Printf("[mcp] shutdown %q: %v", id, err)
			}
		}(id, mc)
	}
	wg.Wait()
}

func shutdownClient(ctx context.Context, mc *managedClient) error {
	if mc == nil {
		return nil
	}
	if mc.cmd == nil || mc.cmd.Process == nil {
		if mc.client != nil {
			_ = mc.client.Close()
		}
		return nil
	}

	// Mark the exit as intentional BEFORE signalling so watchExit's
	// observation of done is guaranteed to see the flag set. Without
	// this ordering watchExit could race the signal-induced exit and
	// fire ExitUnexpected for a shutdown the caller initiated.
	mc.intentional.Store(true)
	if mc.client != nil {
		_ = mc.client.Close()
	}

	// Phase 1: SIGTERM. Real MCP servers built on mcp-go's ServeStdio
	// install a signal handler but their stdin reader still blocks on
	// syscall read until SIGKILL or EOF. We use SIGTERM as a
	// best-effort hint and follow up quickly.
	_ = mc.cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(gracefulShutdownInterval)
	defer timer.Stop()
	select {
	case <-mc.done:
		drainWithTimeout(mc.stderrDone, time.Second)
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}

	// Phase 2: SIGKILL — guarantees the kernel closes the child's
	// stdout/stderr pipes, which lets cmd.Wait() and our stderr
	// capture goroutine return.
	_ = mc.cmd.Process.Kill()
	drainWithTimeout(mc.done, 5*time.Second)
	drainWithTimeout(mc.stderrDone, time.Second)
	return nil
}

// drainWithTimeout waits for ch to close OR for d to elapse, whichever
// comes first. We use this in shutdown paths because hanging on a
// goroutine that for some reason never exits would deadlock the whole
// app shutdown — which is worse than leaving one zombie pipe behind.
func drainWithTimeout[T any](ch <-chan T, d time.Duration) {
	if ch == nil {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
	}
}

// Restart shuts down (if running) and re-spawns spec. Any caller-side
// caches (e.g. tool registry adapters) should be rebuilt afterwards.
func (m *Manager) Restart(ctx context.Context, spec Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	_ = m.Shutdown(ctx, spec.ID)
	return m.Spawn(ctx, spec)
}

// Health returns the most recent health snapshot for id. Returns an
// error if id is not managed.
func (m *Manager) Health(id string) (HealthStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mc, ok := m.clients[id]
	if !ok {
		return HealthUnknown, fmt.Errorf("mcp server %q not managed", id)
	}
	return mc.health, nil
}

// Tools returns the cached tool list for id (the snapshot taken at
// initialise time; the bridge layer is responsible for refreshing).
func (m *Manager) Tools(id string) ([]mcp.Tool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mc, ok := m.clients[id]
	if !ok {
		return nil, fmt.Errorf("mcp server %q not managed", id)
	}
	out := make([]mcp.Tool, len(mc.tools))
	copy(out, mc.tools)
	return out, nil
}

// RefreshTools re-fetches tools/list and updates the cached snapshot.
// Returns the new list.
func (m *Manager) RefreshTools(ctx context.Context, id string) ([]mcp.Tool, error) {
	m.mu.Lock()
	mc, ok := m.clients[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("mcp server %q not managed", id)
	}
	res, err := mc.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		m.recordCallError(id, err)
		return nil, err
	}
	m.mu.Lock()
	mc.tools = res.Tools
	m.mu.Unlock()
	return res.Tools, nil
}

// CallTool invokes name on the named server. Errors are reflected in
// the returned error AND in the server's lastError, but they do NOT
// flip health to unhealthy unless the subprocess has actually exited
// (callers are expected to surface is_error tool_results to the model).
func (m *Manager) CallTool(ctx context.Context, id, name string, args map[string]any) (*mcp.CallToolResult, error) {
	m.mu.Lock()
	mc, ok := m.clients[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("mcp server %q not managed", id)
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := mc.client.CallTool(ctx, req)
	if err != nil {
		m.recordCallError(id, err)
		return nil, err
	}
	return res, nil
}

func (m *Manager) recordCallError(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mc, ok := m.clients[id]; ok {
		mc.lastError = err
	}
}

// Logs returns the most recent up-to-n stderr lines for id (oldest
// first). n<=0 returns whatever the ring currently holds.
func (m *Manager) Logs(id string, n int) ([]string, error) {
	m.mu.Lock()
	mc, ok := m.clients[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("mcp server %q not managed", id)
	}
	return mc.logRing.Snapshot(n), nil
}

// Info returns a concurrent-safe snapshot of every managed server,
// sorted by id, suitable for the settings UI.
func (m *Manager) Info() []ServerInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.clients))
	for id := range m.clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ServerInfo, 0, len(ids))
	for _, id := range ids {
		mc := m.clients[id]
		info := ServerInfo{
			ID:        id,
			Health:    mc.health,
			StartedAt: mc.startedAt,
		}
		if mc.lastError != nil {
			info.LastError = mc.lastError.Error()
		}
		info.Tools = make([]mcp.Tool, len(mc.tools))
		copy(info.Tools, mc.tools)
		out = append(out, info)
	}
	return out
}

// emptyReader satisfies io.Reader for the transport.NewIO logging arg
// (which we don't use because we capture stderr ourselves).
type emptyReader struct{}

func (emptyReader) Read(p []byte) (int, error) { return 0, io.EOF }
