package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// magpieSidecar runs `magpie serve` as a child process owned by the App. It
// keeps the gateway healthy (health checks, restart with backoff), persists
// the loopback port, and runs the magpie CLI against the same instance so
// bootstrap and quota calls see the same configuration.
//
// Lifecycle mirrors cua_driver_daemon.go EnsureRunning: a supervisor goroutine
// owns the child; Stop sends SIGTERM, waits, then kills.

const (
	// magpieStateDirName sits under ~/.yansu-agent. Its config/ and cache/
	// subdirectories are XDG_CONFIG_HOME and XDG_CACHE_HOME for the child, so
	// Yansu never reads or writes ~/.config/magpie.
	magpieStateDirName = "magpie"
	magpieLogName      = "magpie.log"
	magpiePortFileName = "magpie.json"
	// magpieReservedPort is never chosen: a user's own magpie may hold it.
	magpieReservedPort = 3425
	// magpieLocalAPIKey is the placeholder the native runner sends. Loopback
	// magpie accepts any bearer or x-api-key.
	magpieLocalAPIKey = "yansu-local"
)

// Production timings. Tests shrink them on the magpieSidecar value.
const (
	magpieStartDeadline  = 15 * time.Second
	magpieHealthInterval = 10 * time.Second
	magpieBackoffBase    = time.Second
	magpieBackoffMax     = time.Minute
	magpieRestartWindow  = 10 * time.Minute
	magpieRestartLimit   = 5
	magpieStopGrace      = 5 * time.Second
	magpieProbeTimeout   = 3 * time.Second
)

var errMagpieStateDir = errors.New("magpie state directory is unavailable (no home directory)")

// magpieSidecarSnapshot is the sidecar state the Wails bindings expose.
type magpieSidecarSnapshot struct {
	Available  bool
	Running    bool
	Healthy    bool
	Degraded   bool
	Port       int
	Version    string
	LastError  string
	BinaryPath string
	// External is true when the sidecar fronts the user's own magpie.
	External  bool
	ConfigDir string
}

// magpieChild is one launched `magpie serve` process.
type magpieChild struct {
	cmd    *exec.Cmd
	exited chan struct{} // closed once cmd.Wait returns
}

type magpieSidecar struct {
	// stateDir is ~/.yansu-agent/magpie. Tests point it at a temp directory.
	stateDir string
	// binaryPath pins the binary (tests). Empty means LocateBundledMagpie.
	binaryPath string
	logPath    string

	// external is set by adoptUserInstall when the user has their own magpie:
	// the CLI and the runner talk to that gateway on its own port, nothing
	// overrides its config directory, and a child `magpie serve` is launched
	// only while nothing else (usually the magpie app) answers on that port.
	external  bool
	fixedPort int
	configDir string

	startDeadline  time.Duration
	healthInterval time.Duration
	backoffBase    time.Duration
	backoffMax     time.Duration
	restartWindow  time.Duration
	restartLimit   int
	stopGrace      time.Duration

	// onHealthy runs in its own goroutine on each transition to healthy.
	onHealthy func()

	// bundled lists where this app's own gateway binary may live. Stale
	// gateways are looked for at these paths even when the user's magpie is
	// in use. Nil means magpieBundleCandidates.
	bundled func() []string

	// bootstrapMu serializes Bootstrap runs (see magpie_bootstrap.go).
	bootstrapMu sync.Mutex

	mu        sync.Mutex
	port      int
	version   string
	resolved  string // binary used by Start; the CLI uses the same one
	childPID  int
	healthy   bool
	lastError string
	restarts  []time.Time
	stopCh    chan struct{} // non-nil while supervised
	loopDone  chan struct{} // closed when the supervisor returns
}

// newMagpieSidecar returns an unstarted sidecar. Nothing is launched, so the
// App can create it at construction time.
func newMagpieSidecar(stateDir string) *magpieSidecar {
	s := &magpieSidecar{
		stateDir:       stateDir,
		startDeadline:  magpieStartDeadline,
		healthInterval: magpieHealthInterval,
		backoffBase:    magpieBackoffBase,
		backoffMax:     magpieBackoffMax,
		restartWindow:  magpieRestartWindow,
		restartLimit:   magpieRestartLimit,
		stopGrace:      magpieStopGrace,
	}
	if stateDir != "" {
		// ~/.yansu-agent/logs/magpie.log, next to the state directory.
		s.logPath = filepath.Join(filepath.Dir(stateDir), "logs", magpieLogName)
	}
	return s
}

