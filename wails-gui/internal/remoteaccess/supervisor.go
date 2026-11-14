package remoteaccess

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	StateOff        State = "off"
	StateStarting   State = "starting"
	StateReady      State = "ready"
	StateRestarting State = "restarting"
	StateFailed     State = "failed"

	maxChildLogBytes int64 = 4 << 20
)

var ticketPattern = regexp.MustCompile(`\bendpoint[a-z2-7]{20,}\b`)

// redactedTicket stands in for an endpoint ticket in every log line.
const redactedTicket = "endpoint[REDACTED]"

type State string

type Snapshot struct {
	State     State  `json:"state"`
	RelayURL  string `json:"relayUrl,omitempty"`
	Ticket    string `json:"ticket,omitempty"`
	LastError string `json:"lastError,omitempty"`
	Restarts  int    `json:"restarts"`
}

type Config struct {
	RelayBin               string
	DumbpipeBin            string
	StateDir               string
	PushGatewayURL         string
	PushGatewayEnvironment string
	PushGatewayHubID       string
	PushGatewayHubPub      string
	PushGatewayHubKey      string
	Backoff                func(attempt int) time.Duration
	Logf                   func(format string, args ...any)
	OnChange               func()
}

type nodeKey struct {
	Version   int    `json:"version"`
	Secret    string `json:"secret"`
	CreatedAt string `json:"createdAt"`
}

type Supervisor struct {
	cfg Config

	// lifecycle serializes Start and Stop; acquire it before mu.
	lifecycle     sync.Mutex
	mu            sync.Mutex
	snapshot      Snapshot
	changed       chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	started       bool
	stopped       bool
	port          int
	secret        string
	relayHealthy  bool
	relayRunning  bool
	dumbpipeRun   bool
	readySince    time.Time
	processes     map[string]*exec.Cmd
	resetDumbpipe chan struct{}
	wg            sync.WaitGroup
}

func New(cfg Config) *Supervisor {
	return &Supervisor{
		cfg:           cfg,
		snapshot:      Snapshot{State: StateOff},
		changed:       make(chan struct{}),
		processes:     make(map[string]*exec.Cmd),
		resetDumbpipe: make(chan struct{}, 1),
	}
}

func (s *Supervisor) Start(ctx context.Context) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started && !s.stopped {
		return nil
	}
	if s.cfg.StateDir == "" {
		return s.failLocked(errors.New("remote access state directory is required"))
	}
	for label, path := range map[string]string{"relay": s.cfg.RelayBin, "dumbpipe": s.cfg.DumbpipeBin} {
		if err := executable(path); err != nil {
			return s.failLocked(fmt.Errorf("%s binary: %w", label, err))
		}
	}
	if err := os.MkdirAll(s.cfg.StateDir, 0700); err != nil {
		return s.failLocked(fmt.Errorf("create remote access state directory: %w", err))
	}
	if err := os.Chmod(s.cfg.StateDir, 0700); err != nil {
		return s.failLocked(fmt.Errorf("secure remote access state directory: %w", err))
	}
	if err := os.MkdirAll(filepath.Join(s.cfg.StateDir, "relay"), 0700); err != nil {
		return s.failLocked(fmt.Errorf("create relay state directory: %w", err))
	}
	if err := os.Chmod(filepath.Join(s.cfg.StateDir, "relay"), 0700); err != nil {
		return s.failLocked(fmt.Errorf("secure relay state directory: %w", err))
	}
	key, err := loadOrCreateKey(s.keyPath())
	if err != nil {
		return s.failLocked(err)
	}
	port, err := loadOrChoosePort(filepath.Join(s.cfg.StateDir, "relay-port"))
	if err != nil {
		return s.failLocked(err)
	}
	s.port, s.secret = port, key.Secret
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.started, s.stopped = true, false
	s.relayHealthy, s.relayRunning, s.dumbpipeRun = false, false, false
	s.readySince = time.Time{}
	s.snapshot = Snapshot{State: StateStarting, RelayURL: s.relayURL()}
	s.signalLocked()
	s.wg.Add(3)
	go s.runRelay()
	go s.runDumbpipe()
	go s.monitorRelay()
	return nil
}