// adoptUserInstall points an unstarted sidecar at the user's own magpie. It
// reports false when the sidecar is already supervising something, in which
// case the next app start picks the install up.
func (s *magpieSidecar) adoptUserInstall(in magpieInstall) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loopDone != nil {
		return s.external
	}
	if in.BinaryPath == "" || in.Port() == 0 {
		return false
	}
	s.external = true
	s.binaryPath = in.BinaryPath
	s.fixedPort = in.Port()
	s.configDir = in.ConfigDir
	return true
}

// External reports whether the sidecar fronts the user's own magpie.
func (s *magpieSidecar) External() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.external
}

// defaultMagpieStateDir resolves ~/.yansu-agent/magpie, or "" when the home
// directory cannot be determined.
func defaultMagpieStateDir() string {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dataRoot, magpieStateDirName)
}

// Start launches magpie serve under supervision and waits up to
// startDeadline for it to answer. A nil error means healthy. An error with
// the supervisor still running means the child is launched but not yet
// healthy; BaseURL is usable and the supervisor keeps retrying. An error with
// nothing running means the binary or state directory is unavailable.
func (s *magpieSidecar) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.loopDone != nil {
		s.mu.Unlock()
		return s.awaitHealthy(ctx)
	}
	s.mu.Unlock()

	if s.stateDir == "" {
		return s.fail(errMagpieStateDir)
	}
	// Reconcile before resolving or invoking any binary, including version
	// probes. A missing binary must not prevent credential withdrawal.
	if err := s.reconcileByok(); err != nil {
		return s.fail(err)
	}
	bin, err := s.binary()
	if err != nil {
		return s.fail(err)
	}
	if err := s.ensureStateDirs(); err != nil {
		return s.fail(err)
	}
	// Stop gateways an earlier run left behind before picking a port, so the
	// port they held is free to reuse.
	s.reapStaleChild(bin)
	port, err := s.choosePort()
	if err != nil {
		return s.fail(err)
	}
	version := s.probeVersion(ctx, bin, port)

	s.mu.Lock()
	if s.loopDone != nil {
		// Another caller started the sidecar while we were choosing a port.
		s.mu.Unlock()
		return s.awaitHealthy(ctx)
	}
	s.resolved = bin
	s.port = port
	s.version = version
	s.lastError = ""
	stop := make(chan struct{})
	done := make(chan struct{})
	s.stopCh = stop
	s.loopDone = done
	s.mu.Unlock()

	go s.supervise(bin, port, stop, done)
	return s.awaitHealthy(ctx)
}

// Stop ends supervision, sends SIGTERM to the child, waits stopGrace and
// kills it if it is still running. Safe to call when not started.
func (s *magpieSidecar) Stop() {
	s.mu.Lock()
	stop, done := s.stopCh, s.loopDone
	s.stopCh, s.loopDone = nil, nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done

	s.mu.Lock()
	s.healthy = false
	s.childPID = 0
	s.mu.Unlock()
}

// BaseURL returns http://127.0.0.1:<port> while the sidecar is supervised,
// or "" when it is not running. The native runner appends /v1/messages.
func (s *magpieSidecar) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loopDone == nil || s.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

// Healthy reports whether the last health check passed.
func (s *magpieSidecar) Healthy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthy
}

// Status returns the sidecar snapshot for the Wails bindings.
func (s *magpieSidecar) Status() magpieSidecarSnapshot {
	located, locateErr := s.binary()

	s.mu.Lock()
	defer s.mu.Unlock()
	snap := magpieSidecarSnapshot{
		Available: locateErr == nil,
		Running:   s.loopDone != nil,
		Healthy:   s.healthy,
		Degraded:  s.degradedLocked(time.Now()),
		Port:      s.port,
		Version:   s.version,
		LastError: s.lastError,
	}
	snap.BinaryPath = located
	if s.resolved != "" {
		snap.BinaryPath = s.resolved
		snap.Available = true
	}
	snap.External = s.external
	snap.ConfigDir = s.configDir
	return snap
}

// childProcessID returns the pid of the running child, or 0.
func (s *magpieSidecar) childProcessID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.childPID
}

func (s *magpieSidecar) fail(err error) error {
	s.setLastError(err)
	return err
}

func (s *magpieSidecar) setLastError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.lastError = ""
		return
	}
	s.lastError = err.Error()
}

// binary returns the pinned binary, or the located bundled one.
func (s *magpieSidecar) binary() (string, error) {
	if s.binaryPath != "" {
		if isExecutableFile(s.binaryPath) {
			return s.binaryPath, nil
		}
		return "", &MagpieNotFoundError{Searched: []string{s.binaryPath}}
	}
	return LocateBundledMagpie()
}