func (s *Supervisor) WaitReady(ctx context.Context) (Snapshot, error) {
	for {
		s.mu.Lock()
		snap, changed := s.snapshot, s.changed
		s.mu.Unlock()
		switch snap.State {
		case StateReady:
			return snap, nil
		case StateFailed:
			return snap, errors.New(snap.LastError)
		case StateOff:
			return snap, errors.New("remote access is stopped")
		}
		select {
		case <-ctx.Done():
			return snap, ctx.Err()
		case <-changed:
		}
	}
}

func (s *Supervisor) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

func (s *Supervisor) Reset(ctx context.Context) (Snapshot, error) {
	s.mu.Lock()
	if !s.started || s.stopped {
		s.mu.Unlock()
		return s.Snapshot(), errors.New("remote access is not running")
	}
	oldTicket := s.snapshot.Ticket
	key, err := createKey()
	if err == nil {
		err = writeKeyAtomic(s.keyPath(), key)
	}
	if err != nil {
		s.mu.Unlock()
		return s.Snapshot(), err
	}
	s.secret = key.Secret
	s.snapshot.Ticket = ""
	s.snapshot.State, s.snapshot.LastError = StateStarting, ""
	s.signalLocked()
	select {
	case s.resetDumbpipe <- struct{}{}:
	default:
	}
	s.mu.Unlock()

	for {
		s.mu.Lock()
		snap, changed := s.snapshot, s.changed
		s.mu.Unlock()
		if snap.State == StateReady && snap.Ticket != "" && snap.Ticket != oldTicket {
			return snap, nil
		}
		if snap.State == StateFailed {
			return snap, errors.New(snap.LastError)
		}
		select {
		case <-ctx.Done():
			return snap, ctx.Err()
		case <-changed:
		}
	}
}

func (s *Supervisor) Stop() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	s.mu.Lock()
	if !s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cancel := s.cancel
	s.mu.Unlock()
	cancel()
	s.wg.Wait()
	s.mu.Lock()
	s.relayHealthy, s.relayRunning, s.dumbpipeRun = false, false, false
	s.readySince = time.Time{}
	s.snapshot = Snapshot{State: StateOff, RelayURL: s.relayURL(), Restarts: s.snapshot.Restarts}
	s.signalLocked()
	s.mu.Unlock()
}

func (s *Supervisor) runRelay() {
	defer s.wg.Done()
	s.runChild("relay", func() (*exec.Cmd, error) {
		cmd := exec.Command(s.cfg.RelayBin)
		cmd.Env = relayEnvironment(s.cfg, s.relayURL(), filepath.Join(s.cfg.StateDir, "relay"))
		return cmd, nil
	}, nil)
}

func (s *Supervisor) runDumbpipe() {
	defer s.wg.Done()
	s.runChild("dumbpipe", func() (*exec.Cmd, error) {
		s.mu.Lock()
		secret := s.secret
		port := s.port
		s.mu.Unlock()
		cmd := exec.Command(s.cfg.DumbpipeBin, "listen-tcp", "--host", fmt.Sprintf("127.0.0.1:%d", port))
		cmd.Env = selectedEnvironment()
		cmd.Env = append(cmd.Env, "IROH_SECRET="+secret, "NO_COLOR=1")
		return cmd, nil
	}, s.resetDumbpipe)
}

type childFactory func() (*exec.Cmd, error)

func (s *Supervisor) runChild(name string, factory childFactory, reset <-chan struct{}) {
	attempt := 0
	for {
		if s.ctx.Err() != nil {
			return
		}
		cmd, err := factory()
		if err != nil {
			s.failRunning(fmt.Errorf("start %s: %w", name, err))
			return
		}
		outputDone, logFile, err := s.startChild(name, cmd)
		if err != nil {
			s.failRunning(fmt.Errorf("start %s: %w", name, err))
			return
		}
		wait := make(chan error, 1)
		childDone := make(chan struct{})
		go func() {
			err := cmd.Wait()
			<-outputDone
			_ = logFile.Close()
			wait <- err
			close(childDone)
		}()
		s.setRunning(name, cmd, true)

		var resetRequested bool
		select {
		case err = <-wait:
		case <-s.ctx.Done():
			terminateProcessGroup(cmd, childDone, 5*time.Second)
			<-outputDone
			s.setRunning(name, cmd, false)
			return
		case <-reset:
			resetRequested = true
			terminateProcessGroup(cmd, childDone, 5*time.Second)
			err = <-wait
		}
		<-outputDone
		s.mu.Lock()
		healthyLongEnough := !s.readySince.IsZero() && time.Since(s.readySince) >= 5*time.Minute
		s.mu.Unlock()
		s.setRunning(name, cmd, false)
		if s.ctx.Err() != nil {
			return
		}
		if healthyLongEnough {
			attempt = 0
		}
		if !resetRequested {
			s.incrementRestarts()
			s.setError(fmt.Errorf("%s exited: %w", name, err))
		}
		delay := s.backoff(attempt)
		attempt++
		s.setRestarting()
		timer := time.NewTimer(delay)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Supervisor) startChild(name string, cmd *exec.Cmd) (<-chan struct{}, *os.File, error) {
	logPath := filepath.Join(s.cfg.StateDir, name+".log")
	logFile, logWriter, err := openCappedLog(logPath)
	if err != nil {
		return nil, nil, err
	}
	cmd.Stdout = logWriter
	if name == "dumbpipe" {
		cmd.Stdout = ticketRedactingWriter{writer: logWriter}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = logFile.Close()
		return nil, nil, err
	}
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, nil, err
	}
	s.mu.Lock()
	s.processes[name] = cmd
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if name == "dumbpipe" {
				s.mu.Lock()
				secret := s.secret
				s.mu.Unlock()
				line = strings.ReplaceAll(line, secret, "[REDACTED]")
				if ticket := ticketPattern.FindString(line); ticket != "" {
					s.mu.Lock()
					s.snapshot.Ticket = ticket
					s.refreshStateLocked()
					s.signalLocked()
					s.mu.Unlock()
				}
				// The ticket reaches this computer's relay from anywhere. It goes
				// to the phone in the sealed pairing and /remote-access replies,
				// never into a log.
				line = ticketPattern.ReplaceAllString(line, redactedTicket)
			}
			_, _ = io.WriteString(logWriter, line+"\n")
			s.logLine(name, line)
		}
	}()
	return done, logFile, nil
}

// ticketRedactingWriter keeps an endpoint ticket that dumbpipe prints on
// stdout out of its log file. A write that splits a ticket is not redacted;
// dumbpipe prints the ticket as one line.
type ticketRedactingWriter struct {
	writer io.Writer
}

func (w ticketRedactingWriter) Write(p []byte) (int, error) {
	if _, err := w.writer.Write(ticketPattern.ReplaceAll(p, []byte(redactedTicket))); err != nil {
		return 0, err
	}
	return len(p), nil
}

type cappedWriter struct {
	mu        sync.Mutex
	writer    io.Writer
	remaining int64
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.remaining <= 0 {
		return len(p), nil
	}
	writeLen := len(p)
	if int64(writeLen) > w.remaining {
		writeLen = int(w.remaining)
	}
	n, err := w.writer.Write(p[:writeLen])
	w.remaining -= int64(n)
	if err != nil || n != writeLen {
		w.remaining = 0
	}
	return len(p), nil
}