// ensureStateDirs creates the state, config and cache directories.
func (s *magpieSidecar) ensureStateDirs() error {
	for _, dir := range []string{
		s.stateDir,
		filepath.Join(s.stateDir, "config"),
		filepath.Join(s.stateDir, "cache"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create magpie state dir: %w", err)
		}
	}
	return nil
}

// choosePort returns the persisted loopback port when it is free, and
// otherwise picks a new one and persists it. Never returns magpieReservedPort.
func (s *magpieSidecar) choosePort() (int, error) {
	if s.external && s.fixedPort > 0 {
		// The user's gateway has its own address; nothing is persisted.
		return s.fixedPort, nil
	}
	path := filepath.Join(s.stateDir, magpiePortFileName)
	var record struct {
		Port int `json:"port"`
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &record)
	}
	if record.Port > 0 && record.Port != magpieReservedPort && portFree(record.Port) {
		return record.Port, nil
	}

	port, err := freeLoopbackPort()
	if err != nil {
		return 0, err
	}
	data, err := json.Marshal(map[string]int{"port": port})
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return 0, fmt.Errorf("persist magpie port: %w", err)
	}
	return port, nil
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func freeLoopbackPort() (int, error) {
	for attempt := 0; attempt < 32; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, fmt.Errorf("pick magpie port: %w", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if port != magpieReservedPort {
			return port, nil
		}
	}
	return 0, errors.New("pick magpie port: no free loopback port")
}

// probeVersion runs `magpie version` once per Start. Failure is not fatal:
// the status reports an empty version.
func (s *magpieSidecar) probeVersion(ctx context.Context, bin string, port int) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Env = s.commandEnv(port)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// commandEnv is the environment for `magpie serve` and for every CLI call, so
// both talk to the same instance and state.
func (s *magpieSidecar) commandEnv(port int) []string {
	overrides := []string{
		"MAGPIE_ADDR=" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
	}
	if !s.external {
		// The bundled copy keeps its own state so Isoform never touches
		// ~/.config/magpie; the user's own magpie keeps its own settings.
		overrides = append(overrides,
			"XDG_CONFIG_HOME="+filepath.Join(s.stateDir, "config"),
			"XDG_CACHE_HOME="+filepath.Join(s.stateDir, "cache"),
			"DO_NOT_TRACK=1",
			"MAGPIE_NO_STATS=1",
		)
	}
	keys := make(map[string]bool, len(overrides))
	for _, kv := range overrides {
		keys[kv[:strings.IndexByte(kv, '=')]] = true
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 && keys[kv[:i]] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, overrides...)
}

// withEnvOverrides replaces entries in env by name with overrides.
func withEnvOverrides(env, overrides []string) []string {
	if len(overrides) == 0 {
		return env
	}
	names := make(map[string]bool, len(overrides))
	for _, kv := range overrides {
		if i := strings.IndexByte(kv, '='); i > 0 {
			names[kv[:i]] = true
		}
	}
	out := make([]string, 0, len(env)+len(overrides))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 && names[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, overrides...)
}

// awaitHealthy polls until the gateway answers or startDeadline passes.
func (s *magpieSidecar) awaitHealthy(ctx context.Context) error {
	if s.checkHealth(ctx) {
		return nil
	}
	deadline := time.NewTimer(s.startDeadline)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.fail(ctx.Err())
		case <-deadline.C:
			return s.fail(fmt.Errorf("magpie did not answer within %s", s.startDeadline))
		case <-tick.C:
			if s.checkHealth(ctx) {
				return nil
			}
		}
	}
}

// checkHealth probes GET /v1/models and records the result. The transition
// to healthy triggers onHealthy.
func (s *magpieSidecar) checkHealth(ctx context.Context) bool {
	s.mu.Lock()
	port := s.port
	s.mu.Unlock()
	ok := false
	if port > 0 {
		if status, _, err := s.probe(ctx, port, "/v1/models"); err == nil && status == http.StatusOK {
			ok = true
		}
	}
	s.setHealthy(ok)
	return ok
}

func (s *magpieSidecar) setHealthy(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wasHealthy := s.healthy
	s.healthy = ok
	if ok && !wasHealthy && s.onHealthy != nil {
		go s.onHealthy()
	}
}