func openCappedLog(path string) (*os.File, *cappedWriter, error) {
	info, err := os.Stat(path)
	if err == nil && info.Size() >= maxChildLogBytes {
		backupPath := path + ".1"
		if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		if err := os.Rename(path, backupPath); err != nil {
			return nil, nil, err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}

	logFile, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	info, err = logFile.Stat()
	if err != nil {
		_ = logFile.Close()
		return nil, nil, err
	}
	remaining := maxChildLogBytes - info.Size()
	if remaining < 0 {
		remaining = 0
	}
	return logFile, &cappedWriter{writer: logFile, remaining: remaining}, nil
}

func (s *Supervisor) monitorRelay() {
	defer s.wg.Done()
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			resp, err := client.Get(s.relayURL() + "/v1/health")
			healthy := err == nil && resp.StatusCode == http.StatusOK
			if resp != nil {
				_ = resp.Body.Close()
			}
			s.mu.Lock()
			changed := s.relayHealthy != healthy
			s.relayHealthy = healthy
			s.refreshStateLocked()
			if changed {
				s.signalLocked()
			}
			s.mu.Unlock()
			if healthy {
				timer.Reset(5 * time.Second)
			} else {
				timer.Reset(250 * time.Millisecond)
			}
		}
	}
}

func (s *Supervisor) setRunning(name string, cmd *exec.Cmd, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processes[name] == cmd {
		delete(s.processes, name)
	}
	if name == "relay" {
		s.relayRunning = running
	} else {
		s.dumbpipeRun = running
		if !running {
			s.snapshot.Ticket = ""
		}
	}
	s.refreshStateLocked()
	s.signalLocked()
}

func (s *Supervisor) setRestarting() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped && s.snapshot.State != StateFailed {
		s.snapshot.State = StateRestarting
		s.signalLocked()
	}
}

func (s *Supervisor) incrementRestarts() {
	s.mu.Lock()
	s.snapshot.Restarts++
	s.mu.Unlock()
}

func (s *Supervisor) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil && s.ctx.Err() == nil {
		s.snapshot.LastError = err.Error()
		s.refreshStateLocked()
		s.signalLocked()
	}
	s.logf("%v", err)
}

func (s *Supervisor) failRunning(err error) {
	s.mu.Lock()
	s.snapshot.State = StateFailed
	s.snapshot.LastError = err.Error()
	cancel := s.cancel
	s.signalLocked()
	s.mu.Unlock()
	s.logf("%v", err)
	if cancel != nil {
		cancel()
	}
}

func (s *Supervisor) refreshStateLocked() {
	if s.stopped || s.snapshot.State == StateFailed {
		return
	}
	s.snapshot.RelayURL = s.relayURL()
	if s.relayHealthy && s.snapshot.Ticket != "" {
		if s.snapshot.State != StateReady {
			s.readySince = time.Now()
		}
		s.snapshot.State = StateReady
		s.snapshot.LastError = ""
	} else {
		s.readySince = time.Time{}
		if s.snapshot.State != StateRestarting {
			s.snapshot.State = StateStarting
		}
	}
}

func (s *Supervisor) failLocked(err error) error {
	s.snapshot.State = StateFailed
	s.snapshot.LastError = err.Error()
	s.signalLocked()
	return err
}

func (s *Supervisor) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
	if s.cfg.OnChange != nil {
		go s.cfg.OnChange()
	}
}

func (s *Supervisor) backoff(attempt int) time.Duration {
	if s.cfg.Backoff != nil {
		if d := s.cfg.Backoff(attempt); d >= 0 {
			return d
		}
	}
	if attempt > 6 {
		attempt = 6
	}
	delay := time.Second << attempt
	if delay > 60*time.Second {
		delay = 60 * time.Second
	}
	return delay
}

func (s *Supervisor) logLine(name, line string) {
	s.logf("%s: %s", name, line)
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}