// probe performs one GET against the loopback gateway.
func (s *magpieSidecar) probe(ctx context.Context, port int, path string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, magpieProbeTimeout)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("x-api-key", magpieLocalAPIKey)
	req.Header.Set("authorization", "Bearer "+magpieLocalAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// supervise runs the child until stop closes. Each failure (exit or failed
// health check) is followed by a restart after backoff: backoffBase, doubling
// up to backoffMax. A run that lasted restartWindow resets the backoff. Once
// restartLimit restarts fall inside restartWindow the sidecar is degraded and
// retries every backoffMax.
func (s *magpieSidecar) supervise(bin string, port int, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stop
		cancel()
	}()

	backoff := s.backoffBase
	for {
		// The user's own gateway is usually served by their magpie app. While
		// it answers, nothing is launched; a child steps in only when it
		// stops answering.
		if s.external && s.checkHealth(ctx) {
			s.setHealthy(true)
			reason := s.watchExternal(ctx, stop)
			select {
			case <-stop:
				return
			default:
			}
			s.setHealthy(false)
			s.setLastError(errors.New(reason))
			continue
		}
		launchedAt := time.Now()
		child, err := s.launch(bin, port)
		if err != nil {
			s.setLastError(fmt.Errorf("start magpie: %w", err))
		} else {
			var reason string
			if s.waitForChild(ctx, child) {
				reason = s.monitor(ctx, stop, child.exited)
			} else {
				reason = "magpie did not answer its health check after start"
			}
			select {
			case <-stop:
				s.terminate(child)
				return
			default:
			}
			s.kill(child)
			s.setLastError(errors.New(reason))
		}
		s.setHealthy(false)

		select {
		case <-stop:
			return
		default:
		}

		now := time.Now()
		s.recordRestart(now)
		if now.Sub(launchedAt) >= s.restartWindow {
			backoff = s.backoffBase
		}
		wait := backoff
		s.mu.Lock()
		if s.degradedLocked(now) {
			wait = s.backoffMax
		}
		s.mu.Unlock()
		log.Printf("[magpie] restarting gateway in %s", wait)

		select {
		case <-stop:
			return
		case <-time.After(wait):
		}
		backoff *= 2
		if backoff > s.backoffMax {
			backoff = s.backoffMax
		}
	}
}

// waitForChild polls health until the new child answers. It gives up when the
// child exits, the start deadline passes, or ctx ends. Without this poll a
// restarted child would look unhealthy until the first monitor tick.
func (s *magpieSidecar) waitForChild(ctx context.Context, child *magpieChild) bool {
	deadline := time.NewTimer(s.startDeadline)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if s.checkHealth(ctx) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-child.exited:
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

// monitor returns why the child needs replacing, or "stop" once ctx ends.
func (s *magpieSidecar) monitor(ctx context.Context, stop <-chan struct{}, exited <-chan struct{}) string {
	ticker := time.NewTicker(s.healthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return "stopped"
		case <-exited:
			return "magpie process exited"
		case <-ticker.C:
			if !s.checkHealth(ctx) {
				select {
				case <-stop:
					return "stopped"
				default:
				}
				return "magpie stopped answering health checks"
			}
		}
	}
}

// watchExternal polls the user's gateway until it stops answering or the
// sidecar is stopped, and says which.
func (s *magpieSidecar) watchExternal(ctx context.Context, stop <-chan struct{}) string {
	tick := time.NewTicker(s.healthInterval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return "stopped"
		case <-tick.C:
			if !s.checkHealth(ctx) {
				return "the user's magpie gateway stopped answering"
			}
		}
	}
}

// launch starts `magpie serve`, logging its output to the magpie log.
func (s *magpieSidecar) launch(bin string, port int) (*magpieChild, error) {
	// The supervisor can restart without Start being called again.
	if err := s.reconcileByok(); err != nil {
		return nil, err
	}
	logFile, err := s.openLog()
	if err != nil {
		logFile = nil
	}
	cmd := exec.Command(bin, "serve")
	cmd.Env = s.commandEnv(port)
	cmd.Stdin = nil
	setMagpieProcessGroup(cmd)
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, err
	}
	pid := cmd.Process.Pid

	s.mu.Lock()
	s.childPID = pid
	external := s.external
	s.mu.Unlock()
	// A magpie the user installed is not ours to track or stop later.
	if !external {
		if err := s.writeChildRecord(magpieChildRecord{PID: pid, Binary: bin, Port: port}); err != nil {
			log.Printf("[magpie] %v", err)
		}
	}

	// Waiting starts only after the pid is recorded, so a child that exits at
	// once cannot clear it before it is set.
	child := &magpieChild{cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		if logFile != nil {
			_ = logFile.Close()
		}
		s.childExited(pid)
		close(child.exited)
	}()
	log.Printf("[magpie] serve started pid=%d port=%d", pid, port)
	return child, nil
}

func (s *magpieSidecar) openLog() (*os.File, error) {
	if s.logPath == "" {
		return nil, errors.New("no magpie log path")
	}
	if err := os.MkdirAll(filepath.Dir(s.logPath), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// terminate sends SIGTERM, waits stopGrace, then kills. On Windows Signal
// returns an error and the child is killed at once.
func (s *magpieSidecar) terminate(child *magpieChild) {
	if child == nil || child.cmd.Process == nil {
		return
	}
	select {
	case <-child.exited:
		return
	default:
	}
	if err := signalMagpieChild(child.cmd, syscall.SIGTERM); err != nil {
		s.kill(child)
		return
	}
	grace := time.NewTimer(s.stopGrace)
	defer grace.Stop()
	select {
	case <-child.exited:
	case <-grace.C:
		s.kill(child)
	}
}

// kill force-stops a child that is still running and waits for it to exit.
func (s *magpieSidecar) kill(child *magpieChild) {
	if child == nil || child.cmd.Process == nil {
		return
	}
	select {
	case <-child.exited:
		return
	default:
	}
	_ = signalMagpieChild(child.cmd, syscall.SIGKILL)
	select {
	case <-child.exited:
	case <-time.After(s.stopGrace):
	}
}

// childExited forgets a child that has been waited on, so neither the record
// nor KillNow can name a pid the OS may hand to another process.
func (s *magpieSidecar) childExited(pid int) {
	s.mu.Lock()
	if s.childPID == pid {
		s.childPID = 0
	}
	s.mu.Unlock()
	s.clearChildRecord(pid)
}

// KillNow force-kills the running gateway without waiting for it. The
// shutdown watchdog calls it before os.Exit, which would otherwise leave a
// gateway that is slow to stop running with no parent.
func (s *magpieSidecar) KillNow() {
	if pid := s.childProcessID(); pid > 0 {
		killMagpiePID(pid)
	}
}

// recordRestart appends a restart time and drops entries outside the window.
func (s *magpieSidecar) recordRestart(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restarts = append(s.restarts, now)
	s.pruneRestartsLocked(now)
}

func (s *magpieSidecar) pruneRestartsLocked(now time.Time) {
	kept := s.restarts[:0]
	for _, at := range s.restarts {
		if now.Sub(at) < s.restartWindow {
			kept = append(kept, at)
		}
	}
	s.restarts = kept
}

// degradedLocked reports whether restartLimit restarts fall in the window.
func (s *magpieSidecar) degradedLocked(now time.Time) bool {
	s.pruneRestartsLocked(now)
	return len(s.restarts) >= s.restartLimit
}

// runCLI runs a magpie subcommand against this sidecar's instance and
// returns its stdout. The caller passes masked arguments to log; secrets such
// as provider keys are never logged.
func (s *magpieSidecar) runCLI(ctx context.Context, args ...string) ([]byte, error) {
	return s.runCLIEnv(ctx, nil, args...)
}

// runCLIEnv is runCLI with extra environment entries that replace inherited
// ones of the same name.
func (s *magpieSidecar) runCLIEnv(ctx context.Context, extraEnv []string, args ...string) ([]byte, error) {
	// Includes startup migrations and catalog probes that bypass Start.
	// Keep the lock through the command: magpie CLI writes can otherwise
	// publish a provider snapshot concurrently with an Isoform config save.
	magpieByokMu.Lock()
	defer magpieByokMu.Unlock()
	if _, err := reconcileMagpieByokFromConfig(s.stateDir); err != nil {
		return nil, err
	}
	s.mu.Lock()
	bin := s.resolved
	port := s.port
	s.mu.Unlock()
	if bin == "" {
		located, err := s.binary()
		if err != nil {
			return nil, err
		}
		bin = located
	}
	if port == 0 {
		persisted, err := s.persistedPort()
		if err != nil {
			return nil, err
		}
		port = persisted
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = withEnvOverrides(s.commandEnv(port), extraEnv)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	log.Printf("[magpie] cli %s exit=%v", maskMagpieArgs(args), err)
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return stdout.Bytes(), fmt.Errorf("magpie %s: %w: %s", args[0], err, detail)
	}
	return stdout.Bytes(), nil
}

func (s *magpieSidecar) persistedPort() (int, error) {
	data, err := os.ReadFile(filepath.Join(s.stateDir, magpiePortFileName))
	if err != nil {
		return 0, errors.New("magpie is not running")
	}
	var record struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(data, &record); err != nil || record.Port <= 0 {
		return 0, errors.New("magpie is not running")
	}
	return record.Port, nil
}

// maskMagpieArgs hides key=... values in a CLI argument list for logging.
func maskMagpieArgs(args []string) string {
	masked := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, "key=") {
			masked[i] = "key=***"
			continue
		}
		masked[i] = arg
	}
	return strings.Join(masked, " ")
}