func (s *Supervisor) keyPath() string { return filepath.Join(s.cfg.StateDir, "node-key.json") }
func (s *Supervisor) relayURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.port)
}

func executable(path string) error {
	if path == "" {
		return errors.New("path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("%q is not executable", path)
	}
	return nil
}

func selectedEnvironment() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func relayEnvironment(cfg Config, address, stateDir string) []string {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return selectedEnvironment()
	}
	_ = os.Chmod(stateDir, 0700)
	env := selectedEnvironment()
	env = append(env,
		"ISOFORM_RELAY_ADDR=127.0.0.1:"+strings.TrimPrefix(address, "http://127.0.0.1:"),
		"ISOFORM_RELAY_STATE_DIR="+stateDir,
		"ISOFORM_RELAY_TRUSTED_PROXIES=none",
	)
	if cfg.PushGatewayURL != "" {
		env = append(env, "ISOFORM_RELAY_PUSH_GATEWAY_URL="+cfg.PushGatewayURL)
		env = append(env, "ISOFORM_RELAY_PUSH_GATEWAY_ENVIRONMENT="+cfg.PushGatewayEnvironment)
		env = append(env, "ISOFORM_RELAY_PUSH_GATEWAY_HUB_ID="+cfg.PushGatewayHubID)
		env = append(env, "ISOFORM_RELAY_PUSH_GATEWAY_HUB_PUB="+cfg.PushGatewayHubPub)
		env = append(env, "ISOFORM_RELAY_PUSH_GATEWAY_HUB_KEY="+cfg.PushGatewayHubKey)
	}
	return env
}

func loadOrChoosePort(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if p, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && p > 0 && p < 65536 && portFree(p) {
			return p, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("read relay port: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("choose relay port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if err := atomicWrite(path, []byte(strconv.Itoa(port)+"\n"), 0600); err != nil {
		return 0, fmt.Errorf("persist relay port: %w", err)
	}
	return port, nil
}

func portFree(port int) bool {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

func loadOrCreateKey(path string) (nodeKey, error) {
	info, err := os.Stat(path)
	if err == nil {
		if info.Mode().Perm() != 0600 {
			return nodeKey{}, fmt.Errorf("node key %s must have mode 0600", path)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nodeKey{}, fmt.Errorf("read node key: %w", readErr)
		}
		var key nodeKey
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if decodeErr := decoder.Decode(&key); decodeErr != nil {
			return nodeKey{}, fmt.Errorf("decode node key: %w", decodeErr)
		}
		if err := validateKey(key); err != nil {
			return nodeKey{}, fmt.Errorf("invalid node key: %w", err)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nodeKey{}, fmt.Errorf("inspect node key: %w", err)
	}
	key, err := createKey()
	if err != nil {
		return nodeKey{}, err
	}
	if err := writeKeyAtomic(path, key); err != nil {
		return nodeKey{}, err
	}
	return key, nil
}

func createKey() (nodeKey, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nodeKey{}, fmt.Errorf("generate node key: %w", err)
	}
	return nodeKey{Version: 1, Secret: hex.EncodeToString(secret), CreatedAt: time.Now().UTC().Format(time.RFC3339)}, nil
}

func validateKey(key nodeKey) error {
	if key.Version != 1 || len(key.Secret) != 64 {
		return errors.New("unsupported version or invalid secret")
	}
	if _, err := hex.DecodeString(key.Secret); err != nil {
		return errors.New("secret is not hexadecimal")
	}
	if _, err := time.Parse(time.RFC3339, key.CreatedAt); err != nil {
		return errors.New("createdAt is not RFC3339")
	}
	return nil
}

func writeKeyAtomic(path string, key nodeKey) error {
	if err := validateKey(key); err != nil {
		return fmt.Errorf("invalid node key: %w", err)
	}
	data, err := json.Marshal(key)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, data, 0600); err != nil {
		return fmt.Errorf("write node key: %w", err)
	}
	return nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".remote-access-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}
